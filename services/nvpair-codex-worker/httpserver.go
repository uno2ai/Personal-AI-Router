// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"nvpair-shared/codexprotocol"
)

type TaskResponse struct {
	Record     codexprotocol.TaskRecord `json:"record"`
	Idempotent bool                     `json:"idempotent"`
}

type ResultResponse struct {
	Record  codexprotocol.TaskRecord `json:"record"`
	Handoff codexprotocol.Handoff    `json:"handoff"`
}

type taskRun struct {
	session *AppServerSession
	cancel  context.CancelFunc
}

type workerHTTPServer struct {
	store          *TaskStore
	factory        AppServerFactory
	policy         WorkspacePolicy
	maxConcurrency int

	mu     sync.Mutex
	active map[string]*taskRun
}

func NewServer(store *TaskStore, factory AppServerFactory, policy WorkspacePolicy) http.Handler {
	return NewServerWithCapacity(store, factory, policy, 1)
}

func NewServerWithCapacity(store *TaskStore, factory AppServerFactory, policy WorkspacePolicy, maxConcurrency int) http.Handler {
	if maxConcurrency <= 0 {
		maxConcurrency = 1
	}
	return &workerHTTPServer{
		store:          store,
		factory:        factory,
		policy:         policy,
		maxConcurrency: maxConcurrency,
		active:         make(map[string]*taskRun),
	}
}

func (s *workerHTTPServer) Close() {
	s.mu.Lock()
	runs := make([]*taskRun, 0, len(s.active))
	for _, run := range s.active {
		runs = append(runs, run)
	}
	s.mu.Unlock()
	for _, run := range runs {
		run.session.Cancel()
		run.cancel()
	}
}

func (s *workerHTTPServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/worker":
		s.handleWorker(w)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/tasks":
		s.handleCreate(w, r)
	case strings.HasPrefix(r.URL.Path, "/v1/tasks/"):
		s.handleTask(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *workerHTTPServer) handleWorker(w http.ResponseWriter) {
	writeJSON(w, http.StatusOK, map[string]any{
		"protocolVersion": codexprotocol.ProtocolVersion,
		"version":         Version,
		"capabilities": map[string]any{
			"os":             runtime.GOOS,
			"architecture":   runtime.GOARCH,
			"maxConcurrency": s.maxConcurrency,
		},
	})
}

func (s *workerHTTPServer) handleCreate(w http.ResponseWriter, r *http.Request) {
	payload, err := readBoundedBody(w, r, codexprotocol.MaxContextBytes)
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, err.Error())
		return
	}
	request, err := codexprotocol.DecodeTaskRequest(payload)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	cwd, err := s.policy.Resolve(request.Workspace)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	record, idempotent, err := s.store.AcceptAt(request, cwd, s.maxConcurrency)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !idempotent {
		go s.runTask(request, record, cwd)
	}
	writeJSON(w, http.StatusAccepted, TaskResponse{Record: record, Idempotent: idempotent})
}

func (s *workerHTTPServer) handleTask(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/tasks/"), "/")
	if len(parts) == 1 && r.Method == http.MethodGet {
		s.handleStatus(w, parts[0])
		return
	}
	if len(parts) == 2 && parts[1] == "events" && r.Method == http.MethodGet {
		s.handleEvents(w, r, parts[0])
		return
	}
	if len(parts) == 2 && parts[1] == "result" && r.Method == http.MethodGet {
		s.handleResult(w, parts[0])
		return
	}
	if len(parts) == 2 && parts[1] == "cancel" && r.Method == http.MethodPost {
		s.handleCancel(w, r, parts[0])
		return
	}
	http.NotFound(w, r)
}

func (s *workerHTTPServer) handleStatus(w http.ResponseWriter, taskID string) {
	record, ok := s.store.Get(taskID)
	if !ok {
		http.NotFound(w, nil)
		return
	}
	writeJSON(w, http.StatusOK, record)
}

func (s *workerHTTPServer) handleEvents(w http.ResponseWriter, r *http.Request, taskID string) {
	if _, ok := s.store.Get(taskID); !ok {
		http.NotFound(w, r)
		return
	}
	after := uint64(0)
	if raw := r.URL.Query().Get("after"); raw != "" {
		parsed, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "after must be an unsigned sequence")
			return
		}
		after = parsed
	}
	events := s.store.Events(taskID, after)
	if len(events) > 100 {
		events = events[len(events)-100:]
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	for _, event := range events {
		encoded, err := json.Marshal(event)
		if err != nil {
			return
		}
		_, _ = w.Write(append(encoded, '\n'))
	}
}

func (s *workerHTTPServer) handleResult(w http.ResponseWriter, taskID string) {
	record, ok := s.store.Get(taskID)
	if !ok {
		http.NotFound(w, nil)
		return
	}
	if record.Handoff == nil {
		writeError(w, http.StatusConflict, "task has no terminal handoff")
		return
	}
	writeJSON(w, http.StatusOK, ResultResponse{Record: record, Handoff: *record.Handoff})
}

func (s *workerHTTPServer) handleCancel(w http.ResponseWriter, r *http.Request, taskID string) {
	payload, err := readBoundedBody(w, r, 8*1024)
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, err.Error())
		return
	}
	var mutation codexprotocol.Mutation
	if err := decodeSingleJSON(payload, &mutation); err != nil {
		writeError(w, http.StatusBadRequest, "decode cancellation: "+err.Error())
		return
	}
	if mutation.TaskID != taskID {
		writeError(w, http.StatusConflict, "taskId does not match request path")
		return
	}
	if record, ok := s.store.Get(taskID); ok && record.State.Terminal() {
		if record.AttemptID == mutation.AttemptID && record.LeaseEpoch == mutation.LeaseEpoch {
			writeJSON(w, http.StatusOK, record)
			return
		}
	}
	if err := s.store.Mutate(mutation, func(record *codexprotocol.TaskRecord) error {
		record.State = codexprotocol.StateCancelling
		return nil
	}); err != nil {
		writeStoreError(w, err)
		return
	}
	s.mu.Lock()
	run := s.active[taskID]
	s.mu.Unlock()
	if run != nil {
		run.session.Cancel()
		// RunWithChild observes ctx cancellation by sending turn/interrupt and
		// only then closing the process; it no longer uses CommandContext's
		// immediate kill path.
		run.cancel()
	}
	record, _ := s.store.Get(taskID)
	writeJSON(w, http.StatusAccepted, record)
}

func decodeSingleJSON(payload []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func (s *workerHTTPServer) runTask(request codexprotocol.TaskRequest, record codexprotocol.TaskRecord, cwd string) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(request.Context.Limits.WallSeconds)*time.Second)
	session := s.factory.New()
	s.mu.Lock()
	s.active[record.TaskID] = &taskRun{session: session, cancel: cancel}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.active, record.TaskID)
		s.mu.Unlock()
		cancel()
	}()

	if err := s.transition(record.Mutation, codexprotocol.StateStarting, "start"); err != nil {
		return
	}
	if err := s.transition(record.Mutation, codexprotocol.StateRunning, "run"); err != nil {
		return
	}
	runtimeRequest := request
	verifiedCWD, err := s.policy.Resolve(request.Workspace)
	if err != nil || verifiedCWD != cwd {
		// The path is revalidated immediately before child creation to narrow
		// the filesystem check/use window. No task is started on drift.
		handoff := codexprotocol.Handoff{Version: codexprotocol.HandoffVersion, TaskID: record.TaskID, AttemptID: record.AttemptID, Status: codexprotocol.StateLost, Summary: "workspace changed before execution"}
		mutation := record.Mutation
		mutation.RequestID = record.RequestID + ":workspace-drift"
		_ = s.store.Mutate(mutation, func(current *codexprotocol.TaskRecord) error {
			current.State = codexprotocol.StateLost
			current.Handoff = &handoff
			return nil
		})
		return
	}
	runtimeRequest.Workspace.Path = verifiedCWD
	var eventNumber uint64
	var persistenceErr error
	handoff, err := session.RunWithChild(ctx, runtimeRequest, func(event codexprotocol.TaskEvent) {
		if event.State.Terminal() {
			return
		}
		mutation := record.Mutation
		eventNumber++
		mutation.RequestID = fmt.Sprintf("%s:event-%d", record.RequestID, eventNumber)
		if err := s.store.MutateEvent(mutation, event.Kind, event.Metadata, func(current *codexprotocol.TaskRecord) error {
			current.State = event.State
			return nil
		}); err != nil && persistenceErr == nil {
			persistenceErr = err
			// Stop producing side effects as soon as durable event persistence
			// fails; the terminal record will remain fenced/lost if it cannot be
			// written either.
			session.Cancel()
		}
	}, func(identity string) error {
		mutation := record.Mutation
		mutation.RequestID = record.RequestID + ":child"
		return s.store.Mutate(mutation, func(current *codexprotocol.TaskRecord) error {
			current.ChildIdentity = identity
			return nil
		})
	})
	if persistenceErr != nil {
		err = fmt.Errorf("persist Worker event: %w", persistenceErr)
	}
	if err != nil {
		state := codexprotocol.StateLost
		if errors.Is(err, context.Canceled) {
			state = codexprotocol.StateCancelled
		} else if errors.Is(err, context.DeadlineExceeded) {
			state = codexprotocol.StateFailed
		}
		handoff = codexprotocol.Handoff{
			Version:   codexprotocol.HandoffVersion,
			TaskID:    record.TaskID,
			AttemptID: record.AttemptID,
			Status:    state,
			Summary:   "app-server execution did not produce a verified handoff",
		}
	}
	if err := handoff.Validate(record.TaskID, record.AttemptID); err != nil {
		handoff = codexprotocol.Handoff{
			Version:   codexprotocol.HandoffVersion,
			TaskID:    record.TaskID,
			AttemptID: record.AttemptID,
			Status:    codexprotocol.StateFailed,
			Summary:   "invalid app-server handoff",
		}
	}
	mutation := record.Mutation
	mutation.RequestID = fmt.Sprintf("%s:terminal", record.RequestID)
	if err := s.store.Mutate(mutation, func(current *codexprotocol.TaskRecord) error {
		current.State = handoff.Status
		current.Handoff = &handoff
		return nil
	}); err != nil {
		// A journal failure must not be hidden: the last durable record remains
		// the source of truth and its lease stays held until an explicit fence.
		return
	}
}

func (s *workerHTTPServer) transition(mutation codexprotocol.Mutation, state codexprotocol.TaskState, suffix string) error {
	mutation.RequestID = mutation.RequestID + ":" + suffix
	return s.store.Mutate(mutation, func(record *codexprotocol.TaskRecord) error {
		record.State = state
		return nil
	})
}

func readBoundedBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, error) {
	reader := http.MaxBytesReader(w, r.Body, limit+1)
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("request body exceeds %d bytes", limit)
	}
	return data, nil
}

func writeStoreError(w http.ResponseWriter, err error) {
	status := http.StatusConflict
	if errors.Is(err, ErrTaskNotFound) {
		status = http.StatusNotFound
	}
	if errors.Is(err, ErrTerminalTask) {
		status = http.StatusConflict
	}
	writeError(w, status, err.Error())
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
