// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPWorkerClientRequiresLoopbackAndRejectsRedirectBodies(t *testing.T) {
	if _, err := NewHTTPWorkerClient("http://198.51.100.10:14324"); err == nil {
		t.Fatal("accepted a non-loopback Worker peer")
	}
	if _, err := NewHTTPWorkerClient("http://localhost:14324"); err == nil {
		t.Fatal("accepted a hostname Worker peer")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/secret", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	client, err := NewHTTPWorkerClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Worker(context.Background())
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("redirect/body material escaped through client error: %v", err)
	}
}
