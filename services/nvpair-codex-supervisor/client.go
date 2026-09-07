// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"nvpair-shared/clustertrust"
	"nvpair-shared/codexprotocol"
	"nvpair-shared/protectedfile"
)

type WorkerClient interface {
	Worker(context.Context) (json.RawMessage, error)
	Create(context.Context, codexprotocol.TaskRequest) (json.RawMessage, error)
	Status(context.Context, string) (json.RawMessage, error)
	Result(context.Context, string) (json.RawMessage, error)
	Cancel(context.Context, codexprotocol.Mutation) (json.RawMessage, error)
	Artifact(context.Context, string, string) ([]byte, error)
}

type HTTPWorkerClient struct {
	baseURL   string
	authToken string
	http      *http.Client
	mesh      *clustertrust.Mesh
	peerUUID  string
	pool      *clustertrust.PeerClientPool
}

type localWorkerCredential struct {
	BearerToken             string `json:"bearerToken"`
	ServerCertificateSHA256 string `json:"serverCertificateSha256"`
	CredentialGeneration    uint64 `json:"credentialGeneration"`
}

// NewPinnedLocalWorkerClient creates the same-user client for the broker-owned
// local Worker. The Worker uses a self-signed certificate, so the certificate
// chain is intentionally not trusted; the exact leaf DER is pinned instead.
func NewPinnedLocalWorkerClient(rawURL, credentialPath, expectedCertificateSHA256 string, expectedCredentialGeneration uint64) (*HTTPWorkerClient, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return nil, errors.New("local Worker URL must be an absolute https URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("local Worker URL must not contain credentials or query data")
	}
	ip := net.ParseIP(parsed.Hostname())
	if ip == nil || !ip.IsLoopback() {
		return nil, errors.New("local Worker URL must use a literal loopback address")
	}
	expected, err := decodeCertificateFingerprint(expectedCertificateSHA256)
	if err != nil {
		return nil, err
	}
	credential, err := readLocalWorkerCredential(credentialPath)
	if err != nil {
		return nil, err
	}
	credentialFingerprint, err := decodeCertificateFingerprint(credential.ServerCertificateSHA256)
	if err != nil {
		return nil, fmt.Errorf("local Worker credential certificate pin: %w", err)
	}
	if !strings.EqualFold(credential.ServerCertificateSHA256, expectedCertificateSHA256) || !equalBytes(credentialFingerprint, expected) {
		return nil, errors.New("local Worker credential certificate pin does not match runtime descriptor")
	}
	if expectedCredentialGeneration == 0 || credential.CredentialGeneration != expectedCredentialGeneration {
		return nil, errors.New("local Worker credential generation does not match runtime descriptor")
	}
	transport := &http.Transport{
		Proxy: nil,
		TLSClientConfig: &tls.Config{
			MinVersion:         tls.VersionTLS13,
			InsecureSkipVerify: true, // the leaf DER is verified below.
			VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
				if len(rawCerts) == 0 {
					return errors.New("local Worker did not present a certificate")
				}
				digest := sha256.Sum256(rawCerts[0])
				if !equalBytes(digest[:], expected) {
					return errors.New("local Worker certificate pin mismatch")
				}
				return nil
			},
		},
	}
	return &HTTPWorkerClient{
		baseURL:   strings.TrimRight(parsed.String(), "/"),
		authToken: credential.BearerToken,
		http: &http.Client{
			Timeout:   30 * time.Second,
			Transport: transport,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func readLocalWorkerCredential(path string) (localWorkerCredential, error) {
	if err := protectedfile.Check(filepath.Dir(path)); err != nil {
		return localWorkerCredential{}, fmt.Errorf("unprotected containing directory: %w", err)
	}
	if err := protectedfile.Check(path); err != nil {
		return localWorkerCredential{}, fmt.Errorf("unprotected local Worker credential: %w", err)
	}
	file, err := os.Open(path)
	if err != nil {
		return localWorkerCredential{}, fmt.Errorf("open local Worker credential: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 16<<10))
	decoder.DisallowUnknownFields()
	var credential localWorkerCredential
	if err := decoder.Decode(&credential); err != nil {
		return localWorkerCredential{}, fmt.Errorf("decode local Worker credential: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return localWorkerCredential{}, errors.New("local Worker credential contains trailing JSON")
		}
		return localWorkerCredential{}, fmt.Errorf("decode trailing local Worker credential: %w", err)
	}
	if strings.TrimSpace(credential.BearerToken) == "" || strings.TrimSpace(credential.ServerCertificateSHA256) == "" || credential.CredentialGeneration == 0 {
		return localWorkerCredential{}, errors.New("local Worker credential is incomplete")
	}
	return credential, nil
}

func decodeCertificateFingerprint(value string) ([]byte, error) {
	decoded, err := hex.DecodeString(strings.TrimSpace(value))
	if err != nil || len(decoded) != sha256.Size {
		return nil, errors.New("certificate pin must be a SHA-256 hex digest")
	}
	return decoded, nil
}

func equalBytes(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	var difference byte
	for i := range left {
		difference |= left[i] ^ right[i]
	}
	return difference == 0
}

// NewMTLSWorkerClient creates a remote client for a Worker discovered through
// PAIR. The peer UUID selects the exact pinned certificate; the URL is only an
// address hint and must be a literal IP, never a hostname.
func NewMTLSWorkerClient(rawURL string, mesh *clustertrust.Mesh, peerUUID string) (*HTTPWorkerClient, error) {
	if mesh == nil || strings.TrimSpace(peerUUID) == "" {
		return nil, errors.New("mTLS Worker client requires a mesh and peer UUID")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return nil, errors.New("mTLS Worker URL must be an absolute https URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || net.ParseIP(parsed.Hostname()) == nil {
		return nil, errors.New("mTLS Worker URL must use a literal IP without credentials or query data")
	}
	mesh.Refresh()
	pool := clustertrust.NewPeerClientPool(mesh, 30*time.Second)
	if _, ok := pool.Client(peerUUID); !ok {
		return nil, errors.New("Worker peer is not currently pinned")
	}
	return &HTTPWorkerClient{baseURL: strings.TrimRight(parsed.String(), "/"), mesh: mesh, peerUUID: peerUUID, pool: pool}, nil
}

func NewHTTPWorkerClient(rawURL string, authTokens ...string) (*HTTPWorkerClient, error) {
	if len(authTokens) > 1 {
		return nil, errors.New("Worker client accepts at most one auth token")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "http" || parsed.Host == "" {
		return nil, errors.New("worker URL must be an absolute http URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("worker URL must not contain credentials or query data")
	}
	host := parsed.Hostname()
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return nil, errors.New("Worker URL must use a literal loopback address")
	}
	return &HTTPWorkerClient{
		baseURL: strings.TrimRight(parsed.String(), "/"),
		authToken: func() string {
			if len(authTokens) == 1 {
				return authTokens[0]
			}
			return ""
		}(),
		http: &http.Client{
			Timeout:   30 * time.Second,
			Transport: &http.Transport{Proxy: nil},
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func (c *HTTPWorkerClient) Worker(ctx context.Context) (json.RawMessage, error) {
	return c.do(ctx, http.MethodGet, "/v1/worker", nil)
}

func (c *HTTPWorkerClient) Create(ctx context.Context, request codexprotocol.TaskRequest) (json.RawMessage, error) {
	encoded, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	return c.do(ctx, http.MethodPost, "/v1/tasks", encoded)
}

func (c *HTTPWorkerClient) Status(ctx context.Context, taskID string) (json.RawMessage, error) {
	return c.do(ctx, http.MethodGet, "/v1/tasks/"+url.PathEscape(taskID), nil)
}

func (c *HTTPWorkerClient) Result(ctx context.Context, taskID string) (json.RawMessage, error) {
	return c.do(ctx, http.MethodGet, "/v1/tasks/"+url.PathEscape(taskID)+"/result", nil)
}

func (c *HTTPWorkerClient) Cancel(ctx context.Context, mutation codexprotocol.Mutation) (json.RawMessage, error) {
	encoded, err := json.Marshal(mutation)
	if err != nil {
		return nil, err
	}
	return c.do(ctx, http.MethodPost, "/v1/tasks/"+url.PathEscape(mutation.TaskID)+"/cancel", encoded)
}

func (c *HTTPWorkerClient) Artifact(ctx context.Context, taskID, artifactID string) ([]byte, error) {
	return c.doBytes(ctx, http.MethodGet, "/v1/tasks/"+url.PathEscape(taskID)+"/artifacts/"+url.PathEscape(artifactID))
}

func (c *HTTPWorkerClient) do(ctx context.Context, method, path string, body []byte) (json.RawMessage, error) {
	var reader io.Reader
	if body != nil {
		reader = strings.NewReader(string(body))
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if c.authToken != "" {
		request.Header.Set("Authorization", "Bearer "+c.authToken)
	}
	client, err := c.requestClient()
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, codexprotocol.MaxContextBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > codexprotocol.MaxContextBytes {
		return nil, errors.New("Worker response exceeds 256 KiB")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("Worker returned HTTP %d", response.StatusCode)
	}
	if !json.Valid(data) {
		return nil, errors.New("Worker returned invalid JSON")
	}
	return json.RawMessage(data), nil
}

func (c *HTTPWorkerClient) doBytes(ctx context.Context, method, path string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	if c.authToken != "" {
		request.Header.Set("Authorization", "Bearer "+c.authToken)
	}
	client, err := c.requestClient()
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, codexprotocol.MaxArtifactBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > codexprotocol.MaxArtifactBytes {
		return nil, errors.New("Worker artifact exceeds 8 MiB response limit")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("Worker returned HTTP %d", response.StatusCode)
	}
	return data, nil
}

func (c *HTTPWorkerClient) requestClient() (*http.Client, error) {
	if c.pool == nil {
		return c.http, nil
	}
	c.mesh.Refresh()
	client, ok := c.pool.Client(c.peerUUID)
	if !ok {
		return nil, errors.New("Worker peer is no longer pinned")
	}
	return client, nil
}
