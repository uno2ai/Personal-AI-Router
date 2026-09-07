//go:build windows

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestWorkspaceLeaseReleaseFromDifferentThread(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	key := filepath.Join(t.TempDir(), "workspace")
	lease, err := acquireWorkspaceProcessLease(key)
	if err != nil {
		t.Fatal(err)
	}
	released := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		released <- releaseWorkspaceProcessLease(lease)
	}()
	if err := <-released; err != nil {
		t.Fatal(err)
	}
	next, err := acquireWorkspaceProcessLease(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := releaseWorkspaceProcessLease(next); err != nil {
		t.Fatal(err)
	}
}
