//go:build windows

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

type taskIndexLock struct{ handle windows.Handle }

func acquireTaskIndexLock(path string) (*taskIndexLock, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve task index lock path: %w", err)
	}
	digest := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(absolute))))
	name, err := windows.UTF16PtrFromString("Local\\NVPAIR-CodexSupervisor-" + hex.EncodeToString(digest[:]))
	if err != nil {
		return nil, fmt.Errorf("encode task index lock name: %w", err)
	}
	handle, err := windows.CreateMutex(nil, true, name)
	if err != nil {
		if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			if handle != 0 {
				_ = windows.CloseHandle(handle)
			}
			return nil, errors.New("task index is already open")
		}
		return nil, fmt.Errorf("create task index lock: %w", err)
	}
	return &taskIndexLock{handle: handle}, nil
}

func releaseTaskIndexLock(lock *taskIndexLock) error {
	if lock == nil || lock.handle == 0 {
		return nil
	}
	err := windows.ReleaseMutex(lock.handle)
	if closeErr := windows.CloseHandle(lock.handle); err == nil {
		err = closeErr
	}
	return err
}
