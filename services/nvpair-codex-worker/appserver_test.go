// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"nvpair-shared/protectedfile"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	if logPath == "" {
		logPath = os.DevNull
	}
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
			result := map[string]any{}
			if os.Getenv("CODEX_FAKE_UNSUPPORTED") != "1" {
				result = map[string]any{"userAgent": "Codex CLI/fixture", "platformFamily": "unix", "platformOs": "test"}
			}
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(message["id"]), "result": result})
			continue
		}
		if method == "initialized" {
			initialized = true
			continue
		}
		if method == "config/read" {
			config := map[string]any{}
			if os.Getenv("CODEX_FAKE_MCP_CONFIG") == "1" {
				config["mcp_servers"] = map[string]any{"pair-codex-supervisor": map[string]any{"command": "supervisor"}}
			}
			result := map[string]any{
				"config":  map[string]any{},
				"origins": map[string]any{},
				"layers":  []map[string]any{{"config": config, "name": "user", "version": "fixture"}},
			}
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(message["id"]), "result": result})
			continue
		}
		if method == "thread/start" || method == "thread/resume" {
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
			if os.Getenv("CODEX_FAKE_HOLD") == "1" {
				for {
					time.Sleep(time.Hour)
				}
			}
			if os.Getenv("CODEX_FAKE_APPROVAL") == "1" {
				_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": 99, "method": "item/commandExecution/requestApproval", "params": map[string]string{"itemId": "item-1"}})
				if scanner.Scan() {
					_, _ = fmt.Fprintln(logFile, "approval-response")
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

func TestAppServerAdapterRejectsUnsupportedInitializeResponse(t *testing.T) {
	logPath := t.TempDir() + "/unsupported.log"
	t.Setenv("CODEX_FAKE_APP_SERVER", "1")
	t.Setenv("CODEX_FAKE_UNSUPPORTED", "1")
	t.Setenv("CODEX_FAKE_LOG", logPath)
	_, err := NewAppServerFactory(buildFakeAppServer(t)).New().Run(context.Background(), validTaskRequest("r-unsupported", "t-unsupported", "a-unsupported", 1), func(codexprotocol.TaskEvent) {})
	if err == nil || !strings.Contains(err.Error(), "unsupported app-server") {
		t.Fatalf("expected fail-closed compatibility error, got %v", err)
	}
}

func TestAppServerProbeNegotiatesCompatibilityAndCleansUp(t *testing.T) {
	logPath := t.TempDir() + "/probe.log"
	t.Setenv("CODEX_FAKE_APP_SERVER", "1")
	t.Setenv("CODEX_FAKE_LOG", logPath)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := NewAppServerFactory(buildFakeAppServer(t)).Probe(ctx, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if log := readFixtureLog(t, logPath); !strings.Contains(log, "initialize") || !strings.Contains(log, "initialized") {
		t.Fatalf("probe did not complete native handshake: %s", log)
	}
}

func TestWorkerChildDisablesMainMCPPluginsAndHooks(t *testing.T) {
	args := strings.Join(appServerArguments(), " ")
	for _, required := range []string{
		"--disable plugins",
		"--disable hooks",
		"--disable apps",
		"--disable enable_mcp_apps",
		"--disable skill_mcp_dependency_install",
	} {
		if !strings.Contains(args, required) {
			t.Fatalf("app-server child args %q do not contain %q", args, required)
		}
	}
}

func TestWorkerChildRejectsEffectiveMCPConfigurationBeforeStartingThread(t *testing.T) {
	logPath := t.TempDir() + "/mcp-config.log"
	t.Setenv("CODEX_FAKE_APP_SERVER", "1")
	t.Setenv("CODEX_FAKE_MCP_CONFIG", "1")
	t.Setenv("CODEX_FAKE_LOG", logPath)
	_, err := NewAppServerFactory(buildFakeAppServer(t)).New().Run(
		context.Background(),
		validTaskRequest("r-mcp", "t-mcp", "a-mcp", 1),
		func(codexprotocol.TaskEvent) {},
	)
	if err == nil || !strings.Contains(err.Error(), "MCP configuration") {
		t.Fatalf("expected inherited MCP configuration to fail closed, got %v", err)
	}
	log := readFixtureLog(t, logPath)
	if strings.Contains(log, "thread/start") {
		t.Fatalf("thread started before MCP isolation was verified: %s", log)
	}
}

func TestWorkerChildUsesIsolatedCodexHomeWithoutCopyingUserConfiguration(t *testing.T) {
	userHome := t.TempDir()
	stateRoot := t.TempDir()
	t.Setenv("CODEX_HOME", userHome)
	if err := os.WriteFile(filepath.Join(userHome, "auth.json"), []byte(`{"token":"fixture"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(userHome, "config.toml"), []byte("[mcp_servers.main]\ncommand = 'supervisor'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	command, err := NewAppServerFactory("codex", stateRoot).command()
	if err != nil {
		t.Fatal(err)
	}
	isolatedHome := filepath.Join(stateRoot, "child-codex-home")
	if value := environmentValue(command.Env, "CODEX_HOME"); value != isolatedHome {
		t.Fatalf("CODEX_HOME=%q, want %q", value, isolatedHome)
	}
	if _, err := os.Stat(filepath.Join(isolatedHome, "config.toml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("user configuration was copied into isolated home: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(isolatedHome, "auth.json"))
	if err != nil || string(data) != `{"token":"fixture"}` {
		t.Fatalf("isolated authentication=%q err=%v", data, err)
	}
	_, err = os.Stat(filepath.Join(isolatedHome, "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := protectedfile.Check(filepath.Join(isolatedHome, "auth.json")); err != nil {
		t.Fatal(err)
	}
}

func environmentValue(environment []string, key string) string {
	prefix := key + "="
	for _, entry := range environment {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix)
		}
	}
	return ""
}

func TestAppServerProbeRejectsUnsupportedInitializeResponse(t *testing.T) {
	t.Setenv("CODEX_FAKE_APP_SERVER", "1")
	t.Setenv("CODEX_FAKE_UNSUPPORTED", "1")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := NewAppServerFactory(buildFakeAppServer(t)).Probe(ctx, t.TempDir()); err == nil || !strings.Contains(err.Error(), "unsupported app-server") {
		t.Fatalf("expected incompatible app-server probe, got %v", err)
	}
}

func TestHandoffSchemaUsesStrictArrayItems(t *testing.T) {
	schema := handoffSchema()
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("handoff schema properties are missing")
	}
	required := stringSet(schema["required"])
	for name, value := range properties {
		if !required[name] {
			t.Fatalf("handoff schema property %q is not required by strict response schema", name)
		}
		property, ok := value.(map[string]any)
		if !ok || property["type"] != "array" {
			continue
		}
		items, ok := property["items"].(map[string]any)
		if !ok {
			t.Fatalf("handoff array %q has no items schema", name)
		}
		itemProperties, ok := items["properties"].(map[string]any)
		if !ok {
			t.Fatalf("handoff array %q item properties are missing", name)
		}
		itemRequired := stringSet(items["required"])
		for itemName := range itemProperties {
			if !itemRequired[itemName] {
				t.Fatalf("handoff array %q item property %q is not required", name, itemName)
			}
		}
	}
}

func TestDecodeHandoffMessageValidatesEmbeddedJSON(t *testing.T) {
	message := `I checked the repository. {"version":1,"taskId":"task-1","attemptId":"attempt-1","status":"completed","summary":"clean","findings":[],"changes":[],"verification":[],"artifacts":[],"recommendedNext":"none"}`
	handoff, err := decodeHandoffMessage(message, "task-1", "attempt-1")
	if err != nil {
		t.Fatalf("decode embedded handoff: %v", err)
	}
	if handoff.Status != codexprotocol.StateCompleted || handoff.Summary != "clean" {
		t.Fatalf("unexpected handoff: %+v", handoff)
	}
}

func TestDecodeHandoffMessageRejectsWrongFence(t *testing.T) {
	message := `{"version":1,"taskId":"other","attemptId":"attempt-1","status":"completed","summary":"clean"}`
	if _, err := decodeHandoffMessage(message, "task-1", "attempt-1"); err == nil {
		t.Fatal("accepted handoff for a different task")
	}
}

func stringSet(value any) map[string]bool {
	values, ok := value.([]string)
	if !ok {
		return nil
	}
	set := make(map[string]bool, len(values))
	for _, value := range values {
		set[value] = true
	}
	return set
}

func TestAppServerAdapterResumesPersistedThreadForFollowUp(t *testing.T) {
	t.Setenv("CODEX_FAKE_APP_SERVER", "1")
	factory := NewAppServerFactory(buildFakeAppServer(t))
	session := factory.New()
	request := validTaskRequest("r-follow-1", "t-follow", "a-follow", 1)
	var threadID string
	if _, err := session.RunWithChildAndThread(context.Background(), request, func(codexprotocol.TaskEvent) {}, nil, func(id string) error {
		threadID = id
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if threadID == "" {
		t.Fatal("first turn did not publish a thread id")
	}
	followUp := request
	followUp.RequestID = "r-follow-2"
	followUp.Context.Objective = "follow up"
	handoff, err := session.RunFollowUpWithChild(context.Background(), followUp, threadID, func(codexprotocol.TaskEvent) {}, nil)
	if err != nil || handoff.Status != codexprotocol.StateCompleted {
		t.Fatalf("follow-up handoff=%+v err=%v", handoff, err)
	}
}

func TestAppServerCompletionRacingTurnStartIsCorrelatedAndReplayed(t *testing.T) {
	state := &appServerRunState{completedCh: make(chan turnResult, 1)}
	state.setThreadID("thread-1")
	state.acceptDelta(pendingDelta{threadID: "thread-1", turnID: "turn-1", delta: `{"version":1}`})
	if result := state.acceptCompletion(turnCompletion{threadID: "thread-1", turnID: "turn-1", status: "completed"}); result != nil {
		t.Fatal("completion was delivered before the expected turn id was known")
	}
	if result := state.setTurnIDAndFlush("turn-1"); result == nil {
		t.Fatal("queued completion was not released after turn correlation")
	} else {
		state.deliverCompletion(*result)
	}
	select {
	case result := <-state.completedCh:
		if result.message != `{"version":1}` {
			t.Fatalf("message=%q", result.message)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for correlated completion")
	}
}
