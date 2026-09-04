// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"nvpair-shared/codexprotocol"
)

type unavailableWorkerClient struct{}

func (unavailableWorkerClient) Worker(context.Context) (json.RawMessage, error) {
	return nil, errors.New("unavailable")
}

func (unavailableWorkerClient) Create(context.Context, codexprotocol.TaskRequest) (json.RawMessage, error) {
	return nil, errors.New("unavailable")
}

func (unavailableWorkerClient) Status(context.Context, string) (json.RawMessage, error) {
	return nil, errors.New("unavailable")
}

func (unavailableWorkerClient) Result(context.Context, string) (json.RawMessage, error) {
	return nil, errors.New("unavailable")
}

func (unavailableWorkerClient) Cancel(context.Context, codexprotocol.Mutation) (json.RawMessage, error) {
	return nil, errors.New("unavailable")
}

func (unavailableWorkerClient) Artifact(context.Context, string, string) ([]byte, error) {
	return nil, errors.New("unavailable")
}

func TestWorkerPoolSelectsEligibleCapabilitiesDeterministically(t *testing.T) {
	caps := func(osName, architecture string, tools ...string) codexprotocol.WorkerCapabilities {
		return codexprotocol.WorkerCapabilities{
			ProtocolVersion:  codexprotocol.ProtocolVersion,
			WorkerVersion:    "test",
			AppServerVersion: "codex-app-server-v1",
			OS:               osName,
			Architecture:     architecture,
			WorkspaceAliases: []string{"local"},
			WorkspaceModes:   []string{"read", "write"},
			ToolLabels:       tools,
			MaxConcurrency:   1,
			AvailableSlots:   1,
		}
	}
	p := NewWorkerPool([]WorkerTarget{
		{ID: "windows", Client: fakeWorkerClient{}, Capabilities: caps("windows", "amd64", "powershell")},
		{ID: "mac", Client: fakeWorkerClient{}, Capabilities: caps("darwin", "arm64", "cuda")},
	})
	chosen, err := p.Select(WorkerRequirements{OS: "darwin", Architecture: "arm64", Workspace: "local", Mode: "write", Tools: []string{"cuda"}})
	if err != nil {
		t.Fatal(err)
	}
	if chosen.ID != "mac" {
		t.Fatalf("selected Worker %q, want mac", chosen.ID)
	}
}

func TestWorkerPoolDoesNotSelectRefreshFailure(t *testing.T) {
	p := NewWorkerPool([]WorkerTarget{{ID: "offline", Client: unavailableWorkerClient{}}})
	if err := p.Refresh(context.Background()); err == nil {
		t.Fatal("refresh unexpectedly succeeded")
	}
	if _, err := p.Select(WorkerRequirements{}); err == nil {
		t.Fatal("offline Worker remained eligible after refresh failure")
	}
}
