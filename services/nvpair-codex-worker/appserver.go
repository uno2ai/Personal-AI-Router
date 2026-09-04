// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"nvpair-shared/codexprotocol"
	"nvpair-shared/jsonrpc"
)

// Version is replaced by services/build.sh and services/build.bat at release
// build time. Development binaries intentionally report dev.
var Version = "dev"

type AppServerFactory struct {
	binary string
}

func NewAppServerFactory(binary string) AppServerFactory {
	if binary == "" {
		binary = "codex"
	}
	return AppServerFactory{binary: binary}
}

func (f AppServerFactory) New() *AppServerSession {
	return &AppServerSession{binary: f.binary}
}

type AppServerSession struct {
	binary string

	mu     sync.Mutex
	cancel context.CancelFunc
	peer   *jsonrpc.Peer
}

func (s *AppServerSession) Run(ctx context.Context, request codexprotocol.TaskRequest, emit func(codexprotocol.TaskEvent)) (codexprotocol.Handoff, error) {
	cwd, err := appServerCWD(request.Workspace.Path)
	if err != nil {
		return codexprotocol.Handoff{}, err
	}
	childCtx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.cancel = cancel
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.cancel = nil
		s.peer = nil
		s.mu.Unlock()
		cancel()
	}()

	cmd := exec.CommandContext(childCtx, s.binary, "app-server", "--listen", "stdio://")
	cmd.Dir = cwd
	cmd.Stderr = io.Discard
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return codexprotocol.Handoff{}, fmt.Errorf("open app-server stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return codexprotocol.Handoff{}, fmt.Errorf("open app-server stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return codexprotocol.Handoff{}, fmt.Errorf("start app-server: %w", err)
	}
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	peer := jsonrpc.NewPeer(jsonrpc.NewCodec(&readWriter{Reader: stdout, Writer: stdin}))
	s.mu.Lock()
	s.peer = peer
	s.mu.Unlock()
	approvalCh := make(chan struct{}, 1)
	completedCh := make(chan struct{}, 1)
	serveDone := make(chan struct{})
	go func() {
		defer close(serveDone)
		peer.Serve(func(message *jsonrpc.Message) {
			s.handleServerRequest(peer, message, approvalCh, emit)
		}, func(method string, _ json.RawMessage) {
			s.handleNotification(method, completedCh, emit)
		})
	}()

	if err := s.call(ctx, peer, "initialize", map[string]any{
		"clientInfo": map[string]string{"name": "nvpair-codex-worker", "version": Version},
	}); err != nil {
		return s.failedProcess(stdin, peer, waitCh, serveDone, err)
	}
	threadResult, err := s.callResult(ctx, peer, "thread/start", map[string]any{
		"cwd":            cwd,
		"sandbox":        sandboxFor(request.Execution.Sandbox),
		"approvalPolicy": "on-request",
		"ephemeral":      true,
	})
	if err != nil {
		return s.failedProcess(stdin, peer, waitCh, serveDone, err)
	}
	var thread struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(threadResult, &thread); err != nil || thread.Thread.ID == "" {
		return s.failedProcess(stdin, peer, waitCh, serveDone, errors.New("app-server thread/start returned no thread id"))
	}
	turnResult, err := s.callResult(ctx, peer, "turn/start", map[string]any{
		"threadId": thread.Thread.ID,
		"input": []map[string]string{{
			"type": "text",
			"text": turnPrompt(request),
		}},
	})
	if err != nil {
		return s.failedProcess(stdin, peer, waitCh, serveDone, err)
	}
	var turn struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(turnResult, &turn); err != nil || turn.Turn.ID == "" {
		return s.failedProcess(stdin, peer, waitCh, serveDone, errors.New("app-server turn/start returned no turn id"))
	}

	select {
	case <-completedCh:
		s.closeProcess(stdin, peer, serveDone, waitCh)
		return codexprotocol.Handoff{
			Version:   codexprotocol.HandoffVersion,
			TaskID:    request.TaskID,
			AttemptID: request.AttemptID,
			Status:    codexprotocol.StateCompleted,
			Summary:   "turn completed",
		}, nil
	case <-approvalCh:
		s.closeProcess(stdin, peer, serveDone, waitCh)
		return codexprotocol.Handoff{
			Version:   codexprotocol.HandoffVersion,
			TaskID:    request.TaskID,
			AttemptID: request.AttemptID,
			Status:    codexprotocol.StateBlocked,
			Summary:   "approval_required",
		}, nil
	case <-ctx.Done():
		interruptCtx, interruptCancel := context.WithTimeout(context.Background(), time.Second)
		_, _, _ = peer.Call(interruptCtx, "turn/interrupt", mustJSON(map[string]any{
			"threadId": thread.Thread.ID,
			"turnId":   turn.Turn.ID,
		}))
		interruptCancel()
		s.closeProcess(stdin, peer, serveDone, waitCh)
		return codexprotocol.Handoff{}, ctx.Err()
	case err := <-waitCh:
		peer.Close()
		<-serveDone
		return codexprotocol.Handoff{}, fmt.Errorf("app-server exited before completion: %w", err)
	}
}

func (s *AppServerSession) Cancel() {
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *AppServerSession) call(ctx context.Context, peer *jsonrpc.Peer, method string, params any) error {
	_, rpcErr, err := peer.Call(ctx, method, mustJSON(params))
	if err != nil {
		return fmt.Errorf("app-server %s: %w", method, err)
	}
	if rpcErr != nil {
		return fmt.Errorf("app-server %s: %s", method, rpcErr.Message)
	}
	return nil
}

func (s *AppServerSession) callResult(ctx context.Context, peer *jsonrpc.Peer, method string, params any) (json.RawMessage, error) {
	result, rpcErr, err := peer.Call(ctx, method, mustJSON(params))
	if err != nil {
		return nil, fmt.Errorf("app-server %s: %w", method, err)
	}
	if rpcErr != nil {
		return nil, fmt.Errorf("app-server %s: %s", method, rpcErr.Message)
	}
	return result, nil
}

func (s *AppServerSession) handleServerRequest(peer *jsonrpc.Peer, message *jsonrpc.Message, approvalCh chan<- struct{}, emit func(codexprotocol.TaskEvent)) {
	if strings.Contains(message.Method, "requestApproval") || message.Method == "item/tool/requestUserInput" {
		emit(codexprotocol.TaskEvent{State: codexprotocol.StateWaitingApproval, Kind: "approval_required", Metadata: map[string]string{"source": "app-server"}, At: time.Now().UTC()})
		_ = peer.Respond(message.ID, map[string]string{"decision": "decline"})
		select {
		case approvalCh <- struct{}{}:
		default:
		}
		return
	}
	_ = peer.RespondError(message.ID, -32601, "interactive app-server request is not supported")
}

func (s *AppServerSession) handleNotification(method string, completedCh chan<- struct{}, emit func(codexprotocol.TaskEvent)) {
	switch method {
	case "turn/completed":
		emit(codexprotocol.TaskEvent{State: codexprotocol.StateCompleted, Kind: "turn_completed", At: time.Now().UTC()})
		select {
		case completedCh <- struct{}{}:
		default:
		}
	case "item/agentMessage/delta", "item/commandExecution/outputDelta":
		emit(codexprotocol.TaskEvent{State: codexprotocol.StateRunning, Kind: "progress", Metadata: map[string]string{"source": method}, At: time.Now().UTC()})
	case "error":
		emit(codexprotocol.TaskEvent{State: codexprotocol.StateFailed, Kind: "app_server_error", At: time.Now().UTC()})
	}
}

func (s *AppServerSession) failedProcess(stdin io.WriteCloser, peer *jsonrpc.Peer, waitCh <-chan error, serveDone <-chan struct{}, err error) (codexprotocol.Handoff, error) {
	s.closeProcess(stdin, peer, serveDone, waitCh)
	return codexprotocol.Handoff{}, err
}

func (s *AppServerSession) closeProcess(stdin io.WriteCloser, peer *jsonrpc.Peer, serveDone <-chan struct{}, waitCh <-chan error) {
	peer.Close()
	_ = stdin.Close()
	select {
	case <-serveDone:
	case <-time.After(time.Second):
	}
	select {
	case <-waitCh:
	case <-time.After(time.Second):
	}
}

func appServerCWD(path string) (string, error) {
	if path == "local" || path == "." {
		return os.Getwd()
	}
	if !filepath.IsAbs(path) {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return "", err
		}
		path = absolute
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("app-server cwd is not a directory")
	}
	return path, nil
}

func sandboxFor(sandbox string) string {
	if sandbox == "workspace-write" {
		return "workspace-write"
	}
	return "read-only"
}

func turnPrompt(request codexprotocol.TaskRequest) string {
	return "Objective: " + request.Context.Objective + "\nRequired evidence: " + strings.Join(request.Context.RequiredEvidence, "; ")
}

func mustJSON(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}

type readWriter struct {
	io.Reader
	io.Writer
}
