<!--
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
-->

# Native Codex Supervisor over PAIR Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the Phase 1 local vertical slice in which Main Codex calls a real local MCP server, the Supervisor submits a bounded task to a Worker Gateway, and the Worker launches native `codex app-server` in a policy-checked local workspace and returns a durable, compact handoff.

**Architecture:** Add a small shared `codexprotocol` package for versioned envelopes, bounded context/handoffs, task state, events, leases, and artifact metadata. Add two standalone Go modules: `nvpair-codex-worker` exposes the loopback Supervisor Protocol and owns the journal, leases, workspace policy, and app-server child; `nvpair-codex-supervisor` exposes MCP over stdio and calls the Worker over loopback HTTP. Phase 1 deliberately does not advertise mDNS, open a LAN port, or accept remote approvals; those are subsequent spec phases after the local contract is proven.

**Tech Stack:** Go 1.25; `net/http`; newline-delimited JSON-RPC via the existing `nvpair-shared/jsonrpc`; native `codex app-server --listen stdio://`; append-only JSONL with `fsync`; `httptest`; `os/exec`; MCP JSON-RPC over stdio.

**Spec:** `docs/superpowers/specs/2026-09-04-codex-supervisor-pair-control-plane.md`

## Global Constraints

- Native local Codex processes are the only agent runtime; no Xamong runtime is introduced.
- The Main Codex is the delegation authority; Worker-to-Worker delegation and remote approval relay are absent in v1.
- The Worker must validate `cwd`, sandbox, approval policy, and workspace allowlist before starting `codex app-server`.
- Context packages are capped at 256 KiB and handoffs at 64 KiB.
- Every mutation carries `protocolVersion`, `requestId`, `taskId`, `attemptId`, and `leaseEpoch`.
- The Worker journals a mutation and calls `fsync` before acknowledging it; startup replays the journal and never silently replays a side-effecting turn.
- A `taskId` has at most one active attempt and a workspace has at most one active lease; network loss never automatically releases either.
- Approval requests become `blocked/approval_required`; no protocol endpoint can turn an AI-generated approval into a human approval.
- Do not log prompts, response bodies, hidden reasoning, source bodies, credentials, pins, or key material.
- Every new Go module has tests beside its source and an SPDX header on every file.
- Commits are small and signed off with `git commit -s`.

---

### Task 1: Add the shared Supervisor Protocol types and validation

**Files:**
- Create: `services/shared/codexprotocol/codexprotocol.go`
- Test: `services/shared/codexprotocol/codexprotocol_test.go`
- Modify: `services/shared/go.mod` only if the package needs a new dependency; use the standard library first.

**Interfaces:**
- Produces `codexprotocol.TaskRequest`, `ContextPackage`, `WorkspaceSpec`, `ExecutionSpec`, `Mutation`, `TaskRecord`, `TaskEvent`, `Handoff`, and `ArtifactManifest` for both new services.
- Produces `DecodeTaskRequest([]byte) (TaskRequest, error)`, `ContextPackage.Validate() error`, `Handoff.Validate(taskID, attemptID string) error`, and `Mutation.Validate() error`.
- Produces state constants `accepted`, `starting`, `running`, `waiting_approval`, `cancelling`, `completed`, `blocked`, `failed`, `cancelled`, and `lost`.

- [ ] **Step 1: Write the failing validation tests.**

```go
func TestDecodeTaskRequestRejectsOversizedContext(t *testing.T) {
	request := validTaskRequest()
	request.Context.Objective = strings.Repeat("x", MaxContextBytes)
	payload, err := json.Marshal(request)
	if err != nil { t.Fatal(err) }
	if _, err := DecodeTaskRequest(payload); err == nil || !strings.Contains(err.Error(), "256 KiB") {
		t.Fatalf("expected bounded-context error, got %v", err)
	}
}

func TestMutationRequiresAllFencingFields(t *testing.T) {
	mutation := Mutation{ProtocolVersion: 1, RequestID: "r1", TaskID: "t1", AttemptID: "a1"}
	if err := mutation.Validate(); err == nil || !strings.Contains(err.Error(), "leaseEpoch") {
		t.Fatalf("expected lease error, got %v", err)
	}
}

func TestHandoffRejectsMismatchedTaskAndOversizedEvidence(t *testing.T) {
	handoff := validHandoff()
	handoff.TaskID = "other-task"
	if err := handoff.Validate("task-1", "attempt-1"); err == nil {
		t.Fatal("expected task identity mismatch")
	}
	handoff = validHandoff()
	handoff.Summary = strings.Repeat("x", MaxHandoffBytes)
	if err := handoff.Validate("task-1", "attempt-1"); err == nil || !strings.Contains(err.Error(), "64 KiB") {
		t.Fatalf("expected bounded-handoff error, got %v", err)
	}
}
```

- [ ] **Step 2: Run the focused test to verify it fails.**

Run: `cd services/shared && go test ./codexprotocol`

Expected: FAIL because the package, types, and validators do not exist.

- [ ] **Step 3: Implement the protocol types and closed-set validation.**

Define `MaxContextBytes = 256 << 10`, `MaxHandoffBytes = 64 << 10`, `ProtocolVersion = 1`, and JSON tags matching the spec (`requestId`, `taskId`, `attemptId`, `leaseEpoch`, `relevantDecisions`, `requiredEvidence`, `wallSeconds`, `approval`, `baseRevision`, and `sha256`). Marshal the complete value to enforce byte limits. Reject missing IDs, non-positive epochs, unsupported protocol versions, empty objectives, unknown task states, absolute or parent-traversing workspace paths, and execution modes outside `read`/`write` plus `local-only` approval. Keep validation independent of the filesystem so the Worker can perform filesystem checks in its policy package.

- [ ] **Step 4: Run the focused tests and package-wide shared tests.**

Run: `cd services/shared && go test ./codexprotocol ./...`

Expected: PASS, with no changes to existing shared wire contracts.

- [ ] **Step 5: Commit the shared contract.**

```bash
git add services/shared/codexprotocol services/shared/go.mod
git commit -s -m "feat: add Codex supervisor protocol types"
```

### Task 2: Build durable Worker journal, task state, and fenced leases

**Files:**
- Create: `services/nvpair-codex-worker/go.mod`
- Create: `services/nvpair-codex-worker/journal.go`
- Create: `services/nvpair-codex-worker/store.go`
- Test: `services/nvpair-codex-worker/store_test.go`

**Interfaces:**
- Consumes `nvpair-shared/codexprotocol` from Task 1.
- Produces `NewJournal(path string) (*Journal, error)`, `(*Journal).Append(JournalEntry) error`, `(*Journal).Replay() ([]JournalEntry, error)`, `NewTaskStore(journal *Journal) (*TaskStore, error)`, `(*TaskStore).Accept(TaskRequest) (TaskRecord, bool, error)`, `(*TaskStore).Get(taskID string) (TaskRecord, bool)`, `(*TaskStore).Mutate(Mutation, func(*TaskRecord) error) error`, and `(*TaskStore).Events(taskID string, after uint64) []TaskEvent`.

- [ ] **Step 1: Write tests for fsync-before-ack, replay, idempotency, and fencing.**

```go
func TestJournalReplayRebuildsIdempotencyAfterReopen(t *testing.T) {
	root := t.TempDir()
	request := validTaskRequest("request-1", "task-1", "attempt-1", 1)
	first, err := NewTaskStore(mustJournal(filepath.Join(root, "tasks.jsonl")))
	if err != nil { t.Fatal(err) }
	created, duplicate, err := first.Accept(request)
	if err != nil || duplicate || created.TaskID != "task-1" { t.Fatalf("accept: %+v duplicate=%v err=%v", created, duplicate, err) }
	if err := first.Close(); err != nil { t.Fatal(err) }
	second, err := NewTaskStore(mustJournal(filepath.Join(root, "tasks.jsonl")))
	if err != nil { t.Fatal(err) }
	replayed, duplicate, err := second.Accept(request)
	if err != nil || !duplicate || replayed.AttemptID != "attempt-1" { t.Fatalf("replay: %+v duplicate=%v err=%v", replayed, duplicate, err) }
}

func TestStaleEpochCannotMutateOrReleaseWorkspace(t *testing.T) {
	store := newTestStore(t)
	request := validTaskRequest("request-1", "task-1", "attempt-1", 4)
	if _, _, err := store.Accept(request); err != nil { t.Fatal(err) }
	err := store.Mutate(Mutation{ProtocolVersion: 1, RequestID: "cancel-1", TaskID: "task-1", AttemptID: "attempt-1", LeaseEpoch: 3}, func(record *TaskRecord) error {
		record.State = StateCancelled
		return nil
	})
	if err == nil || !errors.Is(err, ErrStaleLease) { t.Fatalf("expected stale lease, got %v", err) }
}

func TestWorkspaceLeaseBlocksSecondTaskAcrossSupervisors(t *testing.T) {
	store := newTestStore(t)
	if _, _, err := store.Accept(validTaskRequest("r1", "t1", "a1", 1)); err != nil { t.Fatal(err) }
	if _, _, err := store.Accept(validTaskRequest("r2", "t2", "a2", 1)); !errors.Is(err, ErrWorkspaceBusy) {
		t.Fatalf("expected workspace lease conflict, got %v", err)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail.**

Run: `cd services/nvpair-codex-worker && go test ./...`

Expected: FAIL because the Worker module and store do not exist.

- [ ] **Step 3: Implement the append-only JSONL journal.**

Open the configured file with `O_CREATE|O_APPEND|O_WRONLY`, mode `0600`. Encode one JSON object per line containing an operation kind, request identity, full `TaskRecord` snapshot, optional event, and optional compact result. Write the line, call `Sync`, and only then return. Replay line-by-line, reject malformed or conflicting sequence numbers, reconstruct idempotency by `requestId`, reconstruct current records by `taskId`, and rebuild per-task event sequence numbers. `Close` must sync and close the file. Do not compact in Phase 1.

- [ ] **Step 4: Implement the in-memory index and fenced mutation rules.**

`Accept` validates the request, returns the prior record for an identical `requestId`/payload, rejects a request ID reused with different bytes, rejects an occupied canonical workspace, persists the accepted record before returning, and records the workspace lease. `Mutate` checks protocol version, task ID, attempt ID, and exact `leaseEpoch` under one mutex; only then invokes the state transition, increments the event sequence, persists the new snapshot/event, and updates indexes. Never release a lease merely because a caller lost its HTTP connection. Expose an explicit `FenceAndRelease` method for the later operator-confirmed child-termination path, but do not call it from network error handling.

- [ ] **Step 5: Run the tests and commit.**

Run: `cd services/nvpair-codex-worker && go test ./...`

Expected: PASS, including reopening the same journal and rejecting stale epochs.

```bash
git add services/nvpair-codex-worker
git commit -s -m "feat: add durable Codex worker task store"
```

### Task 3: Add Worker workspace policy and native app-server adapter

**Files:**
- Create: `services/nvpair-codex-worker/policy.go`
- Create: `services/nvpair-codex-worker/appserver.go`
- Create: `services/nvpair-codex-worker/appserver_test.go`
- Modify: `services/nvpair-codex-worker/go.mod` to retain only the local `nvpair-shared` replacement.

**Interfaces:**
- Consumes `TaskStore` and `codexprotocol.TaskRequest` from Task 2 and `nvpair-shared/jsonrpc`.
- Produces `NewWorkspacePolicy(root string) (WorkspacePolicy, error)`, `WorkspacePolicy.Resolve(spec WorkspaceSpec) (string, error)`, `NewAppServerFactory(binary string) AppServerFactory`, and `AppServerSession.Run(ctx context.Context, request TaskRequest, emit func(TaskEvent)) (Handoff, error)`.

- [ ] **Step 1: Write failing policy and adapter tests.**

```go
func TestWorkspacePolicyRejectsTraversalAndSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o600); err != nil { t.Fatal(err) }
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil { t.Skipf("symlink unavailable: %v", err) }
	policy, err := NewWorkspacePolicy(root)
	if err != nil { t.Fatal(err) }
	for _, path := range []string{"../secret", "linked/secret.txt", "/tmp/outside"} {
		if _, err := policy.Resolve(WorkspaceSpec{Path: path, Mode: "read"}); err == nil {
			t.Errorf("Resolve(%q) accepted an unsafe path", path)
		}
	}
}

func TestAppServerAdapterUsesThreadStartAndTurnStart(t *testing.T) {
	fixture := buildFakeAppServer(t)
	session := NewAppServerFactory(fixture).New()
	handoff, err := session.Run(context.Background(), validTaskRequest("r1", "t1", "a1", 1), func(event TaskEvent) {})
	if err != nil { t.Fatal(err) }
	if handoff.Status != "completed" || handoff.TaskID != "t1" { t.Fatalf("handoff=%+v", handoff) }
	if got := readFixtureLog(t); !strings.Contains(got, `"method":"thread/start"`) || !strings.Contains(got, `"method":"turn/start"`) {
		t.Fatalf("adapter did not issue native app-server methods: %s", got)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail.**

Run: `cd services/nvpair-codex-worker && go test ./... -run 'TestWorkspacePolicy|TestAppServerAdapter'`

Expected: FAIL because policy, factory, and adapter types are not defined.

- [ ] **Step 3: Implement canonical workspace resolution.**

Canonicalize the configured root with `filepath.EvalSymlinks` and require it to be an existing directory. Reject absolute request paths, empty path components, `.`/`..` components, and any resolved path not contained by `filepath.Rel` under the canonical root. Walk every existing component with `os.Lstat`; reject symlinks on all platforms and reject Windows reparse points in the Windows-specific implementation. Return the canonical path only after all checks. Resolve the phase-one workspace alias `local` to the configured root; do not accept arbitrary host paths from the Supervisor.

- [ ] **Step 4: Implement the app-server child adapter with fail-closed approval handling.**

Start `exec.CommandContext(ctx, binary, "app-server", "--listen", "stdio://")`, connect stdin/stdout through `jsonrpc.NewPeer`, and send `initialize` with a fixed client name/version. Send `thread/start` with the validated canonical `cwd`, `sandbox` mapped from `read` to `read-only` and `write` to `workspace-write`, and `approvalPolicy` set to `on-request` for `local-only`. Send `turn/start` with the returned thread ID and one text input containing the bounded objective and required evidence. Convert public `item/agentMessage/delta`, `item/commandExecution/outputDelta`, `turn/completed`, and `error` frames into bounded `TaskEvent` metadata; never copy raw prompt or response bodies into logs or the network result. On `item/commandExecution/requestApproval`, `item/fileChange/requestApproval`, `item/permissions/requestApproval`, or `item/tool/requestUserInput`, emit `approval_required`, return a denial to the child, and finish with `blocked` rather than accepting an AI-generated approval. On cancellation call `turn/interrupt` for the current thread/turn, wait for process exit, and expose child identity for lease reconciliation.

- [ ] **Step 5: Run tests and commit the adapter.**

Run: `cd services/nvpair-codex-worker && go test ./...`

Expected: PASS, including traversal rejection, symlink rejection, native method exchange, approval blocking, and cancellation fixture coverage.

```bash
git add services/nvpair-codex-worker
git commit -s -m "feat: add safe Codex app-server worker adapter"
```

### Task 4: Expose the loopback Worker HTTP protocol

**Files:**
- Create: `services/nvpair-codex-worker/httpserver.go`
- Create: `services/nvpair-codex-worker/main.go`
- Create: `services/nvpair-codex-worker/httpserver_test.go`
- Create: `services/nvpair-codex-worker/README.md`

**Interfaces:**
- Consumes `TaskStore`, `WorkspacePolicy`, and `AppServerFactory` from Tasks 2–3.
- Produces `NewServer(store *TaskStore, factory AppServerFactory, policy WorkspacePolicy) http.Handler`, loopback flags `--listen`, `--state-root`, `--workspace-root`, `--codex-bin`, and JSON endpoints `GET /v1/worker`, `POST /v1/tasks`, `GET /v1/tasks/{id}`, `GET /v1/tasks/{id}/events`, `POST /v1/tasks/{id}/cancel`, and `GET /v1/tasks/{id}/result`.

- [ ] **Step 1: Write endpoint tests before implementation.**

```go
func TestCreateTaskPersistsBeforeReturningAndIsIdempotent(t *testing.T) {
	server := newTestHTTPServer(t)
	payload := mustJSON(validTaskRequest("request-1", "task-1", "attempt-1", 1))
	first := post(t, server.URL+"/v1/tasks", payload)
	second := post(t, server.URL+"/v1/tasks", payload)
	if first.Code != http.StatusAccepted || second.Code != http.StatusAccepted { t.Fatalf("codes %d %d", first.Code, second.Code) }
	var a, b TaskResponse
	decode(t, first, &a); decode(t, second, &b)
	if a.Record != b.Record || !b.Idempotent { t.Fatalf("non-idempotent duplicate: %+v %+v", a, b) }
}

func TestApprovalCannotBeRelayedOverHTTP(t *testing.T) {
	server := newTestHTTPServer(t)
	request := validTaskRequest("request-1", "task-1", "attempt-1", 1)
	request.Execution.Approval = "local-only"
	result := post(t, server.URL+"/v1/tasks", mustJSON(request))
	if result.Code != http.StatusAccepted { t.Fatalf("create status %d", result.Code) }
	approval := post(t, server.URL+"/v1/tasks/task-1/approvals/a1", []byte(`{"approved":true}`))
	if approval.Code != http.StatusNotFound { t.Fatalf("remote approval endpoint exists: %d", approval.Code) }
}

func TestCancellationRequiresCurrentLeaseEpoch(t *testing.T) {
	server := newTestHTTPServer(t)
	stale := post(t, server.URL+"/v1/tasks/task-1/cancel", []byte(`{"requestId":"c1","attemptId":"attempt-1","leaseEpoch":0}`))
	if stale.Code != http.StatusConflict { t.Fatalf("status=%d", stale.Code) }
}
```

- [ ] **Step 2: Run endpoint tests to verify they fail.**

Run: `cd services/nvpair-codex-worker && go test ./... -run 'TestCreateTask|TestApprovalCannot|TestCancellation'`

Expected: FAIL because the HTTP server and response types do not exist.

- [ ] **Step 3: Implement the HTTP handlers and task runner.**

Use `http.ServeMux` with strict method/path matching and `http.MaxBytesReader`. Decode one `TaskRequest`, validate it, call `TaskStore.Accept`, then start the asynchronous app-server runner only for a newly accepted record. The runner transitions `accepted → starting → running`, emits bounded events, records `waiting_approval`/`blocked` or terminal `completed`/`failed`, and validates handoff identity and size before journaling it. Return `202 Accepted` with the current record and `idempotent` boolean. Status and result endpoints expose only compact metadata. Events support `after` and return NDJSON capped to the last 100 events. Cancellation validates the request tuple and invokes the session interrupt; it is idempotent for the current terminal record and `409 Conflict` for stale tuples.

- [ ] **Step 4: Implement the Worker CLI and operational README.**

Default `--listen` to `127.0.0.1:0` for phase-one safety. Require explicit `--workspace-root`; default `--state-root` to a child `codex-worker` directory under the existing `appdir` when available. Default `--codex-bin` to `codex`. Handle SIGINT/SIGTERM by stopping the HTTP server, cancelling active sessions, syncing the journal, and exiting without changing active leases to released. Document the exact curl smoke test and the later `cw=14324` registration phase without advertising a LAN endpoint in this binary.

- [ ] **Step 5: Run the Worker tests and commit.**

Run: `cd services/nvpair-codex-worker && go test ./...`

Expected: PASS, including HTTP idempotency, stale cancellation rejection, absent approval mutation route, bounded events, and graceful shutdown.

```bash
git add services/nvpair-codex-worker
git commit -s -m "feat: expose loopback Codex worker protocol"
```

### Task 5: Expose Supervisor MCP tools over stdio

**Files:**
- Create: `services/nvpair-codex-supervisor/go.mod`
- Create: `services/nvpair-codex-supervisor/main.go`
- Create: `services/nvpair-codex-supervisor/mcp.go`
- Create: `services/nvpair-codex-supervisor/client.go`
- Test: `services/nvpair-codex-supervisor/mcp_test.go`
- Create: `services/nvpair-codex-supervisor/README.md`

**Interfaces:**
- Consumes the Worker HTTP routes from Task 4 and `nvpair-shared/jsonrpc` for MCP framing.
- Produces MCP tools `workers.list`, `tasks.delegate`, `tasks.status`, `tasks.result`, and `tasks.cancel` with `--worker-url` and `--ipc`-style stdio operation.

- [ ] **Step 1: Write MCP protocol tests before implementation.**

```go
func TestToolsListContainsOnlyBoundedSupervisorSurface(t *testing.T) {
	server := newMCPServer(testWorkerClient(t))
	response := callMCP(t, server, `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	var result struct{ Tools []struct{ Name string `json:"name"` } `json:"tools"` }
	decodeResult(t, response, &result)
	got := toolNames(result.Tools)
	if diff := cmp.Diff([]string{"artifacts.get", "tasks.cancel", "tasks.delegate", "tasks.result", "tasks.status", "workers.list"}, got); diff != "" { t.Fatal(diff) }
}

func TestDelegateReturnsWorkerHandoffWithoutTranscript(t *testing.T) {
	server := newMCPServer(testWorkerClient(t))
	response := callMCP(t, server, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"tasks.delegate","arguments":{"objective":"run tests","workspace":"local","mode":"read"}}}`)
	text := responseText(t, response)
	if strings.Contains(text, "hidden reasoning") || strings.Contains(text, "stdout transcript") { t.Fatalf("unbounded output: %s", text) }
	if !strings.Contains(text, "taskId") || !strings.Contains(text, "status") { t.Fatalf("missing compact handoff: %s", text) }
}
```

- [ ] **Step 2: Run MCP tests to verify they fail.**

Run: `cd services/nvpair-codex-supervisor && go test ./...`

Expected: FAIL because the module and MCP handlers do not exist.

- [ ] **Step 3: Implement the Worker HTTP client.**

Use an `http.Client` with a 30-second control timeout and a 256 KiB request body limit. `Delegate` creates a random request/task/attempt ID, sends a `TaskRequest`, and returns the Worker response. `Status`, `Result`, and `Cancel` forward only the bounded route payloads. On connection failure return an operational error to MCP without inventing a retry or releasing a Worker lease.

- [ ] **Step 4: Implement MCP initialize, tools/list, and tools/call.**

Support `initialize`, `notifications/initialized`, `tools/list`, and `tools/call`; return JSON-RPC `-32601` for every other method. Define input schemas that require an objective and allow only `workspace: "local"`, `mode: "read"|"write"`, and `approval: "local-only"`. `tasks.delegate` maps the bounded MCP arguments into `TaskRequest`; other tools require a task ID and pass through current fencing metadata returned by status. Serialize only the compact Worker result into MCP text/structured content. Never add a generic relay or an approval tool.

- [ ] **Step 5: Implement the Supervisor CLI and README.**

Default `--worker-url` to a required explicit value in Phase 1; refuse an empty URL. Run the MCP server on stdin/stdout and send operational logs to stderr. Document a local Codex configuration example using the built Supervisor binary and the loopback Worker URL, plus a note that the Supervisor is not the PAIR inference scheduler.

- [ ] **Step 6: Run tests and commit.**

Run: `cd services/nvpair-codex-supervisor && go test ./...`

Expected: PASS, including the exact tool inventory, bounded delegation output, unknown-method handling, and no approval relay.

```bash
git add services/nvpair-codex-supervisor
git commit -s -m "feat: add Codex supervisor MCP bridge"
```

### Task 6: Integrate build/version/documentation boundaries without changing PAIR inference

**Files:**
- Modify: `services/versions.json`
- Modify: `services/build.sh`
- Modify: `services/build.bat`
- Modify: `services/readme.md`
- Modify: `docs/superpowers/specs/2026-09-04-codex-supervisor-pair-control-plane.md` only if implementation behavior differs from the approved design.
- Test: `services/nvpair-codex-worker/main_test.go`
- Test: `services/nvpair-codex-supervisor/main_test.go`

**Interfaces:**
- Consumes the two new binaries from Tasks 4–5.
- Produces reproducible `--version` output, staged binaries, module tests, and operational docs.

- [ ] **Step 1: Write version/build tests before editing scripts.**

```go
func TestVersionFlagIsDefined(t *testing.T) {
	if Version == "" { t.Fatal("version must be linkable by services/build.sh") }
}
```

Add a shell-level assertion in the existing build verification path that each key in `versions.json` has a corresponding build target and staged binary name.

- [ ] **Step 2: Run the checks to verify the new targets fail.**

Run: `node scripts/spdx-headers.mjs`; then from `services/` run `jq -e 'has("components") and (.components | has("nvpair-codex-worker") and has("nvpair-codex-supervisor"))' versions.json`.

Expected: the SPDX check passes for existing files, while the manifest assertion fails because the two new component keys have not yet been added.

- [ ] **Step 3: Add both components to the version manifest and build scripts.**

Add `nvpair-codex-worker` and `nvpair-codex-supervisor` as additive minor-version components, update build counts from 13 to 15, stamp `main.Version`, copy both binaries to `services/build/bin`, and list them in `services/readme.md`. Keep the UI broker’s supervised child list unchanged in this phase; the two new processes are explicitly launched/configured as the local vertical slice and are not silently added to Electron startup.

- [ ] **Step 4: Document the run order and security boundary.**

Document: (1) start Worker with an explicit workspace root, (2) start Supervisor pointing at the Worker loopback URL, (3) add the Supervisor as a local MCP server for Main Codex, (4) call `tasks.delegate`, and (5) poll `tasks.status`/`tasks.result`. State that `cw=14324`, mTLS, `clustertrust.Mesh`, mDNS collision quarantine, artifact transport, and broker supervision are Phase 2–5 work and are not implied by the local HTTP listener.

- [ ] **Step 5: Run build/version/doc checks and commit.**

Run: `cd services/nvpair-codex-worker && go test ./...`; `cd ../nvpair-codex-supervisor && go test ./...`; `node scripts/spdx-headers.mjs`; `git diff --check`.

Expected: PASS, with both new binaries represented consistently in the manifest, scripts, and service index.

```bash
git add services/versions.json services/build.sh services/build.bat services/readme.md services/nvpair-codex-worker services/nvpair-codex-supervisor
git commit -s -m "build: package Codex supervisor services"
```

### Task 7: Run the local end-to-end acceptance suite and review the security controls

**Files:**
- Create: `services/tests/codex_supervisor_test.go`
- Modify: `services/tests/go.mod` only if the cross-process test needs a direct module replacement.
- Modify: `docs/superpowers/specs/2026-09-04-codex-supervisor-pair-control-plane.md` only for measured Phase 1 behavior.

**Interfaces:**
- Consumes the staged Worker and Supervisor binaries from Tasks 4–6.
- Produces evidence for acceptance tests 1–6 and 11–14 that are applicable to the local vertical slice.

- [ ] **Step 1: Write the cross-process acceptance cases.**

Use these exact test names and assertions in `services/tests/codex_supervisor_test.go`:

- `TestLocalDelegationProducesBoundedHandoff`: launch the fixture Worker and Supervisor as child processes, send an MCP `tools/call` for `tasks.delegate`, decode the returned JSON, and assert `status == "completed"`, a non-empty `taskId`, and encoded handoff bytes no greater than `64 << 10`.
- `TestRestartReplaysIdempotencyWithoutSecondTurn`: submit one fixed `requestId`/`taskId`/`attemptId`, stop and restart the Worker using the same state root, submit the identical payload, and assert the fixture app-server log contains exactly one `turn/start` request.
- `TestStaleAttemptCannotRetrySameWorkspace`: make the fixture child disappear after acceptance, wait for the Worker to report `lost`, submit a retry for the same workspace, and assert `409 Conflict` plus no second fixture child until the old child is explicitly proven terminated and fenced.
- `TestApprovalIsBlockedAndCannotBeRelayed`: make the fixture emit an approval request, assert the Worker reaches `blocked` with reason `approval_required`, and assert `POST /v1/tasks/{id}/approvals/{approvalId}` returns `404 Not Found`.
- `TestCancellationIsScopedToOneTask`: start two fixture tasks in distinct workspaces, cancel one with its current lease tuple, and assert that task becomes `cancelled` while the other reaches `completed`.
- `TestUnsafeArtifactsAndWorkspacePathsAreRejected`: submit absolute paths, `..`, a symlink escape, an undeclared artifact, and an artifact over the configured byte limit; assert each request is rejected before the fixture child starts.

- [ ] **Step 2: Run the acceptance suite and record failures.**

Run: `cd services/tests && go test ./... -run CodexSupervisor -v`

Expected: failures identify integration defects; no acceptance claim is made until the real subprocesses, journal reopen, and fixture call counts are observed.

- [ ] **Step 3: Fix only defects proven by the failing acceptance tests.**

Preserve the protocol boundary: no test fix may add an approval relay, automatic retry after an unresolved child, arbitrary path serving, or inference-scheduler coupling. Re-run the focused failing test after each fix.

- [ ] **Step 4: Run the complete verification set.**

Run: `cd services/shared && go test ./...`; `cd ../nvpair-codex-worker && go test ./...`; `cd ../nvpair-codex-supervisor && go test ./...`; `cd ../tests && go test ./...`; `node scripts/spdx-headers.mjs`; `git diff --check`; `git status --short`.

Expected: all applicable tests pass; the only explicitly deferred acceptance items are PAIR remote transport/registration, C1 revocation timing, M2 authenticated collision quarantine, and artifact serving over the future mTLS endpoint.

- [ ] **Step 5: Commit the verified vertical slice.**

```bash
git add services/tests docs/superpowers/specs/2026-09-04-codex-supervisor-pair-control-plane.md
git commit -s -m "test: verify local Codex supervisor flow"
```

## Spec coverage check

- Requirements appendix items 1–8: Tasks 3–5 implement the local delegation, native app-server, bounded handoff, Main-only MCP authority, and capability-shaped request fields; PAIR reuse remains isolated for later phases.
- C1: explicitly deferred from Phase 1 and remains a remote-transport gate; the canonical spec documents the measured two-second poll behavior and five-second target.
- C2: enforced in Tasks 3–5 by omitting the approval route and converting approval requests to blocked.
- C3: enforced in Task 2 and the cross-process acceptance suite by durable workspace leases, epochs, and no network-loss retry.
- M1: not introduced in Phase 1; Task 6 does not advertise `14324` until the existing `noderec`/broker registration change is implemented as its own phase.
- M2: not introduced in Phase 1; Task 7 does not claim authenticated collision handling.
- M3: enforced in Task 3 for workspace resolution; artifact staging/serving is deferred to the capability/artifact phase.
- M4: enforced in Task 2 and Task 7 for JSONL replay and duplicate-turn prevention.
- M5: enforced in Task 2 for Worker-authoritative capacity/workspace lease behavior; no UX assumption is used as a lock.

The plan intentionally implements only the independently testable local vertical slice. Phase 2 service registration, Phase 3 pinned mTLS/revocation, Phase 4 capability/artifact transport, and Phase 5 cross-platform packaging each remain separate follow-on plans because they have independent security and integration gates.
