//go:build windows

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// npm shims are installation locators only. Never execute or parse shell code.
func nativeCodexExecutable(path string) (string, error) {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".exe" {
		return path, nil
	}
	if (ext != ".cmd" && ext != ".ps1") || !strings.EqualFold(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)), "codex") {
		return "", fmt.Errorf("unsupported Codex launcher; configure the full path to native codex.exe")
	}
	arch, triple := "x64", "x86_64-pc-windows-msvc"
	if runtime.GOARCH == "arm64" {
		arch, triple = "arm64", "aarch64-pc-windows-msvc"
	}
	modules := filepath.Join(filepath.Dir(path), "node_modules")
	// npm can install the optional platform package nested or hoisted.
	for _, parent := range []string{filepath.Join(modules, "@openai", "codex", "node_modules"), modules} {
		candidate := filepath.Join(parent, "@openai", "codex-win32-"+arch, "vendor", triple, "bin", "codex.exe")
		info, err := os.Stat(candidate)
		if err == nil && info.Mode().IsRegular() {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("native codex.exe is missing from the npm installation; reinstall @openai/codex including optional dependencies or configure its native executable path")
}
