# Codex Desktop Integration Design

**Status:** Draft for implementation review  
**Date:** 2026-09-05  
**Parent design:** [Codex Supervisor Pair Control Plane](2026-09-04-codex-supervisor-pair-control-plane.md)

## 1. Decision summary

The existing repository contains a usable backend control-plane foundation, but
the PAIR desktop application does not package, launch, configure, or expose it.
This design extends the product boundary so that a fresh PAIR installation can
make Codex pair execution operational after an explicit one-time setup.

The recommended ownership model is:

```text
PAIR Electron
└── nvpair-ui-broker
    └── configured local Codex Worker (optional, broker-owned)
        └── native codex app-server per accepted task

Main Codex
└── local stdio MCP process manager
    └── bundled nvpair-codex-supervisor
        ├── local Worker over authenticated loopback HTTP
        └── remote Workers over pinned mTLS
```

Electron continues to launch only the PAIR broker. The broker may launch one
configured local Worker when the user explicitly enables local execution. Main
Codex, not Electron or the broker, owns the Supervisor's MCP stdio session.
PAIR Desktop bundles the Supervisor and manages its registration and health, but
does not attach its own stdio to that process.

Remote Workers remain independently deployable. Installing or uninstalling the
desktop application must not kill, replace, or delete an independently managed
Worker on the same computer.

## 2. Problem and current gap

The current services commit is not a desktop-integrated feature:

- `desktop/src` has no Codex references.
- `MODULAR_RUNTIME_BINARIES` and `MODULAR_BUNDLED_BINARIES` omit both Codex
  components; the desktop package contains 13 binaries while the services build
  contains 15.
- Electron starts `nvpair-ui-broker` only.
- The broker's `--codex-worker-port` option only registers a `cw` service entry;
  it does not launch, probe, supervise, or stop a Worker.
- The Supervisor speaks MCP over its own stdin/stdout. Starting it as an
  Electron or broker child would attach MCP to the wrong owner and would not
  make its tools available to Main Codex.
- Existing service integration tests launch Worker and Supervisor directly and
  use a fake app-server. They do not launch Electron, a packaged app, or a real
  Main Codex MCP session.
- The current discovery-file path performs one discovery pass while the
  quarantine state requires two matching authenticated observations. A normal
  first snapshot can therefore produce zero eligible Workers and exit.

The parent control-plane specification's statement that desktop/preload
contracts remain unchanged is a valid boundary for the standalone backend, but
it cannot also be used as the completion criterion for the desktop product.
This design explicitly adds the desktop integration scope.

## 3. Goals

1. Ship both Codex components in the PAIR desktop artifact for supported
   platforms.
2. Let a user configure an optional local Worker from PAIR Desktop without a
   terminal or hand-written command line.
3. Let the user review and apply a safe Main Codex MCP registration using the
   packaged Supervisor path.
4. Make Worker readiness truthful: executable, workspace, account, policy,
   authentication, listener, and app-server compatibility are checked before
   the UI advertises the capability as ready.
5. Keep task payloads on the dedicated Supervisor-to-Worker protocol. The PAIR
   broker carries lifecycle and bounded status metadata only; it is not a task
   bus.
6. Provide visible Worker/task health and actionable recovery states.
7. Prove the complete path with packaged Electron tests and a native smoke test
   on each claimed platform.

## 4. Non-goals

- An embedded chat or replacement for Main Codex inside PAIR.
- Worker-to-Worker delegation or general workflow/DAG execution.
- Remote approval relay or automatic privilege escalation.
- Unattended write access by default. v1 Desktop defaults to read-only task
  execution unless the user explicitly grants a local write policy.
- Automatic Git synchronization, patch application, or cross-machine thread
  cloning.
- Changes to PAIR's inference scheduler, inference request headers, or model
  failover semantics.
- An always-on LAN Supervisor daemon.
- Automatic reassignment after uncertain task acceptance.

## 5. Ownership and trust boundaries

| Component | Owns | Must not own |
|---|---|---|
| PAIR Desktop | Installation, setup, consent, MCP registration management, local configuration, health/task projection, recovery guidance | Task prompts, raw app-server transcripts, remote execution authorization decisions |
| Electron | Typed IPC, configuration persistence, broker launch, registration manager, renderer-facing state | Direct Worker task execution or Supervisor MCP stdio |
| PAIR broker | Existing PAIR workers plus optional local Worker lifecycle, readiness, shutdown, and private discovery/status snapshot | Remote task payloads, Main Codex conversation, approval decisions |
| Main Codex | Conversation, decomposition, bounded context, delegation decisions, handoff evaluation, final answer | Worker-local policy, remote credentials, unrestricted file access |
| Codex Supervisor | MCP tools, authenticated discovery, deterministic Worker selection, task correlation, transport, result projection | LLM/model decisions, workspace access, local desktop UI |
| Worker Gateway | Authentication, local policy ceiling, workspace admission, leases/fencing, app-server lifecycle, artifacts, durable journal | Delegation to another Worker, weakening local policy |
| Worker Codex | Native task execution with the Worker's local user, tools, workspace, and Codex credentials | Main conversation or Supervisor authority |
| PAIR cluster trust | Pairing, certificates, pins, membership, node identity, discovery hints | Authorization for arbitrary remote execution without the Worker ACL |

Pairing, execution authorization, and task approval are separate decisions.
Being paired does not grant a Main identity permission to execute code or read a
workspace.

## 6. Process and protocol design

### 6.1 Electron and broker

Electron keeps the existing single broker process model. It passes the resolved
Codex Worker binary path and a protected configuration reference to the broker.
The broker starts a local Worker only when the configured Worker profile is
enabled and valid.

The Worker uses a dedicated control channel for broker lifecycle, separate from
task HTTP traffic:

```text
broker -> Worker control stdin:  start/stop/status messages
Worker -> broker control stdout: ready/status/stopped messages
broker <-> Worker task HTTP:    loopback bearer or configured mTLS
```

The managed mode must handle parent EOF and explicit shutdown. Standalone
service mode must continue to work without a parent pipe. Control messages must
contain no task objective, context, prompt, response, artifact body, or
credential.

Worker readiness is a structured record:

```json
{
  "generation": "uuid",
  "transport": "loopback-bearer",
  "endpoint": "http://127.0.0.1:random-port",
  "protocolVersion": 1,
  "appServerVersion": "codex-app-server-v1",
  "workspace": "configured alias",
  "codexExecutable": "/absolute/path/codex",
  "codexExecutableVersion": "0.153.2",
  "account": "os-user",
  "state": "ready"
}
```

`ready` is emitted only after the endpoint binds, the configured workspace is
validated, the Codex executable is discoverable and compatible, the effective
policy is valid, and the configured account can initialize the app-server. A
live TCP listener alone is `starting` or `unavailable`, never `ready`.

The broker registers `cw` only for a ready, explicitly remote-enabled Worker
with an authenticated mTLS listener and an explicit Supervisor ACL. A local
loopback Worker is not advertised on the LAN.

### 6.2 Main Codex and Supervisor

The Supervisor remains a command-based local MCP server. PAIR Desktop manages a
named registration such as `pair-codex-supervisor` using an absolute packaged
path and explicit arguments. Main Codex launches that command when the user
conversation invokes the tools.

The registration manager runs in Electron's main process and must:

1. Read the existing Main Codex MCP configuration without discarding unrelated
   entries.
2. Show the exact command, executable, arguments, and scope to the user.
3. Create a backup and atomically update the configuration.
4. Preserve an unrelated existing registration under a different name and
   refuse ambiguous replacement without user confirmation.
5. Verify the resulting registration and report `registered`, `waiting_for_main`,
   `connected`, or `failed` truthfully. A successful file write is not a live
   connection.
6. Remove only the PAIR-managed entry when the user disables the integration.

The Supervisor must not expose a TCP listener. Desktop status access uses a
private, same-user Unix-domain socket or Windows named pipe opened by the
Supervisor in addition to MCP stdio. The management protocol is narrow:

- `status`: Supervisor health and connection state;
- `workers.list`: authenticated capability summaries;
- `tasks.list`: task IDs, attempts, Worker identity, state, timestamps, and
  latest sequence;
- `tasks.cancel`: a validated task/attempt/epoch cancellation request.

The management channel never carries task context, prompts, responses, raw
transcripts, or artifact contents. Worker journals remain authoritative. The
Supervisor persists its destination and task-owner index so Desktop can restore
history after restart without authorizing replay.

### 6.3 Discovery

The broker publishes a private, atomically replaced runtime descriptor containing
the authenticated PAIR directory snapshot required by Supervisor discovery:

```json
{
  "schemaVersion": 1,
  "generation": 42,
  "writtenAt": "2026-09-05T00:00:00Z",
  "expiresAt": "2026-09-05T00:00:10Z",
  "nodes": [
    {
      "hostUuid": "...",
      "clusterUuid": "...",
      "trusted": true,
      "ip": "192.0.2.10",
      "ips": ["192.0.2.10"],
      "services": {"cw": {"port": 14324}}
    }
  ]
}
```

This is an address hint, not authorization. Supervisor still refreshes Mesh,
presents its certificate, pins the Worker certificate, verifies the Worker ACL,
and requires two consecutive matching authenticated probes before selecting a
new identity.

Supervisor remains alive with an empty pool when no Worker is ready. It reloads
the descriptor on generation changes and periodically retries candidates. A
single initial snapshot cannot permanently make all Workers unavailable.

### 6.4 Worker execution isolation

When the same user runs Main Codex and Worker Codex, the Worker child receives an
explicit isolated Codex configuration that does not inherit the Supervisor MCP
registration. The Worker must not be able to delegate through Main's tools.

The Worker also needs these hardening gates before Desktop readiness is trusted:

- cancellation intent is armed before a task goroutine can start;
- terminal state and lease release require confirmed process-tree termination;
- app-server turn response publication has a barrier/buffer before accepting
  fast notifications;
- workspace containment uses stable no-follow identity checks across launch,
  including Windows reparse points;
- Windows journal exclusion is an OS-released file/handle lock, not a stale
  marker file;
- mutation idempotency includes operation and canonical effect payload, not only
  a fencing tuple;
- journal write/sync uncertainty poisons admission until controlled recovery;
- task reads/cancel/follow-up are bound to the authenticated owner or an
  explicitly documented shared-administration policy.

## 7. Desktop configuration and UI

Add a `Codex` section beside existing Cluster and Service settings. The first
screen separates two independent choices:

1. **Connect Main Codex** — manage the local MCP registration.
2. **Allow this computer to run tasks** — configure and enable the local Worker.

Worker configuration includes:

- native Codex executable selection and compatibility check;
- exactly one configured workspace alias/root per v1 Worker profile;
- local OS account and Codex login status;
- read-only/read-write execution policy, with read-only as the default;
- local concurrency limit;
- execution grant for specific paired Main identities;
- local-only versus remotely enabled transport;
- state/artifact retention and storage location.

The UI states are explicit:

`disabled`, `setup_required`, `starting`, `ready`, `busy`, `unauthorized`,
`incompatible`, `failed`, and `stopped`.

Add a compact Codex Tasks view showing task ID, Worker, state, attempt, start/
finish time, and artifact metadata. It does not show prompts, full responses,
hidden reasoning, credentials, or raw app-server logs.

Proposed typed boundaries:

```text
window.pairApi.codex.getState()
window.pairApi.codex.configureWorker(input)
window.pairApi.codex.setWorkerEnabled(enabled)
window.pairApi.codex.getMcpRegistration()
window.pairApi.codex.applyMcpRegistration()
window.pairApi.codex.removeMcpRegistration()
window.pairApi.codex.listTasks()
window.pairApi.codex.cancelTask(taskId, attemptId, leaseEpoch)
window.pairApi.codex.onStateChanged(callback)
```

All renderer input is schema-validated in the main process. No generic command
execution API is exposed. Tokens, private keys, task bodies, and filesystem
contents never enter renderer state or generic subprocess logging.

## 8. Packaging and platform lifecycle

The desktop inventory becomes 15 shipped components:

- add `nvpair-codex-worker` to `MODULAR_RUNTIME_BINARIES` as a broker-owned
  optional runtime;
- add `nvpair-codex-supervisor` to `MODULAR_BUNDLED_BINARIES` as a packaged
  command, not an Electron-owned runtime;
- make unexplained service omissions and missing versions fatal in the desktop
  build;
- include both binaries in `cli-bin/manifest.json` with version, size, hash,
  target architecture, and final package verification;
- keep the standalone services installer for remote Worker hosts;
- do not add an inbound firewall rule for local-only mode;
- add a scoped Worker firewall rule only when the user enables remote mode.

State belongs under the existing per-user PAIR data root:

```text
codex/config.json
codex-worker/desktop/       journal and staged artifacts
codex/runtime/              socket/pipe, token, readiness, generation files
codex/mcp-registration.json managed registration metadata
```

The desktop-managed Worker and an independently deployed Worker must use
different state roots and lifecycle identities. Installer cleanup must target
owned process IDs/jobs/services, not global image-name kills.

| Event | Desktop-managed behavior |
|---|---|
| Login | Start PAIR and restore enabled Worker only after configuration validation. |
| Close window | Keep current tray behavior; Worker remains available. |
| Explicit quit | Stop admission, withdraw advertisement, terminate/reap local task trees, flush state, then stop broker. |
| Broker crash | Worker containment and journal recovery prevent duplicate execution; no automatic side-effect replay. |
| Upgrade | Quiesce owned processes, replace files, preserve state/registration, verify hashes, restart and reconcile. |
| Uninstall | Stop only owned processes and remove only owned registration/firewall entries; preserve data by default. |
| Remote Worker install | Independent service lifecycle; desktop upgrades never stop or delete it. |

Platform-specific requirements:

- Windows: Job Objects for task descendants, user-scoped state ACLs, actual
  executable/service stop coordination, junction/reparse checks, and x64/arm64
  packaged execution tests.
- macOS: absolute executable paths, user-session launch context, signatures,
  permissions, LaunchAgent behavior, and no dependence on privileged helper for
  task execution.
- Linux: user-service or explicitly configured system-service mode, cgroup/
  process-group cleanup, environment/path validation, restart limits, and
  package pre-removal coordination.

## 9. Implementation phases

### Phase 0 — Scope and backend hardening

Correct the parent specification's completion status and add this design as the
desktop integration scope. Fix the Worker lifecycle, cancellation, app-server
notification, journal locking, workspace identity, idempotency, owner binding,
and discovery startup issues listed in §6 before wiring the UI.

### Phase 1 — Desktop packaging and broker-managed Worker

Add the 15-component desktop inventory, configuration plumbing, Worker managed
control channel, readiness schema, actual endpoint handoff, process containment,
broker startup/restart/stop behavior, and platform-specific package checks.

### Phase 2 — Settings, state, and consent

Add the typed IPC channels, Codex settings, local execution grant, workspace and
Codex prerequisite validation, protected runtime files, and truthful state
projection. Add unit and contract tests for missing prerequisites and renderer
input validation.

### Phase 3 — Main Codex MCP registration and observation

Implement atomic managed registration, verification/removal, private Supervisor
management socket/pipe, persistent task index, task history, and cancellation.
Keep MCP stdio owned by Main Codex.

### Phase 4 — Discovery and multi-host operation

Publish broker runtime descriptors, refresh Supervisor candidates, fix quarantine
bootstrap, expose authenticated Worker readiness, and prove local plus remote
Worker selection without port scanning or duplicate workspace owners.

### Phase 5 — Packaged product acceptance

Activate Desktop E2E, build clean installer artifacts, execute native smoke
tests, test upgrade/uninstall and failure injection on supported platforms, and
only then mark the parent design fully implemented.

## 10. Acceptance criteria

The feature is not complete until all of the following are demonstrated on the
claimed platform matrix:

1. A clean desktop artifact contains both Codex binaries with correct target
   format, architecture, version, hash, and executable permissions.
2. Launching PAIR with Codex disabled starts no Worker listener and does not
   alter Main Codex configuration.
3. Enabling a valid local Worker starts exactly one broker-owned Worker and
   reports `ready` only after Codex executable/login/workspace checks pass.
4. Invalid workspace, missing executable, missing login, incompatible app-server,
   and occupied-port conditions produce actionable states without preventing
   normal PAIR startup.
5. Main Codex can apply the managed MCP registration, start the packaged
   Supervisor over stdio, call `workers.list`, and delegate a bounded read-only
   task to a local or remote Worker.
6. A real native app-server child runs with the configured account, workspace,
   and effective restrictive policy; exactly one side-effecting turn occurs.
7. The UI restores task identity and state after window close, broker restart,
   Supervisor restart, and application upgrade without duplicate rows or work.
8. A paired but unauthorized Main cannot delegate; removing a pin rejects new
   requests and cancels affected active work within the controlled five-second
   bound after confirmed process-tree cleanup.
9. Worker discovery changes are reflected without restarting Main Codex or
   editing a manual snapshot; an empty pool remains a usable waiting state.
10. Approval-required work becomes an honest terminal blocked state with local
    recovery instructions and no remote approval action.
11. Cancellation, crash, shutdown, and restart preserve fencing and never
    silently replay an uncertain side-effecting task.
12. Artifacts are declared, digest-verified, size-limited, path-contained, and
    transferred only through the authenticated Worker protocol.
13. Desktop and Worker logs, IPC diagnostics, and management snapshots contain
    no prompt, response, credential, hidden reasoning, or artifact body.
14. Desktop unit, contract, process, packaged-app, and native smoke tests run
    non-empty named suites; empty test selections and `--passWithNoTests` do not
    satisfy the release gate.
15. Removing PAIR Desktop does not terminate or delete an independently managed
    remote Worker installation.

## 11. Design review checklist

- [x] Six independent reviews completed against the current fork commit.
- [x] Desktop absence of Codex references and 13-versus-15 inventory gap
      independently confirmed.
- [x] Process ownership conflict between Electron, broker, Main Codex, and
      Supervisor resolved by keeping Main-owned MCP stdio.
- [x] Discovery bootstrap, readiness, approval, and packaged E2E gaps captured.
- [x] Windows/macOS/Linux lifecycle and independent remote Worker ownership
      included.
- [ ] User review of this written design.
- [ ] Implementation plan and code changes.

