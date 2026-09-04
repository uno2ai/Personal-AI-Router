// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
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
	appliedMutations map[string]string
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
		appliedMutations: make(map[string]string),
		workspaceLeases:  make(map[string]string),
	}
	entries, err := journal.Replay()
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		store.apply(entry)
	}
	if err := store.reconcileUnresolved(); err != nil {
		_ = journal.Close()
		return nil, err
	}
	return store, nil
}

func (s *TaskStore) reconcileUnresolved() error {
	taskIDs := make([]string, 0, len(s.records))
	for taskID, record := range s.records {
		if !record.State.Terminal() {
			taskIDs = append(taskIDs, taskID)
		}
	}
	sort.Strings(taskIDs)
	for _, taskID := range taskIDs {
		record := s.records[taskID]
		mutation := record.Mutation
		mutation.RequestID = "restart:" + taskID + ":" + fmt.Sprint(record.LeaseEpoch)
		record.State = codexprotocol.StateLost
		record.Fenced = false
		record.UpdatedAt = time.Now().UTC()
		event := codexprotocol.TaskEvent{TaskID: record.TaskID, AttemptID: record.AttemptID, LeaseEpoch: record.LeaseEpoch, Seq: record.LastEventSeq + 1, State: record.State, Kind: "restart_reconciled", At: record.UpdatedAt}
		record.LastEventSeq = event.Seq
		entry := journalEntry{Kind: "mutate", RequestID: mutation.RequestID, MutationHash: hashMutation(mutation), Record: record, Event: &event}
		if err := s.journal.Append(entry); err != nil {
			return fmt.Errorf("reconcile task %s: %w", taskID, err)
		}
		s.apply(entry)
	}
	return nil
}

func (s *TaskStore) Accept(request codexprotocol.TaskRequest) (codexprotocol.TaskRecord, bool, error) {
	return s.AcceptAt(request, request.Workspace.ID+"\x00"+request.Workspace.Path, 1)
}

// AcceptAt atomically admits a task against the canonical workspace lease key
// and the Worker capacity limit. The HTTP boundary must pass the policy-
// resolved path here; request.Workspace.Path is only a user-facing alias.
func (s *TaskStore) AcceptAt(request codexprotocol.TaskRequest, canonicalWorkspace string, capacity int) (codexprotocol.TaskRecord, bool, error) {
	if err := request.Validate(); err != nil {
		return codexprotocol.TaskRecord{}, false, err
	}
	if canonicalWorkspace == "" {
		return codexprotocol.TaskRecord{}, false, errors.New("canonical workspace is required")
	}
	if capacity <= 0 {
		return codexprotocol.TaskRecord{}, false, errors.New("Worker capacity must be positive")
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return codexprotocol.TaskRecord{}, false, fmt.Errorf("encode task request: %w", err)
	}
	hash := sha256.Sum256(payload)
	hashString := hex.EncodeToString(hash[:])
	workspaceKey := workspaceLeaseKey(canonicalWorkspace)

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
		if !previous.Fenced {
			return codexprotocol.TaskRecord{}, false, ErrTaskBusy
		}
		if request.LeaseEpoch < previous.LeaseEpoch {
			return codexprotocol.TaskRecord{}, false, ErrStaleLease
		}
	}
	if len(s.workspaceLeases) >= capacity {
		return codexprotocol.TaskRecord{}, false, ErrCapacity
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
	return s.MutateEvent(mutation, "state_changed", nil, change)
}

func (s *TaskStore) MutateEvent(mutation codexprotocol.Mutation, kind string, metadata map[string]string, change func(*codexprotocol.TaskRecord) error) error {
	if err := mutation.Validate(); err != nil {
		return err
	}
	if change == nil {
		return errors.New("task mutation callback is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	mutationHash := hashMutation(mutation)
	if previousHash, applied := s.appliedMutations[mutation.RequestID]; applied {
		if previousHash != mutationHash {
			return ErrRequestConflict
		}
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
		Kind:       kind,
		Metadata:   cloneMetadata(metadata),
		At:         next.UpdatedAt,
	}
	next.LastEventSeq = event.Seq
	if err := event.Validate(); err != nil {
		return err
	}
	entry := journalEntry{Kind: "mutate", RequestID: mutation.RequestID, MutationHash: mutationHash, Record: next, Event: &event}
	if err := s.journal.Append(entry); err != nil {
		return err
	}
	s.apply(entry)
	return nil
}

func (s *TaskStore) FenceAndRelease(mutation codexprotocol.Mutation) error {
	if err := mutation.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	mutationHash := hashMutation(mutation)
	if previousHash, applied := s.appliedMutations[mutation.RequestID]; applied {
		if previousHash != mutationHash {
			return ErrRequestConflict
		}
		return nil
	}
	record, exists := s.records[mutation.TaskID]
	if !exists {
		return ErrTaskNotFound
	}
	if record.AttemptID != mutation.AttemptID || record.LeaseEpoch != mutation.LeaseEpoch {
		return ErrStaleLease
	}
	if record.Fenced {
		return nil
	}
	record.LeaseEpoch++
	record.State = codexprotocol.StateLost
	record.Fenced = true
	record.UpdatedAt = time.Now().UTC()
	event := codexprotocol.TaskEvent{
		TaskID:     record.TaskID,
		AttemptID:  record.AttemptID,
		LeaseEpoch: record.LeaseEpoch,
		Seq:        record.LastEventSeq + 1,
		State:      record.State,
		Kind:       "lease_fenced",
		At:         record.UpdatedAt,
	}
	record.LastEventSeq = event.Seq
	entry := journalEntry{Kind: "fence", RequestID: mutation.RequestID, MutationHash: mutationHash, Record: record, Event: &event}
	if err := s.journal.Append(entry); err != nil {
		return err
	}
	s.apply(entry)
	return nil
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
	if entry.Kind == "fence" {
		s.records[entry.Record.TaskID] = entry.Record
		s.appliedMutations[entry.RequestID] = entry.MutationHash
		if entry.Event != nil {
			s.events[entry.Record.TaskID] = append(s.events[entry.Record.TaskID], *entry.Event)
		}
		delete(s.workspaceLeases, entry.Record.WorkspaceKey)
		return
	}
	if entry.Kind != "mutate" {
		return
	}
	s.records[entry.Record.TaskID] = entry.Record
	mutationHash := entry.MutationHash
	if mutationHash == "" {
		mutationHash = hashMutation(entry.Record.Mutation)
	}
	s.appliedMutations[entry.RequestID] = mutationHash
	if entry.Event != nil {
		s.events[entry.Record.TaskID] = append(s.events[entry.Record.TaskID], *entry.Event)
	}
	if entry.Record.State == codexprotocol.StateCompleted || entry.Record.State == codexprotocol.StateBlocked || entry.Record.State == codexprotocol.StateCancelled || entry.Record.Fenced {
		delete(s.workspaceLeases, entry.Record.WorkspaceKey)
	} else {
		s.workspaceLeases[entry.Record.WorkspaceKey] = entry.Record.TaskID
	}
}

var ErrCapacity = errors.New("Worker capacity is full")

func hashMutation(mutation codexprotocol.Mutation) string {
	encoded, _ := json.Marshal(mutation)
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:])
}

func workspaceLeaseKey(canonicalPath string) string {
	hash := sha256.Sum256([]byte(canonicalWorkspaceIdentity(canonicalPath)))
	return "workspace:" + hex.EncodeToString(hash[:])
}

func cloneMetadata(metadata map[string]string) map[string]string {
	if len(metadata) == 0 {
		return nil
	}
	copy := make(map[string]string, len(metadata))
	for key, value := range metadata {
		copy[key] = value
	}
	return copy
}
