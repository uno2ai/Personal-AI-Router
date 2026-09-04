// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
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
	for scanner.Scan() {
		line := scanner.Bytes()
		if logFile != nil {
			_, _ = fmt.Fprintln(logFile, string(line))
		}
		var message struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.Unmarshal(line, &message); err != nil {
			return
		}
		switch message.Method {
		case "initialize":
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": map[string]any{}})
		case "thread/start":
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
					_, _ = fmt.Fprintln(logFile, scanner.Text())
				}
				return
			case "exit":
				return
			case "hold":
				continue
			default:
				_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "turn/completed", "params": map[string]string{"turnId": "fixture-turn"}})
			}
		case "turn/interrupt":
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": map[string]any{}})
			return
		}
	}
}
