// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"nvpair-shared/codexprotocol"
)

type fakeWorkerClient struct{}

type leakyWorkerClient struct{}

func (fakeWorkerClient) Worker(context.Context) (json.RawMessage, error) {
	return json.RawMessage(`{"protocolVersion":1,"version":"test"}`), nil
}

func (fakeWorkerClient) Create(context.Context, codexprotocol.TaskRequest) (json.RawMessage, error) {
	return json.RawMessage(`{"record":{"taskId":"task-1","attemptId":"attempt-1","leaseEpoch":1,"state":"accepted"},"idempotent":false}`), nil
}

func (fakeWorkerClient) Status(context.Context, string) (json.RawMessage, error) {
	return json.RawMessage(`{"taskId":"task-1","state":"running"}`), nil
}

func (fakeWorkerClient) Result(context.Context, string) (json.RawMessage, error) {
	return json.RawMessage(`{"record":{"taskId":"task-1","attemptId":"attempt-1"},"handoff":{"version":1,"taskId":"task-1","attemptId":"attempt-1","status":"completed","summary":"done"}}`), nil
}

func (fakeWorkerClient) Cancel(context.Context, codexprotocol.Mutation) (json.RawMessage, error) {
	return json.RawMessage(`{"taskId":"task-1","state":"cancelling"}`), nil
}

func (leakyWorkerClient) Worker(context.Context) (json.RawMessage, error) {
	return json.RawMessage(`{"protocolVersion":1,"version":"test","capabilities":{"os":"darwin","architecture":"arm64","maxConcurrency":1},"prompt":"secret"}`), nil
}
func (leakyWorkerClient) Create(context.Context, codexprotocol.TaskRequest) (json.RawMessage, error) {
	return json.RawMessage(`{"record":{"taskId":"task-1","state":"accepted"},"idempotent":false,"prompt":"secret"}`), nil
}
func (leakyWorkerClient) Status(context.Context, string) (json.RawMessage, error) {
	return json.RawMessage(`{"taskId":"task-1","state":"running","response":"secret"}`), nil
}
func (leakyWorkerClient) Result(context.Context, string) (json.RawMessage, error) {
	return json.RawMessage(`{"record":{"taskId":"task-1"},"handoff":{"status":"completed","summary":"done"},"transcript":"secret"}`), nil
}
func (leakyWorkerClient) Cancel(context.Context, codexprotocol.Mutation) (json.RawMessage, error) {
	return json.RawMessage(`{"taskId":"task-1","state":"cancelling","source":"secret"}`), nil
}

func callMCP(t *testing.T, server *MCPServer, request string) json.RawMessage {
	t.Helper()
	var output bytes.Buffer
	if err := server.Serve(strings.NewReader(request+"\n"), &output); err != nil {
		t.Fatal(err)
	}
	var response struct {
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatalf("decode MCP response %q: %v", output.String(), err)
	}
	if len(response.Error) > 0 && string(response.Error) != "null" {
		t.Fatalf("MCP error: %s", response.Error)
	}
	return response.Result
}

func responseText(t *testing.T, result json.RawMessage) string {
	t.Helper()
	var decoded struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(result, &decoded); err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, item := range decoded.Content {
		texts = append(texts, item.Text)
	}
	return strings.Join(texts, "\n")
}

func TestToolsListContainsOnlyBoundedSupervisorSurface(t *testing.T) {
	server := NewMCPServer(fakeWorkerClient{})
	result := callMCP(t, server, `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	var decoded struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(result, &decoded); err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(decoded.Tools))
	for _, tool := range decoded.Tools {
		got = append(got, tool.Name)
	}
	want := []string{"artifacts.get", "tasks.cancel", "tasks.delegate", "tasks.result", "tasks.status", "workers.list"}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("tools=%v, want %v", got, want)
	}
	for _, name := range got {
		if strings.Contains(name, "approval") || strings.Contains(name, "relay") {
			t.Fatalf("unsafe tool exposed: %s", name)
		}
	}
}

func TestDelegateReturnsWorkerHandoffWithoutTranscript(t *testing.T) {
	server := NewMCPServer(fakeWorkerClient{})
	result := callMCP(t, server, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"tasks.delegate","arguments":{"objective":"run tests","workspace":"local","mode":"read"}}}`)
	text := responseText(t, result)
	if strings.Contains(text, "hidden reasoning") || strings.Contains(text, "stdout transcript") {
		t.Fatalf("unbounded output: %s", text)
	}
	if !strings.Contains(text, "taskId") || !strings.Contains(text, "state") {
		t.Fatalf("missing compact task response: %s", text)
	}
}

func TestUnknownMCPMethodReturnsMethodNotFound(t *testing.T) {
	server := NewMCPServer(fakeWorkerClient{})
	var output bytes.Buffer
	if err := server.Serve(strings.NewReader(`{"jsonrpc":"2.0","id":3,"method":"approvals/approve","params":{}}`+"\n"), &output); err != nil {
		t.Fatal(err)
	}
	var response struct {
		Error struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Error.Code != -32601 {
		t.Fatalf("error code=%d, want -32601", response.Error.Code)
	}
}

func TestWorkerResponsesAreWhitelistedBeforeMCPExposure(t *testing.T) {
	server := NewMCPServer(leakyWorkerClient{})
	result := callMCP(t, server, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"workers.list","arguments":{}}}`)
	if strings.Contains(responseText(t, result), "secret") {
		t.Fatal("untrusted Worker response field crossed the MCP boundary")
	}
}
