# Codex Desktop Integration Design

**Status:** Implementation in progress; local native delegation, live discovery, and macOS arm64 packaged Electron startup are wired and verified; Main Codex and platform release gates remain
**Date:** 2026-09-05  
**Parent design:** [Codex Supervisor Pair Control Plane](2026-09-04-codex-supervisor-pair-control-plane.md)

**Last review:** Astra max adversarial review of the desktop integration design.
The top-level process ownership split remains viable, but implementation is
intentionally blocked until the contracts and security gates recorded below are
closed.

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

### 2.1 Astra max review disposition

The review result is **blocked for implementation planning**, not rejected at
the architecture level. The following distinction is important:

- **Retained decision:** Electron owns the broker and Desktop state; Main Codex
  owns every Supervisor MCP stdio session; Workers remain the execution and
  policy boundary.
- **P0 contract gaps:** local Worker bootstrap and authenticated endpoint
  handoff; supported Main Codex client/config/reload behavior; Supervisor
  instance multiplicity and private management; broker-to-Worker lifecycle
  protocol.
- **P0 security gaps:** isolated child Codex configuration; renderer origin,
  frame, and schema enforcement; task-owner binding; pre-dispatch durable
  intent and uncertain-ACK recovery; process-tree termination proof; stable
  workspace identity; journal poisoning and cross-Worker lease fencing.
- **P1 operational gaps:** dynamic discovery and revocation semantics;
  local-versus-remote transport; metadata DTOs and freshness; scoped
  installer/update/uninstall behavior; non-empty packaged Main/Codex smoke
  tests.

The existing repository does not yet provide these properties merely because
the binaries build or the backend unit tests pass. The implementation plan
must therefore start with contract and adversarial tests, then wire the
desktop surface only after those gates are green.

### 2.2 Implementation checkpoint

The contract implementation now includes packaged Worker/Supervisor inventory,
broker-owned managed Worker lifecycle, protected runtime descriptors, native
app-server readiness probing, policy-ceiling enforcement, durable Supervisor
dispatch intent, per-state-root instance locking, Unix management sockets and
Windows named-pipe management, typed Desktop IPC/UI, metadata-only task
projection, live pinned remote discovery with revocation removal, connected vs
waiting Main registration state, and scoped Codex installer behavior. A native
local Worker→Supervisor MCP smoke test now covers a real installed Codex CLI
through a completed read-only handoff. A macOS arm64 Electron package was also
built and launched from its packaged app directory: the package contained both
Codex binaries, the packaged broker started, and the broker resolved the
packaged Worker path. Separately, the installed Main Codex CLI launched that
packaged Supervisor and completed `workers.list` against a managed native
Worker. The remaining release gates are the combined Electron-owned enabled
Worker plus Main-Codex session, measured platform-matrix revocation and
cleanup, and upgrade/uninstall oracles; those are not claimed by unit,
cross-compilation, or a raw MCP-client smoke alone.

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
Supervisor -> Worker task HTTP: loopback or configured mTLS
broker <-> Worker control:       lifecycle and bounded status only
```

The managed mode must handle parent EOF and explicit shutdown. Standalone
service mode must continue to work without a parent pipe. The control protocol
is versioned and has explicit schemas for `start`, `status`, `drain`,
`shutdown`, `configRevision`, and `stopped`; it contains no task objective,
context, prompt, response, artifact body, or credential. Configuration changes
are revisioned: the broker drains the old Worker, withdraws its readiness, and
starts a new boot epoch rather than mutating a live execution policy.

Worker readiness is a structured record:

```json
{
  "generation": "uuid",
  "transport": "pinned-local-tls",
  "endpoint": "https://127.0.0.1:random-port",
  "protocolVersion": 1,
  "appServerVersion": "codex-app-server-v1",
  "workspace": "configured alias",
  "codexExecutable": "/absolute/path/codex",
  "codexExecutableVersion": "0.153.2",
  "account": "os-user",
  "installationId": "...",
  "workerInstanceId": "...",
  "bootEpoch": 7,
  "policyRevision": 12,
  "state": "ready"
}
```

`ready` is emitted only after the endpoint binds, the configured workspace is
validated, the Codex executable is discoverable and compatible, the effective
policy is valid, and the configured account can initialize the app-server. A
live TCP listener alone is `starting` or `unavailable`, never `ready`.

The broker must publish this record through a protected, versioned runtime
descriptor before the Supervisor can connect. The descriptor includes the
installation and Worker instance identity, boot epoch, endpoint, transport,
credential reference and generation, policy revision, and expiry. It contains
no bearer token or private key. Endpoint rotation invalidates the previous
generation and stale descriptors are rejected. Production local transport is
pinned local TLS (or an equivalent authenticated local channel); a bare
loopback bearer is a development-only fallback because it authenticates the
caller but not the server.

If one Worker serves both local and remote Supervisors, the implementation must
use one authenticated transport that covers both paths or two explicitly
isolated listeners and policy surfaces. A process-wide exclusive
"bearer-or-mTLS" switch is not sufficient for simultaneous local and remote
operation.

The broker registers `cw` only for a ready, explicitly remote-enabled Worker
with an authenticated mTLS listener and an explicit Supervisor ACL. A local
loopback Worker is not advertised on the LAN.

### 6.2 Main Codex and Supervisor

The Supervisor remains a command-based local MCP server. PAIR Desktop manages a
named registration such as `pair-codex-supervisor` using an absolute packaged
path and explicit arguments. Main Codex launches that command when the user
conversation invokes the tools.

The v1 support matrix must name the exact Main Codex client, executable
versions, operating-system user/session, and configuration scope. The initial
contract is a native Main Codex process running as the same user and using the
effective local `CODEX_HOME`; WSL, remote Main sessions, and alternate config
roots are unsupported until they have a separate transport and ownership
contract. The registration manager must resolve the effective config path and
precedence rather than assuming a single file.

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

Registration and activation are separate states. The manager records the
config fingerprint and managed entry version, detects concurrent edits, and
states whether the currently running Main loaded the entry. The supported
activation path is an explicit Main restart/reload operation; a file write must
never be reported as tool availability. A registration test is only valid when
the real supported Main client loads the entry and invokes `workers.list`.

Each Main MCP session owns one Supervisor instance. The Supervisor must not
expose a public TCP listener. Desktop status access uses a per-instance,
private, same-user Unix-domain socket or Windows named pipe opened by the
Supervisor in addition to MCP stdio. Each instance has a unique instance ID,
endpoint, registry record, and task-index namespace. The management protocol
is narrow:

- `status`: Supervisor health and connection state;
- `workers.list`: authenticated capability summaries;
- `tasks.list`: task IDs, attempts, Worker identity, state, timestamps, and
  latest sequence;
- `tasks.cancel`: a validated task/attempt/epoch cancellation request.

The endpoint is protected by 0700 Unix permissions or an explicit Windows
same-user ACL plus peer identity verification; a generic named-pipe/listener
helper is not sufficient by itself. The Supervisor registry and task index use
an interprocess lock and recover stale endpoints. The management channel never
carries task context, prompts, responses, raw transcripts, or artifact
contents. Worker journals remain authoritative. The Supervisor persists its
destination and task-owner index so Desktop can restore history after restart
without authorizing replay. A task index persists the complete pre-dispatch
intent and selected destination before sending the first request, then records
uncertain acknowledgement and recovery state instead of silently creating a
new task.

### 6.3 Discovery

Local managed Worker publication and remote discovery hints are separate
records. The broker publishes a private, atomically replaced local runtime
descriptor containing the authenticated endpoint needed by the Supervisor:

```json
{
  "schemaVersion": 1,
  "installationId": "...",
  "workerInstanceId": "...",
  "bootEpoch": 7,
  "generation": 42,
  "writtenAt": "2026-09-05T00:00:00Z",
  "expiresAt": "2026-09-05T00:00:10Z",
  "localWorker": {
    "endpoint": "https://127.0.0.1:random-port",
    "transport": "pinned-local-tls",
    "credentialRef": "runtime/worker-credential",
    "credentialGeneration": 3,
    "policyRevision": 12
  },
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

The descriptor is an address hint, not authorization. Supervisor still
authenticates the local Worker, refreshes Mesh for remote candidates, presents
its certificate, pins the Worker certificate, verifies the Worker ACL, and
requires two consecutive matching authenticated probes before selecting a new
remote identity. Independent Worker installations must advertise an explicit
supported `cw` endpoint; discovery must not infer port 14324 by scanning.

The pool has explicit heartbeat/expiry, probe-round, pin-rotation, failed-probe
reset, duplicate-identity, and replacement rules. A `cw` capability is bound
to a Worker installation and instance epoch, not merely a host UUID. v1 either
allows one Worker installation per node or defines a deterministic selection
and authorization rule; it must not silently merge multiple Workers behind
one node identity.

Supervisor remains alive with an empty pool when no Worker is ready. It reloads
the descriptor on generation changes and periodically retries candidates. A
single initial snapshot cannot permanently make all Workers unavailable.

### 6.4 Worker execution isolation

When the same user runs Main Codex and Worker Codex, the Worker child receives an
explicit isolated Codex configuration that does not inherit the Supervisor MCP
registration. The Worker must not be able to delegate through Main's tools.

The isolation contract names the child `CODEX_HOME`, config files, plugins,
hooks, credentials/auth source, environment allowlist, history location, MCP
allowlist, and filesystem roots. Excluding one Supervisor entry is not enough:
layered configuration and inherited plugins/hooks must also be excluded or
explicitly allowed. The Worker policy is a hard ceiling enforced on every task
creation and follow-up; a Desktop read-only setting cannot be widened by a
request, remote identity, or child configuration.

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
- task state separates `taskState`, `cleanupState`, and `leaseHeld`; a `lost`
  task with unconfirmed cleanup is not eligible for reassignment;
- local recovery can inspect and fence an orphaned attempt without replaying its
  side effects;
- workspace admission uses a stable filesystem identity and an exclusive
  cross-process lease, not only a path string or process-local mutex.

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

The main-process IPC boundary also enforces an exact allowed renderer origin,
the top-level frame (not only `webContents`), and a non-empty runtime schema
before any privileged Codex operation. The allowlist must fail closed when the
renderer URL is unset; `file://` or an arbitrary recognized window URL is not a
substitute for an exact origin. Task cancellation uses an opaque capability or
validated task/attempt/epoch reference and an idempotency key rather than a
renderer-supplied process identifier.

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
- add a scoped Worker firewall rule only when the user enables remote mode;
- use versioned, installation-owned binaries and a post-signing manifest;
- never stop processes by global image name; cleanup is limited to recorded
  process IDs/jobs/services owned by this installation;
- preserve or explicitly drain Main-owned Supervisor instances during Desktop
  upgrade before replacing their binary;
- make service accounts, state ACLs, firewall ownership, and uninstall scope
  explicit for each platform.

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

## 9. Implementation gates and phases

Release implementation remains gated until these criteria have executable
contracts and adversarial tests:

1. **Contract gate:** supported Main client/config scope, instance multiplicity,
   transport/authentication, owner policy, workspace identity, schemas, and
   shutdown/recovery semantics are fixed.
2. **Worker safety gate:** child isolation, policy ceiling, owner binding,
   cancellation/process containment, stable filesystem checks, journal
   durability, idempotency, and local orphan recovery are proven.
3. **Broker lifecycle gate:** asynchronous managed Worker control, protected
   endpoint publication, truthful readiness/capacity, EOF/crash handling, and
   bounded drain are proven.
4. **Supervisor gate:** empty-pool startup, dynamic authenticated discovery,
   durable pre-dispatch recovery, multi-instance management, and independent
   Worker advertisement are proven.
5. **Desktop/Main gate:** exact origin/schema checks, typed UI/IPC, toggle
   combinations, and real Main registration/load/tool invocation are proven.
6. **Installer gate:** scoped coexistence, upgrade/uninstall, conditional
   firewall behavior, state preservation, and final binary manifests are
   proven.
7. **Release gate:** every claimed platform has a non-empty packaged native
   acceptance suite with a supported Main/Codex version.

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
5. The supported real Main Codex client can apply or reload the managed MCP
   registration, start the packaged Supervisor over stdio, call `workers.list`,
   and delegate a bounded read-only task to a local or remote Worker.
6. A real native app-server child runs with the configured account, workspace,
   and effective restrictive policy; exactly one side-effecting turn occurs.
7. The UI restores task identity and state after window close, broker restart,
   Supervisor restart, and application upgrade without duplicate rows or work.
8. A paired but unauthorized Main cannot delegate; removing a pin creates a
   durable revocation record, rejects new requests, and cancels affected active
   work within the controlled five-second bound measured from confirmed
   revocation observation and process-tree cleanup.
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

The following additional oracles are mandatory; the original list above is
necessary but not sufficient:

16. The supported Main client, effective config path, config fingerprint,
    reload/restart behavior, and currently connected Supervisor instance are
    observable; a successful JSON/config write alone cannot pass.
17. Two concurrent Main sessions receive two isolated Supervisor instances with
    distinct management endpoints, task indexes, and cancellation scopes.
18. A local Worker endpoint rotates credentials and boot epoch; stale runtime
    descriptors and stale endpoint generations are rejected.
19. A Worker child cannot load Main's MCP entry through layered config,
    plugins, hooks, environment, credentials, or history paths.
20. A lost response after durable pre-dispatch intent does not create a second
    task or reassign the workspace; recovery reports the uncertain outcome.
21. Cancellation proves descendant cleanup or reports `cleanupState=unconfirmed`
    and retains the lease; it never reports a clean terminal state based only on
    a leader-process exit.
22. Two Worker processes with different journals cannot acquire the same
    workspace lease, including symlink/reparse replacement and `.` paths.
23. Renderer navigation, child frames, malformed IPC, and unset renderer URL
    cannot reach privileged Codex configuration, registration, or cancellation.
24. Local-only, remote-enabled, read-only, read-write, disabled, and broker
    crash/restart combinations each produce the documented state and capability
    set.
25. Upgrade and uninstall leave unrelated Worker installations, Main config
    entries, processes, firewall rules, and user data untouched.

## 11. Design review checklist

- [x] Six independent reviews completed against the current fork commit.
- [x] Desktop absence of Codex references and 13-versus-15 inventory gap
      independently confirmed.
- [x] Process ownership conflict between Electron, broker, Main Codex, and
      Supervisor resolved by keeping Main-owned MCP stdio.
- [x] Discovery bootstrap, readiness, approval, and packaged E2E gaps captured.
- [x] Windows/macOS/Linux lifecycle and independent remote Worker ownership
      included.
- [x] Astra max review consolidated into explicit contract, security, installer,
      and release gates.
- [x] Local runtime descriptor, endpoint credential generation, and transport
      contract.
- [ ] Supported Main client/config/reload contract and multi-instance registry.
- [ ] Worker child isolation, owner binding, pre-dispatch recovery, process
      cleanup proof, and cross-process workspace fencing.
- [x] Broker managed-control protocol and dynamic discovery/revocation contract.
- [x] Exact renderer origin/frame/schema enforcement and typed IPC implementation.
- [ ] Scoped installer/update/uninstall behavior and non-empty packaged native
      acceptance suites.
- [x] macOS arm64 package contains the Codex binaries and starts the packaged
      Electron broker with the packaged Worker path.
- [x] Installed Main Codex CLI can launch the packaged Supervisor and complete
      a read-only `workers.list` call against a managed Worker.
- [ ] User review of this written design.
- [x] Implementation plan and code changes.
