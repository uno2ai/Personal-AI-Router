// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"path/filepath"
	"testing"

	"nvpair-shared/codexprotocol"
)

func validTaskRequest(requestID, taskID, attemptID string, epoch uint64) codexprotocol.TaskRequest {
	return codexprotocol.TaskRequest{
		Mutation: codexprotocol.Mutation{
			ProtocolVersion: codexprotocol.ProtocolVersion,
			RequestID:       requestID,
			TaskID:          taskID,
			AttemptID:       attemptID,
			LeaseEpoch:      epoch,
		},
		Context: codexprotocol.ContextPackage{
			Version:   codexprotocol.ContextVersion,
			Objective: "run the requested verification",
			Limits:    codexprotocol.Limits{WallSeconds: 60},
		},
		Workspace: codexprotocol.WorkspaceSpec{ID: "local", Path: "local", Mode: "read"},
		Execution: codexprotocol.ExecutionSpec{
			Sandbox:  "read-only",
			Approval: "local-only",
		},
	}
}

func mustJournal(path string) *Journal {
	journal, err := NewJournal(path)
	if err != nil {
		panic(err)
	}
	return journal
}

func newTestStore(t *testing.T) *TaskStore {
	t.Helper()
	return mustOpenStore(t, filepath.Join(t.TempDir(), "tasks.jsonl"))
}

func mustOpenStore(t *testing.T, path string) *TaskStore {
	t.Helper()
	store, err := NewTaskStore(mustJournal(path))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestJournalReplayRebuildsIdempotencyAfterReopen(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "tasks.jsonl")
	request := validTaskRequest("request-1", "task-1", "attempt-1", 1)
	first, err := NewTaskStore(mustJournal(path))
	if err != nil {
		t.Fatal(err)
	}
	created, duplicate, err := first.Accept(request)
	if err != nil || duplicate || created.TaskID != "task-1" {
		t.Fatalf("accept: %+v duplicate=%v err=%v", created, duplicate, err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := NewTaskStore(mustJournal(path))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	replayed, duplicate, err := second.Accept(request)
	if err != nil || !duplicate || replayed.AttemptID != "attempt-1" {
		t.Fatalf("replay: %+v duplicate=%v err=%v", replayed, duplicate, err)
	}
}

func TestStaleEpochCannotMutateOrReleaseWorkspace(t *testing.T) {
	store := newTestStore(t)
	request := validTaskRequest("request-1", "task-1", "attempt-1", 4)
	if _, _, err := store.Accept(request); err != nil {
		t.Fatal(err)
	}
	err := store.Mutate(codexprotocol.Mutation{
		ProtocolVersion: codexprotocol.ProtocolVersion,
		RequestID:       "cancel-1",
		TaskID:          "task-1",
		AttemptID:       "attempt-1",
		LeaseEpoch:      3,
	}, func(record *codexprotocol.TaskRecord) error {
		record.State = codexprotocol.StateCancelled
		return nil
	})
	if err == nil || !errors.Is(err, ErrStaleLease) {
		t.Fatalf("expected stale lease, got %v", err)
	}
}

func TestWorkspaceLeaseBlocksSecondTaskAcrossSupervisors(t *testing.T) {
	store := newTestStore(t)
	if _, _, err := store.Accept(validTaskRequest("r1", "t1", "a1", 1)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Accept(validTaskRequest("r2", "t2", "a2", 1)); !errors.Is(err, ErrWorkspaceBusy) {
		t.Fatalf("expected workspace lease conflict, got %v", err)
	}
}

func TestTaskIDCannotBeReusedUntilExplicitlyFenced(t *testing.T) {
	store := newTestStore(t)
	request := validTaskRequest("r1", "t1", "a1", 1)
	if _, _, err := store.Accept(request); err != nil {
		t.Fatal(err)
	}
	if err := store.Mutate(codexprotocol.Mutation{
		ProtocolVersion: codexprotocol.ProtocolVersion,
		RequestID:       "finish-1",
		TaskID:          "t1",
		AttemptID:       "a1",
		LeaseEpoch:      1,
	}, func(record *codexprotocol.TaskRecord) error {
		record.State = codexprotocol.StateFailed
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Accept(validTaskRequest("r2", "t1", "a2", 2)); !errors.Is(err, ErrTaskBusy) {
		t.Fatalf("expected explicit-fence conflict, got %v", err)
	}
	if err := store.FenceAndRelease(codexprotocol.Mutation{
		ProtocolVersion: codexprotocol.ProtocolVersion,
		RequestID:       "fence-1",
		TaskID:          "t1",
		AttemptID:       "a1",
		LeaseEpoch:      1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, duplicate, err := store.Accept(validTaskRequest("r2", "t1", "a2", 2)); err != nil || duplicate {
		t.Fatalf("expected fenced retry, duplicate=%v err=%v", duplicate, err)
	}
}
