//go:build windows

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// A named kernel mutex is reclaimed by Windows when the owning process dies;
// a create-exclusive sentinel file would otherwise leave a false lock after a
// crash.
type journalLockHandle struct{ handle windows.Handle }

func acquireJournalLock(path string) (*journalLockHandle, error) {
	lockPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve journal lock path: %w", err)
	}
	digest := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(lockPath))))
	name, err := windows.UTF16PtrFromString("Local\\NVPAIR-CodexJournal-" + hex.EncodeToString(digest[:]))
	if err != nil {
		return nil, fmt.Errorf("encode journal lock name: %w", err)
	}
	lock, err := windows.CreateMutex(nil, true, name)
	if err != nil {
		if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			if lock != 0 {
				_ = windows.CloseHandle(lock)
			}
			return nil, fmt.Errorf("journal is already open")
		}
		return nil, fmt.Errorf("create journal lock: %w", err)
	}
	return &journalLockHandle{handle: lock}, nil
}

func releaseJournalLock(lock *journalLockHandle) error {
	if lock == nil || lock.handle == 0 {
		return nil
	}
	err := windows.ReleaseMutex(lock.handle)
	if closeErr := windows.CloseHandle(lock.handle); err == nil {
		err = closeErr
	}
	return err
}
