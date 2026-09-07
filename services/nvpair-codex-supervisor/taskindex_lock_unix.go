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

type taskIndexLock struct{ file *os.File }

func acquireTaskIndexLock(path string) (*taskIndexLock, error) {
	if err := protectedfile.EnsureDir(filepath.Dir(path)); err != nil {
		return nil, fmt.Errorf("prepare lock directory: %w", err)
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open task index lock: %w", err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		return nil, fmt.Errorf("task index is already open: %w", err)
	}
	return &taskIndexLock{file: lock}, nil
}

func releaseTaskIndexLock(lock *taskIndexLock) error {
	if lock == nil || lock.file == nil {
		return nil
	}
	err := syscall.Flock(int(lock.file.Fd()), syscall.LOCK_UN)
	closeErr := lock.file.Close()
	if err == nil {
		err = closeErr
	}
	return err
}
