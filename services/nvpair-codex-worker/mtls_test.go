// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"nvpair-shared/clustertrust"
	"nvpair-shared/codexprotocol"
)

func TestMTLSWorkerRequiresCurrentPinAndACL(t *testing.T) {
	serverPEM, serverKey, serverDER := testIdentity(t, "server-principal")
	clientPEM, clientKey, clientDER := testIdentity(t, "client-principal")
	serverDir := testClusterDir(t, serverPEM, serverKey, map[string][]byte{"client-principal": clientPEM})
	clientDir := testClusterDir(t, clientPEM, clientKey, map[string][]byte{"server-principal": serverPEM})
	serverMesh := clustertrust.Open(serverDir)
	clientMesh := clustertrust.Open(clientDir)
	if !serverMesh.Clustered() || !clientMesh.Clustered() {
		t.Fatal("test meshes must be clustered")
	}
	root := t.TempDir()
	policy, err := NewWorkspacePolicy(root)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := NewJournal(filepath.Join(root, "tasks.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewTaskStore(journal)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	worker := NewServerWithSecurity(store, NewAppServerFactory("codex"), policy, nil, 1, serverMesh, []string{"client-principal"})
	srv := httptest.NewUnstartedServer(worker)
	srv.TLS = serverMesh.ServerTLSConfig()
	srv.StartTLS()
	defer srv.Close()

	clientTLS, ok := clientMesh.ClientTLSConfig("server-principal")
	if !ok {
		t.Fatal("client could not build pinned TLS config")
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: clientTLS}}
	response, err := client.Get(srv.URL + "/v1/worker")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("authorized mTLS status=%d", response.StatusCode)
	}
	request, err := http.NewRequest(http.MethodGet, srv.URL+"/v1/worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = "192.0.2.10:14324"
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("authorized remote-host mTLS status=%d", response.StatusCode)
	}

	// Remove the server's client pin. The existing TLS connection may still be
	// reusable, so close idle connections and prove the HTTP-layer recheck fails.
	if err := os.Remove(filepath.Join(serverDir, "trusted", "client-principal.json")); err != nil {
		t.Fatal(err)
	}
	serverMesh.Refresh()
	client.Transport.(*http.Transport).CloseIdleConnections()
	response, err = client.Get(srv.URL + "/v1/worker")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("de-pinned client status=%d, want 403", response.StatusCode)
	}
	_ = serverDER
	_ = clientDER
}

func TestMTLSRevocationCancelsActiveTaskWithinFiveSeconds(t *testing.T) {
	t.Setenv("CODEX_FAKE_APP_SERVER", "1")
	t.Setenv("CODEX_FAKE_HOLD", "1")
	logPath := filepath.Join(t.TempDir(), "revocation-app-server.log")
	t.Setenv("CODEX_FAKE_LOG", logPath)
	serverPEM, serverKey, _ := testIdentity(t, "revocation-server")
	clientPEM, clientKey, _ := testIdentity(t, "revocation-client")
	serverDir := testClusterDir(t, serverPEM, serverKey, map[string][]byte{"revocation-client": clientPEM})
	clientDir := testClusterDir(t, clientPEM, clientKey, map[string][]byte{"revocation-server": serverPEM})
	serverMesh := clustertrust.Open(serverDir)
	clientMesh := clustertrust.Open(clientDir)
	root := t.TempDir()
	policy, err := NewWorkspacePolicy(root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewTaskStore(mustJournal(filepath.Join(root, "tasks.jsonl")))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	worker := NewServerWithSecurity(store, NewAppServerFactory(mustExecutable(t)), policy, nil, 1, serverMesh, []string{"revocation-client"})
	srv := httptest.NewUnstartedServer(worker)
	srv.TLS = serverMesh.ServerTLSConfig()
	srv.StartTLS()
	defer srv.Close()
	revocationCtx, stop := context.WithCancel(context.Background())
	defer stop()
	go worker.RevocationLoop(revocationCtx)
	clientTLS, ok := clientMesh.ClientTLSConfig("revocation-server")
	if !ok {
		t.Fatal("client could not build pinned TLS config")
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: clientTLS}}
	request := validTaskRequest("revocation-request", "revocation-task", "revocation-attempt", 1)
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	post, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/tasks", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	post.Header.Set("Content-Type", "application/json")
	response, err := client.Do(post)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("create status=%d", response.StatusCode)
	}
	deadline := time.Now().Add(2 * time.Second)
	running := false
	for time.Now().Before(deadline) {
		record, ok := store.Get("revocation-task")
		if ok && record.State == codexprotocol.StateRunning && record.SupervisorPrincipal == "revocation-client" {
			running = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !running {
		record, _ := store.Get("revocation-task")
		t.Fatalf("task did not reach running state: %+v", record)
	}
	time.Sleep(100 * time.Millisecond)
	if record, _ := store.Get("revocation-task"); record.State != codexprotocol.StateRunning {
		if data, readErr := os.ReadFile(logPath); readErr == nil {
			t.Logf("fake app-server log: %s", data)
		}
		t.Logf("events before revoke: %+v", store.Events("revocation-task", 0))
		t.Fatalf("hold fixture ended before revocation: %+v", record)
	}
	rotatedPEM, _, _ := testIdentity(t, "revocation-client")
	rotatedPin, err := json.Marshal(map[string]string{"nodeUuid": "revocation-client", "certPem": string(rotatedPEM)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(serverDir, "trusted", "revocation-client.json"), rotatedPin, 0o600); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	deadline = start.Add(5 * time.Second)
	for time.Now().Before(deadline) {
		record, _ := store.Get("revocation-task")
		if record.State == codexprotocol.StateCancelled || record.State == codexprotocol.StateLost {
			t.Logf("revocation terminal state=%s after %v", record.State, time.Since(start))
			if elapsed := time.Since(start); elapsed > 5*time.Second {
				t.Fatalf("revocation took %v", elapsed)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	record, _ := store.Get("revocation-task")
	t.Fatalf("revoked active task was not cancelled within five seconds: %+v", record)
}

func testClusterDir(t *testing.T, certPEM, keyPEM []byte, pins map[string][]byte) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "trusted"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "admission.json"), []byte(`{"clusterId":"test-cluster","epoch":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "node.crt"), certPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "node.key"), keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	for uuid, cert := range pins {
		payload, err := json.Marshal(map[string]string{"nodeUuid": uuid, "certPem": string(cert)})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "trusted", uuid+".json"), payload, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func testIdentity(t *testing.T, uuid string) ([]byte, []byte, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: uuid}, URIs: mustURI(t, "urn:nvpair:node:"+uuid), NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, der
}

func mustURI(t *testing.T, value string) []*url.URL {
	t.Helper()
	uri, err := url.Parse(value)
	if err != nil {
		t.Fatal(err)
	}
	return []*url.URL{uri}
}
