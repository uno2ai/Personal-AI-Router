// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"testing"

	"nvpair-shared/codexprotocol"
)

func TestWorkspacePolicyRejectsTraversalAndSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	policy, err := NewWorkspacePolicy(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../secret", "linked/secret.txt", "/tmp/outside"} {
		if _, err := policy.Resolve(codexprotocol.WorkspaceSpec{Path: path, Mode: "read"}); err == nil {
			t.Errorf("Resolve(%q) accepted an unsafe path", path)
		}
	}
}

func TestWorkspacePolicyResolvesOnlyConfiguredLocalAlias(t *testing.T) {
	root := t.TempDir()
	policy, err := NewWorkspacePolicy(root)
	if err != nil {
		t.Fatal(err)
	}
	got, err := policy.Resolve(codexprotocol.WorkspaceSpec{ID: "local", Path: "local", Mode: "read"})
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("resolved path %q, want %q", got, want)
	}
}
