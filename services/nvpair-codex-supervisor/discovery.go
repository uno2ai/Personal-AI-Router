// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"
	"sync"

	"nvpair-shared/clustertrust"
	"nvpair-shared/codexprotocol"
	"nvpair-shared/noderec"
)

// DiscoveryProbe is the authenticated result used to correlate an mDNS hint
// with the certificate principal that actually answered it. HostUUID is a
// correlation key only; Principal is the security identity.
type DiscoveryProbe struct {
	HostUUID              string
	AdvertisedClusterUUID string
	Principal             string
	CurrentPin            bool
}

type quarantineObservation struct {
	probe       DiscoveryProbe
	consecutive int
}

// CollisionQuarantine keeps a host UUID out of scheduling while its mDNS
// correlation is changing or disagreeing with authenticated Worker responses.
// Two consecutive identical, pinned probes are required to clear it.
type CollisionQuarantine struct {
	mu    sync.Mutex
	state map[string]quarantineObservation
}

func NewCollisionQuarantine() *CollisionQuarantine {
	return &CollisionQuarantine{state: make(map[string]quarantineObservation)}
}

// Observe returns true only after two consecutive successful probes agree on
// host UUID, advertised cluster UUID, authenticated principal, and current
// pin. A mismatch resets the observation and remains quarantined.
func (q *CollisionQuarantine) Observe(probe DiscoveryProbe) bool {
	if q == nil || strings.TrimSpace(probe.HostUUID) == "" ||
		strings.TrimSpace(probe.AdvertisedClusterUUID) == "" ||
		strings.TrimSpace(probe.Principal) == "" || !probe.CurrentPin ||
		probe.Principal != probe.AdvertisedClusterUUID {
		return false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	previous, ok := q.state[probe.HostUUID]
	if !ok || previous.probe != probe {
		q.state[probe.HostUUID] = quarantineObservation{probe: probe, consecutive: 1}
		return false
	}
	previous.consecutive++
	q.state[probe.HostUUID] = previous
	return previous.consecutive >= 2
}

func (q *CollisionQuarantine) IsQuarantined(hostUUID string) bool {
	if q == nil {
		return true
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	observation, ok := q.state[hostUUID]
	return !ok || observation.consecutive < 2
}

// failoverWorkerClient walks the node's advertised IP order. It never probes
// an unadvertised port: the port came from the cw TXT key and each address is
// only an address hint for that exact service.
type failoverWorkerClient struct {
	clients []WorkerClient
}

func (c *failoverWorkerClient) Worker(ctx context.Context) (json.RawMessage, error) {
	return c.tryJSON(ctx, func(client WorkerClient) (json.RawMessage, error) { return client.Worker(ctx) })
}

func (c *failoverWorkerClient) Create(ctx context.Context, request codexprotocol.TaskRequest) (json.RawMessage, error) {
	return c.tryJSON(ctx, func(client WorkerClient) (json.RawMessage, error) { return client.Create(ctx, request) })
}

func (c *failoverWorkerClient) Status(ctx context.Context, taskID string) (json.RawMessage, error) {
	return c.tryJSON(ctx, func(client WorkerClient) (json.RawMessage, error) { return client.Status(ctx, taskID) })
}

func (c *failoverWorkerClient) Result(ctx context.Context, taskID string) (json.RawMessage, error) {
	return c.tryJSON(ctx, func(client WorkerClient) (json.RawMessage, error) { return client.Result(ctx, taskID) })
}

func (c *failoverWorkerClient) Cancel(ctx context.Context, mutation codexprotocol.Mutation) (json.RawMessage, error) {
	return c.tryJSON(ctx, func(client WorkerClient) (json.RawMessage, error) { return client.Cancel(ctx, mutation) })
}

func (c *failoverWorkerClient) Artifact(ctx context.Context, taskID, artifactID string) ([]byte, error) {
	var lastErr error
	for _, client := range c.clients {
		data, err := client.Artifact(ctx, taskID, artifactID)
		if err == nil {
			return data, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("Worker has no advertised addresses")
	}
	return nil, lastErr
}

func (c *failoverWorkerClient) tryJSON(ctx context.Context, call func(WorkerClient) (json.RawMessage, error)) (json.RawMessage, error) {
	var lastErr error
	for _, client := range c.clients {
		data, err := call(client)
		if err == nil {
			return data, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("Worker has no advertised addresses")
	}
	return nil, lastErr
}

// WorkerDiscovery converts existing PAIR DirectoryNode snapshots into
// authenticated Supervisor targets. It is deliberately a consumer of the
// existing node-scanner output; it does not run a fixed-port scan.
type WorkerDiscovery struct {
	mesh       *clustertrust.Mesh
	quarantine *CollisionQuarantine
}

func NewWorkerDiscovery(mesh *clustertrust.Mesh) *WorkerDiscovery {
	return &WorkerDiscovery{mesh: mesh, quarantine: NewCollisionQuarantine()}
}

func (d *WorkerDiscovery) Discover(ctx context.Context, nodes []noderec.DirectoryNode) ([]WorkerTarget, error) {
	if d == nil || d.mesh == nil {
		return nil, errors.New("Worker discovery requires a cluster mesh")
	}
	d.mesh.Refresh()
	sorted := append([]noderec.DirectoryNode(nil), nodes...)
	sort.SliceStable(sorted, func(i, j int) bool {
		left := sorted[i].ClusterUUID + "\x00" + sorted[i].HostUUID
		right := sorted[j].ClusterUUID + "\x00" + sorted[j].HostUUID
		return left < right
	})
	targets := make([]WorkerTarget, 0, len(sorted))
	var failures []string
	for _, node := range sorted {
		service, advertised := node.Services[noderec.ServiceCodexWorker]
		port := service.Port
		if !advertised || port <= 0 || node.HostUUID == "" || node.ClusterUUID == "" || !node.Trusted {
			continue
		}
		if !d.mesh.HasPin(node.ClusterUUID) {
			continue
		}
		addresses := workerAddresses(node)
		clients := make([]WorkerClient, 0, len(addresses))
		for _, address := range addresses {
			rawURL := (&url.URL{Scheme: "https", Host: net.JoinHostPort(address, fmt.Sprint(port))}).String()
			client, err := NewMTLSWorkerClient(rawURL, d.mesh, node.ClusterUUID)
			if err != nil {
				failures = append(failures, node.HostUUID+": "+err.Error())
				continue
			}
			clients = append(clients, client)
		}
		if len(clients) == 0 {
			continue
		}
		client := &failoverWorkerClient{clients: clients}
		raw, err := client.Worker(ctx)
		if err != nil {
			failures = append(failures, node.HostUUID+": "+err.Error())
			continue
		}
		var response struct {
			Principal string `json:"principal"`
		}
		if err := json.Unmarshal(raw, &response); err != nil {
			failures = append(failures, node.HostUUID+": invalid Worker identity")
			continue
		}
		probe := DiscoveryProbe{HostUUID: node.HostUUID, AdvertisedClusterUUID: node.ClusterUUID, Principal: response.Principal, CurrentPin: d.mesh.HasPin(response.Principal)}
		if !d.quarantine.Observe(probe) {
			continue
		}
		targets = append(targets, WorkerTarget{ID: response.Principal, Client: client})
	}
	if len(targets) == 0 && len(failures) > 0 {
		return nil, fmt.Errorf("no discoverable Codex Workers: %s", strings.Join(failures, "; "))
	}
	return targets, nil
}

func workerAddresses(node noderec.DirectoryNode) []string {
	addresses := make([]string, 0, len(node.IPs)+1)
	seen := make(map[string]bool)
	appendAddress := func(value string) {
		value = strings.TrimSpace(value)
		if net.ParseIP(value) == nil || seen[value] {
			return
		}
		seen[value] = true
		addresses = append(addresses, value)
	}
	appendAddress(node.IP)
	for _, address := range node.IPs {
		appendAddress(address)
	}
	return addresses
}
