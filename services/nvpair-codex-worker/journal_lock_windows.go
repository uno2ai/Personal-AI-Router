//go:build windows

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"nvpair-shared/winmutex"
	"path/filepath"
	"strings"
)

type journalLockHandle struct{ mutex *winmutex.Mutex }

func acquireJournalLock(path string) (*journalLockHandle, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(absolute))))
	mutex, err := winmutex.Acquire("Local\\NVPAIR-CodexJournal-" + hex.EncodeToString(digest[:]))
	if errors.Is(err, winmutex.ErrBusy) {
		return nil, errors.New("journal is already open")
	}
	if err != nil {
		return nil, err
	}
	return &journalLockHandle{mutex: mutex}, nil
}
func releaseJournalLock(lock *journalLockHandle) error {
	if lock == nil {
		return nil
	}
	return lock.mutex.Close()
}
