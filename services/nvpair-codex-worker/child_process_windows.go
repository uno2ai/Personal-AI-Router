//go:build windows

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"os"
	"os/exec"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var childJobs sync.Map

func prepareChildProcess(cmd *exec.Cmd, cwd string) (func(), func() error, error) {
	cmd.Dir = cwd
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, nil, err
	}
	var limits windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		_ = windows.CloseHandle(job)
		return nil, nil, err
	}
	attached := false
	cleanup := func() {
		if !attached {
			_ = windows.CloseHandle(job)
		}
	}
	verify := func() error {
		process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
		if err != nil {
			return err
		}
		defer windows.CloseHandle(process)
		if err := windows.AssignProcessToJobObject(job, process); err != nil {
			return err
		}
		childJobs.Store(cmd, job)
		attached = true
		return nil
	}
	return cleanup, verify, nil
}

func terminateChild(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	if value, ok := childJobs.LoadAndDelete(cmd); ok {
		job := value.(windows.Handle)
		err := windows.TerminateJobObject(job, 1)
		closeErr := windows.CloseHandle(job)
		if err != nil && !errors.Is(err, windows.ERROR_PROCESS_ABORTED) {
			return err
		}
		return closeErr
	}
	if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}

func releaseChildProcess(cmd *exec.Cmd) {
	if value, ok := childJobs.LoadAndDelete(cmd); ok {
		_ = windows.CloseHandle(value.(windows.Handle))
	}
}
