//go:build windows

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
	"nvpair-shared/protectedfile"
)

func TestArtifactStoreReadRejectsBroadStoredFile(t *testing.T) {
	root := t.TempDir()
	store, err := NewArtifactStore(root, 1024)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "task-1", "artifact-1")
	if err := protectedfile.WriteFile(path, []byte("fixture")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read("task-1", "artifact-1"); err != nil {
		t.Fatal(err)
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read("task-1", "artifact-1"); err == nil {
		t.Fatal("stored artifact read bypassed protected file validation")
	}
}
