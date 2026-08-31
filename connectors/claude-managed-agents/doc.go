// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package claudemanagedagents is a READ-ONLY observation connector for the Claude
// Managed Agents (CMA) control plane — the rich, persisted resource surface that is
// ORTHOGONAL to A2A (connectors/a2a) and MCP governance (connectors/mcp), and which a
// "Anthropic-first" control plane must inventory, audit and (where governed) act on:
// Vaults + MCP credentials, Memory Stores with their immutable memory-version audit +
// redaction, sessions (mounts, vault use, outcome verdicts) and their multi-agent
// threads, agent definitions (tools[] permission policies, skills, multiagent roster),
// the self-hosted work queue, the Dreams memory-curation jobs (research preview), and
// the signed webhooks that are — for a self-hosted product — the native observability
// path that needs no held-open SSE stream (C1-C5 CUR-3).
//
// It OBSERVES, it does not ACT (docs/SECURITY-HARDENING.md). Every API call is a GET; it never creates,
// rotates or archives a vault credential, never writes a memory, never posts a
// tool-confirmation, never creates/cancels a dream, and never executes work-queue
// items. The one privileged surface a self-hosted control plane MUST terminate — the
// inbound signed webhook — is built FAIL-CLOSED (webhook.go): an unsigned,
// wrongly-signed, stale or replayed delivery is rejected and produces no observation.
//
// PERMITTED vs OBSERVED. The connector emits BOTH sides of the access-map
// least-privilege diff, distinguished by the edge's SignalSource:
//
//   - model.SignalPolicy (PERMITTED — declared grants): a vault credential's binding to
//     its MCP server (the credential identity may reach the server), an agent's
//     declared tools[] expanded per toolset (built-in enum / explicit MCP tool configs
//     / custom tools, with their always_allow|always_ask permission_policy), an agent's
//     skills[] attachment, and the multiagent roster (which sub-agents a coordinator
//     may spawn). A grant never observed in use surfaces as an unused grant —
//     over-provisioning made visible.
//   - model.SignalCMA (OBSERVED — facts): workspace inventory carriers (vault,
//     credential, memory store, environment, dream), session memory-store mounts with
//     their read_only/read_write access mode, session vault use, environment→session
//     work execution, session→thread spawns, and the Dreams provenance (the pipeline
//     session read the input store + transcripts and wrote the output store).
//
// model.FindingReport carries the governance/posture facts: a credential that cannot
// refresh (vault_credential.refresh_failed), a workspace-scoped vault as a
// lateral-movement surface, a read_write memory mount as a prompt-injection write
// target, an immutable memory-version redaction as EVIDENCE-of-erasure, an always_ask
// tool-confirmation pending a human (recovered from the session EVENT list — the
// session resource carries no stop_reason — and routed to the HITL bridge by the
// composition root; the ANT2-14 queue), a TERMINAL outcome-grader verdict, an unpinned
// (version "latest") custom Skill, a work-queue backlog / no-workers-polling liveness
// gap — and the Dreams admission surface (below). A terminal dream's token usage is
// emitted as a model.CostSample (CostType "dream", unpriced/estimated).
//
// DREAMS (research preview, GATED — dreams.go). A dream is an ASYNC MEMORY MUTATION:
// it reads one memory store + 1–100 past sessions and produces a NEW output store (the
// inputs are never modified). The connector treats it as high-risk by construction:
// every dream is inventoried with full provenance; every OUTPUT store is UNTRUSTED
// until a human admits it (deny-closed: the operator records the governed
// approval in admitted_dream_outputs; everything else stays pending-admission); an
// unadmitted output observed ATTACHED to a session is a HIGH drift finding. The
// cryptographic integrity of the store's content is the contract — this connector
// supplies the admission gate and the use case, not the crypto.
//
// HONEST DEGRADES (verified against platform.claude.com/docs/en/managed-agents/* and
// the live API reference, 2026-06-10):
//
//   - DREAMS GATING. The dreaming-2026-04-21 preview is access-gated and the no-access
//     error shape is undocumented. A 403/404 on /v1/dreams declares the surface
//     uncovered ONCE (a posture finding) and stops polling it until restart — never
//     fabricated data, never a finding per refresh.
//   - WEBHOOK HMAC. The receiver implements the documented scheme deny-closed
//     (HMAC-SHA256 over the raw body keyed by the base64-decoded whsec_ secret,
//     constant-time compare, the standard "v1,<base64>" framing tolerated, a 5-minute
//     freshness window over the envelope created_at, and event.id replay de-dup that
//     remembers an id only after successful emission, so a 503'd delivery's retry is
//     never lost). The exact wire framing is beta-gated; if Anthropic finalizes a
//     different framing, only webhooksig.go changes.
//   - RIGHT-TO-ERASURE. A CMA memory-version redaction is observed and surfaced as
//     evidence-of-erasure; the control plane has NO cryptographic-shred / key-erasure
//     primitive today (verified: modules/compliance maps GDPR Art.17 as an honest gap).
//     True RTBF reconciliation against the append-only ledger is the future
//     session; this connector supplies the evidence signal, it does not claim the control.
//   - WORK QUEUE. The connector OBSERVES the queue (GET .../work and .../work/stats with
//     the org API key) for depth/backlog/worker-liveness; it is NOT a sandbox worker and
//     never claims (poll/ack/heartbeat) or executes work items. The worker-side OAuth
//     tokens (sk-ant-oat01...) are never held: workers are the operator's, not ours.
//   - PERMISSION POLICIES are not a standalone CMA resource: they live on an agent's
//     tools[] (always_allow|always_ask — there is no always_deny). The DECLARED policy
//     travels as PERMITTED edges (agentToolEdges); the GATE FIRING (a session paused on
//     requires_action) is observed via the session event list, event-driven off the
//     session.status_idled webhook. The always_ask round-trip (user.tool_confirmation)
//     is routed through the HITL bridge by the AGPL composition root, never by this
//     Apache connector (which cannot import /core and must not decide).
//   - GRADER COST. The live outcome_evaluations[] schema carries NO usage field, so the
//     grader's separate context-window cost is not attributable from the REST resource
//     (the outcomeCostSample modeled a fabricated shape and was removed).
//
// Boundary (LICENSING.md): Apache-2.0; imports only /sdk (+ the connector-internal httpx
// read-only GET client and redact scrubber), NEVER /core. Minimal data (docs/SECURITY-HARDENING.md): it
// carries vlt_/vcrd_/memstore_/memver_/env_/work_/sesn_/sthr_/outc_/drm_/agent_
// REFERENCES, resource types and state — never a credential value, a memory's content,
// a prompt, a dream's instructions, an event's message content, or a webhook payload
// body; sensitive finding detail is hashed (redact.Hash).
package claudemanagedagents
