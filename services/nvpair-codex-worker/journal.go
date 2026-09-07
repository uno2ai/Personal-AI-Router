// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"nvpair-shared/codexprotocol"
	"nvpair-shared/protectedfile"
)

const journalVersion = 1

type journalEntry struct {
	Version      int                      `json:"version"`
	Sequence     uint64                   `json:"sequence"`
	Kind         string                   `json:"kind"`
	RequestID    string                   `json:"requestId"`
	RequestHash  string                   `json:"requestHash,omitempty"`
	MutationHash string                   `json:"mutationHash,omitempty"`
	Record       codexprotocol.TaskRecord `json:"record"`
	Event        *codexprotocol.TaskEvent `json:"event,omitempty"`
}

type Journal struct {
	mu       sync.Mutex
	file     *protectedfile.File
	lockFile *journalLockHandle
	path     string
	sequence uint64
	closed   bool
}

func NewJournal(path string) (*Journal, error) {
	if path == "" {
		return nil, errors.New("journal path is required")
	}

	lockFile, err := acquireJournalLock(path)
	if err != nil {
		return nil, err
	}
	file, err := protectedfile.OpenAppend(path)
	if err != nil {
		_ = releaseJournalLock(lockFile)
		return nil, fmt.Errorf("open journal: %w", err)
	}

	entries, err := readJournal(path)
	if err != nil {
		_ = file.Close()
		_ = releaseJournalLock(lockFile)
		return nil, err
	}
	var sequence uint64
	for _, entry := range entries {
		if entry.Sequence > sequence {
			sequence = entry.Sequence
		}
	}
	return &Journal{file: file, lockFile: lockFile, path: path, sequence: sequence}, nil
}

func (j *Journal) Append(entry journalEntry) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return errors.New("journal is closed")
	}
	if entry.Version == 0 {
		entry.Version = journalVersion
	}
	if entry.Version != journalVersion {
		return fmt.Errorf("unsupported journal version %d", entry.Version)
	}
	j.sequence++
	entry.Sequence = j.sequence
	encoded, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("encode journal entry: %w", err)
	}
	encoded = append(encoded, '\n')
	if _, err := j.file.Write(encoded); err != nil {
		return fmt.Errorf("write journal: %w", err)
	}
	if err := j.file.Sync(); err != nil {
		return fmt.Errorf("sync journal: %w", err)
	}
	return nil
}

func (j *Journal) Replay() ([]journalEntry, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return readJournal(j.path)
}

func (j *Journal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return nil
	}
	j.closed = true
	if err := j.file.Sync(); err != nil {
		_ = j.file.Close()
		return err
	}
	err := j.file.Close()
	if lockErr := releaseJournalLock(j.lockFile); err == nil {
		err = lockErr
	}
	return err
}

func readJournal(path string) ([]journalEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read journal: %w", err)
	}

	var entries []journalEntry
	var previous uint64
	textData := string(data)
	lines := strings.Split(textData, "\n")
	for index, line := range lines {
		if line == "" && index == len(lines)-1 {
			continue
		}
		var entry journalEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			// A process can die after writing a partial final JSONL record but
			// before the newline reaches durable storage. Earlier records are
			// already fsync'd, so only that demonstrably incomplete tail may be
			// ignored. A malformed newline-terminated record remains fatal.
			if index == len(lines)-1 && !strings.HasSuffix(textData, "\n") && strings.Contains(err.Error(), "unexpected end of JSON input") {
				if truncateErr := repairJournalTail(path, data[:strings.LastIndexByte(textData, '\n')+1]); truncateErr != nil {
					return nil, fmt.Errorf("repair incomplete journal tail: %w", truncateErr)
				}
				break
			}
			return nil, fmt.Errorf("decode journal entry: %w", err)
		}
		if entry.Version != journalVersion {
			return nil, fmt.Errorf("unsupported journal version %d", entry.Version)
		}
		if entry.Sequence == 0 || entry.Sequence <= previous {
			return nil, fmt.Errorf("journal sequence %d is not greater than %d", entry.Sequence, previous)
		}
		previous = entry.Sequence
		entries = append(entries, entry)
		if index == len(lines)-1 && !strings.HasSuffix(textData, "\n") {
			// A valid final JSON object without its frame delimiter is still
			// recoverable, but it must be repaired before the next append or
			// two JSON objects would be concatenated into one invalid line.
			if err := repairJournalTail(path, append(data, '\n')); err != nil {
				return nil, fmt.Errorf("repair journal delimiter: %w", err)
			}
		}
	}
	return entries, nil
}

func repairJournalTail(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}
