// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"nvpair-shared/codexruntime"
)

func TestCodexWorkerStartMessageUsesProtectedConfigReference(t *testing.T) {
	message, err := codexWorkerStartMessage("/protected/codex-worker.json", 12)
	if err != nil {
		t.Fatal(err)
	}
	if message.Kind != codexruntime.ControlKindStart || message.ConfigPath != "/protected/codex-worker.json" || message.ConfigRevision != 12 {
		t.Fatalf("start message = %+v", message)
	}
	if message.WorkspaceAlias != "local" {
		t.Fatalf("workspace alias = %q, want local", message.WorkspaceAlias)
	}
}

func TestCodexWorkerIsNotStartedWithoutConfiguration(t *testing.T) {
	paths := workerPaths{codexWorker: "/bin/worker"}
	if paths.codexWorkerConfig != "" {
		t.Fatal("a path-only Worker configuration unexpectedly enables startup")
	}
}
