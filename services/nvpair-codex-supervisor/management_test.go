// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestManagementSocketListsMetadataWithoutTaskBody(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("named-pipe management transport is a separate Windows implementation")
	}
	index, err := OpenTaskIndex(filepath.Join(t.TempDir(), "dispatch.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	intent := validDispatchIntent()
	intent.Request.Context.Objective = "must not cross the management boundary"
	if err := index.Begin(intent); err != nil {
		t.Fatal(err)
	}
	if err := index.MarkAcknowledged(intent.TaskID, intent.WorkerID); err != nil {
		t.Fatal(err)
	}
	server := NewMCPServerWithWorkers([]WorkerTarget{{ID: "local", Client: fakeWorkerClient{}}})
	server.SetTaskIndex(index)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	socketPath := filepath.Join(shortManagementTestDir(t), "test.sock")
	if err := server.StartManagementSocket(ctx, socketPath); err != nil {
		t.Fatal(err)
	}

	var conn net.Conn
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		conn, err = net.Dial("unix", socketPath)
		if err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte(`{"method":"tasks.list"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Tasks []map[string]any `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(line), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Tasks) != 1 || response.Tasks[0]["taskId"] != intent.TaskID {
		t.Fatalf("metadata response=%s", line)
	}
	if strings.Contains(line, "must not cross") || strings.Contains(line, "objective") {
		t.Fatalf("task body crossed management boundary: %s", line)
	}
}

func shortManagementTestDir(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp("", "nvm-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	return directory
}

func TestManagementSocketFallbackUsesPrivateDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix socket path fallback")
	}
	root := shortManagementTestDir(t)
	t.Setenv("TMPDIR", root)
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	path := ManagementSocketPath(filepath.Join(root, strings.Repeat("long", 40)), os.Getpid())
	if filepath.Dir(path) == root || filepath.Dir(filepath.Dir(path)) != root || len(path) >= 104 {
		t.Fatalf("fallback is not a short private child: %s", path)
	}
	server := NewMCPServerWithWorkers([]WorkerTarget{{ID: "local", Client: fakeWorkerClient{}}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := server.StartManagementSocket(ctx, path); err != nil {
		t.Fatal(err)
	}
	for target, want := range map[string]os.FileMode{root: 0o755, filepath.Dir(path): 0o700, path: 0o600} {
		info, err := os.Stat(target)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != want {
			t.Fatalf("%s mode=%o, want %o", target, info.Mode().Perm(), want)
		}
	}
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
}

func TestManagementSocketRejectsSharedTempRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix socket directories")
	}
	root := shortManagementTestDir(t)
	t.Setenv("TMPDIR", root)
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(shortManagementTestDir(t), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	server := NewMCPServerWithWorkers([]WorkerTarget{{ID: "local", Client: fakeWorkerClient{}}})
	for _, directory := range []string{root, alias} {
		if err := server.StartManagementSocket(context.Background(), filepath.Join(directory, "test.sock")); err == nil {
			t.Fatal("accepted shared temp root")
		}
	}
	info, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("shared root mode changed: %o", info.Mode().Perm())
	}
}

func TestManagementRegistryConfigurationIDRoundtrip(t *testing.T) {
	directory := t.TempDir()
	path := ManagementSocketPath(directory, os.Getpid())
	cleanup, err := WriteManagementRegistry(directory, path, os.Getpid(), "configuration-test")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	registryPath := filepath.Join(directory, fmt.Sprintf("supervisor-%d.json", os.Getpid()))
	data, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	var registry managementRegistry
	if err := json.Unmarshal(data, &registry); err != nil {
		t.Fatal(err)
	}
	if registry.ConfigurationID != "configuration-test" || registry.SocketPath != path || registry.PID != os.Getpid() || registry.SchemaVersion != 1 {
		t.Fatalf("registry roundtrip: %+v", registry)
	}
	cleanup()
	if _, err := os.Stat(registryPath); !os.IsNotExist(err) {
		t.Fatalf("registry cleanup: %v", err)
	}
}
