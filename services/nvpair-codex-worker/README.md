<!--
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
-->

# nvpair-codex-worker

The Codex Worker Gateway is the Phase 1 local execution boundary for native
Codex app-server processes. It accepts bounded task envelopes over a loopback
HTTP endpoint, validates the configured workspace, starts `codex app-server`
with the requested restrictive policy, and persists task state in an
append-only JSONL journal.

This binary is not part of the PAIR inference scheduler and is not started by
the Electron broker in Phase 1. It does not listen on a LAN address, advertise
mDNS, accept remote approvals, or retry an unresolved app-server attempt.

## Run

```bash
go build -o nvpair-codex-worker .
./nvpair-codex-worker --workspace-root "$PWD"
```

The default listener is `127.0.0.1:0`; use an explicit `--listen` address when
starting the Supervisor. `--state-root` overrides the per-user
`Nvidia Corporation/Personal AI Router/codex-worker` state directory, and
`--codex-bin` overrides the `codex` executable used for the child process.
`--max-concurrency` sets the number of simultaneously leased workspaces
(default `1`). Non-loopback listener addresses are rejected in Phase 1.

## Protocol

The Phase 1 routes are:

- `GET /v1/worker` — protocol version and local capabilities.
- `POST /v1/tasks` — accept one bounded task request idempotently.
- `GET /v1/tasks/{taskId}` — read compact task state.
- `GET /v1/tasks/{taskId}/events?after={seq}` — read bounded NDJSON events.
- `POST /v1/tasks/{taskId}/cancel` — cancel using the current fencing tuple.
- `GET /v1/tasks/{taskId}/result` — read a validated compact handoff.

There is intentionally no `/approvals` route. A native app-server approval
request is recorded as `approval_required`, denied by the fail-closed adapter,
and returned as `blocked`; an AI-generated message cannot be relayed as a human
approval.

The task journal is synced before a mutation response is acknowledged. On
restart it rebuilds idempotency records, task state, leases, and event
sequences, and uses an OS-held journal lock to prevent two Workers from
mutating one state root. A network failure or unexplained child exit produces
`lost` and does not release a workspace lease or start a second attempt;
explicit fencing is required before reuse.
