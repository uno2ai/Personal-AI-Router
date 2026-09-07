// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"

	"nvpair-shared/protectedfile"
)

const protectedFileLimit = 4 << 20

// The one-shot helper runs before logging and supervision. Errors deliberately
// omit paths, operating-system errors and input/output bytes.
func protectedFileCLI(operation, path string, input io.Reader, output io.Writer) error {
	if !filepath.IsAbs(path) {
		return errors.New("protected file requires an absolute path")
	}
	if operation == "write" {
		data, err := io.ReadAll(io.LimitReader(input, protectedFileLimit+1))
		if err != nil || len(data) > protectedFileLimit {
			return errors.New("protected file input invalid or exceeds 4 MiB")
		}
		if err := protectedfile.WriteFilePreservingParent(path, data); err != nil {
			return errors.New("protected file write rejected")
		}
		return nil
	}
	var file *protectedfile.File
	var err error
	switch operation {
	case "read":
		file, err = protectedfile.Open(path)
	case "read-owned-input":
		file, err = protectedfile.OpenOwnedInput(path)
	default:
		return errors.New("unsupported protected file operation")
	}
	if errors.Is(err, os.ErrNotExist) {
		return os.ErrNotExist
	}
	if err != nil {
		return errors.New("protected file read rejected")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, protectedFileLimit+1))
	if err != nil || len(data) > protectedFileLimit {
		return errors.New("protected file read invalid or exceeds 4 MiB")
	}
	if _, err := output.Write(data); err != nil {
		return errors.New("protected file output failed")
	}
	return nil
}
