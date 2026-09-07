//go:build windows

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package protectedfile

import (
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsProtectionRejectsBroadACLs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.json")
	if err := WriteFile(path, []byte("fixture")); err != nil {
		t.Fatal(err)
	}
	if err := Check(path); err != nil {
		t.Fatalf("protected file rejected: %v", err)
	}
	for _, grant := range []string{"WD", "BU", "AU"} {
		t.Run(grant, func(t *testing.T) {
			sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + grant + ")")
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
			if err := Check(path); err == nil {
				t.Fatal("broad access accepted")
			}
			if err := Protect(path); err != nil {
				t.Fatal(err)
			}
		})
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := Check(path); err == nil {
		t.Fatal("null DACL accepted")
	}
	if err := Protect(path); err != nil {
		t.Fatal(err)
	}
}
func TestWindowsProtectionInheritedBeforeWrite(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private", "nested")
	if err := EnsureDir(root); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{root, filepath.Dir(root)} {
		if err := Check(path); err != nil {
			t.Fatal(err)
		}
	}
	file, err := os.CreateTemp(root, "child-*")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	// Even an empty file must inherit private access before any secret is written.
	if err := Check(file.Name()); err != nil {
		t.Fatal(err)
	}
}
