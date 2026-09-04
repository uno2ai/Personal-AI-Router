// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"nvpair-shared/appdir"
	"nvpair-shared/clustertrust"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:0", "loopback listen address")
	workspaceRoot := flag.String("workspace-root", "", "required absolute Worker workspace root")
	stateRoot := flag.String("state-root", "", "durable Worker state directory")
	codexBin := flag.String("codex-bin", "codex", "Codex executable")
	maxConcurrency := flag.Int("max-concurrency", 1, "maximum number of active task leases")
	authToken := flag.String("auth-token", "", "required bearer token for Supervisor requests")
	clusterDir := flag.String("cluster-dir", "", "PAIR cluster directory; enables pinned mTLS remote mode")
	allowedSupervisors := flag.String("supervisor-allowlist", "", "comma-separated authorized Supervisor certificate principals in mTLS mode")
	toolLabels := flag.String("tool-labels", "", "comma-separated local capability labels advertised to the Supervisor")
	artifactMaxBytes := flag.Int64("artifact-max-bytes", 8<<20, "maximum size of one staged artifact")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(Version)
		return
	}
	if *workspaceRoot == "" {
		log.Fatal("--workspace-root is required")
	}
	if *clusterDir == "" {
		if err := validateLoopbackListen(*listen); err != nil {
			log.Fatal(err)
		}
		if *authToken == "" {
			log.Fatal("--auth-token is required")
		}
	} else if err := validateRemoteListen(*listen); err != nil {
		log.Fatal(err)
	} else if strings.TrimSpace(*allowedSupervisors) == "" {
		log.Fatal("--supervisor-allowlist is required in mTLS mode")
	}
	if *maxConcurrency <= 0 {
		log.Fatal("--max-concurrency must be positive")
	}

	policy, err := NewWorkspacePolicy(*workspaceRoot)
	if err != nil {
		log.Fatalf("workspace policy: %v", err)
	}
	root := *stateRoot
	if root == "" {
		root, err = appdir.Path("codex-worker")
		if err != nil {
			log.Fatalf("state root: %v", err)
		}
	}
	journal, err := NewJournal(filepath.Join(root, "tasks.jsonl"))
	if err != nil {
		log.Fatalf("journal: %v", err)
	}
	store, err := NewTaskStore(journal)
	if err != nil {
		_ = journal.Close()
		log.Fatalf("task store: %v", err)
	}
	artifacts, err := NewArtifactStore(filepath.Join(root, "artifacts"), *artifactMaxBytes)
	if err != nil {
		_ = store.Close()
		log.Fatalf("artifact store: %v", err)
	}
	if err := artifacts.Prune(time.Now().UTC()); err != nil {
		_ = store.Close()
		log.Fatalf("prune expired artifacts: %v", err)
	}

	var worker *workerHTTPServer
	var mesh *clustertrust.Mesh
	if *clusterDir != "" {
		mesh = clustertrust.Open(*clusterDir)
		var principals []string
		for _, value := range strings.Split(*allowedSupervisors, ",") {
			if value = strings.TrimSpace(value); value != "" {
				principals = append(principals, value)
			}
		}
		worker = NewServerWithSecurity(store, NewAppServerFactory(*codexBin), policy, artifacts, *maxConcurrency, mesh, principals)
	} else {
		worker = NewServerWithArtifacts(store, NewAppServerFactory(*codexBin), policy, artifacts, *maxConcurrency, *authToken).(*workerHTTPServer)
	}
	worker.toolLabels = commaSeparated(*toolLabels)
	httpServer := &http.Server{Handler: worker}
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		_ = store.Close()
		log.Fatalf("listen %s: %v", *listen, err)
	}
	log.Printf("Codex Worker listening on %s", listener.Addr().String())
	serveErr := make(chan error, 1)
	revocationCtx, stopRevocation := context.WithCancel(context.Background())
	defer stopRevocation()
	if mesh != nil {
		go worker.RevocationLoop(revocationCtx)
		go func() { serveErr <- httpServer.Serve(tls.NewListener(listener, mesh.ServerTLSConfig())) }()
	} else {
		go func() { serveErr <- httpServer.Serve(listener) }()
	}

	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case <-signalCtx.Done():
		stopRevocation()
		worker.Close()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = httpServer.Shutdown(shutdownCtx)
		cancel()
	case err := <-serveErr:
		if err != nil && err != http.ErrServerClosed {
			log.Printf("Worker server stopped: %v", err)
		}
	}
	_ = store.Close()
}

func validateLoopbackListen(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("--listen must be host:port: %w", err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("--listen must use a loopback address, got %q", host)
	}
	return nil
}

func validateRemoteListen(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("--listen must be host:port: %w", err)
	}
	if host == "" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil {
		return nil
	}
	return fmt.Errorf("--listen host must be an IP address in mTLS mode, got %q", host)
}

func commaSeparated(raw string) []string {
	var values []string
	for _, item := range strings.Split(raw, ",") {
		if item = strings.TrimSpace(item); item != "" {
			values = append(values, item)
		}
	}
	return values
}
