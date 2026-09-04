<!--
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
-->

# nvpair-codex-supervisor

The Codex Supervisor is a local MCP server for Main Codex. It forwards bounded
delegation requests to native `nvpair-codex-worker` processes and returns
compact task state, handoffs, and explicitly requested artifacts. It contains
no model, does not execute shell commands, and is not PAIR's inference
scheduler.

## Run

For one local machine, start the Worker and then configure the Supervisor as a
stdio MCP server for Main Codex:

```bash
WORKER_TOKEN="$(openssl rand -hex 32)"
./nvpair-codex-worker --workspace-root "$PWD" --listen 127.0.0.1:14324 --auth-token "$WORKER_TOKEN"
./nvpair-codex-supervisor --worker-url http://127.0.0.1:14324 --worker-token "$WORKER_TOKEN"
```

Phase 1 uses a loopback-only HTTP connection with a required bearer token;
the token is a local process secret and must not be logged or exposed to a
remote host.

Configure the Supervisor executable as a local stdio MCP server for the Main
Codex process. The MCP surface contains `workers.list`,
`tasks.delegate`, `tasks.status`, `tasks.result`, `tasks.cancel`, and the
`artifacts.get`.

For paired remote Workers, the Worker advertises `cw=14324` in PAIR's existing
`_nvpair-node._tcp` TXT record. The Supervisor consumes a PAIR
`discovery:nodes` JSON snapshot, uses the advertised `cw` port and ranked IP
candidates, and authenticates each candidate with the current pinned mTLS
principal. No fixed-port scan is used:

```bash
./nvpair-codex-supervisor \
  --cluster-dir "$PAIR_CLUSTER_DIR" \
  --discovery-file "$PAIR_DISCOVERY_NODES_JSON"
```

An explicit endpoint list is available for controlled setup and diagnostics:

```bash
./nvpair-codex-supervisor \
  --cluster-dir "$PAIR_CLUSTER_DIR" \
  --worker-endpoints worker-principal=https://192.0.2.20:14324
```

The Supervisor performs deterministic capability filtering (OS, architecture,
workspace mode, tool labels, protocol version, and available slots), then
prefers locality/capacity and breaks ties by Worker ID. It never calls
`nvpair-job-scheduler` for this decision.

There is no approval tool or approval relay. Worker approval events end in
`blocked/approval_required`; only a human at the Worker host can continue.
The Supervisor itself remains local stdio-only and is not exposed by the
Windows firewall.
