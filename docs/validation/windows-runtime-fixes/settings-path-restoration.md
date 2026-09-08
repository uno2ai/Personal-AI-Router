<!--
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
-->

# Settings path restoration

The settings state now returns the saved workspace root and Codex executable.
Opening the Codex settings panel displays those values. Refreshing worker state
preserves edits in progress, including explicitly cleared fields and policy edits.
No credentials or additional private configuration fields are returned.

Validated on macOS on 2026-09-08: Desktop unit suite, typecheck, service contract
check, lint (formatting warnings remain), dead-code check, and Electron production
bundle build. The unit suite needs `GOFLAGS=-buildvcs=false` in the local nested
Git/SVN checkout. The configuration regression checks retrieval through a new
manager instance after saving.

The packaged GUI checks below now cover both macOS and Windows. This does not
replace the prior Windows runtime acceptance record or claim a new Windows
installer build.

## macOS packaged GUI acceptance

On 2026-09-08, built commit `abeb67e` with
`GOFLAGS=-buildvcs=false npm run build:mac:arm64` (exit 0). Unsigned arm64
APP, DMG and ZIP were produced. Ran the packaged APP on macOS 26.6.2 using
an opt-in E2E userData directory under the OS temporary directory.

Verified through the native GUI:

- Saved a temporary workspace path and `/opt/homebrew/bin/codex`.
- Both inputs displayed the saved paths after the service restart on save.
- Edited the executable input without saving; Refresh preserved the edit.
- Switched to Cluster and back; both inputs showed the saved values.
- Quit the app (exit 0), launched it again with the same profile, and confirmed
  both saved paths were displayed in Codex Pair settings.

Worker remained disabled and MCP unregistered for this settings-only check.
The app was left open on the verified settings screen. An updater warning about
missing `app-update.yml` was observed in this locally packaged build.

DMG SHA256: `ef46001f61b2a4429ab08800c7fecfd775ac5b99242022b9ac3ab244af4b83dc`.

## Windows packaged GUI acceptance

On 2026-09-08, verified `36feec958b4f8beaea3616f4ef9708308c471f72`
on native Windows x64 using Node 26.0.0. Desktop unit tests passed
(238 pass, 2 existing skips), all three typecheck targets passed, and
`npm run build` plus `electron-builder --win --x64 --dir` produced a fresh
unpacked Windows package. The existing verified Go binaries were reused;
this change does not modify Go source. No new NSIS installer was built.

The real packaged Electron window used the previous isolated Windows GUI
profile, which already held saved paths and an enabled Worker. Fifteen GUI
assertions passed:

- Existing saved workspace and executable paths appeared on opening Settings.
- Saving a new Windows workspace path containing spaces and a native
  `codex.exe` path displayed both saved values after the service restart.
- Refresh preserved unsaved workspace, executable and policy edits, including
  an explicitly emptied executable input.
- Switching to Cluster and back restored saved paths and the saved policy;
  unsaved edits did not persist.
- Graceful app exit returned 0. Relaunch restored both saved paths and the
  Worker reached Ready.

The updated verification app is open on Settings > Codex Pair. Screenshots
were captured and visually inspected. The prior Main Codex probe is no longer
running, so this reused profile also shows its stale Supervisor named-pipe
ENOENT warning; this run does not claim to retest Main MCP or fix that warning.

[Redacted Windows verification logs](windows-path-restoration-36feec9.zip)
include the assertion result and unit/typecheck/build/package output with
per-file SHA-256 checksums. Raw screenshots, controller scripts and logs remain
locally in `.validation/windows-path-restoration`. Initial controller-only
launch/selector errors were corrected and retained separately in the evidence;
no additional product changes were required for the passing GUI run.
