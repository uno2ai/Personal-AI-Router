//go:build !windows

package main

import (
	"fmt"
	"os"
	"syscall"
)

type taskIndexLock struct{ file *os.File }

func acquireTaskIndexLock(path string) (*taskIndexLock, error) {
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open task index lock: %w", err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		return nil, fmt.Errorf("task index is already open: %w", err)
	}
	return &taskIndexLock{file: lock}, nil
}

func releaseTaskIndexLock(lock *taskIndexLock) error {
	if lock == nil || lock.file == nil {
		return nil
	}
	err := syscall.Flock(int(lock.file.Fd()), syscall.LOCK_UN)
	closeErr := lock.file.Close()
	if err == nil {
		err = closeErr
	}
	return err
}
