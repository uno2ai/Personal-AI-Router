// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"nvpair-shared/clustertrust"
	"nvpair-shared/codexprotocol"
	"nvpair-shared/codexruntime"
)

func TestManagedRemoteConfigValidation(t *testing.T) {
	for mask := 1; mask < 8; mask++ {
		c := validManagedConfig(t.TempDir())
		if mask&1 != 0 {
			c.RemoteListen = "127.0.0.1:0"
		}
		if mask&2 != 0 {
			c.ClusterDir = t.TempDir()
		}
		if mask&4 != 0 {
			c.SupervisorAllowlist = []string{"supervisor"}
		}
		if err := c.Validate(); (err == nil) != (mask == 7) {
			t.Fatalf("field combination %d: %v", mask, err)
		}
	}
	for _, address := range []string{"localhost:1234", "127.0.0.1", "127.0.0.1:", "127.0.0.1:https", "127.0.0.1:-1", "127.0.0.1:65536"} {
		c := validManagedConfig(t.TempDir())
		c.RemoteListen, c.ClusterDir, c.SupervisorAllowlist = address, t.TempDir(), []string{"supervisor"}
		if c.Validate() == nil {
			t.Fatalf("accepted %q", address)
		}
	}
	for _, peers := range [][]string{{}, {""}, {" "}, {" supervisor"}} {
		c := validManagedConfig(t.TempDir())
		c.RemoteListen, c.ClusterDir, c.SupervisorAllowlist = "127.0.0.1:0", t.TempDir(), peers
		if c.Validate() == nil {
			t.Fatalf("accepted peers %#v", peers)
		}
	}
}

func managedRemoteFixture(t *testing.T) (managedWorkerConfig, *http.Client, *http.Client) {
	t.Helper()
	t.Setenv("CODEX_FAKE_APP_SERVER", "1")
	serverPEM, serverKey, _ := testIdentity(t, "managed-server")
	clientPEM, clientKey, _ := testIdentity(t, "managed-client")
	otherPEM, otherKey, _ := testIdentity(t, "other-client")
	c := validManagedConfig(t.TempDir())
	c.CodexBin = mustExecutable(t)
	c.RemoteListen = "127.0.0.1:0"
	c.ClusterDir = testClusterDir(t, serverPEM, serverKey, map[string][]byte{"managed-client": clientPEM, "other-client": otherPEM})
	c.SupervisorAllowlist = []string{"managed-client"}
	clientFor := func(cert, key []byte) *http.Client {
		mesh := clustertrust.Open(testClusterDir(t, cert, key, map[string][]byte{"managed-server": serverPEM}))
		cfg, ok := mesh.ClientTLSConfig("managed-server")
		if !ok {
			t.Fatal("missing server pin")
		}
		transport := &http.Transport{TLSClientConfig: cfg}
		t.Cleanup(transport.CloseIdleConnections)
		return &http.Client{Transport: transport, Timeout: 3 * time.Second}
	}
	return c, clientFor(clientPEM, clientKey), clientFor(otherPEM, otherKey)
}

func TestManagedRemoteSharesWorkerAndStops(t *testing.T) {
	c, remote, unauthorized := managedRemoteFixture(t)
	t.Setenv("CODEX_FAKE_HOLD", "1")
	controller, err := startManagedWorker(c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = controller.Stop() })
	remoteURL := "https://" + controller.remoteListener.Addr().String()
	descriptor := controller.descriptorCopy()
	if descriptor.Transport != codexruntime.TransportPinnedLocalTLS || descriptor.Endpoint == remoteURL {
		t.Fatalf("local descriptor changed: %+v", descriptor)
	}
	// Verify the local listener's certificate against the descriptor pin.
	localTLS, ok := clustertrust.Open(c.ClusterDir).ClientTLSConfig("managed-server")
	if !ok {
		t.Fatal("missing local identity")
	}
	localTLS.Certificates = nil
	localTLS.VerifyPeerCertificate = func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		digest := sha256.Sum256(rawCerts[0])
		if hex.EncodeToString(digest[:]) != descriptor.ServerCertificateSHA256 {
			return errors.New("local certificate pin mismatch")
		}
		return nil
	}
	localTransport := &http.Transport{TLSClientConfig: localTLS}
	t.Cleanup(localTransport.CloseIdleConnections)
	local := &http.Client{Transport: localTransport, Timeout: 3 * time.Second}
	request := func(client *http.Client, endpoint, token, method, path string, body []byte, want int) []byte {
		t.Helper()
		r, err := http.NewRequest(method, endpoint+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		if method == http.MethodPost {
			r.Header.Set("Content-Type", "application/json")
		}
		response, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != want {
			t.Fatalf("%s %s: status %d, want %d: %s", method, path, response.StatusCode, want, data)
		}
		return data
	}
	request(remote, remoteURL, "", "GET", "/v1/worker", nil, 200)
	request(unauthorized, remoteURL, c.AuthToken, "GET", "/v1/worker", nil, 403)
	request(local, descriptor.Endpoint, "", "GET", "/v1/worker", nil, 401)
	request(local, descriptor.Endpoint, c.AuthToken, "GET", "/v1/worker", nil, 200)
	// A bearer token cannot replace the remote listener's client certificate.
	noCertConfig := remote.Transport.(*http.Transport).TLSClientConfig.Clone()
	noCertConfig.Certificates = nil
	noCertTransport := &http.Transport{TLSClientConfig: noCertConfig}
	defer noCertTransport.CloseIdleConnections()
	noCert := &http.Client{Transport: noCertTransport, Timeout: time.Second}
	r, _ := http.NewRequest("GET", remoteURL+"/v1/worker", nil)
	r.Header.Set("Authorization", "Bearer "+c.AuthToken)
	if response, err := noCert.Do(r); err == nil {
		response.Body.Close()
		t.Fatal("remote TLS accepted no client certificate")
	}
	writeTask := validTaskRequest("write-r", "write-t", "write-a", 1)
	writeTask.Workspace.Mode = "write"
	writeTask.Execution.Sandbox = "workspace-write"
	for _, endpoint := range []struct {
		client     *http.Client
		url, token string
	}{{remote, remoteURL, ""}, {local, descriptor.Endpoint, c.AuthToken}} {
		request(endpoint.client, endpoint.url, endpoint.token, "POST", "/v1/tasks", mustJSONBytes(t, writeTask), 403)
	}
	task := validTaskRequest("shared-r", "shared-t", "shared-a", 1)
	request(remote, remoteURL, "", "POST", "/v1/tasks", mustJSONBytes(t, task), 202)
	deadline := time.Now().Add(3 * time.Second)
	for {
		record, ok := controller.store.Get("shared-t")
		if ok && record.State == codexprotocol.StateRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("task did not run: %+v", record)
		}
		time.Sleep(10 * time.Millisecond)
	}
	request(local, descriptor.Endpoint, c.AuthToken, "GET", "/v1/tasks/shared-t", nil, 200)
	for _, endpoint := range []struct {
		client     *http.Client
		url, token string
	}{{remote, remoteURL, ""}, {local, descriptor.Endpoint, c.AuthToken}} {
		data := request(endpoint.client, endpoint.url, endpoint.token, "GET", "/v1/worker", nil, 200)
		var result struct {
			Capabilities codexprotocol.WorkerCapabilities `json:"capabilities"`
		}
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatal(err)
		}
		if result.Capabilities.AvailableSlots != 0 {
			t.Fatalf("slots not shared: %s", data)
		}
	}
	request(local, descriptor.Endpoint, c.AuthToken, "POST", "/v1/tasks", mustJSONBytes(t, validTaskRequest("full-r", "full-t", "full-a", 1)), 409)
	if err := os.Remove(filepath.Join(c.ClusterDir, "trusted", "managed-client.json")); err != nil {
		t.Fatal(err)
	}
	request(remote, remoteURL, "", "GET", "/v1/worker", nil, 403)
	deadline = time.Now().Add(5 * time.Second)
	for {
		record, _ := controller.store.Get("shared-t")
		if record.State == codexprotocol.StateCancelled || record.State == codexprotocol.StateLost {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("managed revocation did not cancel task: %+v", record)
		}
		time.Sleep(20 * time.Millisecond)
	}
	request(local, descriptor.Endpoint, c.AuthToken, "GET", "/v1/worker", nil, 200)
	if err := controller.Stop(); err != nil {
		t.Fatal(err)
	}
	for _, done := range []chan struct{}{controller.heartbeatDone, controller.revocationDone} {
		select {
		case <-done:
		default:
			t.Fatal("watcher still running after Stop")
		}
	}
	for _, listener := range []net.Listener{controller.localListener, controller.remoteListener} {
		conn, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
		if err == nil {
			conn.Close()
			t.Fatal("listener still accepts after Stop")
		}
	}
	if _, err := os.Stat(c.RuntimeDescriptorPath); !os.IsNotExist(err) {
		t.Fatalf("descriptor remains: %v", err)
	}
}

func TestManagedRemoteStartupFailureReleasesListener(t *testing.T) {
	c, _, _ := managedRemoteFixture(t)
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	c.RemoteListen = reserved.Addr().String()
	if controller, err := startManagedWorker(c); err == nil {
		controller.Stop()
		t.Fatal("accepted occupied remote port")
	}
	reserved.Close()
	c.CodexBin = filepath.Join(t.TempDir(), "missing-codex")
	if controller, err := startManagedWorker(c); err == nil {
		controller.Stop()
		t.Fatal("accepted unavailable executable")
	}
	rebound, err := net.Listen("tcp", c.RemoteListen)
	if err != nil {
		t.Fatalf("startup leaked remote listener: %v", err)
	}
	rebound.Close()
	c.ClusterDir = t.TempDir()
	if controller, err := startManagedWorker(c); err == nil {
		controller.Stop()
		t.Fatal("accepted unpaired cluster")
	}
}
