// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package codexruntime

import (
	"strings"
	"testing"
	"time"
)

func TestControlMessageRoundTripAndStrictValidation(t *testing.T) {
	message := ControlMessage{
		SchemaVersion:  ControlSchemaVersion,
		Kind:           ControlKindStart,
		RequestID:      "request-1",
		ConfigRevision: 4,
		ConfigPath:     "/protected/codex-worker.json",
		WorkspaceAlias: "local",
	}
	raw, err := MarshalControlMessage(message)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := UnmarshalControlMessage(raw)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != message {
		t.Fatalf("decoded message %#v, want %#v", decoded, message)
	}

	if _, err := UnmarshalControlMessage([]byte(`{"schemaVersion":1,"kind":"start","requestId":"r","task":"secret"}`)); err == nil {
		t.Fatal("accepted an unknown task-shaped field")
	}
	if _, err := UnmarshalControlMessage([]byte(`{"schemaVersion":1,"kind":"unknown","requestId":"r"}`)); err == nil {
		t.Fatal("accepted an unknown control kind")
	}
}

func TestControlEventNeverSerializesTaskBodies(t *testing.T) {
	event := ControlEvent{
		SchemaVersion: ControlSchemaVersion,
		Kind:          ControlEventReady,
		RequestID:     "request-1",
		State:         RuntimeStateReady,
		Descriptor: func() *RuntimeDescriptor {
			descriptor := validDescriptor(time.Now().UTC())
			return &descriptor
		}(),
	}
	raw, err := MarshalControlEvent(event)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "prompt") || strings.Contains(string(raw), "artifact") {
		t.Fatalf("control event contains task-body field: %s", raw)
	}
}

func TestUnmarshalControlMessageRejectsTrailingJSON(t *testing.T) {
	raw := []byte(`{"schemaVersion":1,"kind":"status","requestId":"r"} {"extra":true}`)
	if _, err := UnmarshalControlMessage(raw); err == nil {
		t.Fatal("UnmarshalControlMessage accepted trailing JSON")
	}
}
