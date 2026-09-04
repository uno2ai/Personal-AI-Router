//go:build !windows

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// canonicalWorkspaceIdentity prefers the filesystem object identity over a
// spelling of the path. This prevents aliases such as different relative
// spellings from acquiring independent task leases. The clean path fallback
// keeps the pure store tests useful for synthetic, nonexistent paths.
func canonicalWorkspaceIdentity(path string) string {
	clean := filepath.Clean(path)
	info, err := os.Stat(clean)
	if err == nil {
		if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			return fmt.Sprintf("unix:%d:%d", stat.Dev, stat.Ino)
		}
	}
	return "path:" + clean
}
