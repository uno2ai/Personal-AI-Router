//go:build windows

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func canonicalWorkspaceIdentity(path string) string {
	clean := filepath.Clean(path)
	name, err := windows.UTF16PtrFromString(clean)
	if err == nil {
		handle, err := windows.CreateFile(name, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
		if err == nil {
			var info windows.ByHandleFileInformation
			infoErr := windows.GetFileInformationByHandle(handle, &info)
			_ = windows.CloseHandle(handle)
			if infoErr == nil {
				index := uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow)
				return fmt.Sprintf("windows:%d:%d", info.VolumeSerialNumber, index)
			}
		}
	}
	// Windows path lookup is case-insensitive by default. Lowercasing the
	// fallback still prevents the common alias, while existing paths use the
	// stronger volume/file-index identity above.
	return "path:" + strings.ToLower(clean)
}
