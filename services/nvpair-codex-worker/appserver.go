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

	mu            sync.Mutex
	peer          *jsonrpc.Peer
	process       *os.Process
	cancelRequest chan struct{}
	cancelOnce    *sync.Once
}

func (s *AppServerSession) Run(ctx context.Context, request codexprotocol.TaskRequest, emit func(codexprotocol.TaskEvent)) (codexprotocol.Handoff, error) {
	return s.RunWithChild(ctx, request, emit, nil)
}

func (s *AppServerSession) RunWithChild(ctx context.Context, request codexprotocol.TaskRequest, emit func(codexprotocol.TaskEvent), onChild func(string) error) (codexprotocol.Handoff, error) {
	cwd, err := appServerCWD(request.Workspace.Path)
	if err != nil {
		return codexprotocol.Handoff{}, err
	}
	s.mu.Lock()
	s.cancelRequest = make(chan struct{}, 1)
	cancelRequest := s.cancelRequest
	s.cancelOnce = &sync.Once{}
	s.mu.Unlock()
	callCtx, cancelCalls := context.WithCancel(ctx)
	defer cancelCalls()
	go func() {
		select {
		case <-cancelRequest:
			cancelCalls()
		case <-callCtx.Done():
		}
	}()
	defer func() {
		s.mu.Lock()
		s.peer = nil
		s.process = nil
		s.cancelRequest = nil
		s.cancelOnce = nil
		s.mu.Unlock()
	}()

	cmd := exec.Command(s.binary, "app-server", "--listen", "stdio://")
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
	s.mu.Lock()
	s.process = cmd.Process
	s.mu.Unlock()
	if onChild != nil {
		if err := onChild(fmt.Sprintf("pid:%d", cmd.Process.Pid)); err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return codexprotocol.Handoff{}, fmt.Errorf("persist app-server child identity: %w", err)
		}
	}
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	peer := jsonrpc.NewPeer(jsonrpc.NewCodec(&readWriter{Reader: stdout, Writer: stdin}))
	s.mu.Lock()
	s.peer = peer
	s.mu.Unlock()
	approvalCh := make(chan struct{}, 1)
	state := &appServerRunState{completedCh: make(chan turnResult, 1)}
	serveDone := make(chan struct{})
	go func() {
		defer close(serveDone)
		peer.Serve(func(message *jsonrpc.Message) {
			s.handleServerRequest(peer, message, approvalCh, emit)
		}, func(method string, params json.RawMessage) {
			s.handleNotification(method, params, state, emit)
		})
	}()

	if err := s.call(callCtx, peer, "initialize", map[string]any{
		"clientInfo": map[string]string{"name": "nvpair-codex-worker", "version": Version},
	}); err != nil {
		return s.failedProcess(cmd, stdin, peer, waitCh, serveDone, err)
	}
	if err := peer.Notify("initialized", map[string]any{}); err != nil {
		return s.failedProcess(cmd, stdin, peer, waitCh, serveDone, fmt.Errorf("app-server initialized: %w", err))
	}
	threadResult, err := s.callResult(callCtx, peer, "thread/start", map[string]any{
		"cwd":            cwd,
		"sandbox":        sandboxFor(request.Execution.Sandbox),
		"approvalPolicy": "on-request",
		"ephemeral":      true,
	})
	if err != nil {
		return s.failedProcess(cmd, stdin, peer, waitCh, serveDone, err)
	}
	var thread struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(threadResult, &thread); err != nil || thread.Thread.ID == "" {
		return s.failedProcess(cmd, stdin, peer, waitCh, serveDone, errors.New("app-server thread/start returned no thread id"))
	}
	state.setThreadID(thread.Thread.ID)
	turnResult, err := s.callResult(callCtx, peer, "turn/start", map[string]any{
		"threadId": thread.Thread.ID,
		"input": []map[string]string{{
			"type": "text",
			"text": turnPrompt(request),
		}},
		"cwd":            cwd,
		"approvalPolicy": "on-request",
		"sandboxPolicy":  sandboxPolicyFor(request.Execution.Sandbox, cwd),
		"outputSchema":   handoffSchema(),
	})
	if err != nil {
		return s.failedProcess(cmd, stdin, peer, waitCh, serveDone, err)
	}
	var turn struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(turnResult, &turn); err != nil || turn.Turn.ID == "" {
		return s.failedProcess(cmd, stdin, peer, waitCh, serveDone, errors.New("app-server turn/start returned no turn id"))
	}
	state.setTurnID(turn.Turn.ID)

	select {
	case completed := <-state.completedCh:
		s.closeProcess(cmd, stdin, peer, serveDone, waitCh)
		if completed.err != nil {
			return codexprotocol.Handoff{}, completed.err
		}
		var handoff codexprotocol.Handoff
		if err := json.Unmarshal([]byte(completed.message), &handoff); err != nil {
			return codexprotocol.Handoff{}, fmt.Errorf("app-server final message is not a handoff: %w", err)
		}
		if err := handoff.Validate(request.TaskID, request.AttemptID); err != nil {
			return codexprotocol.Handoff{}, fmt.Errorf("validate app-server handoff: %w", err)
		}
		return handoff, nil
	case <-approvalCh:
		s.closeProcess(cmd, stdin, peer, serveDone, waitCh)
		return codexprotocol.Handoff{
			Version:   codexprotocol.HandoffVersion,
			TaskID:    request.TaskID,
			AttemptID: request.AttemptID,
			Status:    codexprotocol.StateBlocked,
			Summary:   "approval_required",
		}, nil
	case <-ctx.Done():
		s.interruptAndClose(ctx.Err(), cmd, stdin, peer, serveDone, waitCh, thread.Thread.ID, turn.Turn.ID)
		return codexprotocol.Handoff{}, ctx.Err()
	case <-cancelRequest:
		s.interruptAndClose(context.Canceled, cmd, stdin, peer, serveDone, waitCh, thread.Thread.ID, turn.Turn.ID)
		return codexprotocol.Handoff{}, context.Canceled
	case err := <-waitCh:
		peer.Close()
		<-serveDone
		return codexprotocol.Handoff{}, fmt.Errorf("app-server exited before completion: %w", err)
	}
}

func (s *AppServerSession) Cancel() {
	s.mu.Lock()
	cancel := s.cancelRequest
	once := s.cancelOnce
	s.mu.Unlock()
	if cancel != nil && once != nil {
		once.Do(func() { close(cancel) })
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

type appServerRunState struct {
	mu          sync.Mutex
	threadID    string
	turnID      string
	public      strings.Builder
	completedCh chan turnResult
}

func (s *appServerRunState) setThreadID(id string) {
	s.mu.Lock()
	s.threadID = id
	s.mu.Unlock()
}

func (s *appServerRunState) setTurnID(id string) {
	s.mu.Lock()
	s.turnID = id
	s.mu.Unlock()
}

func (s *appServerRunState) turnIDValue() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.turnID
}

func (s *appServerRunState) ids() (string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.threadID, s.turnID
}

type turnResult struct {
	message string
	err     error
}

func (s *AppServerSession) handleNotification(method string, params json.RawMessage, state *appServerRunState, emit func(codexprotocol.TaskEvent)) {
	currentThreadID, currentTurnID := state.ids()
	switch method {
	case "turn/completed":
		emit(codexprotocol.TaskEvent{State: codexprotocol.StateCompleted, Kind: "turn_completed", At: time.Now().UTC()})
		var completed struct {
			ThreadID string `json:"threadId"`
			TurnID   string `json:"turnId"`
			Turn     struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"turn"`
		}
		if json.Unmarshal(params, &completed) != nil {
			state.completedCh <- turnResult{err: errors.New("decode turn/completed notification")}
			return
		}
		turnID := completed.TurnID
		status := "completed"
		if completed.Turn.ID != "" {
			turnID = completed.Turn.ID
			status = completed.Turn.Status
		}
		if completed.ThreadID != "" && completed.ThreadID != currentThreadID {
			return
		}
		if turnID != "" && turnID != currentTurnID {
			return
		}
		if status == "failed" || status == "interrupted" {
			state.completedCh <- turnResult{err: fmt.Errorf("app-server turn %s", status)}
			return
		}
		state.completedCh <- turnResult{message: strings.TrimSpace(state.public.String())}
	case "item/agentMessage/delta", "item/commandExecution/outputDelta":
		if method == "item/agentMessage/delta" {
			var delta struct {
				ThreadID string `json:"threadId"`
				TurnID   string `json:"turnId"`
				Delta    string `json:"delta"`
			}
			if json.Unmarshal(params, &delta) == nil && (delta.ThreadID == "" || delta.ThreadID == currentThreadID) && (delta.TurnID == "" || delta.TurnID == currentTurnID) {
				if state.public.Len()+len(delta.Delta) <= codexprotocol.MaxHandoffBytes {
					state.public.WriteString(delta.Delta)
				}
			}
		}
		emit(codexprotocol.TaskEvent{State: codexprotocol.StateRunning, Kind: "progress", Metadata: map[string]string{"source": method}, At: time.Now().UTC()})
	case "error":
		emit(codexprotocol.TaskEvent{State: codexprotocol.StateFailed, Kind: "app_server_error", At: time.Now().UTC()})
		state.completedCh <- turnResult{err: errors.New("app-server emitted an error")}
	}
}

func (s *AppServerSession) failedProcess(cmd *exec.Cmd, stdin io.WriteCloser, peer *jsonrpc.Peer, waitCh <-chan error, serveDone <-chan struct{}, err error) (codexprotocol.Handoff, error) {
	s.closeTransport(stdin, peer, serveDone)
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	select {
	case <-waitCh:
	case <-time.After(time.Second):
	}
	return codexprotocol.Handoff{}, err
}

func (s *AppServerSession) closeProcess(cmd *exec.Cmd, stdin io.WriteCloser, peer *jsonrpc.Peer, serveDone <-chan struct{}, waitCh <-chan error) {
	s.closeTransport(stdin, peer, serveDone)
	select {
	case <-waitCh:
	default:
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		select {
		case <-waitCh:
		case <-time.After(time.Second):
		}
	}
}

func (s *AppServerSession) closeTransport(stdin io.WriteCloser, peer *jsonrpc.Peer, serveDone <-chan struct{}) {
	peer.Close()
	_ = stdin.Close()
	select {
	case <-serveDone:
	case <-time.After(time.Second):
	}
}

func (s *AppServerSession) interruptAndClose(reason error, cmd *exec.Cmd, stdin io.WriteCloser, peer *jsonrpc.Peer, serveDone <-chan struct{}, waitCh <-chan error, threadID, turnID string) {
	interruptCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	_, _, _ = peer.Call(interruptCtx, "turn/interrupt", mustJSON(map[string]any{
		"threadId": threadID,
		"turnId":   turnID,
	}))
	cancel()
	s.closeProcess(cmd, stdin, peer, serveDone, waitCh)
	_ = reason
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

func sandboxPolicyFor(sandbox, cwd string) map[string]any {
	if sandbox == "workspace-write" {
		return map[string]any{"type": "workspaceWrite", "writableRoots": []string{cwd}}
	}
	return map[string]any{"type": "readOnly", "networkAccess": false}
}

func turnPrompt(request codexprotocol.TaskRequest) string {
	return "You are a delegated Worker. Return only the JSON handoff object described below; do not include markdown or commentary.\n" +
		"Task ID: " + request.TaskID + "\nAttempt ID: " + request.AttemptID + "\n" +
		"Objective: " + request.Context.Objective +
		"\nRelevant decisions: " + strings.Join(request.Context.RelevantDecisions, "; ") +
		"\nConstraints: " + strings.Join(request.Context.Constraints, "; ") +
		"\nRequired evidence: " + strings.Join(request.Context.RequiredEvidence, "; ") +
		"\nHandoff status must be one of completed, blocked, failed, cancelled, or lost."
}

func handoffSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"version": map[string]any{"type": "integer"}, "taskId": map[string]any{"type": "string"}, "attemptId": map[string]any{"type": "string"},
			"status": map[string]any{"type": "string", "enum": []string{"completed", "blocked", "failed", "cancelled", "lost"}}, "summary": map[string]any{"type": "string"},
			"findings": map[string]any{"type": "array"}, "changes": map[string]any{"type": "array"}, "verification": map[string]any{"type": "array"}, "artifacts": map[string]any{"type": "array"}, "recommendedNext": map[string]any{"type": "string"},
		},
		"required": []string{"version", "taskId", "attemptId", "status", "summary"},
	}
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
