<!--
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
-->

# Windows runtime repair validation

This repair branch starts at `codex/windows-validation-5fd4f72` (`538cdfe`),
which records the first Windows validation of upstream `5fd4f72`.
The original validation evidence remains in [windows-5fd4f72](../windows-5fd4f72/README.md).
This branch has not been merged into main; native macOS regression testing and final review remain with the maintainer.

## Environment

- Native Windows x64; Go 1.26.4.
- Node 26.0.0 and npm 11.12.1, satisfying the repository's Node >=25.5 requirement.
- npm dependencies come from the unchanged lockfile installed during the original validation.
- Windows x64 packaging targets Desktop 0.1.1.
- Installed native Codex CLI: 0.152.1.

## Runtime changes and focused evidence

Windows protected state uses explicit private ACLs instead of Unix mode bits.
File operations walk one path component at a time using native directory
handles, retain those handles during I/O, reject reparse traversal and
untrusted file ownership, and create private files before writing bytes.
Existing append files must already be private, have protected ACL inheritance,
and have no hard-link aliases. Stored artifacts use the protected reader.
The implementation supports the validating host's existing AppData and E:
ancestry without changing those directories' ACLs.

Named mutexes are acquired and released on the same locked OS thread. Windows
Codex children start suspended, enter their kill-on-close Job Object, and then
resume. Tests exercise cross-thread release, abandoned-owner recovery, job
ownership, descendant shutdown, and preservation of an independent Worker.
Supported npm Codex `.ps1`/`.cmd` shims locate the installed native executable;
their contents are not passed to a shell.

The broker's absolute-path fixture now uses a platform-native path. The fake
Codex app-server used by cross-process tests implements the configuration probe
required before starting a task. Test cleanup retains and stops only the
processes owned by the fixture.

Desktop reuses the bundled Go broker's bounded, one-shot protected-file helper
for native ACL-aware reads and writes. Managed state requires private ACLs;
existing external Main configuration is read through an explicit owner-checked
input API, preserving unrelated configuration. Writes and backups preserve
existing parent permissions on Unix. No routing logic moves into TypeScript,
and no JSON-RPC contract changes are introduced.

The native E2E harness retains early process messages, bounds pending output
and shutdown waits, and reports operational state without dumping prompts,
responses or raw process stderr. Windows unit fixtures inspect real ACLs and
use directory junctions to exercise escape protection without symlink privilege.

Focused native Windows Worker, Supervisor and shared-package runs pass. Seven
Codex cross-process tests pass, including managed restart/shutdown preserving
an independently running Worker. Independent review found and drove correction
of intermediate-path handling, untrusted ownership, inherited ACL changes, and
a Unix first-use lock regression; the final Task 1 review accepted `3f0c299`.

Linux test binaries crosscompiled with Go were executed under WSL2 Ubuntu:
the protectedfile tests and five Journal plus five TaskIndex tests passed.
The new missing-parent regression was observed failing before the Unix-only
lock preparation fix and passing afterward. Darwin arm64 test binaries were
compiled, but were not executed on macOS.

The native fixture that assigns an actual foreign owner is skipped because
the current Windows token cannot assign that SID (`ERROR_INVALID_OWNER`).
The native security-descriptor owner-rejection test passes; this is not
equivalent to running the skipped live ownership fixture.

## Discovery diagnosis

All nine discovery-related tests that timed out in the first Windows run passed
when rerun without changing discovery code or extending timeouts:

- The three `TestInProcess` cases passed in 31.816 seconds including TestMain builds.
- `TestModelsHTTPEnrichment`, `TestModelsPeriodicRefreshConvergesWithoutMDNSChange`,
  `TestWorkloadManagerRehydratesActiveWorkloadOnRestart`,
  `TestWorkloadManagerRehydratesRecentTerminalOnRestart`,
  `TestWorkloadFailedOnNodeLoss`, and `TestWorkloadManagerOutboundBroadcast`
  passed together in 144.816 seconds including builds.
- A standalone probe using PAIR's mDNS browser discovered both a legacy
  zeroconf advertiser and PAIR's native advertiser within the same four-second
  observation period, with no reported send failures.

Adapter and firewall inspection was read-only. The host had Wi-Fi, a VPN adapter,
and virtual adapters, and application-specific firewall allow rules were present.
The evidence establishes that the original failure was not consistently
reproducible in the later environment. It does not establish which adapter,
firewall rule, network state, or timing condition caused the first failure.
There is no discovery-code change or timeout increase in this branch.
All nine cases also passed in the subsequent full 18-module Go run.

Evidence: `mdns-original-tests.log`, `mdns-six-original-tests.log`,
`mdns-ab-probe.log`, and the read-only firewall logs in the accompanying archive.

## Verification matrix

| Check | Source revision | Result |
| --- | --- | --- |
| All 18 Go modules, `go test -v -count=1 ./...` | `7e3e00c` | All module commands exit 0; 1,243 top-level tests pass, 13 skip, 0 fail |
| Desktop unit tests | `7e3e00c` | 238 pass, 2 skip |
| Desktop typecheck, lint, dead-code, service contracts, build-script verification; root SPDX | `7e3e00c` | All exit 0; lint has 25 pre-existing formatting warnings, no errors |
| Windows x64 installer build | `7e3e00c` | Exit 0 |
| Packaged native Codex task, first attempt | `9225e38` | Packaging passes; actual task blocks on approval; macOS-only Electron test skips |
| Final packaged native Codex E2E | `7e3e00c` | 2 pass, 1 macOS-only skip; real delegated task completes, 22.748 seconds |

The two Worker path-security tests that previously skipped now use native
Windows directory junctions. Their traversal, native absolute path, slash
absolute path, link escape and size assertions run independently; both tests
and all nine subcases pass on Windows and under WSL Linux. The final full
18-module run at `7e3e00c` includes this correction and the native sandbox argv
regression. Earlier full-run and focused-rerun evidence is also retained.

Remaining skips cover platform-specific sockets/signals/shell fixtures,
unavailable live foreign-owner or symlink privileges, unreadable-directory
semantics, and an intentionally subprocess-only test helper. They are untested
coverage, not passing assertions. Detailed names and reasons are in the verbose
module logs. Windows arm64 and native macOS were not executed.

The Electron restart/ownership E2E is macOS-only. Windows installation and the
GUI checklist (settings visibility, ready state, Main MCP registration,
`workers.list`, shutdown ownership and recovery after app restart) still require
manual confirmation. Go cross-process ownership/restart coverage does not
establish that the Windows GUI checklist passed.

## Review and decisions

Independent whole-branch review accepted production source at `5b3a8aa`; a
separate review accepted the four-file fixture correction at `9225e38`.
A scoped review accepted the native sandbox correction at `7e3e00c`.
No Critical or Important finding remained in these reviewed ranges. Two
nonblocking test follow-ups remain: hoisted/missing native npm package fixtures,
and injected Job Object assignment/resume failure coverage.

The native path design pins ancestor handles without demanding private ACLs
on ordinary AppData or drive ancestors. This preserves usable existing Windows
layouts while checking the actual opened managed file. The additional native
API complexity has dedicated traversal, ownership, inheritance and publication
tests. Discovery remains unchanged because the original failures did not
reproduce; an intermittent environment-dependent failure may still recur.

## Native task integration correction

The first packaged real-model task reached Worker ready, Supervisor discovery,
task delegation and child/thread creation, then transitioned to
`waiting_approval` and `blocked`. Metadata-only diagnostics identified a
`git status` command approval request. The isolated `CODEX_HOME` copied
authentication but omitted the Windows sandbox selection, leaving the native
Windows execution backend disabled.

Windows app-server startup now explicitly selects Codex's `unelevated`
restricted-token sandbox in the isolated child arguments. Read-only sandbox
policy and `on-request` approval handling remain enforced. No host setup,
Main config import, sandbox bypass or automatic approval is introduced.
The integration uses restricted-token enforcement; it does not claim to test
the elevated dedicated-user Windows backend. Unix arguments remain unchanged
and their regression tests were executed under WSL.

The argv regression failed before the correction and passed afterward. A
diagnostic native rerun with the corrected Worker completed the real task
(two tests passed; the macOS-only Electron scenario skipped). Temporary
approval classification instrumentation was removed; the committed harness
retains terminal state and allowlisted journal metadata for future failures.
The final installer was then rebuilt at `7e3e00c`; its packaged Worker and
Supervisor repeated the real-model test successfully (2 pass, 1 macOS-only
skip). See `final-7e3e00c-codex-native-e2e.log`.

## Installer

Verified source: `7e3e00cdc044565044f24aa8f269e0fcfcc95895`.
The subsequent evidence commit changes documentation and the log archive only.

- Output: `desktop/release/0.1.1/windows/NVPAIR-Setup-0.1.1-x64.exe`
- Size: 152,019,190 bytes.
- SHA-256: `52D9232EFE66B827533D7876C781EBFC164B976C8A3570212F2B753B2580A413`
- Packaged versions: Worker 0.1.1, Supervisor 0.1.1, broker 0.40.3.

The full release-directory listing and artifact metadata are in the archive.
The installer remains local, outside Git. Build-generated notice inventory and
CSS formatting changes were inspected and restored in the source checkout;
their raw diffs remain in local validation logs. The packaged build contains its
generated Windows notices and styles. The original checkout's pre-existing
package-lock modification was preserved.

## Version scope

The Worker and Supervisor patch versions change from 0.1.0 to 0.1.1; the broker
changes from 0.40.2 to 0.40.3. These binaries compile the modified shared runtime
or protected-file code. Product/installer release metadata and Desktop 0.1.1
remain unchanged because this branch is a repair candidate for review.

## Reproduce on Windows

Use Go >=1.25, Node >=25.5 and an authenticated native Codex installation.
Run from this repair branch. The validation used Node 26.0.0 and Codex 0.152.1.

```powershell
Get-ChildItem services -Recurse -Filter go.mod | ForEach-Object {
    Push-Location $_.Directory.FullName
    try {
        go test -v -count=1 ./...
        if ($LASTEXITCODE -ne 0) { throw "Go tests failed: $($_.Directory.Name)" }
    } finally { Pop-Location }
}
Set-Location desktop
npm ci
npm run test:unit -- --run
npm run typecheck
npm run lint
npm run dead-code:check
npm run service-contracts:check
npm run verify:build-scripts
npm run build:win:x64
$env:PAIR_RUN_CODEX_E2E = '1'
$env:PAIR_CODEX_E2E_CLI_BIN = (Resolve-Path '.\release\0.1.1\windows\win-unpacked\resources\cli-bin').Path
$env:PAIR_CODEX_BIN = (Get-Command codex).Source
npx vitest run --project e2e tests/e2e/codex-desktop.e2e.test.ts
```

Inspect each command's exit code before proceeding. The supported installed
Codex npm shim is resolved to `codex.exe` by the Worker. The final validation
exercises that `.ps1` locator path; it does not execute the script as the child.
The native model task requires working authentication and service availability.

## Evidence handling

The shared archive contains diagnostic text and reports, with local profile and
repository paths, host identifiers, and private network addresses redacted.
UUIDs and identity fingerprints are also removed from shared diagnostic text.
The original logs remain in the validating checkout's local `.validation`
directory. Installers, native test binaries, source archives, credentials,
prompts, and model response bodies are not published as validation artifacts.

Download [the redacted logs and review reports](windows-runtime-fixes-7e3e00c.zip).
The archive includes `SHA256SUMS.txt` for its individual evidence files.
