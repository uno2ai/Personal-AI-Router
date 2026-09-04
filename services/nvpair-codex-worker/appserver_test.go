// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"nvpair-shared/codexprotocol"
)

func TestMain(m *testing.M) {
	if os.Getenv("CODEX_FAKE_APP_SERVER") == "1" {
		os.Exit(runFakeAppServer())
	}
	os.Exit(m.Run())
}

func runFakeAppServer() int {
	logPath := os.Getenv("CODEX_FAKE_LOG")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return 2
	}
	defer logFile.Close()
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	initialized := false
	for scanner.Scan() {
		line := scanner.Text()
		var message map[string]json.RawMessage
		if json.Unmarshal([]byte(line), &message) != nil {
			return 3
		}
		var method string
		_ = json.Unmarshal(message["method"], &method)
		if method != "" {
			_, _ = fmt.Fprintln(logFile, method)
		}
		if method == "initialize" {
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(message["id"]), "result": map[string]any{}})
			continue
		}
		if method == "initialized" {
			initialized = true
			continue
		}
		if method == "thread/start" {
			if os.Getenv("CODEX_FAKE_STRICT_HANDSHAKE") == "1" && !initialized {
				_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(message["id"]), "error": map[string]any{"code": -32000, "message": "initialized notification required"}})
				continue
			}
			result := map[string]any{"thread": map[string]string{"id": "thread-1"}}
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(message["id"]), "result": result})
			continue
		}
		if method == "turn/start" {
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(message["id"]), "result": map[string]any{"turn": map[string]string{"id": "turn-1"}}})
			if os.Getenv("CODEX_FAKE_APPROVAL") == "1" {
				_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": 99, "method": "item/commandExecution/requestApproval", "params": map[string]string{"itemId": "item-1"}})
				if scanner.Scan() {
					_, _ = fmt.Fprintln(logFile, scanner.Text())
				}
				return 0
			}
			var turnParams struct {
				Input []struct {
					Text string `json:"text"`
				} `json:"input"`
			}
			_ = json.Unmarshal(message["params"], &turnParams)
			prompt := ""
			if len(turnParams.Input) > 0 {
				prompt = turnParams.Input[0].Text
			}
			taskID := promptValue(prompt, "Task ID: ")
			attemptID := promptValue(prompt, "Attempt ID: ")
			handoff := fmt.Sprintf(`{"version":1,"taskId":%q,"attemptId":%q,"status":"completed","summary":"fixture completed"}`, taskID, attemptID)
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "item/agentMessage/delta", "params": map[string]string{"itemId": "item-1", "threadId": "thread-1", "turnId": "turn-1", "delta": handoff}})
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "turn/completed", "params": map[string]any{"threadId": "thread-1", "turn": map[string]string{"id": "turn-1", "status": "completed"}}})
			continue
		}
		if method == "turn/interrupt" {
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(message["id"]), "result": map[string]any{}})
			continue
		}
	}
	return 0
}

func promptValue(prompt, prefix string) string {
	start := strings.Index(prompt, prefix)
	if start < 0 {
		return ""
	}
	value := prompt[start+len(prefix):]
	if end := strings.IndexByte(value, '\n'); end >= 0 {
		value = value[:end]
	}
	return strings.TrimSpace(value)
}

func buildFakeAppServer(t *testing.T) string {
	t.Helper()
	return mustExecutable(t)
}

func mustExecutable(t *testing.T) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return executable
}

func readFixtureLog(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestAppServerAdapterUsesThreadStartAndTurnStart(t *testing.T) {
	logPath := t.TempDir() + "/app-server.log"
	t.Setenv("CODEX_FAKE_APP_SERVER", "1")
	t.Setenv("CODEX_FAKE_STRICT_HANDSHAKE", "1")
	t.Setenv("CODEX_FAKE_LOG", logPath)
	factory := NewAppServerFactory(buildFakeAppServer(t))
	session := factory.New()
	handoff, err := session.Run(context.Background(), validTaskRequest("r1", "t1", "a1", 1), func(event codexprotocol.TaskEvent) {})
	if err != nil {
		t.Fatal(err)
	}
	if handoff.Status != codexprotocol.StateCompleted || handoff.TaskID != "t1" {
		t.Fatalf("handoff=%+v", handoff)
	}
	log := readFixtureLog(t, logPath)
	if !strings.Contains(log, "initialize") || !strings.Contains(log, "initialized") || !strings.Contains(log, "thread/start") || !strings.Contains(log, "turn/start") {
		t.Fatalf("adapter did not issue native app-server methods: %s", log)
	}
}

func TestAppServerAdapterBlocksApprovalRequests(t *testing.T) {
	logPath := t.TempDir() + "/approval.log"
	t.Setenv("CODEX_FAKE_APP_SERVER", "1")
	t.Setenv("CODEX_FAKE_APPROVAL", "1")
	t.Setenv("CODEX_FAKE_LOG", logPath)
	session := NewAppServerFactory(buildFakeAppServer(t)).New()
	handoff, err := session.Run(context.Background(), validTaskRequest("r2", "t2", "a2", 1), func(event codexprotocol.TaskEvent) {})
	if err != nil {
		t.Fatal(err)
	}
	if handoff.Status != codexprotocol.StateBlocked || handoff.Summary != "approval_required" {
		t.Fatalf("handoff=%+v", handoff)
	}
	if strings.Contains(readFixtureLog(t, logPath), "approved") {
		t.Fatalf("approval fixture log contains approval material")
	}
}
