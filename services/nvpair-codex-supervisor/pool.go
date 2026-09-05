// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"nvpair-shared/codexprotocol"
)

type WorkerTarget struct {
	ID           string
	Source       string
	Client       WorkerClient
	Capabilities codexprotocol.WorkerCapabilities
	Assigned     uint64
	LastError    string
}

const (
	workerSourceLocal     = "local"
	workerSourceStatic    = "static"
	workerSourceDiscovery = "discovery"
)

type WorkerRequirements struct {
	WorkerID     string
	OS           string
	Architecture string
	Workspace    string
	Mode         string
	Tools        []string
}

type WorkerPool struct {
	mu      sync.Mutex
	targets []WorkerTarget
}

func NewWorkerPool(targets []WorkerTarget) *WorkerPool {
	copyTargets := append([]WorkerTarget(nil), targets...)
	for i := range copyTargets {
		if copyTargets[i].Source == "" {
			if copyTargets[i].ID == "local" {
				copyTargets[i].Source = workerSourceLocal
			} else {
				copyTargets[i].Source = workerSourceStatic
			}
		}
	}
	sort.Slice(copyTargets, func(i, j int) bool { return copyTargets[i].ID < copyTargets[j].ID })
	return &WorkerPool{targets: copyTargets}
}

func (p *WorkerPool) Refresh(ctx context.Context) error {
	p.mu.Lock()
	targets := append([]WorkerTarget(nil), p.targets...)
	p.mu.Unlock()
	var firstErr error
	for i := range targets {
		raw, err := targets[i].Client.Worker(ctx)
		if err != nil {
			targets[i].LastError = err.Error()
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		caps, err := decodeWorkerCapabilities(raw)
		if err != nil {
			targets[i].LastError = err.Error()
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		targets[i].Capabilities = caps
		targets[i].LastError = ""
	}
	p.mu.Lock()
	for _, updated := range targets {
		for i := range p.targets {
			if p.targets[i].ID == updated.ID {
				updated.Assigned = p.targets[i].Assigned
				p.targets[i] = updated
			}
		}
	}
	p.mu.Unlock()
	return firstErr
}

func (p *WorkerPool) Select(req WorkerRequirements) (WorkerTarget, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	eligible := make([]WorkerTarget, 0, len(p.targets))
	for _, target := range p.targets {
		if !targetEligible(target, req) {
			continue
		}
		eligible = append(eligible, target)
	}
	if len(eligible) == 0 {
		return WorkerTarget{}, errors.New("no eligible Worker matches the requested capabilities")
	}
	sort.SliceStable(eligible, func(i, j int) bool {
		left, right := eligible[i], eligible[j]
		leftLocal := hasString(left.Capabilities.WorkspaceAliases, req.Workspace)
		rightLocal := hasString(right.Capabilities.WorkspaceAliases, req.Workspace)
		if leftLocal != rightLocal {
			return leftLocal
		}
		if left.Capabilities.AvailableSlots != right.Capabilities.AvailableSlots {
			return left.Capabilities.AvailableSlots > right.Capabilities.AvailableSlots
		}
		if left.Assigned != right.Assigned {
			return left.Assigned < right.Assigned
		}
		return left.ID < right.ID
	})
	chosen := eligible[0]
	for i := range p.targets {
		if p.targets[i].ID == chosen.ID {
			p.targets[i].Assigned++
			chosen.Assigned = p.targets[i].Assigned
			break
		}
	}
	return chosen, nil
}

func (p *WorkerPool) Snapshot() []WorkerTarget {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]WorkerTarget(nil), p.targets...)
}

// SetLocalTarget atomically replaces the broker-owned local Worker entry while
// preserving discovered remote Workers. A nil target means the local runtime
// is unavailable or its descriptor is invalid.
func (p *WorkerPool) SetLocalTarget(target *WorkerTarget) {
	p.mu.Lock()
	defer p.mu.Unlock()
	filtered := p.targets[:0]
	for _, current := range p.targets {
		if current.ID != "local" {
			filtered = append(filtered, current)
		}
	}
	p.targets = filtered
	if target != nil {
		target.Source = workerSourceLocal
		p.targets = append(p.targets, *target)
	}
	sort.Slice(p.targets, func(i, j int) bool { return p.targets[i].ID < p.targets[j].ID })
}

// ReplaceDiscoveredTargets updates only the discovery-backed remote set. Local
// runtime and explicitly configured endpoint targets remain intact, while a
// disappeared or de-pinned discovered Worker is removed from new scheduling.
// Existing task owners are retained separately by MCPServer for status/result
// recovery; this pool operation only controls future selection.
func (p *WorkerPool) ReplaceDiscoveredTargets(targets []WorkerTarget) {
	p.mu.Lock()
	defer p.mu.Unlock()
	assigned := make(map[string]uint64)
	for _, current := range p.targets {
		if current.Source == workerSourceDiscovery {
			assigned[current.ID] = current.Assigned
		}
	}
	retained := p.targets[:0]
	for _, current := range p.targets {
		if current.Source != workerSourceDiscovery {
			retained = append(retained, current)
		}
	}
	for _, target := range targets {
		target.Source = workerSourceDiscovery
		target.Assigned = assigned[target.ID]
		retained = append(retained, target)
	}
	p.targets = retained
	sort.Slice(p.targets, func(i, j int) bool { return p.targets[i].ID < p.targets[j].ID })
}

func targetEligible(target WorkerTarget, req WorkerRequirements) bool {
	if target.LastError != "" {
		return false
	}
	if req.WorkerID != "" && req.WorkerID != target.ID {
		return false
	}
	caps := target.Capabilities
	if caps.AvailableSlots == 0 && caps.MaxConcurrency > 0 {
		return false
	}
	if req.OS != "" && req.OS != caps.OS {
		return false
	}
	if req.Architecture != "" && req.Architecture != caps.Architecture {
		return false
	}
	if req.Workspace != "" && len(caps.WorkspaceAliases) > 0 && !hasString(caps.WorkspaceAliases, req.Workspace) {
		return false
	}
	if req.Mode != "" && len(caps.WorkspaceModes) > 0 && !hasString(caps.WorkspaceModes, req.Mode) {
		return false
	}
	for _, required := range req.Tools {
		if !hasString(caps.ToolLabels, required) {
			return false
		}
	}
	return true
}

func hasString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func decodeWorkerCapabilities(raw json.RawMessage) (codexprotocol.WorkerCapabilities, error) {
	var response struct {
		ProtocolVersion int                              `json:"protocolVersion"`
		Version         string                           `json:"version"`
		Capabilities    codexprotocol.WorkerCapabilities `json:"capabilities"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return codexprotocol.WorkerCapabilities{}, err
	}
	caps := response.Capabilities
	if caps.ProtocolVersion == 0 {
		caps.ProtocolVersion = response.ProtocolVersion
	}
	if caps.WorkerVersion == "" {
		caps.WorkerVersion = response.Version
	}
	// A legacy/local test double that predates capability publication remains
	// selectable, but real Workers always fill these fields and pass strict
	// validation.
	if caps.AppServerVersion == "" {
		caps.AppServerVersion = "unknown"
	}
	if caps.OS == "" {
		caps.OS = "unknown"
	}
	if caps.Architecture == "" {
		caps.Architecture = "unknown"
	}
	if caps.MaxConcurrency == 0 {
		caps.MaxConcurrency = 1
	}
	if caps.AvailableSlots == 0 && len(caps.WorkspaceAliases) == 0 && len(caps.ToolLabels) == 0 {
		caps.AvailableSlots = caps.MaxConcurrency
	}
	if caps.ProtocolVersion != codexprotocol.ProtocolVersion || strings.TrimSpace(caps.WorkerVersion) == "" {
		return codexprotocol.WorkerCapabilities{}, fmt.Errorf("Worker capabilities are incompatible")
	}
	if err := caps.Validate(); err != nil {
		return codexprotocol.WorkerCapabilities{}, fmt.Errorf("Worker capabilities are invalid: %w", err)
	}
	return caps, nil
}
