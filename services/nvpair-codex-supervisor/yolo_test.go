// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"nvpair-shared/codexprotocol"
)

type policyWorkerClient struct {
	fakeWorkerClient
	caps     codexprotocol.WorkerCapabilities
	requests []codexprotocol.TaskRequest
}

func (c *policyWorkerClient) Worker(context.Context) (json.RawMessage, error) {
	return json.Marshal(map[string]any{"protocolVersion": 1, "version": "test", "capabilities": c.caps})
}

func (c *policyWorkerClient) Create(ctx context.Context, r codexprotocol.TaskRequest) (json.RawMessage, error) {
	c.requests = append(c.requests, r)
	return c.fakeWorkerClient.Create(ctx, r)
}

func yoloCapabilities() codexprotocol.WorkerCapabilities {
	return codexprotocol.WorkerCapabilities{ProtocolVersion: 1, WorkerVersion: "test", AppServerVersion: "test", OS: "darwin", Architecture: "arm64", WorkspaceAliases: []string{"local"}, WorkspaceModes: []string{"read", "write"}, SandboxModes: []string{"read-only", "workspace-write", "danger-full-access"}, ApprovalModes: []string{"local-only", "never"}, MaxConcurrency: 1, AvailableSlots: 1}
}

func TestDelegateYOLOFiltersCapabilitiesAndMapsPolicy(t *testing.T) {
	for _, osName := range []string{"darwin", "windows"} {
		legacy := &policyWorkerClient{}
		optIn := &policyWorkerClient{caps: yoloCapabilities()}
		optIn.caps.OS = osName
		server := NewMCPServerWithWorkers([]WorkerTarget{{ID: "a-old", Client: legacy}, {ID: "z-opt-in", Client: optIn}})
		_, code, message := server.delegate(context.Background(), json.RawMessage(`{"objective":"test","mode":"yolo"}`))
		if code != 0 {
			t.Fatalf("YOLO rejected: %d %s", code, message)
		}
		if len(legacy.requests) != 0 || len(optIn.requests) != 1 {
			t.Fatalf("wrong selection: old=%d opt-in=%d", len(legacy.requests), len(optIn.requests))
		}
		r := optIn.requests[0]
		if r.Workspace.Mode != "write" || r.Execution.Sandbox != "danger-full-access" || r.Execution.Approval != "never" {
			t.Fatalf("YOLO mapping: %+v", r)
		}
		if err := r.Validate(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDelegateYOLONeverFallsBackToOldOrPartialCapabilities(t *testing.T) {
	for _, missing := range []string{"all", "sandbox", "approval", "write"} {
		caps := yoloCapabilities()
		switch missing {
		case "all":
			caps = codexprotocol.WorkerCapabilities{}
		case "sandbox":
			caps.SandboxModes = []string{"workspace-write"}
		case "approval":
			caps.ApprovalModes = []string{"local-only"}
		case "write":
			caps.WorkspaceModes = nil
		}
		client := &policyWorkerClient{caps: caps}
		server := NewMCPServer(client)
		result, code, message := server.delegate(context.Background(), json.RawMessage(`{"objective":"test","mode":"yolo","workerId":"local"}`))
		if code != 0 {
			t.Fatalf("mode should parse before selection: %d %s", code, message)
		}
		if len(client.requests) != 0 {
			t.Fatal("YOLO dispatched without complete capabilities")
		}
		if !result.(toolResult).IsError {
			t.Fatal("missing capabilities reported success")
		}
	}
}

func TestDelegateRejectsMismatchedApproval(t *testing.T) {
	for _, raw := range []string{`{"objective":"test","mode":"read","approval":"never"}`, `{"objective":"test","mode":"write","approval":"never"}`, `{"objective":"test","mode":"yolo","approval":"local-only"}`} {
		client := &policyWorkerClient{caps: yoloCapabilities()}
		_, code, _ := NewMCPServer(client).delegate(context.Background(), json.RawMessage(raw))
		if code != -32602 || len(client.requests) != 0 {
			t.Fatalf("accepted conflicting policy %s", raw)
		}
	}
}

func TestDelegateSchemaAdvertisesYOLO(t *testing.T) {
	result := callMCP(t, NewMCPServer(fakeWorkerClient{}), `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	var decoded struct {
		Tools []struct {
			Name        string
			InputSchema struct {
				Properties map[string]struct{ Enum []string }
			}
		}
	}
	if err := json.Unmarshal(result, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, tool := range decoded.Tools {
		if tool.Name != "tasks.delegate" {
			continue
		}
		if !slices.Contains(tool.InputSchema.Properties["mode"].Enum, "yolo") || !slices.Contains(tool.InputSchema.Properties["approval"].Enum, "never") {
			t.Fatal("YOLO policy missing from MCP schema")
		}
		return
	}
	t.Fatal("delegate schema missing")
}

func TestSupervisorDefaultTaskModeCLI(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "supervisor")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	if output, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, output)
	}
	for _, tc := range []struct{ defaultMode, mode, sandbox, approval string }{
		{"", "", "read-only", "local-only"},
		{"read", "", "read-only", "local-only"},
		{"write", "", "workspace-write", "local-only"},
		{"yolo", "", "danger-full-access", "never"},
		{"yolo", "read", "read-only", "local-only"},
		{"yolo", "write", "workspace-write", "local-only"},
	} {
		t.Run(tc.defaultMode+"/"+tc.mode, func(t *testing.T) {
			requests := make(chan codexprotocol.TaskRequest, 1)
			worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/worker" {
					_ = json.NewEncoder(w).Encode(map[string]any{"capabilities": yoloCapabilities()})
					return
				}
				var task codexprotocol.TaskRequest
				if err := json.NewDecoder(r.Body).Decode(&task); err != nil {
					http.Error(w, err.Error(), 400)
					return
				}
				requests <- task
				data, _ := (fakeWorkerClient{}).Create(r.Context(), task)
				w.WriteHeader(202)
				_, _ = w.Write(data)
			}))
			defer worker.Close()
			args := []string{"--worker-url", worker.URL, "--worker-token", "test", "--state-root", t.TempDir()}
			if tc.defaultMode != "" {
				args = append(args, "--default-task-mode", tc.defaultMode)
			}
			command := exec.Command(binary, args...)
			arguments := map[string]string{"objective": "test"}
			if tc.mode != "" {
				arguments["mode"] = tc.mode
			}
			payload, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "tasks.delegate", "arguments": arguments}})
			if err != nil {
				t.Fatal(err)
			}
			command.Stdin = strings.NewReader(string(payload) + "\n")
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("CLI: %v %s", err, output)
			}
			select {
			case task := <-requests:
				if task.Execution.Sandbox != tc.sandbox || task.Execution.Approval != tc.approval {
					t.Fatalf("wrong default/override: %+v", task.Execution)
				}
			default:
				t.Fatalf("no dispatch: %s", output)
			}
		})
	}
	command := exec.Command(binary, "--default-task-mode", "typo")
	if output, err := command.CombinedOutput(); err == nil || !strings.Contains(string(output), "default-task-mode") {
		t.Fatalf("invalid mode accepted: %v %s", err, output)
	}
}
