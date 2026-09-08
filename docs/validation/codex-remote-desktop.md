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

## Desktop candidate acceptance, 2026-09-08

Candidate implementation: `bd3cd7d` on `codex/desktop-remote-integration`.
This is not a declaration that both platforms are fully accepted.

- Mac packaged GUI saved incoming/outgoing remote settings, applied MCP
  registration to an isolated Main profile, and started its managed Worker.
  The GUI reported Ready; OS inspection confirmed that this same managed
  Worker listened on the selected Tailscale address at port 14324. Incoming
  access was explicitly enabled by the user for the paired Windows principal
  only, with a temporary Git workspace and a read-only policy ceiling.
- Desktop unit suite: 45 files, 245 tests passed using
  `GOFLAGS=-buildvcs=false npm run test:unit`. Without that environment flag,
  the local parent SVN checkout conflicts with the nested Git checkout during
  the test broker's Go build; that run failed before 17 tests executed.
- Worker and Supervisor `GOFLAGS=-buildvcs=false go test -race ./...` passed
  on Mac. Windows cross-compilation is not native Windows execution.
- Mac arm64 package build passed. Signing/notarization are outside the
  personal-use acceptance scope.

### Initial blocked checks and observed limitations

The following records describe the initial run. The follow-up below supersedes
the Windows GUI and approval-client gaps. Windows-local reverse execution is
recorded below; the everyday Codex app UI remains a separate gap.

1. Main Codex launched from the saved isolated MCP configuration could not
   invoke `workers.list`: it reported that the MCP call required approval,
   while its approval policy was `never`. The CLI exited zero, but the
   requested end-to-end task did **not** pass. Validate in a Main session
   where the user can approve the tool; do not disable safeguards to turn
   this failure into a pass.
2. Direct remote dispatch of the candidate clone/native Go tests to Windows
   reached `blocked` / `approval_required` (task
   `task-27e0128d847071fecc951644`). No native test success is inferred from
   that result. Candidate Windows GUI lifecycle/remote-listener acceptance
   also remains unverified; the old standalone Worker is not that candidate.
3. After the isolated Main process exited, Desktop correctly showed Waiting
   for Main, but its task-metadata error still displayed a stale management
   socket `ECONNREFUSED`. This is an observed diagnostic/UI limitation, not
   evidence that the managed Worker stopped.
4. Windows-to-Mac verification was dispatched directly through the remote
   Worker (task `task-3262a863866336a14b4fe6e9`). Its returned handoff was
   blocked: the nested Windows Supervisor exited 1 with `open task index:
   Access is denied` in its workspace state-root, before initialization.
   A validation harness was created, but no reverse delegation occurred.
   This result does not establish the exact underlying Windows permission
   cause; repeat from the Windows local Codex context with its normal user
   approval flow, not by weakening the remote sandbox.

The test Mac profile remains isolated from the user's normal Main Codex
configuration. Remote reception can be stopped with Disable worker, or by
clearing both incoming address and allowlist and saving the settings.

## Windows follow-up and Mac-to-GUI execution

The Windows operator reported verification of candidate `ae36de3`:

- Windows package build and actual GUI launch passed.
- Remote settings survived a clean exit/relaunch and the Worker became Ready.
- Main Codex requested real MCP approval; one-time approval allowed
  `workers.list` to return both local and Mac Workers in the validation client.
- Packaged native E2E: two passed, one macOS-only test skipped.
- Unauthenticated remote access was rejected.

The operator identified the new GUI Worker as PID 75092 on port 14325 and
left the older Workers running. This report is Windows-side evidence, not
a claim that the Mac agent independently inspected the Windows GUI.

The Mac agent then directly verified the new port 14325 endpoint with pinned
mTLS. `workers.list` returned Windows amd64 Worker 0.2.0 with read-only policy.
Task `task-0d8d98ddce707fce1c362875` completed and its handoff reported:

- `Get-Location; git status --short` exited zero.
- Working directory was the new `pair-candidate-gui-5v1cUC/workspace` temporary
  Windows GUI workspace, not the old standalone Worker's workspace.
- Git status was clean and the result included `PAIR_MAC_WINDOWS_GUI_OK`.

This closes Mac-to-new-Windows-Desktop-managed read-only task execution.
The Mac check used a Supervisor JSON-RPC test harness, not the everyday
Codex app's approval UI. The everyday Codex app approval interaction remains
a distinct outstanding check. No production Main configuration
or existing Worker process was changed by this follow-up.

## Windows-local reverse execution and Main-exit display

Windows built the candidate Supervisor natively at `ae36de3`, initialized it
with the existing PAIR cluster and a separate temporary state directory, and
queried the paired Mac Worker over mTLS. Read-only task
`task-86c1fadaa1744233e06769c1` completed with a child identity and no Supervisor
stderr output. This closes Windows-to-Mac execution through a Supervisor test
client. It does not establish the everyday Codex app approval UI.

The original initialization harness also succeeded from the Windows-local
user context using its original executable and state directory. The remote
sandbox's `Access is denied` was not reproduced; its exact permission cause
remains undetermined. No ACL or sandbox restriction was weakened.

Windows native Worker and Supervisor tests passed. Desktop tests at that
candidate passed 243 tests with two skips, and all three typecheck targets
passed. Windows race instrumentation was unavailable (CGO disabled and no
C compiler on PATH); Mac race evidence remains separate.

The renderer now checks Main's connection state before requesting task
metadata and rechecks after a failed request to handle Main exiting during
the request. Waiting/unregistered Main clears stale task rows and socket
errors; failures while Main remains connected are still shown. Regression
tests cover both exit timings and preservation of connected-state errors.

Windows validation of this display fix: 246 Desktop tests passed, two skipped;
all typecheck targets, lint, dead-code and service-contract checks passed.
The rebuilt packaged GUI showed Worker Ready and Waiting for Main without
stale socket errors after refreshing the isolated MCP registration for the
new package path. The verified GUI Worker now uses PID 85324 on the same
100.70.48.68:14325 endpoint and original candidate workspace. Existing
Workers 39384 and 3432 remained running.
