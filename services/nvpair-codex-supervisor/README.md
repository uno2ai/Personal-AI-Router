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

`tasks.delegate` accepts `mode: "yolo"`, mapping it to a `write` workspace,
`danger-full-access` sandbox, and `never` approval. It selects only Workers
that explicitly advertise all three capabilities, even when `workerId` is
specified. Older Workers and partially matching capabilities cannot receive a
YOLO task; no fallback to read/write execution occurs.

`--default-task-mode read|write|yolo` controls delegation when `mode` is omitted.
The default is `read`. Explicit `read` or `write` requests keep their existing
execution settings even when the configured default is `yolo`. Approval can be
omitted to use the mode's paired value: `local-only` for read/write and `never`
for YOLO. Conflicting approval values are rejected. The managing application
owns Main's shell and MCP approval configuration separately.

There is no approval tool or approval relay. Worker approval events end in
`blocked/approval_required`; only a human at the Worker host can continue.
The Supervisor itself remains local stdio-only and is not exposed by the
Windows firewall.

## Local protected state on Windows

The local Worker credential must permit access only to the current user, SYSTEM,
and Administrators. The Supervisor validates ACLs on the actual opened file
handle and retains its directory handles while reading, rather than checking a
mutable path and reopening it. Broad, missing, or inheritance-enabled DACLs and
reparse traversal are rejected. Disabling DACL inheritance prevents a later
ancestor ACL change from exposing an already-open file. New task-index files are private at creation; existing broad or
hard-linked index files are rejected before append. Unix group/world checks
remain enforced and existing Unix traversal parents are not changed.
