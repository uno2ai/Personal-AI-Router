//go:build windows

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestResolveWindowsCodexNPMShims(t *testing.T) {
	root := filepath.Join(t.TempDir(), "npm path & spaces")
	arch, triple := "x64", "x86_64-pc-windows-msvc"
	if runtime.GOARCH == "arm64" {
		arch, triple = "arm64", "aarch64-pc-windows-msvc"
	}
	native := filepath.Join(root, "node_modules", "@openai", "codex", "node_modules", "@openai", "codex-win32-"+arch, "vendor", triple, "bin", "codex.exe")
	if err := os.MkdirAll(filepath.Dir(native), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(native, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, ext := range []string{".ps1", ".cmd"} {
		shim := filepath.Join(root, "codex"+ext)
		if err := os.WriteFile(shim, []byte("throw 'must never execute shell'"), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := resolveCodexExecutable(shim)
		if err != nil || got != native {
			t.Errorf("resolve %s = %q, %v; want %q", ext, got, err, native)
		}
	}
}
func TestRejectUnsupportedWindowsCodexScript(t *testing.T) {
	script := filepath.Join(t.TempDir(), "untrusted.cmd")
	if err := os.WriteFile(script, []byte("echo unsafe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveCodexExecutable(script); err == nil {
		t.Fatal("accepted unsupported script")
	}
}
