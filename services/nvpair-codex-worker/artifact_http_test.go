// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"nvpair-shared/codexprotocol"
)

func TestArtifactEndpointRequiresDeclaredOpaqueID(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "report.txt"), []byte("report"), 0o600); err != nil {
		t.Fatal(err)
	}
	policy, err := NewWorkspacePolicy(root)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := NewJournal(filepath.Join(root, "tasks.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewTaskStore(journal)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	artifacts, err := NewArtifactStore(filepath.Join(root, "artifact-state"), 1024)
	if err != nil {
		t.Fatal(err)
	}
	request := validTaskRequest("artifact-request", "artifact-task", "artifact-attempt", 1)
	record, _, err := store.AcceptAt(request, root, 1)
	if err != nil {
		t.Fatal(err)
	}
	handoff := validArtifactHandoff("report.txt")
	if err := artifacts.StageHandoff(record.TaskID, root, &handoff); err != nil {
		t.Fatal(err)
	}
	mutation := record.Mutation
	mutation.RequestID += ":terminal"
	if err := store.Mutate(mutation, func(current *codexprotocol.TaskRecord) error {
		current.State = codexprotocol.StateCompleted
		current.Handoff = &handoff
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewServerWithArtifacts(store, NewAppServerFactory("codex"), policy, artifacts, 1, ""))
	defer server.Close()
	response, err := http.Get(server.URL + "/v1/tasks/artifact-task/artifacts/" + handoff.Artifacts[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || string(data) != "report" {
		t.Fatalf("artifact response status=%d body=%q", response.StatusCode, data)
	}
	response, err = http.Get(server.URL + "/v1/tasks/artifact-task/artifacts/not-declared")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("undeclared artifact status=%d", response.StatusCode)
	}
}
