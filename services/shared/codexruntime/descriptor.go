// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package codexruntime defines the small, secret-free contract used to publish
// a broker-owned Codex Worker runtime and to control its lifecycle.
package codexruntime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const RuntimeSchemaVersion = 1

type Transport string

const (
	TransportPinnedLocalTLS Transport = "pinned-local-tls"
	TransportMTLS           Transport = "mtls"
)

type RuntimeState string

const (
	RuntimeStateStarting    RuntimeState = "starting"
	RuntimeStateReady       RuntimeState = "ready"
	RuntimeStateBusy        RuntimeState = "busy"
	RuntimeStateDraining    RuntimeState = "draining"
	RuntimeStateUnavailable RuntimeState = "unavailable"
	RuntimeStateStopped     RuntimeState = "stopped"
)

// RuntimeDescriptor deliberately contains references to credentials, never
// credential values. It is safe to hand this record to a same-user Supervisor
// after the containing file has been protected by the broker.
type RuntimeDescriptor struct {
	SchemaVersion           int          `json:"schemaVersion"`
	InstallationID          string       `json:"installationId"`
	WorkerInstanceID        string       `json:"workerInstanceId"`
	BootEpoch               uint64       `json:"bootEpoch"`
	Generation              uint64       `json:"generation"`
	WrittenAt               time.Time    `json:"writtenAt"`
	ExpiresAt               time.Time    `json:"expiresAt"`
	Endpoint                string       `json:"endpoint"`
	Transport               Transport    `json:"transport"`
	ServerCertificateSHA256 string       `json:"serverCertificateSha256"`
	CredentialRef           string       `json:"credentialRef"`
	CredentialGeneration    uint64       `json:"credentialGeneration"`
	PolicyRevision          uint64       `json:"policyRevision"`
	State                   RuntimeState `json:"state"`
}

func (d RuntimeDescriptor) Validate(now time.Time) error {
	if d.SchemaVersion != RuntimeSchemaVersion {
		return fmt.Errorf("unsupported runtime schema version %d", d.SchemaVersion)
	}
	if strings.TrimSpace(d.InstallationID) == "" || strings.TrimSpace(d.WorkerInstanceID) == "" {
		return errors.New("installationId and workerInstanceId are required")
	}
	if d.BootEpoch == 0 || d.Generation == 0 {
		return errors.New("bootEpoch and generation must be positive")
	}
	if d.WrittenAt.IsZero() || d.ExpiresAt.IsZero() || !d.ExpiresAt.After(d.WrittenAt) {
		return errors.New("writtenAt and expiresAt must be ordered timestamps")
	}
	if !d.ExpiresAt.After(now) {
		return errors.New("runtime descriptor is expired")
	}
	u, err := url.Parse(d.Endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return errors.New("endpoint must be an https URL")
	}
	if d.Transport != TransportPinnedLocalTLS && d.Transport != TransportMTLS {
		return fmt.Errorf("unsupported runtime transport %q", d.Transport)
	}
	if strings.TrimSpace(d.ServerCertificateSHA256) == "" {
		return errors.New("serverCertificateSha256 is required")
	}
	if strings.TrimSpace(d.CredentialRef) == "" || d.CredentialGeneration == 0 {
		return errors.New("credential reference and generation are required")
	}
	if d.PolicyRevision == 0 {
		return errors.New("policyRevision must be positive")
	}
	switch d.State {
	case RuntimeStateStarting, RuntimeStateReady, RuntimeStateBusy, RuntimeStateDraining, RuntimeStateUnavailable, RuntimeStateStopped:
	default:
		return fmt.Errorf("unsupported runtime state %q", d.State)
	}
	return nil
}

func WriteDescriptor(path string, descriptor RuntimeDescriptor) error {
	if err := descriptor.Validate(time.Now().UTC()); err != nil {
		return err
	}
	if previous, err := readDescriptorFile(path); err == nil {
		if descriptor.BootEpoch < previous.BootEpoch ||
			(descriptor.BootEpoch == previous.BootEpoch && descriptor.Generation <= previous.Generation) {
			return fmt.Errorf("descriptor version %d/%d is not newer than stored %d/%d", descriptor.BootEpoch, descriptor.Generation, previous.BootEpoch, previous.Generation)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read existing runtime descriptor: %w", err)
	}

	data, err := json.MarshalIndent(descriptor, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal runtime descriptor: %w", err)
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create runtime directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".runtime-*.tmp")
	if err != nil {
		return fmt.Errorf("create runtime temporary file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("protect runtime descriptor: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write runtime descriptor: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync runtime descriptor: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close runtime descriptor: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("publish runtime descriptor: %w", err)
	}
	return nil
}

func ReadDescriptor(path string, now time.Time) (RuntimeDescriptor, error) {
	descriptor, err := readDescriptorFile(path)
	if err != nil {
		return RuntimeDescriptor{}, err
	}
	if err := descriptor.Validate(now); err != nil {
		return RuntimeDescriptor{}, err
	}
	return descriptor, nil
}

func readDescriptorFile(path string) (RuntimeDescriptor, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return RuntimeDescriptor{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var descriptor RuntimeDescriptor
	if err := decoder.Decode(&descriptor); err != nil {
		return RuntimeDescriptor{}, fmt.Errorf("decode runtime descriptor: %w", err)
	}
	if decoder.More() {
		return RuntimeDescriptor{}, errors.New("runtime descriptor contains trailing JSON")
	}
	return descriptor, nil
}
