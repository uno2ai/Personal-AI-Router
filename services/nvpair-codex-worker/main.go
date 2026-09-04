// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"nvpair-shared/appdir"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:0", "loopback listen address")
	workspaceRoot := flag.String("workspace-root", "", "required absolute Worker workspace root")
	stateRoot := flag.String("state-root", "", "durable Worker state directory")
	codexBin := flag.String("codex-bin", "codex", "Codex executable")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(Version)
		return
	}
	if *workspaceRoot == "" {
		log.Fatal("--workspace-root is required")
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

	worker := &workerHTTPServer{
		store:   store,
		factory: NewAppServerFactory(*codexBin),
		policy:  policy,
		active:  make(map[string]*taskRun),
	}
	httpServer := &http.Server{Handler: worker}
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		_ = store.Close()
		log.Fatalf("listen %s: %v", *listen, err)
	}
	log.Printf("Codex Worker listening on %s", listener.Addr().String())
	serveErr := make(chan error, 1)
	go func() { serveErr <- httpServer.Serve(listener) }()

	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case <-signalCtx.Done():
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
