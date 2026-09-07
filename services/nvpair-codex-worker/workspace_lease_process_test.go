// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestWorkspaceLeaseBlocksDifferentWorkerProcessesWithDifferentJournals(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	childTemp := filepath.Join(root, "child-tmp")
	parentTemp := filepath.Join(root, "parent-tmp")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, directory := range []string{childTemp, parentTemp} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	child := exec.Command(os.Args[0], "-test.run=TestWorkspaceLeaseProcessHelper")
	child.Dir = childTemp
	child.Env = append(os.Environ(),
		"PAIR_WORKSPACE_LEASE_HELPER=1",
		"PAIR_WORKSPACE_LEASE_JOURNAL="+filepath.Join(root, "child", "tasks.jsonl"),
		"PAIR_WORKSPACE_LEASE_PATH="+workspace,
		"TMPDIR="+childTemp,
	)
	stdin, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	child.Stderr = os.Stderr
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = child.Wait()
	})
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "READY\n" {
		t.Fatalf("workspace lease helper did not become ready: line=%q err=%v", line, err)
	}
	t.Setenv("TMPDIR", parentTemp)

	store, err := NewTaskStore(mustJournal(filepath.Join(root, "parent", "tasks.jsonl")))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, _, err = store.AcceptAt(validTaskRequest("parent-request", "parent-task", "parent-attempt", 1), workspace, 1)
	if !errors.Is(err, ErrWorkspaceBusy) {
		t.Fatalf("different Worker process acquired the same workspace: %v", err)
	}
}

func TestWorkspaceLeaseProcessHelper(t *testing.T) {
	if os.Getenv("PAIR_WORKSPACE_LEASE_HELPER") != "1" {
		t.Skip("helper subprocess only")
	}
	store, err := NewTaskStore(mustJournal(os.Getenv("PAIR_WORKSPACE_LEASE_JOURNAL")))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	request := validTaskRequest("child-request", "child-task", "child-attempt", 1)
	if _, _, err := store.AcceptAt(request, os.Getenv("PAIR_WORKSPACE_LEASE_PATH"), 1); err != nil {
		t.Fatal(err)
	}
	fmt.Println("READY")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
}
