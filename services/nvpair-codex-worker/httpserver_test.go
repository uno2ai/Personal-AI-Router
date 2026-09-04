// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"nvpair-shared/codexprotocol"
)

func newTestHTTPServer(t *testing.T) *httptest.Server {
	t.Helper()
	root := t.TempDir()
	policy, err := NewWorkspacePolicy(root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewTaskStore(mustJournal(root + "/tasks.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	t.Setenv("CODEX_FAKE_APP_SERVER", "1")
	server := httptest.NewServer(NewServer(store, NewAppServerFactory(mustExecutable(t)), policy))
	t.Cleanup(server.Close)
	return server
}

func TestFollowUpUsesCurrentLeaseAndResumesThePersistedThread(t *testing.T) {
	server := newTestHTTPServer(t)
	request := validTaskRequest("follow-create", "follow-task", "follow-attempt", 1)
	response := postJSON(t, server.URL+"/v1/tasks", mustJSONBytes(t, request))
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("create status %d", response.StatusCode)
	}
	var accepted TaskResponse
	decodeJSON(t, response, &accepted)
	var current codexprotocol.TaskRecord
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		statusResponse, err := http.Get(server.URL + "/v1/tasks/follow-task")
		if err != nil {
			t.Fatal(err)
		}
		if err := json.NewDecoder(statusResponse.Body).Decode(&current); err != nil {
			statusResponse.Body.Close()
			t.Fatal(err)
		}
		statusResponse.Body.Close()
		if current.State.Terminal() && current.ThreadID != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if current.State != codexprotocol.StateCompleted || current.ThreadID == "" {
		t.Fatalf("initial task did not complete with a thread: %+v", current)
	}
	follow := followUpRequest{Mutation: current.Mutation, Context: request.Context}
	follow.RequestID = "follow-up-request"
	follow.Context.Objective = "verify the follow-up turn"
	response = postJSON(t, server.URL+"/v1/tasks/follow-task/turns", mustJSONBytes(t, follow))
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("follow-up status %d", response.StatusCode)
	}
	var reopened TaskResponse
	decodeJSON(t, response, &reopened)
	if reopened.Record.LeaseEpoch != current.LeaseEpoch+1 || reopened.Record.State != codexprotocol.StateAccepted {
		t.Fatalf("follow-up did not advance the fenced lease: %+v", reopened.Record)
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		statusResponse, err := http.Get(server.URL + "/v1/tasks/follow-task")
		if err != nil {
			t.Fatal(err)
		}
		if err := json.NewDecoder(statusResponse.Body).Decode(&current); err != nil {
			statusResponse.Body.Close()
			t.Fatal(err)
		}
		statusResponse.Body.Close()
		if current.State.Terminal() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if current.State != codexprotocol.StateCompleted || current.LeaseEpoch != reopened.Record.LeaseEpoch {
		t.Fatalf("follow-up did not complete under the new lease: %+v", current)
	}
}

func mustJSONBytes(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func postJSON(t *testing.T, url string, payload []byte) *http.Response {
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

func decodeJSON(t *testing.T, response *http.Response, target any) {
	t.Helper()
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		t.Fatal(err)
	}
}

func TestCreateTaskPersistsBeforeReturningAndIsIdempotent(t *testing.T) {
	server := newTestHTTPServer(t)
	payload := mustJSONBytes(t, validTaskRequest("request-1", "task-1", "attempt-1", 1))
	first := postJSON(t, server.URL+"/v1/tasks", payload)
	second := postJSON(t, server.URL+"/v1/tasks", payload)
	if first.StatusCode != http.StatusAccepted || second.StatusCode != http.StatusAccepted {
		t.Fatalf("codes %d %d", first.StatusCode, second.StatusCode)
	}
	var a, b TaskResponse
	decodeJSON(t, first, &a)
	decodeJSON(t, second, &b)
	if a.Record != b.Record || !b.Idempotent {
		t.Fatalf("non-idempotent duplicate: %+v %+v", a, b)
	}
}

func TestApprovalCannotBeRelayedOverHTTP(t *testing.T) {
	server := newTestHTTPServer(t)
	request := validTaskRequest("request-1", "task-1", "attempt-1", 1)
	result := postJSON(t, server.URL+"/v1/tasks", mustJSONBytes(t, request))
	if result.StatusCode != http.StatusAccepted {
		t.Fatalf("create status %d", result.StatusCode)
	}
	approval := postJSON(t, server.URL+"/v1/tasks/task-1/approvals/a1", []byte(`{"approved":true}`))
	if approval.StatusCode != http.StatusNotFound {
		t.Fatalf("remote approval endpoint exists: %d", approval.StatusCode)
	}
}

func TestCancellationRequiresCurrentLeaseEpoch(t *testing.T) {
	server := newTestHTTPServer(t)
	stale := postJSON(t, server.URL+"/v1/tasks/task-1/cancel", []byte(`{"protocolVersion":1,"requestId":"c1","taskId":"task-1","attemptId":"attempt-1","leaseEpoch":0}`))
	if stale.StatusCode != http.StatusConflict {
		t.Fatalf("status=%d", stale.StatusCode)
	}
}

func TestEventStreamReturnsOnlyEventsAfterSequence(t *testing.T) {
	server := newTestHTTPServer(t)
	response := postJSON(t, server.URL+"/v1/tasks", mustJSONBytes(t, validTaskRequest("request-1", "task-1", "attempt-1", 1)))
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("create status %d", response.StatusCode)
	}
	get, err := http.Get(server.URL + "/v1/tasks/task-1/events?after=0")
	if err != nil {
		t.Fatal(err)
	}
	defer get.Body.Close()
	if get.StatusCode != http.StatusOK || get.Header.Get("Content-Type") != "application/x-ndjson" {
		t.Fatalf("events response: status=%d content-type=%q", get.StatusCode, get.Header.Get("Content-Type"))
	}
}

func TestAuthenticatedWorkerRejectsMissingTokenOriginAndNonJSONPosts(t *testing.T) {
	root := t.TempDir()
	policy, err := NewWorkspacePolicy(root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewTaskStore(mustJournal(root + "/tasks.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	handler := NewServerWithCapacityAndAuth(store, NewAppServerFactory(mustExecutable(t)), policy, 1, "worker-secret")
	server := httptest.NewServer(handler)
	defer server.Close()

	request, err := http.NewRequest(http.MethodGet, server.URL+"/v1/worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing token status=%d", response.StatusCode)
	}

	request, _ = http.NewRequest(http.MethodGet, server.URL+"/v1/worker", nil)
	request.Header.Set("Authorization", "Bearer worker-secret")
	request.Header.Set("Origin", "https://example.invalid")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("browser origin status=%d", response.StatusCode)
	}

	request, _ = http.NewRequest(http.MethodPost, server.URL+"/v1/tasks", bytes.NewReader(mustJSONBytes(t, validTaskRequest("r-auth", "t-auth", "a-auth", 1))))
	request.Header.Set("Authorization", "Bearer worker-secret")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("non-JSON post status=%d", response.StatusCode)
	}
}
