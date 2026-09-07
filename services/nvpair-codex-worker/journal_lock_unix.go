//go:build !windows

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"nvpair-shared/protectedfile"
)

type journalLockHandle struct{ file *os.File }

func acquireJournalLock(path string) (*journalLockHandle, error) {
	if err := protectedfile.EnsureDir(filepath.Dir(path)); err != nil {
		return nil, fmt.Errorf("prepare lock directory: %w", err)
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open journal lock: %w", err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		return nil, fmt.Errorf("journal is already open: %w", err)
	}
	return &journalLockHandle{file: lock}, nil
}

func releaseJournalLock(handle *journalLockHandle) error {
	if handle == nil {
		return nil
	}
	lock := handle.file
	if lock == nil {
		return nil
	}
	err := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	closeErr := lock.Close()
	if err != nil {
		return err
	}
	return closeErr
}
