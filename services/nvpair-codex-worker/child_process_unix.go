//go:build !windows

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

func prepareChildProcess(cmd *exec.Cmd, cwd string) (func(), func() error, error) {
	// Retain a no-follow directory descriptor while the child is spawned. Go's
	// os/exec changes directory before installing ExtraFiles, so the descriptor
	// cannot be used as cmd.Dir directly; verify the name resolves to this same
	// object immediately after Start and fence the child on drift.
	fd, err := unix.Open(cwd, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, nil, err
	}
	directory := os.NewFile(uintptr(fd), cwd)
	info, err := directory.Stat()
	if err != nil {
		_ = directory.Close()
		return nil, nil, err
	}
	cmd.Dir = cwd
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cleanup := func() { _ = directory.Close() }
	verify := func() error {
		current, err := os.Stat(cwd)
		if err != nil {
			return err
		}
		if !os.SameFile(info, current) {
			return fmt.Errorf("working directory changed during spawn")
		}
		return nil
	}
	return cleanup, verify, nil
}

func terminateChild(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if err == syscall.ESRCH {
		return nil
	}
	if err == syscall.EPERM {
		// Some hardened macOS configurations reject process-group signalling
		// even for a child we own. Still terminate the tracked child rather
		// than releasing its lease as if cleanup had succeeded; the caller's
		// wait confirms this direct process is gone.
		if directErr := cmd.Process.Kill(); directErr != nil && !errors.Is(directErr, os.ErrProcessDone) {
			return directErr
		}
		return nil
	}
	return err
}

func releaseChildProcess(*exec.Cmd) {}
