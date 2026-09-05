// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"nvpair-shared/clustertrust"
	"nvpair-shared/noderec"
)

var Version = "dev"

func main() {
	workerURL := flag.String("worker-url", "", "required loopback Worker HTTP URL")
	workerToken := flag.String("worker-token", "", "required bearer token for the Worker")
	workerEndpoints := flag.String("worker-endpoints", "", "comma-separated Worker targets: id=https://ip:14324")
	clusterDir := flag.String("cluster-dir", "", "PAIR cluster directory used for remote Worker mTLS")
	discoveryFile := flag.String("discovery-file", "", "JSON file containing the PAIR discovery:nodes snapshot")
	runtimeDescriptor := flag.String("runtime-descriptor", "", "broker-owned local Worker runtime descriptor")
	stateRoot := flag.String("state-root", "", "durable Supervisor state directory")
	managementSocketDir := flag.String("management-socket-dir", "", "private same-user Supervisor management registry directory")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(Version)
		return
	}
	targets, err := parseWorkerTargets(*workerURL, *workerEndpoints, *workerToken, *clusterDir, *discoveryFile, *runtimeDescriptor)
	if err != nil {
		log.Fatal(err)
	}
	server := NewMCPServerWithWorkers(targets)
	server.SetLocalRuntimeDescriptor(*runtimeDescriptor)
	resolvedStateRoot := *stateRoot
	if resolvedStateRoot == "" {
		resolvedStateRoot = defaultSupervisorStateRoot()
	}
	index, err := OpenTaskIndex(filepath.Join(resolvedStateRoot, "dispatch-index.jsonl"))
	if err != nil {
		log.Fatal(err)
	}
	defer index.Close()
	server.SetTaskIndex(index)
	managementCtx, stopManagement := context.WithCancel(context.Background())
	defer stopManagement()
	if *managementSocketDir != "" {
		socketPath := ManagementSocketPath(*managementSocketDir, os.Getpid())
		removeRegistry, err := WriteManagementRegistry(*managementSocketDir, socketPath, os.Getpid())
		if err != nil {
			log.Fatal(err)
		}
		defer removeRegistry()
		if err := server.StartManagementSocket(managementCtx, socketPath); err != nil {
			log.Fatal(err)
		}
	}
	if err := server.Serve(os.Stdin, os.Stdout); err != nil {
		log.Printf("Supervisor stopped: %v", err)
	}
}

func defaultSupervisorStateRoot() string {
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		return filepath.Join(os.TempDir(), "nvpair-codex-supervisor")
	}
	return filepath.Join(base, "nvidia", "nvpair-codex-supervisor")
}

func parseWorkerTargets(loopbackURL, endpoints, token, clusterDir, discoveryFile, runtimeDescriptor string) ([]WorkerTarget, error) {
	var targets []WorkerTarget
	if loopbackURL != "" {
		if token == "" {
			return nil, fmt.Errorf("--worker-token is required with --worker-url")
		}
		client, err := NewHTTPWorkerClient(loopbackURL, token)
		if err != nil {
			return nil, err
		}
		targets = append(targets, WorkerTarget{ID: "local", Client: client})
	}
	if endpoints != "" {
		if clusterDir == "" {
			return nil, fmt.Errorf("--cluster-dir is required for --worker-endpoints")
		}
		mesh := clustertrust.Open(clusterDir)
		for _, item := range strings.Split(endpoints, ",") {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			id, rawURL, ok := strings.Cut(item, "=")
			if !ok || strings.TrimSpace(id) == "" || strings.TrimSpace(rawURL) == "" {
				return nil, fmt.Errorf("Worker endpoint must be id=https://ip:port")
			}
			if _, err := url.Parse(rawURL); err != nil {
				return nil, err
			}
			client, err := NewMTLSWorkerClient(strings.TrimSpace(rawURL), mesh, strings.TrimSpace(id))
			if err != nil {
				return nil, fmt.Errorf("Worker %s: %w", id, err)
			}
			targets = append(targets, WorkerTarget{ID: strings.TrimSpace(id), Client: client})
		}
	}
	if discoveryFile != "" {
		if clusterDir == "" {
			return nil, fmt.Errorf("--cluster-dir is required for --discovery-file")
		}
		mesh := clustertrust.Open(clusterDir)
		nodes, err := readDiscoveryNodes(discoveryFile)
		if err != nil {
			return nil, err
		}
		discovered, err := NewWorkerDiscovery(mesh).Discover(context.Background(), nodes)
		if err != nil {
			return nil, err
		}
		targets = append(targets, discovered...)
	}
	if len(targets) == 0 && runtimeDescriptor == "" {
		return nil, fmt.Errorf("--worker-url, --worker-endpoints, or --runtime-descriptor is required")
	}
	return targets, nil
}

func readDiscoveryNodes(path string) ([]noderec.DirectoryNode, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read discovery snapshot: %w", err)
	}
	var nodes []noderec.DirectoryNode
	if err := json.Unmarshal(data, &nodes); err == nil {
		return nodes, nil
	}
	var envelope struct {
		Nodes []noderec.DirectoryNode `json:"nodes"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, fmt.Errorf("decode discovery snapshot: %w", err)
	}
	return envelope.Nodes, nil
}
