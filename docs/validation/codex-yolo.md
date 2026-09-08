<!--
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
-->

# Personal YOLO execution

YOLO is an explicit local opt-in, not a new installation default. It runs
delegated commands with the Worker's user permissions, without sandboxing or
approval prompts. The configured workspace remains the starting directory;
it is **not** a filesystem boundary in this mode.

## Desktop setup on both computers

1. Install/build the candidate containing YOLO support on each computer.
   An older Worker cannot be enabled by a newer Supervisor alone.
2. In Settings > Codex Pair select **YOLO — no sandbox or approvals** under
   Policy ceiling. Save settings and restart the managed Worker if needed.
3. Apply registration and reload Main Codex. The PAIR-owned MCP entry receives
   `--default-task-mode yolo` and `default_tools_approval_mode = "approve"`.
   Other MCP servers and plugins are unchanged.
4. Main's own command execution uses `approval_policy = "never"` and
   `sandbox_mode = "danger-full-access"` in its Codex configuration, or the
   CLI's `--dangerously-bypass-approvals-and-sandbox` invocation flag.
   Existing sessions may need to reload their permission configuration.
5. Confirm `workers.list` advertises `danger-full-access` and `never` for the
   intended Worker. Delegate a harmless temporary-file task and inspect the
   actual task execution policy and terminal result.

MCP tool approval is separate from command approval. The server-specific key
is documented in the [Codex configuration reference](https://developers.openai.com/codex/config-reference/).

## Contract and rollback

`tasks.delegate` accepts `mode: "yolo"`, which requests the protocol pair
`sandbox: "danger-full-access"`, `approval: "never"`. Explicit `read` and
`write` requests keep their sandboxed behavior. With the Desktop YOLO
registration, omitted mode defaults to YOLO; elsewhere it defaults to read.
A Worker not locally opted into YOLO must reject unrestricted requests.
No silent downgrade or fallback to an older Worker is allowed.

PAIR still enforces peer certificate pins, Supervisor allowlists, initial
workspace resolution, concurrency/fencing, deadlines, cancellation, and
artifact-export containment. These controls do not restrict arbitrary
commands' file access once YOLO execution begins.

To revert, select Read-only or Workspace write, save, restart the Worker,
Apply registration, and reload Main. This removes the PAIR auto-approval
setting and YOLO default from the generated registration. Existing running
tasks retain their start-time policy; cancel them before reducing access.

## Acceptance status

- Mac Desktop unit suite: 249 passed. Typecheck, lint (warnings only), service
  contract check, dead-code check and SPDX checks passed.
- Worker and Supervisor race suites and shared protocol tests passed.
  Windows amd64 cross-builds passed; they are not Windows runtime tests.
- Mac arm64 package built with Worker/Supervisor 0.3.0. Existing validation
  profile was configured through the production configuration manager; GUI
  showed YOLO and Worker Ready after launch.
- The normal Mac Main configuration already used `never` and
  `danger-full-access`. A backed-up PAIR-only registration was added with
  the YOLO default and server-specific MCP auto-approval. Other MCP entries
  were preserved.
- A real Main Codex CLI run used that registration without a per-invocation
  override for PAIR approvals. `workers.list`, delegation, status and result
  calls ran without human approval. Omitted mode produced a completed task
  with persisted execution policy `danger-full-access` / `never`.
- The task created `pair-yolo-ok.txt`, exported the declared artifact, and
  returned `PAIR_YOLO_OK` plus a newline. File bytes were independently checked
  on Mac (13 bytes). One premature result query failed while the task was
  still running; polling then returned the completed result.

Windows deployment remains pending. The already-running Windows 0.2.0
Workers cannot accept YOLO and do not expose remote administrative settings.
The Windows local Codex must build/install this branch, preserve the current
pairing/network/workspace settings, select YOLO and Apply registration, then
reload Main and verify both advertised policy and an actual completed task.
Do not infer that its settings changed from the successful Mac test.
