// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package codexprotocol defines the versioned contract between the Main Codex
// Supervisor and a Worker Gateway. It deliberately contains no transport or
// filesystem code so the same validation is used by loopback and future mTLS
// transports.
package codexprotocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"
)

const (
	ProtocolVersion    = 1
	ContextVersion     = 1
	HandoffVersion     = 1
	MaxContextBytes    = 256 << 10
	MaxHandoffBytes    = 64 << 10
	MaxArtifactBytes   = 8 << 20
	MaxEventMetadata   = 8 << 10
	MaxCapabilityBytes = 16 << 10
)

type TaskState string

const (
	StateAccepted        TaskState = "accepted"
	StateStarting        TaskState = "starting"
	StateRunning         TaskState = "running"
	StateWaitingApproval TaskState = "waiting_approval"
	StateCancelling      TaskState = "cancelling"
	StateCompleted       TaskState = "completed"
	StateBlocked         TaskState = "blocked"
	StateFailed          TaskState = "failed"
	StateCancelled       TaskState = "cancelled"
	StateLost            TaskState = "lost"
)

func (s TaskState) Valid() bool {
	switch s {
	case StateAccepted, StateStarting, StateRunning, StateWaitingApproval,
		StateCancelling, StateCompleted, StateBlocked, StateFailed,
		StateCancelled, StateLost:
		return true
	default:
		return false
	}
}

func (s TaskState) Terminal() bool {
	switch s {
	case StateCompleted, StateBlocked, StateFailed, StateCancelled, StateLost:
		return true
	default:
		return false
	}
}

type Mutation struct {
	ProtocolVersion int    `json:"protocolVersion"`
	RequestID       string `json:"requestId"`
	TaskID          string `json:"taskId"`
	AttemptID       string `json:"attemptId"`
	LeaseEpoch      uint64 `json:"leaseEpoch"`
}

func (m Mutation) Validate() error {
	if m.ProtocolVersion != ProtocolVersion {
		return fmt.Errorf("unsupported protocolVersion %d", m.ProtocolVersion)
	}
	for name, value := range map[string]string{
		"requestId": m.RequestID,
		"taskId":    m.TaskID,
		"attemptId": m.AttemptID,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	if m.LeaseEpoch == 0 {
		return errors.New("leaseEpoch must be positive")
	}
	return nil
}

type ContextPackage struct {
	Version           int      `json:"version"`
	Objective         string   `json:"objective"`
	RelevantDecisions []string `json:"relevantDecisions,omitempty"`
	Constraints       []string `json:"constraints,omitempty"`
	RequiredEvidence  []string `json:"requiredEvidence,omitempty"`
	Limits            Limits   `json:"limits"`
	Inputs            []Input  `json:"inputs,omitempty"`
}

type Limits struct {
	WallSeconds int `json:"wallSeconds"`
}

type Input struct {
	ArtifactID string `json:"artifactId"`
	SHA256     string `json:"sha256"`
}

// WorkerCapabilities is the authenticated, locally configured scheduling
// surface published by a Worker. It contains no prompt or workspace contents.
type WorkerCapabilities struct {
	ProtocolVersion  int      `json:"protocolVersion"`
	WorkerVersion    string   `json:"workerVersion"`
	AppServerVersion string   `json:"appServerVersion"`
	OS               string   `json:"os"`
	Architecture     string   `json:"architecture"`
	WorkspaceAliases []string `json:"workspaceAliases,omitempty"`
	WorkspaceModes   []string `json:"workspaceModes,omitempty"`
	ToolLabels       []string `json:"toolLabels,omitempty"`
	SandboxModes     []string `json:"sandboxModes,omitempty"`
	ApprovalModes    []string `json:"approvalModes,omitempty"`
	MaxConcurrency   int      `json:"maxConcurrency"`
	AvailableSlots   int      `json:"availableSlots"`
}

func (c WorkerCapabilities) Validate() error {
	if c.ProtocolVersion != ProtocolVersion {
		return fmt.Errorf("unsupported worker protocolVersion %d", c.ProtocolVersion)
	}
	if strings.TrimSpace(c.WorkerVersion) == "" || strings.TrimSpace(c.AppServerVersion) == "" {
		return errors.New("worker and app-server versions are required")
	}
	if strings.TrimSpace(c.OS) == "" || strings.TrimSpace(c.Architecture) == "" {
		return errors.New("worker OS and architecture are required")
	}
	if c.MaxConcurrency <= 0 || c.AvailableSlots < 0 || c.AvailableSlots > c.MaxConcurrency {
		return errors.New("worker capacity is invalid")
	}
	encoded, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if len(encoded) > MaxCapabilityBytes {
		return fmt.Errorf("worker capabilities exceed %d bytes", MaxCapabilityBytes)
	}
	return nil
}

func (p ContextPackage) Validate() error {
	if p.Version != ContextVersion {
		return fmt.Errorf("unsupported context version %d", p.Version)
	}
	if strings.TrimSpace(p.Objective) == "" {
		return errors.New("objective is required")
	}
	if p.Limits.WallSeconds <= 0 {
		return errors.New("limits.wallSeconds must be positive")
	}
	if p.Limits.WallSeconds > 24*60*60 {
		return errors.New("limits.wallSeconds exceeds 24 hours")
	}
	for _, input := range p.Inputs {
		if !safeToken(input.ArtifactID) || !isSHA256(input.SHA256) {
			return errors.New("context input artifact identity is invalid")
		}
	}
	encoded, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("encode context: %w", err)
	}
	if len(encoded) > MaxContextBytes {
		return fmt.Errorf("context exceeds 256 KiB: %d bytes", len(encoded))
	}
	return nil
}

func safeToken(value string) bool {
	if value == "" || len(value) > 160 {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}

func isSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F') || (r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}

type WorkspaceSpec struct {
	ID           string `json:"id"`
	Path         string `json:"path"`
	Mode         string `json:"mode"`
	BaseRevision string `json:"baseRevision,omitempty"`
}

func (w WorkspaceSpec) Validate() error {
	if strings.TrimSpace(w.ID) == "" {
		return errors.New("workspace.id is required")
	}
	if strings.TrimSpace(w.Path) == "" {
		return errors.New("workspace.path is required")
	}
	if filepath.IsAbs(w.Path) {
		return errors.New("workspace.path must be relative")
	}
	portablePath := strings.ReplaceAll(w.Path, "\\", "/")
	if strings.HasPrefix(portablePath, "/") || strings.HasSuffix(portablePath, "/") ||
		strings.Contains(portablePath, "//") || strings.Contains(w.Path, ":") {
		return errors.New("workspace.path must not contain absolute, drive, or empty components")
	}
	for _, component := range strings.Split(portablePath, "/") {
		if component == ".." || component == "." {
			return errors.New("workspace.path cannot contain . or .. components")
		}
	}
	switch w.Mode {
	case "read", "write":
		return nil
	default:
		return fmt.Errorf("unsupported workspace.mode %q", w.Mode)
	}
}

type ExecutionSpec struct {
	Sandbox  string `json:"sandbox"`
	Approval string `json:"approval"`
}

func (e ExecutionSpec) Validate() error {
	if e.Sandbox != "read-only" && e.Sandbox != "workspace-write" {
		return fmt.Errorf("unsupported execution.sandbox %q", e.Sandbox)
	}
	if e.Approval != "local-only" {
		return fmt.Errorf("unsupported execution.approval %q", e.Approval)
	}
	return nil
}

type TaskRequest struct {
	Mutation                    `json:",inline"`
	SupervisorPrincipal         string         `json:"supervisorPrincipal,omitempty"`
	SupervisorCertificateSHA256 string         `json:"supervisorCertificateSHA256,omitempty"`
	Context                     ContextPackage `json:"context"`
	Workspace                   WorkspaceSpec  `json:"workspace"`
	Execution                   ExecutionSpec  `json:"execution"`
}

func (r TaskRequest) Validate() error {
	if err := r.Mutation.Validate(); err != nil {
		return err
	}
	if err := r.Context.Validate(); err != nil {
		return err
	}
	if err := r.Workspace.Validate(); err != nil {
		return err
	}
	if err := r.Execution.Validate(); err != nil {
		return err
	}
	if r.Workspace.Mode == "read" && r.Execution.Sandbox != "read-only" {
		return errors.New("read workspace requires read-only sandbox")
	}
	if r.Workspace.Mode == "write" && r.Execution.Sandbox != "workspace-write" {
		return errors.New("write workspace requires workspace-write sandbox")
	}
	return nil
}

func DecodeTaskRequest(payload []byte) (TaskRequest, error) {
	if len(payload) > MaxContextBytes {
		return TaskRequest{}, fmt.Errorf("context request exceeds 256 KiB: %d bytes", len(payload))
	}
	var request TaskRequest
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return TaskRequest{}, fmt.Errorf("decode task request: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return TaskRequest{}, errors.New("decode task request: multiple JSON values")
		}
		return TaskRequest{}, fmt.Errorf("decode task request: trailing data: %w", err)
	}
	if err := request.Validate(); err != nil {
		return TaskRequest{}, err
	}
	return request, nil
}

type TaskRecord struct {
	Mutation                    `json:",inline"`
	State                       TaskState     `json:"state"`
	WorkspaceKey                string        `json:"workspaceKey"`
	Workspace                   WorkspaceSpec `json:"workspace"`
	Execution                   ExecutionSpec `json:"execution"`
	SupervisorPrincipal         string        `json:"supervisorPrincipal,omitempty"`
	SupervisorCertificateSHA256 string        `json:"supervisorCertificateSHA256,omitempty"`
	ChildIdentity               string        `json:"childIdentity,omitempty"`
	ThreadID                    string        `json:"threadId,omitempty"`
	LastEventSeq                uint64        `json:"lastEventSeq"`
	UpdatedAt                   time.Time     `json:"updatedAt"`
	Handoff                     *Handoff      `json:"handoff,omitempty"`
	Fenced                      bool          `json:"fenced"`
}

type TaskEvent struct {
	TaskID     string            `json:"taskId"`
	AttemptID  string            `json:"attemptId"`
	LeaseEpoch uint64            `json:"leaseEpoch"`
	Seq        uint64            `json:"seq"`
	State      TaskState         `json:"state"`
	Kind       string            `json:"kind"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	At         time.Time         `json:"at"`
}

func (e TaskEvent) Validate() error {
	if e.TaskID == "" || e.AttemptID == "" || e.LeaseEpoch == 0 || e.Seq == 0 {
		return errors.New("event identity and sequence are required")
	}
	if !e.State.Valid() {
		return fmt.Errorf("unsupported event state %q", e.State)
	}
	if strings.TrimSpace(e.Kind) == "" {
		return errors.New("event kind is required")
	}
	encoded, err := json.Marshal(e.Metadata)
	if err != nil {
		return fmt.Errorf("encode event metadata: %w", err)
	}
	if len(encoded) > MaxEventMetadata {
		return fmt.Errorf("event metadata exceeds %d bytes", MaxEventMetadata)
	}
	return nil
}

type ArtifactManifest struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

type Handoff struct {
	Version         int                `json:"version"`
	TaskID          string             `json:"taskId"`
	AttemptID       string             `json:"attemptId"`
	Status          TaskState          `json:"status"`
	Summary         string             `json:"summary"`
	Findings        []Finding          `json:"findings,omitempty"`
	Changes         []Change           `json:"changes,omitempty"`
	Verification    []Verification     `json:"verification,omitempty"`
	Artifacts       []ArtifactManifest `json:"artifacts,omitempty"`
	RecommendedNext string             `json:"recommendedNext,omitempty"`
}

type Finding struct {
	Severity string `json:"severity"`
	Location string `json:"location,omitempty"`
	Detail   string `json:"detail"`
}

type Change struct {
	Path    string `json:"path"`
	Summary string `json:"summary"`
}

type Verification struct {
	Command    string `json:"command"`
	Outcome    string `json:"outcome"`
	ArtifactID string `json:"artifactId,omitempty"`
}

func (h Handoff) Validate(taskID, attemptID string) error {
	if h.Version != HandoffVersion {
		return fmt.Errorf("unsupported handoff version %d", h.Version)
	}
	if h.TaskID != taskID {
		return fmt.Errorf("handoff taskId %q does not match %q", h.TaskID, taskID)
	}
	if h.AttemptID != attemptID {
		return fmt.Errorf("handoff attemptId %q does not match %q", h.AttemptID, attemptID)
	}
	if !h.Status.Terminal() {
		return fmt.Errorf("handoff status %q is not terminal", h.Status)
	}
	if strings.TrimSpace(h.Summary) == "" {
		return errors.New("handoff summary is required")
	}
	encoded, err := json.Marshal(h)
	if err != nil {
		return fmt.Errorf("encode handoff: %w", err)
	}
	if len(encoded) > MaxHandoffBytes {
		return fmt.Errorf("handoff exceeds 64 KiB: %d bytes", len(encoded))
	}
	return nil
}
