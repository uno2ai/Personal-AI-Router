//go:build windows

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package protectedfile

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
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
func TestWindowsPrivateCreationBeforeWrite(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private", "nested")
	if err := EnsureDir(root); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{root, filepath.Dir(root)} {
		if err := Check(path); err != nil {
			t.Fatal(err)
		}
	}
	file, err := CreateTemp(root, "child-*")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	// Private creation must disable inheritance before any secret is written.
	if err := Check(file.Name()); err != nil {
		t.Fatal(err)
	}
}

// Junction creation uses only an empty test-owned directory and a test-owned target.
func makeTestJunction(t *testing.T, link, target string) {
	t.Helper()
	if err := os.Mkdir(link, 0o700); err != nil {
		t.Fatal(err)
	}
	name, err := windows.UTF16PtrFromString(link)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	substitute, err := windows.UTF16FromString(`\??\` + target)
	if err != nil {
		t.Fatal(err)
	}
	printName, err := windows.UTF16FromString(target)
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 16+2*(len(substitute)+len(printName)))
	binary.LittleEndian.PutUint32(data, windows.IO_REPARSE_TAG_MOUNT_POINT)
	binary.LittleEndian.PutUint16(data[4:], uint16(len(data)-8))
	binary.LittleEndian.PutUint16(data[10:], uint16(2*(len(substitute)-1)))
	binary.LittleEndian.PutUint16(data[12:], uint16(2*len(substitute)))
	binary.LittleEndian.PutUint16(data[14:], uint16(2*(len(printName)-1)))
	for i, value := range append(substitute, printName...) {
		binary.LittleEndian.PutUint16(data[16+2*i:], value)
	}
	var returned uint32
	if err := windows.DeviceIoControl(handle, windows.FSCTL_SET_REPARSE_POINT, &data[0], uint32(len(data)), nil, 0, &returned, nil); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsRejectsIntermediateJunctionBeforeSecretWrite(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	private := filepath.Join(outside, "private")
	if err := os.Mkdir(private, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "redirect")
	makeTestJunction(t, link, outside)
	path := filepath.Join(link, "private", "credential.json")
	if err := WriteFile(path, []byte("fixture")); err == nil {
		t.Error("accepted intermediate junction")
	}
	if _, err := os.Stat(filepath.Join(private, "credential.json")); !os.IsNotExist(err) {
		t.Error("secret written through junction")
	}
	if err := Check(filepath.Join(link, "private")); err == nil {
		t.Error("read protection accepted intermediate junction")
	}
}

func TestWindowsRejectsUntrustedOwnerBeforeProtection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "foreign-owned")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	original, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	owner, _, err := original.Owner()
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := windows.StringToSid("S-1-5-32-545")
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION, foreign, nil, nil, nil); err != nil {
		t.Skipf("token cannot assign an untrusted test owner: %v", err)
	}
	defer windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION, owner, nil, nil, nil)
	before, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	if err := Protect(path); err == nil {
		t.Error("Protect accepted foreign owner")
	}
	if err := EnsureDir(path); err == nil {
		t.Error("EnsureDir accepted foreign owner")
	}
	after, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	if before.String() != after.String() {
		t.Error("foreign-owned ACL changed before rejection")
	}
}

func TestWindowsForeignOwnerDescriptorCannotBeProtected(t *testing.T) {
	current, err := currentSID()
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.SecurityDescriptorFromString("O:BUD:P(A;;FA;;;" + current + ")")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := protectionDescriptor(sd); err == nil {
		t.Fatal("foreign owner accepted by protection mutation boundary")
	}
}
func TestWindowsPinnedPublicationBlocksParentReplacement(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "private")
	staged, err := CreateTemp(directory, "stage-*")
	if err != nil {
		t.Fatal(err)
	}
	defer staged.Close()
	if _, err := staged.Write([]byte("fixture")); err != nil {
		t.Fatal(err)
	}
	if err := staged.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(directory, directory+"-moved"); err == nil {
		t.Fatal("pinned directory was replaceable")
	}
	if err := os.Rename(root, root+"-moved"); err == nil {
		t.Fatal("pinned ancestor was replaceable")
	}
	target := filepath.Join(directory, "secret")
	if err := staged.Publish(target); err != nil {
		t.Fatal(err)
	}
	if err := staged.Close(); err != nil {
		t.Fatal(err)
	}
	file, err := Open(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(directory, directory+"-moved"); err == nil {
		file.Close()
		t.Fatal("reader released parents before reading")
	}
	data, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, []byte("fixture")) {
		t.Fatal("reader did not return original data")
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(directory, directory+"-moved"); err != nil {
		t.Fatalf("directory handles leaked: %v", err)
	}
}
func TestWindowsPreexistingDeleteHandleRejectsWrite(t *testing.T) {
	directory := t.TempDir()
	name, err := windows.UTF16PtrFromString(directory)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.DELETE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	path := filepath.Join(directory, "secret")
	if err := WriteFile(path, []byte("fixture")); err == nil {
		t.Fatal("write proceeded with an existing ancestor deletion handle")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("failed operation wrote data")
	}
}
func TestWindowsAppendRejectsBroadFileWithoutChangingIt(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "journal")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
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
	if file, err := OpenAppend(path); err == nil {
		file.Close()
		t.Fatal("append accepted broadly accessible file")
	}
	if err := Check(path); err == nil {
		t.Fatal("append repaired broad ACL before checking")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "original" {
		t.Fatal("append rejection changed file")
	}
}
func TestWindowsAppendRejectsHardLinkAlias(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal")
	if err := WriteFile(path, []byte("original")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(path, path+"-alias"); err != nil {
		t.Fatal(err)
	}
	if file, err := OpenAppend(path); err == nil {
		file.Close()
		t.Fatal("append accepted hard-link alias")
	}
}
func TestWindowsFailedPublicationDiscardsPrivateStage(t *testing.T) {
	directory := t.TempDir()
	staged, err := CreateTemp(directory, "stage-*")
	if err != nil {
		t.Fatal(err)
	}
	name := staged.Name()
	if _, err := staged.Write([]byte("fixture")); err != nil {
		t.Fatal(err)
	}
	if err := staged.Publish(filepath.Join(t.TempDir(), "elsewhere")); err == nil {
		t.Fatal("publication escaped pinned directory")
	}
	if err := staged.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(name); !os.IsNotExist(err) {
		t.Fatal("failed stage leaked")
	}
}

func TestWindowsMissingProtectedFileRetainsNotExistError(t *testing.T) {
	if file, err := Open(filepath.Join(t.TempDir(), "missing")); !errors.Is(err, os.ErrNotExist) {
		if file != nil {
			file.Close()
		}
		t.Fatalf("missing file lost os.ErrNotExist: %v", err)
	}
}

func setTestDACL(t *testing.T, path, sddl string, flags windows.SECURITY_INFORMATION) {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|flags, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
}
func TestWindowsAppendRequiresProtectedDACLBeforeParentCanChange(t *testing.T) {
	directory := t.TempDir()
	sid, err := currentSID()
	if err != nil {
		t.Fatal(err)
	}
	setTestDACL(t, directory, "D:P(A;;FA;;;"+sid+")(A;;WD;;;WD)", windows.PROTECTED_DACL_SECURITY_INFORMATION)
	path := filepath.Join(directory, "journal")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Presently private, but the parent can still propagate a future broad ACE.
	setTestDACL(t, path, "D:(A;;FA;;;"+sid+")", windows.UNPROTECTED_DACL_SECURITY_INFORMATION)
	if file, err := OpenAppend(path); err == nil {
		file.Close()
		t.Error("accepted non-protected existing DACL")
	}
	if file, err := Open(path); err == nil {
		file.Close()
		t.Error("reader accepted non-protected existing DACL")
	}
	if err := Check(path); err == nil {
		t.Error("Check accepted non-protected existing DACL")
	}
	// A fresh staged file carries SE_DACL_PROTECTED and remains private while open.
	if err := WriteFile(path, []byte("original")); err != nil {
		t.Fatal(err)
	}
	file, err := OpenAppend(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	setTestDACL(t, directory, "D:P(A;;FA;;;"+sid+")(A;OICI;FA;;;WD)", windows.PROTECTED_DACL_SECURITY_INFORMATION)
	if err := checkHandle(windows.Handle(file.Fd())); err != nil {
		t.Fatalf("parent ACL changed protected file: %v", err)
	}
	if _, err := file.Write([]byte("-future")); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := Check(path); err != nil {
		t.Fatal(err)
	}
}
