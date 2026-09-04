// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"nvpair-shared/codexprotocol"
)

type WorkspacePolicy struct {
	root string
}

func NewWorkspacePolicy(root string) (WorkspacePolicy, error) {
	if root == "" {
		return WorkspacePolicy{}, errors.New("workspace root is required")
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return WorkspacePolicy{}, fmt.Errorf("resolve workspace root: %w", err)
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return WorkspacePolicy{}, fmt.Errorf("stat workspace root: %w", err)
	}
	if !info.IsDir() {
		return WorkspacePolicy{}, errors.New("workspace root is not a directory")
	}
	return WorkspacePolicy{root: canonical}, nil
}

func (p WorkspacePolicy) Resolve(spec codexprotocol.WorkspaceSpec) (string, error) {
	if err := spec.Validate(); err != nil {
		return "", err
	}
	if spec.ID != "local" {
		return "", fmt.Errorf("unsupported workspace id %q", spec.ID)
	}
	relative := spec.Path
	if relative == "local" {
		relative = "."
	} else if strings.HasPrefix(relative, "local/") || strings.HasPrefix(relative, "local\\") {
		relative = relative[len("local/"):]
	}
	candidate := filepath.Join(p.root, relative)
	if err := rejectSymlinkComponents(p.root, candidate); err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve workspace path: %w", err)
	}
	contained, err := filepath.Rel(p.root, canonical)
	if err != nil || contained == ".." || strings.HasPrefix(contained, ".."+string(filepath.Separator)) {
		return "", errors.New("workspace path escapes configured root")
	}
	// Re-check immediately after canonicalization to narrow the check/use
	// window. The app-server is launched with this exact canonical cwd; a
	// future Windows implementation must additionally reject reparse points.
	if err := rejectSymlinkComponents(p.root, candidate); err != nil {
		return "", err
	}
	return canonical, nil
}

func rejectSymlinkComponents(root, target string) error {
	relative, err := filepath.Rel(root, target)
	if err != nil {
		return fmt.Errorf("compare workspace paths: %w", err)
	}
	current := root
	if relative == "." {
		return nil
	}
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return fmt.Errorf("inspect workspace component: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 || isReparsePoint(current) {
			return fmt.Errorf("workspace path contains symlink component %q", component)
		}
	}
	return nil
}
