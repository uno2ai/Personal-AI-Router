// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package tests

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"nvpair-shared/codexprotocol"
)

func TestLocalDelegationProducesBoundedHandoff(t *testing.T) {
	workspace := t.TempDir()
	worker, workerURL, _ := startCodexWorker(t, workspace, t.TempDir())
	defer stopCodexProcess(worker)
	supervisor, input, responses := startCodexSupervisor(t, workerURL)
	defer stopCodexSupervisor(supervisor, input)

	result := callSupervisor(t, input, responses, 1, "tools/call", map[string]any{
		"name": "tasks.delegate",
		"arguments": map[string]any{
			"objective": "return a short verification result",
			"workspace": "local",
			"mode":      "read",
			"approval":  "local-only",
		},
	})
	text := supervisorResultText(t, result)
	var accepted struct {
		Record struct {
			TaskID string `json:"taskId"`
		} `json:"record"`
	}
	if err := json.Unmarshal([]byte(text), &accepted); err != nil {
		t.Fatalf("decode delegation result %q: %v", text, err)
	}
	if accepted.Record.TaskID == "" {
		t.Fatalf("delegation returned no taskId: %s", text)
	}
	resultPayload := waitForWorkerResult(t, workerURL, accepted.Record.TaskID)
	var completed struct {
		Handoff codexprotocol.Handoff `json:"handoff"`
	}
	if err := json.Unmarshal(resultPayload, &completed); err != nil {
		t.Fatal(err)
	}
	if completed.Handoff.Status != codexprotocol.StateCompleted {
		t.Fatalf("handoff=%+v", completed.Handoff)
	}
	encoded, err := json.Marshal(completed.Handoff)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > codexprotocol.MaxHandoffBytes {
		t.Fatalf("handoff is %d bytes", len(encoded))
	}
}

func TestRestartReplaysIdempotencyWithoutSecondTurn(t *testing.T) {
	workspace := t.TempDir()
	stateRoot := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "app-server.log")
	request := crossProcessTaskRequest("request-restart", "task-restart", "attempt-1", 1)
	worker, workerURL, _ := startCodexWorker(t, workspace, stateRoot, "CODEX_FAKE_LOG="+logPath)
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	first := postWorkerJSON(t, workerURL+"/v1/tasks", payload)
	if first.StatusCode != http.StatusAccepted {
		t.Fatalf("first create status %d", first.StatusCode)
	}
	_ = waitForWorkerResult(t, workerURL, request.TaskID)
	stopCodexProcess(worker)

	worker, workerURL, _ = startCodexWorker(t, workspace, stateRoot, "CODEX_FAKE_LOG="+logPath)
	second := postWorkerJSON(t, workerURL+"/v1/tasks", payload)
	if second.StatusCode != http.StatusAccepted {
		t.Fatalf("replay status %d", second.StatusCode)
	}
	var replay struct {
		Idempotent bool `json:"idempotent"`
	}
	decodeHTTPJSON(t, second, &replay)
	if !replay.Idempotent {
		t.Fatal("replayed request was not marked idempotent")
	}
	stopCodexProcess(worker)
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(logData), "turn/start\n"); got != 1 {
		t.Fatalf("fixture saw %d turn/start calls, want 1", got)
	}
}

func TestStaleAttemptCannotRetrySameWorkspace(t *testing.T) {
	workspace := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "app-server.log")
	worker, workerURL, _ := startCodexWorker(t, workspace, t.TempDir(), "CODEX_FAKE_LOG="+logPath, "CODEX_FAKE_MODE=exit")
	t.Cleanup(func() { stopCodexProcess(worker) })
	request := crossProcessTaskRequest("request-lost", "task-lost", "attempt-1", 1)
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	created := postWorkerJSON(t, workerURL+"/v1/tasks", payload)
	if created.StatusCode != http.StatusAccepted {
		t.Fatalf("create status %d", created.StatusCode)
	}
	waitForWorkerState(t, workerURL, request.TaskID, codexprotocol.StateLost)
	retry := crossProcessTaskRequest("request-lost-retry", "task-lost", "attempt-2", 2)
	retryPayload, err := json.Marshal(retry)
	if err != nil {
		t.Fatal(err)
	}
	retried := postWorkerJSON(t, workerURL+"/v1/tasks", retryPayload)
	if retried.StatusCode != http.StatusConflict {
		t.Fatalf("unfenced retry status %d", retried.StatusCode)
	}
	other := crossProcessTaskRequest("request-lost-other", "task-lost-other", "attempt-1", 1)
	otherPayload, err := json.Marshal(other)
	if err != nil {
		t.Fatal(err)
	}
	otherResponse := postWorkerJSON(t, workerURL+"/v1/tasks", otherPayload)
	if otherResponse.StatusCode != http.StatusConflict {
		t.Fatalf("different task reused an unfenced workspace: status %d", otherResponse.StatusCode)
	}
	stopCodexProcess(worker)
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(logData), "turn/start\n"); got != 1 {
		t.Fatalf("fixture saw %d turn/start calls, want 1", got)
	}
}

func TestApprovalIsBlockedAndCannotBeRelayed(t *testing.T) {
	workspace := t.TempDir()
	worker, workerURL, _ := startCodexWorker(t, workspace, t.TempDir(), "CODEX_FAKE_MODE=approval")
	defer stopCodexProcess(worker)
	request := crossProcessTaskRequest("request-approval", "task-approval", "attempt-1", 1)
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	created := postWorkerJSON(t, workerURL+"/v1/tasks", payload)
	if created.StatusCode != http.StatusAccepted {
		t.Fatalf("create status %d", created.StatusCode)
	}
	resultPayload := waitForWorkerResult(t, workerURL, request.TaskID)
	var blocked struct {
		Handoff codexprotocol.Handoff `json:"handoff"`
	}
	if err := json.Unmarshal(resultPayload, &blocked); err != nil {
		t.Fatal(err)
	}
	if blocked.Handoff.Status != codexprotocol.StateBlocked || blocked.Handoff.Summary != "approval_required" {
		t.Fatalf("handoff=%+v", blocked.Handoff)
	}
	approval := postWorkerJSON(t, workerURL+"/v1/tasks/task-approval/approvals/a1", []byte(`{"approved":true}`))
	if approval.StatusCode != http.StatusNotFound {
		t.Fatalf("approval relay route returned %d", approval.StatusCode)
	}
}

func TestCancellationIsScopedToOneTask(t *testing.T) {
	workspace := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "cancel-app-server.log")
	if err := os.Mkdir(filepath.Join(workspace, "one"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(workspace, "two"), 0o700); err != nil {
		t.Fatal(err)
	}
	worker, workerURL, _ := startCodexWorker(t, workspace, t.TempDir(), "CODEX_FAKE_MODE=hold", "CODEX_FAKE_LOG="+logPath)
	defer stopCodexProcess(worker)
	first := crossProcessTaskRequest("request-cancel-1", "task-cancel-1", "attempt-1", 1)
	first.Workspace.Path = "one"
	second := crossProcessTaskRequest("request-cancel-2", "task-cancel-2", "attempt-1", 1)
	second.Workspace.Path = "two"
	firstPayload, _ := json.Marshal(first)
	secondPayload, _ := json.Marshal(second)
	if response := postWorkerJSON(t, workerURL+"/v1/tasks", firstPayload); response.StatusCode != http.StatusAccepted {
		t.Fatalf("first create status %d", response.StatusCode)
	}
	if response := postWorkerJSON(t, workerURL+"/v1/tasks", secondPayload); response.StatusCode != http.StatusAccepted {
		t.Fatalf("second create status %d", response.StatusCode)
	}
	waitForWorkerState(t, workerURL, first.TaskID, codexprotocol.StateRunning)
	waitForWorkerState(t, workerURL, second.TaskID, codexprotocol.StateRunning)
	waitForLogEntry(t, logPath, "turn/start")
	cancelPayload, _ := json.Marshal(first.Mutation)
	cancel := postWorkerJSON(t, workerURL+"/v1/tasks/"+first.TaskID+"/cancel", cancelPayload)
	if cancel.StatusCode != http.StatusAccepted {
		t.Fatalf("cancel status %d", cancel.StatusCode)
	}
	waitForWorkerState(t, workerURL, first.TaskID, codexprotocol.StateCancelled)
	state := getWorkerStatus(t, workerURL, second.TaskID)
	if state.State != codexprotocol.StateRunning && state.State != codexprotocol.StateStarting {
		t.Fatalf("second task changed after first cancellation: %s", state.State)
	}
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(logData, []byte("turn/interrupt")) {
		t.Fatalf("cancellation did not issue native turn/interrupt: %s", logData)
	}
}

func waitForLogEntry(t *testing.T, path, entry string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil && bytes.Contains(data, []byte(entry)) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("fixture log %s did not contain %q", path, entry)
}

func TestUnsafeWorkspacePathsAreRejectedBeforeChildStart(t *testing.T) {
	workspace := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "app-server.log")
	worker, workerURL, _ := startCodexWorker(t, workspace, t.TempDir(), "CODEX_FAKE_LOG="+logPath)
	defer stopCodexProcess(worker)
	paths := []string{"../outside", filepath.Join(workspace, "outside")}
	for index, path := range paths {
		request := crossProcessTaskRequest("request-unsafe-"+strconv.Itoa(index), "task-unsafe-"+strconv.Itoa(index), "attempt-1", 1)
		request.Workspace.Path = path
		payload, _ := json.Marshal(request)
		response := postWorkerJSON(t, workerURL+"/v1/tasks", payload)
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("path %q returned %d", path, response.StatusCode)
		}
	}
	if data, err := os.ReadFile(logPath); err == nil && bytes.Contains(data, []byte("turn/start")) {
		t.Fatal("unsafe workspace request started an app-server turn")
	}
}

func crossProcessTaskRequest(requestID, taskID, attemptID string, epoch uint64) codexprotocol.TaskRequest {
	return codexprotocol.TaskRequest{
		Mutation:  codexprotocol.Mutation{ProtocolVersion: codexprotocol.ProtocolVersion, RequestID: requestID, TaskID: taskID, AttemptID: attemptID, LeaseEpoch: epoch},
		Context:   codexprotocol.ContextPackage{Version: codexprotocol.ContextVersion, Objective: "return a fixture result", Limits: codexprotocol.Limits{WallSeconds: 60}},
		Workspace: codexprotocol.WorkspaceSpec{ID: "local", Path: "local", Mode: "read"},
		Execution: codexprotocol.ExecutionSpec{Sandbox: "read-only", Approval: "local-only"},
	}
}

func startCodexWorker(t *testing.T, workspace, state string, environment ...string) (*exec.Cmd, string, string) {
	t.Helper()
	port := freeCodexPort(t)
	logPath := filepath.Join(t.TempDir(), "worker.log")
	args := []string{"--workspace-root", workspace, "--state-root", state, "--codex-bin", fakeCodexBin, "--max-concurrency", "2", "--listen", "127.0.0.1:" + strconv.Itoa(port)}
	cmd := exec.Command(workerBin, args...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), environment...)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waitForWorker(t, "http://127.0.0.1:"+strconv.Itoa(port))
	return cmd, "http://127.0.0.1:" + strconv.Itoa(port), logPath
}

func startCodexSupervisor(t *testing.T, workerURL string) (*exec.Cmd, io.WriteCloser, *bufio.Reader) {
	t.Helper()
	cmd := exec.Command(supervisorBin, "--worker-url", workerURL)
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return cmd, input, bufio.NewReader(output)
}

func stopCodexSupervisor(cmd *exec.Cmd, input io.WriteCloser) {
	_ = input.Close()
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	_ = cmd.Wait()
}

func stopCodexProcess(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(os.Interrupt)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		_ = cmd.Process.Kill()
		<-done
	}
}

func callSupervisor(t *testing.T, input io.Writer, responses *bufio.Reader, id int, method string, params any) json.RawMessage {
	t.Helper()
	request := map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}
	if err := json.NewEncoder(input).Encode(request); err != nil {
		t.Fatal(err)
	}
	line := readLineWithTimeout(t, responses, 5*time.Second)
	var response struct {
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(line, &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Error) > 0 && string(response.Error) != "null" {
		t.Fatalf("Supervisor error: %s", response.Error)
	}
	return response.Result
}

func supervisorResultText(t *testing.T, result json.RawMessage) string {
	t.Helper()
	var response struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(result, &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Content) == 0 {
		t.Fatal("Supervisor result has no content")
	}
	return response.Content[0].Text
}

func readLineWithTimeout(t *testing.T, reader *bufio.Reader, timeout time.Duration) []byte {
	t.Helper()
	result := make(chan []byte, 1)
	errorsCh := make(chan error, 1)
	go func() {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			errorsCh <- err
			return
		}
		result <- line
	}()
	select {
	case line := <-result:
		return line
	case err := <-errorsCh:
		t.Fatal(err)
	case <-time.After(timeout):
		t.Fatal("timed out reading subprocess response")
	}
	return nil
}

func postWorkerJSON(t *testing.T, url string, payload []byte) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { response.Body.Close() })
	return response
}

func decodeHTTPJSON(t *testing.T, response *http.Response, target any) {
	t.Helper()
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		t.Fatal(err)
	}
}

func waitForWorker(t *testing.T, baseURL string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		response, err := http.Get(baseURL + "/v1/worker")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("Worker did not become ready at %s", baseURL)
}

func waitForWorkerResult(t *testing.T, baseURL, taskID string) []byte {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		response, err := http.Get(baseURL + "/v1/tasks/" + taskID + "/result")
		if err == nil {
			data, readErr := io.ReadAll(response.Body)
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return data
			}
			if readErr != nil {
				t.Fatal(readErr)
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("Worker did not produce result for %s", taskID)
	return nil
}

type workerStatus struct {
	State codexprotocol.TaskState `json:"state"`
}

func getWorkerStatus(t *testing.T, baseURL, taskID string) workerStatus {
	t.Helper()
	response, err := http.Get(baseURL + "/v1/tasks/" + taskID)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var status workerStatus
	decodeHTTPJSON(t, response, &status)
	return status
}

func waitForWorkerState(t *testing.T, baseURL, taskID string, want codexprotocol.TaskState) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status := getWorkerStatus(t, baseURL, taskID)
		if status.State == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("Worker task %s did not reach %s", taskID, want)
}

func freeCodexPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}
