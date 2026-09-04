//go:build windows

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"
)

// Windows file locking is provided by the platform when the lock handle is
// held open. The exclusive create keeps this conservative for the Phase 1
// local Worker; a future Windows hardening pass should replace it with
// LockFileEx so stale handles are reclaimed by the kernel.
func acquireJournalLock(path string) (*os.File, error) {
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("journal is already open: %w", err)
	}
	return lock, nil
}

func releaseJournalLock(lock *os.File) error {
	if lock == nil {
		return nil
	}
	name := lock.Name()
	err := lock.Close()
	if removeErr := os.Remove(name); err == nil {
		err = removeErr
	}
	return err
}
