# Design Specification: Native Local Codex on an Agent-Aware PAIR Fabric

**Status:** Superseded by `2026-09-04-codex-supervisor-pair-control-plane.md`
**Date:** 2026-09-04
**Repository:** NVIDIA Personal-AI-Router v0.1.1 (`13b68115fa2c9c1d94f1ead1358f8d5a527cfecf`)

## 1. Decision

Use the existing local Codex CLI as the agent and use PAIR as its inference
fabric. PAIR remains responsible for node discovery, cluster trust, model
inventory, engine health, workload accounting, failover, and model-node
routing. Codex remains responsible for the source tree, shell, tools,
approvals, sessions, and any workspace isolation such as Git worktrees.

This customization is implemented in the existing PAIR repository. It does
not introduce an `xamong-agentd`, a second agent runtime, a task database, a
workflow engine, or a new workspace manager. It also does not require a
GitHub fork. The local clone is the customization workspace.

The first provider is Ollama-compatible local inference because Codex CLI
already supports `--oss --local-provider ollama` and PAIR already owns the
Ollama-compatible local proxy. LM Studio remains on the existing path and is
covered by the same routing contract where the proxy supports it.

## 2. Requirements derived from the supplied conversation

The supplied conversation makes the following boundaries and behaviors
requirements, rather than implementation instructions:

1. Agent execution and source access stay on the machine running Codex. GPU
   workers do not need the repository.
2. Multiple local Codex processes may run concurrently. Their workspaces are
   isolated by Codex/user tooling, preferably with Git worktrees.
3. Agent identity and compute placement are separate. A Codex process is not
   permanently pinned to one GPU node.
4. PAIR should use request context such as role, task class, priority, and
   latency sensitivity when that context is available.
5. Existing PAIR discovery, mTLS, model ownership, failover, workload
   tracking, and engine lifecycle behavior should be reused rather than
   replaced.
6. Agent collaboration, persistent memory, routines, approval policy, and
   Git/worktree management are outside PAIR's responsibility for this scope.

The first four are routing concerns only. The last item prevents the
customization from turning PAIR into an agent-control product.

## 3. Current source to reuse

The implementation will extend these existing paths:

- `services/ollama-proxy`: local Ollama-compatible ingress, model extraction,
  eligibility filtering, candidate ordering, optimistic reservations,
  streaming, and failover.
- `services/lmstudio-proxy`: the corresponding OpenAI-compatible ingress and
  routing behavior for LM Studio.
- `services/nvpair-job-scheduler`: cluster-wide node ranking from pending
  workloads and telemetry pressure.
- `services/shared/schedulerwire`: the existing scheduler-to-proxy priority
  snapshot contract.
- `services/nvpair-ui-broker`: authoritative workload/discovery relay and
  existing client lifecycle. No second broker is introduced.
- `services/nvpair-engine-manager`: existing engine/model lifecycle and
  inventory instead of a new model registry.

The current scheduler's model eligibility and global node ranking remain the
source of truth. The customization adds a request-local preference layer at
candidate ordering time; it does not duplicate discovery or invent a second
cluster membership system.

## 4. Proposed request contract

The model request body remains unchanged so existing Ollama and LM Studio
clients continue to work. Optional routing hints are carried in HTTP headers
on the local loopback request:

| Header | Meaning | Validation |
| --- | --- | --- |
| `X-PAIR-Agent-ID` | Stable identifier for the local Codex process or profile | Printable token, bounded length |
| `X-PAIR-Agent-Role` | `planner`, `coder`, `reviewer`, `tester`, or custom role | Bounded token |
| `X-PAIR-Task-Class` | `interactive`, `background`, `batch`, or `review` | Known enum; unknown values ignored |
| `X-PAIR-Priority` | Relative urgency | Integer `0..100`; absent means default |
| `X-PAIR-Deadline` | Optional RFC3339 deadline | Invalid value ignored |
| `X-PAIR-Context-Tokens` | Approximate input context size | Non-negative bounded integer |
| `X-PAIR-Expected-Output-Tokens` | Approximate output size | Non-negative bounded integer |

Hints are advisory. Invalid or absent hints never reject an otherwise valid
model request. Hints are not used as authentication, authorization, or a
request-level node pin. The local proxy remains loopback-only and peer
requests remain protected by the existing mTLS trust model.

The first implementation consumes `task class` and `priority` for routing.
The other fields are parsed and retained in the request-local structure so
the contract can grow without changing the model body; they do not affect
placement until PAIR has reliable node capability and latency telemetry for
them.

## 5. Routing behavior

Candidate construction keeps the existing order of responsibility:

1. Parse the model using the existing proxy-specific request parser.
2. Resolve model owners from the existing inventory.
3. Apply the existing manual node selection semantics, if configured.
4. Remove untrusted, unavailable, self-loop, and unreachable candidates using
   the existing guards.
5. Use the existing scheduler priority snapshot as the baseline order.
6. Apply an optional request-local preference adjustment for the hints.
7. Preserve deterministic stable-ID tie breaking and the existing failover
   sequence.

The preference adjustment is intentionally conservative:

- `interactive` requests prefer the best currently ranked node and receive a
  bounded urgency bonus from `priority`.
- `background` requests avoid displacing interactive reservations when there
  is an equivalent eligible candidate.
- `batch` and `review` use the baseline scheduler order unless priority is
  explicitly provided.
- Manual node selection remains authoritative and bypasses the advisory
  preference layer, matching current behavior.
- A request without routing headers produces the same candidate order as the
  current release.

No claim is made that PAIR pools VRAM, shards a model, or schedules tokens
across machines. Each selected engine still serves the complete request on
one node.

## 6. Codex integration

Codex itself is not modified. The repository will provide a documented local
profile/launcher that:

1. verifies the PAIR local endpoint and selected model are reachable;
2. sets the Codex OSS provider to the PAIR local Ollama-compatible endpoint;
3. starts Codex in the caller's current workspace;
4. optionally supplies the routing headers for a named local Codex profile;
5. leaves Codex's normal tool execution, approvals, session persistence, and
   Git behavior unchanged.

The launcher is an integration convenience, not an agent daemon. Its process
lifetime is the Codex session lifetime and it does not own tasks or files.
The design must verify the installed Codex CLI's supported endpoint and
header configuration before documenting a command as guaranteed. If custom
headers cannot be supplied by the installed CLI, model routing still works
without them and the agent-aware preference path remains an opt-in API for
other compatible clients.

## 7. Observability and privacy

Existing workload events continue to report model, engine, node, timing, and
status through the existing broker path. This scope does not add prompt or
response bodies to logs, events, or persisted state. Agent ID, role, task
class, and priority are metadata only and may be included in diagnostic
metadata where the existing contract permits it; secrets and model content
must never be included.

Routing decisions should be testable through structured in-memory decision
inputs. Production logs may report the selected node and sanitized hint
values, subject to current PAIR logging rules. Header values must not be
forwarded to a node as trusted identity claims; remote proxies may use them
only as advisory request context.

## 8. Compatibility and failure rules

- Standard Ollama and LM Studio clients remain accepted without new headers.
- Existing `/api/*` Ollama and `/v1/*` LM Studio/OpenAI-compatible paths keep
  their current response and streaming behavior.
- A malformed hint is ignored, not surfaced as a model API error.
- If the hint-aware policy cannot make a decision, the existing scheduler
  order is used.
- If no eligible model owner exists, the existing proxy error is returned.
- If a chosen node fails, the existing failover policy is used.
- Node trust, certificate pinning, loopback ingress, and manual pin semantics
  are not weakened.
- The broker remains a single-client local control channel. The integration
  must not start a competing broker on a host already owned by the desktop or
  TUI client.

## 9. Implementation boundary

The implementation is complete for this design when it contains:

1. a shared, unit-tested routing-hint parser and bounded validation;
2. a shared, unit-tested request preference policy with a no-hints identity
   path;
3. Ollama proxy integration tests proving metadata-aware ordering, manual pin
   precedence, failover preservation, and backward compatibility;
4. LM Studio proxy integration tests covering the same contract where its
   request path supports the hint headers;
5. Codex local endpoint compatibility checks and a launcher/profile example;
6. focused service tests plus the relevant full Go test suites;
7. documentation that clearly states the workspace/inference separation and
   the single-broker constraint.

The following are deliberately excluded from this implementation: persistent
agent records, agent-to-agent messaging, memory, routines, task DAGs,
workspace provisioning, cloud fallback, new inference engines, UI redesign,
model sharding, and changes to Codex's executable.

## 10. Acceptance criteria

The design is validated when all of these are true:

- A local Codex session can use a PAIR Ollama-compatible endpoint with a
  selected local or remote model.
- Two concurrent Codex sessions can issue independent requests through the
  same PAIR node without process-global selection races.
- A request with no hints follows the current candidate order.
- An eligible request with `interactive` and higher priority is ordered ahead
  of an equivalent background request without violating model ownership,
  trust, manual pin, or failover rules.
- The model request body and streamed response remain compatible with the
  existing clients.
- No prompt, response, credential, or source file is sent to PAIR worker
  nodes except as part of the model inference request itself.
- All changes stay in the local clone; no GitHub fork is created.
