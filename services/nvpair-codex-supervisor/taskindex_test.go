// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"path/filepath"
	"testing"

	"nvpair-shared/codexprotocol"
)

func validDispatchIntent() TaskIntent {
	return TaskIntent{
		TaskID: "task-1", RequestID: "request-1", AttemptID: "attempt-1", LeaseEpoch: 1,
		WorkerID: "local", State: DispatchPending,
		Request: codexprotocol.TaskRequest{
			Mutation:  codexprotocol.Mutation{ProtocolVersion: codexprotocol.ProtocolVersion, RequestID: "request-1", TaskID: "task-1", AttemptID: "attempt-1", LeaseEpoch: 1},
			Context:   codexprotocol.ContextPackage{Version: codexprotocol.ContextVersion, Objective: "run tests", Limits: codexprotocol.Limits{WallSeconds: 60}},
			Workspace: codexprotocol.WorkspaceSpec{ID: "local", Path: "local", Mode: "read"},
			Execution: codexprotocol.ExecutionSpec{Sandbox: "read-only", Approval: "local-only"},
		},
	}
}

func TestTaskIndexPersistsPreDispatchIntentAndOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "supervisor", "tasks.jsonl")
	index, err := OpenTaskIndex(path)
	if err != nil {
		t.Fatal(err)
	}
	intent := validDispatchIntent()
	if err := index.Begin(intent); err != nil {
		t.Fatal(err)
	}
	if err := index.MarkAcknowledged(intent.TaskID, intent.WorkerID); err != nil {
		t.Fatal(err)
	}
	if err := index.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenTaskIndex(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, ok := reopened.Get(intent.TaskID)
	if !ok || got.State != DispatchAcknowledged || got.WorkerID != "local" || got.Request.Context.Objective != "run tests" {
		t.Fatalf("replayed intent=%#v, found=%v", got, ok)
	}
}

func TestTaskIndexFencesDuplicateAttemptAndPreservesUncertainState(t *testing.T) {
	index, err := OpenTaskIndex(filepath.Join(t.TempDir(), "tasks.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	intent := validDispatchIntent()
	if err := index.Begin(intent); err != nil {
		t.Fatal(err)
	}
	if err := index.MarkUncertain(intent.TaskID, "connection lost after dispatch"); err != nil {
		t.Fatal(err)
	}
	if err := index.Begin(intent); err == nil {
		t.Fatal("duplicate active attempt was accepted")
	}
	got, ok := index.Get(intent.TaskID)
	if !ok || got.State != DispatchUncertain || got.LastError == "" {
		t.Fatalf("uncertain intent=%#v, found=%v", got, ok)
	}
}

func TestTaskIndexRejectsMismatchedRequestIdentity(t *testing.T) {
	index, err := OpenTaskIndex(filepath.Join(t.TempDir(), "tasks.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	intent := validDispatchIntent()
	intent.Request.TaskID = "different-task"
	if err := index.Begin(intent); err == nil {
		t.Fatal("TaskIndex accepted mismatched request identity")
	}
}

func TestTaskIndexRejectsConcurrentOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dispatch-index.jsonl")
	first, err := OpenTaskIndex(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if _, err := OpenTaskIndex(path); err == nil {
		t.Fatal("second Supervisor opened the same task index")
	}
}

func TestTaskIndexCreatesMissingParentBeforeLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new-state", "nested", "tasks.jsonl")
	index, err := OpenTaskIndex(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := index.Close(); err != nil {
		t.Fatal(err)
	}
}
