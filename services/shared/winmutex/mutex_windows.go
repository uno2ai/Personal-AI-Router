//go:build windows

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package winmutex owns each Windows mutex on a dedicated OS thread.
package winmutex

import (
	"errors"
	"golang.org/x/sys/windows"
	"runtime"
	"sync"
)

var ErrBusy = errors.New("mutex is already owned")

type Mutex struct {
	release chan struct{}
	done    chan struct{}
	once    sync.Once
	err     error
}

// Acquire performs a nonblocking wait, including recovery of an abandoned owner.
func Acquire(name string) (*Mutex, error) {
	m := &Mutex{release: make(chan struct{}), done: make(chan struct{})}
	ready := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		defer close(m.done)
		encoded, err := windows.UTF16PtrFromString(name)
		if err != nil {
			ready <- err
			return
		}
		handle, err := windows.CreateMutex(nil, false, encoded)
		if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			ready <- err
			return
		}
		defer windows.CloseHandle(handle)
		result, err := windows.WaitForSingleObject(handle, 0)
		if err != nil {
			ready <- err
			return
		}
		if result != windows.WAIT_OBJECT_0 && result != windows.WAIT_ABANDONED {
			ready <- ErrBusy
			return
		}
		ready <- nil
		<-m.release
		m.err = windows.ReleaseMutex(handle)
	}()
	if err := <-ready; err != nil {
		<-m.done
		return nil, err
	}
	return m, nil
}

func (m *Mutex) Close() error {
	if m == nil {
		return nil
	}
	m.once.Do(func() { close(m.release) })
	<-m.done
	return m.err
}
