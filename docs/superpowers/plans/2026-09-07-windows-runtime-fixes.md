<!--
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
-->

# Windows runtime fixes implementation plan

**Goal:** Correct the Windows failures recorded for 5fd4f72 and publish a reviewed branch with native verification evidence.

**Architecture:** Go services remain authoritative for credential protection, executable selection and process ownership. Windows ACL validation replaces Unix mode inspection on Windows without weakening Unix protections. Cross-platform tests use real platform paths and explicit resource cleanup. Discovery diagnostics isolate the network before any production discovery change.

**Spec:** User-approved Windows repair scope, captured below; baseline evidence is [the Windows validation report](../../validation/windows-5fd4f72/README.md).

**Tech stack:** Go >=1.25, Node >=25.5, Electron/TypeScript, Windows ACL and process APIs. No new dependencies, no protocol changes unless every relay/consumer is updated, no timeout increases to mask failures. Keep original checkout modifications intact. Sign off commits and push only the repair branch. Main merge and macOS regression testing belong to the user.

## Task 1: Go native runtime and protected files

Files: services/shared (new focused Windows/Unix protection helpers if needed), services/nvpair-codex-worker, services/nvpair-codex-supervisor, services/nvpair-ui-broker/codexworker_test.go, services/tests/codex_supervisor_test.go.

- [x] Reproduce the existing Supervisor/Worker/broker failures; retain command output.
- [x] Add regression coverage for actual Windows ACL rejection of broad access and acceptance of protected files; Unix modes must stay enforced.
- [x] Protect worker config, credentials, isolated authentication and parent directories before secret writes; do not treat chmod on Windows as protection.
- [x] Fix mutex ownership and handle release with deterministic cross-thread/process contention tests, including release/reacquire and abandoned-owner recovery.
- [x] Resolve Windows npm Codex shims to the installed native executable or reject unsupported invocation with an actionable error; preserve argument boundaries and avoid arbitrary shell interpretation.
- [x] Fix Windows test absolute paths and owned-process shutdown; preserve independently owned workers.
- [x] Run the affected Go module tests and Codex cross-process tests; report exact evidence, any remaining failure, and affected service versions to bump.

## Task 2: Desktop platform tests and native E2E

Files: desktop/src/electron/codex/config-store.ts, codex-manager.ts, mcp-registration.ts; desktop/tests/modular/codex-config.test.ts, e2e-user-data-path.test.ts; desktop/tests/e2e/codex-desktop.e2e.test.ts; relevant Windows testing documentation.

- [x] Reproduce unit failures on supported Node; confirm Go protection interface from Task 1 before choosing how desktop config is secured.
- [x] Use native directory ACL protection for Windows secret/config writes, with failing regression coverage; retain Unix permission checks.
- [x] Test symlink escape via Windows directory junctions when symlink privilege is absent; only skip an unavailable capability with an explicit reason, never skip the path-security assertion silently.
- [x] Make native E2E report actionable control errors without printing secrets or task output, and clean up processes with bounded waits.
- [x] Run unit tests, typecheck, lint, contracts and dead-code checks; distinguish pre-existing issues.

## Task 3: Discovery isolation, end-to-end verification and publication

Files: services/tests discovery/network fixtures only where evidence warrants; docs/validation/windows-runtime-fixes; services/versions.json; relevant build/test docs.

- [x] Reproduce discovery independently of PAIR with a minimal zeroconf probe and inspect active adapters/firewall rules read-only.
- [x] Distinguish mDNS transport availability from application handling; do not weaken firewall policy or increase timeouts. Any code fix needs a regression test first.
- [x] Apply required component patch bumps for changed compiled output.
- [x] Run all Go modules with verbose skip accounting, all requested Desktop checks, Windows x64 package build, and packaged native Codex E2E. Retain logs from every attempt.
- [x] Review complete diff independently, resolve findings, redact shared logs, and commit/push the repair branch for user macOS review. Do not merge main.

## Completion evidence

Final verified source: `7e3e00cdc044565044f24aa8f269e0fcfcc95895`.
All 18 Go modules pass (1,243 top-level passes, 13 explicit skips); Desktop
has 238 passes and 2 skips, with all static gates passing. Windows x64 packaging
and the actual packaged Worker/Supervisor Codex task pass (2 E2E passes,
1 macOS-only skip). All nine original discovery failures pass unchanged.

Native integration exposed one additional Windows issue: isolated app-server
startup omitted the native sandbox selection. Windows now selects the
restricted-token sandbox explicitly while retaining task limits and approval
handling; Unix argv behavior is unchanged and was executed under WSL.
The complete source and both final correction ranges received independent
review with no remaining Critical/Important findings. The evidence-only
closeout commit contains the [report and redacted archive](../../validation/windows-runtime-fixes/README.md).
Main merge, native macOS regression and Windows manual GUI confirmation remain
with the maintainer.
