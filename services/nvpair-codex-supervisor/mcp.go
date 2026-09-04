// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"nvpair-shared/codexprotocol"
	"nvpair-shared/jsonrpc"
)

type MCPServer struct {
	client WorkerClient
}

func NewMCPServer(client WorkerClient) *MCPServer {
	return &MCPServer{client: client}
}

type mcpReadWriter struct {
	io.Reader
	io.Writer
}

func (s *MCPServer) Serve(input io.Reader, output io.Writer) error {
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
		result, rpcCode, rpcMessage := s.handle(context.Background(), message.Method, message.Params)
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
		{Name: "artifacts.get", Description: "Read a declared Worker artifact when artifact transport is enabled.", InputSchema: objectSchemaWith(objectSchema, map[string]any{"artifactId": map[string]any{"type": "string"}}, []string{"artifactId"})},
		{Name: "tasks.cancel", Description: "Cancel one task using its current lease tuple.", InputSchema: objectSchemaWith(objectSchema, map[string]any{"taskId": map[string]any{"type": "string"}, "attemptId": map[string]any{"type": "string"}, "leaseEpoch": map[string]any{"type": "integer", "minimum": 1}}, []string{"taskId", "attemptId", "leaseEpoch"})},
		{Name: "tasks.delegate", Description: "Delegate one bounded task to the local Codex Worker.", InputSchema: objectSchemaWith(objectSchema, map[string]any{"objective": map[string]any{"type": "string"}, "workspace": map[string]any{"type": "string", "enum": []string{"local"}}, "mode": map[string]any{"type": "string", "enum": []string{"read", "write"}}, "approval": map[string]any{"type": "string", "enum": []string{"local-only"}}}, []string{"objective"})},
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
		return s.callWorker(ctx, "workers.list", func() (json.RawMessage, error) { return s.client.Worker(ctx) })
	case "tasks.delegate":
		return s.delegate(ctx, call.Arguments)
	case "tasks.status":
		var args struct {
			TaskID string `json:"taskId"`
		}
		if err := decodeArgs(call.Arguments, &args); err != nil || args.TaskID == "" {
			return nil, -32602, "tasks.status requires taskId"
		}
		return s.callWorker(ctx, "tasks.status", func() (json.RawMessage, error) { return s.client.Status(ctx, args.TaskID) })
	case "tasks.result":
		var args struct {
			TaskID string `json:"taskId"`
		}
		if err := decodeArgs(call.Arguments, &args); err != nil || args.TaskID == "" {
			return nil, -32602, "tasks.result requires taskId"
		}
		return s.callWorker(ctx, "tasks.result", func() (json.RawMessage, error) { return s.client.Result(ctx, args.TaskID) })
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
		return s.callWorker(ctx, "tasks.cancel", func() (json.RawMessage, error) { return s.client.Cancel(ctx, mutation) })
	case "artifacts.get":
		return toolError("artifact transport is not available in Phase 1"), 0, ""
	default:
		return nil, -32602, "unknown Supervisor tool"
	}
}

func (s *MCPServer) delegate(ctx context.Context, raw json.RawMessage) (any, int, string) {
	var args struct {
		Objective string `json:"objective"`
		Workspace string `json:"workspace"`
		Mode      string `json:"mode"`
		Approval  string `json:"approval"`
	}
	if err := decodeArgs(raw, &args); err != nil || strings.TrimSpace(args.Objective) == "" {
		return nil, -32602, "tasks.delegate requires objective"
	}
	if args.Workspace == "" {
		args.Workspace = "local"
	}
	if args.Mode == "" {
		args.Mode = "read"
	}
	if args.Approval == "" {
		args.Approval = "local-only"
	}
	if args.Workspace != "local" || (args.Mode != "read" && args.Mode != "write") || args.Approval != "local-only" {
		return nil, -32602, "tasks.delegate accepts only local workspace, read/write mode, and local-only approval"
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
		Workspace: codexprotocol.WorkspaceSpec{ID: "local", Path: "local", Mode: args.Mode},
		Execution: codexprotocol.ExecutionSpec{Sandbox: sandboxForMode(args.Mode), Approval: "local-only"},
	}
	return s.callWorker(ctx, "tasks.delegate", func() (json.RawMessage, error) { return s.client.Create(ctx, request) })
}

func (s *MCPServer) callWorker(ctx context.Context, toolName string, call func() (json.RawMessage, error)) (any, int, string) {
	result, err := call()
	if err != nil {
		return toolError(err.Error()), 0, ""
	}
	sanitized, err := sanitizeWorkerResponse(toolName, result)
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

func sanitizeWorkerResponse(toolName string, raw json.RawMessage) (json.RawMessage, error) {
	switch toolName {
	case "workers.list":
		var response struct {
			ProtocolVersion int            `json:"protocolVersion"`
			Version         string         `json:"version"`
			Capabilities    map[string]any `json:"capabilities"`
		}
		if err := json.Unmarshal(raw, &response); err != nil {
			return nil, err
		}
		capabilities := map[string]any{}
		for _, key := range []string{"os", "architecture", "maxConcurrency"} {
			if value, ok := response.Capabilities[key]; ok {
				capabilities[key] = value
			}
		}
		return json.Marshal(map[string]any{"protocolVersion": response.ProtocolVersion, "version": response.Version, "capabilities": capabilities})
	case "tasks.delegate":
		var response struct {
			Record     codexprotocol.TaskRecord `json:"record"`
			Idempotent bool                     `json:"idempotent"`
		}
		if err := json.Unmarshal(raw, &response); err != nil {
			return nil, err
		}
		return json.Marshal(response)
	case "tasks.status", "tasks.cancel":
		var response codexprotocol.TaskRecord
		if err := json.Unmarshal(raw, &response); err != nil {
			return nil, err
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
