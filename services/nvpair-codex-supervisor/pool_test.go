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

func TestWorkerPoolReplacesAndRemovesLocalTarget(t *testing.T) {
	remote := WorkerTarget{ID: "remote", Client: fakeWorkerClient{}}
	pool := NewWorkerPool([]WorkerTarget{remote})
	localOne := WorkerTarget{ID: "local", Client: fakeWorkerClient{}}
	pool.SetLocalTarget(&localOne)
	if got := len(pool.Snapshot()); got != 2 {
		t.Fatalf("pool size after local add=%d, want 2", got)
	}
	localTwo := WorkerTarget{ID: "local", Client: unavailableWorkerClient{}}
	pool.SetLocalTarget(&localTwo)
	snapshot := pool.Snapshot()
	if len(snapshot) != 2 || snapshot[0].ID != "local" || snapshot[1].ID != "remote" {
		t.Fatalf("snapshot after local replacement=%#v", snapshot)
	}
	if snapshot[0].Client == nil {
		t.Fatal("replacement removed local client")
	}
	pool.SetLocalTarget(nil)
	snapshot = pool.Snapshot()
	if len(snapshot) != 1 || snapshot[0].ID != "remote" {
		t.Fatalf("snapshot after local removal=%#v", snapshot)
	}
}

func TestWorkerPoolReplacesOnlyDiscoveredTargets(t *testing.T) {
	pool := NewWorkerPool([]WorkerTarget{
		{ID: "local", Source: workerSourceLocal, Client: fakeWorkerClient{}},
		{ID: "static", Source: workerSourceStatic, Client: fakeWorkerClient{}},
		{ID: "old-remote", Source: workerSourceDiscovery, Client: fakeWorkerClient{}},
	})
	pool.ReplaceDiscoveredTargets([]WorkerTarget{{ID: "new-remote", Client: fakeWorkerClient{}}})
	snapshot := pool.Snapshot()
	if len(snapshot) != 3 {
		t.Fatalf("snapshot=%#v, want local, static, new-remote", snapshot)
	}
	if snapshot[0].ID != "local" || snapshot[1].ID != "new-remote" || snapshot[2].ID != "static" {
		t.Fatalf("snapshot order/content=%#v", snapshot)
	}
	if snapshot[1].Source != workerSourceDiscovery {
		t.Fatalf("discovered replacement source=%q", snapshot[1].Source)
	}
}
