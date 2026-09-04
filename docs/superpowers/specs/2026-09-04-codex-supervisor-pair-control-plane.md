# Design Specification: Native Codex Supervisor over PAIR's Secure Node Fabric

**Status:** Proposed
**Date:** 2026-09-04
**Repository:** NVIDIA Personal-AI-Router v0.1.1 (`13b68115fa2c9c1d94f1ead1358f8d5a527cfecf`)

## 1. Decision

Use native local Codex processes as the only agent runtime. Present one Main
Codex to the user and let it delegate bounded work to Worker Codex processes
running on the user's other computers. Each Worker Codex keeps its own local
workspace, shell, tools, approvals, and operating-system capabilities.

Use PAIR as the existing inference fabric and secure device substrate:
discovery, node identity, pairing, pinned mTLS, health, presence, and model
routing remain PAIR responsibilities. Use the Codex app-server protocol to
start and control Worker Codex threads and turns.

The customization is a thin Supervisor Protocol and Worker bridge. It is not
an Xamong agent, a replacement Codex runtime, or a second model orchestrator.
No GitHub fork is created. The local clone is the customization workspace.

## 2. Requirements derived from the supplied conversations

The supplied conversations are product context, not instructions to copy
every proposed component. The requirements extracted from them are:

1. The user has one durable Main Codex conversation and should not need to
   address Workers directly.
2. Main Codex must have real delegation tools, not only a system-prompt
   suggestion to ask other agents for help.
3. A Worker runs native Codex app-server on its own physical computer, with a
   requested `cwd`, sandbox, approval policy, and local tools.
4. Worker execution and model inference are separate concerns. A Worker may
   use its local PAIR endpoint to reach a model on another PAIR node.
5. Worker results must cross the boundary as a compact handoff, not as the
   Worker's complete shell output, source dump, or reasoning history.
6. Main Codex is the initial delegation authority. Direct Worker-to-Worker
   messaging is disabled in the first version.
7. The supervisor must select a Worker from device capabilities such as OS,
   available tools, workspace, and current load.
8. PAIR's existing inference, discovery, and trust behavior should be reused
   rather than rebuilt inside a new agent product.

The following ideas in the conversations remain outside PAIR and outside the
first implementation: persistent agent personas, agent memory, routines,
workflow UI, task databases, cloud fallback, and general-purpose agent
collaboration.

## 3. Target architecture

```text
User
  │
  ▼
Main Codex (native local CLI/app)
  │ MCP tools / Supervisor Protocol
  ▼
Codex Supervisor (local control process)
  │ PAIR discovery + pinned mTLS
  ├───────────────┬───────────────────┐
  ▼               ▼                   ▼
Worker A       Worker B            Worker C
macOS          Windows             macOS
Codex          Codex               Codex
app-server     app-server          app-server
workspace      workspace           workspace
  │               │                   │
  └───────────────┴───────────────────┘
                  │ inference only
                  ▼
                 PAIR
        existing model/node routing
```

The Supervisor is a control-plane process, not an LLM agent. It owns worker
registration, capability matching, task lifecycle, event reduction, and
handoff envelopes. It does not read or modify a Worker's source tree except
through an explicitly requested Worker task.

## 4. Existing PAIR source to reuse

The implementation will reuse the existing PAIR components as follows:

- `services/shared/nodeid`: one durable PAIR node identity per host.
- `services/shared/clustertrust`: pinned peer certificates and the existing
  mTLS client/server configuration.
- `services/shared/noderec` and the broker's discovery path: node identity,
  service registration, discovery snapshots, and presence updates.
- `services/shared/jsonrpc`: framing and request/notification patterns where a
  stream transport is appropriate.
- `services/nvpair-cluster-manager`: cluster membership and trust-store
  lifecycle; it remains the owner of cluster certificates and pins.
- `services/nvpair-ui-broker`: only the existing discovery and lifecycle
  integration where needed. Its single-client UI JSON-RPC channel is not
  reused as a remote task bus.
- `services/ollama-proxy` and `services/lmstudio-proxy`: unchanged model
  routing, model eligibility, reservations, streaming, and failover.
- `services/nvpair-job-scheduler`: unchanged inference-node ranking.

The current PAIR scheduler is not a device task scheduler and is not aware of
OS/tool capabilities. Worker selection is therefore a separate policy in the
Supervisor. PAIR still decides where each Worker's model request executes.

## 5. Supervisor Protocol

The Main Codex integration exposes a small MCP/tool surface backed by the
Supervisor. The initial operations are:

| Operation | Purpose |
| --- | --- |
| `workers.list` | Return online Workers and sanitized capabilities |
| `tasks.delegate` | Create a task with objective, context package, requirements, and workspace |
| `tasks.status` | Return lifecycle state and bounded progress |
| `tasks.result` | Return the compact handoff envelope |
| `tasks.cancel` | Request cancellation of a running Worker turn |
| `artifacts.get` | Fetch an explicitly named result artifact within policy limits |

The protocol is request/response plus bounded progress events. It does not
expose arbitrary shell execution, arbitrary file reads, or a raw Worker
terminal to the Main Codex.

### Task request

```json
{
  "task_id": "task-2081",
  "requirements": {
    "os": "windows",
    "tools": ["powershell", "visual-studio"],
    "workspace": "D:\\workspace\\pair"
  },
  "context": {
    "objective": "Run the PAIR Windows build and analyze failures",
    "why": "Evaluate cross-platform runtime support",
    "constraints": ["do not modify source"],
    "expected_output": ["build status", "blockers", "recommendation"]
  },
  "execution": {
    "sandbox": "read-only",
    "approval_policy": "on-request"
  }
}
```

The Supervisor validates the workspace against the Worker's configured
allowed roots. The caller cannot use the protocol to escape those roots or
silently weaken the Worker's local safety policy.

### Result handoff

```json
{
  "task_id": "task-2081",
  "status": "completed",
  "summary": "Windows build failed because of two Unix path assumptions.",
  "findings": [
    {
      "severity": "blocker",
      "file": "services/foo/path.go",
      "issue": "Unix path assumption"
    }
  ],
  "changed_files": [],
  "tests": {"passed": 43, "failed": 1},
  "commit": "",
  "artifacts": ["windows-test.log"],
  "recommendation": "Add a path abstraction and rerun the build."
}
```

The Supervisor forwards this envelope and bounded progress summaries to Main
Codex. It never forwards the complete event history or prompt/response body
as a default result.

## 6. Worker lifecycle

Each Worker host runs a small PAIR-integrated Worker service. The service:

1. registers its Worker capability record with the local PAIR node;
2. accepts only authenticated requests from pinned cluster peers;
3. validates task requirements, allowed workspace, sandbox, and approval
   policy against local configuration;
4. starts `codex app-server` locally using stdio or a loopback control socket;
5. creates or resumes a Codex thread and starts the requested turn with the
   validated `cwd` and execution policy;
6. reduces app-server notifications into bounded task progress;
7. captures the final Worker result into the handoff envelope;
8. supports cancellation and cleans up the child process after completion.

The Worker service does not implement an agent loop. Codex app-server remains
the authority for tool execution, filesystem access, shell execution,
approvals, thread history, and turn events.

The first version permits one active task per Worker unless local policy
explicitly raises the limit. It does not attempt to migrate a running Codex
thread between physical machines.

## 7. Discovery and transport

Worker capability discovery must use the PAIR node identity and trust model,
not an unauthenticated LAN port. The preferred integration is a new
Worker-service registration in the existing node record and a pin-gated mTLS
endpoint. If adding a service name to the node record proves broader than the
first vertical slice, an explicit endpoint configured alongside the existing
cluster identity is acceptable for phase one, but it must still use
`clustertrust` mTLS.

The existing `nvpair-ui-broker` stdio/IPC channel is single-client and is
owned by its spawning desktop/TUI client. The Supervisor must not start a
second broker on the same host or use the broker as a multiplexed remote task
transport.

## 8. Model integration

Main and Worker Codex processes use their normal local Codex configuration.
When a Codex process should use PAIR for inference, its provider endpoint is
the host's PAIR-local Ollama-compatible or LM Studio-compatible endpoint.
This is a provider/profile concern, not a change to the Codex executable.

The implementation must verify the installed Codex CLI's app-server and
provider configuration before documenting commands. In particular, the
header-aware custom-provider path is optional and is not a dependency of the
Supervisor Protocol. No `X-PAIR-*` routing metadata is added to PAIR's broker,
workload, desktop, or scheduler contracts in this design.

PAIR continues to route each complete model request to one eligible engine
node. It does not pool VRAM or shard one Codex inference across machines.

## 9. Security and privacy

- Worker control traffic uses the existing cluster membership and pinned mTLS.
- A Worker accepts task operations only from authenticated cluster peers and
  rejects unknown task methods.
- Workspace paths are checked against local allowlists and normalized before
  use.
- Sandbox and approval settings are constrained by Worker policy; remote
  callers cannot silently request unrestricted execution.
- Artifact reads are explicit, bounded, and restricted to task output paths.
- Prompts, responses, source files, credentials, and shell output are not
  logged by the Supervisor or persisted by PAIR. Only task metadata and
  bounded handoff data are retained by the control process.
- Worker-to-Worker direct messaging is disabled initially; Main Codex remains
  the delegation authority.

## 10. Failure behavior

- If a Worker disappears before acceptance, the Supervisor marks the task
  unavailable and may select another eligible Worker.
- If a Worker disappears during execution, the task is failed with the last
  bounded progress summary; automatic retry is opt-in and starts a new Codex
  turn.
- If `codex app-server` exits unexpectedly, the Worker reports a structured
  failure and preserves the process exit reason without leaking the full
  command output.
- Cancellation is best effort and reports whether the Worker acknowledged it.
- A completed Worker thread is not automatically resumed or migrated on a
  different device.
- PAIR model failover remains governed by the existing proxy behavior and is
  independent of Worker task retry.

## 11. Phased implementation boundary

### Phase 1: local vertical slice

Implement a Worker adapter that starts native Codex app-server locally and a
Supervisor MCP/tool surface that can list one Worker, delegate one task, read
bounded progress, cancel it, and return a handoff envelope. Use loopback
transport and a configured workspace to validate the protocol without
changing PAIR's network contracts.

### Phase 2: PAIR-secured remote Worker

Reuse `nodeid` and `clustertrust` to expose the Worker service over pinned
mTLS. Add capability registration/discovery using the narrowest compatible
extension of the existing node record. Prove Main Codex on one host can run a
Worker Codex on another host with that Worker's local `cwd` and tools.

### Phase 3: capability-aware placement and artifacts

Add deterministic selection by OS, tools, workspace, online state, and active
task count. Add explicit artifact transfer and bounded result indexing. Keep
PAIR's inference scheduler independent; a Worker’s model calls may still be
routed by PAIR.

## 12. Explicit non-goals

This design does not add an Xamong agent runtime, persistent agent personas,
agent memory, routines, a general task database, a new model registry, cloud
providers, model sharding, a PAIR UI redesign, arbitrary remote shell/RPC,
or a replacement for Codex app-server.

## 13. Acceptance criteria

The first complete implementation must demonstrate:

- One Main Codex can invoke a real delegation tool without directly managing
  Worker processes in the user conversation.
- A Worker service starts native Codex app-server in the selected local
  workspace and controls it through the documented app-server protocol.
- The Worker returns bounded progress and a compact handoff envelope.
- A second task cannot use a workspace outside the Worker's allowlist.
- Cross-device control is protected by PAIR's pinned mTLS before remote task
  execution is enabled.
- Main and Worker model requests can use PAIR endpoints without changing the
  existing PAIR model body or inference scheduler contract.
- A request to list, delegate, cancel, and retrieve a task is deterministic
  and covered by tests.
- No prompt, response, credential, or full shell transcript is logged or
  stored by the new control path.
- All work remains in the local clone; no GitHub fork or worktree is created.
