<!--
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
-->

# nvpair-codex-supervisor

The Codex Supervisor is a local MCP server for Main Codex. It forwards bounded
delegation requests to a loopback `nvpair-codex-worker` and returns compact
task state and handoffs. It contains no model, does not execute shell commands,
and is not PAIR's inference scheduler.

## Run

Start the Worker with an explicit workspace root, then point the Supervisor at
the Worker URL printed by its startup log:

```bash
WORKER_TOKEN="$(openssl rand -hex 32)"
./nvpair-codex-worker --workspace-root "$PWD" --listen 127.0.0.1:14324 --auth-token "$WORKER_TOKEN"
./nvpair-codex-supervisor --worker-url http://127.0.0.1:14324 --worker-token "$WORKER_TOKEN"
```

Phase 1 uses a loopback-only HTTP connection with a required bearer token;
the token is a local process secret and must not be logged or exposed to a
remote host.

Configure the resulting Supervisor executable as a local stdio MCP server for
the Main Codex process. The MCP surface contains `workers.list`,
`tasks.delegate`, `tasks.status`, `tasks.result`, `tasks.cancel`, and the
phase-gated `artifacts.get` placeholder, which returns an explicit unavailable
result until artifact transport is implemented.

There is no approval tool or approval relay. Worker approval events end in
`blocked/approval_required`. Phase 1 also does not advertise mDNS, use PAIR
mTLS, or alter the PAIR scheduler/broker; those integrations are separate
security-gated phases in the design spec.
