// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
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
	done    chan struct{}
}

var ErrCancellationRequested = errors.New("task cancellation requested")

type workerHTTPServer struct {
	store          *TaskStore
	factory        AppServerFactory
	policy         WorkspacePolicy
	maxConcurrency int
	authToken      string

	mu     sync.Mutex
	active map[string]*taskRun
	runs   sync.WaitGroup
	closed bool
}

func NewServer(store *TaskStore, factory AppServerFactory, policy WorkspacePolicy) http.Handler {
	return NewServerWithCapacity(store, factory, policy, 1)
}

func NewServerWithCapacity(store *TaskStore, factory AppServerFactory, policy WorkspacePolicy, maxConcurrency int) http.Handler {
	return NewServerWithCapacityAndAuth(store, factory, policy, maxConcurrency, "")
}

func NewServerWithCapacityAndAuth(store *TaskStore, factory AppServerFactory, policy WorkspacePolicy, maxConcurrency int, authToken string) http.Handler {
	if maxConcurrency <= 0 {
		maxConcurrency = 1
	}
	return &workerHTTPServer{
		store:          store,
		factory:        factory,
		policy:         policy,
		maxConcurrency: maxConcurrency,
		authToken:      authToken,
		active:         make(map[string]*taskRun),
	}
}

func (s *workerHTTPServer) Close() {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
	}
	runs := make([]*taskRun, 0, len(s.active))
	for _, run := range s.active {
		runs = append(runs, run)
	}
	s.mu.Unlock()
	for _, run := range runs {
		run.session.Cancel()
		run.cancel()
	}
	done := make(chan struct{})
	go func() {
		s.runs.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
}

func (s *workerHTTPServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r) {
		return
	}
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

func (s *workerHTTPServer) authorize(w http.ResponseWriter, r *http.Request) bool {
	if s.authToken != "" {
		provided := strings.TrimSpace(r.Header.Get("Authorization"))
		expected := "Bearer " + s.authToken
		if subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="codex-worker"`)
			writeError(w, http.StatusUnauthorized, "Worker authorization required")
			return false
		}
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		writeError(w, http.StatusForbidden, "browser origins are not accepted")
		return false
	}
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		writeError(w, http.StatusForbidden, "request Host must be a loopback address")
		return false
	}
	if r.Method == http.MethodPost && !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "POST requests require application/json")
		return false
	}
	return true
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
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		writeError(w, http.StatusServiceUnavailable, "Worker is shutting down")
		return
	}
	s.runs.Add(1)
	s.mu.Unlock()
	runTracked := true
	defer func() {
		if runTracked {
			s.runs.Done()
		}
	}()
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
		runTracked = false
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
		w.Header().Set("X-Next-After", strconv.FormatUint(events[99].Seq, 10))
		events = events[:100]
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
	defer s.runs.Done()
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(request.Context.Limits.WallSeconds)*time.Second)
	session := s.factory.New()
	s.mu.Lock()
	run := &taskRun{session: session, cancel: cancel, done: make(chan struct{})}
	s.active[record.TaskID] = run
	shuttingDown := s.closed
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.active, record.TaskID)
		s.mu.Unlock()
		cancel()
		close(run.done)
	}()

	if shuttingDown {
		s.finishLost(record, "Worker shutdown before app-server start")
		return
	}
	if current, ok := s.store.Get(record.TaskID); !ok {
		return
	} else if current.State == codexprotocol.StateCancelling {
		s.finishCancelled(record)
		return
	}
	if err := s.transition(record.Mutation, codexprotocol.StateStarting, "start"); err != nil {
		if errors.Is(err, ErrCancellationRequested) {
			s.finishCancelled(record)
		}
		return
	}
	if err := s.transition(record.Mutation, codexprotocol.StateRunning, "run"); err != nil {
		if errors.Is(err, ErrCancellationRequested) {
			s.finishCancelled(record)
		}
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
		if record.State == codexprotocol.StateCancelling && (state == codexprotocol.StateStarting || state == codexprotocol.StateRunning) {
			return ErrCancellationRequested
		}
		record.State = state
		return nil
	})
}

func (s *workerHTTPServer) finishCancelled(record codexprotocol.TaskRecord) {
	s.finishTerminal(record, codexprotocol.StateCancelled, "cancelled before app-server start", ":cancelled")
}

func (s *workerHTTPServer) finishLost(record codexprotocol.TaskRecord, summary string) {
	s.finishTerminal(record, codexprotocol.StateLost, summary, ":shutdown")
}

func (s *workerHTTPServer) finishTerminal(record codexprotocol.TaskRecord, state codexprotocol.TaskState, summary, suffix string) {
	handoff := codexprotocol.Handoff{
		Version:   codexprotocol.HandoffVersion,
		TaskID:    record.TaskID,
		AttemptID: record.AttemptID,
		Status:    state,
		Summary:   summary,
	}
	mutation := record.Mutation
	mutation.RequestID += suffix
	_ = s.store.Mutate(mutation, func(current *codexprotocol.TaskRecord) error {
		current.State = state
		current.Handoff = &handoff
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
