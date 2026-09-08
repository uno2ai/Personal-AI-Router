<!--
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
-->

# Windows GUI acceptance after main integration

On 2026-09-08, the maintainer authorized main integration and native Windows
GUI verification. Local and remote main were fast-forwarded to
`cc9d31b0778d25845c7f047d5147ebd21e079b80`. The existing local
`desktop/package-lock.json` modification was preserved byte-for-byte.

The actual packaged `win-unpacked/PAIR.exe` from the verified Windows x64
build was launched visibly. Playwright attached to Electron and operated the
rendered controls; screenshots were inspected. No production code was changed
for this acceptance run. The package's runtime source is `7e3e00c`; `cc9d31b`
adds validation evidence only.

## Results

| Check | Observed result |
| --- | --- |
| Application launch | Native Windows window renders; first-run onboarding and Settings are accessible |
| Codex settings | Settings > Codex Pair exposes Worker configuration and Main MCP controls |
| Save and enable | Actual input/button interactions save the disposable workspace and npm `.ps1` locator; Worker reaches Ready |
| Main MCP registration | Apply registration writes the managed entry into the isolated Main configuration; GUI shows Waiting for Main Codex, then Connected while Main runs |
| Main `workers.list` | Native Codex CLI 0.152.1 loads that GUI-written registration, completes exactly one `workers.list` call and receives worker ID `local` |
| Close the window | App remains in the tray; its Worker and the independent Worker remain alive |
| Quit the application | Graceful Electron `app.quit()` exits the broker and its owned Worker; the separately started Worker stays alive |
| Relaunch | Worker returns to Ready with its original identity and a newer boot epoch; enabled state and registration fingerprint persist |
| Main after relaunch | A second native Main invocation again completes `workers.list` and receives `local`; the GUI shows Connected |

The separately started Worker was stopped through its own managed shutdown
command only after the ownership and restart assertions completed.

## Main approval policy

Initial noninteractive Main attempts failed with the operational error
`MCP tool call requires approval, but approval policy is never`. These were
approval refusals, not successful tool calls or evidence of a broken PAIR
transport. The successful probes retained the read-only sandbox and used
invocation-only overrides for the explicitly requested metadata tool:

```text
mcp_servers.pair-codex-supervisor.enabled_tools=["workers.list"]
mcp_servers.pair-codex-supervisor.tools={"workers.list"={approval_mode="approve"}}
```

The inline table preserves the literal dot in the tool name. No other MCP tool
was exposed by this test override, and no permanent global approval setting
was changed. The settings are documented in the
[official configuration reference](https://learn.chatgpt.com/docs/config-file/config-reference).
The Worker sandbox, approval handling and task limits were unchanged.

## Nonblocking UI finding

After configuration or relaunch, the Workspace root and Codex executable
inputs are empty even though their saved settings are still used and the Worker
is Ready. The GUI therefore does not show the currently configured paths.
This is a display/usability issue; persisted configuration and restart were
verified independently. It remains a follow-up, not a fixed item in this report.

## Evidence and boundaries

[Redacted GUI metadata](windows-gui-cc9d31b.zip) contains the state transitions,
Main MCP call outcomes, PID-based shutdown assertions, restart assertions and
per-file SHA-256 checksums. Only operational metadata was recorded from Main;
prompts, model response bodies and credentials are excluded.

Full screenshots, local fixture/controller scripts and original metadata remain
in `.validation/windows-gui` on the validating Windows PC. Screenshots were
not published because they contain local profile, host and network information.

PAIR user data and the Main `CODEX_HOME` were isolated in a temporary test
profile; the regular Main configuration was not registered or modified.
The test uses the real packaged Electron application and native Go services,
not mocked UI state. This acceptance does not cover the NSIS installation
wizard, normal-profile migration, Windows arm64 or native macOS. The permanent
macOS-only Electron E2E remains platform-gated; this report records the separate
Windows GUI run.
