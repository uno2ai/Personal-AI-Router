<!--
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
-->

# Personal Mac and Windows remote Desktop integration

## Scope

Connect the existing Desktop-owned Worker and Main MCP registration across the
user's paired Mac arm64 and Windows x64 computers. Different locations use a
reachable private VPN address. Static peer endpoints are intentional because
mDNS is not assumed to cross the VPN. Signing and other platforms are excluded.

## Design

- Persist a network block in the protected Desktop configuration. Existing
  schema-1 files migrate to disabled remote access. The renderer sees connection
  metadata only, not certificates, keys or local bearer credentials.
- Keep the existing loopback pinned-TLS listener and local runtime descriptor.
  Opt-in remote listening adds a second listener to the same managed Worker,
  sharing its store, concurrency limit, workspace policy and artifact store.
- Remote requests require PAIR's existing pinned mTLS identity and an explicit
  Supervisor allowlist. Remote startup fails closed if unpaired or unable to
  bind. Remote listener and revocation watcher share the Worker's lifecycle.
- Main MCP registration retains local routing and adds configured peer endpoints
  with the existing cluster directory. Configuration drift prompts re-registration
  and Main reload; configuration files are not presented as proof of a live link.
- UI edits survive Refresh and saved values return on re-entry. Saving remote
  settings uses the existing configuration/restart path.

## Implementation and verification

1. Implement and test managed dual listeners, authorization, policy and shutdown.
2. Wire Desktop storage, UI, managed config and MCP registration; test migration,
   unsafe endpoint rejection and persisted-to-runtime mapping.
3. Review both changes and address concrete findings; build macOS package and
   verify real GUI settings and registered Supervisor against the Windows peer.
4. Run native Windows checks through the paired Worker where sandbox scope permits.
5. Record cross-device read, write, artifact integrity, cancellation, restart and
   controlled connection-loss evidence; keep unexecuted checks explicit.

## Operational boundaries

Remote task execution is not a general remote desktop/admin session. It cannot
silently reconfigure firewalls, bypass local approval, or replace its own owner.
An independently started diagnostic Worker does not prove Desktop lifecycle
integration; acceptance must distinguish these paths.
