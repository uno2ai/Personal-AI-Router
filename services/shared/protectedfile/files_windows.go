//go:build windows

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package protectedfile

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Directory handles deny delete sharing, preventing replacement while an
// operation is in flight. Every lookup below them is a single relative name;
// no later named-path operation can redirect us through a replaced ancestor.
type directoryChain struct {
	handles []windows.Handle
	path    string
}

func (d *directoryChain) close() {
	for i := len(d.handles) - 1; i >= 0; i-- {
		windows.CloseHandle(d.handles[i])
	}
	d.handles = nil
}
func (d *directoryChain) last() windows.Handle { return d.handles[len(d.handles)-1] }
func regularHandle(handle windows.Handle, directory bool) error {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errors.New("protected path traverses a reparse point")
	}
	if directory && info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		return errors.New("protected parent is not a directory")
	}
	return nil
}
func openRelative(parent windows.Handle, name string, access, disposition, options uint32, sd *windows.SECURITY_DESCRIPTOR) (windows.Handle, error) {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `\/:`) {
		return 0, errors.New("invalid protected path component")
	}
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return 0, err
	}
	attributes := windows.OBJECT_ATTRIBUTES{RootDirectory: parent, ObjectName: objectName, Attributes: windows.OBJ_CASE_INSENSITIVE, SecurityDescriptor: sd}
	attributes.Length = uint32(unsafe.Sizeof(attributes))
	var result windows.Handle
	var status windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(&result, access|windows.SYNCHRONIZE, &attributes, &status, nil, windows.FILE_ATTRIBUTE_NORMAL, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, disposition, options|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT, 0, 0)
	if err != nil {
		var status windows.NTStatus
		if errors.As(err, &status) {
			return 0, status.Errno()
		}
		return 0, err
	}
	if err := regularHandle(result, options&windows.FILE_DIRECTORY_FILE != 0); err != nil {
		windows.CloseHandle(result)
		return 0, err
	}
	return result, nil
}
func openDirectoryChain(path string, create, protectFinal bool) (*directoryChain, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	absolute = filepath.Clean(absolute)
	volume := filepath.VolumeName(absolute)
	if volume == "" {
		return nil, errors.New("protected path has no Windows volume")
	}
	root := volume + `\`
	name, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(name, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES|windows.FILE_LIST_DIRECTORY, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	chain := &directoryChain{handles: []windows.Handle{handle}, path: absolute}
	fail := func(err error) (*directoryChain, error) { chain.close(); return nil, err }
	if err := regularHandle(handle, true); err != nil {
		return fail(err)
	}
	remainder := strings.TrimPrefix(absolute, root)
	if remainder == "" {
		if protectFinal {
			return fail(errors.New("volume root cannot be a private state directory"))
		}
		return chain, nil
	}
	parts := strings.Split(remainder, `\`)
	sd, err := privateDescriptor()
	if err != nil {
		return fail(err)
	}
	for i, part := range parts {
		access := uint32(windows.READ_CONTROL | windows.FILE_READ_ATTRIBUTES | windows.FILE_LIST_DIRECTORY)
		if protectFinal && i == len(parts)-1 {
			access |= windows.WRITE_DAC
		}
		disposition := uint32(windows.FILE_OPEN)
		if create {
			disposition = windows.FILE_OPEN_IF
		}
		next, err := openRelative(chain.last(), part, access, disposition, windows.FILE_DIRECTORY_FILE, sd)
		if err != nil {
			return fail(err)
		}
		chain.handles = append(chain.handles, next)
	}
	if protectFinal {
		existing, err := securityForHandle(chain.last())
		if err != nil {
			return fail(err)
		}
		if err := checkOwner(existing); err != nil {
			return fail(err)
		}
	}
	return chain, nil
}

// EnsureDir creates missing components with the private SD at creation. Existing
// traversal ancestors are pinned, never chmodded or required to be private.
func EnsureDir(path string) error {
	chain, err := openDirectoryChain(path, true, true)
	if err != nil {
		return err
	}
	defer chain.close()
	return protectHandle(chain.last())
}
func openCheckedPath(path string, access, disposition, options uint32, sd *windows.SECURITY_DESCRIPTOR) (*File, error) {
	chain, err := openDirectoryChain(filepath.Dir(path), false, false)
	if err != nil {
		return nil, err
	}
	handle, err := openRelative(chain.last(), filepath.Base(path), access, disposition, options, sd)
	if err != nil {
		chain.close()
		return nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	return &File{File: file, release: chain.close}, nil
}

// Open checks the actual file handle before exposing any bytes to its reader.
func Open(path string) (*File, error) {
	f, err := openCheckedPath(path, windows.GENERIC_READ|windows.READ_CONTROL, windows.FILE_OPEN, windows.FILE_NON_DIRECTORY_FILE, nil)
	if err != nil {
		return nil, err
	}
	if err := checkHandle(windows.Handle(f.Fd())); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// OpenOwnedInput reads external, user-owned input without asserting privacy or
// changing its ACL. It is not suitable for managed secrets. Like Open it pins
// ancestors and rejects reparse points, and checks the actual opened owner.
func OpenOwnedInput(path string) (*File, error) {
	f, err := openCheckedPath(path, windows.GENERIC_READ|windows.READ_CONTROL, windows.FILE_OPEN, windows.FILE_NON_DIRECTORY_FILE, nil)
	if err != nil {
		return nil, err
	}
	sd, err := securityForHandle(windows.Handle(f.Fd()))
	if err == nil {
		err = checkOwner(sd)
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// OpenAppend requires an existing file to already be private. Narrowing an ACL
// cannot revoke data handles previously obtained by another user.
func OpenAppend(path string) (*File, error) {
	chain, err := openDirectoryChain(filepath.Dir(path), true, true)
	if err != nil {
		return nil, err
	}
	sd, err := privateDescriptor()
	if err != nil {
		chain.close()
		return nil, err
	}
	handle, err := openRelative(chain.last(), filepath.Base(path), windows.FILE_APPEND_DATA|windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL, windows.FILE_OPEN_IF, windows.FILE_NON_DIRECTORY_FILE, sd)
	if err != nil {
		chain.close()
		return nil, err
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil || info.NumberOfLinks != 1 {
		windows.CloseHandle(handle)
		chain.close()
		if err != nil {
			return nil, err
		}
		return nil, errors.New("append file must not have hard-link aliases")
	}
	if err := checkHandle(handle); err != nil {
		windows.CloseHandle(handle)
		chain.close()
		return nil, err
	}
	return &File{File: os.NewFile(uintptr(handle), path), release: chain.close}, nil
}

// CreateTemp creates a private file directly below its pinned directory.
func CreateTemp(directory, pattern string) (*File, error) {
	chain, err := openDirectoryChain(directory, true, true)
	if err != nil {
		return nil, err
	}
	sd, err := privateDescriptor()
	if err != nil {
		chain.close()
		return nil, err
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		chain.close()
		return nil, err
	}
	base := strings.ReplaceAll(pattern, "*", hex.EncodeToString(random[:]))
	if !strings.Contains(pattern, "*") {
		base += hex.EncodeToString(random[:])
	}
	handle, err := openRelative(chain.last(), base, windows.GENERIC_WRITE|windows.GENERIC_READ|windows.READ_CONTROL|windows.DELETE, windows.FILE_CREATE, windows.FILE_NON_DIRECTORY_FILE, sd)
	if err != nil {
		chain.close()
		return nil, err
	}
	if err := checkHandle(handle); err != nil {
		windows.CloseHandle(handle)
		chain.close()
		return nil, err
	}
	file := &File{File: os.NewFile(uintptr(handle), filepath.Join(directory, base)), release: chain.close}
	published := false
	file.beforeClose = func() error {
		if published {
			return nil
		}
		var status windows.IO_STATUS_BLOCK
		disposition := byte(1)
		return windows.NtSetInformationFile(handle, &status, &disposition, 1, windows.FileDispositionInformation)
	}
	file.publish = func(path string) error {
		absolute, err := filepath.Abs(filepath.Dir(path))
		if err != nil {
			return err
		}
		if !strings.EqualFold(filepath.Clean(absolute), chain.path) {
			return errors.New("protected publication must stay in its containing directory")
		}
		if err := renameRelative(handle, chain.last(), filepath.Base(path)); err != nil {
			return err
		}
		published = true
		return nil
	}
	return file, nil
}

type fileRenameInformation struct {
	ReplaceIfExists uint32
	RootDirectory   windows.Handle
	FileNameLength  uint32
	FileName        [1]uint16
}

func renameRelative(handle, parent windows.Handle, name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `\/:`) {
		return errors.New("invalid protected publication name")
	}
	encoded, err := windows.UTF16FromString(name)
	if err != nil {
		return err
	}
	var layout fileRenameInformation
	size := int(unsafe.Offsetof(layout.FileName)) + 2*(len(encoded)-1)
	buffer := make([]byte, size)
	info := (*fileRenameInformation)(unsafe.Pointer(&buffer[0]))
	info.ReplaceIfExists = windows.FILE_RENAME_REPLACE_IF_EXISTS
	info.RootDirectory = parent
	info.FileNameLength = uint32(2 * (len(encoded) - 1))
	copy(unsafe.Slice(&info.FileName[0], len(encoded)-1), encoded[:len(encoded)-1])
	var status windows.IO_STATUS_BLOCK
	return windows.NtSetInformationFile(handle, &status, &buffer[0], uint32(size), windows.FileRenameInformation)
}
func WriteFile(path string, data []byte) error {
	file, err := CreateTemp(filepath.Dir(path), ".private-*")
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Publish(path); err != nil {
		return fmt.Errorf("publish protected file: %w", err)
	}
	return file.Close()
}

// WriteFilePreservingParent writes a private Desktop configuration file without
// changing existing containing-directory permissions.
func WriteFilePreservingParent(path string, data []byte) error {
	return WriteFile(path, data)
}
