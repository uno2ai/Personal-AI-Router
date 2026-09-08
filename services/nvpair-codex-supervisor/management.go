// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"nvpair-shared/codexprotocol"
	"nvpair-shared/ipc"
	"nvpair-shared/protectedfile"
)

type managementRequest struct {
	Method     string `json:"method"`
	TaskID     string `json:"taskId,omitempty"`
	AttemptID  string `json:"attemptId,omitempty"`
	LeaseEpoch uint64 `json:"leaseEpoch,omitempty"`
}

type managementRegistry struct {
	ConfigurationID string `json:"configurationId,omitempty"`
	SchemaVersion   int    `json:"schemaVersion"`
	PID             int    `json:"pid"`
	SocketPath      string `json:"socketPath"`
	WrittenAt       string `json:"writtenAt"`
}

func ManagementSocketPath(directory string, pid int) string {
	if runtime.GOOS == "windows" {
		return `\\.\pipe\nvpair-codex-supervisor-` + fmt.Sprintf("%d", pid)
	}
	short := filepath.Join(directory, fmt.Sprintf("supervisor-%d.sock", pid))
	if len(short) < 90 {
		return short
	}
	digest := sha256.Sum256([]byte(directory))
	return filepath.Join(os.TempDir(), "nvp-"+hex.EncodeToString(digest[:])[:12], fmt.Sprintf("%d.sock", pid))
}

func WriteManagementRegistry(directory, socketPath string, pid int, configurationID string) (func(), error) {
	if strings.TrimSpace(directory) == "" || !filepath.IsAbs(directory) {
		return nil, errors.New("management registry directory must be absolute")
	}
	if err := protectedfile.EnsureDir(directory); err != nil {
		return nil, fmt.Errorf("create management registry directory: %w", err)
	}
	path := filepath.Join(directory, fmt.Sprintf("supervisor-%d.json", pid))
	data, err := json.Marshal(managementRegistry{ConfigurationID: configurationID, SchemaVersion: 1, PID: pid, SocketPath: socketPath, WrittenAt: time.Now().UTC().Format(time.RFC3339Nano)})
	if err != nil {
		return nil, err
	}
	if err := protectedfile.WriteFile(path, append(data, '\n')); err != nil {
		return nil, fmt.Errorf("write management registry: %w", err)
	}
	return func() { _ = os.Remove(path) }, nil
}

func (s *MCPServer) StartManagementSocket(ctx context.Context, path string) error {
	if strings.TrimSpace(path) == "" || (runtime.GOOS != "windows" && !filepath.IsAbs(path)) {
		return errors.New("management socket path must be absolute")
	}
	if runtime.GOOS != "windows" {
		directory := filepath.Dir(path)
		// EnsureDir protects its target with chmod. Never apply it to the OS
		// temp root, including an alternate spelling through a symlink.
		if parent, err := os.Stat(directory); err == nil {
			if temp, err := os.Stat(os.TempDir()); err == nil && os.SameFile(parent, temp) {
				return errors.New("management socket requires a private subdirectory, not the OS temp root")
			}
		}
		if err := protectedfile.EnsureDir(directory); err != nil {
			return fmt.Errorf("create management socket directory: %w", err)
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove stale management socket: %w", err)
		}
	}
	listener, err := ipc.Listen(path)
	if err != nil {
		return fmt.Errorf("listen on management socket: %w", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0o600); err != nil {
			_ = listener.Close()
			_ = os.Remove(path)
			return fmt.Errorf("protect management socket: %w", err)
		}
	}
	go func() {
		<-ctx.Done()
		_ = listener.Close()
		if runtime.GOOS != "windows" {
			_ = os.Remove(path)
		}
	}()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go s.serveManagementConnection(ctx, conn)
		}
	}()
	return nil
}

func (s *MCPServer) serveManagementConnection(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 1024), 64<<10)
	encoder := json.NewEncoder(conn)
	for scanner.Scan() {
		if ctx.Err() != nil {
			return
		}
		var request managementRequest
		decoder := json.NewDecoder(strings.NewReader(scanner.Text()))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			_ = encoder.Encode(map[string]any{"error": "invalid management request"})
			continue
		}
		result, err := s.handleManagementRequest(ctx, request)
		if err != nil {
			_ = encoder.Encode(map[string]any{"error": err.Error()})
			continue
		}
		_ = encoder.Encode(result)
	}
}

func (s *MCPServer) handleManagementRequest(ctx context.Context, request managementRequest) (any, error) {
	s.mu.Lock()
	index := s.index
	s.mu.Unlock()
	switch request.Method {
	case "status":
		workers := s.pool.Snapshot()
		return map[string]any{"state": "running", "workerCount": len(workers), "taskCount": func() int {
			if index == nil {
				return 0
			}
			return len(index.SnapshotMetadata())
		}()}, nil
	case "tasks.list":
		if index == nil {
			return map[string]any{"tasks": []TaskMetadata{}}, nil
		}
		return map[string]any{"tasks": index.SnapshotMetadata()}, nil
	case "tasks.cancel":
		if request.TaskID == "" || request.AttemptID == "" || request.LeaseEpoch == 0 {
			return nil, errors.New("tasks.cancel requires taskId, attemptId, and leaseEpoch")
		}
		if index == nil {
			return nil, errors.New("task index is unavailable")
		}
		intent, ok := index.Get(request.TaskID)
		if !ok || intent.AttemptID != request.AttemptID || intent.LeaseEpoch != request.LeaseEpoch {
			return nil, errors.New("task owner or lease is unavailable")
		}
		mutation := codexprotocol.Mutation{ProtocolVersion: codexprotocol.ProtocolVersion, RequestID: "management-cancel:" + request.TaskID, TaskID: request.TaskID, AttemptID: request.AttemptID, LeaseEpoch: request.LeaseEpoch}
		for _, client := range s.taskClients(request.TaskID) {
			raw, err := client.Cancel(ctx, mutation)
			if err != nil {
				continue
			}
			sanitized, err := sanitizeWorkerResponse("tasks.cancel", raw, request.TaskID)
			if err != nil {
				return nil, errors.New("Worker returned an invalid cancellation response")
			}
			return map[string]any{"task": json.RawMessage(sanitized)}, nil
		}
		return nil, errors.New("task owner is unavailable")
	default:
		return nil, fmt.Errorf("unknown management method %q", request.Method)
	}
}
