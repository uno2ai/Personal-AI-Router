// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"nvpair-shared/clustertrust"
	"nvpair-shared/codexprotocol"
	"nvpair-shared/jsonrpc"
)

type MCPServer struct {
	client           WorkerClient
	pool             *WorkerPool
	mu               sync.Mutex
	owners           map[string]WorkerClient
	localRuntimePath string
	defaultTaskMode  string
	index            *TaskIndex
}

func NewMCPServer(client WorkerClient) *MCPServer {
	return &MCPServer{client: client, pool: NewWorkerPool([]WorkerTarget{{ID: "local", Client: client}}), owners: make(map[string]WorkerClient)}
}

func NewMCPServerWithWorkers(targets []WorkerTarget) *MCPServer {
	pool := NewWorkerPool(targets)
	return &MCPServer{pool: pool, owners: make(map[string]WorkerClient)}
}

func (s *MCPServer) SetLocalRuntimeDescriptor(path string) {
	s.mu.Lock()
	s.localRuntimePath = path
	s.mu.Unlock()
}

func (s *MCPServer) SetTaskIndex(index *TaskIndex) {
	s.mu.Lock()
	s.index = index
	s.mu.Unlock()
}

func (s *MCPServer) SetDiscoveredWorkers(targets []WorkerTarget) {
	s.pool.ReplaceDiscoveredTargets(targets)
}

// StartDiscoveryWatcher keeps the remote Worker set live for the lifetime of
// the Main-owned Supervisor. The discovery snapshot is only a hint: every
// candidate is re-probed over pinned mTLS before it enters the pool, and a
// successful snapshot with a removed pin intentionally clears that candidate.
func (s *MCPServer) StartDiscoveryWatcher(ctx context.Context, clusterDir, discoveryFile string) {
	if strings.TrimSpace(clusterDir) == "" || strings.TrimSpace(discoveryFile) == "" {
		return
	}
	mesh := clustertrust.Open(clusterDir)
	discovery := NewWorkerDiscovery(mesh)
	refresh := func() {
		nodes, err := readDiscoveryNodes(discoveryFile)
		if err != nil {
			return
		}
		probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		targets, err := discovery.Discover(probeCtx, nodes)
		cancel()
		if err != nil {
			return
		}
		s.SetDiscoveredWorkers(targets)
	}
	refresh()
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				refresh()
			}
		}
	}()
}

type mcpReadWriter struct {
	io.Reader
	io.Writer
}

func (s *MCPServer) Serve(input io.Reader, output io.Writer) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.mu.Lock()
	localRuntimePath := s.localRuntimePath
	s.mu.Unlock()
	if localRuntimePath != "" {
		s.refreshLocalRuntime(localRuntimePath, time.Now().UTC())
		go s.watchLocalRuntime(ctx, localRuntimePath)
	}
	codec := jsonrpc.NewCodec(&mcpReadWriter{Reader: input, Writer: output})
	for {
		message, err := codec.Read()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if message.IsNotification() {
			continue
		}
		if !message.IsRequest() {
			continue
		}
		result, rpcCode, rpcMessage := s.handle(ctx, message.Method, message.Params)
		if rpcCode != 0 {
			if err := codec.RespondError(message.ID, rpcCode, rpcMessage); err != nil {
				return err
			}
			continue
		}
		if err := codec.Respond(message.ID, result); err != nil {
			return err
		}
	}
}

func (s *MCPServer) watchLocalRuntime(ctx context.Context, path string) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.refreshLocalRuntime(path, now.UTC())
		}
	}
}

func (s *MCPServer) refreshLocalRuntime(path string, now time.Time) {
	target, err := loadLocalRuntimeTarget(path, now)
	if err != nil || target == nil {
		s.pool.SetLocalTarget(nil)
		return
	}
	s.pool.SetLocalTarget(target)
}

func (s *MCPServer) handle(ctx context.Context, method string, params json.RawMessage) (any, int, string) {
	switch method {
	case "initialize":
		return map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]string{"name": "nvpair-codex-supervisor", "version": Version},
		}, 0, ""
	case "tools/list":
		return map[string]any{"tools": supervisorTools()}, 0, ""
	case "tools/call":
		return s.handleTool(ctx, params)
	default:
		return nil, -32601, "method not found"
	}
}

type mcpTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func supervisorTools() []mcpTool {
	objectSchema := map[string]any{"type": "object", "additionalProperties": false}
	return []mcpTool{
		{Name: "artifacts.get", Description: "Read a declared Worker artifact by task and opaque artifact ID.", InputSchema: objectSchemaWith(objectSchema, map[string]any{"taskId": map[string]any{"type": "string"}, "artifactId": map[string]any{"type": "string"}}, []string{"taskId", "artifactId"})},
		{Name: "tasks.cancel", Description: "Cancel one task using its current lease tuple.", InputSchema: objectSchemaWith(objectSchema, map[string]any{"taskId": map[string]any{"type": "string"}, "attemptId": map[string]any{"type": "string"}, "leaseEpoch": map[string]any{"type": "integer", "minimum": 1}}, []string{"taskId", "attemptId", "leaseEpoch"})},
		{Name: "tasks.delegate", Description: "Delegate one bounded task to an eligible native Codex Worker. YOLO requires an opted-in Worker and runs without sandbox or approval prompts. Omitted mode uses the Supervisor's configured default.", InputSchema: objectSchemaWith(objectSchema, map[string]any{"objective": map[string]any{"type": "string"}, "workspace": map[string]any{"type": "string", "enum": []string{"local"}}, "mode": map[string]any{"type": "string", "enum": []string{"read", "write", "yolo"}}, "approval": map[string]any{"type": "string", "enum": []string{"local-only", "never"}}, "workerId": map[string]any{"type": "string"}, "os": map[string]any{"type": "string"}, "architecture": map[string]any{"type": "string"}, "tools": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}}, []string{"objective"})},
		{Name: "tasks.result", Description: "Read the compact handoff for one task.", InputSchema: objectSchemaWith(objectSchema, map[string]any{"taskId": map[string]any{"type": "string"}}, []string{"taskId"})},
		{Name: "tasks.status", Description: "Read compact state for one task.", InputSchema: objectSchemaWith(objectSchema, map[string]any{"taskId": map[string]any{"type": "string"}}, []string{"taskId"})},
		{Name: "workers.list", Description: "Read authenticated Worker capability metadata.", InputSchema: objectSchema},
	}
}

func objectSchemaWith(base map[string]any, properties map[string]any, required []string) map[string]any {
	copy := make(map[string]any, len(base)+2)
	for key, value := range base {
		copy[key] = value
	}
	copy["properties"] = properties
	if len(required) > 0 {
		copy["required"] = required
	}
	return copy
}

type toolCall struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type toolResult struct {
	Content           []mcpContent    `json:"content"`
	IsError           bool            `json:"isError,omitempty"`
	StructuredContent json.RawMessage `json:"structuredContent,omitempty"`
}

type mcpContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (s *MCPServer) handleTool(ctx context.Context, params json.RawMessage) (any, int, string) {
	var call toolCall
	if err := json.Unmarshal(params, &call); err != nil || call.Name == "" {
		return nil, -32602, "tools/call requires a tool name"
	}
	if len(call.Arguments) == 0 {
		call.Arguments = json.RawMessage(`{}`)
	}
	switch call.Name {
	case "workers.list":
		return s.workersList(ctx)
	case "tasks.delegate":
		return s.delegate(ctx, call.Arguments)
	case "tasks.status":
		var args struct {
			TaskID string `json:"taskId"`
		}
		if err := decodeArgs(call.Arguments, &args); err != nil || args.TaskID == "" {
			return nil, -32602, "tasks.status requires taskId"
		}
		return s.taskCall(ctx, "tasks.status", args.TaskID, func(client WorkerClient) (json.RawMessage, error) { return client.Status(ctx, args.TaskID) })
	case "tasks.result":
		var args struct {
			TaskID string `json:"taskId"`
		}
		if err := decodeArgs(call.Arguments, &args); err != nil || args.TaskID == "" {
			return nil, -32602, "tasks.result requires taskId"
		}
		return s.taskCall(ctx, "tasks.result", args.TaskID, func(client WorkerClient) (json.RawMessage, error) { return client.Result(ctx, args.TaskID) })
	case "tasks.cancel":
		var args struct {
			TaskID     string `json:"taskId"`
			AttemptID  string `json:"attemptId"`
			LeaseEpoch uint64 `json:"leaseEpoch"`
		}
		if err := decodeArgs(call.Arguments, &args); err != nil || args.TaskID == "" || args.AttemptID == "" || args.LeaseEpoch == 0 {
			return nil, -32602, "tasks.cancel requires taskId, attemptId, and leaseEpoch"
		}
		requestID, err := newID("cancel")
		if err != nil {
			return toolError("could not allocate cancellation request id"), 0, ""
		}
		mutation := codexprotocol.Mutation{ProtocolVersion: codexprotocol.ProtocolVersion, RequestID: requestID, TaskID: args.TaskID, AttemptID: args.AttemptID, LeaseEpoch: args.LeaseEpoch}
		return s.taskCall(ctx, "tasks.cancel", args.TaskID, func(client WorkerClient) (json.RawMessage, error) { return client.Cancel(ctx, mutation) })
	case "artifacts.get":
		var args struct {
			TaskID     string `json:"taskId"`
			ArtifactID string `json:"artifactId"`
		}
		if err := decodeArgs(call.Arguments, &args); err != nil || args.TaskID == "" || args.ArtifactID == "" {
			return nil, -32602, "artifacts.get requires taskId and artifactId"
		}
		return s.callArtifact(ctx, args.TaskID, args.ArtifactID)
	default:
		return nil, -32602, "unknown Supervisor tool"
	}
}

func (s *MCPServer) delegate(ctx context.Context, raw json.RawMessage) (any, int, string) {
	var args struct {
		Objective    string   `json:"objective"`
		Workspace    string   `json:"workspace"`
		Mode         string   `json:"mode"`
		Approval     string   `json:"approval"`
		WorkerID     string   `json:"workerId"`
		OS           string   `json:"os"`
		Architecture string   `json:"architecture"`
		Tools        []string `json:"tools"`
	}
	if err := decodeArgs(raw, &args); err != nil || strings.TrimSpace(args.Objective) == "" {
		return nil, -32602, "tasks.delegate requires objective"
	}
	if args.Workspace == "" {
		args.Workspace = "local"
	}
	if args.Mode == "" {
		args.Mode = s.defaultTaskMode
		if args.Mode == "" {
			args.Mode = "read"
		}
	}
	expectedApproval := "local-only"
	workspaceMode := args.Mode
	if args.Mode == "yolo" {
		expectedApproval = "never"
		workspaceMode = "write"
	}
	if args.Approval == "" {
		args.Approval = expectedApproval
	}
	if args.Workspace != "local" || (args.Mode != "read" && args.Mode != "write" && args.Mode != "yolo") || args.Approval != expectedApproval {
		return nil, -32602, "tasks.delegate requires local workspace and read/write with local-only approval or yolo with never approval"
	}
	taskID, err := newID("task")
	if err != nil {
		return toolError("could not allocate task id"), 0, ""
	}
	requestID, err := newID("request")
	if err != nil {
		return toolError("could not allocate request id"), 0, ""
	}
	attemptID, err := newID("attempt")
	if err != nil {
		return toolError("could not allocate attempt id"), 0, ""
	}
	request := codexprotocol.TaskRequest{
		Mutation:  codexprotocol.Mutation{ProtocolVersion: codexprotocol.ProtocolVersion, RequestID: requestID, TaskID: taskID, AttemptID: attemptID, LeaseEpoch: 1},
		Context:   codexprotocol.ContextPackage{Version: codexprotocol.ContextVersion, Objective: args.Objective, Limits: codexprotocol.Limits{WallSeconds: 1800}},
		Workspace: codexprotocol.WorkspaceSpec{ID: "local", Path: "local", Mode: workspaceMode},
		Execution: codexprotocol.ExecutionSpec{Sandbox: sandboxForMode(args.Mode), Approval: args.Approval},
	}
	if err := s.pool.Refresh(ctx); err != nil && len(s.pool.Snapshot()) == 0 {
		return toolError("no reachable Worker"), 0, ""
	}
	target, err := s.pool.Select(WorkerRequirements{WorkerID: args.WorkerID, OS: args.OS, Architecture: args.Architecture, Workspace: args.Workspace, Mode: args.Mode, Tools: args.Tools})
	if err != nil {
		return toolError(err.Error()), 0, ""
	}
	s.mu.Lock()
	index := s.index
	s.mu.Unlock()
	if index != nil {
		if err := index.Begin(TaskIntent{TaskID: taskID, RequestID: requestID, AttemptID: attemptID, LeaseEpoch: 1, WorkerID: target.ID, Request: request}); err != nil {
			return toolError("could not persist dispatch intent: " + err.Error()), 0, ""
		}
	}
	rawResult, err := target.Client.Create(ctx, request)
	if err != nil {
		if index != nil {
			_ = index.MarkUncertain(taskID, err.Error())
		}
		return toolError("dispatch outcome is uncertain; retry status for the same task: " + err.Error()), 0, ""
	}
	sanitized, err := sanitizeWorkerResponse("tasks.delegate", rawResult, taskID)
	if err != nil {
		if index != nil {
			_ = index.MarkUncertain(taskID, "Worker returned an invalid dispatch response")
		}
		return toolError("dispatch outcome is uncertain; Worker response was invalid"), 0, ""
	}
	if index != nil {
		if err := index.MarkAcknowledged(taskID, target.ID); err != nil {
			_ = index.MarkUncertain(taskID, "could not persist acknowledged dispatch")
			return toolError("dispatch outcome is uncertain; acknowledgement could not be persisted"), 0, ""
		}
	}
	var response struct {
		Record codexprotocol.TaskRecord `json:"record"`
	}
	if json.Unmarshal(sanitized, &response) == nil && response.Record.TaskID != "" {
		s.mu.Lock()
		s.owners[response.Record.TaskID] = target.Client
		s.mu.Unlock()
	}
	return toolSuccess(sanitized), 0, ""
}

func (s *MCPServer) workersList(ctx context.Context) (any, int, string) {
	if err := s.pool.Refresh(ctx); err != nil && len(s.pool.Snapshot()) == 0 {
		return toolError("no reachable Worker"), 0, ""
	}
	type publicWorker struct {
		ID           string                           `json:"id"`
		Capabilities codexprotocol.WorkerCapabilities `json:"capabilities"`
		LastError    string                           `json:"lastError,omitempty"`
	}
	workers := make([]publicWorker, 0)
	for _, target := range s.pool.Snapshot() {
		workers = append(workers, publicWorker{ID: target.ID, Capabilities: target.Capabilities, LastError: target.LastError})
	}
	encoded, err := json.Marshal(map[string]any{"workers": workers})
	if err != nil {
		return toolError("could not encode Worker list"), 0, ""
	}
	return toolSuccess(encoded), 0, ""
}

func (s *MCPServer) callArtifact(ctx context.Context, taskID, artifactID string) (any, int, string) {
	for _, client := range s.taskClients(taskID) {
		data, err := client.Artifact(ctx, taskID, artifactID)
		if err == nil {
			return toolSuccess(mustArtifactJSON(taskID, artifactID, data)), 0, ""
		}
	}
	return toolError("artifact is unavailable"), 0, ""
}

func mustArtifactJSON(taskID, artifactID string, data []byte) json.RawMessage {
	digest := sha256.Sum256(data)
	encoded, _ := json.Marshal(map[string]any{"taskId": taskID, "artifactId": artifactID, "bytes": len(data), "sha256": fmt.Sprintf("%x", digest[:]), "dataBase64": base64.StdEncoding.EncodeToString(data)})
	return encoded
}

func (s *MCPServer) taskClients(taskID string) []WorkerClient {
	s.mu.Lock()
	owner := s.owners[taskID]
	index := s.index
	s.mu.Unlock()
	if owner != nil {
		return []WorkerClient{owner}
	}
	if index != nil {
		intent, ok := index.Get(taskID)
		if !ok {
			return nil
		}
		for _, target := range s.pool.Snapshot() {
			if target.ID == intent.WorkerID {
				return []WorkerClient{target.Client}
			}
		}
		return nil
	}
	clients := make([]WorkerClient, 0)
	for _, target := range s.pool.Snapshot() {
		clients = append(clients, target.Client)
	}
	return clients
}

func (s *MCPServer) taskCall(ctx context.Context, toolName, taskID string, call func(WorkerClient) (json.RawMessage, error)) (any, int, string) {
	var lastErr error
	for _, client := range s.taskClients(taskID) {
		result, err := call(client)
		if err != nil {
			lastErr = err
			continue
		}
		projected, code, message := s.callWorker(ctx, toolName, taskID, func() (json.RawMessage, error) { return result, nil })
		if code == 0 && taskID != "" {
			s.markTerminalFromResult(taskID, projected)
		}
		return projected, code, message
	}
	if lastErr == nil {
		lastErr = errors.New("task owner is unavailable")
	}
	return toolError(lastErr.Error()), 0, ""
}

func (s *MCPServer) markTerminalFromResult(taskID string, projected any) {
	tool, ok := projected.(toolResult)
	if !ok || len(tool.StructuredContent) == 0 {
		return
	}
	var record codexprotocol.TaskRecord
	if err := json.Unmarshal(tool.StructuredContent, &record); err != nil {
		var envelope struct {
			Record codexprotocol.TaskRecord `json:"record"`
		}
		if json.Unmarshal(tool.StructuredContent, &envelope) == nil {
			record = envelope.Record
		}
	}
	if !record.State.Terminal() {
		return
	}
	s.mu.Lock()
	index := s.index
	s.mu.Unlock()
	if index != nil {
		_ = index.MarkTerminal(taskID)
	}
}

func (s *MCPServer) callWorker(ctx context.Context, toolName, expectedTaskID string, call func() (json.RawMessage, error)) (any, int, string) {
	result, err := call()
	if err != nil {
		return toolError(err.Error()), 0, ""
	}
	sanitized, err := sanitizeWorkerResponse(toolName, result, expectedTaskID)
	if err != nil {
		return toolError("Worker returned an invalid response"), 0, ""
	}
	return toolSuccess(sanitized), 0, ""
}

func decodeArgs(raw json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	return func() error {
		if err := decoder.Decode(&extra); err != io.EOF {
			if err == nil {
				return errors.New("multiple JSON values")
			}
			return err
		}
		return nil
	}()
}

func sanitizeWorkerResponse(toolName string, raw json.RawMessage, expectedTaskID string) (json.RawMessage, error) {
	switch toolName {
	case "workers.list":
		var response struct {
			ProtocolVersion int                              `json:"protocolVersion"`
			Version         string                           `json:"version"`
			Capabilities    codexprotocol.WorkerCapabilities `json:"capabilities"`
		}
		if err := json.Unmarshal(raw, &response); err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"protocolVersion": response.ProtocolVersion, "version": response.Version, "capabilities": response.Capabilities})
	case "tasks.delegate":
		var response struct {
			Record     codexprotocol.TaskRecord `json:"record"`
			Idempotent bool                     `json:"idempotent"`
		}
		if err := json.Unmarshal(raw, &response); err != nil {
			return nil, err
		}
		if response.Record.TaskID == "" || response.Record.AttemptID == "" || response.Record.LeaseEpoch == 0 {
			return nil, errors.New("Worker returned an incomplete task record")
		}
		if expectedTaskID != "" && response.Record.TaskID != expectedTaskID {
			return nil, errors.New("Worker returned a different task")
		}
		return json.Marshal(response)
	case "tasks.status", "tasks.cancel":
		var response codexprotocol.TaskRecord
		if err := json.Unmarshal(raw, &response); err != nil {
			return nil, err
		}
		if response.TaskID == "" || response.AttemptID == "" || response.LeaseEpoch == 0 {
			return nil, errors.New("Worker returned an incomplete task record")
		}
		if expectedTaskID != "" && response.TaskID != expectedTaskID {
			return nil, errors.New("Worker returned a different task")
		}
		return json.Marshal(response)
	case "tasks.result":
		var response struct {
			Record  codexprotocol.TaskRecord `json:"record"`
			Handoff codexprotocol.Handoff    `json:"handoff"`
		}
		if err := json.Unmarshal(raw, &response); err != nil {
			return nil, err
		}
		if response.Record.TaskID == "" || response.Record.AttemptID == "" || response.Record.LeaseEpoch == 0 {
			return nil, errors.New("Worker returned an incomplete task record")
		}
		if expectedTaskID != "" && response.Record.TaskID != expectedTaskID {
			return nil, errors.New("Worker returned a different task")
		}
		if err := response.Handoff.Validate(response.Record.TaskID, response.Record.AttemptID); err != nil {
			return nil, err
		}
		return json.Marshal(response)
	default:
		return nil, errors.New("unsupported Worker response")
	}
}

func toolSuccess(raw json.RawMessage) toolResult {
	return toolResult{Content: []mcpContent{{Type: "text", Text: string(raw)}}, StructuredContent: raw}
}

func toolError(message string) toolResult {
	return toolResult{Content: []mcpContent{{Type: "text", Text: message}}, IsError: true}
}

func sandboxForMode(mode string) string {
	if mode == "yolo" {
		return "danger-full-access"
	}
	if mode == "write" {
		return "workspace-write"
	}
	return "read-only"
}

func newID(prefix string) (string, error) {
	var data [12]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return prefix + "-" + hex.EncodeToString(data[:]), nil
}
