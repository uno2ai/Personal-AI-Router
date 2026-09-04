// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package codexprotocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func validTaskRequest(requestID, taskID, attemptID string, epoch uint64) TaskRequest {
	return TaskRequest{
		Mutation: Mutation{
			ProtocolVersion: ProtocolVersion,
			RequestID:       requestID,
			TaskID:          taskID,
			AttemptID:       attemptID,
			LeaseEpoch:      epoch,
		},
		Context: ContextPackage{
			Version:           1,
			Objective:         "run the requested verification",
			RelevantDecisions: []string{"return command names and outcomes"},
			Constraints:       []string{"do not modify files"},
			RequiredEvidence:  []string{"test outcome"},
			Limits:            Limits{WallSeconds: 60},
		},
		Workspace: WorkspaceSpec{ID: "local", Path: "local", Mode: "read"},
		Execution: ExecutionSpec{Sandbox: "read-only", Approval: "local-only"},
	}
}

func validHandoff() Handoff {
	return Handoff{
		Version:   1,
		TaskID:    "task-1",
		AttemptID: "attempt-1",
		Status:    StateCompleted,
		Summary:   "verification completed",
	}
}

func TestDecodeTaskRequestRejectsOversizedContext(t *testing.T) {
	request := validTaskRequest("request-1", "task-1", "attempt-1", 1)
	request.Context.Objective = strings.Repeat("x", MaxContextBytes)
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeTaskRequest(payload); err == nil || !strings.Contains(err.Error(), "256 KiB") {
		t.Fatalf("expected bounded-context error, got %v", err)
	}
}

func TestMutationRequiresAllFencingFields(t *testing.T) {
	mutation := Mutation{ProtocolVersion: ProtocolVersion, RequestID: "r1", TaskID: "t1", AttemptID: "a1"}
	if err := mutation.Validate(); err == nil || !strings.Contains(err.Error(), "leaseEpoch") {
		t.Fatalf("expected lease error, got %v", err)
	}
}

func TestHandoffRejectsMismatchedTaskAndOversizedEvidence(t *testing.T) {
	handoff := validHandoff()
	handoff.TaskID = "other-task"
	if err := handoff.Validate("task-1", "attempt-1"); err == nil {
		t.Fatal("expected task identity mismatch")
	}
	handoff = validHandoff()
	handoff.Summary = strings.Repeat("x", MaxHandoffBytes)
	if err := handoff.Validate("task-1", "attempt-1"); err == nil || !strings.Contains(err.Error(), "64 KiB") {
		t.Fatalf("expected bounded-handoff error, got %v", err)
	}
}

func TestTaskRequestCrossValidatesWorkspaceModeAndSandbox(t *testing.T) {
	request := validTaskRequest("r", "t", "a", 1)
	request.Execution.Sandbox = "workspace-write"
	if err := request.Validate(); err == nil {
		t.Fatal("read workspace accepted workspace-write sandbox")
	}
	request = validTaskRequest("r", "t", "a", 1)
	request.Workspace.Mode = "write"
	if err := request.Validate(); err == nil {
		t.Fatal("write workspace accepted read-only sandbox")
	}
}

func TestDecodeTaskRequestRejectsTrailingJSON(t *testing.T) {
	payload, err := json.Marshal(validTaskRequest("r", "t", "a", 1))
	if err != nil {
		t.Fatal(err)
	}
	payload = append(payload, []byte(` {}`)...)
	if _, err := DecodeTaskRequest(payload); err == nil {
		t.Fatal("accepted trailing JSON value")
	}
}
