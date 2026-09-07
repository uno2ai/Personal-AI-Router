//go:build !windows

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package protectedfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUnixDesktopWritePreservesExistingParent(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "config.toml")
	if err := WriteFilePreservingParent(path, []byte("fixture")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(directory)
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatal("existing parent permissions changed")
	}
	info, err = os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("desktop config was not private")
	}
}

func TestUnixDesktopWriteCreatesPrivateParents(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "missing", "nested")
	if err := WriteFilePreservingParent(filepath.Join(directory, "config.toml"), []byte("fixture")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{directory, filepath.Dir(directory)} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Fatal("new parent was not private")
		}
	}
}

func TestUnixOwnedInputPreservesModesAndRejectsFinalSymlink(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "config.toml")
	if err := os.WriteFile(path, []byte("fixture"), 0o644); err != nil {
		t.Fatal(err)
	}
	file, err := OpenOwnedInput(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o644 {
		t.Fatal("read changed input modes")
	}
	info, err = os.Stat(directory)
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatal("read changed parent modes")
	}
	if err := os.Symlink(path, path+"-link"); err != nil {
		t.Fatal(err)
	}
	if file, err := OpenOwnedInput(path + "-link"); err == nil {
		file.Close()
		t.Fatal("owned input followed final symlink")
	}
}
