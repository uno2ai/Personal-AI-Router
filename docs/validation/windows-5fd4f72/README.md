<!--
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
-->

# Windows validation of 5fd4f72

Date: 2026-09-07. Windows x64 (build 26200), Go 1.26.4, Node 24.18.0, npm 11.5.1.
Node is below the project's required >=25.5.0, so these desktop results do not establish verification on a supported Node version.

Fetched origin/main and confirmed 5fd4f72. Tested a git archive in `.validation/5fd4f72` because the original checkout is 9e46b41 with a pre-existing desktop/package-lock.json modification. Original tracked files were preserved. Source fixes were not made.

| Check | Result |
|---|---|
| Go modules | 14 returned success, 4 failed; 21 failed tests in total. Success does not imply conditional tests ran. |
| Go cross-process tests | Failed after 518.205s, with 14 failed tests; no global timeout |
| npm ci | Passed, engine warning |
| Desktop unit tests | 225 passed, 3 failed, 2 skipped |
| npm run typecheck | Passed |
| npm run service-contracts:check | Passed |
| npm run build:win:x64 | Passed |
| Native E2E, instructions exactly | 1 passed, 1 failed, 1 skipped |
| Native E2E, actual codex.exe | 1 passed, 1 failed, 1 skipped |
| Installation/UI/restart manual checklist | Not performed |

## Observed failures

- Go Supervisor: four tests fail at the Unix mode-bit credential check (`client.go:119`), including pinned TLS client initialization and loading a local target.
- Go Worker: isolated authentication permissions are reported as 666; managed config revision reports `Attempt to release mutex not owned by caller.`
- Go broker: `/protected/codex-worker.json` in a test is not an absolute Windows path.
- Go cross-process: five Codex task tests fail; nine discovery/model enrichment/workload propagation tests fail, mostly with discovery/propagation timeouts. These network-sensitive failures have not been isolated from host/network conditions. One leftover test-owned Worker process was terminated after the test command completed.
- Desktop: two codex-config tests expect Unix mode bits; e2e-user-data-path test cannot create a symlink (EPERM).
- Native E2E with `(Get-Command codex).Source`: resolves to codex.ps1. Diagnostic Worker error: `%1 is not a valid Win32 application.` The E2E itself only reports `timed out waiting for managed Worker ready`.
- Retrying with the installed native codex.exe passes the ready stage but fails at tasks.delegate: `delegatedResult.isError` is true (test line 382). No successful delegated task was established.
- The macOS-only Electron restart E2E is skipped on Windows.

## Artifacts

- Installer: `5fd4f72/desktop/release/0.1.1/windows/NVPAIR-Setup-0.1.1-x64.exe` (158436731 bytes).
- Logs: `logs/go-all.log`, `logs/test-unit.log`, `logs/typecheck.log`, `logs/service-contracts-check.log`, `logs/build-win-x64.log`, `logs/codex-e2e.log`, `logs/codex-e2e-exe.log`, `logs/worker-diagnostic.log`.
- Full packaged directory listing: `logs/windows-folder-list.txt`.
- Initial sandbox failures are separately preserved in `logs/go-tests.log` and `logs/npm-ci.log`; they are not product test failures.
- Installer SHA256: `0EF1D523D3772139B289C9E94F79D37D618A43F82EA1ABFBDAB79C2B90E03206`.

## Sharing notes

User profile paths, repository absolute paths, and private IPv4 addresses have been replaced with placeholders in this shared copy. Original diagnostic messages and test results are otherwise retained. Installer and source checkout are not included.

Download [the redacted logs and complete directory listing](./windows-validation-5fd4f72.zip). Paths in the Artifacts section refer to the original local validation workspace; the installer is not committed.
