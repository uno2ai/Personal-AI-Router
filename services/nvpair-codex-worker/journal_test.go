// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestJournalReplaysOnlyIncompleteFinalTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.jsonl")
	if err := os.WriteFile(path, []byte(`{"version":1,"sequence":1,"kind":"accept","requestId":"r","record":{}}`+"\n"+`{"version":1,"sequence":2,"kind":"mutate"`), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := readJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Sequence != 1 {
		t.Fatalf("entries=%+v", entries)
	}
	journal, err := NewJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.Append(journalEntry{Kind: "repaired", RequestID: "r2"}); err != nil {
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err = readJournal(path)
	if err != nil || len(entries) != 2 || entries[1].Sequence != 2 {
		t.Fatalf("repaired entries=%+v err=%v", entries, err)
	}
}

func TestJournalRejectsMalformedCompleteRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.jsonl")
	if err := os.WriteFile(path, []byte(`{"version":1,"sequence":1,"kind":"accept",}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readJournal(path); err == nil {
		t.Fatal("malformed complete journal record was accepted")
	}
}
