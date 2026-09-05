//go:build !windows

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestTerminateAndWaitKillsDescendantProcessGroup(t *testing.T) {
	root := t.TempDir()
	pidPath := filepath.Join(root, "descendant.pid")
	cmd := exec.Command(os.Args[0], "-test.run=TestProcessTreeHelper")
	cmd.Env = append(os.Environ(),
		"PAIR_PROCESS_TREE_HELPER=1",
		"PAIR_PROCESS_TREE_PID_PATH="+pidPath,
	)
	cleanup, _, err := prepareChildProcess(cmd, root)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var descendantPID int
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		data, readErr := os.ReadFile(pidPath)
		if readErr == nil {
			descendantPID, err = strconv.Atoi(strings.TrimSpace(string(data)))
			if err == nil && descendantPID > 0 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if descendantPID == 0 {
		_ = terminateAndWait(cmd)
		t.Fatal("process-tree helper did not publish its descendant PID")
	}
	if err := terminateAndWait(cmd); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		err = syscall.Kill(descendantPID, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("descendant process %d survived app-server cleanup: %v", descendantPID, err)
}

func TestProcessTreeHelper(t *testing.T) {
	if os.Getenv("PAIR_PROCESS_TREE_HELPER") != "1" {
		t.Skip("helper subprocess only")
	}
	descendant := exec.Command("sleep", "60")
	if err := descendant.Start(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		os.Getenv("PAIR_PROCESS_TREE_PID_PATH"),
		[]byte(strconv.Itoa(descendant.Process.Pid)),
		0o600,
	); err != nil {
		_ = descendant.Process.Kill()
		t.Fatal(err)
	}
	_ = descendant.Wait()
}
