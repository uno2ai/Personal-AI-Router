//go:build !windows

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package protectedfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUnixProtectionRejectsBroadModes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret")
	if err := WriteFile(path, []byte("fixture")); err != nil {
		t.Fatal(err)
	}
	if err := Check(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Check(path); err == nil {
		t.Fatal("broad file accepted")
	}
	if err := Protect(path); err != nil {
		t.Fatal(err)
	}
	if err := Check(path); err != nil {
		t.Fatal(err)
	}
}

func TestUnixKeepsStickyAndSymlinkTraversalParents(t *testing.T) {
	root := t.TempDir()
	shared := filepath.Join(root, "shared")
	if err := os.Mkdir(shared, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(shared, 0o777|os.ModeSticky); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "alias")
	if err := os.Symlink(shared, link); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(filepath.Join(link, "private", "secret"), []byte("fixture")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(shared)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o777 || info.Mode()&os.ModeSticky == 0 {
		t.Fatal("existing sticky ancestor was changed")
	}
}
