// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"nvpair-shared/codexprotocol"
)

func yoloTask() codexprotocol.TaskRequest {
	r := validTaskRequest("yolo-r", "yolo-t", "yolo-a", 1)
	r.Workspace.Mode = "write"
	r.Execution = codexprotocol.ExecutionSpec{Sandbox: "danger-full-access", Approval: "never"}
	return r
}

func TestManagedYOLOOptIn(t *testing.T) {
	c := validManagedConfig(t.TempDir())
	c.PolicyCeiling = "danger-full-access"
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestYOLOHTTPRequiresOptInAndAdvertisesOnlyWhenEnabled(t *testing.T) {
	for _, ceiling := range []string{"", "read-only", "workspace-write", "danger-full-access"} {
		t.Run(ceiling, func(t *testing.T) {
			t.Setenv("CODEX_FAKE_APP_SERVER", "1")
			root := t.TempDir()
			policy, err := NewWorkspacePolicy(root)
			if err != nil {
				t.Fatal(err)
			}
			store, err := NewTaskStore(mustJournal(filepath.Join(root, "tasks.jsonl")))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			worker := NewServer(store, NewAppServerFactory(mustExecutable(t)), policy).(*workerHTTPServer)
			worker.policyCeiling = ceiling
			defer worker.Close()
			capsResponse := httptest.NewRecorder()
			worker.ServeHTTP(capsResponse, httptest.NewRequest("GET", "http://127.0.0.1:1/v1/worker", nil))
			var response struct {
				Capabilities codexprotocol.WorkerCapabilities `json:"capabilities"`
			}
			if err := json.Unmarshal(capsResponse.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			enabled := ceiling == "danger-full-access"
			if slices.Contains(response.Capabilities.SandboxModes, "danger-full-access") != enabled || slices.Contains(response.Capabilities.ApprovalModes, "never") != enabled {
				t.Errorf("incorrect YOLO capabilities: %s", capsResponse.Body.String())
			}
			r := httptest.NewRequest("POST", "http://127.0.0.1:1/v1/tasks", bytes.NewReader(mustJSONBytes(t, yoloTask())))
			r.Header.Set("Content-Type", "application/json")
			result := httptest.NewRecorder()
			worker.ServeHTTP(result, r)
			want := 403
			if enabled {
				want = 202
			}
			if result.Code != want {
				t.Fatalf("status=%d want=%d: %s", result.Code, want, result.Body.String())
			}
			if !enabled {
				if _, ok := store.Get("yolo-t"); ok {
					t.Fatal("denied YOLO persisted")
				}
			}
		})
	}
}

func TestAppServerExecutionPoliciesAreNotDowngraded(t *testing.T) {
	for _, tc := range []struct{ sandbox, approval, wireApproval, wireType string }{
		{"read-only", "local-only", "on-request", "readOnly"},
		{"workspace-write", "local-only", "on-request", "workspaceWrite"},
		{"danger-full-access", "never", "never", "dangerFullAccess"},
	} {
		for _, resume := range []bool{false, true} {
			t.Run(tc.sandbox+fmt.Sprint(resume), func(t *testing.T) {
				t.Setenv("CODEX_FAKE_APP_SERVER", "1")
				capture := filepath.Join(t.TempDir(), "requests.jsonl")
				t.Setenv("CODEX_FAKE_CAPTURE", capture)
				r := yoloTask()
				r.Execution = codexprotocol.ExecutionSpec{Sandbox: tc.sandbox, Approval: tc.approval}
				r.Workspace.Path = t.TempDir()
				if tc.sandbox == "read-only" {
					r.Workspace.Mode = "read"
				}
				session := NewAppServerFactory(mustExecutable(t)).New()
				var err error
				if resume {
					_, err = session.RunFollowUpWithChild(context.Background(), r, "thread-1", func(codexprotocol.TaskEvent) {}, nil)
				} else {
					_, err = session.Run(context.Background(), r, func(codexprotocol.TaskEvent) {})
				}
				if err != nil {
					t.Fatal(err)
				}
				file, err := os.Open(capture)
				if err != nil {
					t.Fatal(err)
				}
				defer file.Close()
				scanner := bufio.NewScanner(file)
				seenThread, seenTurn := false, false
				for scanner.Scan() {
					var msg struct {
						Method string
						Params struct {
							Sandbox, ApprovalPolicy, Cwd string
							SandboxPolicy                map[string]any
						}
					}
					if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
						t.Fatal(err)
					}
					if msg.Method != "thread/start" && msg.Method != "thread/resume" && msg.Method != "turn/start" {
						continue
					}
					if msg.Params.ApprovalPolicy != tc.wireApproval || msg.Params.Cwd != r.Workspace.Path {
						t.Errorf("incorrect policy/cwd: %s", scanner.Text())
					}
					if msg.Method == "turn/start" {
						seenTurn = true
						if msg.Params.SandboxPolicy["type"] != tc.wireType {
							t.Errorf("sandbox downgraded: %s", scanner.Text())
						}
						if tc.sandbox == "danger-full-access" && len(msg.Params.SandboxPolicy) != 1 {
							t.Errorf("YOLO has restrictive policy fields: %s", scanner.Text())
						}
					} else {
						seenThread = true
						if (msg.Method == "thread/resume") != resume || msg.Params.Sandbox != tc.sandbox {
							t.Errorf("incorrect thread policy: %s", scanner.Text())
						}
					}
				}
				if err := scanner.Err(); err != nil {
					t.Fatal(err)
				}
				if !seenThread || !seenTurn {
					t.Fatal("missing captured thread/turn requests")
				}
			})
		}
	}
}

func TestAppServerRejectsUnknownOrMismatchedExecutionBeforeSpawn(t *testing.T) {
	for _, execution := range []codexprotocol.ExecutionSpec{{Sandbox: "typo", Approval: "local-only"}, {Sandbox: "danger-full-access", Approval: "local-only"}, {Sandbox: "read-only", Approval: "never"}} {
		r := yoloTask()
		r.Execution = execution
		_, err := NewAppServerFactory("missing-executable").New().Run(context.Background(), r, func(codexprotocol.TaskEvent) {})
		if err == nil || !strings.Contains(err.Error(), "execution") {
			t.Fatalf("expected execution rejection before spawn: %v", err)
		}
	}
}

func TestYOLOFollowUpRechecksOptIn(t *testing.T) {
	root := t.TempDir()
	store, err := NewTaskStore(mustJournal(filepath.Join(root, "tasks.jsonl")))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	r := yoloTask()
	if _, _, err := store.AcceptAt(r, root, 1); err != nil {
		t.Fatal(err)
	}
	mutation := r.Mutation
	mutation.RequestID = "complete"
	if err := store.Mutate(mutation, func(record *codexprotocol.TaskRecord) error {
		record.State = codexprotocol.StateCompleted
		record.ThreadID = "thread-1"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	policy, err := NewWorkspacePolicy(root)
	if err != nil {
		t.Fatal(err)
	}
	worker := NewServer(store, NewAppServerFactory("missing-executable"), policy).(*workerHTTPServer)
	worker.policyCeiling = "workspace-write"
	defer worker.Close()
	follow := followUpRequest{Mutation: r.Mutation, Context: r.Context}
	follow.RequestID = "resume"
	request := httptest.NewRequest("POST", "http://127.0.0.1:1/v1/tasks/yolo-t/turns", bytes.NewReader(mustJSONBytes(t, follow)))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	worker.ServeHTTP(response, request)
	if response.Code != 403 {
		t.Fatalf("YOLO resumed after opt-out: %d %s", response.Code, response.Body.String())
	}
	record, _ := store.Get(r.TaskID)
	if record.LeaseEpoch != 1 || record.State != codexprotocol.StateCompleted {
		t.Fatalf("denied resume mutated lease: %+v", record)
	}
}

func TestManagedYOLOKeepsMTLSWorkspaceFencingAndCancellation(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		t.Run(fmt.Sprint(revoke), func(t *testing.T) {
			config, client, unauthorized := managedRemoteFixture(t)
			config.PolicyCeiling = "danger-full-access"
			t.Setenv("CODEX_FAKE_HOLD", "1")
			controller, err := startManagedWorker(config)
			if err != nil {
				t.Fatal(err)
			}
			defer controller.Stop()
			endpoint := "https://" + controller.remoteListener.Addr().String()
			post := func(client *http.Client, path string, body any, want int) {
				t.Helper()
				r, err := http.NewRequest("POST", endpoint+path, bytes.NewReader(mustJSONBytes(t, body)))
				if err != nil {
					t.Fatal(err)
				}
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("Authorization", "Bearer "+config.AuthToken)
				response, err := client.Do(r)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				if response.StatusCode != want {
					t.Fatalf("%s status=%d want=%d", path, response.StatusCode, want)
				}
			}
			post(unauthorized, "/v1/tasks", yoloTask(), 403)
			escape := yoloTask()
			escape.Workspace.Path = "../outside"
			post(client, "/v1/tasks", escape, 400)
			post(client, "/v1/tasks", yoloTask(), 202)
			waitState := func(terminal bool) {
				t.Helper()
				deadline := time.Now().Add(5 * time.Second)
				for time.Now().Before(deadline) {
					record, _ := controller.store.Get("yolo-t")
					if !terminal && record.State == codexprotocol.StateRunning {
						return
					}
					if terminal && (record.State == codexprotocol.StateCancelled || record.State == codexprotocol.StateLost) {
						return
					}
					time.Sleep(10 * time.Millisecond)
				}
				record, _ := controller.store.Get("yolo-t")
				t.Fatalf("YOLO did not reach expected state terminal=%v: %+v", terminal, record)
			}
			waitState(false)
			mutation := yoloTask().Mutation
			mutation.RequestID = "cancel-yolo"
			mutation.LeaseEpoch++
			post(client, "/v1/tasks/yolo-t/cancel", mutation, 409)
			if revoke {
				if err := os.Remove(filepath.Join(config.ClusterDir, "trusted", "managed-client.json")); err != nil {
					t.Fatal(err)
				}
				mutation.LeaseEpoch--
				post(client, "/v1/tasks/yolo-t/cancel", mutation, 403)
			} else {
				mutation.LeaseEpoch--
				post(client, "/v1/tasks/yolo-t/cancel", mutation, 202)
			}
			waitState(true)
		})
	}
}

func TestStandaloneYOLORequiresExplicitFlag(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "worker")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	if output, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, output)
	}
	for _, ceiling := range []string{"", "workspace-write", "danger-full-access"} {
		t.Run(ceiling, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			args := []string{"--workspace-root", t.TempDir(), "--state-root", t.TempDir(), "--listen", "127.0.0.1:0", "--auth-token", "test", "--codex-bin", filepath.Join(t.TempDir(), "missing")}
			if ceiling != "" {
				args = append(args, "--policy-ceiling", ceiling)
			}
			command := exec.CommandContext(ctx, binary, args...)
			stderr, err := command.StderrPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { cancel(); _ = command.Wait() }()
			scanner := bufio.NewScanner(stderr)
			address := ""
			for scanner.Scan() {
				if _, value, found := strings.Cut(scanner.Text(), "Codex Worker listening on "); found {
					address = value
					break
				}
			}
			if address == "" {
				t.Fatal("CLI did not start with requested policy ceiling")
			}
			request, err := http.NewRequest("POST", "http://"+address+"/v1/tasks", bytes.NewReader(mustJSONBytes(t, yoloTask())))
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Bearer test")
			client := &http.Client{Timeout: time.Second}
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			want := 403
			if ceiling == "danger-full-access" {
				want = 202
			}
			if response.StatusCode != want {
				t.Fatalf("CLI policy=%q status=%d want=%d", ceiling, response.StatusCode, want)
			}
		})
	}
	if output, err := exec.Command(binary, "--policy-ceiling", "typo").CombinedOutput(); err == nil || !strings.Contains(string(output), "policy-ceiling") {
		t.Fatalf("invalid CLI policy: %v %s", err, output)
	}
}
