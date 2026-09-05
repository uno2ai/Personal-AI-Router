// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"context"
	"encoding/json"
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
	socketPath := filepath.Join(os.TempDir(), "nvpair-supervisor-test.sock")
	_ = os.Remove(socketPath)
	t.Cleanup(func() { _ = os.Remove(socketPath) })
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
