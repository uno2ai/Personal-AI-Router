// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"nvpair-shared/codexruntime"
)

const managedWorkerConfigSchemaVersion = 1

var (
	managedDescriptorTTL             = 15 * time.Second
	managedDescriptorRefreshInterval = 5 * time.Second
)

type managedWorkerConfig struct {
	SchemaVersion         int      `json:"schemaVersion"`
	InstallationID        string   `json:"installationId"`
	WorkerInstanceID      string   `json:"workerInstanceId"`
	BootEpoch             uint64   `json:"bootEpoch"`
	Generation            uint64   `json:"generation"`
	WorkspaceRoot         string   `json:"workspaceRoot"`
	StateRoot             string   `json:"stateRoot"`
	CodexBin              string   `json:"codexBin"`
	MaxConcurrency        int      `json:"maxConcurrency"`
	AuthToken             string   `json:"authToken"`
	RuntimeDescriptorPath string   `json:"runtimeDescriptorPath"`
	CredentialRef         string   `json:"credentialRef"`
	CredentialGeneration  uint64   `json:"credentialGeneration"`
	PolicyRevision        uint64   `json:"policyRevision"`
	ArtifactMaxBytes      int64    `json:"artifactMaxBytes"`
	ToolLabels            []string `json:"toolLabels,omitempty"`
	WorkspaceAlias        string   `json:"workspaceAlias"`
}

func (c managedWorkerConfig) Validate() error {
	if c.SchemaVersion != managedWorkerConfigSchemaVersion {
		return fmt.Errorf("unsupported managed Worker config schema version %d", c.SchemaVersion)
	}
	if strings.TrimSpace(c.InstallationID) == "" || strings.TrimSpace(c.WorkerInstanceID) == "" {
		return errors.New("installationId and workerInstanceId are required")
	}
	if c.BootEpoch == 0 || c.Generation == 0 {
		return errors.New("bootEpoch and generation must be positive")
	}
	if !filepath.IsAbs(c.WorkspaceRoot) || !filepath.IsAbs(c.StateRoot) {
		return errors.New("workspaceRoot and stateRoot must be absolute paths")
	}
	if strings.TrimSpace(c.CodexBin) == "" || strings.TrimSpace(c.AuthToken) == "" {
		return errors.New("codexBin and authToken are required")
	}
	if c.MaxConcurrency <= 0 {
		return errors.New("maxConcurrency must be positive")
	}
	if !filepath.IsAbs(c.RuntimeDescriptorPath) || !filepath.IsAbs(c.CredentialRef) {
		return errors.New("runtimeDescriptorPath and credentialRef must be absolute paths")
	}
	if c.CredentialGeneration == 0 || c.PolicyRevision == 0 {
		return errors.New("credentialGeneration and policyRevision must be positive")
	}
	if c.ArtifactMaxBytes <= 0 {
		return errors.New("artifactMaxBytes must be positive")
	}
	if strings.TrimSpace(c.WorkspaceAlias) == "" {
		return errors.New("workspaceAlias is required")
	}
	return nil
}

func readManagedConfig(path string) (managedWorkerConfig, error) {
	file, err := os.Open(path)
	if err != nil {
		return managedWorkerConfig{}, fmt.Errorf("open managed Worker config: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var config managedWorkerConfig
	if err := decoder.Decode(&config); err != nil {
		return managedWorkerConfig{}, fmt.Errorf("decode managed Worker config: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return managedWorkerConfig{}, errors.New("managed Worker config contains trailing JSON")
		}
		return managedWorkerConfig{}, fmt.Errorf("decode trailing managed Worker config: %w", err)
	}
	if err := config.Validate(); err != nil {
		return managedWorkerConfig{}, err
	}
	return config, nil
}

type managedWorkerController struct {
	mu            sync.Mutex
	config        managedWorkerConfig
	worker        *workerHTTPServer
	store         *TaskStore
	httpServer    *http.Server
	descriptor    codexruntime.RuntimeDescriptor
	heartbeatStop chan struct{}
	heartbeatDone chan struct{}
	closed        bool
}

func runManagedControl(input io.Reader, output io.Writer) error {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	var controller *managedWorkerController
	emit := func(event codexruntime.ControlEvent) error {
		data, err := codexruntime.MarshalControlEvent(event)
		if err != nil {
			return err
		}
		_, err = output.Write(data)
		return err
	}
	stopController := func() {
		if controller != nil {
			_ = controller.Stop()
			controller = nil
		}
	}
	defer stopController()

	for scanner.Scan() {
		message, err := codexruntime.UnmarshalControlMessage(scanner.Bytes())
		if err != nil {
			if emit(codexruntime.ControlEvent{
				SchemaVersion: codexruntime.ControlSchemaVersion,
				Kind:          codexruntime.ControlEventError,
				Error:         err.Error(),
			}) != nil {
				return err
			}
			continue
		}

		switch message.Kind {
		case codexruntime.ControlKindStart:
			if controller != nil {
				if err := emit(codexruntime.ControlEvent{SchemaVersion: codexruntime.ControlSchemaVersion, Kind: codexruntime.ControlEventError, RequestID: message.RequestID, Error: "Worker is already started"}); err != nil {
					return err
				}
				continue
			}
			config, err := readManagedConfig(message.ConfigPath)
			if err == nil && message.ConfigRevision != config.PolicyRevision {
				err = fmt.Errorf("control config revision %d does not match Worker policy revision %d", message.ConfigRevision, config.PolicyRevision)
			}
			if err == nil {
				controller, err = startManagedWorker(config)
			}
			if err != nil {
				if emit(codexruntime.ControlEvent{SchemaVersion: codexruntime.ControlSchemaVersion, Kind: codexruntime.ControlEventError, RequestID: message.RequestID, Error: err.Error()}) != nil {
					return err
				}
				continue
			}
			if err := emit(codexruntime.ControlEvent{SchemaVersion: codexruntime.ControlSchemaVersion, Kind: codexruntime.ControlEventReady, RequestID: message.RequestID, ConfigRevision: message.ConfigRevision, State: codexruntime.RuntimeStateReady, Descriptor: controller.descriptorCopy()}); err != nil {
				return err
			}
		case codexruntime.ControlKindStatus:
			if controller == nil {
				if err := emit(codexruntime.ControlEvent{SchemaVersion: codexruntime.ControlSchemaVersion, Kind: codexruntime.ControlEventStatus, RequestID: message.RequestID, State: codexruntime.RuntimeStateUnavailable}); err != nil {
					return err
				}
				continue
			}
			if err := controller.Refresh(); err != nil {
				return err
			}
			if err := emit(codexruntime.ControlEvent{SchemaVersion: codexruntime.ControlSchemaVersion, Kind: codexruntime.ControlEventStatus, RequestID: message.RequestID, State: controller.descriptorCopy().State, Descriptor: controller.descriptorCopy()}); err != nil {
				return err
			}
		case codexruntime.ControlKindDrain:
			if controller != nil {
				controller.mu.Lock()
				controller.descriptor.State = codexruntime.RuntimeStateDraining
				descriptor := controller.descriptor
				controller.mu.Unlock()
				if err := emit(codexruntime.ControlEvent{SchemaVersion: codexruntime.ControlSchemaVersion, Kind: codexruntime.ControlEventStatus, RequestID: message.RequestID, State: descriptor.State, Descriptor: &descriptor}); err != nil {
					return err
				}
			}
		case codexruntime.ControlKindShutdown:
			stopController()
			if err := emit(codexruntime.ControlEvent{SchemaVersion: codexruntime.ControlSchemaVersion, Kind: codexruntime.ControlEventStopped, RequestID: message.RequestID, State: codexruntime.RuntimeStateStopped}); err != nil {
				return err
			}
			return nil
		case codexruntime.ControlKindConfigRevision:
			if controller == nil {
				if err := emit(codexruntime.ControlEvent{SchemaVersion: codexruntime.ControlSchemaVersion, Kind: codexruntime.ControlEventError, RequestID: message.RequestID, Error: "configRevision requires a running Worker"}); err != nil {
					return err
				}
				continue
			}
			config, err := readManagedConfig(message.ConfigPath)
			if err == nil && message.ConfigRevision <= controller.config.PolicyRevision {
				err = fmt.Errorf("config revision %d is not newer than current revision %d", message.ConfigRevision, controller.config.PolicyRevision)
			}
			if err == nil {
				err = controller.Stop()
			}
			if err == nil {
				controller, err = startManagedWorker(config)
			}
			if err != nil {
				if emit(codexruntime.ControlEvent{SchemaVersion: codexruntime.ControlSchemaVersion, Kind: codexruntime.ControlEventError, RequestID: message.RequestID, Error: err.Error()}); err != nil {
					return err
				}
				controller = nil
				continue
			}
			if err := emit(codexruntime.ControlEvent{SchemaVersion: codexruntime.ControlSchemaVersion, Kind: codexruntime.ControlEventReady, RequestID: message.RequestID, ConfigRevision: message.ConfigRevision, State: codexruntime.RuntimeStateReady, Descriptor: controller.descriptorCopy()}); err != nil {
				return err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return nil
}

func startManagedWorker(config managedWorkerConfig) (*managedWorkerController, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	codexBin, err := resolveCodexExecutable(config.CodexBin)
	if err != nil {
		return nil, err
	}
	config.CodexBin = codexBin
	if err := os.MkdirAll(config.WorkspaceRoot, 0o700); err != nil {
		return nil, fmt.Errorf("create Worker workspace: %w", err)
	}
	if err := os.MkdirAll(config.StateRoot, 0o700); err != nil {
		return nil, fmt.Errorf("create Worker state root: %w", err)
	}
	policy, err := NewWorkspacePolicy(config.WorkspaceRoot)
	if err != nil {
		return nil, fmt.Errorf("workspace policy: %w", err)
	}
	journal, err := NewJournal(filepath.Join(config.StateRoot, "tasks.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("journal: %w", err)
	}
	store, err := NewTaskStore(journal)
	if err != nil {
		_ = journal.Close()
		return nil, fmt.Errorf("task store: %w", err)
	}
	artifacts, err := NewArtifactStore(filepath.Join(config.StateRoot, "artifacts"), config.ArtifactMaxBytes)
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("artifact store: %w", err)
	}
	if err := artifacts.Prune(time.Now().UTC()); err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("prune artifacts: %w", err)
	}
	worker := NewServerWithArtifacts(store, NewAppServerFactory(config.CodexBin), policy, artifacts, config.MaxConcurrency, config.AuthToken).(*workerHTTPServer)
	worker.toolLabels = append([]string(nil), config.ToolLabels...)
	certificate, fingerprint, err := localServerCertificate()
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("listen local Worker: %w", err)
	}
	if err := writeManagedCredential(config.CredentialRef, config.AuthToken, fingerprint, config.CredentialGeneration); err != nil {
		_ = listener.Close()
		_ = store.Close()
		return nil, err
	}
	httpServer := &http.Server{Handler: worker}
	tlsListener := tls.NewListener(listener, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS13})
	controller := &managedWorkerController{
		config:     config,
		worker:     worker,
		store:      store,
		httpServer: httpServer,
		descriptor: codexruntime.RuntimeDescriptor{
			SchemaVersion:           codexruntime.RuntimeSchemaVersion,
			InstallationID:          config.InstallationID,
			WorkerInstanceID:        config.WorkerInstanceID,
			BootEpoch:               max(config.BootEpoch, uint64(time.Now().UnixNano())),
			Generation:              config.Generation,
			WrittenAt:               time.Now().UTC(),
			ExpiresAt:               time.Now().UTC().Add(managedDescriptorTTL),
			Endpoint:                "https://" + listener.Addr().String(),
			Transport:               codexruntime.TransportPinnedLocalTLS,
			ServerCertificateSHA256: fingerprint,
			CredentialRef:           config.CredentialRef,
			CredentialGeneration:    config.CredentialGeneration,
			PolicyRevision:          config.PolicyRevision,
			State:                   codexruntime.RuntimeStateReady,
		},
	}
	if err := codexruntime.WriteDescriptor(config.RuntimeDescriptorPath, controller.descriptor); err != nil {
		_ = listener.Close()
		_ = store.Close()
		return nil, fmt.Errorf("publish runtime descriptor: %w", err)
	}
	controller.heartbeatStop = make(chan struct{})
	controller.heartbeatDone = make(chan struct{})
	go controller.runHeartbeat()
	go func() {
		if err := httpServer.Serve(tlsListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			// The control loop reports the lifecycle; Serve errors are not task data.
		}
	}()
	return controller, nil
}

func resolveCodexExecutable(value string) (string, error) {
	if filepath.IsAbs(value) {
		info, err := os.Stat(value)
		if err != nil {
			return "", fmt.Errorf("Codex executable is unavailable: %w", err)
		}
		if info.IsDir() {
			return "", errors.New("Codex executable path is a directory")
		}
		if info.Mode().Perm()&0o111 == 0 && runtime.GOOS != "windows" {
			return "", errors.New("Codex executable is not executable")
		}
		return value, nil
	}
	resolved, err := exec.LookPath(value)
	if err != nil {
		return "", fmt.Errorf("find Codex executable %q: %w", value, err)
	}
	return resolved, nil
}

func (c *managedWorkerController) runHeartbeat() {
	ticker := time.NewTicker(managedDescriptorRefreshInterval)
	defer ticker.Stop()
	defer close(c.heartbeatDone)
	for {
		select {
		case <-c.heartbeatStop:
			return
		case <-ticker.C:
			_ = c.Refresh()
		}
	}
}

func (c *managedWorkerController) descriptorCopy() *codexruntime.RuntimeDescriptor {
	c.mu.Lock()
	defer c.mu.Unlock()
	descriptor := c.descriptor
	return &descriptor
}

func (c *managedWorkerController) Refresh() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("Worker is stopped")
	}
	c.descriptor.Generation++
	c.descriptor.WrittenAt = time.Now().UTC()
	c.descriptor.ExpiresAt = c.descriptor.WrittenAt.Add(managedDescriptorTTL)
	return codexruntime.WriteDescriptor(c.config.RuntimeDescriptorPath, c.descriptor)
}

func (c *managedWorkerController) Stop() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	server := c.httpServer
	worker := c.worker
	store := c.store
	descriptorPath := c.config.RuntimeDescriptorPath
	heartbeatStop := c.heartbeatStop
	heartbeatDone := c.heartbeatDone
	if heartbeatStop != nil {
		close(heartbeatStop)
	}
	c.mu.Unlock()
	if heartbeatDone != nil {
		<-heartbeatDone
	}
	worker.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	err := server.Shutdown(ctx)
	cancel()
	if closeErr := store.Close(); err == nil {
		err = closeErr
	}
	if removeErr := os.Remove(descriptorPath); err == nil && !errors.Is(removeErr, os.ErrNotExist) {
		err = removeErr
	}
	return err
}

func writeManagedCredential(path, token, fingerprint string, generation uint64) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create credential directory: %w", err)
	}
	data, err := json.Marshal(struct {
		BearerToken             string `json:"bearerToken"`
		ServerCertificateSHA256 string `json:"serverCertificateSha256"`
		CredentialGeneration    uint64 `json:"credentialGeneration"`
	}{BearerToken: token, ServerCertificateSHA256: fingerprint, CredentialGeneration: generation})
	if err != nil {
		return fmt.Errorf("marshal Worker credential: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open Worker credential: %w", err)
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		_ = file.Close()
		return fmt.Errorf("write Worker credential: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync Worker credential: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close Worker credential: %w", err)
	}
	return nil
}

func localServerCertificate() (tls.Certificate, string, error) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, "", fmt.Errorf("generate local Worker certificate key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return tls.Certificate{}, "", fmt.Errorf("generate local Worker certificate serial: %w", err)
	}
	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "nvpair-codex-worker-local"},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return tls.Certificate{}, "", fmt.Errorf("create local Worker certificate: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return tls.Certificate{}, "", fmt.Errorf("marshal local Worker key: %w", err)
	}
	certificate, err := tls.X509KeyPair(
		pemEncode("CERTIFICATE", der),
		pemEncode("PRIVATE KEY", keyDER),
	)
	if err != nil {
		return tls.Certificate{}, "", fmt.Errorf("load local Worker certificate: %w", err)
	}
	digest := sha256.Sum256(der)
	return certificate, hex.EncodeToString(digest[:]), nil
}

func pemEncode(kind string, data []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: data})
}
