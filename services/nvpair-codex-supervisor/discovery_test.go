// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"nvpair-shared/codexprotocol"
	"nvpair-shared/noderec"
)

func TestCollisionQuarantineRequiresTwoConsistentPinnedProbes(t *testing.T) {
	q := NewCollisionQuarantine()
	probe := DiscoveryProbe{HostUUID: "host-1", AdvertisedClusterUUID: "principal-1", Principal: "principal-1", CurrentPin: true}
	if q.Observe(probe) {
		t.Fatal("one successful probe must remain quarantined")
	}
	if !q.Observe(probe) {
		t.Fatal("two identical successful probes must clear quarantine")
	}
	if q.Observe(DiscoveryProbe{HostUUID: "host-1", AdvertisedClusterUUID: "principal-2", Principal: "principal-2", CurrentPin: true}) {
		t.Fatal("a different authenticated principal must re-quarantine the host")
	}
	if q.Observe(DiscoveryProbe{HostUUID: "host-1", AdvertisedClusterUUID: "principal-1", Principal: "principal-1", CurrentPin: false}) {
		t.Fatal("a de-pinned probe must never clear quarantine")
	}
}

func TestFailoverWorkerClientPreservesAdvertisedAddressOrder(t *testing.T) {
	first := &sequenceWorkerClient{workerErr: errors.New("first address down")}
	second := &sequenceWorkerClient{worker: json.RawMessage(`{"protocolVersion":1,"version":"test"}`)}
	client := &failoverWorkerClient{clients: []WorkerClient{first, second}}
	got, err := client.Worker(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(second.worker) || first.workerCalls != 1 || second.workerCalls != 1 {
		t.Fatalf("failover result=%s calls=(%d,%d)", got, first.workerCalls, second.workerCalls)
	}
}

func TestWorkerAddressesUsesOnlyNodeRecordCandidates(t *testing.T) {
	node := noderec.DirectoryNode{IP: "192.0.2.10", IPs: []string{"192.0.2.10", "198.51.100.2", "not-an-ip", "203.0.113.5"}}
	want := []string{"192.0.2.10", "198.51.100.2", "203.0.113.5"}
	if got := workerAddresses(node); !reflect.DeepEqual(got, want) {
		t.Fatalf("addresses=%v, want %v", got, want)
	}
}

type sequenceWorkerClient struct {
	worker      json.RawMessage
	workerErr   error
	workerCalls int
}

func (c *sequenceWorkerClient) Worker(context.Context) (json.RawMessage, error) {
	c.workerCalls++
	return c.worker, c.workerErr
}

func (c *sequenceWorkerClient) Create(context.Context, codexprotocol.TaskRequest) (json.RawMessage, error) {
	return nil, errors.New("not implemented")
}

func (c *sequenceWorkerClient) Status(context.Context, string) (json.RawMessage, error) {
	return nil, errors.New("not implemented")
}

func (c *sequenceWorkerClient) Result(context.Context, string) (json.RawMessage, error) {
	return nil, errors.New("not implemented")
}

func (c *sequenceWorkerClient) Cancel(context.Context, codexprotocol.Mutation) (json.RawMessage, error) {
	return nil, errors.New("not implemented")
}

func (c *sequenceWorkerClient) Artifact(context.Context, string, string) ([]byte, error) {
	return nil, errors.New("not implemented")
}
