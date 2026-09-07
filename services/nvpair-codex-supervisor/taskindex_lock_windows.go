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

type taskIndexLock struct{ mutex *winmutex.Mutex }

func acquireTaskIndexLock(path string) (*taskIndexLock, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(absolute))))
	mutex, err := winmutex.Acquire("Local\\NVPAIR-CodexSupervisor-" + hex.EncodeToString(digest[:]))
	if errors.Is(err, winmutex.ErrBusy) {
		return nil, errors.New("task index is already open")
	}
	if err != nil {
		return nil, err
	}
	return &taskIndexLock{mutex: mutex}, nil
}
func releaseTaskIndexLock(lock *taskIndexLock) error {
	if lock == nil {
		return nil
	}
	return lock.mutex.Close()
}
