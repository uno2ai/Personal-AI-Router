//go:build windows

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"nvpair-shared/winmutex"
)

type workspaceProcessLease struct{ mutex *winmutex.Mutex }

func acquireWorkspaceProcessLease(workspaceKey string) (*workspaceProcessLease, error) {
	// workspaceKey is a volume/file identity, not a path relative to this process.
	digest := sha256.Sum256([]byte(workspaceKey))
	mutex, err := winmutex.Acquire("Local\\NVPAIR-CodexWorkspace-" + hex.EncodeToString(digest[:]))
	if errors.Is(err, winmutex.ErrBusy) {
		return nil, ErrWorkspaceBusy
	}
	if err != nil {
		return nil, err
	}
	return &workspaceProcessLease{mutex: mutex}, nil
}
func releaseWorkspaceProcessLease(lock *workspaceProcessLease) error {
	if lock == nil {
		return nil
	}
	return lock.mutex.Close()
}
