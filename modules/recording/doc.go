// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package recording is module IX's privileged-session recording subsystem
// (SEC-G5): an immutable, ledger-anchored, replayable record
// of what a privileged OPERATOR session actually did on the product's most
// sensitive surfaces — the layer above the per-event semantic self-audit, and
// the control PAM-aligned/high-assurance buyers expect for consoles and
// emergency access.
//
// # Model
//
// A RECORDING SESSION is the privileged window of one credential (a human
// operator's login session, or — on the break-glass floor — a service token)
// inside one tenant. Its FRAMES are an append-only trail (DB-level immutability
// guards), one frame per module-route action on a recorded surface: who, when,
// route shape, permission, target identifiers, delegation, outcome, request
// digest. Frames are hash-chained per session (chain.go) and the chain is
// ANCHORED into the tamper-evident audit ledger: an open event when the session
// starts, a periodic anchor every anchorEvery frames, and a seal event when the
// session closes — each anchor binds the chain tip through AuditDraft.
// PayloadHash, so rewriting any frame breaks the session chain AND its sealed,
// signed ledger anchors. The ledger's Meta is write-only on read paths; the
// readable binding deliberately rides TargetKind/TargetID/PayloadHash.
//
// Honest residual (mirrors the ledger's own checkpoint interval, docs/SECURITY-HARDENING.md):
// on an ACTIVE session, frames past the last periodic anchor are bound only by
// the session chain and its mutable tip until the next anchor or the seal
// lands; verify reports anchored_through so the boundary is explicit, never
// implied.
//
// # Capture
//
// Capture happens at the engine's module-route wrapper (core/api Options.
// Recorder seam): Gate runs BEFORE the handler and is DENY-CLOSED — on a
// recorded surface, no appendable evidence means no privileged action (mirrors
// Teleport's fail-closed sync recording mode); Record appends the frame after
// the handler completed. Gate also RESERVES a frame slot, so a post-completion
// append failure leaves a permanently visible reserved>written gap on the
// session row (gap evidence, never a silent hole). Recorded scope: every
// break-glass route for every principal (the mandatory floor, not
// configurable), plus the per-tenant configured privileged namespaces for
// EVERY principal kind (default: governance, claude-policy, claude-agents,
// identity, accessmap, recording itself — watching a recording is recorded).
// Tokens are recorded too: an operator can mint a token carrying their own
// grants in one call, so scoping recording to interactive logins would hand
// the privileged insider a one-step bypass.
//
// Honest limit: only module routes (/v1/m/<ns>) flow through the wrapper. The
// core /v1 surfaces (users/memberships/tokens/SCIM/audit export) are
// ledger-audited but not frame-recorded — the replay correlates them through
// the session's ledger window rather than frames.
//
// # Minimal data and redaction
//
// Frames are structured action events, never transcripts or bodies (the
// Teleport lesson: output capture leaks nothing typed, structured events defeat
// obfuscation): route pattern + redacted identifiers + query parameter NAMES +
// a one-way SHA-256 of the request body. Parameter values pass a bounded
// redactor (redact.go) so an email-shaped or credential-shaped value never
// persists. There is structurally no field a secret could ride in (docs/SECURITY-HARDENING.md).
//
// # Notice and consent
//
// The NIST SP 800-53 AC-8 pattern: the console shows a recording notice on
// every recorded surface. Per-tenant consent mode: "notice" (default —
// acknowledgement-by-use; the first privileged action itself is the recorded
// consent) or "required" (stricter than commercial PAM defaults: the operator
// must explicitly acknowledge via POST /ack before any privileged action;
// deny-closed 403 recording_consent_required until then).
//
// # Break-glass
//
// Recording is MANDATORY for break-glass: activation flows through the
// recorded governance surface (deny-closed by the wrapper), the governance
// module additionally requires an active recording session via its
// RecordingGate seam and binds the grant to the session (the activation ledger
// event carries recording_session in Meta; the session row carries the grant
// id). Every consume/revoke/review frame lands in a recording session, and the
// grant's forced post-review seals the bound session (the module subscribes to
// the governance_breakglass_reviewed finding).
//
// # Replay, verification, summaries
//
// Replay returns the human-readable frame timeline plus the correlated ledger
// events of the session's window — reconstruction, not blobs. Verify recomputes
// the frame chain and checks every ledger anchor (open/anchor/seal) against the
// stored sequence numbers. An optional Claude-backed Summarizer (port wired by
// the composition root, nil-safe 501) produces reviewer-efficiency summaries
// stored as explicitly DERIVED artifacts — never a substitute for the frames.
//
// # Retention (seam)
//
// Sessions carry retention_class ("privileged-session-recording") and the
// tenant config carries retention_days (default 180, the documented commercial
// PAM default). This module implements NO purge and NO legal hold:
// retention/legal-hold engine owns deletion; frames/sessions are its inputs,
// ledger anchors survive any purge (the chain stays verifiable). Operator PII
// stays out of frames (actor ids only), so erasure has nothing to shred
// here.
//
// # Schema pin
//
// The persisted frame schema is declared as schemaVersion; where attribute
// semantics align with OTel GenAI agent spans the mapping is documented against
// the PINNED semconv vocabulary label (semconvVersion = 1.41.1) plus the
// semconv-genai upstream ref (semconvUpstreamRef = main@c321d7e, verified
// 2026-07-05). The GenAI conventions are still Development status (breaking
// renames have happened, e.g. gen_ai.system → gen_ai.provider.name in 1.37.0), so
// the pin plus a mapping layer, never live coupling. Delegation fields follow
// draft-ietf-oauth-transaction-tokens -08 semantics (sub = originating
// principal; append-only chain), with the agent actor fields marked provisional
// (the agents extension is an individual draft, not WG-adopted).
package recording
