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
