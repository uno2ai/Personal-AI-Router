// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import "testing"

func TestVersionFlagIsDefined(t *testing.T) {
	if Version == "" {
		t.Fatal("version must be linkable by services/build.sh")
	}
}
