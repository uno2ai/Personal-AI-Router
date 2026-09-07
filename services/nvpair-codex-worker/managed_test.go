// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"nvpair-shared/codexruntime"
	"nvpair-shared/protectedfile"
)

func validManagedConfig(root string) managedWorkerConfig {
	return managedWorkerConfig{
		SchemaVersion:         managedWorkerConfigSchemaVersion,
		InstallationID:        "installation-1",
		WorkerInstanceID:      "worker-1",
		BootEpoch:             1,
		Generation:            1,
		WorkspaceRoot:         filepath.Join(root, "workspace"),
		StateRoot:             filepath.Join(root, "state"),
		Account:               "test-user",
		CodexBin:              "codex",
		MaxConcurrency:        1,
		AuthToken:             "local-secret",
		RuntimeDescriptorPath: filepath.Join(root, "runtime.json"),
		CredentialRef:         filepath.Join(root, "credential.json"),
		CredentialGeneration:  1,
		PolicyRevision:        1,
		ArtifactMaxBytes:      8 << 20,
		PolicyCeiling:         "read-only",
		WorkspaceAlias:        "local",
	}
}

func TestManagedWorkerConfigValidateRejectsUnsafeOrIncompleteValues(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		name string
		edit func(*managedWorkerConfig)
	}{
		{name: "schema", edit: func(c *managedWorkerConfig) { c.SchemaVersion = 0 }},
		{name: "installation", edit: func(c *managedWorkerConfig) { c.InstallationID = "" }},
		{name: "boot epoch", edit: func(c *managedWorkerConfig) { c.BootEpoch = 0 }},
		{name: "workspace root", edit: func(c *managedWorkerConfig) { c.WorkspaceRoot = "relative" }},
		{name: "state root", edit: func(c *managedWorkerConfig) { c.StateRoot = "relative" }},
		{name: "token", edit: func(c *managedWorkerConfig) { c.AuthToken = "" }},
		{name: "capacity", edit: func(c *managedWorkerConfig) { c.MaxConcurrency = 0 }},
		{name: "descriptor path", edit: func(c *managedWorkerConfig) { c.RuntimeDescriptorPath = "runtime.json" }},
		{name: "policy ceiling", edit: func(c *managedWorkerConfig) { c.PolicyCeiling = "unrestricted" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := validManagedConfig(root)
			tt.edit(&config)
			if err := config.Validate(); err == nil {
				t.Fatalf("Validate() accepted invalid %s config", tt.name)
			}
		})
	}
}

func TestManagedWorkerDoesNotReportReadyForMissingCodexExecutable(t *testing.T) {
	root := t.TempDir()
	config := validManagedConfig(root)
	config.CodexBin = filepath.Join(root, "missing-codex")
	if _, err := startManagedWorker(config); err == nil {
		t.Fatal("managed Worker reported ready without a usable Codex executable")
	}
	descriptor, err := codexruntime.ReadDescriptor(config.RuntimeDescriptorPath, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if descriptor.State != codexruntime.RuntimeStateUnavailable || descriptor.Error == "" {
		t.Fatalf("failure descriptor = %+v", descriptor)
	}
}

func TestReadManagedConfigUsesStrictJSON(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.json")
	if err := os.WriteFile(path, []byte(`{"schemaVersion":1,"installationId":"i","workerInstanceId":"w","bootEpoch":1,"generation":1,"workspaceRoot":"/tmp/workspace","stateRoot":"/tmp/state","codexBin":"codex","maxConcurrency":1,"authToken":"secret","runtimeDescriptorPath":"/tmp/runtime.json","credentialRef":"/tmp/credential.json","credentialGeneration":1,"policyRevision":1,"artifactMaxBytes":8388608,"workspaceAlias":"local","unexpected":"reject"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readManagedConfig(path); err == nil {
		t.Fatal("readManagedConfig accepted an unknown field")
	}
}

func TestManagedControlStartsReportsAndStopsWorker(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CODEX_FAKE_APP_SERVER", "1")
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	config := validManagedConfig(root)
	config.WorkspaceRoot = workspace
	config.CodexBin = mustExecutable(t)
	configPath := filepath.Join(root, "config.json")
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := protectedfile.WriteFile(configPath, data); err != nil {
		t.Fatal(err)
	}

	inputReader, inputWriter := io.Pipe()
	output := make(chan []byte, 8)
	done := make(chan error, 1)
	go func() { done <- runManagedControl(inputReader, controlEventWriter(output)) }()

	start := codexruntime.ControlMessage{
		SchemaVersion:  codexruntime.ControlSchemaVersion,
		Kind:           codexruntime.ControlKindStart,
		RequestID:      "start-1",
		ConfigRevision: 1,
		ConfigPath:     configPath,
		WorkspaceAlias: "local",
	}
	startData, err := codexruntime.MarshalControlMessage(start)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inputWriter.Write(startData); err != nil {
		t.Fatal(err)
	}
	ready := readManagedEvent(t, output)
	if ready.Kind != codexruntime.ControlEventReady || ready.Descriptor == nil {
		t.Fatalf("start event = %+v", ready)
	}
	if ready.Descriptor.State != codexruntime.RuntimeStateReady || ready.Descriptor.Endpoint == "" {
		t.Fatalf("ready descriptor = %+v", ready.Descriptor)
	}
	if _, err := os.Stat(config.RuntimeDescriptorPath); err != nil {
		t.Fatalf("runtime descriptor was not published: %v", err)
	}

	statusData, err := codexruntime.MarshalControlMessage(codexruntime.ControlMessage{
		SchemaVersion: codexruntime.ControlSchemaVersion,
		Kind:          codexruntime.ControlKindStatus,
		RequestID:     "status-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inputWriter.Write(statusData); err != nil {
		t.Fatal(err)
	}
	status := readManagedEvent(t, output)
	if status.Kind != codexruntime.ControlEventStatus || status.State != codexruntime.RuntimeStateReady {
		t.Fatalf("status event = %+v", status)
	}

	previousBootEpoch := ready.Descriptor.BootEpoch
	config.PolicyRevision = 2
	data, err = json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := protectedfile.WriteFile(configPath, data); err != nil {
		t.Fatal(err)
	}
	revisionData, err := codexruntime.MarshalControlMessage(codexruntime.ControlMessage{
		SchemaVersion:  codexruntime.ControlSchemaVersion,
		Kind:           codexruntime.ControlKindConfigRevision,
		RequestID:      "revision-1",
		ConfigRevision: 2,
		ConfigPath:     configPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inputWriter.Write(revisionData); err != nil {
		t.Fatal(err)
	}
	restarted := readManagedEvent(t, output)
	if restarted.Kind != codexruntime.ControlEventReady || restarted.Descriptor == nil || restarted.Descriptor.PolicyRevision != 2 {
		t.Fatalf("revision event = %+v", restarted)
	}
	if restarted.Descriptor.BootEpoch <= previousBootEpoch {
		t.Fatalf("boot epoch did not advance: previous=%d current=%d", previousBootEpoch, restarted.Descriptor.BootEpoch)
	}

	shutdownData, err := codexruntime.MarshalControlMessage(codexruntime.ControlMessage{
		SchemaVersion: codexruntime.ControlSchemaVersion,
		Kind:          codexruntime.ControlKindShutdown,
		RequestID:     "shutdown-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inputWriter.Write(shutdownData); err != nil {
		t.Fatal(err)
	}
	stopped := readManagedEvent(t, output)
	if stopped.Kind != codexruntime.ControlEventStopped || stopped.State != codexruntime.RuntimeStateStopped {
		t.Fatalf("stopped event = %+v", stopped)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("managed control loop did not stop")
	}
}

func TestManagedWorkerHeartbeatRefreshesRuntimeDescriptor(t *testing.T) {
	oldTTL := managedDescriptorTTL
	oldInterval := managedDescriptorRefreshInterval
	managedDescriptorTTL = 40 * time.Millisecond
	managedDescriptorRefreshInterval = 10 * time.Millisecond
	defer func() {
		managedDescriptorTTL = oldTTL
		managedDescriptorRefreshInterval = oldInterval
	}()

	root := t.TempDir()
	t.Setenv("CODEX_FAKE_APP_SERVER", "1")
	config := validManagedConfig(root)
	config.CodexBin = mustExecutable(t)
	controller, err := startManagedWorker(config)
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Stop()
	first, err := codexruntime.ReadDescriptor(config.RuntimeDescriptorPath, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(250 * time.Millisecond)
	for time.Now().Before(deadline) {
		current, readErr := codexruntime.ReadDescriptor(config.RuntimeDescriptorPath, time.Now().UTC())
		if readErr == nil && current.Generation > first.Generation {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("managed Worker heartbeat did not refresh the runtime descriptor")
}

type controlEventWriter chan<- []byte

func (w controlEventWriter) Write(data []byte) (int, error) {
	copyOfData := append([]byte(nil), data...)
	w <- copyOfData
	return len(data), nil
}

func readManagedEvent(t *testing.T, events <-chan []byte) codexruntime.ControlEvent {
	t.Helper()
	var line []byte
	select {
	case line = <-events:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for managed event")
	}
	var event codexruntime.ControlEvent
	if err := json.Unmarshal(line, &event); err != nil {
		t.Fatalf("decode managed event: %v (%s)", err, line)
	}
	return event
}
