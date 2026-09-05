# Codex Desktop Integration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the existing Codex Supervisor/Worker control plane operational from the PAIR desktop application with packaged binaries, broker-owned local Worker lifecycle, Main Codex MCP registration, typed Desktop status/control, and packaged acceptance tests.

**Architecture:** Electron owns PAIR configuration, typed IPC, and the existing `nvpair-ui-broker` child. The broker optionally owns one local `nvpair-codex-worker` child through a versioned control protocol and publishes a protected runtime descriptor. Main Codex owns each `nvpair-codex-supervisor` MCP stdio session; the Supervisor authenticates local and remote Workers, persists task intent, and exposes metadata-only management to Desktop.

**Tech Stack:** Go 1.25 services, Go `net/http`, existing `nvpair-shared` modules, Electron 42, TypeScript, React 19, Vitest, Electron IPC, existing modular binary manifest/build pipeline.

**Spec:** `docs/superpowers/specs/2026-09-05-codex-desktop-integration-design.md`

## Global Constraints

- Electron owns the broker and Desktop state; Main Codex owns every Supervisor MCP stdio session; a Supervisor is never spawned as an Electron-owned MCP child.
- The broker-to-Worker control channel carries lifecycle and bounded status only; task prompts, context, responses, transcripts, credentials, and artifact bodies stay on Supervisor-to-Worker task transport.
- A local Worker is disabled by default and is started only from explicit persisted Desktop configuration; a local-only Worker is never advertised on the LAN.
- Worker `ready` requires executable, workspace, account/app-server, policy, listener, transport, and authorization checks; a live listener alone is not readiness.
- Every task state transition preserves owner identity, attempt/lease epoch, cleanup state, and idempotency; uncertain acknowledgement is never converted into a new task.
- Renderer IPC is fail-closed on exact origin, top-level frame, and runtime schema validation; no generic command execution is introduced.
- Existing unrelated PAIR service behavior, Main Codex configuration entries, remote Worker installations, and user data must remain unchanged.
- Every production code change follows a red test, expected failure, minimal green implementation, and full affected-suite verification before refactoring.
- No implementation task is considered complete without a named test command and captured exit status.

---

## Code map

- `desktop/src/shared/constants/modular-binaries.ts` owns the shipped/runtime binary inventory and launch ownership.
- `desktop/src/electron/service-bridge/modular-supervisor.ts` resolves packaged binaries and starts the Electron-owned broker.
- `desktop/scripts/build-modular-binaries.ts` builds every name declared by the inventory and writes `cli-bin/manifest.json`.
- `services/nvpair-ui-broker/main.go` parses broker flags and resolves child paths; `broker.go` owns broker worker lifecycle and discovery registration.
- `services/nvpair-codex-worker/main.go` owns Worker process startup, HTTP transport, state root, and shutdown; `httpserver.go` owns task protocol and readiness capabilities.
- `services/nvpair-codex-supervisor/main.go`, `pool.go`, `client.go`, and `mcp.go` own MCP stdio, Worker selection, transport, and task delegation.
- `services/shared/codexprotocol` owns the existing task/capability wire types; the new `services/shared/codexruntime` package owns local runtime descriptors and control messages.
- `desktop/src/shared/types/ipc-channels.ts`, `desktop/src/preload/api/`, and `desktop/src/electron/ipc/` own typed renderer/native boundaries.
- `desktop/src/electron/ipc/safe-handle.ts` is the common Electron sender trust boundary.
- `desktop/src/ui/stores/` and the existing Settings/Overview components own renderer state and UI integration.
- `services/installer/` and `desktop/electron-builder.config.ts` own platform lifecycle and packaged resources.

## Task 1: Ship the Codex binaries and pass their paths to the broker

**Files:**
- Modify: `desktop/src/shared/constants/modular-binaries.ts`
- Modify: `desktop/src/electron/service-bridge/modular-supervisor.ts`
- Create: `desktop/tests/modular/codex-binary-inventory.test.ts`

**Interfaces:**
- Produces the process name `'codex-worker'` for broker-owned optional runtime startup and the bundled base name `nvpair-codex-supervisor` for Main Codex registration.
- `brokerStartupArgs()` passes `--codex-worker-path <absolute path>` only when the packaged Worker exists; it never starts the Supervisor.

- [x] **Step 1: Write the failing inventory test.**

  Add Vitest assertions that `modularShippedBinaryBaseNames()` contains
  `nvpair-codex-worker` and `nvpair-codex-supervisor`, that only
  `codex-worker` is in `MODULAR_RUNTIME_BINARIES`, and that the Worker launch
  owner is `broker` with `optional: true` while the Supervisor is bundled-only.

- [x] **Step 2: Run the focused test and verify the expected failure.**

  Run `npm exec vitest run tests/modular/codex-binary-inventory.test.ts` from
  `desktop/`. It must fail because neither Codex binary is currently in the
  inventory.

- [x] **Step 3: Add the inventory entries and broker path flag.**

  Extend `ModularProcessName`, append the optional broker-owned Worker entry,
  append the Supervisor to `MODULAR_BUNDLED_BINARIES`, and add one
  `passPath('--codex-worker-path', 'codex-worker')` call in
  `brokerStartupArgs()`. Do not add a Supervisor `passPath` call.

- [x] **Step 4: Run the focused tests and packaging contract checks.**

  Run `npm exec vitest run tests/modular/codex-binary-inventory.test.ts`,
  `npm run typecheck`, and `npm run service-contracts:check` from `desktop/`.
  The test and both checks must exit 0.

- [x] **Step 5: Commit the packaging slice.**

  Commit with `feat: package Codex desktop binaries`.

## Task 2: Add the broker-managed Worker control protocol and runtime descriptor

**Files:**
- Create: `services/shared/codexruntime/descriptor.go`
- Create: `services/shared/codexruntime/control.go`
- Create: `services/shared/codexruntime/descriptor_test.go`
- Create: `services/shared/codexruntime/control_test.go`
- Modify: `services/nvpair-codex-worker/main.go`
- Create: `services/nvpair-codex-worker/managed.go`
- Create: `services/nvpair-codex-worker/managed_test.go`
- Modify: `services/nvpair-ui-broker/main.go`
- Modify: `services/nvpair-ui-broker/broker.go`
- Create: `services/nvpair-ui-broker/codexworker.go`
- Create: `services/nvpair-ui-broker/codexworker_test.go`

**Interfaces:**
- `codexruntime.RuntimeDescriptor` contains schema version, installation ID, Worker instance ID, boot epoch, endpoint, transport, credential reference/generation, policy revision, write/expiry timestamps, and readiness state.
- `codexruntime.ControlMessage` supports `start`, `status`, `drain`, `shutdown`, and `configRevision`; `ControlEvent` supports `ready`, `status`, `stopped`, and `error`.
- `codexworkerSupervisor.Start(ctx, config)`, `Status()`, `Drain(ctx)`, and `Stop(ctx)` are broker-owned lifecycle methods and expose no task-body field.

- [x] **Step 1: Write descriptor/control serialization and validation tests.**

  Cover rejection of zero schema, missing installation/instance IDs,
  non-increasing boot epochs, expired descriptors, secret material in JSON,
  unknown control kinds, task-body-shaped fields, and config revision rollback.

- [x] **Step 2: Run the shared tests and verify they fail for missing types.**

  Run `go test ./...` in `services/shared`; the new package tests must fail to
  compile because the runtime package does not exist.

- [x] **Step 3: Implement the shared runtime contract.**

  Use strict JSON decoding, bounded strings, RFC3339 UTC timestamps, and
  atomic descriptor writes (`create temp -> chmod user-only -> fsync -> rename`).
  Keep credential values out of the descriptor; only a protected credential
  reference and generation are serialized.

- [x] **Step 4: Add the managed Worker stdin/stdout protocol test.**

  Start the Worker in managed mode with a temporary workspace and fake
  app-server factory, send `start` and `status`, assert a `ready` event with a
  non-empty boot epoch and descriptor, send `drain` and `shutdown`, and assert
  `stopped`. Close parent stdin and assert bounded shutdown without a task
  request.

- [x] **Step 5: Implement Worker managed mode and broker supervision.**

  Add `--managed-control` to the Worker. Keep standalone signal mode intact.
  The broker passes a protected config path and Worker path, starts exactly one
  enabled instance, consumes structured events, publishes the descriptor, and
  withdraws readiness before config revision changes or shutdown. Broker EOF,
  Worker crash, duplicate start, and drain timeout must become explicit states.

- [x] **Step 6: Run affected Go tests and commit.**

  Run `go test ./...` in `services/shared`, `services/nvpair-codex-worker`, and
  `services/nvpair-ui-broker`, then commit with
  `feat: manage the local Codex Worker from the broker`.

## Task 3: Make Supervisor consume authenticated local runtime state and persist intent

**Files:**
- Create: `services/nvpair-codex-supervisor/runtime.go`
- Create: `services/nvpair-codex-supervisor/runtime_test.go`
- Create: `services/nvpair-codex-supervisor/taskindex.go`
- Create: `services/nvpair-codex-supervisor/taskindex_test.go`
- Modify: `services/nvpair-codex-supervisor/main.go`
- Modify: `services/nvpair-codex-supervisor/pool.go`
- Modify: `services/nvpair-codex-supervisor/client.go`
- Modify: `services/nvpair-codex-supervisor/mcp.go`

**Interfaces:**
- `runtime.LoadLocalDescriptor(path, now)` returns a validated local Worker target or a typed waiting state; stale generations and expired descriptors are rejected.
- `TaskIndex.Begin`, `MarkAcknowledged`, `MarkUncertain`, `MarkTerminal`, and `SnapshotMetadata` persist destination, owner, attempt, lease epoch, idempotency key, and cleanup state before dispatch.
- A Supervisor starts and stays alive with an empty pool, reloads descriptor generations, and never reassigns an uncertain dispatch.

- [x] **Step 1: Write failing runtime/index tests.**

  Cover descriptor expiry, boot-epoch replacement, credential-generation
  mismatch, empty-pool startup, durable pre-dispatch intent, lost ACK, restart
  recovery, owner-bound reads/cancel/follow-up, and metadata-only pagination.

- [x] **Step 2: Run the focused supervisor tests and verify the expected failures.**

  Run `go test ./...` in `services/nvpair-codex-supervisor`; the new tests must
  fail because the runtime loader and durable index are absent.

- [x] **Step 3: Implement local descriptor loading and dynamic pool refresh.**

  Add a local authenticated target without LAN advertisement, keep remote mTLS
  discovery separate, and make refresh/revocation/failed-probe/duplicate
  identity decisions explicit. A first empty snapshot must not terminate MCP.

- [x] **Step 4: Implement the durable task index and metadata management API.**

  Persist complete intent before the first HTTP request, record uncertain ACK
  instead of creating a second task, enforce the authenticated owner on every
  task operation, and expose only bounded metadata through the private
  management protocol.

- [x] **Step 5: Run Go tests, race tests for the index, and commit.**

  Run `go test ./...` and `go test -race ./...` in the Supervisor module, then
  commit with `feat: make Supervisor recovery and local discovery durable`.

## Task 4: Implement typed Electron configuration, registration, and IPC security

**Files:**
- Create: `desktop/src/shared/types/codex.ts`
- Create: `desktop/src/electron/codex/config-store.ts`
- Create: `desktop/src/electron/codex/mcp-registration.ts`
- Create: `desktop/src/electron/codex/codex-manager.ts`
- Create: `desktop/src/electron/ipc/codex.ipc.ts`
- Modify: `desktop/src/shared/types/ipc-channels.ts`
- Modify: `desktop/src/preload/api/index.ts`
- Modify: `desktop/src/electron/ipc/index.ts`
- Modify: `desktop/src/electron/ipc/safe-handle.ts`
- Create: `desktop/tests/modular/codex-config.test.ts`
- Create: `desktop/tests/modular/codex-ipc.test.ts`
- Create: `desktop/tests/modular/safe-handle-origin.test.ts`

**Interfaces:**
- `CodexConfig` stores enabled state, workspace root, policy ceiling, account, Worker state root, and registration ownership metadata; secrets remain in protected OS/user files and never enter renderer state.
- `CodexManager.getState()`, `configureWorker(input)`, `setWorkerEnabled(enabled)`, `getMcpRegistration()`, `applyMcpRegistration()`, `removeMcpRegistration()`, `listTasks(page)`, and `cancelTask(ref)` are the only privileged operations exposed to preload.
- MCP registration reports `registered`, `waiting_for_main`, `connected`, or `failed`; file write is never equivalent to live MCP availability.

- [x] **Step 1: Write failing config/registration/origin tests.**

  Cover atomic backup-preserving registration, unrelated-entry preservation,
  ambiguous replacement refusal, effective `CODEX_HOME` resolution,
  concurrent fingerprint changes, managed-entry-only removal, schema rejection,
  exact origin rejection, child-frame rejection, and unset-origin fail-closed
  behavior.

- [x] **Step 2: Run the focused config/origin tests and verify red.**

  Run the three new Vitest files; they must fail because the Codex manager and
  hardened sender checks do not exist.

- [x] **Step 3: Implement main-process config and registration.**

  Use atomic temp-file replacement with mode `0600` where supported, retain one
  timestamped backup, fingerprint before write, and reject concurrent edits.
  Register the packaged Supervisor with an absolute path and explicit args in
  the resolved Main config scope. Detect actual Main activation only through
  the Supervisor management state.

- [x] **Step 4: Harden `safeHandle` and add typed preload methods.**

  Require the exact configured renderer origin, `event.senderFrame ===
  event.sender.mainFrame`, and a validated channel payload before dispatching.
  Do not treat an empty `ELECTRON_RENDERER_URL` as a wildcard and do not allow
  arbitrary `file://` origins for privileged Codex calls.

- [x] **Step 5: Run desktop unit tests and typecheck, then commit.**

  Run `npm run test:unit -- --run` and `npm run typecheck` in `desktop/`, then
  commit with `feat: add typed Codex desktop control plane`.

## Task 5: Add Desktop state, settings, task metadata, and local execution consent

**Files:**
- Create: `desktop/src/ui/stores/codex.store.ts`
- Create: `desktop/src/ui/components/Codex/CodexSettings.tsx`
- Create: `desktop/src/ui/components/Codex/CodexTasks.tsx`
- Modify: `desktop/src/ui/types/settings-window.ts`
- Modify: `desktop/src/ui/components/Settings.tsx`
- Modify: `desktop/src/ui/components/MainApp/MainApp.tsx`
- Create: `desktop/tests/modular/codex-store.test.ts`
- Create: `desktop/tests/modular/codex-ui.test.tsx`

**Interfaces:**
- The store exposes the documented states `disabled`, `setup_required`, `starting`, `ready`, `busy`, `unauthorized`, `incompatible`, `failed`, and `stopped`.
- UI displays Worker, task ID, state, attempt, timestamps, and artifact metadata only; it never renders prompts, raw responses, credentials, hidden reasoning, or artifact bodies.

- [ ] **Step 1: Write failing store/UI tests.**

  Cover disabled-by-default behavior, local read-only default, explicit write
  consent, actionable missing-prerequisite state, state updates after broker
  restart, task pagination, and opaque cancellation references.

- [ ] **Step 2: Run the focused tests and verify red.**

  Run the two new Vitest files; they must fail because the Codex store and
  components are not present.

- [ ] **Step 3: Implement the store and settings/task surfaces.**

  Connect only to typed preload methods, make Worker enablement explicit, show
  local/remote and read-only/read-write capability separately, and keep
  metadata bounded and freshness-labeled.

- [ ] **Step 4: Run the full Desktop unit/typecheck suites and commit.**

  Run `npm run test:unit -- --run` and `npm run typecheck`, then commit with
  `feat: expose Codex Worker state and tasks in Desktop`.

## Task 6: Package lifecycle, installer scope, and native acceptance

**Files:**
- Modify: `desktop/electron-builder.config.ts`
- Modify: `services/installer/nvpair-setup.nsi`
- Modify: `services/installer/macos/com.nvidia.nvpair.codex-worker.plist`
- Modify: `services/installer/linux/nvpair-codex-worker.service`
- Create: `desktop/tests/modular/codex-packaging.test.ts`
- Create: `desktop/tests/e2e/codex-desktop.e2e.test.ts`
- Modify: `desktop/vitest.config.ts`
- Modify: `docs/superpowers/specs/2026-09-05-codex-desktop-integration-design.md`

**Interfaces:**
- Installer cleanup uses installation-owned process IDs/jobs/services and managed registration/firewall ownership; it never kills by global image name.
- The packaged test suite has a non-empty named E2E project and verifies the real packaged binary manifest, broker-owned Worker, Main MCP registration, local task, remote task, cancellation, restart, and independent Worker coexistence.

- [ ] **Step 1: Write failing package/E2E contract tests.**

  Assert both Codex binaries are present in the final manifest, the Supervisor
  is not listed as an Electron runtime child, E2E include patterns are
  non-empty, and an unrelated Worker PID/config/firewall entry is preserved.

- [ ] **Step 2: Run the focused tests and verify red.**

  Run `npm exec vitest run tests/modular/codex-packaging.test.ts` and the E2E
  project selector; failures must identify the missing package or suite rather
  than silently passing zero tests.

- [ ] **Step 3: Implement scoped platform lifecycle and package checks.**

  Add conditional local/remote firewall behavior, versioned manifests, update
  drain/restart coordination, installation-owned cleanup, and platform-specific
  executable/state ACL checks.

- [ ] **Step 4: Implement the non-empty packaged native suite.**

  Start the built broker/Worker/Supervisor on the supported host, use the real
  Main Codex MCP client contract, run a bounded read-only task, revoke trust,
  verify cancellation and cleanup state, restart all owners, and confirm no
  duplicate task or unrelated process is touched.

- [ ] **Step 5: Run release verification and commit.**

  Run the complete Go suites, `npm run test:unit -- --run`, `npm run typecheck`,
  `npm run service-contracts:check`, platform packaging checks, and the named
  native E2E suite. Commit with `feat: integrate Codex pair execution in Desktop`.

## Completion checklist

- [ ] All six tasks have red-green test evidence and commits.
- [ ] All seven implementation gates in the design document are backed by an executable test or a documented platform oracle.
- [ ] The design document checklist is updated to show only verified items.
- [ ] `git diff --check`, full tests, typechecks, package manifest verification, and native acceptance all pass on the claimed platform.
- [ ] No push or merge is performed without an explicit user request.
