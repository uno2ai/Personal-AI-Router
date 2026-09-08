<!--
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
-->

# Personal Mac and Windows remote Codex

## Setup

1. Connect both computers to the same private VPN when they are at different
   locations. Use reachable VPN IPs; mDNS across the VPN is not required.
2. In PAIR, Add node using the other computer's IP and accept the PIN locally.
3. In Settings > Codex Pair, configure the Worker workspace, Codex executable
   and policy ceiling. Remote work uses this same workspace and policy.
4. Set the existing PAIR cluster directory: macOS uses
   `~/Library/Application Support/Nvidia Corporation/Personal AI Router/cluster`;
   Windows uses `%LOCALAPPDATA%\Nvidia Corporation\Personal AI Router\cluster`.
   Expand these to absolute paths in the input. Do not create new certificates
   or copy private keys between hosts.
5. On a computer accepting work, enter its VPN `IP:14324` and the paired Main
   computer's certificate principal in Allowed paired Supervisor IDs. Enable
   the Worker. The existing local listener remains available.
6. On Main's computer, enter `peer-principal=https://peer-IP:14324` under Remote
   Workers to use. Save, Apply registration, and reload Main Codex. Repeat in
   the reverse direction only if both computers should accept remote work.

Peer IDs are the public certificate principals established during pairing,
not friendly display names. An address alone does not grant access. Remote
connections require exact peer certificate pins and the explicit allowlist.
An empty listen address plus empty allowlist disables incoming remote access.
Changing peer endpoints requires re-registration and Main reload. Desktop
checks the registration revision reported by a live Supervisor before showing
Connected. Main must stay running for task metadata controls to work.

Use the IP of the intended private interface, rather than a wildcard address.
Do not expose the Worker using public router port forwarding.

## Cross-device runtime evidence, 2026-09-08

Actual Mac arm64 to Windows x64 over Tailscale, using a separately started
Windows Worker, established:

- PAIR PIN pairing and pinned mTLS capability query succeeded.
- Read-only remote task completed and returned a clean Git status.
- Running task cancellation reached cancelled and restored the available slot;
  Windows-side inspection confirmed the child exited and managed Worker survived.
- Supervisor restart preserved completed/cancelled states and event sequences.
- Windows Worker restart preserved previous task states and accepted a new task
  that completed successfully.
- Write task created a 24-byte test artifact. `artifacts.get` returned the exact
  requested bytes and matching SHA-256, independently recomputed on Mac.
- `scripts/verify-codex-remote-reconnect.mjs` interrupted only the test's TCP
  connections while a real task ran. Status failed during the interruption;
  recovery completed with the same attempt and child identity. No VPN or
  firewall configuration was changed for this check.

These results establish the remote service path; they do not themselves prove
the new Desktop-managed remote listener on both operating systems. Additional
Desktop acceptance is recorded separately below when executed.
