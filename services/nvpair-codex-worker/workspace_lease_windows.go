//go:build windows

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
)

type workspaceProcessLease struct{ handle windows.Handle }

func acquireWorkspaceProcessLease(workspaceKey string) (*workspaceProcessLease, error) {
	digest := sha256.Sum256([]byte(workspaceKey))
	name, err := windows.UTF16PtrFromString("Local\\NVPAIR-CodexWorkspace-" + hex.EncodeToString(digest[:]))
	if err != nil {
		return nil, fmt.Errorf("encode workspace lease name: %w", err)
	}
	handle, err := windows.CreateMutex(nil, true, name)
	if err != nil {
		if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			if handle != 0 {
				_ = windows.CloseHandle(handle)
			}
			return nil, ErrWorkspaceBusy
		}
		return nil, fmt.Errorf("create workspace lease: %w", err)
	}
	return &workspaceProcessLease{handle: handle}, nil
}

func releaseWorkspaceProcessLease(lease *workspaceProcessLease) error {
	if lease == nil || lease.handle == 0 {
		return nil
	}
	err := windows.ReleaseMutex(lease.handle)
	if closeErr := windows.CloseHandle(lease.handle); err == nil {
		err = closeErr
	}
	return err
}
