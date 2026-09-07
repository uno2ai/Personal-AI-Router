// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"nvpair-shared/protectedfile"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestProtectedFileCLI(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "broker")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	if output, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, output)
	}
	run := func(operation, path string, input []byte) ([]byte, []byte, error) {
		cmd := exec.Command(binary, "--protected-file", operation, "--protected-path", path)
		cmd.Stdin = bytes.NewReader(input)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		return stdout.Bytes(), stderr.Bytes(), err
	}
	file := filepath.Join(t.TempDir(), "space & ' $ config.json")
	data := []byte("private-fixture")
	stdout, stderr, err := run("write", file, data)
	if err != nil {
		t.Fatalf("write failed: %v %s", err, stderr)
	}
	if len(stdout) != 0 || len(stderr) != 0 {
		t.Fatal("write emitted output")
	}
	if err := protectedfile.Check(file); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err = run("read", file, nil)
	if err != nil || !bytes.Equal(stdout, data) || len(stderr) != 0 {
		t.Fatal("read did not round trip")
	}
	original := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(original, data, 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, _, err = run("read-owned-input", original, nil)
	if err != nil || !bytes.Equal(stdout, data) {
		t.Fatal("owned input rejected")
	}
	_, stderr, err = run("write", file, bytes.Repeat([]byte("x"), (4<<20)+1))
	if err == nil || len(stderr) > 150 {
		t.Fatal("oversize input not safely rejected")
	}
	stdout, _, err = run("read", file, nil)
	if err != nil || !bytes.Equal(stdout, data) {
		t.Fatal("oversize input changed destination")
	}
	destinationDirectory := filepath.Join(filepath.Dir(file), "occupied")
	if err := os.Mkdir(destinationDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	_, _, err = run("write", destinationDirectory, data)
	if err == nil {
		t.Fatal("directory destination accepted")
	}
	entries, err := os.ReadDir(filepath.Dir(file))
	if err != nil || len(entries) != 2 {
		t.Fatal("failed stage leaked")
	}
}
