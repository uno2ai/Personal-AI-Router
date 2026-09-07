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
	"nvpair-shared/protectedfile"
)

// Version is replaced by services/build.sh and services/build.bat at release
// build time. Development binaries intentionally report dev.
var Version = "dev"

// SupportedAppServerVersion is the semantic adapter contract, not the CLI's
// human-facing version string. The adapter fails closed when the initialize
// response does not negotiate this contract.
const SupportedAppServerVersion = "codex-app-server-v1"

type AppServerFactory struct {
	binary       string
	isolatedHome *isolatedCodexHome
}

type isolatedCodexHome struct {
	path string
	mu   sync.Mutex
}

func NewAppServerFactory(binary string, stateRoot ...string) AppServerFactory {
	if binary == "" {
		binary = "codex"
	}
	factory := AppServerFactory{binary: binary}
	if len(stateRoot) > 0 && strings.TrimSpace(stateRoot[0]) != "" {
		factory.isolatedHome = &isolatedCodexHome{path: filepath.Join(stateRoot[0], "child-codex-home")}
	}
	return factory
}

// appServerArguments prevents a delegated Worker child from recursively
// loading Main Codex's MCP servers, apps, plugins, or hooks. The child still
// uses the user's normal Codex authentication, model settings, and native
// app-server protocol; only extension surfaces that could route execution back
// into Main are disabled at the command-line layer.
func appServerArguments() []string {
	return []string{
		"app-server",
		"--disable", "plugins",
		"--disable", "hooks",
		"--disable", "apps",
		"--disable", "enable_mcp_apps",
		"--disable", "skill_mcp_dependency_install",
		"--listen", "stdio://",
	}
}

func (f AppServerFactory) command() (*exec.Cmd, error) {
	binary, err := resolveCodexExecutable(f.binary)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(binary, appServerArguments()...)
	if f.isolatedHome == nil {
		return cmd, nil
	}
	if err := f.isolatedHome.prepare(); err != nil {
		return nil, err
	}
	cmd.Env = replaceEnvironment(os.Environ(), map[string]string{
		"CODEX_HOME":        f.isolatedHome.path,
		"CODEX_SQLITE_HOME": filepath.Join(f.isolatedHome.path, "sqlite"),
	})
	return cmd, nil
}

func (h *isolatedCodexHome) prepare() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := protectedfile.EnsureDir(h.path); err != nil {
		return fmt.Errorf("create isolated Codex home: %w", err)
	}
	if err := protectedfile.Check(h.path); err != nil {
		return fmt.Errorf("protect isolated Codex home: %w", err)
	}
	if err := protectedfile.EnsureDir(filepath.Join(h.path, "sqlite")); err != nil {
		return fmt.Errorf("create isolated Codex sqlite home: %w", err)
	}
	return syncCodexAuth(h.path)
}

func syncCodexAuth(isolatedHome string) error {
	sourceHome := strings.TrimSpace(os.Getenv("CODEX_HOME"))
	if sourceHome == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("resolve Codex authentication home: %w", err)
		}
		sourceHome = filepath.Join(userHome, ".codex")
	}
	source := filepath.Join(sourceHome, "auth.json")
	destination := filepath.Join(isolatedHome, "auth.json")
	sourceInfo, err := os.Stat(source)
	if errors.Is(err, os.ErrNotExist) {
		if removeErr := os.Remove(destination); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return fmt.Errorf("remove stale isolated Codex authentication: %w", removeErr)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect Codex authentication: %w", err)
	}
	if destinationInfo, statErr := os.Stat(destination); statErr == nil && !sourceInfo.ModTime().After(destinationInfo.ModTime()) && protectedfile.Check(destination) == nil {
		return nil
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return fmt.Errorf("read Codex authentication: %w", err)
	}
	temporary, err := os.CreateTemp(isolatedHome, ".auth-*.tmp")
	if err != nil {
		return fmt.Errorf("stage isolated Codex authentication: %w", err)
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := protectedfile.Protect(temporary.Name()); err != nil {
		return fmt.Errorf("protect staged Codex authentication: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		return fmt.Errorf("write staged Codex authentication: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync staged Codex authentication: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close staged Codex authentication: %w", err)
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		return fmt.Errorf("install isolated Codex authentication: %w", err)
	}
	committed = true
	return nil
}

func replaceEnvironment(environment []string, replacements map[string]string) []string {
	result := make([]string, 0, len(environment)+len(replacements))
	for _, entry := range environment {
		key, _, found := strings.Cut(entry, "=")
		if found {
			if _, replaced := replacements[key]; replaced {
				continue
			}
		}
		result = append(result, entry)
	}
	for key, value := range replacements {
		result = append(result, key+"="+value)
	}
	return result
}

// Probe starts the configured native app-server long enough to negotiate the
// initialize contract. This is deliberately separate from a task run: a
// successful listener bind does not prove that the executable, account, or
// app-server protocol is usable.
func (f AppServerFactory) Probe(ctx context.Context, cwd string) error {
	resolvedCWD, err := appServerCWD(cwd)
	if err != nil {
		return err
	}
	cmd, err := f.command()
	if err != nil {
		return fmt.Errorf("prepare isolated app-server configuration: %w", err)
	}
	cleanupChild, verifyChild, err := prepareChildProcess(cmd, resolvedCWD)
	if err != nil {
		return fmt.Errorf("prepare app-server probe: %w", err)
	}
	cmd.Stderr = io.Discard
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cleanupChild()
		return fmt.Errorf("open app-server probe stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		cleanupChild()
		return fmt.Errorf("open app-server probe stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		cleanupChild()
		return fmt.Errorf("start app-server probe: %w", err)
	}
	if err := verifyChild(); err != nil {
		_ = terminateAndWait(cmd)
		cleanupChild()
		return fmt.Errorf("verify app-server probe working directory: %w", err)
	}
	cleanupChild()
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	probeWriter := &probeWriteSignal{Writer: stdin, written: make(chan struct{})}
	peer := jsonrpc.NewPeer(jsonrpc.NewCodecAllowMissingJSONRPCVersion(&readWriter{Reader: stdout, Writer: probeWriter}))
	serveDone := make(chan struct{})
	go func() {
		defer close(serveDone)
		peer.Serve(nil, nil)
	}()
	cleanup := func(probeErr error) error {
		session := &AppServerSession{}
		if closeErr := session.closeProcess(cmd, stdin, peer, serveDone, waitCh); closeErr != nil {
			if probeErr == nil {
				return closeErr
			}
			return fmt.Errorf("%w (probe cleanup: %v)", probeErr, closeErr)
		}
		return probeErr
	}
	resultCh := make(chan struct {
		result json.RawMessage
		rpcErr *jsonrpc.RPCError
		err    error
	}, 1)
	go func() {
		result, rpcErr, err := peer.Call(ctx, "initialize", mustJSON(map[string]any{
			"clientInfo": map[string]string{"name": "nvpair-codex-worker-probe", "version": Version},
		}))
		resultCh <- struct {
			result json.RawMessage
			rpcErr *jsonrpc.RPCError
			err    error
		}{result: result, rpcErr: rpcErr, err: err}
	}()
	// Some native Codex CLI builds do not flush the initialize result until the
	// lifecycle notification is already queued on the same stdio transport.
	// The notification is valid only after the initialize request has physically
	// reached the child. Waiting on the writer barrier prevents a scheduler race
	// from putting initialized before initialize on the wire.
	select {
	case <-probeWriter.written:
	case call := <-resultCh:
		return cleanup(formatProbeInitializeResult(call.result, call.rpcErr, call.err))
	case <-ctx.Done():
		return cleanup(ctx.Err())
	}
	if err := peer.Notify("initialized", map[string]any{}); err != nil {
		return cleanup(fmt.Errorf("app-server probe initialized: %w", err))
	}
	if err := verifyNoMCPConfiguration(ctx, peer, resolvedCWD); err != nil {
		return cleanup(err)
	}
	call := <-resultCh
	result, rpcErr, err := call.result, call.rpcErr, call.err
	if err := formatProbeInitializeResult(result, rpcErr, err); err != nil {
		return cleanup(err)
	}
	return cleanup(nil)
}

func formatProbeInitializeResult(result json.RawMessage, rpcErr *jsonrpc.RPCError, err error) error {
	if err != nil {
		return fmt.Errorf("app-server probe initialize: %w", err)
	}
	if rpcErr != nil {
		return fmt.Errorf("app-server probe initialize: %s", rpcErr.Message)
	}
	return validateAppServerInitialize(result)
}

func verifyNoMCPConfiguration(ctx context.Context, peer *jsonrpc.Peer, cwd string) error {
	result, rpcErr, err := peer.Call(ctx, "config/read", mustJSON(map[string]any{
		"cwd":           cwd,
		"includeLayers": true,
	}))
	if err != nil {
		return fmt.Errorf("verify app-server MCP isolation: %w", err)
	}
	if rpcErr != nil {
		return fmt.Errorf("verify app-server MCP isolation: %s", rpcErr.Message)
	}
	var response struct {
		Layers []struct {
			Config         json.RawMessage `json:"config"`
			DisabledReason *string         `json:"disabledReason"`
		} `json:"layers"`
	}
	if err := json.Unmarshal(result, &response); err != nil {
		return fmt.Errorf("verify app-server MCP isolation response: %w", err)
	}
	if response.Layers == nil {
		return errors.New("verify app-server MCP isolation response: config layers are missing")
	}
	for _, layer := range response.Layers {
		if layer.DisabledReason != nil {
			continue
		}
		var config any
		if err := json.Unmarshal(layer.Config, &config); err != nil {
			return fmt.Errorf("verify app-server MCP isolation layer: %w", err)
		}
		if containsConfiguredMCP(config) {
			return errors.New("app-server MCP configuration is present in an active config layer")
		}
	}
	return nil
}

func containsConfiguredMCP(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, nested := range typed {
			if (key == "mcp_servers" || key == "mcpServers") && !emptyConfigurationValue(nested) {
				return true
			}
			if containsConfiguredMCP(nested) {
				return true
			}
		}
	case []any:
		for _, nested := range typed {
			if containsConfiguredMCP(nested) {
				return true
			}
		}
	}
	return false
}

func emptyConfigurationValue(value any) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case map[string]any:
		return len(typed) == 0
	case []any:
		return len(typed) == 0
	default:
		return false
	}
}

type probeWriteSignal struct {
	io.Writer
	written chan struct{}
	once    sync.Once
}

func (w *probeWriteSignal) Write(data []byte) (int, error) {
	n, err := w.Writer.Write(data)
	if n == len(data) {
		w.once.Do(func() { close(w.written) })
	}
	return n, err
}

func (f AppServerFactory) New() *AppServerSession {
	return &AppServerSession{factory: f}
}

type AppServerSession struct {
	factory AppServerFactory

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
	return s.runWithThread(ctx, request, emit, onChild, nil, "")
}

func (s *AppServerSession) RunWithChildAndThread(ctx context.Context, request codexprotocol.TaskRequest, emit func(codexprotocol.TaskEvent), onChild func(string) error, onThread func(string) error) (codexprotocol.Handoff, error) {
	return s.runWithThread(ctx, request, emit, onChild, onThread, "")
}

// RunFollowUpWithChild resumes a previously persisted native thread in a new
// local app-server child. The task/lease fencing is owned by the Worker store;
// this method only performs the native thread/resume + turn/start exchange.
func (s *AppServerSession) RunFollowUpWithChild(ctx context.Context, request codexprotocol.TaskRequest, threadID string, emit func(codexprotocol.TaskEvent), onChild func(string) error) (codexprotocol.Handoff, error) {
	if strings.TrimSpace(threadID) == "" {
		return codexprotocol.Handoff{}, errors.New("app-server follow-up requires a persisted thread id")
	}
	return s.runWithThread(ctx, request, emit, onChild, nil, threadID)
}

func (s *AppServerSession) runWithThread(ctx context.Context, request codexprotocol.TaskRequest, emit func(codexprotocol.TaskEvent), onChild func(string) error, onThread func(string) error, resumeThreadID string) (codexprotocol.Handoff, error) {
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

	cmd, err := s.factory.command()
	if err != nil {
		return codexprotocol.Handoff{}, fmt.Errorf("prepare isolated app-server configuration: %w", err)
	}
	cleanupChild, verifyChild, err := prepareChildProcess(cmd, cwd)
	if err != nil {
		return codexprotocol.Handoff{}, err
	}
	cmd.Stderr = io.Discard
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cleanupChild()
		return codexprotocol.Handoff{}, fmt.Errorf("open app-server stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		cleanupChild()
		return codexprotocol.Handoff{}, fmt.Errorf("open app-server stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		cleanupChild()
		return codexprotocol.Handoff{}, fmt.Errorf("start app-server: %w", err)
	}
	if err := verifyChild(); err != nil {
		_ = terminateAndWait(cmd)
		cleanupChild()
		return codexprotocol.Handoff{}, fmt.Errorf("verify app-server working directory: %w", err)
	}
	cleanupChild()
	s.mu.Lock()
	s.process = cmd.Process
	s.mu.Unlock()
	if onChild != nil {
		if err := onChild(fmt.Sprintf("pid:%d", cmd.Process.Pid)); err != nil {
			_ = terminateAndWait(cmd)
			return codexprotocol.Handoff{}, fmt.Errorf("persist app-server child identity: %w", err)
		}
	}
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	peer := jsonrpc.NewPeer(jsonrpc.NewCodecAllowMissingJSONRPCVersion(&readWriter{Reader: stdout, Writer: stdin}))
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

	initializeResult, err := s.callResult(callCtx, peer, "initialize", map[string]any{
		"clientInfo": map[string]string{"name": "nvpair-codex-worker", "version": Version},
	})
	if err != nil {
		return s.failedProcess(cmd, stdin, peer, waitCh, serveDone, err)
	}
	if err := validateAppServerInitialize(initializeResult); err != nil {
		return s.failedProcess(cmd, stdin, peer, waitCh, serveDone, err)
	}
	if err := peer.Notify("initialized", map[string]any{}); err != nil {
		return s.failedProcess(cmd, stdin, peer, waitCh, serveDone, fmt.Errorf("app-server initialized: %w", err))
	}
	if err := verifyNoMCPConfiguration(callCtx, peer, cwd); err != nil {
		return s.failedProcess(cmd, stdin, peer, waitCh, serveDone, err)
	}
	var threadResult json.RawMessage
	if resumeThreadID == "" {
		threadResult, err = s.callResult(callCtx, peer, "thread/start", map[string]any{
			"cwd":            cwd,
			"sandbox":        sandboxFor(request.Execution.Sandbox),
			"approvalPolicy": "on-request",
			"ephemeral":      false,
		})
	} else {
		threadResult, err = s.callResult(callCtx, peer, "thread/resume", map[string]any{
			"threadId":       resumeThreadID,
			"cwd":            cwd,
			"sandbox":        sandboxFor(request.Execution.Sandbox),
			"approvalPolicy": "on-request",
			"excludeTurns":   true,
		})
	}
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
	if resumeThreadID != "" && thread.Thread.ID != resumeThreadID {
		return s.failedProcess(cmd, stdin, peer, waitCh, serveDone, errors.New("app-server thread/resume returned a different thread id"))
	}
	state.setThreadID(thread.Thread.ID)
	if onThread != nil {
		if err := onThread(thread.Thread.ID); err != nil {
			return s.failedProcess(cmd, stdin, peer, waitCh, serveDone, fmt.Errorf("persist app-server thread identity: %w", err))
		}
	}
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
	if pending := state.setTurnIDAndFlush(turn.Turn.ID); pending != nil {
		state.deliverCompletion(*pending)
	}

	select {
	case completed := <-state.completedCh:
		if err := s.closeProcess(cmd, stdin, peer, serveDone, waitCh); err != nil {
			return codexprotocol.Handoff{}, err
		}
		if completed.err != nil {
			return codexprotocol.Handoff{}, completed.err
		}
		handoff, err := decodeHandoffMessage(completed.message, request.TaskID, request.AttemptID)
		if err != nil {
			return codexprotocol.Handoff{}, err
		}
		return handoff, nil
	case <-approvalCh:
		if err := s.closeProcess(cmd, stdin, peer, serveDone, waitCh); err != nil {
			return codexprotocol.Handoff{}, err
		}
		return codexprotocol.Handoff{
			Version:   codexprotocol.HandoffVersion,
			TaskID:    request.TaskID,
			AttemptID: request.AttemptID,
			Status:    codexprotocol.StateBlocked,
			Summary:   "approval_required",
		}, nil
	case <-ctx.Done():
		return codexprotocol.Handoff{}, s.interruptAndClose(ctx.Err(), cmd, stdin, peer, serveDone, waitCh, thread.Thread.ID, turn.Turn.ID)
	case <-cancelRequest:
		return codexprotocol.Handoff{}, s.interruptAndClose(context.Canceled, cmd, stdin, peer, serveDone, waitCh, thread.Thread.ID, turn.Turn.ID)
	case err := <-waitCh:
		terminationErr := terminateChild(cmd)
		peer.Close()
		<-serveDone
		releaseChildProcess(cmd)
		if terminationErr != nil && !errors.Is(terminationErr, os.ErrProcessDone) {
			return codexprotocol.Handoff{}, fmt.Errorf("app-server exited before completion and child cleanup failed: %w", terminationErr)
		}
		return codexprotocol.Handoff{}, fmt.Errorf("app-server exited before completion: %w", err)
	}
}

// decodeHandoffMessage accepts a verified JSON handoff embedded in otherwise
// conversational output. Native Codex versions can occasionally prepend a
// short progress sentence even when an output schema is supplied. Every
// candidate remains subject to the task/attempt fence and Handoff.Validate;
// unverified text is never returned as a task result.
func decodeHandoffMessage(message, taskID, attemptID string) (codexprotocol.Handoff, error) {
	if len(message) > codexprotocol.MaxHandoffBytes {
		return codexprotocol.Handoff{}, fmt.Errorf("app-server final message exceeds %d bytes", codexprotocol.MaxHandoffBytes)
	}
	for offset := 0; offset < len(message); {
		relative := strings.IndexByte(message[offset:], '{')
		if relative < 0 {
			break
		}
		start := offset + relative
		decoder := json.NewDecoder(strings.NewReader(message[start:]))
		var handoff codexprotocol.Handoff
		if err := decoder.Decode(&handoff); err == nil {
			if err := handoff.Validate(taskID, attemptID); err == nil {
				return handoff, nil
			}
		}
		offset = start + 1
	}
	return codexprotocol.Handoff{}, errors.New("app-server final message is not a verified handoff")
}

// validateAppServerInitialize is the adapter's fail-closed compatibility
// boundary. The currently installed native app-server does not expose a
// separate protocol-version field; its initialize result identifies the
// implementation through userAgent. An empty or non-Codex result is therefore
// incompatible instead of being treated as a best-effort implementation.
func validateAppServerInitialize(raw json.RawMessage) error {
	var result struct {
		UserAgent      string `json:"userAgent"`
		PlatformFamily string `json:"platformFamily"`
		PlatformOS     string `json:"platformOs"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return fmt.Errorf("app-server initialize returned invalid result: %w", err)
	}
	if !strings.HasPrefix(strings.TrimSpace(result.UserAgent), "Codex ") {
		return errors.New("unsupported app-server initialize response")
	}
	if strings.TrimSpace(result.PlatformFamily) == "" || strings.TrimSpace(result.PlatformOS) == "" {
		return errors.New("unsupported app-server initialize platform metadata")
	}
	return nil
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
	mu                sync.Mutex
	threadID          string
	turnID            string
	public            strings.Builder
	pendingDeltas     []pendingDelta
	pendingCompletion *turnCompletion
	completionSent    bool
	completedCh       chan turnResult
}

type pendingDelta struct {
	threadID string
	turnID   string
	delta    string
}

type turnCompletion struct {
	threadID string
	turnID   string
	status   string
}

func (s *appServerRunState) setThreadID(id string) {
	s.mu.Lock()
	s.threadID = id
	s.mu.Unlock()
}

func (s *appServerRunState) setTurnIDAndFlush(id string) *turnResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.turnID = id
	for _, delta := range s.pendingDeltas {
		if s.matchesLocked(delta.threadID, delta.turnID) && s.public.Len()+len(delta.delta) <= codexprotocol.MaxHandoffBytes {
			s.public.WriteString(delta.delta)
		}
	}
	s.pendingDeltas = nil
	if !s.completionSent && s.pendingCompletion != nil && s.matchesLocked(s.pendingCompletion.threadID, s.pendingCompletion.turnID) {
		completion := *s.pendingCompletion
		s.pendingCompletion = nil
		return s.finalizeCompletionLocked(completion)
	}
	return nil
}

func (s *appServerRunState) matchesLocked(threadID, turnID string) bool {
	return threadID == s.threadID && turnID == s.turnID
}

func (s *appServerRunState) acceptDelta(delta pendingDelta) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if delta.threadID == "" || delta.turnID == "" || s.completionSent {
		return
	}
	if s.threadID == "" || s.turnID == "" {
		s.pendingDeltas = append(s.pendingDeltas, delta)
		return
	}
	if s.matchesLocked(delta.threadID, delta.turnID) && s.public.Len()+len(delta.delta) <= codexprotocol.MaxHandoffBytes {
		s.public.WriteString(delta.delta)
	}
}

func (s *appServerRunState) acceptCompletion(completion turnCompletion) *turnResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	if completion.threadID == "" || completion.turnID == "" || !validTurnCompletionStatus(completion.status) || s.completionSent {
		return nil
	}
	if s.threadID == "" || s.turnID == "" {
		s.pendingCompletion = &completion
		return nil
	}
	if !s.matchesLocked(completion.threadID, completion.turnID) {
		return nil
	}
	return s.finalizeCompletionLocked(completion)
}

func (s *appServerRunState) finalizeCompletionLocked(completion turnCompletion) *turnResult {
	s.completionSent = true
	if completion.status == "failed" || completion.status == "interrupted" {
		return &turnResult{err: fmt.Errorf("app-server turn %s", completion.status)}
	}
	return &turnResult{message: strings.TrimSpace(s.public.String())}
}

func (s *appServerRunState) fail(err error) {
	s.mu.Lock()
	if s.completionSent {
		s.mu.Unlock()
		return
	}
	s.completionSent = true
	s.mu.Unlock()
	s.deliverCompletion(turnResult{err: err})
}

func (s *appServerRunState) deliverCompletion(result turnResult) {
	select {
	case s.completedCh <- result:
	default:
	}
}

func validTurnCompletionStatus(status string) bool {
	switch status {
	case "completed", "failed", "interrupted":
		return true
	default:
		return false
	}
}

type turnResult struct {
	message string
	err     error
}

func (s *AppServerSession) handleNotification(method string, params json.RawMessage, state *appServerRunState, emit func(codexprotocol.TaskEvent)) {
	switch method {
	case "turn/completed":
		var completed struct {
			ThreadID string `json:"threadId"`
			Turn     struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"turn"`
		}
		if json.Unmarshal(params, &completed) != nil || completed.ThreadID == "" || completed.Turn.ID == "" || !validTurnCompletionStatus(completed.Turn.Status) {
			state.fail(errors.New("invalid turn/completed notification"))
			return
		}
		if result := state.acceptCompletion(turnCompletion{threadID: completed.ThreadID, turnID: completed.Turn.ID, status: completed.Turn.Status}); result != nil {
			emit(codexprotocol.TaskEvent{State: codexprotocol.StateCompleted, Kind: "turn_completed", At: time.Now().UTC()})
			state.deliverCompletion(*result)
		}
	case "item/agentMessage/delta", "item/commandExecution/outputDelta":
		if method == "item/agentMessage/delta" {
			var delta struct {
				ThreadID string `json:"threadId"`
				TurnID   string `json:"turnId"`
				Delta    string `json:"delta"`
			}
			if json.Unmarshal(params, &delta) == nil {
				state.acceptDelta(pendingDelta{threadID: delta.ThreadID, turnID: delta.TurnID, delta: delta.Delta})
			}
		}
		emit(codexprotocol.TaskEvent{State: codexprotocol.StateRunning, Kind: "progress", Metadata: map[string]string{"source": method}, At: time.Now().UTC()})
	case "error":
		emit(codexprotocol.TaskEvent{State: codexprotocol.StateFailed, Kind: "app_server_error", At: time.Now().UTC()})
		state.fail(errors.New("app-server emitted an error"))
	}
}

func (s *AppServerSession) failedProcess(cmd *exec.Cmd, stdin io.WriteCloser, peer *jsonrpc.Peer, waitCh <-chan error, serveDone <-chan struct{}, err error) (codexprotocol.Handoff, error) {
	if closeErr := s.closeProcess(cmd, stdin, peer, serveDone, waitCh); closeErr != nil {
		return codexprotocol.Handoff{}, fmt.Errorf("%w (child cleanup: %v)", err, closeErr)
	}
	return codexprotocol.Handoff{}, err
}

func terminateAndWait(cmd *exec.Cmd) error {
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	if err := terminateChild(cmd); err != nil && !errors.Is(err, os.ErrProcessDone) {
		releaseChildProcess(cmd)
		return err
	}
	select {
	case <-waitCh:
		return nil
	case <-time.After(2 * time.Second):
		return errors.New("app-server process tree did not terminate")
	}
}

func (s *AppServerSession) closeProcess(cmd *exec.Cmd, stdin io.WriteCloser, peer *jsonrpc.Peer, serveDone <-chan struct{}, waitCh <-chan error) error {
	s.closeTransport(stdin, peer, serveDone)
	// Signal the process group/job even when the leader has already exited so
	// descendants cannot outlive a terminal task.
	terminateErr := terminateChild(cmd)
	if terminateErr != nil && !errors.Is(terminateErr, os.ErrProcessDone) {
		return fmt.Errorf("terminate app-server process tree: %w", terminateErr)
	}
	select {
	case <-waitCh:
		releaseChildProcess(cmd)
		return nil
	default:
	}
	select {
	case <-waitCh:
		releaseChildProcess(cmd)
		return nil
	case <-time.After(2 * time.Second):
		return errors.New("app-server process tree did not terminate")
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

func (s *AppServerSession) interruptAndClose(reason error, cmd *exec.Cmd, stdin io.WriteCloser, peer *jsonrpc.Peer, serveDone <-chan struct{}, waitCh <-chan error, threadID, turnID string) error {
	interruptCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	_, _, _ = peer.Call(interruptCtx, "turn/interrupt", mustJSON(map[string]any{
		"threadId": threadID,
		"turnId":   turnID,
	}))
	cancel()
	if err := s.closeProcess(cmd, stdin, peer, serveDone, waitCh); err != nil {
		// Do not wrap reason here: a failed termination must become lost at
		// the Worker boundary, never cancelled with its lease released.
		return fmt.Errorf("interrupt after %v; child cleanup: %w", reason, err)
	}
	return reason
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
	object := func(properties map[string]any, required []string) map[string]any {
		return map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties":           properties,
			"required":             required,
		}
	}
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"version": map[string]any{"type": "integer"}, "taskId": map[string]any{"type": "string"}, "attemptId": map[string]any{"type": "string"},
			"status": map[string]any{"type": "string", "enum": []string{"completed", "blocked", "failed", "cancelled", "lost"}}, "summary": map[string]any{"type": "string"},
			"findings": map[string]any{"type": "array", "items": object(map[string]any{
				"severity": map[string]any{"type": "string"}, "location": map[string]any{"type": "string"}, "detail": map[string]any{"type": "string"},
			}, []string{"severity", "location", "detail"})},
			"changes": map[string]any{"type": "array", "items": object(map[string]any{
				"path": map[string]any{"type": "string"}, "summary": map[string]any{"type": "string"},
			}, []string{"path", "summary"})},
			"verification": map[string]any{"type": "array", "items": object(map[string]any{
				"command": map[string]any{"type": "string"}, "outcome": map[string]any{"type": "string"}, "artifactId": map[string]any{"type": "string"},
			}, []string{"command", "outcome", "artifactId"})},
			"artifacts": map[string]any{"type": "array", "items": object(map[string]any{
				"id": map[string]any{"type": "string"}, "name": map[string]any{"type": "string"}, "sha256": map[string]any{"type": "string"}, "bytes": map[string]any{"type": "integer"},
			}, []string{"id", "name", "sha256", "bytes"})},
			"recommendedNext": map[string]any{"type": "string"},
		},
		"required": []string{"version", "taskId", "attemptId", "status", "summary", "findings", "changes", "verification", "artifacts", "recommendedNext"},
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
