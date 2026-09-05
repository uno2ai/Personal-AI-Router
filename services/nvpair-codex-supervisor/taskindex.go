// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"nvpair-shared/codexprotocol"
)

const taskIndexVersion = 1

var (
	ErrDispatchAlreadyIndexed = errors.New("task already has a dispatch intent")
	ErrDispatchNotFound       = errors.New("dispatch intent not found")
)

type DispatchState string

const (
	DispatchPending      DispatchState = "pending"
	DispatchAcknowledged DispatchState = "acknowledged"
	DispatchUncertain    DispatchState = "uncertain"
	DispatchTerminal     DispatchState = "terminal"
)

type TaskIntent struct {
	TaskID     string                    `json:"taskId"`
	RequestID  string                    `json:"requestId"`
	AttemptID  string                    `json:"attemptId"`
	LeaseEpoch uint64                    `json:"leaseEpoch"`
	WorkerID   string                    `json:"workerId"`
	State      DispatchState             `json:"state"`
	Request    codexprotocol.TaskRequest `json:"request"`
	LastError  string                    `json:"lastError,omitempty"`
	CreatedAt  time.Time                 `json:"createdAt"`
	UpdatedAt  time.Time                 `json:"updatedAt"`
}

type TaskMetadata struct {
	TaskID     string        `json:"taskId"`
	RequestID  string        `json:"requestId"`
	AttemptID  string        `json:"attemptId"`
	LeaseEpoch uint64        `json:"leaseEpoch"`
	WorkerID   string        `json:"workerId"`
	State      DispatchState `json:"state"`
	LastError  string        `json:"lastError,omitempty"`
	CreatedAt  time.Time     `json:"createdAt"`
	UpdatedAt  time.Time     `json:"updatedAt"`
}

type taskIndexEntry struct {
	Version  int        `json:"version"`
	Sequence uint64     `json:"sequence"`
	Kind     string     `json:"kind"`
	Intent   TaskIntent `json:"intent"`
}

type TaskIndex struct {
	mu       sync.Mutex
	file     *os.File
	lock     *taskIndexLock
	path     string
	sequence uint64
	intents  map[string]TaskIntent
	closed   bool
}

func OpenTaskIndex(path string) (*TaskIndex, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("task index path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create task index directory: %w", err)
	}
	lock, err := acquireTaskIndexLock(path)
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		_ = releaseTaskIndexLock(lock)
		return nil, fmt.Errorf("open task index: %w", err)
	}
	entries, err := readTaskIndex(path)
	if err != nil {
		_ = file.Close()
		_ = releaseTaskIndexLock(lock)
		return nil, err
	}
	index := &TaskIndex{file: file, lock: lock, path: path, intents: make(map[string]TaskIntent)}
	for _, entry := range entries {
		if entry.Sequence > index.sequence {
			index.sequence = entry.Sequence
		}
		index.intents[entry.Intent.TaskID] = entry.Intent
	}
	return index, nil
}

func (i *TaskIndex) Begin(intent TaskIntent) error {
	if intent.State == "" {
		intent.State = DispatchPending
	}
	if err := intent.Validate(); err != nil {
		return err
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed {
		return errors.New("task index is closed")
	}
	if _, exists := i.intents[intent.TaskID]; exists {
		return ErrDispatchAlreadyIndexed
	}
	now := time.Now().UTC()
	intent.State = DispatchPending
	intent.CreatedAt = now
	intent.UpdatedAt = now
	return i.appendLocked("begin", intent)
}

func (i *TaskIndex) MarkAcknowledged(taskID, workerID string) error {
	if strings.TrimSpace(workerID) == "" {
		return errors.New("workerId is required")
	}
	return i.update(taskID, func(intent *TaskIntent) error {
		if intent.State != DispatchPending {
			return fmt.Errorf("cannot acknowledge dispatch in state %q", intent.State)
		}
		intent.WorkerID = workerID
		intent.State = DispatchAcknowledged
		intent.LastError = ""
		return nil
	})
}

func (i *TaskIndex) MarkUncertain(taskID, reason string) error {
	return i.update(taskID, func(intent *TaskIntent) error {
		if intent.State == DispatchTerminal {
			return fmt.Errorf("cannot mark terminal dispatch uncertain")
		}
		intent.State = DispatchUncertain
		intent.LastError = strings.TrimSpace(reason)
		if intent.LastError == "" {
			intent.LastError = "dispatch outcome is unknown"
		}
		return nil
	})
}

func (i *TaskIndex) MarkTerminal(taskID string) error {
	return i.update(taskID, func(intent *TaskIntent) error {
		intent.State = DispatchTerminal
		intent.LastError = ""
		return nil
	})
}

func (i *TaskIndex) Get(taskID string) (TaskIntent, bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	intent, ok := i.intents[taskID]
	return intent, ok
}

func (i *TaskIndex) Snapshot() []TaskIntent {
	i.mu.Lock()
	defer i.mu.Unlock()
	intents := make([]TaskIntent, 0, len(i.intents))
	for _, intent := range i.intents {
		intents = append(intents, intent)
	}
	sort.Slice(intents, func(left, right int) bool { return intents[left].TaskID < intents[right].TaskID })
	return intents
}

func (i *TaskIndex) SnapshotMetadata() []TaskMetadata {
	i.mu.Lock()
	defer i.mu.Unlock()
	metadata := make([]TaskMetadata, 0, len(i.intents))
	for _, intent := range i.intents {
		metadata = append(metadata, TaskMetadata{
			TaskID: intent.TaskID, RequestID: intent.RequestID, AttemptID: intent.AttemptID,
			LeaseEpoch: intent.LeaseEpoch, WorkerID: intent.WorkerID, State: intent.State,
			LastError: intent.LastError, CreatedAt: intent.CreatedAt, UpdatedAt: intent.UpdatedAt,
		})
	}
	sort.Slice(metadata, func(left, right int) bool { return metadata[left].CreatedAt.Before(metadata[right].CreatedAt) })
	return metadata
}

func (i *TaskIndex) update(taskID string, change func(*TaskIntent) error) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed {
		return errors.New("task index is closed")
	}
	intent, ok := i.intents[taskID]
	if !ok {
		return ErrDispatchNotFound
	}
	if err := change(&intent); err != nil {
		return err
	}
	intent.UpdatedAt = time.Now().UTC()
	return i.appendLocked("state", intent)
}

func (i *TaskIndex) appendLocked(kind string, intent TaskIntent) error {
	i.sequence++
	entry := taskIndexEntry{Version: taskIndexVersion, Sequence: i.sequence, Kind: kind, Intent: intent}
	data, err := json.Marshal(entry)
	if err != nil {
		i.sequence--
		return fmt.Errorf("encode task index entry: %w", err)
	}
	if _, err := i.file.Write(append(data, '\n')); err != nil {
		i.sequence--
		return fmt.Errorf("write task index: %w", err)
	}
	if err := i.file.Sync(); err != nil {
		i.sequence--
		return fmt.Errorf("sync task index: %w", err)
	}
	i.intents[intent.TaskID] = intent
	return nil
}

func (i *TaskIndex) Close() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed {
		return nil
	}
	i.closed = true
	err := i.file.Close()
	if lockErr := releaseTaskIndexLock(i.lock); err == nil {
		err = lockErr
	}
	return err
}

func readTaskIndex(path string) ([]taskIndexEntry, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read task index: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	entries := make([]taskIndexEntry, 0)
	var previous uint64
	for scanner.Scan() {
		var entry taskIndexEntry
		decoder := json.NewDecoder(strings.NewReader(scanner.Text()))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&entry); err != nil {
			return nil, fmt.Errorf("decode task index: %w", err)
		}
		if entry.Version != taskIndexVersion || entry.Sequence == 0 || entry.Sequence <= previous {
			return nil, errors.New("task index sequence or version is invalid")
		}
		if entry.Kind != "begin" && entry.Kind != "state" {
			return nil, fmt.Errorf("unknown task index entry kind %q", entry.Kind)
		}
		if err := entry.Intent.Validate(); err != nil {
			return nil, fmt.Errorf("invalid task index intent: %w", err)
		}
		if entry.Intent.State == DispatchPending && entry.Kind == "state" {
			return nil, errors.New("state entry cannot return to pending")
		}
		previous = entry.Sequence
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan task index: %w", err)
	}
	return entries, nil
}

func (i TaskIntent) Validate() error {
	for name, value := range map[string]string{"taskId": i.TaskID, "requestId": i.RequestID, "attemptId": i.AttemptID, "workerId": i.WorkerID} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	if i.LeaseEpoch == 0 {
		return errors.New("leaseEpoch must be positive")
	}
	if i.Request.TaskID != i.TaskID || i.Request.RequestID != i.RequestID || i.Request.AttemptID != i.AttemptID || i.Request.LeaseEpoch != i.LeaseEpoch {
		return errors.New("task intent and request identity do not match")
	}
	if err := i.Request.Validate(); err != nil {
		return fmt.Errorf("task request: %w", err)
	}
	switch i.State {
	case DispatchPending, DispatchAcknowledged, DispatchUncertain, DispatchTerminal:
	default:
		return fmt.Errorf("invalid dispatch state %q", i.State)
	}
	return nil
}
