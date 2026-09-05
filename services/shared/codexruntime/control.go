// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package codexruntime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const ControlSchemaVersion = 1

type ControlKind string

const (
	ControlKindStart          ControlKind = "start"
	ControlKindStatus         ControlKind = "status"
	ControlKindDrain          ControlKind = "drain"
	ControlKindShutdown       ControlKind = "shutdown"
	ControlKindConfigRevision ControlKind = "configRevision"
)

type ControlMessage struct {
	SchemaVersion  int         `json:"schemaVersion"`
	Kind           ControlKind `json:"kind"`
	RequestID      string      `json:"requestId"`
	ConfigRevision uint64      `json:"configRevision,omitempty"`
	ConfigPath     string      `json:"configPath,omitempty"`
	WorkspaceAlias string      `json:"workspaceAlias,omitempty"`
}

type ControlEventKind string

const (
	ControlEventReady   ControlEventKind = "ready"
	ControlEventStatus  ControlEventKind = "status"
	ControlEventStopped ControlEventKind = "stopped"
	ControlEventError   ControlEventKind = "error"
)

type ControlEvent struct {
	SchemaVersion  int                `json:"schemaVersion"`
	Kind           ControlEventKind   `json:"kind"`
	RequestID      string             `json:"requestId,omitempty"`
	ConfigRevision uint64             `json:"configRevision,omitempty"`
	State          RuntimeState       `json:"state"`
	Descriptor     *RuntimeDescriptor `json:"descriptor,omitempty"`
	Error          string             `json:"error,omitempty"`
}

func (m ControlMessage) validate() error {
	if m.SchemaVersion != ControlSchemaVersion {
		return fmt.Errorf("unsupported control schema version %d", m.SchemaVersion)
	}
	if strings.TrimSpace(m.RequestID) == "" {
		return errors.New("requestId is required")
	}
	switch m.Kind {
	case ControlKindStart:
		if m.ConfigRevision == 0 || strings.TrimSpace(m.ConfigPath) == "" {
			return errors.New("start requires configRevision and configPath")
		}
	case ControlKindConfigRevision:
		if m.ConfigRevision == 0 || strings.TrimSpace(m.ConfigPath) == "" {
			return errors.New("configRevision requires configRevision and configPath")
		}
	case ControlKindStatus, ControlKindDrain, ControlKindShutdown:
	default:
		return fmt.Errorf("unknown control kind %q", m.Kind)
	}
	return nil
}

func (e ControlEvent) validate() error {
	if e.SchemaVersion != ControlSchemaVersion {
		return fmt.Errorf("unsupported control schema version %d", e.SchemaVersion)
	}
	if e.Kind != ControlEventReady && e.Kind != ControlEventStatus && e.Kind != ControlEventStopped && e.Kind != ControlEventError {
		return fmt.Errorf("unknown control event kind %q", e.Kind)
	}
	if e.Kind == ControlEventError && strings.TrimSpace(e.Error) == "" {
		return errors.New("error event requires error")
	}
	if e.Descriptor != nil && e.Kind == ControlEventReady {
		if err := e.Descriptor.Validate(e.Descriptor.WrittenAt); err != nil {
			return fmt.Errorf("event descriptor: %w", err)
		}
	}
	return nil
}

func MarshalControlMessage(message ControlMessage) ([]byte, error) {
	if err := message.validate(); err != nil {
		return nil, err
	}
	return marshalLine(message)
}

func UnmarshalControlMessage(data []byte) (ControlMessage, error) {
	var message ControlMessage
	if err := unmarshalStrict(data, &message); err != nil {
		return ControlMessage{}, err
	}
	if err := message.validate(); err != nil {
		return ControlMessage{}, err
	}
	return message, nil
}

func MarshalControlEvent(event ControlEvent) ([]byte, error) {
	if err := event.validate(); err != nil {
		return nil, err
	}
	return marshalLine(event)
}

func marshalLine(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func unmarshalStrict(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode control message: %w", err)
	}
	if decoder.More() {
		return errors.New("control message contains trailing JSON")
	}
	return nil
}
