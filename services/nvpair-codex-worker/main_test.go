// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"strings"
	"testing"
)

func TestVersionFlagIsDefined(t *testing.T) {
	if Version == "" {
		t.Fatal("version must be linkable by services/build.sh")
	}
}

func TestWorkerListenMustBeLoopback(t *testing.T) {
	for _, address := range []string{"0.0.0.0:14324", ":14324", "192.0.2.10:14324"} {
		if err := validateLoopbackListen(address); err == nil {
			t.Errorf("accepted non-loopback address %q", address)
		}
	}
	for _, address := range []string{"127.0.0.1:14324", "[::1]:14324"} {
		if err := validateLoopbackListen(address); err != nil {
			t.Errorf("rejected loopback address %q: %v", address, err)
		}
	}
}

func TestWorkerListenErrorDoesNotExposeUnexpectedAddress(t *testing.T) {
	err := validateLoopbackListen("203.0.113.10:14324")
	if err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("unexpected validation error: %v", err)
	}
}
