// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

func main() {
	logPath := os.Getenv("CODEX_FAKE_LOG")
	var logFile *os.File
	if logPath != "" {
		var err error
		logFile, err = os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return
		}
		defer logFile.Close()
	}

	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	initialized := false
	for scanner.Scan() {
		line := scanner.Bytes()
		var message struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(line, &message); err != nil {
			return
		}
		if logFile != nil && message.Method != "" {
			_, _ = fmt.Fprintln(logFile, message.Method)
		}
		switch message.Method {
		case "initialize":
			result := map[string]any{}
			if os.Getenv("CODEX_FAKE_UNSUPPORTED") != "1" {
				result = map[string]any{"userAgent": "Codex CLI/fixture", "platformFamily": "unix", "platformOs": "test"}
			}
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": result})
		case "initialized":
			initialized = true
		case "thread/start", "thread/resume":
			if !initialized {
				_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": message.ID, "error": map[string]any{"code": -32000, "message": "initialized notification required"}})
				continue
			}
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": map[string]any{"thread": map[string]string{"id": "fixture-thread"}}})
		case "turn/start":
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": map[string]any{"turn": map[string]string{"id": "fixture-turn"}}})
			switch os.Getenv("CODEX_FAKE_MODE") {
			case "approval":
				_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": 99, "method": "item/commandExecution/requestApproval", "params": map[string]string{"itemId": "fixture-item"}})
				if !scanner.Scan() {
					return
				}
				if logFile != nil {
					_, _ = fmt.Fprintln(logFile, "approval-response")
				}
				return
			case "exit":
				return
			case "hold":
				continue
			default:
				var turnParams struct {
					Input []struct {
						Text string `json:"text"`
					} `json:"input"`
				}
				_ = json.Unmarshal(message.Params, &turnParams)
				prompt := ""
				if len(turnParams.Input) > 0 {
					prompt = turnParams.Input[0].Text
				}
				handoff := fmt.Sprintf(`{"version":1,"taskId":%q,"attemptId":%q,"status":"completed","summary":"fixture completed"}`, promptValue(prompt, "Task ID: "), promptValue(prompt, "Attempt ID: "))
				_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "item/agentMessage/delta", "params": map[string]string{"itemId": "fixture-item", "threadId": "fixture-thread", "turnId": "fixture-turn", "delta": handoff}})
				_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "turn/completed", "params": map[string]any{"threadId": "fixture-thread", "turn": map[string]string{"id": "fixture-turn", "status": "completed"}}})
			}
		case "turn/interrupt":
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": map[string]any{}})
			return
		}
	}
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
