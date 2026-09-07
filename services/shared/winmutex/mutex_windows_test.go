//go:build windows

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package winmutex

import (
	"bufio"
	"errors"
	"fmt"
	"golang.org/x/sys/windows"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestMutexRecoversAbandonedProcessOwner(t *testing.T) {
	name := "Local\\NVPAIR-test-" + filepath.Base(t.TempDir())
	encoded, err := windows.UTF16PtrFromString(name)
	if err != nil {
		t.Fatal(err)
	}
	// Keep the kernel object alive after the child dies, exercising WAIT_ABANDONED.
	keeper, err := windows.CreateMutex(nil, false, encoded)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(keeper)
	child := exec.Command(os.Args[0], "-test.run=TestMutexOwnerHelper")
	child.Env = append(os.Environ(), "PAIR_MUTEX_HELPER="+name)
	out, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	input, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	waited := false
	defer func() {
		if !waited {
			child.Process.Kill()
			child.Wait()
		}
	}()
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || line != "READY\n" {
		t.Fatalf("helper readiness %q %v", line, err)
	}
	if m, err := Acquire(name); !errors.Is(err, ErrBusy) {
		if m != nil {
			m.Close()
		}
		t.Fatalf("contention: %v", err)
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	waited = true
	m, err := Acquire(name)
	if err != nil {
		t.Fatalf("abandoned owner not recovered: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := Acquire(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := next.Close(); err != nil {
		t.Fatal(err)
	}
}
func TestMutexOwnerHelper(t *testing.T) {
	name := os.Getenv("PAIR_MUTEX_HELPER")
	if name == "" {
		return
	}
	m, err := Acquire(name)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	fmt.Println("READY")
	bufio.NewReader(os.Stdin).ReadByte()
}
