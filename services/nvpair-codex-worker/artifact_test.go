// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nvpair-shared/codexprotocol"
)

func TestArtifactStoreStagesDigestAddressedDeclaredFile(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "report.txt"), []byte("report"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewArtifactStore(filepath.Join(root, "artifacts"), 1024)
	if err != nil {
		t.Fatal(err)
	}
	handoff := codexprotocol.Handoff{
		Version:   codexprotocol.HandoffVersion,
		TaskID:    "task-1",
		AttemptID: "attempt-1",
		Status:    codexprotocol.StateCompleted,
		Summary:   "done",
		Artifacts: []codexprotocol.ArtifactManifest{{Name: "report.txt"}},
	}
	if err := store.StageHandoff("task-1", workspace, &handoff); err != nil {
		t.Fatal(err)
	}
	if len(handoff.Artifacts) != 1 || handoff.Artifacts[0].ID == "" || handoff.Artifacts[0].SHA256 == "" || handoff.Artifacts[0].Bytes != 6 {
		t.Fatalf("staged manifest=%+v", handoff.Artifacts)
	}
	data, err := store.Read("task-1", handoff.Artifacts[0].ID)
	if err != nil || string(data) != "report" {
		t.Fatalf("read staged artifact=%q err=%v", data, err)
	}
}

func TestArtifactStoreRejectsPathEscapeSymlinkAndOversize(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(workspace, "linked")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	store, err := NewArtifactStore(filepath.Join(root, "artifacts"), 4)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../secret.txt", "/tmp/secret.txt", "linked/secret.txt"} {
		handoff := validArtifactHandoff(name)
		if err := store.StageHandoff("task-1", workspace, &handoff); err == nil {
			t.Errorf("artifact %q was accepted", name)
		} else if !errors.Is(err, ErrUnsafeArtifactPath) && !strings.Contains(err.Error(), "artifact") {
			t.Errorf("artifact %q returned unrelated error: %v", name, err)
		}
	}
	if err := os.WriteFile(filepath.Join(workspace, "large.txt"), []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	handoff := validArtifactHandoff("large.txt")
	if err := store.StageHandoff("task-1", workspace, &handoff); err == nil || !errors.Is(err, ErrArtifactTooLarge) {
		t.Fatalf("oversize artifact error=%v", err)
	}
}

func TestArtifactStorePrunesOnlyExpiredTaskDirectories(t *testing.T) {
	root := t.TempDir()
	store, err := NewArtifactStore(filepath.Join(root, "artifacts"), 1024)
	if err != nil {
		t.Fatal(err)
	}
	oldDir := filepath.Join(root, "artifacts", "old-task")
	newDir := filepath.Join(root, "artifacts", "new-task")
	for _, dir := range []string{oldDir, newDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	if err := os.Chtimes(oldDir, now.Add(-defaultArtifactRetention-time.Minute), now.Add(-defaultArtifactRetention-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newDir, now, now); err != nil {
		t.Fatal(err)
	}
	if err := store.Prune(now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oldDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired artifact directory remains: %v", err)
	}
	if _, err := os.Stat(newDir); err != nil {
		t.Fatalf("fresh artifact directory was pruned: %v", err)
	}
}

func validArtifactHandoff(name string) codexprotocol.Handoff {
	return codexprotocol.Handoff{
		Version:   codexprotocol.HandoffVersion,
		TaskID:    "task-1",
		AttemptID: "attempt-1",
		Status:    codexprotocol.StateCompleted,
		Summary:   "done",
		Artifacts: []codexprotocol.ArtifactManifest{{Name: name}},
	}
}
