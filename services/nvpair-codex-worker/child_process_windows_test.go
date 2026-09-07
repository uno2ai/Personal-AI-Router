//go:build windows

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"fmt"
	"golang.org/x/sys/windows"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestWindowsChildCannotRunBeforeJobAssignment(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=TestWindowsChildProcessHelper")
	cmd.Env = append(os.Environ(), "PAIR_WINDOWS_CHILD_HELPER=1")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	cleanup, verify, err := prepareChildProcess(cmd, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { input.Close(); terminateAndWait(cmd) }()
	ready := make(chan string, 1)
	go func() { line, _ := bufio.NewReader(out).ReadString('\n'); ready <- line }()
	select {
	case line := <-ready:
		t.Fatalf("child ran before job assignment: %q", line)
	case <-time.After(100 * time.Millisecond):
	}
	if err := verify(); err != nil {
		t.Fatal(err)
	}
	var descendantPID int
	select {
	case line := <-ready:
		descendantPID, err = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "READY ")))
		if err != nil || descendantPID <= 0 {
			t.Fatalf("readiness %q", line)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("attached child did not resume")
	}
	// Closing the job kills even a process that is waiting indefinitely for input.
	process, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(cmd.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(process)
	descendant, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(descendantPID))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(descendant)
	releaseChildProcess(cmd)
	result, err := windows.WaitForSingleObject(process, 2000)
	if err != nil || result != windows.WAIT_OBJECT_0 {
		t.Fatalf("job close did not stop child: %d %v", result, err)
	}
	result, err = windows.WaitForSingleObject(descendant, 2000)
	if err != nil || result != windows.WAIT_OBJECT_0 {
		t.Fatalf("descendant escaped job: %d %v", result, err)
	}
}
func TestWindowsChildProcessHelper(t *testing.T) {
	mode := os.Getenv("PAIR_WINDOWS_CHILD_HELPER")
	if mode == "" {
		return
	}
	if mode == "2" {
		for {
			time.Sleep(time.Hour)
		}
	}
	descendant := exec.Command(os.Args[0], "-test.run=TestWindowsChildProcessHelper")
	descendant.Env = replaceEnvironment(os.Environ(), map[string]string{"PAIR_WINDOWS_CHILD_HELPER": "2"})
	if err := descendant.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { descendant.Process.Kill(); descendant.Wait() }()
	fmt.Printf("READY %d\n", descendant.Process.Pid)
	bufio.NewReader(os.Stdin).ReadByte()
}
