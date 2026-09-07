//go:build !windows

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package protectedfile

import (
	"errors"
	"os"
)

func Protect(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	mode := os.FileMode(0o600)
	if info.IsDir() {
		mode = 0o700
	} else if !info.Mode().IsRegular() {
		return errors.New("protected path is not a regular file")
	}
	return os.Chmod(path, mode)
}
func Check(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return errors.New("protected path is not a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return errors.New("protected path must not be group/world accessible")
	}
	return nil
}
