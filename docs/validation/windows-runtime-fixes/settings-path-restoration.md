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

Windows GUI verification of this change remains pending: reopen the panel and
restart the app, confirm both paths display, then edit a field and refresh to
confirm the unsaved edit is preserved. This does not replace the prior Windows
runtime acceptance record or claim a new Windows installer build.

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
