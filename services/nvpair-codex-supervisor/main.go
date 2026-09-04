// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"flag"
	"fmt"
	"log"
	"os"
)

var Version = "dev"

func main() {
	workerURL := flag.String("worker-url", "", "required loopback Worker HTTP URL")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(Version)
		return
	}
	if *workerURL == "" {
		log.Fatal("--worker-url is required")
	}
	client, err := NewHTTPWorkerClient(*workerURL)
	if err != nil {
		log.Fatal(err)
	}
	if err := NewMCPServer(client).Serve(os.Stdin, os.Stdout); err != nil {
		log.Printf("Supervisor stopped: %v", err)
	}
}
