<!--
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
-->

# NVIDIA Personal AI Router — Linux install

This tarball is the portable "extract anywhere" backend bundle. The shipped
distribution places its graphical UI alongside this bundle in the same
installation directory. The UI launches `nvpair-ui-broker`, which drives the
API and supervises the workers under `bin/`.

## 1. Extract

```bash
tar xf NVIDIA-Personal-AI-Router-<version>-linux-amd64.tar.gz
cd NVIDIA-Personal-AI-Router-<version>
```

You should end up with this layout:

```
NVIDIA-Personal-AI-Router-<version>/
├── bin/
│   ├── nvpair-ui-broker              # primary entry point (JSON-RPC over stdio / IPC)
│   ├── nvpair-codex-worker            # native Codex execution gateway
│   ├── nvpair-codex-supervisor        # Main Codex local MCP bridge
│   ├── ollama-proxy
│   ├── lmstudio-proxy
│   ├── nvpair-node-info
│   ├── nvpair-node-scanner
│   ├── nvpair-manual-nodes
│   ├── nvpair-workload-manager
│   ├── nvpair-errors
│   ├── nvpair-engine-manager
│   ├── nvpair-node-settings
│   ├── nvpair-cluster-manager
│   └── nvpair-job-scheduler
└── INSTALL.md                     # this file
```

## 2. Run

The bundled UI normally launches the broker from the same installation
directory. For backend-only development or direct API access, run it yourself.
It supervises the other workers (which it expects as siblings in the same
`bin/` directory) and speaks newline-delimited JSON-RPC 2.0 over stdio (or a
Unix socket via `--ipc`):

```bash
./bin/nvpair-ui-broker
```

The bundled UI connects to the broker over this contract. Other clients can use
the same API; see the `nvpair-ui-broker` README for its JSON-RPC surface.

### Native Codex Worker

For a local Worker/Supervisor pair:

```bash
WORKER_TOKEN="$(openssl rand -hex 32)"
./bin/nvpair-codex-worker --workspace-root "$PWD" --listen 127.0.0.1:14324 --auth-token "$WORKER_TOKEN"
./bin/nvpair-codex-supervisor --worker-url http://127.0.0.1:14324 --worker-token "$WORKER_TOKEN"
```

For a paired remote Worker, use TCP 14324 with PAIR's shared cluster
directory. The Worker verifies the Main Supervisor certificate against the
current pin before every request:

```bash
./bin/nvpair-codex-worker --workspace-root /srv/project \
  --listen 0.0.0.0:14324 --cluster-dir "$PAIR_CLUSTER_DIR" \
  --supervisor-allowlist "$MAIN_SUPERVISOR_PRINCIPAL"
```

The Supervisor stays local to Main Codex and can consume a PAIR
`discovery:nodes` snapshot with `--discovery-file`; no fixed-port scan is used.
For boot-time operation, adapt the bundled `nvpair-codex-worker.service`
template, set the
workspace/state/cluster paths, install it for the intended user, and enable it
with `systemctl --user` (or run it under the host's service manager). Do not
run the Worker as root unless that is the deliberate workspace owner.

## 3. Uninstall

Since nothing was installed by a package manager, removal is just deleting the
extracted directory:

```bash
rm -rf /path/to/NVIDIA-Personal-AI-Router-<version>
```

The workers store configuration in `~/.config/Nvidia Corporation/Personal AI Router/`
(manual-node list, log-level preference, cluster identity/pins, etc.). Remove
that directory too if you want a fully clean slate.

## Requirements

- 64-bit Linux on `x86_64`. ARM builds are not produced yet.
- mDNS on UDP 5353. The workers run their own — a custom per-interface responder
  (`nvpair-shared/mdns`) plus a `grandcat/zeroconf` browser (`nvpair-shared/discovery`),
  bound with `SO_REUSEADDR` — and coexist fine with a system responder like
  `avahi-daemon`; no configuration needed.

## Troubleshooting

- **No nodes discovered**: confirm UDP 5353 isn't blocked by your firewall and
  that other machines on the LAN are actually advertising. Run the broker with
  `--log-level debug` to see live discovery logs on stderr.
- **"Bind: address already in use"**: port 11435 is the Ollama proxy port;
  another instance of the proxy or another process is holding it. Find the
  offender with `ss -tlnp | grep 11435` and stop or kill it.

Report issues at the project's tracker. Include the broker's `--log-level debug`
output and your distro / kernel version.
