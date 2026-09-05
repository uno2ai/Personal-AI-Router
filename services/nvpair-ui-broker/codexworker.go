// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"nvpair-shared/codexruntime"
)

const codexWorkerReadyTimeout = 15 * time.Second

type codexWorkerProcess struct {
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	done     chan struct{}
	waitOnce sync.Once
	stopOnce sync.Once
}

func codexWorkerStartMessage(configPath string, configRevision uint64) (codexruntime.ControlMessage, error) {
	if !filepath.IsAbs(configPath) {
		return codexruntime.ControlMessage{}, fmt.Errorf("managed Codex Worker config path must be absolute: %q", configPath)
	}
	message := codexruntime.ControlMessage{
		SchemaVersion:  codexruntime.ControlSchemaVersion,
		Kind:           codexruntime.ControlKindStart,
		RequestID:      fmt.Sprintf("codex-worker-start-%d", time.Now().UnixNano()),
		ConfigRevision: configRevision,
		ConfigPath:     configPath,
		WorkspaceAlias: "local",
	}
	if _, err := codexruntime.MarshalControlMessage(message); err != nil {
		return codexruntime.ControlMessage{}, err
	}
	return message, nil
}

func readCodexWorkerConfigRevision(path string) (uint64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("read managed Codex Worker config: %w", err)
	}
	var config struct {
		PolicyRevision uint64 `json:"policyRevision"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return 0, fmt.Errorf("decode managed Codex Worker config: %w", err)
	}
	if config.PolicyRevision == 0 {
		return 0, errors.New("managed Codex Worker config has no positive policyRevision")
	}
	return config.PolicyRevision, nil
}

func startCodexWorker(path, configPath string, configRevision uint64) (*codexWorkerProcess, error) {
	message, err := codexWorkerStartMessage(configPath, configRevision)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(path, "--managed-control")
	configureSubprocess(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open Codex Worker control stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("open Codex Worker control stdout: %w", err)
	}
	cmd.Stderr = stderrOut
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("start Codex Worker: %w", err)
	}
	process := &codexWorkerProcess{cmd: cmd, stdin: stdin, done: make(chan struct{})}
	ready := make(chan error, 1)
	go process.readEvents(stdout, ready)
	go func() {
		_ = cmd.Wait()
		process.waitOnce.Do(func() { close(process.done) })
		select {
		case ready <- errors.New("Codex Worker exited before ready"):
		default:
		}
	}()
	data, err := codexruntime.MarshalControlMessage(message)
	if err != nil {
		process.Stop()
		return nil, err
	}
	if _, err := stdin.Write(data); err != nil {
		process.Stop()
		return nil, fmt.Errorf("send Codex Worker start: %w", err)
	}
	select {
	case err := <-ready:
		if err != nil {
			process.Stop()
			return nil, err
		}
	case <-process.done:
		return nil, errors.New("Codex Worker exited before ready")
	case <-time.After(codexWorkerReadyTimeout):
		process.Stop()
		return nil, fmt.Errorf("Codex Worker did not become ready within %s", codexWorkerReadyTimeout)
	}
	return process, nil
}

func (p *codexWorkerProcess) readEvents(stdout io.Reader, ready chan<- error) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		var event codexruntime.ControlEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			select {
			case ready <- fmt.Errorf("decode Codex Worker event: %w", err):
			default:
			}
			continue
		}
		switch event.Kind {
		case codexruntime.ControlEventReady:
			select {
			case ready <- nil:
			default:
			}
		case codexruntime.ControlEventError:
			select {
			case ready <- errors.New(event.Error):
			default:
			}
		}
	}
}

func (p *codexWorkerProcess) Done() <-chan struct{} { return p.done }

func (p *codexWorkerProcess) Stop() {
	p.stopOnce.Do(func() {
		data, err := codexruntime.MarshalControlMessage(codexruntime.ControlMessage{
			SchemaVersion: codexruntime.ControlSchemaVersion,
			Kind:          codexruntime.ControlKindShutdown,
			RequestID:     fmt.Sprintf("codex-worker-shutdown-%d", time.Now().UnixNano()),
		})
		if err == nil {
			_, _ = p.stdin.Write(data)
		}
		waitForStdinClose("codex-worker", p.cmd, p.stdin, p.done)
	})
}
