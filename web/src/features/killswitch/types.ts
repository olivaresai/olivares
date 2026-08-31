// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// DTOs for the estate kill switch + guardian rules — mirror the Go DTOs in
// modules/governance/{killswitch,killswitch_evidence,guardian}.go 1:1 (snake_case
// JSON tags). The web is a thin client (ARCHITECTURE.md): the stop row is the engine's
// SINGLE source of truth — the view renders it and dispatches privileged, audited
// operations; it never derives stop state on its own.
//
// Lifecycle invariants encoded here:
//  - the stored status is ONLY 'active' | 'reenabled'; the post-review is a FLAG on
//    the row, not a third status. The UI presents active → reenabled (post-review
//    due) → reviewed via presentedStopState().
//  - ENGAGE is deliberately cheap (admin-tier + mandatory reason, no approval gate);
//    the engaging session's assurance level (engaged_aal) is recorded for forensics.
//  - RE-ENABLE is NEVER unilateral: the first POST opens an dual-control
//    approval (202 pending_approval envelope, two distinct humans, no break-glass);
//    re-POSTing reports progress and the call that finds it approved flips the stop
//    (200, plain stop DTO).
import type { ApprovalDTO } from '@/features/governance/types'

// --- the stop row --------------------------------------------------------------

/** Stored stop status (review is a flag, never a status). */
export type KillSwitchStatus = 'active' | 'reenabled' | (string & {})
export const KILL_SWITCH_STATUSES: KillSwitchStatus[] = ['active', 'reenabled']

export type KillSwitchScopeKind = 'estate' | 'agent'
export type KillSwitchSource = 'operator' | 'guardian' | (string & {})

/** One stop row (item of GET /killswitch, body of the engage 201, …). */
export interface KillSwitchDTO {
  id: string
  /** 'estate' (tenant-wide) or 'agent' (one agent across its surfaces). */
  scope_kind: KillSwitchScopeKind | (string & {})
  /** The agent ref as GIVEN at engage (agent scope only). */
  scope_ref?: string
  /** Resolved agent UUID, when the ref matched the inventory. */
  agent_id?: string
  /** Resolved agent external id, when the ref matched the inventory. */
  agent_external_id?: string
  status: KillSwitchStatus
  /** Mandatory engage justification (operator prose). */
  reason?: string
  /** 'operator' (console engage) or 'guardian' (a rule fired it). */
  source: KillSwitchSource
  /** Guardian rule id, when source=guardian. */
  rule_ref?: string
  /** Audit-actor handle ('user:<id>'), never an email. */
  engaged_by?: string
  /** Recorded assurance level of the engaging session (0 for guardian). */
  engaged_aal: number
  engaged_at?: string
  /** The engage self-audit Seq — the evidence pack's timeline anchor. */
  engage_audit_seq: number
  /** Pending actuation approvals canceled by the engage. */
  revoked_approvals: number
  /** The bound dual-control re-enable approval id, once one was opened. */
  reenable_approval?: string
  reenabled_by?: string
  reenabled_at?: string
  reenable_audit_seq?: number
  /** True once the forced post-review landed (closes the incident loop). */
  reviewed: boolean
  reviewed_by?: string
  reviewed_at?: string
  review_note?: string
}

/** Live posture (GET /killswitch/state) — what the banner shows first. */
export interface KillSwitchStateDTO {
  estate_stopped: boolean
  active: KillSwitchDTO[]
}

/** Write body of POST /killswitch. scope_ref MUST be empty for an estate stop and
 * is required for an agent stop; reason is mandatory. */
export interface EngageKillSwitchRequest {
  scope_kind: KillSwitchScopeKind
  scope_ref?: string
  reason: string
}

/** Write body of POST /killswitch/{id}/reenable (optional operator note carried
 * onto the dual-control request). */
export interface ReenableKillSwitchRequest {
  reason?: string
}

/** The 202 envelope while the dual-control approval is collecting decisions. */
export interface ReenablePendingDTO {
  status: 'pending_approval'
  approval: ApprovalDTO
  stop: KillSwitchDTO
}

/** POST /reenable resolves to the pending envelope (202) or, once two distinct
 * humans approved, the flipped stop row (200). */
export type ReenableResponse = KillSwitchDTO | ReenablePendingDTO

export function isReenablePending(
  r: ReenableResponse,
): r is ReenablePendingDTO {
  return (
    'approval' in r && (r as ReenablePendingDTO).status === 'pending_approval'
  )
}

/** Write body of POST /killswitch/{id}/review — the note is mandatory. */
export interface ReviewKillSwitchRequest {
  note: string
}

/** The UI's presented lifecycle state: active → reenabled-but-unreviewed (the
 * forced post-review is due) → reviewed (incident loop closed). Derived from the
 * stored status + reviewed flag, never stored. */
export type StopPresentedState = 'active' | 'review_due' | 'reviewed'

export function presentedStopState(stop: KillSwitchDTO): StopPresentedState {
  if (stop.status === 'reenabled') {
    return stop.reviewed ? 'reviewed' : 'review_due'
  }
  return 'active'
}

// --- evidence pack -------------------------------------------------------------

/** The incident evidence pack (GET /killswitch/{id}/evidence). The console
 * downloads it VERBATIM as JSON (the bytes are the evidence — pack_sha256 seals
 * them); only the fields the UI mentions are typed, the rest rides through. */
export interface EvidencePack {
  generated_at?: string
  tenant?: string
  pack_sha256?: string
  [key: string]: unknown
  /**
   * ⛔ LO QUE EL PAQUETE NO TRAE. `modules/governance/killswitch_evidence.go:117-118` acota la
   * cronología y los hallazgos cuando son muchos, y lo declara con estas dos banderas. Una
   * evidencia de incidente que se lee como completa sin serlo es el peor sitio para esa
   * confusión: la completitud es exactamente lo que se le exige.
   */
  timeline_truncated?: boolean
  findings_truncated?: boolean
}

// --- guardian rules + containment trail -----------------------------------------

export type GuardianAction = 'stop_agent' | 'quarantine_nhi' | 'stop_estate'
export const GUARDIAN_ACTIONS: GuardianAction[] = [
  'stop_agent',
  'quarantine_nhi',
  'stop_estate',
]

export type GuardianMode = 'auto' | 'approval'
export const GUARDIAN_MODES: GuardianMode[] = ['auto', 'approval']

/** Shared severity scale for the rule floor (guardian rules default to 'high'). */
/** Los cuatro escalones que el motor acepta en `agent_tier`, en su orden, y la lista es
 * SUYA: `modules/governance/guardian.go:267-270` rechaza con 400 cualquier otro valor
 * («agent_tier must be one of low, medium, high, critical (or empty for any)»). El vacío
 * NO es un quinto valor: es «cualquier tier», y por eso no viaja en el cuerpo. */
export const GUARDIAN_AGENT_TIERS = ['low', 'medium', 'high', 'critical'] as const
export type GuardianAgentTier = (typeof GUARDIAN_AGENT_TIERS)[number]

export const GUARDIAN_SEVERITIES = [
  'info',
  'low',
  'medium',
  'high',
  'critical',
] as const
export type GuardianSeverity = (typeof GUARDIAN_SEVERITIES)[number]
export const GUARDIAN_DEFAULT_SEVERITY: GuardianSeverity = 'high'

/** One containment rule (item of GET /guardian/rules). */
export interface GuardianRuleDTO {
  id: string
  /** Operator-facing identity — immutable after creation. */
  name: string
  enabled: boolean
  /** Comma-separated finding-kind allowlist; empty = any kind (the guardian's own
   * 'killswitch_*'/'guardian_*' findings are ALWAYS excluded server-side). */
  match_kinds?: string
  min_severity: GuardianSeverity | (string & {})
  action: GuardianAction | (string & {})
  /** 'auto' acts immediately; 'approval' queues behind one human decision. */
  mode: GuardianMode | (string & {})
  /** Acota la regla a un escalón de riesgo del agente; ausente = cualquiera. El motor lo
   * devuelve desde `guardian.go:135`, y la consola no lo conocía: una regla creada por API
   * con tier se veía en la tabla como si aplicara a todos. */
  agent_tier?: string
  created_by?: string
  note?: string
}

/** Write body of POST /guardian/rules. */
export interface CreateGuardianRuleRequest {
  name: string
  enabled?: boolean
  match_kinds?: string
  /** Defaults to 'high' server-side when omitted. */
  min_severity?: string
  action: GuardianAction
  mode: GuardianMode
  /** Omitido = cualquier tier. No se manda vacío: el motor distingue «ausente» de «''»
   * sólo en que ambos significan «cualquiera», y mandar ruido es inventar intención. */
  agent_tier?: GuardianAgentTier
  note?: string
}

/** Write body of PUT /guardian/rules/{id} — partial; name is immutable. */
export interface UpdateGuardianRuleRequest {
  enabled?: boolean
  match_kinds?: string
  min_severity?: string
  action?: GuardianAction
  mode?: GuardianMode
  agent_tier?: GuardianAgentTier | ''
  note?: string
}

export type GuardianActionStatus =
  | 'pending'
  | 'executed'
  | 'rejected'
  | 'expired'
  | 'failed'
  | (string & {})

export const GUARDIAN_ACTION_STATUSES: GuardianActionStatus[] = [
  'pending',
  'executed',
  'rejected',
  'expired',
  'failed',
]

/** One trail entry (item of GET /guardian/actions): what a rule did, or queued,
 * for one finding. */
export interface GuardianActionDTO {
  id: string
  rule_id: string
  rule_name: string
  finding_kind: string
  /** Dedup identity of the finding (detail hash) — a fingerprint, never content. */
  finding_ref: string
  finding_severity: string
  target_kind: string
  target_ref?: string
  action: GuardianAction | (string & {})
  mode: GuardianMode | (string & {})
  status: GuardianActionStatus
  /** The HITL confirmation approval, when mode=approval. */
  approval_id?: string
  /** The stop row a stop_agent/stop_estate containment engaged. */
  killswitch_id?: string
  detail?: string
  executed_at?: string
}
