//go:build !windows

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package protectedfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

// EnsureDir creates missing ancestors privately and protects the target directory.
// It does not change permissions on existing ancestors outside the target.
func EnsureDir(path string) error {
	return ensureDir(path, true)
}

func ensureDir(path string, protectExisting bool) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		parent := filepath.Dir(path)
		if parent == path {
			return err
		}
		if _, err := os.Lstat(parent); errors.Is(err, os.ErrNotExist) {
			if err := ensureDir(parent, protectExisting); err != nil {
				return err
			}
		}
		if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("protected directory is not a directory")
	}
	if !protectExisting {
		return nil
	}
	return Protect(path)
}

// WriteFile atomically replaces a file with privately staged, synced data.
func WriteFile(path string, data []byte) error {
	return writeFile(path, data, true)
}

func writeFile(path string, data []byte, protectParent bool) error {
	if err := ensureDir(filepath.Dir(path), protectParent); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("protected destination is not a regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".private-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := Protect(f.Name()); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// WriteFilePreservingParent writes a private Desktop configuration file without
// changing existing containing-directory permissions.
func WriteFilePreservingParent(path string, data []byte) error {
	return writeFile(path, data, false)
}

// Open reads a protected regular file without following its final symlink.
func Open(path string) (*File, error) {
	if err := Check(filepath.Dir(path)); err != nil {
		return nil, err
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	info, err := f.Stat()
	if err == nil && (!info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0) {
		err = errors.New("file must be private and regular")
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return &File{File: f}, nil
}

// OpenOwnedInput reads external configuration without changing its modes or
// requiring private containing directories. Final symlinks and foreign owners
// are rejected; system traversal aliases such as /var remain usable.
func OpenOwnedInput(path string) (*File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	info, err := f.Stat()
	if err == nil {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !info.Mode().IsRegular() || !ok || stat.Uid != uint32(os.Geteuid()) {
			err = errors.New("input must be an owned regular file")
		}
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return &File{File: f}, nil
}

// OpenAppend rejects an unsafe existing file before any data can be appended.
func OpenAppend(path string) (*File, error) {
	if err := EnsureDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	fd, err := unix.Open(path, unix.O_WRONLY|unix.O_APPEND|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	info, err := f.Stat()
	if err == nil && (!info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0) {
		err = errors.New("append file must be private and regular")
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return &File{File: f}, nil
}
func CreateTemp(directory, pattern string) (*File, error) {
	if err := EnsureDir(directory); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(directory, pattern)
	if err != nil {
		return nil, err
	}
	return &File{File: f, publish: func(path string) error { return os.Rename(f.Name(), path) }}, nil
}
