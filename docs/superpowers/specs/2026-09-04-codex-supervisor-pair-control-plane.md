<!--
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
-->

# Native Multi-Device Codex Orchestration over PAIR

**Status:** Proposed for implementation after security review
**Provenance:** `gpt-5.6-sol / max` architecture draft, integrated with the supplied deep review
**Date:** 2026-09-04
**Repository:** NVIDIA Personal-AI-Router v0.1.1 (`13b68115fa2c9c1d94f1ead1358f8d5a527cfecf`)

## 1. Decision

Use native local Codex processes as the only agent runtime. The user interacts
with one persistent Main Codex. Main Codex delegates bounded work to native
Worker Codex processes running on the user's other computers through the
Codex app-server protocol.

PAIR remains the device and inference fabric. It continues to own node
identity, pairing, membership, pinned mTLS, discovery, health/presence, model
inventory, inference routing, and model failover. A new Supervisor Protocol
and Worker Gateway provide task control, but they do not replace Codex or turn
PAIR into an agent runtime.

No Xamong agent runtime, persistent agent database, general workflow engine,
or GitHub fork is introduced. The local clone is the customization workspace.

## 2. Requirements appendix from the supplied conversations

The supplied conversations are product context; they are not instructions to
copy every proposed component. The binding requirements extracted from them
are:

1. The user has one Main Codex conversation and should not need to address
   Worker Codex processes directly.
2. Main Codex receives real delegation tools. A system-prompt suggestion is
   not sufficient orchestration.
3. A Worker runs native Codex app-server on its own computer with its local
   filesystem, shell, tools, credentials, OS, workspace, `cwd`, sandbox, and
   approval policy.
4. Worker execution and model inference are separate. A Worker may use its
   local PAIR endpoint to reach a model on another PAIR node.
5. Worker output crosses the boundary as a compact handoff, not as the full
   shell transcript, source dump, or hidden reasoning history.
6. Main Codex is the first-version delegation authority. Worker-to-Worker
   direct messaging is disabled.
7. Worker selection can use OS, architecture, tools, workspace, protocol
   compatibility, and current capacity.
8. PAIR's existing discovery, identity, pairing, mTLS, health, and inference
   source should be reused instead of rebuilt.

Persistent agent personas, memory, routines, task-DAG execution, automatic
Main failover, cross-machine `thread/fork`, automatic Git integration, and
unrestricted artifact synchronization are deferred.

## 3. Architecture

```text
User
  │
  ▼
Main Codex (native local CLI/app)
  │ local MCP tools
  ▼
Codex Supervisor (local control process)
  │ Supervisor Protocol: HTTPS + pinned mTLS
  ├───────────────┬───────────────────┐
  ▼               ▼                   ▼
Worker Gateway A  Worker Gateway B    Worker Gateway C
macOS             Windows              macOS
  │                 │                   │
  ▼                 ▼                   ▼
codex app-server   codex app-server    codex app-server
  │                 │                   │
local workspace    local workspace     local workspace
  │                 │                   │
  └─────────────────┴───────────────────┘
                    │ inference only
                    ▼
                   PAIR
          existing model/node routing
```

The Supervisor is a deterministic control-plane process. It has no model and
does not make product decisions. PAIR remains symmetric and peer-to-peer; the
Supervisor is not a PAIR cluster leader.

## 4. Responsibilities and source reuse

### Main Codex

Main Codex maintains the user conversation, decomposes work, creates bounded
context packages, invokes Supervisor tools, evaluates handoffs, and owns the
final answer. Main Codex does not receive a raw Worker terminal.

### Codex Supervisor

The Supervisor is a local MCP server for Main Codex. It discovers Workers,
validates task envelopes, selects an eligible Worker, tracks task state,
transports requests, reconnects to event streams, and returns compact
progress/results. It contains no LLM and does not read or modify a Worker
workspace itself.

### Worker Gateway

The Worker Gateway is a standalone Go service on each participating computer.
It authenticates Supervisors, enforces local workspace and execution policy,
starts and supervises app-server children, normalizes public events, manages
leases, and stores task state/results durably.

### Worker Codex

Worker Codex is the native `codex app-server` process. It remains authoritative
for thread/turn state, tool execution, filesystem access, shell commands,
approvals, and local execution events.

### PAIR components to reuse

- `services/shared/nodeid`: durable host identity.
- `services/shared/clustertrust`: cluster membership, pinned certificate
  material, mTLS client/server configuration, and live pin checks.
- `services/shared/noderec`: `_nvpair-node._tcp` records, service-port TXT
  parsing, IP candidate ordering, and discovery data structures.
- `services/shared/jsonrpc`: stream framing where a local adapter needs it.
- `services/nvpair-cluster-manager`: cluster membership and trust-store
  lifecycle; it remains the certificate/pin owner.
- `services/nvpair-ui-broker`: optional local supervision and existing
  `discovery:register` forwarding for the Worker service. It is not a remote
  task bus and its single-client UI channel is not reused for task traffic.
- `services/ollama-proxy`, `services/lmstudio-proxy`, and
  `services/nvpair-job-scheduler`: unchanged inference routing and failover.

The inference scheduler is model/node ranking, not OS/tool task scheduling.
Worker capability selection therefore belongs to the Supervisor.

## 5. Supervisor Protocol v1

Supervisor Protocol v1 is JSON over HTTPS on a dedicated mTLS-only Worker
endpoint. The default port is `14324`, advertised in the existing PAIR node
record as `cw=14324` (Codex Worker). It never tunnels raw app-server methods.

| Operation | Purpose |
| --- | --- |
| `GET /v1/worker` | Authenticated identity, capabilities, protocol/app-server version, capacity |
| `POST /v1/tasks` | Idempotently accept one bounded task attempt |
| `GET /v1/tasks/{id}` | Current state, owner, lease epoch, and latest event sequence |
| `GET /v1/tasks/{id}/events?after={seq}` | Reconnectable bounded NDJSON progress stream |
| `POST /v1/tasks/{id}/turns` | Bounded follow-up using the same fenced Worker thread |
| `POST /v1/tasks/{id}/cancel` | Idempotent cancellation |
| `GET /v1/tasks/{id}/result` | Validated compact handoff |
| `GET /v1/tasks/{id}/artifacts/{artifactId}` | Fetch a declared, digest-verified artifact |

There is deliberately no remote approval endpoint in v1. A Worker approval
event transitions the task to `blocked` with reason `approval_required`.
Only a local human at the Worker host may approve it. Main Codex cannot turn
an AI-generated message into a human approval through this protocol.

Every mutation carries `protocolVersion`, `requestId`, `taskId`, `attemptId`,
and `leaseEpoch`. The Worker durably records the request before acknowledging
it. Replaying a request returns the original result; a conflicting request for
an existing task is rejected.

Events have a monotonically increasing `seq`. Reconnection resumes after the
last committed sequence. Only normalized state, progress, terminal, and
artifact metadata cross the network. Raw reasoning and full app-server
transcripts do not.

The local MCP surface exposed to Main is intentionally small:

- `workers.list`
- `tasks.delegate`
- `tasks.status`
- `tasks.result`
- `tasks.cancel`
- `artifacts.get`

Workers cannot delegate. Any review or follow-up task is created by Main
through the Supervisor.

## 6. Worker lifecycle and durable fencing

The Worker Gateway performs this sequence:

1. Start with a configured PAIR cluster directory, workspace allowlist,
   Supervisor certificate allowlist, capacity, and local execution policy.
2. Load and replay its durable task journal before accepting new work.
3. Register `cw=14324` with the local PAIR broker when broker supervision is
   enabled; otherwise use the configured endpoint registry for phase one.
4. Accept a task only after mTLS authentication, ACL validation, schema
   validation, workspace resolution, and capacity/lease checks.
5. Persist the acceptance and lease record, then start one native
   `codex app-server` child for the attempt.
6. Initialize the app-server adapter, create or resume a thread, and start a
   turn with the validated `cwd`, sandbox, approval policy, and context.
7. Normalize public app-server events into bounded progress and sequence them
   durably.
8. On completion, validate the handoff, register declared artifacts, persist
   the terminal result, and close only that task's app-server child.

### Lease model

The Worker enforces at most one active attempt per `taskId` and per canonical
local workspace. A lease record contains:

```text
taskId, attemptId, leaseEpoch, workspaceKey,
supervisorPrincipal, state, childIdentity, updatedAt
```

`leaseEpoch` increases monotonically. Every follow-up, cancellation, event
acknowledgement, and result operation must match the current task/attempt/
epoch tuple. A fenced or stale tuple receives a conflict and cannot mutate
state.

Network loss never triggers automatic reassignment. A retry is allowed only
after the old app-server child is confirmed terminated and the old lease is
persistently fenced/released. If the Worker disappeared and termination
cannot be proven, the task remains `lost` and a new attempt is not started
automatically. This prevents an old and new attempt from changing the same
workspace concurrently.

Multiple Supervisors are supported at the Worker boundary even though the
product UX has one Main Codex. The Worker is authoritative for slot counts,
task idempotency, and workspace leases. The first accepted request owns a task;
different supervisors cannot use the same task ID or workspace lease without
an explicit release/fence operation.

## 7. Discovery, service registration, and mTLS

`cw=14324` is added to the existing `noderec.ServiceKey` order and registered
through the existing broker discovery path. The mDNS schema version remains
unchanged because the existing parser already supports forward-compatible
service keys. Older PAIR nodes ignore `cw`; a Supervisor requires the key for
automatic Worker discovery. Fixed-port probing without a TXT service key is
not used.

The Supervisor treats mDNS as an address hint only:

1. Parse the node record and collect its `hostUuid`, `clusterUuid`, `cw` port,
   and ranked IP candidates.
2. Refresh the live `clustertrust.Mesh` and require a current cluster
   membership and pin.
3. Dial the advertised candidates with the Supervisor's PAIR certificate and
   byte-for-byte pinned server certificate.
4. Accept Worker capability data only after the Worker verifies the client's
   current pin and local Supervisor ACL.

The TLS certificate principal is the security identity. `hostUuid` is only a
display/correlation value. Discovery entries are keyed by authenticated
certificate principal, not hostname. A hostname/correlation collision is
quarantined when an authenticated `/v1/worker` response does not agree with
the advertised identity. Quarantine clears only after two consecutive
successful probes with a unique matching principal and current pin; until
then the entry cannot be selected.

`clustertrust.Mesh.Watch` is a polling watcher, not a push/live signal. Its
current `RefreshInterval` is two seconds. The Worker therefore:

- calls `mesh.Refresh()` before every HTTP authorization check;
- revalidates the authenticated peer certificate against the current pin on
  every request, including requests on a reused HTTP connection;
- runs a two-second revocation poller for active tasks and cancels tasks whose
  Supervisor principal or certificate is no longer current.

The revocation target is `2 * clustertrust.RefreshInterval + 1 second` under
normal scheduling, and the integration test measures this bound. Pin removal
or certificate rotation is not described as instantaneous. An unclustered
Worker refuses mTLS and never falls back to plaintext.

The `nvpair-ui-broker` channel remains single-client and local. It may spawn
the Worker and forward its service registration, but Supervisor task payloads
always use the dedicated Worker endpoint.

## 8. Codex app-server adapter

A versioned adapter isolates Supervisor Protocol from app-server changes. Its
semantic operations are:

- initialize and negotiate a supported app-server version;
- start or resume a local thread;
- start a turn with validated `cwd`, sandbox, approval policy, and context;
- consume public progress, tool, approval, completion, and error events;
- interrupt a turn and close the child process.

The exact method names described in the supplied conversation are verified
against the installed Codex CLI during implementation. Unsupported versions
make a Worker `incompatible`; the adapter does not approximate missing
semantics. Cross-machine `thread/fork` is excluded from v1.

## 9. Capability-aware Worker selection

Workers publish authenticated, locally configured capabilities:

- OS and architecture;
- Worker Protocol and app-server versions;
- workspace aliases, canonical roots, and access modes;
- tool labels such as PowerShell, Xcode, Docker, CUDA, or browser;
- supported sandbox/approval modes;
- maximum concurrency and currently available slots.

Selection is deterministic:

1. Filter by current trust, protocol compatibility, workspace alias, OS/
   architecture, required tools, access mode, and local policy.
2. Honor an explicit Worker selection only if it is eligible.
3. Prefer exact workspace/tool locality.
4. Prefer available capacity and then least-recent assignment.
5. Break ties by authenticated cluster principal.

PAIR's job scheduler is never queried or modified for this decision. It
continues to rank inference destinations for the model requests made by Main
or Worker Codex.

## 10. Context, handoff, and artifacts

The Supervisor sends a bounded context package, not the full Main conversation.
The v1 package limit is 256 KiB excluding separately transferred artifacts:

```json
{
  "version": 1,
  "objective": "One measurable bounded outcome",
  "relevantDecisions": ["Only facts needed for this task"],
  "workspace": {"id": "pair", "mode": "read", "baseRevision": "optional"},
  "constraints": ["No edits", "Run Windows-native tests"],
  "requiredEvidence": ["commands", "test outcomes", "file references"],
  "limits": {"wallSeconds": 1800},
  "execution": {"sandbox": "workspace-read", "approval": "local-only"},
  "inputs": [{"artifactId": "a1", "sha256": "..."}]
}
```

The handoff limit is 64 KiB:

```json
{
  "version": 1,
  "taskId": "task-2081",
  "attemptId": "attempt-1",
  "status": "completed",
  "summary": "Windows build failed because of two Unix path assumptions.",
  "findings": [{"severity": "blocker", "location": "services/foo/path.go", "detail": "Unix path assumption"}],
  "changes": [],
  "verification": [{"command": "go test ./...", "outcome": "passed", "artifactId": "log1"}],
  "artifacts": [{"id": "log1", "sha256": "...", "bytes": 1234}],
  "recommendedNext": "Add a path abstraction and rerun the build."
}
```

Artifacts are copied into a per-task staging directory and addressed by an
opaque ID and SHA-256 digest. The endpoint never streams an arbitrary path
directly. The safe path implementation must:

- reject absolute paths, `..`, empty components, and paths outside the
  canonical per-task root;
- reject symlink/reparse-point components using `Lstat` plus canonical
  containment checks;
- use platform-specific no-follow opening where available;
- copy to a Worker-created, digest-addressed staging file before serving;
- enforce maximum bytes and task-local allowlists.

Artifact reads are explicit and bounded. Full source trees, undeclared files,
credentials, and large inline outputs are not returned to Main by default.

## 11. Security-critical decisions and controls

### C1: revocation latency

The two-second `Mesh.Watch` polling interval is documented as a bound, not as
live push. Request authorization refreshes the mesh and checks the current
certificate pin. Active-task revalidation cancels affected tasks within the
defined five-second target under normal scheduling. Tests measure pin removal,
new-request rejection, active-task cancellation, and certificate rotation.

### C2: approval authenticity

Remote approval relay is not part of v1. The protocol has no approval mutation
endpoint. An approval request is terminally `blocked` for remote control and
requires a local human at the Worker host. A future human approval channel
must introduce a separately authenticated, one-time user capability before it
can be added.

### C3: stale-attempt fencing

Durable task records, per-workspace leases, monotonic `leaseEpoch`, child
identity, and stale-tuple rejection prevent a lost attempt from racing a
retry. Network loss alone never releases a lease or starts a retry. An
operator must prove child termination before fencing/releasing an old attempt.

### M4: durable idempotency and restart

The Worker uses an append-only JSONL journal under its configured state root.
Each mutation is written and `fsync`ed before its response is acknowledged.
Startup replays the journal to rebuild task state, idempotency records,
leases, and event sequences. Terminal records and artifacts are retained for
seven days by default; active/lost records remain until explicitly resolved.
Compaction is allowed only after no active lease depends on the compacted
records.

After a Worker restart, a running child is adopted only if its persisted
process identity can be proven. Otherwise the attempt becomes `lost` and is
not replayed. No side-effecting turn is silently restarted.

### M5: multiple Supervisors

The single-Main UX is not a security assumption. Worker ACLs may authorize
multiple Supervisor certificate principals, but Worker-side capacity,
idempotency, task ownership, and workspace leases are authoritative. A second
Supervisor receives the existing task for a duplicate request ID or a
conflict/busy response for an occupied task/workspace. There is no preemption.

### M2: identity collision

Authenticated certificate principal is the unique Worker key. `hostUuid` and
hostname collisions are quarantined, never merged. The state is cleared only
after two consecutive authenticated probes agree on principal, host UUID,
cluster UUID, and current pin.

## 12. Failure and cancellation

Task states are `accepted`, `starting`, `running`, `waiting_approval`,
`cancelling`, `completed`, `blocked`, `failed`, `cancelled`, and `lost`.

- A Worker unavailable before acceptance may be replaced by another eligible
  Worker.
- A Worker lost after acceptance becomes `lost`; it is not automatically
  retried because side effects may have occurred.
- Reconnection reconciles by task ID, attempt ID, and lease epoch; it never
  starts a duplicate turn.
- Cancellation interrupts the specific app-server child, waits for the grace
  period, then terminates only that child if necessary.
- Stale events and terminal mutations from a fenced attempt are ignored.
- Partial declared artifacts remain available for the retention period.
- PAIR model failover remains independent of Worker task retry.

## 13. Exact implementation boundary

### Existing source reused

PAIR pairing, cluster-manager, cluster directory, certificates, pins,
`nodeid`, `clustertrust`, `noderec`, candidate IP ordering, pooled mTLS client
patterns, model inventory, inference proxies, scheduler, and existing broker
process supervision remain the foundation.

### New or narrowly changed source

- New `services/nvpair-codex-worker` Go service for the Worker Gateway.
- New `services/nvpair-codex-supervisor` local MCP/control service.
- New shared protocol/types package for task envelopes, leases, events,
  handoffs, and artifact manifests.
- `services/shared/clustertrust`: expose the minimal current-pin check needed
  to revalidate a saved peer certificate during active-task revocation.
- `services/shared/noderec`: add `ServiceCodexWorker = "cw"` and deterministic
  TXT emission/parse tests.
- `services/nvpair-ui-broker`: optional Worker child path and existing
  discovery registration forwarding only; no remote task methods or payload
  body changes.
- Installer/README/firewall documentation for the optional `cw` service.

There are no changes to PAIR inference scheduler behavior, schedulerwire,
model request bodies, inference headers, existing broker namespaces, desktop
preload APIs, or PAIR workload payloads. PAIR's UI broker is never a remote
task transport.

## 14. Phased implementation

1. **Local vertical slice:** Worker Gateway over loopback, app-server adapter,
   workspace policy, journal/leases, bounded context/handoff, cancellation,
   and local MCP tools.
2. **PAIR service registration:** `cw` node-record advertisement, optional
   broker supervision, port/firewall handling, and discovery tests.
3. **Secure remote transport:** pinned mTLS, per-request pin revalidation,
   active-task revocation polling, Supervisor ACL, multi-address dialing, and
   hostname collision quarantine.
4. **Capability scheduling and artifacts:** deterministic Worker selection,
   digest-addressed staging, artifact transfer, concurrent Workers, and
   reconnectable event streams.
5. **Cross-platform hardening:** macOS/Windows/Linux service packaging,
   app-server version checks, restart reconciliation, audit tests, and local
   approval UX documentation.

## 15. Acceptance tests

1. Main Codex invokes a real local MCP delegation tool and receives a bounded
   Worker handoff.
2. Worker app-server starts with the resolved local `cwd`, restrictive
   effective sandbox, and configured local approval policy.
3. A duplicate mutation request is idempotent across Worker restart because
   its journal record was persisted before acknowledgement.
4. A lost attempt cannot be retried while its workspace lease or child
   identity is unresolved; stale epoch mutations are rejected.
5. Cancellation affects only the targeted task and yields an idempotent
   terminal state.
6. Approval events produce `blocked/approval_required`; no remote protocol
   call can continue the turn as an approval.
7. Removing a pin or rotating a peer certificate causes new requests to be
   rejected after refresh and active tasks to be cancelled within five
   seconds in the controlled integration test.
8. Unpaired, wrong-certificate, plaintext, de-pinned, and paired-but-
   unauthorized callers are rejected.
9. `cw=14324` is discovered through the existing node TXT record; no port
   scan is needed, and port/address failover remains deterministic.
10. Conflicting host UUIDs are kept as separate authenticated principals and
    remain quarantined until two consistent probes clear the state.
11. Symlink/reparse traversal, `..`, absolute paths, undeclared artifacts,
    and over-limit artifact reads are rejected.
12. Two authorized Supervisors cannot exceed Worker capacity or acquire the
    same workspace lease concurrently.
13. A Worker/app-server restart never silently replays a side-effecting turn.
14. Main receives no full conversation, hidden reasoning, undeclared file, or
    credential; logs contain no prompt, response, or source body.
15. Existing PAIR broker methods, desktop bridge, scheduler messages, model
    request bodies, inference headers, and unrelated mDNS records remain
    contract-compatible.

## 16. Remaining non-critical risks

- Installed Codex app-server versions may differ in initialization, resume,
  cancellation, structured output, or event names; the adapter must fail
  closed on unsupported versions.
- Windows service accounts may not share the interactive user's Codex login,
  desktop session, or tool environment.
- Workspace revisions and dirty state can differ across computers; automatic
  patch application and cross-machine Git synchronization are out of scope.
- An abrupt host power loss can leave an external side effect unknown even
  when the journal correctly prevents automatic replay; retry remains an
  explicit operator decision.
