//go:build windows

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var childJobs sync.Map

func prepareChildProcess(cmd *exec.Cmd, cwd string) (func(), func() error, error) {
	cmd.Dir = cwd
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	// Assign the suspended child before it can create descendants outside its job.
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED
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
		return resumeChildProcess(uint32(cmd.Process.Pid))
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

// Go closes CreateProcess's primary thread handle. Locate that sole suspended
// thread through the documented Toolhelp API, then resume after job assignment.
func resumeChildProcess(pid uint32) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	for err := windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		if entry.OwnerProcessID != pid {
			continue
		}
		thread, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		if err != nil {
			return err
		}
		_, resumeErr := windows.ResumeThread(thread)
		closeErr := windows.CloseHandle(thread)
		if resumeErr != nil {
			return resumeErr
		}
		return closeErr
	}
	return errors.New("suspended app-server thread was not found")
}
