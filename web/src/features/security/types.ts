// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// DTOs for module IX (Security · guardrails · forensics), mirroring the published UI
// data contract. The plane is DETECTIVE BY
// DEFAULT: a guardrail verdict is `allow`/`flag` unless enforcement is explicitly
// enabled AND governed — only then is `block` reachable. Evidence is MINIMAL: a
// `detail_hash` is a one-way FINGERPRINT, never a payload; excerpts arrive already
// redacted. Timestamps are RFC3339 strings. No response ever carries a prompt,
// output, or secret.

import type { CheckpointStatus } from '@/lib/api/types'

// --- 1. Findings -------------------------------------------------------------

export type FindingSeverity = 'low' | 'medium' | 'high' | 'critical' | string
export type FindingStatus = 'open' | 'triaged' | 'resolved' | 'dismissed'

/** GET /findings · GET /findings/{id}. `detail_hash` is a fingerprint (HashChip),
 *  never a payload. The evidence (kind/severity/detail_hash) is IMMUTABLE — triage
 *  only changes the flow `status`. */
export interface Finding {
  id: string
  /** The detector family: `guardrail` / `anomaly` / `forensic` / … */
  kind: string
  severity: FindingSeverity
  status: FindingStatus
  /** The detector / origin (`pii`, `prompt_injection`, `anti_evasion_correlated`…). */
  source: string
  /** The kind of subject (`session` / `agent` / `resource` / `identity`). */
  subject_kind: string
  subject_ref: string
  title: string
  /** One-way fingerprint of the redacted detail — NEVER a payload (docs/SECURITY-HARDENING.md).*/
  detail_hash: string
  occurred_at: string
  metadata?: Record<string, unknown>
}

/** Filters for GET /findings (all optional). */
export interface FindingFilters {
  kind?: string
  severity?: string
  status?: string
  source?: string
  subject_kind?: string
  /** ⛔ ESTA VISTA LO ENVÍA SIEMPRE —y el matiz importa, porque decía «no es opcional
   *  de hecho» y eso afirmaba del CONTRATO lo que sólo es cierto de esta pantalla: el
   *  backend sí sabe continuar por cursor—. Sin `limit` el repositorio genérico pagina a 100
   *  (`core/internal/store/sqlstore/generic.go`, `defaultLimit`) y `handleListFindings`
   *  devuelve `has_more: true` que nadie miraba. En la pantalla de seguridad, enseñar
   *  las primeras cien sin decirlo se lee «éstos son los hallazgos», que es la
   *  afirmación más cara que puede hacer esta consola. */
  limit?: number
}

/** GET /findings/export?format=sarif — the server's exact bytes plus the two
 *  things the response carries around them: the suggested filename and whether
 *  the export stopped at its result cap. */
export interface FindingsExportResult {
  filename: string
  content_type: string
  /** Raw response body, byte-for-byte as the server produced it. */
  text: string
  /** True when the server hit its result cap: the file is valid but partial. */
  truncated: boolean
}

/** PATCH /findings/{id} body — triage only changes the flow status. */
export interface FindingTriageInput {
  status: FindingStatus
}

/** GET /safety-posture — one provider-surface roll-up. `subject_kind` is the
 *  provider safety surface: `openai.moderation`, `bedrock.guardrail`, `azure.rai_policy`… */
export interface SafetyPostureProvider {
  subject_kind: string
  total: number
  open: number
  by_severity: Record<string, number>
}

/** GET /safety-posture — the read-first provider AI-safety posture view: the
 *  per-surface roll-up plus the underlying posture findings (most-recent first). It
 *  aggregates the OpenAI Moderation / AWS Bedrock Guardrails / Azure RAI posture the
 *  connectors ingest — never a provider payload. */
export interface SafetyPostureView {
  providers: SafetyPostureProvider[]
  items: Finding[]
  has_more: boolean
  /** True only when the tenant exceeds the roll-up scan cap, so the per-provider
   *  counts cover the first N findings rather than the whole estate (rare). */
  counts_partial: boolean
}

// --- 2. Guardrail inspect ----------------------------------------------------

export type Verdict = 'allow' | 'flag' | 'block'
/** Which posture produced the verdict. `block` requires `enforced`. */
export type EnforcementMode = 'detective' | 'enforced'
export type GuardrailSurface = 'input' | 'output' | 'tool_args'

/** One guardrail trip in an inspect response (evidence is minimal). */
export interface Detection {
  /** The guardrail family (`pii`, `prompt_injection`, `jailbreak`, `content`…). */
  class: string
  rule: string
  severity: FindingSeverity
  title: string
  /** Already-redacted placeholder/label — NEVER the secret. */
  excerpt?: string
  /** OWASP framework ref (`LLM01:2025`, `ASI01`…). */
  owasp?: string
  /** MITRE ATLAS ref (`AML.T0051`…). */
  atlas?: string
  /** Would this trip have blocked under the active enforcement policy? */
  enforced: boolean
}

/** POST /security/guardrails/inspect body. The `text` is hashed redacted and never
 *  stored or returned. */
export interface InspectInput {
  surface: GuardrailSurface
  text: string
  agent_ref?: string
  session_ref?: string
  resource_ref?: string
  enforce?: boolean
}

/** POST /security/guardrails/inspect response. */
export interface InspectResult {
  verdict: Verdict
  detections: Detection[]
  /** Findings persisted from this inspection (link to §1). */
  finding_ids: string[]
  /** The posture that produced the verdict: `detective` | `enforced`. */
  enforcement: EnforcementMode
}

// --- 3. Enforcement posture --------------------------------------------------

/** One enforcement row. An empty list = fully detective (the safe default). */
export interface EnforcementEntry {
  /** A guardrail family or `"*"` (wildcard). */
  class: string
  enabled: boolean
  min_severity: FindingSeverity
  /** false => enabled without human governance — render a visible warning. */
  governed: boolean
  set_by?: string
  updated_at?: string
}

/** GET /security/enforcement. */
export interface EnforcementResponse {
  items: EnforcementEntry[]
}

/** PUT /security/enforcement body (admin). Disabling (back to detective) is always
 *  allowed; enabling may be denied by governance (403). */
export interface EnforcementInput {
  class: string
  enabled: boolean
  min_severity?: FindingSeverity
  reason?: string
}

// --- 4. Anomalies ------------------------------------------------------------

/** `approximate` = unreconciled drift — discount it, NEVER title it as a firm
 *  violation (docs/SECURITY-HARDENING.md).*/
export type Confidence = 'attributed' | 'approximate' | string

/** One anomaly. The list is PRIVILEGED + self-audited; ordered by `priority` desc. */
export interface Anomaly {
  /** `access_drift` / `egress_exfil_suspected` / `anti_evasion_correlated` / … */
  kind: string
  severity: FindingSeverity
  /** 0..100; the backend orders by this descending — the UI respects it. */
  priority: number
  subject_kind: string
  subject_ref: string
  title: string
  confidence?: Confidence
  source: string
  occurred_at: string
  /** Non-sensitive context (origin/resource/mode/reconciled…). */
  evidence?: Record<string, unknown>
}

/** GET /security/anomalies. */
export interface AnomaliesResponse {
  items: Anomaly[]
}

// --- 5. Forensic cases + timeline --------------------------------------------

export type CaseStatus = 'open' | 'investigating' | 'contained' | 'closed'

/** GET /security/cases · GET /security/cases/{id}. The metadata is mutable; the
 *  case's chain of custody is append-only. `integrity_ok`/`attested_seq` freeze at
 *  open time so a later tamper is detectable against that frozen state. */
export interface ForensicCase {
  id: string
  title: string
  status: CaseStatus
  severity: FindingSeverity
  subject_kind: string
  subject_ref: string
  summary: string
  opened_by: string
  integrity_ok: boolean
  integrity_reason?: string
  attested_seq: number
  opened_at: string
  closed_at?: string
}

/** Las CUATRO clases de enlace que el motor acepta (`forensic.go:34`). No es un conjunto
 *  abierto: `link_kind` fuera de aquí es un 400 con el mensaje del motor. */
export const CASE_LINK_KINDS = [
  'finding',
  'audit_seq',
  'anomaly',
  'note',
] as const
export type CaseLinkKind = (typeof CASE_LINK_KINDS)[number]

/** Un enlace de la cadena de custodia (`GET /cases/{id}/links`). */
export interface CaseLink {
  id?: string
  link_kind: CaseLinkKind
  link_ref: string
  note?: string
  linked_by?: string
  linked_at?: string
}

/** El cuerpo de `POST /cases`. El motor exige `title` y una severidad del enum, y ABRE el
 *  caso él mismo: `status` no se manda — lo fija en «open» (`forensic.go` handleCreateCase). */
export interface CreateCaseInput {
  title: string
  severity: FindingSeverity
  subject_kind?: string
  subject_ref?: string
  summary?: string
}

/** El cuerpo de `PATCH /cases/{id}`.
 *
 *  ⛔ TODO OPCIONAL, y eso es el contrato, no una comodidad: el motor lee punteros
 *  (`req.Status != nil`), así que un campo AUSENTE se deja como está. Construir este cuerpo
 *  esparciendo el caso completo escribiría de vuelta cada campo y convertiría una edición de
 *  estado en una sobreescritura. */
export interface UpdateCaseInput {
  status?: CaseStatus
  severity?: FindingSeverity
  title?: string
  summary?: string
}

/** The integrity object on a case timeline (a subset of §6 verify). */
export interface CaseIntegrity {
  chain_ok: boolean
  chain_reason?: string
  checkpoints_verified: boolean
  checkpoints_ok: boolean
  /** See IntegrityVerify.checkpoint_status — "pending" is not a failure. */
  checkpoint_status?: CheckpointStatus
  attested_seq: number
  head_seq: number
}

/** One append-only ledger event (matches the kit's TimelineEvent). */
export interface TimelineEntry {
  seq: number
  occurred_at: string
  actor: string
  actor_kind: string
  action: string
  target_kind: string
  target_id: string
  hash: string
  prev_hash: string
  signed: boolean
  linked: boolean
}

/** GET /security/cases/{id}/timeline. Privileged + self-audited. */
export interface CaseTimeline {
  case: ForensicCase
  integrity: CaseIntegrity
  events: TimelineEntry[]
}

// --- 6. Integrity verify -----------------------------------------------------

/** GET /security/integrity/verify. Two verdicts: the hash chain, and the signed
 *  checkpoints. `checkpoints_verified:false` = signing not wired — render
 *  "unavailable", NOT a failure (docs/SECURITY-HARDENING.md).*/
export interface IntegrityVerify {
  chain_ok: boolean
  chain_checked: number
  chain_break_at?: number
  chain_reason?: string
  /** false => no checkpoint key wired — distinguish "unavailable" from a failure. */
  checkpoints_verified: boolean
  /** Strict: false BOTH for a virgin ledger and for a tampered one. Never render
   *  a verdict from this alone — read `checkpoint_status`. */
  checkpoints_ok: boolean
  checkpoints: number
  checkpoint_break_at?: number
  checkpoint_reason?: string
  /** The three answers (core/audit CheckpointStatus): "ok" | "failed" |
   *  "pending" (nothing attested yet — a young ledger, NOT a failure). Absent
   *  only when `checkpoints_verified` is false, i.e. nothing was verified. */
  checkpoint_status?: CheckpointStatus
  attested_seq: number
  head_seq: number
}
