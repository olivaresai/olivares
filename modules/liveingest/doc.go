// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package liveingest is the in-process producer of the bus events that a
// SourceConnector cannot emit (and the voice observe seam). It is module
// XXIV's tiny "live-tap" half: where the cooperative Claude connector emits only
// the sealed model.Observation sum (edge/cost/finding) over its Sink, a detective
// input such as observed agent text (event.ObservedText) or a module-owned
// telemetry event (voice.telemetry.observed) needs Host.Publish — which ONLY an
// in-process module has. The Claude telemetry connector runs OUT-OF-PROCESS as an
// embedded plugin (cmd/olivares/sources.go: pluginBinaryForKind["claude"]),
// so its gRPC SourceService.Gather streams only the sealed Observation oneof
// (sdk/plugin/proto/olivaresv1/v1.proto): it has no event RPC and no text/excerpt
// field, and the wire contract is frozen (buf breaking-check). This module is the
// architectural answer to "who publishes the detective events the connector can't".
//
// Deny-closed and minimal-data by construction (docs/SECURITY-HARDENING.md): it moves NO raw
// payload onto the bus. Every half it owns is honest about being empty rather than
// a silent no-op:
//
//   - guardrail.observed. The security detector chain
//     (modules/security/observed.go) already consumes event.GuardrailObserved; this
//     module is the missing producer. The ONLY observed text available in-process is
//     the tool-ARGUMENT references the connector already redacts and emits on
//     edge.observed (resource.go: a file path, a sanitized URL, an MCP tool ref — a
//     Bash command is reduced to its program, a search query is dropped). The deeper
//     content surfaces (prompt input, model output, raw tool bodies) are NOT
//     available in-process: even under the operator's OTEL_LOG_* capture opt-in the
//     out-of-process connector reduces everything to refs before emission and cannot
//     cross text over the frozen wire (verified). So this producer is DENY-CLOSED:
//     with inspection off (the default) it publishes nothing and the empty half is
//     logged; with the operator opt-in on, it derives a bounded, ALREADY-REDACTED
//     tool_args excerpt from edge.observed and publishes it for the detector chain.
//     It never moves raw content and never widens the connector's capture.
//
//   - voice.telemetry.observed (the module XVI observe seam). The OpenAI Realtime
//     SIP call plane can now publish allow-listed turn metadata when configured
//     (never audio or transcript text). With that call plane unconfigured there is no
//     real telemetry to publish, so it is honestly dormant: it fabricates nothing and
//     the observe half stays visibly empty until a backend feeds it.
//
// Session goal/agent_ref/summary and the orchestration delegation-observe are NOT
// produced here: those signals are already on the bus (identity.agent edges,
// compaction findings, Agent/Task delegation edges), so their owning modules
// (sessions, orchestration) derive them directly from what they already consume —
// no needless event indirection (the live-ingest observe contract).
package liveingest
