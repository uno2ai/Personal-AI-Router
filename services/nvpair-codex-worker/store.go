// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"nvpair-shared/codexprotocol"
)

var (
	ErrRequestConflict = errors.New("request id already exists with different payload")
	ErrWorkspaceBusy   = errors.New("workspace has an active lease")
	ErrTaskBusy        = errors.New("task has an active attempt")
	ErrStaleLease      = errors.New("stale task lease")
	ErrTaskNotFound    = errors.New("task not found")
	ErrTerminalTask    = errors.New("task is terminal")
)

type TaskStore struct {
	mu               sync.Mutex
	journal          *Journal
	records          map[string]codexprotocol.TaskRecord
	events           map[string][]codexprotocol.TaskEvent
	requestHashes    map[string]string
	requestTasks     map[string]string
	requestRecords   map[string]codexprotocol.TaskRecord
	appliedMutations map[string]bool
	workspaceLeases  map[string]string
}

func NewTaskStore(journal *Journal) (*TaskStore, error) {
	if journal == nil {
		return nil, errors.New("journal is required")
	}
	store := &TaskStore{
		journal:          journal,
		records:          make(map[string]codexprotocol.TaskRecord),
		events:           make(map[string][]codexprotocol.TaskEvent),
		requestHashes:    make(map[string]string),
		requestTasks:     make(map[string]string),
		requestRecords:   make(map[string]codexprotocol.TaskRecord),
		appliedMutations: make(map[string]bool),
		workspaceLeases:  make(map[string]string),
	}
	entries, err := journal.Replay()
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		store.apply(entry)
	}
	return store, nil
}

func (s *TaskStore) Accept(request codexprotocol.TaskRequest) (codexprotocol.TaskRecord, bool, error) {
	if err := request.Validate(); err != nil {
		return codexprotocol.TaskRecord{}, false, err
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return codexprotocol.TaskRecord{}, false, fmt.Errorf("encode task request: %w", err)
	}
	hash := sha256.Sum256(payload)
	hashString := hex.EncodeToString(hash[:])
	workspaceKey := request.Workspace.ID + "\x00" + request.Workspace.Path

	s.mu.Lock()
	defer s.mu.Unlock()
	if previousHash, exists := s.requestHashes[request.RequestID]; exists {
		if previousHash != hashString {
			return codexprotocol.TaskRecord{}, false, ErrRequestConflict
		}
		return s.requestRecords[request.RequestID], true, nil
	}
	if owner, exists := s.workspaceLeases[workspaceKey]; exists && owner != request.TaskID {
		return codexprotocol.TaskRecord{}, false, ErrWorkspaceBusy
	}
	if previous, exists := s.records[request.TaskID]; exists {
		if !previous.State.Terminal() {
			return codexprotocol.TaskRecord{}, false, ErrTaskBusy
		}
		if request.LeaseEpoch <= previous.LeaseEpoch {
			return codexprotocol.TaskRecord{}, false, ErrStaleLease
		}
	}
	now := time.Now().UTC()
	record := codexprotocol.TaskRecord{
		Mutation:     request.Mutation,
		State:        codexprotocol.StateAccepted,
		WorkspaceKey: workspaceKey,
		UpdatedAt:    now,
	}
	entry := journalEntry{
		Kind:        "accept",
		RequestID:   request.RequestID,
		RequestHash: hashString,
		Record:      record,
	}
	if err := s.journal.Append(entry); err != nil {
		return codexprotocol.TaskRecord{}, false, err
	}
	s.apply(entry)
	return record, false, nil
}

func (s *TaskStore) Get(taskID string) (codexprotocol.TaskRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[taskID]
	return record, ok
}

func (s *TaskStore) Events(taskID string, after uint64) []codexprotocol.TaskEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	all := s.events[taskID]
	result := make([]codexprotocol.TaskEvent, 0, len(all))
	for _, event := range all {
		if event.Seq > after {
			result = append(result, event)
		}
	}
	return result
}

func (s *TaskStore) Mutate(mutation codexprotocol.Mutation, change func(*codexprotocol.TaskRecord) error) error {
	if err := mutation.Validate(); err != nil {
		return err
	}
	if change == nil {
		return errors.New("task mutation callback is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.appliedMutations[mutation.RequestID] {
		return nil
	}
	record, exists := s.records[mutation.TaskID]
	if !exists {
		return ErrTaskNotFound
	}
	if record.AttemptID != mutation.AttemptID || record.LeaseEpoch != mutation.LeaseEpoch {
		return ErrStaleLease
	}
	if record.State.Terminal() {
		return ErrTerminalTask
	}
	next := record
	if err := change(&next); err != nil {
		return err
	}
	if !next.State.Valid() {
		return fmt.Errorf("unsupported task state %q", next.State)
	}
	next.UpdatedAt = time.Now().UTC()
	event := codexprotocol.TaskEvent{
		TaskID:     next.TaskID,
		AttemptID:  next.AttemptID,
		LeaseEpoch: next.LeaseEpoch,
		Seq:        next.LastEventSeq + 1,
		State:      next.State,
		Kind:       "state_changed",
		At:         next.UpdatedAt,
	}
	entry := journalEntry{Kind: "mutate", RequestID: mutation.RequestID, Record: next, Event: &event}
	if err := s.journal.Append(entry); err != nil {
		return err
	}
	s.apply(entry)
	return nil
}

func (s *TaskStore) FenceAndRelease(mutation codexprotocol.Mutation) error {
	return s.Mutate(mutation, func(record *codexprotocol.TaskRecord) error {
		record.LeaseEpoch++
		record.State = codexprotocol.StateLost
		return nil
	})
}

func (s *TaskStore) Close() error {
	return s.journal.Close()
}

func (s *TaskStore) apply(entry journalEntry) {
	if entry.Kind == "accept" {
		s.records[entry.Record.TaskID] = entry.Record
		s.requestHashes[entry.RequestID] = entry.RequestHash
		s.requestTasks[entry.RequestID] = entry.Record.TaskID
		s.requestRecords[entry.RequestID] = entry.Record
		if !entry.Record.State.Terminal() {
			s.workspaceLeases[entry.Record.WorkspaceKey] = entry.Record.TaskID
		}
		return
	}
	if entry.Kind != "mutate" {
		return
	}
	s.records[entry.Record.TaskID] = entry.Record
	s.appliedMutations[entry.RequestID] = true
	if entry.Event != nil {
		s.events[entry.Record.TaskID] = append(s.events[entry.Record.TaskID], *entry.Event)
	}
	if entry.Record.State.Terminal() {
		delete(s.workspaceLeases, entry.Record.WorkspaceKey)
	} else {
		s.workspaceLeases[entry.Record.WorkspaceKey] = entry.Record.TaskID
	}
}
