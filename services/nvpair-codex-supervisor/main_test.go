// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"path/filepath"
	"testing"
)

func TestVersionFlagIsDefined(t *testing.T) {
	if Version == "" {
		t.Fatal("version must be linkable by services/build.sh")
	}
}

func TestManagedMainSessionsUseStableDistinctTaskIndexes(t *testing.T) {
	root := t.TempDir()
	first := supervisorTaskIndexPath(root, true, 101)
	second := supervisorTaskIndexPath(root, true, 202)
	if first == second {
		t.Fatalf("concurrent Main sessions share task index %q", first)
	}
	if first != filepath.Join(root, "dispatch-index-main-101.jsonl") {
		t.Fatalf("managed Main session index is not stable across Supervisor restarts: %q", first)
	}
	if restarted := supervisorTaskIndexPath(root, true, 101); restarted != first {
		t.Fatalf("restarted Supervisor index=%q, want %q", restarted, first)
	}
	if filepath.Dir(first) != root || filepath.Dir(second) != root {
		t.Fatalf("managed task indexes escaped state root: %q %q", first, second)
	}
	if got := supervisorTaskIndexPath(root, false, 101); got != filepath.Join(root, "dispatch-index.jsonl") {
		t.Fatalf("standalone task index changed: %q", got)
	}
}
