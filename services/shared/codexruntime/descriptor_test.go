// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package codexruntime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func validDescriptor(now time.Time) RuntimeDescriptor {
	return RuntimeDescriptor{
		SchemaVersion:           RuntimeSchemaVersion,
		InstallationID:          "installation-1",
		WorkerInstanceID:        "worker-1",
		BootEpoch:               1,
		Generation:              1,
		WrittenAt:               now.Add(-time.Second),
		ExpiresAt:               now.Add(time.Minute),
		Endpoint:                "https://127.0.0.1:43124",
		Transport:               TransportPinnedLocalTLS,
		ServerCertificateSHA256: "certificate-digest",
		CredentialRef:           "runtime/worker-credential",
		CredentialGeneration:    1,
		PolicyRevision:          1,
		State:                   RuntimeStateReady,
	}
}

func TestRuntimeDescriptorValidateRejectsMissingIdentityAndExpiredState(t *testing.T) {
	now := time.Now().UTC()
	tests := []struct {
		name string
		edit func(*RuntimeDescriptor)
	}{
		{name: "schema", edit: func(d *RuntimeDescriptor) { d.SchemaVersion = 0 }},
		{name: "installation", edit: func(d *RuntimeDescriptor) { d.InstallationID = "" }},
		{name: "instance", edit: func(d *RuntimeDescriptor) { d.WorkerInstanceID = "" }},
		{name: "boot epoch", edit: func(d *RuntimeDescriptor) { d.BootEpoch = 0 }},
		{name: "expiry", edit: func(d *RuntimeDescriptor) { d.ExpiresAt = now.Add(-time.Second) }},
		{name: "endpoint", edit: func(d *RuntimeDescriptor) { d.Endpoint = "" }},
		{name: "certificate", edit: func(d *RuntimeDescriptor) { d.ServerCertificateSHA256 = "" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			descriptor := validDescriptor(now)
			tt.edit(&descriptor)
			if err := descriptor.Validate(now); err == nil {
				t.Fatalf("Validate() accepted invalid %s descriptor", tt.name)
			}
		})
	}
}

func TestWriteDescriptorRejectsRollbackAndKeepsSecretsOutOfJSON(t *testing.T) {
	now := time.Now().UTC()
	path := filepath.Join(t.TempDir(), "runtime.json")
	if err := WriteDescriptor(path, validDescriptor(now)); err != nil {
		t.Fatal(err)
	}

	rolledBack := validDescriptor(now)
	rolledBack.BootEpoch = 1
	rolledBack.Generation = 1
	if err := WriteDescriptor(path, rolledBack); err == nil {
		t.Fatal("WriteDescriptor accepted a non-increasing descriptor version")
	}

	refreshed := validDescriptor(now)
	refreshed.Generation = 2
	if err := WriteDescriptor(path, refreshed); err != nil {
		t.Fatalf("WriteDescriptor rejected a same-boot refresh: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "token") || strings.Contains(string(data), "privateKey") {
		t.Fatalf("descriptor contains secret-looking material: %s", data)
	}
}

func TestReadDescriptorRejectsUnknownSecretFieldsAndExpiry(t *testing.T) {
	now := time.Now().UTC()
	path := filepath.Join(t.TempDir(), "runtime.json")
	secretJSON := `{"schemaVersion":1,"installationId":"i","workerInstanceId":"w","bootEpoch":1,"generation":1,"writtenAt":"2026-09-05T00:00:00Z","expiresAt":"2026-09-05T00:01:00Z","endpoint":"https://127.0.0.1:1","transport":"pinned-local-tls","credentialRef":"x","credentialGeneration":1,"policyRevision":1,"state":"ready","token":"must-not-be-accepted"}`
	if err := os.WriteFile(path, []byte(secretJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadDescriptor(path, now); err == nil {
		t.Fatal("ReadDescriptor accepted an unknown secret field")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	if err := WriteDescriptor(path, validDescriptor(now)); err != nil {
		t.Fatal(err)
	}
	descriptor, err := ReadDescriptor(path, now)
	if err != nil {
		t.Fatal(err)
	}
	if descriptor.State != RuntimeStateReady {
		t.Fatalf("state = %q, want ready", descriptor.State)
	}
}

func TestReadDescriptorRejectsTrailingJSON(t *testing.T) {
	now := time.Now().UTC()
	path := filepath.Join(t.TempDir(), "runtime.json")
	first, err := json.Marshal(validDescriptor(now))
	if err != nil {
		t.Fatal(err)
	}
	data := append(append(first, ' '), []byte(`{"extra":true}`)...)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadDescriptor(path, now); err == nil {
		t.Fatal("ReadDescriptor accepted trailing JSON")
	}
}

func TestUnavailableDescriptorMayCarryActionableErrorWithoutAnEndpoint(t *testing.T) {
	now := time.Now().UTC()
	descriptor := validDescriptor(now)
	descriptor.State = RuntimeStateUnavailable
	descriptor.Endpoint = ""
	descriptor.Transport = ""
	descriptor.ServerCertificateSHA256 = ""
	descriptor.Error = "unsupported app-server initialize response"
	if err := descriptor.Validate(now); err != nil {
		t.Fatalf("Validate() rejected failure descriptor: %v", err)
	}
}
