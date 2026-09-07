// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nvpair-shared/codexruntime"
	"nvpair-shared/protectedfile"
)

func TestPinnedLocalWorkerClientAuthenticatesAndPinsCertificate(t *testing.T) {
	var authorization string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"protocolVersion":1,"version":"test","capabilities":{"os":"darwin","architecture":"arm64","maxConcurrency":1,"availableSlots":1}}`))
	}))
	defer server.Close()

	digest := sha256.Sum256(server.Certificate().Raw)
	fingerprint := hex.EncodeToString(digest[:])
	credentialPath := filepath.Join(t.TempDir(), "credential.json")
	writeTestCredential(t, credentialPath, "local-secret", fingerprint, 1)
	client, err := NewPinnedLocalWorkerClient(server.URL, credentialPath, fingerprint, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Worker(context.Background()); err != nil {
		t.Fatalf("Worker request failed: %v", err)
	}
	if authorization != "Bearer local-secret" {
		t.Fatalf("authorization=%q, want bearer credential", authorization)
	}
}

func TestPinnedLocalWorkerClientRejectsCredentialPinMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credential.json")
	writeTestCredential(t, path, "secret", strings.Repeat("a", 64), 1)
	if _, err := NewPinnedLocalWorkerClient("https://127.0.0.1:43124", path, strings.Repeat("b", 64), 1); err == nil {
		t.Fatal("client accepted a credential certificate pin mismatch")
	}
}

func TestPinnedLocalWorkerClientRejectsCredentialGenerationMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credential.json")
	fingerprint := strings.Repeat("a", 64)
	writeTestCredential(t, path, "secret", fingerprint, 1)
	if _, err := NewPinnedLocalWorkerClient("https://127.0.0.1:43124", path, fingerprint, 2); err == nil {
		t.Fatal("client accepted a stale credential generation")
	}
}

func TestPinnedLocalWorkerClientRejectsUnexpectedPeerCertificate(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"protocolVersion":1,"version":"test"}`))
	}))
	defer server.Close()
	credentialPath := filepath.Join(t.TempDir(), "credential.json")
	untrustedFingerprint := strings.Repeat("b", 64)
	writeTestCredential(t, credentialPath, "secret", untrustedFingerprint, 1)
	client, err := NewPinnedLocalWorkerClient(server.URL, credentialPath, untrustedFingerprint, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Worker(context.Background()); err == nil {
		t.Fatal("request succeeded with an unexpected peer certificate")
	}
}

func TestLoadLocalRuntimeDescriptorUsesProtectedCredentialReference(t *testing.T) {
	now := timeNowUTC()
	runtimePath := filepath.Join(t.TempDir(), "runtime.json")
	descriptor := codexruntime.RuntimeDescriptor{
		SchemaVersion: codexruntime.RuntimeSchemaVersion, InstallationID: "i", WorkerInstanceID: "w",
		BootEpoch: 1, Generation: 1, WrittenAt: now.Add(-time.Second), ExpiresAt: now.Add(time.Minute),
		Endpoint: "https://127.0.0.1:43124", Transport: codexruntime.TransportPinnedLocalTLS,
		ServerCertificateSHA256: strings.Repeat("a", 64), CredentialRef: filepath.Join(t.TempDir(), "credential.json"),
		CredentialGeneration: 1, PolicyRevision: 1, State: codexruntime.RuntimeStateReady,
	}
	if err := codexruntime.WriteDescriptor(runtimePath, descriptor); err != nil {
		t.Fatal(err)
	}
	writeTestCredential(t, descriptor.CredentialRef, "secret", descriptor.ServerCertificateSHA256, descriptor.CredentialGeneration)
	target, err := loadLocalRuntimeTarget(runtimePath, now)
	if err != nil {
		t.Fatal(err)
	}
	if target == nil || target.ID != "local" || target.Client == nil {
		t.Fatalf("target=%#v, want usable local target", target)
	}
}

func TestLoadLocalRuntimeTargetRejectsExpiredDescriptor(t *testing.T) {
	now := timeNowUTC()
	descriptor := codexruntime.RuntimeDescriptor{
		SchemaVersion: codexruntime.RuntimeSchemaVersion, InstallationID: "i", WorkerInstanceID: "w",
		BootEpoch: 1, Generation: 1, WrittenAt: now.Add(-2 * time.Minute), ExpiresAt: now.Add(-time.Second),
		Endpoint: "https://127.0.0.1:43124", Transport: codexruntime.TransportPinnedLocalTLS,
		ServerCertificateSHA256: strings.Repeat("a", 64), CredentialRef: filepath.Join(t.TempDir(), "credential.json"),
		CredentialGeneration: 1, PolicyRevision: 1, State: codexruntime.RuntimeStateReady,
	}
	path := filepath.Join(t.TempDir(), "runtime.json")
	if err := os.WriteFile(path, mustJSON(descriptor), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadLocalRuntimeTarget(path, now); err == nil {
		t.Fatal("expired runtime descriptor was accepted")
	}
}

func TestMCPServerRemovesLocalTargetWhenDescriptorBecomesInvalid(t *testing.T) {
	now := timeNowUTC()
	descriptor := codexruntime.RuntimeDescriptor{
		SchemaVersion: codexruntime.RuntimeSchemaVersion, InstallationID: "i", WorkerInstanceID: "w",
		BootEpoch: 1, Generation: 1, WrittenAt: now.Add(-time.Second), ExpiresAt: now.Add(time.Minute),
		Endpoint: "https://127.0.0.1:43124", Transport: codexruntime.TransportPinnedLocalTLS,
		ServerCertificateSHA256: strings.Repeat("a", 64), CredentialRef: filepath.Join(t.TempDir(), "credential.json"),
		CredentialGeneration: 1, PolicyRevision: 1, State: codexruntime.RuntimeStateReady,
	}
	path := filepath.Join(t.TempDir(), "runtime.json")
	if err := codexruntime.WriteDescriptor(path, descriptor); err != nil {
		t.Fatal(err)
	}
	writeTestCredential(t, descriptor.CredentialRef, "secret", descriptor.ServerCertificateSHA256, descriptor.CredentialGeneration)
	server := NewMCPServerWithWorkers([]WorkerTarget{{ID: "remote", Client: fakeWorkerClient{}}})
	server.refreshLocalRuntime(path, now)
	if len(server.pool.Snapshot()) != 2 {
		t.Fatal("valid local descriptor did not add local target")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	server.refreshLocalRuntime(path, now)
	snapshot := server.pool.Snapshot()
	if len(snapshot) != 1 || snapshot[0].ID != "remote" {
		t.Fatalf("invalid descriptor left local target behind: %#v", snapshot)
	}
}

func mustJSON(value any) []byte {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}

func writeTestCredential(t *testing.T, path, token, fingerprint string, generation uint64) {
	t.Helper()
	data, err := json.Marshal(map[string]any{"bearerToken": token, "serverCertificateSha256": fingerprint, "credentialGeneration": generation})
	if err != nil {
		t.Fatal(err)
	}
	if err := protectedfile.WriteFile(path, append(data, '\n')); err != nil {
		t.Fatal(err)
	}
}

// Isolated to keep test fixtures explicit while avoiding a dependency on the
// Worker package's clock implementation.
func timeNowUTC() time.Time { return time.Now().UTC() }
