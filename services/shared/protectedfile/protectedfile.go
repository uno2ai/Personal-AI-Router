// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package protectedfile protects same-user service state before writing secrets.
package protectedfile

import (
	"errors"
	"os"
	"sync"
)

// File retains the directory handles needed to keep an operation bound to the
// inspected objects. Close releases both the file and its directory handles.
type File struct {
	*os.File
	closeOnce   sync.Once
	closeErr    error
	beforeClose func() error
	release     func()
	publish     func(string) error
}

func (f *File) Close() error {
	f.closeOnce.Do(func() {
		if f.beforeClose != nil {
			f.closeErr = f.beforeClose()
		}
		f.closeErr = errors.Join(f.closeErr, f.File.Close())
		if f.release != nil {
			f.release()
		}
	})
	return f.closeErr
}

// Publish renames a staged file within its pinned containing directory.
// The caller must sync it first and keep it open until publication completes.
func (f *File) Publish(path string) error {
	if f.publish == nil {
		return errors.New("file is not staged for publication")
	}
	return f.publish(path)
}
