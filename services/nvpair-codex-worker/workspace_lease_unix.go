//go:build !windows

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

type workspaceProcessLease struct{ file *os.File }

func acquireWorkspaceProcessLease(workspaceKey string) (*workspaceProcessLease, error) {
	// Do not use os.TempDir(): GUI apps, shells, launchd jobs, and test runners
	// can have different TMPDIR values while running as the same macOS user.
	// /tmp is a stable system namespace; the UID-scoped 0700 directory keeps
	// unrelated users from opening or replacing this user's lock files.
	directory := filepath.Join("/tmp", fmt.Sprintf("nvpair-codex-workspace-locks-%d", os.Getuid()))
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create workspace lease directory: %w", err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return nil, fmt.Errorf("protect workspace lease directory: %w", err)
	}
	lock, err := os.OpenFile(filepath.Join(directory, workspaceKey+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open workspace lease: %w", err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrWorkspaceBusy
		}
		return nil, fmt.Errorf("lock workspace lease: %w", err)
	}
	return &workspaceProcessLease{file: lock}, nil
}

func releaseWorkspaceProcessLease(lease *workspaceProcessLease) error {
	if lease == nil || lease.file == nil {
		return nil
	}
	err := syscall.Flock(int(lease.file.Fd()), syscall.LOCK_UN)
	if closeErr := lease.file.Close(); err == nil {
		err = closeErr
	}
	return err
}
