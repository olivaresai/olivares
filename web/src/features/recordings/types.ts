// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// DTOs for privileged session recording — a 1:1 mirror of
// modules/recording/handlers.go. A recording session captures an operator's
// privileged actions as hash-chained FRAMES anchored to the evidence ledger.
// Minimal-data by construction (docs/SECURITY-HARDENING.md): a frame carries the route shape,
// identifiers and a one-way body digest — never payloads, query values or
// secrets. The web RENDERS the chain; it never recomputes or repairs it.
/** Recording session lifecycle: open and appending, or sealed (final evidence). */
export type RecordingStatus = 'active' | 'sealed' | (string & {})

/** Who the recorded credential belongs to. */
export type SubjectKind = 'user' | 'token' | (string & {})

/** Tenant consent posture: AC-8 banner only, or explicit acknowledgement. */
export type ConsentMode = 'notice' | 'required' | (string & {})

/** The engine's classification of one recorded call. */
export type FrameOutcome =
  'allowed' | 'denied' | 'rejected' | 'error' | (string & {})

/** Provenance of a model-derived summary — derived narrative, never evidence. */
export interface SummaryMeta {
  derived: true
  generated_at: string
  source: string
}

/** One recording session (GET /sessions, /sessions/{id}). */
export interface SessionDTO {
  id: string
  subject: string
  subject_kind: SubjectKind
  subject_user?: string
  cred: string
  status: RecordingStatus
  opened_at: string
  last_at?: string
  sealed_at?: string
  seal_reason?: string
  frames_written: number
  frames_reserved: number
  /** Frames were reserved but never written — a permanent, evident gap. */
  gap: boolean
  tip_hash?: string
  open_seq?: number
  anchor_seq?: number
  seal_seq?: number
  consent_at?: string
  consent_mode?: ConsentMode
  /** Set when the session was opened under a break-glass grant (always recorded). */
  breakglass_grant?: string
  summary?: string
  summary_meta?: SummaryMeta
  retention_class?: string
}

/** One hash-chained frame of the replay (GET /sessions/{id}/replay). */
export interface FrameDTO {
  idx: number
  at: string
  actor: string
  actor_kind: string
  actor_user?: string
  act_as?: string
  namespace: string
  method: string
  pattern: string
  perm: string
  /** Redacted chi URL params (identifiers by convention) — never values. */
  params?: Record<string, string>
  /** Query parameter NAMES only — never values. */
  query_keys?: string
  http_status: number
  outcome: FrameOutcome
  body_sha256?: string
  body_bytes?: number
  dur_ms: number
  prev_hash: string
  hash: string
  anchor_seq?: number
}

/** One correlated evidence-ledger event interleaved with the replay. */
export interface LedgerEventDTO {
  seq: number
  occurred_at: string
  actor: string
  action: string
  target_kind?: string
  target_id?: string
}

/** GET /notice — the AC-8 recording notice for the calling operator. */
export interface NoticeResponse {
  recorded_namespaces: string[]
  breakglass_always: true
  consent_mode: ConsentMode
  /** True when the operator must acknowledge before any privileged action. */
  consent_required: boolean
  acknowledged: boolean
  session_id?: string
  schema: string
  semconv: string
}

/** POST /ack — the operator's explicit AC-8 acknowledgement. */
export interface AckResponse {
  session_id: string
  acknowledged_at: string
}

/**
 * GET /config — the tenant's resolved recording policy (defaults applied). A 1:1
 * mirror of `configDTO` (modules/recording/handlers.go): `breakglass_always` is
 * FORCED true server-side and `retention_enforced` is FORCED false (retention is a
 * classification TAG today — no purge is wired yet), so both are read-only signals,
 * never editable controls.
 */
export interface RecordingConfig {
  namespaces: string[]
  breakglass_always: true
  consent: ConsentMode
  idle_seconds: number
  retention_days: number
  /** Forced false: retention is a tag only — no policy purges a recording yet. */
  retention_enforced: boolean
  ai_summaries: boolean
}

/**
 * PUT /config body — a 1:1 mirror of `putConfigRequest`. Note the asymmetry with
 * the GET DTO: `breakglass_always` (immutable — always on) and `retention_enforced`
 * (not yet wired) are ABSENT from the write body; a save can never toggle them.
 */
export interface RecordingConfigInput {
  namespaces: string[]
  consent: ConsentMode
  idle_seconds: number
  retention_days: number
  ai_summaries: boolean
}
