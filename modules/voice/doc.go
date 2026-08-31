// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package voice is module XVI — voice & realtime agents (README.md §XVI). It is the
// OBSERVE-AND-GOVERN plane for conversational/realtime agents: it does NOT
// reimplement a voice SDK (Realtime API / WebRTC / ASR / TTS) and it never opens a
// media stream itself.
//
// What it does:
//
//   - Governs WHO may open a voice/realtime session, with WHICH model/provider,
//     under WHICH policy (voice_policy is default-DENY: opening a privileged
//     interface with no allowing policy is refused). Opening a session is a
//     two-phase, HITL-gated (the ApprovalGate seam, deny-closed), plan_hash-
//     bound (anti-TOCTOU — an approval cannot be silently upgraded to a stronger
//     model), audited-to-the-real-principal, APPEND-ONLY-evidenced privileged
//     action. Actuation (calling a provider Realtime API) leaves through the
//     deny-closed Dispatcher seam; absent a dispatcher an approved open is
//     honestly "declared, not opened". The module never calls a provider.
//
//   - Tracks a session's METADATA: derived state (live/idle/ended at read time from
//     activity recency — no stored lifecycle), turn counts, duration, latency
//     (avg/max), language. Ingested from the OpenAI Realtime SIP call observer when
//     configured, or any future in-process voice probe, via the module-owned,
//     deny-closed event Type voice.telemetry.observed.
//
//   - Flags voice-interface governance issues as Findings: voice_policy_violation
//     (telemetry for an (agent,model,provider) no policy allows), voice_latency_degraded
//     (latency past the policy SLA bound), voice_ungoverned_open (an open attempted
//     with no approval gate wired — the gap is surfaced, the open still denied),
//     voice_recording_sad_risk, voice_transcript_unclassified, and
//     realtime_session_ungoverned.
//
// RED LINE — minimal data (docs/SECURITY-HARDENING.md). HARD BAN on content: NO column, and the
// telemetry parser's allow-list rejects unknown keys, so no raw audio, audio
// bytes/URLs, full/partial transcript TEXT, ASR/TTS text, prompt/response content,
// or speaker PII can be persisted. Only transcript_ref_hash — the hashHex of an
// EXTERNAL transcript LOCATOR (never of transcript text) — proves a transcript
// exists without holding it. Anything sensitive is hashed in code before
// persistence: FieldSpec.Redact is not enforced on the write path, so the hashing
// is the guarantee. Cost/USD is NOT here — FinOps (module XI) owns dollar amounts.
//
// HONEST COVERAGE. The govern half (policy + two-phase open + ledger) works from day
// one. The observe half is live when the OpenAI Realtime SIP call plane is configured;
// otherwise it stays empty until an IN-PROCESS probe publishes voice.telemetry.observed
// (an out-of-process plugin cannot — the gRPC ControlPlane proto carries no event RPC).
// Start() reports that posture. A voice session ending is NORMAL silence (like a
// finished agent), so there is deliberately NO "stall" finding — emitting one would be
// a false positive with no honest baseline.
//
// COMPOSITION ROOT (wired on-demand at boot; see the dated note below): the boot wires the real
// ApprovalGate, the real Dispatcher (model-provider / core runtime Realtime
// adapter), and the in-process voice probe via the With* options.
//
// Update 2026-07-05 (E2): the OpenAI Realtime SIP call plane is opt-in via
// OLIVARES_VOICE_CALL_CONFIG. Its webhook secret is config field webhook_secret, tenant
// attribution is tenant, and OpenAI project cost attribution is project_ref. Provider
// API credentials are reused from OLIVARES_VOICE_DISPATCH_CONFIG. With the call config
// absent, the receiver is not mounted and the observe half is dormant.
package voice
