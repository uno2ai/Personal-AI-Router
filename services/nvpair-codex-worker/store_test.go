// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
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

func TestLostAttemptCannotResumeUntilExplicitlyFenced(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "tasks.jsonl")
	request := validTaskRequest("lost-request", "lost-task", "lost-attempt", 1)
	first := mustOpenStore(t, path)
	if _, _, err := first.Accept(request); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second := mustOpenStore(t, path)
	reconciled, ok := second.Get(request.TaskID)
	if !ok || reconciled.State != codexprotocol.StateLost || reconciled.Fenced {
		t.Fatalf("restart did not preserve unresolved lost attempt: %+v", reconciled)
	}
	if _, err := second.PrepareFollowUp(reconciled.Mutation); !errors.Is(err, ErrTaskBusy) {
		t.Fatalf("lost attempt was resumable before fencing: %v", err)
	}
	fence := reconciled.Mutation
	fence.RequestID = "lost-fence"
	if err := second.FenceAndRelease(fence); err != nil {
		t.Fatal(err)
	}
	retry := validTaskRequest("lost-retry", request.TaskID, "new-attempt", reconciled.LeaseEpoch+1)
	if _, duplicate, err := second.AcceptAt(retry, root, 1); err != nil || duplicate {
		t.Fatalf("explicitly fenced attempt was not replaceable: duplicate=%v err=%v", duplicate, err)
	}
}

func TestEventSequenceAndMetadataSurviveReplay(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "tasks.jsonl")
	store := mustOpenStore(t, path)
	request := validTaskRequest("r-seq", "t-seq", "a-seq", 1)
	if _, _, err := store.Accept(request); err != nil {
		t.Fatal(err)
	}
	mutation := request.Mutation
	mutation.RequestID = "r-seq-1"
	if err := store.MutateEvent(mutation, "progress", map[string]string{"source": "fixture"}, func(record *codexprotocol.TaskRecord) error {
		record.State = codexprotocol.StateRunning
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	mutation.RequestID = "r-seq-2"
	if err := store.MutateEvent(mutation, "progress", map[string]string{"source": "fixture-2"}, func(record *codexprotocol.TaskRecord) error {
		record.State = codexprotocol.StateRunning
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	events := store.Events(request.TaskID, 1)
	if len(events) != 1 || events[0].Seq != 2 || events[0].Metadata["source"] != "fixture-2" {
		t.Fatalf("events after sequence: %+v", events)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	replayed := mustOpenStore(t, path)
	replayedEvents := replayed.Events(request.TaskID, 0)
	if len(replayedEvents) != 3 || replayedEvents[1].Seq != 2 || replayedEvents[2].Kind != "restart_reconciled" {
		t.Fatalf("replayed events: %+v", replayedEvents)
	}
}

func TestMutationRequestIDReuseWithDifferentTupleConflicts(t *testing.T) {
	store := newTestStore(t)
	request := validTaskRequest("r-mutation", "t-mutation", "a-mutation", 1)
	if _, _, err := store.Accept(request); err != nil {
		t.Fatal(err)
	}
	mutation := request.Mutation
	mutation.RequestID = "same-request"
	if err := store.Mutate(mutation, func(record *codexprotocol.TaskRecord) error {
		record.State = codexprotocol.StateRunning
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	mutation.TaskID = "other-task"
	if err := store.Mutate(mutation, func(record *codexprotocol.TaskRecord) error { return nil }); !errors.Is(err, ErrRequestConflict) {
		t.Fatalf("expected mutation request conflict, got %v", err)
	}
}

func TestCapacityAndCanonicalWorkspaceAreEnforcedAtomically(t *testing.T) {
	store := newTestStore(t)
	first := validTaskRequest("r-cap-1", "t-cap-1", "a-cap-1", 1)
	second := validTaskRequest("r-cap-2", "t-cap-2", "a-cap-2", 1)
	if _, _, err := store.AcceptAt(first, "/canonical/workspace", 1); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.AcceptAt(second, "/canonical/workspace/./", 1); !errors.Is(err, ErrWorkspaceBusy) {
		t.Fatalf("expected canonical workspace conflict, got %v", err)
	}
	third := validTaskRequest("r-cap-3", "t-cap-3", "a-cap-3", 1)
	if _, _, err := store.AcceptAt(third, "/another/workspace", 1); !errors.Is(err, ErrCapacity) {
		t.Fatalf("expected capacity conflict, got %v", err)
	}
}

func TestWorkspaceLeaseKeyUsesFilesystemIdentityForCaseAlias(t *testing.T) {
	root := t.TempDir()
	alias := strings.ToUpper(root)
	first, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := os.Stat(alias)
	if err != nil || !os.SameFile(first, second) {
		t.Skip("filesystem is case-sensitive")
	}
	if workspaceLeaseKey(root) != workspaceLeaseKey(alias) {
		t.Fatalf("case aliases acquired different workspace keys")
	}
}
