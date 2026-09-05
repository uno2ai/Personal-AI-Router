// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"time"

	"nvpair-shared/codexruntime"
)

func loadLocalRuntimeTarget(path string, now time.Time) (*WorkerTarget, error) {
	descriptor, err := codexruntime.ReadDescriptor(path, now)
	if err != nil {
		return nil, err
	}
	if descriptor.Transport != codexruntime.TransportPinnedLocalTLS {
		return nil, fmt.Errorf("local runtime descriptor uses unsupported transport %q", descriptor.Transport)
	}
	switch descriptor.State {
	case codexruntime.RuntimeStateReady, codexruntime.RuntimeStateBusy:
		// Selectable states.
	default:
		return nil, nil
	}
	client, err := NewPinnedLocalWorkerClient(descriptor.Endpoint, descriptor.CredentialRef, descriptor.ServerCertificateSHA256, descriptor.CredentialGeneration)
	if err != nil {
		return nil, fmt.Errorf("load local Worker client: %w", err)
	}
	return &WorkerTarget{ID: "local", Client: client}, nil
}
