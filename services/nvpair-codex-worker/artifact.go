// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"nvpair-shared/codexprotocol"
	"nvpair-shared/protectedfile"
)

const defaultArtifactRetention = 7 * 24 * time.Hour

var (
	ErrUnsafeArtifactPath = errors.New("unsafe artifact path")
	ErrArtifactTooLarge   = errors.New("artifact exceeds configured byte limit")
	ErrArtifactNotFound   = errors.New("artifact not found")
)

// ArtifactStore is the Worker-owned, task-scoped artifact boundary. The source
// path is used only during staging; callers can read a staged artifact only by
// task ID and opaque artifact ID.
type ArtifactStore struct {
	root      string
	maxBytes  int64
	retention time.Duration
}

func NewArtifactStore(root string, maxBytes int64) (*ArtifactStore, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("artifact root is required")
	}
	if maxBytes <= 0 {
		return nil, errors.New("artifact byte limit must be positive")
	}
	canonical, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve artifact root: %w", err)
	}
	if err := protectedfile.EnsureDir(canonical); err != nil {
		return nil, fmt.Errorf("create artifact root: %w", err)
	}
	return &ArtifactStore{root: canonical, maxBytes: maxBytes, retention: defaultArtifactRetention}, nil
}

// StageHandoff copies every declared handoff artifact from the validated task
// workspace into a Worker-created staging file before the handoff is exposed.
// The manifest name is a workspace-relative source name, never a server path.
func (s *ArtifactStore) StageHandoff(taskID, workspace string, handoff *codexprotocol.Handoff) error {
	if s == nil || handoff == nil {
		return errors.New("artifact store and handoff are required")
	}
	if !safeIdentifier(taskID) {
		return ErrUnsafeArtifactPath
	}
	canonicalWorkspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return fmt.Errorf("resolve artifact workspace: %w", err)
	}
	info, err := os.Stat(canonicalWorkspace)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("artifact workspace is not a directory")
	}
	taskDir := filepath.Join(s.root, taskID)
	if err := protectedfile.EnsureDir(taskDir); err != nil {
		return fmt.Errorf("create task artifact directory: %w", err)
	}
	for i := range handoff.Artifacts {
		manifest := &handoff.Artifacts[i]
		if err := validateArtifactName(manifest.Name); err != nil {
			return err
		}
		source, err := resolveArtifactSource(canonicalWorkspace, manifest.Name)
		if err != nil {
			return err
		}
		input, err := openArtifactSource(source)
		if err != nil {
			return fmt.Errorf("open artifact %q: %w", manifest.Name, err)
		}
		id, err := newArtifactID()
		if err != nil {
			_ = input.Close()
			return err
		}
		finalPath := filepath.Join(taskDir, id)
		output, err := protectedfile.CreateTemp(taskDir, "."+id+".tmp-*")
		if err != nil {
			_ = input.Close()
			return fmt.Errorf("create staged artifact: %w", err)
		}
		tmpPath := output.Name()
		hash := sha256.New()
		written, copyErr := io.Copy(io.MultiWriter(output, hash), io.LimitReader(input, s.maxBytes+1))
		closeInErr := input.Close()
		var syncErr error
		if copyErr == nil {
			syncErr = output.Sync()
		}
		cleanupStage := func() { _ = output.Close(); _ = os.Remove(tmpPath) }
		if copyErr != nil {
			cleanupStage()
			return fmt.Errorf("copy artifact: %w", copyErr)
		}
		if syncErr != nil {
			cleanupStage()
			return fmt.Errorf("sync staged artifact: %w", syncErr)
		}
		if closeInErr != nil {
			cleanupStage()
			return fmt.Errorf("close artifact input: %w", closeInErr)
		}
		if written > s.maxBytes {
			cleanupStage()
			return fmt.Errorf("%w: %d bytes", ErrArtifactTooLarge, written)
		}
		if err := output.Publish(finalPath); err != nil {
			cleanupStage()
			return fmt.Errorf("commit staged artifact: %w", err)
		}
		if err := output.Close(); err != nil {
			return fmt.Errorf("close staged artifact: %w", err)
		}
		if err := syncDirectory(taskDir); err != nil {
			return fmt.Errorf("sync staged artifact directory: %w", err)
		}
		manifest.ID = id
		manifest.SHA256 = hex.EncodeToString(hash.Sum(nil))
		manifest.Bytes = written
	}
	return nil
}

func (s *ArtifactStore) Read(taskID, artifactID string) ([]byte, error) {
	if !safeIdentifier(taskID) || !safeIdentifier(artifactID) {
		return nil, ErrArtifactNotFound
	}
	taskDir := filepath.Join(s.root, taskID)
	path := filepath.Join(taskDir, artifactID)
	if !containedPath(s.root, path) {
		return nil, ErrArtifactNotFound
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrArtifactNotFound
	}
	if info.Size() > s.maxBytes {
		return nil, ErrArtifactTooLarge
	}
	file, err := openArtifactSource(path)
	if err != nil {
		return nil, ErrArtifactNotFound
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, s.maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > s.maxBytes {
		return nil, ErrArtifactTooLarge
	}
	return data, nil
}

func (s *ArtifactStore) Prune(now time.Time) error {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return err
	}
	cutoff := now.Add(-s.retention)
	for _, entry := range entries {
		if !entry.IsDir() || !safeIdentifier(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		if err := removeArtifactTaskDir(filepath.Join(s.root, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func validateArtifactName(name string) error {
	if strings.TrimSpace(name) == "" || filepath.IsAbs(name) || strings.ContainsRune(name, '\x00') {
		return fmt.Errorf("%w: %q", ErrUnsafeArtifactPath, name)
	}
	if strings.Contains(name, "\\") || strings.Contains(name, "//") {
		return fmt.Errorf("%w: %q", ErrUnsafeArtifactPath, name)
	}
	for _, component := range strings.Split(name, "/") {
		if component == "" || component == "." || component == ".." {
			return fmt.Errorf("%w: %q", ErrUnsafeArtifactPath, name)
		}
	}
	return nil
}

func resolveArtifactSource(workspace, name string) (string, error) {
	if err := validateArtifactName(name); err != nil {
		return "", err
	}
	path := filepath.Join(workspace, filepath.FromSlash(name))
	if !containedPath(workspace, path) {
		return "", ErrUnsafeArtifactPath
	}
	if err := rejectSymlinkComponents(workspace, path); err != nil {
		return "", fmt.Errorf("artifact source: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve artifact %q: %w", name, err)
	}
	if !containedPath(workspace, canonical) {
		return "", ErrUnsafeArtifactPath
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("artifact %q is not a regular file", name)
	}
	return canonical, nil
}

func containedPath(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func safeIdentifier(value string) bool {
	if value == "" || len(value) > 160 {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}

func newArtifactID() (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return "artifact-" + hex.EncodeToString(data[:]), nil
}

func removeArtifactTaskDir(path string) error { return os.RemoveAll(path) }
