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

This binary is not part of the PAIR inference scheduler. It is normally started
explicitly (or by an external service manager) and never accepts remote
approvals or retries an unresolved app-server attempt.

## Run

Loopback mode is the local development path:

```bash
go build -o nvpair-codex-worker .
WORKER_TOKEN="$(openssl rand -hex 32)"
./nvpair-codex-worker --workspace-root "$PWD" --listen 127.0.0.1:14324 --auth-token "$WORKER_TOKEN"
```

The default listener is `127.0.0.1:0`; use an explicit `--listen` address when
starting the Supervisor. `--state-root` overrides the per-user
`Nvidia Corporation/Personal AI Router/codex-worker` state directory, and
`--codex-bin` overrides the `codex` executable used for the child process.
`--max-concurrency` sets the number of simultaneously leased workspaces
(default `1`). `--auth-token` is required in loopback mode and must be shared
with the local Supervisor. `--tool-labels` publishes comma-separated local
labels such as `powershell,cuda,docker` for capability-aware selection. The
Worker rejects non-loopback listeners in this mode.

On Windows, `--codex-bin` accepts native `codex.exe` or the `codex.cmd` /
`codex.ps1` launcher from an npm installation. The Worker resolves npm launchers
to the installed architecture-specific native executable without invoking a
shell. Install optional npm dependencies, or provide the native executable's
full path, if resolution fails. Other script launchers are rejected.

Worker state, isolated Codex authentication, and local credentials use private
Windows ACLs (current user, SYSTEM, and Administrators) or Unix `0700` directories
and `0600` files. Managed configuration and credential readers inspect the actual file handle
and reject access from other identities. Windows protected I/O pins traversal
directories and rejects junctions before reading, writing, or publishing files;
public traversal ancestors are not changed. Existing journals must already be
private with DACL inheritance disabled, because tightening an ACL cannot revoke
previously opened data handles. Protected Windows readers also require disabled
DACL inheritance so later changes to an ancestor cannot expose an open file.
A launcher must use the protected writer before writing its bearer token.
Isolated Windows app-server children explicitly select Codex's `unelevated`
restricted-token sandbox, since their private Codex home omits Main's settings.
Read/write ceilings and approval handling remain enforced. This mode avoids
machine-wide elevated sandbox setup; it has weaker network isolation than
Codex's elevated sandbox and uses the native sandbox's offline controls.
Windows app-server children start suspended and join a kill-on-close job before
running; descendants terminate when their Worker-owned job closes. Journal and
workspace locks retain Windows thread ownership across asynchronous callbacks.

For a paired remote Worker, use the same PAIR cluster directory that owns the
host certificate and pins:

```bash
./nvpair-codex-worker \
  --workspace-root /work/project \
  --state-root "$HOME/.config/Nvidia Corporation/Personal AI Router/codex-worker" \
  --listen 0.0.0.0:14324 \
  --cluster-dir "$PAIR_CLUSTER_DIR" \
  --supervisor-allowlist "$MAIN_SUPERVISOR_PRINCIPAL"
```

Remote mode is HTTPS with the PAIR leaf certificate and exact pinned peer
certificate. It does not accept bearer tokens or fall back to plaintext. The
Worker refreshes the pin set before every request and polls it every two
seconds to cancel active tasks after revocation or same-principal certificate
rotation; the normal controlled integration bound is five seconds.

Managed mode (`--managed-control`) optionally accepts `remoteListen` (an IP
address or wildcard plus a numeric port), `clusterDir` (an absolute path), and
`supervisorAllowlist` (a nonempty JSON array of certificate principals) in its
protected JSON configuration. Supply all three together or omit all three.
Startup requires an admitted PAIR identity and a successful remote bind; failure
does not start a partially available Worker. Port `0` is accepted for ephemeral
listeners; configure a fixed port for remote Supervisor registration.

This adds a second listener using the standalone mTLS authorization and
revocation rules. The pinned loopback TLS endpoint, bearer credential, and local
runtime descriptor keep their existing format. Both listeners execute through
one Worker, sharing its journal, artifact store, active tasks, concurrency limit,
tool labels, workspace policy, and policy ceiling. Managed shutdown and config
replacement stop both listeners and wait for the revocation and descriptor
watchers. Remote endpoint registration belongs to the managing application.

## Protocol

The Phase 1 routes are:

- `GET /v1/worker` — protocol version and local capabilities.
- `POST /v1/tasks` — accept one bounded task request idempotently.
- `GET /v1/tasks/{taskId}` — read compact task state.
- `GET /v1/tasks/{taskId}/events?after={seq}` — read bounded NDJSON events.
- `POST /v1/tasks/{taskId}/turns` — bounded follow-up guarded by the current
  task/attempt/epoch tuple.
- `POST /v1/tasks/{taskId}/cancel` — cancel using the current fencing tuple.
- `GET /v1/tasks/{taskId}/result` — read a validated compact handoff.
- `GET /v1/tasks/{taskId}/artifacts/{artifactId}` — read only a declared,
  digest-verified staged artifact.

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

Artifacts are copied into a per-task directory with an opaque ID and SHA-256
digest. Source paths are relative to the validated workspace, reject traversal
and symlink/reparse components, and are bounded by `--artifact-max-bytes`
(8 MiB by default). Terminal artifacts are pruned after seven days; active and
lost task records remain until an operator resolves them.
