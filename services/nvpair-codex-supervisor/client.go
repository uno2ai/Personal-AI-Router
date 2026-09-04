// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"nvpair-shared/codexprotocol"
)

type WorkerClient interface {
	Worker(context.Context) (json.RawMessage, error)
	Create(context.Context, codexprotocol.TaskRequest) (json.RawMessage, error)
	Status(context.Context, string) (json.RawMessage, error)
	Result(context.Context, string) (json.RawMessage, error)
	Cancel(context.Context, codexprotocol.Mutation) (json.RawMessage, error)
}

type HTTPWorkerClient struct {
	baseURL string
	http    *http.Client
}

func NewHTTPWorkerClient(rawURL string) (*HTTPWorkerClient, error) {
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
	response, err := c.http.Do(request)
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
