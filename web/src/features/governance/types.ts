// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// DTOs for the governance module (VI) — mirror the Go DTOs split across
// modules/governance/{identity,roster,policy,approvals,sweep,helpers}.go 1:1
// (snake_case JSON tags). The web is a thin client (ARCHITECTURE.md): these are the exact
// shapes the engine returns at /v1/m/governance.
//
// CRITICAL invariants encoded here:
//  - NO field carries a secret value OR a secret reference — the module strips
//    credentials (identity metadata is allow-listed to ou/trust_domain; policy specs
//    are typed + re-serialized with an inline-credential guard). The *_ref / requested_by
//    / decider fields are identity/audit IDENTIFIERS, not secrets, and are rendered as
//    actor handles / opaque refs, never resolved to PII (no email lookup).
//  - abacRule.deny is the policy AUTHOR's stated intent (server-validated true), NOT
//    proof of runtime enforcement; the composition root may leave the ABAC evaluator /
//    roster providers UNWIRED. The UI must never imply "enforced" from enabled=true.

// --- identities & groups -----------------------------------------------------

/** Principal classification surfaced from Metadata.principal_type. */
export type PrincipalType = 'human' | 'nhi' | 'unknown' | (string & {})

/** One reconciled identity (item of GET /identities). */
export interface IdentityDTO {
  /** Internal core Identity id. */
  id: string
  /** external_id — stable directory ref (e.g. 'entity:svc'). NOT a secret. */
  ref: string
  /** Display name (NEVER an email; PII stripped). */
  name?: string
  /** Classification (e.g. 'identity', 'nhi', 'agent_nhi'). */
  kind?: string
  /** Provider/source name. */
  source?: string
  /** human | nhi | unknown (from Metadata.principal_type). */
  principal_type?: PrincipalType
  /** Account disabled flag (from Metadata.disabled). */
  disabled: boolean
}

/** One reconciled collection (item of GET /groups). */
export interface GroupDTO {
  /** Collection col_ref (directory group/role/policy ref). */
  ref: string
  /** col_kind — group | role | policy. */
  kind?: string
  display_name?: string
  source?: string
}

/** One member of a group (item of GET /groups/{ref}/members). */
export interface MemberDTO {
  /** The member's directory ref. */
  member_ref: string
  /** 'identity' or 'collection' (nested). */
  member_kind: string
  /** The nested collection a transitive identity came through (only when
   * ?transitive=true and reached via nesting). */
  via?: string
}

/** Result of POST /roster/sync (a single report, NOT a list). */
export interface RosterReport {
  /** Per-source label (aggregate leaves blank). */
  source?: string
  /** Number of sources actually reconciled this call. */
  sources: number
  /** Providers wired for this tenant. */
  providers_configured: number
  identities: number
  collections: number
  memberships: number
  /** Identity sources whose snapshot did not answer this call. Absent on a clean run.
   *  A partial failure is a 200 — the surviving sources did real work — so this is the
   *  only place the caller learns that `sources` is smaller than it should be. */
  providers_failed?: RosterProviderFailure[]
  /** e.g. 'no identity providers configured for this tenant'. */
  note?: string
}

/** One identity source that could not be snapshotted. */
export interface RosterProviderFailure {
  /** The connector's Go type, e.g. '*conjur.Connector'. */
  provider: string
  /** Fixed text; the error detail stays server-side on purpose. */
  reason: string
}

// --- agent ↔ identity (NHI) bindings -----------------------------------------

/** One agent↔identity binding (item of GET /bindings). */
export interface BindingDTO {
  agent_id: string
  agent_name?: string
  /** Internal core Identity id. */
  identity_id: string
  /** The identity's external_id. */
  identity_ref?: string
  /** True if this identity is bound to >1 agent (attribution collapsed). */
  shared: boolean
  /** Exact number of agents bound to this identity. */
  agent_count: number
}

/** Write body of POST /agents/{agentID}/identity. Exactly one of identity_id /
 * identity_ref / mint selects the target. Send ONLY the chosen mode's fields. */
export interface BindRequest {
  /** Bind to existing Identity by internal id. */
  identity_id?: string
  /** Bind by directory ref (find-or-create). */
  identity_ref?: string
  /** Provision a fresh per-agent NHI ('agent:<id>'). */
  mint?: boolean
  /** Permit binding to an unknown-principal-type identity. */
  allow_unknown?: boolean
}

/** Response of POST /agents/{agentID}/identity. */
export interface BindResponse {
  agent_id: string
  identity_id: string
  identity_ref?: string
  /** True if a new NHI was minted. */
  minted?: boolean
  /** True if the resulting identity is bound to >1 agent. */
  shared: boolean
  agent_count: number
}

/** The three mutually-exclusive bind modes the editor offers. */
export type BindMode = 'identity_id' | 'identity_ref' | 'mint'

// --- policies (ABAC + approval) ----------------------------------------------

export type PolicyKind = 'abac' | 'approval' | (string & {})
export const POLICY_KINDS: PolicyKind[] = ['abac', 'approval']

/** ABAC principal-kind selector. */
export type AbacPrincipalKind = 'user' | 'token' | (string & {})
export const ABAC_PRINCIPAL_KINDS: AbacPrincipalKind[] = ['user', 'token']

/** ABAC verb selector (resource segment before the verb). */
export type AbacVerb = 'read' | 'write' | 'admin' | (string & {})
export const ABAC_VERBS: AbacVerb[] = ['read', 'write', 'admin']

/** One deny rule inside an abac policy spec. deny is ALWAYS true (v1 only supports
 * further-restrict rules) — it is not user-editable. */
export interface AbacRule {
  /** MUST be true. */
  deny: boolean
  /** Full permission string, exact match (<=128). */
  permission?: string
  /** read | write | admin. */
  verb?: string
  /** Resource segment before the verb (<=128). */
  resource?: string
  /** user | token. */
  principal_kind?: string
}

/** Spec body of an abac policy. */
export interface AbacSpec {
  /** 1..64 deny rules; each must select at least one of permission/verb/resource/principal_kind. */
  rules: AbacRule[]
}

/** Which requests an approval policy governs. */
export interface ApprovalMatch {
  /** Empty = wildcard, else exact action match. */
  action?: string
  /** Empty = wildcard, else exact subject_kind match. */
  subject_kind?: string
}

/** Spec body of an approval policy. */
export interface ApprovalSpec {
  /** 0..64 (authoritative threshold). */
  required_approvals?: number
  /** 0..31536000. */
  expires_in_seconds?: number
  /** 0..31536000. */
  escalate_in_seconds?: number
  match?: ApprovalMatch
}

/** Canonical re-serialized policy spec (shape depends on kind). */
export type PolicySpec = AbacSpec | ApprovalSpec | Record<string, unknown>

/** A governance policy (response/item of GET/POST/PUT /policies). */
export interface PolicyDTO {
  id?: string
  name: string
  /** 'abac' | 'approval'. Immutable on PUT. */
  kind: PolicyKind
  enabled: boolean
  /** Canonical re-serialized spec (abacSpec or approvalSpec shape). */
  spec: PolicySpec
}

/** Write body of POST/PUT /policies — exactly the fields the backend accepts (it
 * rejects unknown fields). id is server-assigned. */
export interface PolicyInput {
  name: string
  /** 'abac' | 'approval'. Must equal the stored kind on PUT. */
  kind: PolicyKind
  enabled: boolean
  spec: PolicySpec
}

// --- approval queue (HITL) ---------------------------------------------------

/** EFFECTIVE approval status — expiry is derived at read, so a row may report
 * 'expired' even before a sweep persists it. */
export type ApprovalStatus =
  'pending' | 'approved' | 'rejected' | 'canceled' | 'expired' | (string & {})

export const APPROVAL_STATUSES: ApprovalStatus[] = [
  'pending',
  'approved',
  'rejected',
  'canceled',
  'expired',
]

/** Write body of POST /approvals (open a request). */
export interface CreateApprovalRequest {
  /** Bounded short id (<=128). */
  subject_kind?: string
  /** Bounded (<=4096). */
  subject_ref?: string
  /** REQUIRED, bounded short id (<=128). */
  action: string
  /** Operator prose (<=4096). */
  reason?: string
  /** 0..64 (ignored if a policy matches). */
  required_approvals?: number
  /** 0..31536000. */
  expires_in_seconds?: number
  /** 0..31536000. */
  escalate_in_seconds?: number
}

/** One approval request — the APPROVAL QUEUE row. */
export interface ApprovalDTO {
  id: string
  /** WHAT class of thing the action targets. */
  subject_kind?: string
  /** WHICH specific subject (opaque ref). */
  subject_ref?: string
  /** WHAT action is being requested. */
  action?: string
  /** WHO/WHAT asked — audit-actor string 'user:<id>'/'token:<id>', never an email. */
  requested_by?: string
  /** EFFECTIVE status: pending|approved|rejected|canceled|expired. */
  status: ApprovalStatus
  required_approvals: number
  approve_count: number
  reject_count: number
  /** Requester's context/justification. */
  reason?: string
  /** The authoritative approval policy id, if one matched (opaque ref). */
  policy_ref?: string
  /** RFC3339-ish timestamp string. */
  expires_at?: string
  escalate_at?: string
  /** True once escalation has been materialized (escalated_at set). */
  escalated: boolean
  /** Terminal-decision timestamp string. */
  decided_at?: string
}

/** A decision verb. */
export type DecisionVerb = 'approve' | 'reject'

/** Write body of POST /approvals/{id}/decisions. */
export interface DecisionInput {
  /** 'approve' | 'reject' (case-insensitive server-side). */
  decision: DecisionVerb
  /** Decider's note / reason (<=4096). */
  note?: string
}

/** One immutable, append-only decision (item of GET /approvals/{id}/decisions). */
export interface DecisionDTO {
  /** 'approve' | 'reject'. */
  decision: DecisionVerb | string
  /** Audit-actor string ('user:<id>'/'token:<id>') — WHO decided, never an email. */
  decider: string
  decided_at?: string
  /** Decider's note (operator prose). */
  note?: string
}

/**
 * Mirror the engine's separation-of-duties guard for hiding the Approve/Reject
 * controls before the round-trip (gap #8): the decision endpoint 403s when the
 * principal opened the request (a requester cannot decide their own) or is a token
 * with no stable user id (only a stable user can decide). The backend stays
 * authoritative — this only avoids surfacing a 403 the operator can't act on.
 *
 * @param requestedBy the request's audit-actor handle ('user:<id>' / 'token:<id>')
 * @param actor       the current principal's audit-actor handle, or null
 * @param principalKind the current principal kind ('user' | 'token'), or null
 */
export function canDecideOnRequest(
  requestedBy: string | undefined,
  actor: string | null | undefined,
  principalKind: string | null | undefined,
): boolean {
  // A token has no stable user id → cannot decide.
  if (principalKind === 'token') return false
  // Self-decision is forbidden (the requester cannot also approve/reject).
  if (actor && requestedBy && actor === requestedBy) return false
  return true
}

/** Result of POST /approvals/sweep (a single report, NOT a list). */
export interface SweepReport {
  /** Pending requests examined. */
  scanned: number
  /** Requests escalated this run. */
  escalated: number
  /** Requests expired this run. */
  expired: number
  /** True if pending requests remain unscanned (re-run the sweep). */
  more: boolean
}

// --- break-glass emergency access ------------------------------------------

/** EFFECTIVE status: expiry is derived by the server on every read. */
export type BreakGlassStatus = 'active' | 'revoked' | 'expired' | (string & {})

/** One time-boxed emergency grant returned by the break-glass API. */
export interface BreakGlassDTO {
  id: string
  /** Empty = every action; exact value = one action; trailing * = prefix family. */
  match_action?: string
  reason?: string
  /** Stable audit-actor handle, never an email address. */
  activated_by?: string
  status: BreakGlassStatus
  activated_at?: string
  expires_at?: string
  revoked_at?: string
  use_count: number
  reviewed: boolean
  reviewed_by?: string
  reviewed_at?: string
  review_note?: string
}

/** Body of POST /breakglass. The reason and bounded duration are mandatory in UI. */
export interface ActivateBreakGlassInput {
  match_action?: string
  reason: string
  expires_in_seconds: number
}

/** Body of POST /breakglass/{id}/review. The server requires a substantive note. */
export interface ReviewBreakGlassInput {
  note: string
}

/** One immutable entry in a grant's emergency-use trail. */
export interface BreakGlassUseDTO {
  grant_id: string
  action: string
  subject_kind?: string
  subject_ref?: string
  /** Stable audit-actor handle, never an email address. */
  used_by?: string
  used_at?: string
}

// --- per-agent risk profiles ------------------------------------------

/**
 * Governance agent risk tier vocabulary — the OWASP 4-tier scale
 * (modules/governance/risktier.go RiskTier{Low,Medium,High,Critical}). This is a
 * DIFFERENT axis from the EU AI Act compliance taxonomy: there is NO
 * "unacceptable" here — critical is the highest tier.
 */
export type AgentRiskTier = 'low' | 'medium' | 'high' | 'critical'

/** The four tiers, lowest → highest, as the tier Select offers them. */
export const AGENT_RISK_TIERS: AgentRiskTier[] = [
  'low',
  'medium',
  'high',
  'critical',
]

/** Lifecycle state of a risk profile (agentrisk.go arpState*). */
export type AgentRiskState =
  'unclassified' | 'suggested' | 'reviewed' | (string & {})

/**
 * Observed signals that drove a suggested tier — the map the engine returns
 * (agentrisk.go signalsJSON). Observable facts only; never a fabricated inference.
 */
export interface AgentRiskSignals {
  rw_edges?: number
  total_edges?: number
  distinct_resources?: number
  high_severity_findings?: number
  critical_severity_findings?: number
  /**
   * ⛔ EL BARRIDO NO SIEMPRE TERMINA. Lo emite **`modules/governance/agentrisk.go`** —que es el
   * endpoint de ESTA pantalla, no el `risk.go` de compliance, que es otro DTO con la misma
   * semántica—: `:101-105` declara la bandera, `:204` la pone (`edgeTrunc || findTrunc`) y `:273`
   * la mete en el mapa de señales (`signalsJSON["truncated"] = true`). «A truncated scan may have
   * missed a critical finding», así que la clasificación es FAIL-SAFE y **nunca baja de `high`**
   * (`:145-148`).
   *
   * ⇒ Un nivel `high` puesto POR EL TRUNCADO y uno puesto por un hallazgo crítico observado son
   * la misma etiqueta y **piden acciones opuestas**: repetir la clasificación con más presupuesto,
   * o investigar al agente. Sin este campo la consola no podía distinguirlos, y el operador veía
   * «riesgo alto» con CERO hallazgos y ninguna explicación.
   */
  truncated?: boolean
  scheduled?: boolean
  autonomous?: boolean
}

/**
 * One agent risk profile (item of GET /agent-risk-profiles). Mirrors
 * agentrisk.go agentRiskProfileDTO 1:1.
 *
 * CRITICAL invariant: `effective_tier` is the tier ENFORCEMENT reads
 * (tierfloor.go) — it is operator_tier when the operator overrode, else the
 * heuristic suggested_tier. The UI must render effective prominently and NEVER
 * conflate the operator declaration with the heuristic suggestion.
 */
export interface AgentRiskProfileDTO {
  id: string
  agent_id: string
  /** Operator's authoritative override; present only when the operator set it. */
  operator_tier?: string
  /** Heuristic suggestion from the observed signals. */
  suggested_tier?: string
  /** ALWAYS present: operator_tier if set, else suggested_tier. What enforcement reads. */
  effective_tier: string
  /** unclassified | suggested | reviewed. */
  state: AgentRiskState
  signals?: AgentRiskSignals
  /** Audit-actor handle of the human who reviewed/overrode — never an email. */
  reviewed_by?: string
  reviewed_at?: string
}

/** Body of POST /agent-risk-profiles/classify — recompute the suggestion. */
export interface ClassifyAgentRiskInput {
  agent_id: string
}

/** Body of PUT /agent-risk-profiles/{id}/tier — one of the four tiers, or "" to
 * clear the operator override and fall back to the heuristic suggestion. */
export interface SetAgentRiskTierInput {
  tier: string
}

/**
 * The provenance of the effective tier — the honest UI must never conflate the
 * two. Mirrors the engine's effectiveTier() (agentrisk.go): 'operator' when the
 * operator override is set (effective = operator_tier); 'suggested' when only the
 * heuristic applies (effective = suggested_tier); 'none' when the agent is
 * unclassified (neither present).
 */
export function effectiveTierSource(
  profile: Pick<AgentRiskProfileDTO, 'operator_tier' | 'suggested_tier'>,
): 'operator' | 'suggested' | 'none' {
  if (profile.operator_tier) return 'operator'
  if (profile.suggested_tier) return 'suggested'
  return 'none'
}

// --- routine governance policies (· enforcement) -------------------

/** The scope vocabulary the server validates against (routines.go:26-30). */
export const ROUTINE_SCOPE_KINDS = ['tenant', 'workspace', 'user'] as const
export type RoutineScopeKind = (typeof ROUTINE_SCOPE_KINDS)[number]

/**
 * Cadence-floor bounds, mirrored from the ONE place that decides them:
 * minCadenceFloor / maxCadenceFloor (routines.go:34, :43). 0 is a THIRD legal
 * value meaning "no floor" — a control that only offers [60, 31622400] makes
 * the no-floor setting unreachable and locks the operator out of it.
 */
export const ROUTINE_CADENCE_MIN = 60
export const ROUTINE_CADENCE_MAX = 31622400

/**
 * One routine governance policy — mirrors routinePolicyDTO (routines.go:46-63)
 * 1:1.
 *
 * CRITICAL invariant, and the reason this file spells the two list fields as
 * `string[] | null` rather than `string[] | undefined`: they are TRI-STATE and
 * the server emits them WITHOUT `omitempty` precisely so the states survive the
 * wire (routines.go:55-61).
 *
 *   null  → no list authored: "any cron" / "no blocked environment"
 *   []    → an AUTHORED empty list the enforcement honours: deny every cron /
 *           (for the blocklist) block nothing, written on purpose
 *   [...] → the authored entries
 *
 * A `?? []` anywhere downstream collapses the first into the second and paints
 * a policy that DENIES EVERYTHING as permissive. Use routineListState() and let
 * the type system keep null reachable — never widen these to optional.
 *
 * Second-order honesty: an `[]` on the read side is AMBIGUOUS. A stored column
 * that cannot be parsed is deliberately projected as an empty array rather than
 * as "any" (recJSONStringsPtr, routines.go:86-98), because enforcement refuses
 * such a policy outright. So the UI may say "empty list" but must not claim
 * "the operator authored a deny-all" with certainty.
 */
export interface RoutinePolicyDTO {
  id: string
  name: string
  scope_kind: RoutineScopeKind | (string & {})
  /** '' for a tenant-scoped policy; the workspace id/slug or user ref otherwise. */
  scope_ref?: string
  enabled: boolean
  /** Cadence FLOOR in seconds. 0 = no floor. */
  max_cadence_seconds: number
  /** Cap on active routine declarations in scope. 0 = no cap. */
  max_active_routines: number
  require_approval: boolean
  /** TRI-STATE — see the interface doc. NEVER `?? []`. */
  allowed_cron_patterns: string[] | null
  /** TRI-STATE — see the interface doc. NEVER `?? []`. */
  blocked_environments: string[] | null
  /**
   * The FOURTH state, which the two fields above cannot carry: the stored
   * column could not be parsed. It is projected as `[]` so the read surface
   * never paints it as unconstrained, which makes it indistinguishable from an
   * authored deny-all without these flags — and they are opposite facts. An
   * authored empty list composes normally; an unreadable one makes the whole
   * resolution INDETERMINATE and denies closed.
   *
   * The editor MUST NOT write a list it was shown as unreadable: re-sending the
   * projected `[]` would silently repair the column and drop the indeterminate,
   * turning an unreadable blocklist from "refuse every fire" into "block
   * nothing". Repairing it is an explicit operator decision, not a side effect
   * of editing the cadence floor.
   */
  allowed_cron_patterns_unreadable: boolean
  blocked_environments_unreadable: boolean
  /** Audit-actor handle of the author — never an email. */
  created_by?: string
}

/**
 * The three states of a tri-state policy list, named so a renderer cannot
 * accidentally treat two of them alike.
 *
 *  'unset'      — null: no list authored ("any" for an allowlist, "none" for a blocklist)
 *  'empty'      — an empty array the operator authored
 *  'listed'     — one or more entries
 *  'unreadable' — the stored column could not be parsed. It arrives as `[]` too,
 *                 so only the DTO's *_unreadable flag separates it from 'empty'.
 *                 Enforcement refuses such a policy outright.
 */
export type RoutineListState = 'unset' | 'empty' | 'listed' | 'unreadable'

export function routineListState(
  value: string[] | null,
  unreadable = false,
): RoutineListState {
  // Unreadable wins: the engine projects such a column as `[]`, so checking the
  // value first would classify it as an authored deny-all and lose the fact.
  if (unreadable) return 'unreadable'
  if (value === null) return 'unset'
  return value.length === 0 ? 'empty' : 'listed'
}

/** One scope's cap on active routine declarations (routineActiveCapDTO). */
export interface RoutineActiveCapDTO {
  scope_kind: string
  scope_ref: string
  max: number
}

/**
 * The COMPOSED posture for one resolution scope — routineEffectiveDTO
 * (routines.go). This is the fold ENFORCEMENT runs, read back over HTTP; the
 * console renders it and never re-derives it. Composition is monotone
 * (most-restrictive-wins): floors take the maximum, approval ORs, cron
 * allowlists INTERSECT, blocked environments UNION, and active caps stay a
 * vector because caps at different scopes constrain different populations.
 */
export interface RoutineEffectiveDTO {
  /** The axes the composition was resolved FOR, after the default workspace. */
  scope_workspace_ref: string
  scope_user_ref: string
  /** Whether the user axis was answerable. False reproduces the legacy /
   * unrecognised-owner scope where a user-scoped policy denies closed. */
  scope_user_known: boolean
  default_workspace_ref: string
  /** At least one ENABLED policy matched. */
  in_force: boolean
  /**
   * The resolution could NOT be completed — an enabled policy scopes an axis
   * that could not be supplied, or a stored list is unreadable. Enforcement
   * DENIES CLOSED on this, so the UI must render it as a refusal and never as
   * an absence of controls.
   */
  indeterminate: boolean
  indeterminate_axis: string
  /** Composed cadence floor (maximum of the matched floors). 0 = no floor. */
  min_interval_seconds: number
  /** OR of every matched policy's flag. */
  require_approval: boolean
  /**
   * Distinguishes "no allowlist at all" (any cron admitted) from an authored
   * EMPTY one (every cron denied). `cron_allowed` alone cannot: both are [].
   */
  cron_allowlist_in_force: boolean
  cron_allowed: string[]
  blocked_environments: string[]
  active_caps: RoutineActiveCapDTO[]
  /** The matched policy ids — the drill-down from a composed value to its origin. */
  policy_refs: string[]
  /** Stable fingerprint of the composed decision (evidence, not policy bodies). */
  digest: string
}

/** GET /routine-policies/posture. */
export interface RoutinePostureDTO {
  total_policies: number
  /** The subset with enabled=true. `policies` below carries ALL of them. */
  enabled_policies: number
  policies: RoutinePolicyDTO[]
  /**
   * Present on every successful read. Nullable only so a resolution failure is
   * reported as absent instead of as a zero value that reads like "nothing is
   * in force".
   */
  effective: RoutineEffectiveDTO | null
}

/**
 * Body of POST /routine-policies (createRoutinePolicyRequest, routines.go:132-142).
 *
 * The two list fields are OMITTED for "any/none" and sent as `[]` for an
 * authored deny-all — measured, not assumed: Go decodes a JSON `[]` into a
 * NON-NIL empty slice, so the handler's `if in.AllowedCron != nil` writes the
 * authored empty column, while an absent key and an explicit `null` both leave
 * it unset.
 */
export interface CreateRoutinePolicyInput {
  name: string
  scope_kind: RoutineScopeKind
  scope_ref?: string
  enabled?: boolean
  max_cadence_seconds: number
  max_active_routines: number
  require_approval: boolean
  allowed_cron_patterns?: string[]
  blocked_environments?: string[]
}

/**
 * Body of PUT /routine-policies/{id} (updateRoutinePolicyRequest, routines.go:145-156).
 * Name and scope are IMMUTABLE server-side and are not in this shape.
 *
 * The list fields are `json.RawMessage` on the server so THREE intents survive:
 * key absent = "leave it alone", `null` = "clear back to any/none", `[]` = "an
 * authored deny-all". This editor never sends "absent": the operator sees the
 * current state and what they see is what is written, so an untouched field can
 * never be silently cleared.
 */
export interface UpdateRoutinePolicyInput {
  enabled: boolean
  max_cadence_seconds: number
  max_active_routines: number
  require_approval: boolean
  /**
   * OMITTED means "leave the stored value alone" — the only safe instruction
   * for a column the engine reported as unreadable, because writing anything
   * there repairs it and drops a deny-closed. `null` clears back to any/none,
   * `[]` is an authored deny-all. Three intents, all of them deliberate.
   */
  allowed_cron_patterns?: string[] | null
  blocked_environments?: string[] | null
}

/* -------------------------------------------------------------------------- */
/* AgentCore Cedar export (engine · console)                        */
/* -------------------------------------------------------------------------- */

/**
 * ⛔ THE TWO RESPONSES ON THIS SURFACE DO NOT SHARE A NAMING CONVENTION, and the
 * mismatch is silent: `plan` marshals `agentcore.ExportPlan` DIRECTLY
 * (agentcoreexport.go:159 `writeJSON(w, 200, plan)`), and that struct carries NO
 * json tags (exportplan.go:24-34), so Go emits the Go field names verbatim —
 * `PlanHash`, `EngineID`, `Creates`. `apply` and `pending` go through hand-written
 * DTOs that DO have tags (agentcoreexport.go:105-122), so they are snake_case —
 * `plan_hash`, `approval_ref`.
 *
 * Measured, not inferred, by marshalling both on 2026-08-11:
 *   PLAN    {"PlanHash":"H","EngineID":"E","Tenant":"T","Creates":[…],"Updates":null,…}
 *   APPLY   {"plan_hash":"H","results":[{"name":…,"op":…,"status":…,"error":…}]}
 *   PENDING {"status":"pending","approval_ref":"ref","plan_hash":"H"}
 *
 * Writing `plan_hash` for the PLAN response is the defect this comment exists to
 * prevent: it reads `undefined`, the console then posts an empty hash, and the
 * engine 400s — the operator sees "plan_hash is required" for a plan they are
 * looking at. Mirror each half as the engine actually emits it.
 *
 * The slices are `null`, not `[]`, when empty: ExportPlan has no `omitempty` and
 * a nil Go slice marshals to `null`. Every consumer must treat them as nullable.
 */
export type AgentCoreEnforcementMode = 'ACTIVE' | 'LOG_ONLY'

/** The operator's mode choice. `default` sends NO field, so the engine keeps the
 *  tenant's configured mode (agentcoreexport.go:224-227). */
export type AgentCoreModeChoice = 'default' | AgentCoreEnforcementMode

export type AgentCoreExportOp = 'create' | 'update' | 'delete'

/** agentcore.PlannedChange (exportplan.go:37-46) — PascalCase on the wire. */
export interface AgentCorePlannedChange {
  Op: AgentCoreExportOp
  Name: string
  PolicyID: string
  Statement: string
  Description: string
  EnforcementMode: string
  /** The mode the REMOTE policy carries today; "" for a create. */
  RemoteEnforcementMode: string
  RemoteFingerprint: string
}

/** agentcore.ExportItem (export.go:61-74) — PascalCase on the wire. */
export interface AgentCoreExportItem {
  Kind: string
  Tenant: string
  SubjectKind: string
  SubjectRef: string
  ScopeKind: string
  Workspace: string
  Effect: string
  Perms: string[] | null
  Models: string[] | null
  Sources: string[] | null
  Surfaces: string[] | null
  Access: string
}

/**
 * agentcore.UnsupportedItem (export.go:110-115). The engine's own words:
 * "It is never silently dropped." An Olivares row that could not be projected
 * onto AgentCore is NOT part of the export, so a console that hides this bucket
 * tells the operator their policy reached AWS when it did not.
 */
export interface AgentCoreUnsupportedItem {
  Item: AgentCoreExportItem
  Reason: string
}

/** agentcore.ExportPlan (exportplan.go:24-34) — PascalCase, nullable slices. */
export interface AgentCoreExportPlan {
  PlanHash: string
  EngineID: string
  Tenant: string
  Creates: AgentCorePlannedChange[] | null
  Updates: AgentCorePlannedChange[] | null
  Deletes: AgentCorePlannedChange[] | null
  /** Names already in the desired state remotely. */
  Unchanged: string[] | null
  /** Remote policies this export does not own and will not touch. */
  Unmanaged: string[] | null
  Unsupported: AgentCoreUnsupportedItem[] | null
}

/** agentCoreExportResultDTO (agentcoreexport.go:116-122) — snake_case. */
export interface AgentCoreExportResultDTO {
  name: string
  op: AgentCoreExportOp
  status: string
  status_reasons?: string[]
  /** Set ONLY for the items that failed. Its presence is the failure signal —
   *  the HTTP status is 200 either way (agentcoreexport.go:204-208). */
  error?: string
}

/** agentCoreExportApplyDTO (agentcoreexport.go:111-114) — snake_case. */
export interface AgentCoreExportApplyDTO {
  plan_hash: string
  results: AgentCoreExportResultDTO[] | null
}

/** agentCoreExportPendingDTO (agentcoreexport.go:105-109) — snake_case, 202. */
export interface AgentCoreExportPendingDTO {
  status: string
  approval_ref: string
  plan_hash: string
}

/** Body of POST /agentcore-export/apply (agentCoreExportApplyBody, :100-103). */
export interface AgentCoreExportApplyInput {
  plan_hash: string
  enforcement_mode?: AgentCoreEnforcementMode
}

/**
 * ⛔ THE APPLY ROUTE HAS THREE 2xx OUTCOMES AND ONLY ONE OF THEM IS "IT WORKED".
 * The status alone does not separate them, so neither does `http.post`, which
 * discards it — hence `postWithMeta` and this union (the shape console/api.ts
 * already uses for the sourcescope 200-vs-202 pair, bindingApplyResult:1025).
 *
 *  - `applied` — 200 and EVERY result carries no `error`.
 *  - `partial` — 200 and at least one result carries an `error`. The engine
 *    attempts the remaining writes after a failure and still answers 200
 *    (exporter.go:383-386 returns the results WITH an ExportApplyError, which
 *    agentcoreexport.go:204-208 renders as 200). Reporting this as success would
 *    tell an operator that Cedar policies reached AWS when some did not.
 *  - `pending` — 202: the governance gate has NOT approved the write yet. It is
 *    neither a success nor a failure, and painting it as either is the lost
 *    third response this repository pays for most often.
 */
export type AgentCoreApplyOutcome =
  | { kind: 'applied'; planHash: string; results: AgentCoreExportResultDTO[] }
  | { kind: 'partial'; planHash: string; results: AgentCoreExportResultDTO[] }
  | { kind: 'pending'; planHash: string; approvalRef: string; status: string }

/** El cuerpo de `POST /v1/m/governance/agents` (`agentidentity.go:32`).
 *
 *  ⛔ `sponsor_ref` es OBLIGATORIO y deny-closed —«a sponsor is mandatory for agent
 *  identities»—, y el motor además exige que ese patrocinador (a) esté en el roster y
 *  (b) sea una identidad HUMANA. Un formulario que lo trate como opcional no relaja nada:
 *  sólo mueve el rechazo al servidor y se lo enseña al operador como un error. */
export interface AgentRegistrationInput {
  identity_ref: string
  sponsor_ref: string
  source?: string
  /** low | medium | high | critical. Opcional; el motor lo pasa a minúsculas. */
  criticality?: string
}

/** Las cuatro criticidades que el motor acepta (`validRiskTier`). */
export const AGENT_CRITICALITIES = [
  'low',
  'medium',
  'high',
  'critical',
] as const

/** El resultado de registrar.
 *
 *  ⛔ EL MOTOR NO DEVUELVE CUERPO: contesta **201** si creó una fila nueva y **200** si
 *  PROMOVIÓ una identidad que ya existía (`WriteHeader`, sin JSON). La distinción viaja sólo
 *  en el código, así que la llamada usa `postWithMeta` — leer el cuerpo daría `undefined` y
 *  decir «creado» ante una promoción sería mentir sobre lo que pasó. */
export interface AgentRegistrationOutcome {
  promoted: boolean
}

/** Un estándar emergente de identidad de agente, del registro «design-toward»
 *  (`modules/governance/emerging.go`).
 *
 *  ⛔ CADA CAMPO ESTÁ AQUÍ PARA NO PROMETER DE MÁS. `seam` nombra dónde encajaría en ESTE
 *  código —no dónde está—, y `caveat` es la nota de honestidad que dice por qué se rastrea y
 *  no se implementa. Una pantalla que muestre `name` y `status` sin `caveat` convierte un
 *  registro de seguimiento en un catálogo de soporte. */
export interface EmergingStandard {
  key: string
  name: string
  body: string
  spec: string
  /** La revisión verificada al documentarla. SE MUEVE: por eso viaja junto a `verified_at`. */
  revision: string
  status: string
  /** El punto de integración FUTURO en este código. Nombrado, no cableado. */
  seam: string
  /** Por qué está rastreado y no implementado. Se muestra siempre. */
  caveat: string
  verified_at: string
  /** URL de la fuente primaria. */
  authority: string
}

/** La respuesta de `GET /v1/m/governance/emerging-identity-standards`.
 *
 *  `verified_at` es DELIBERADAMENTE GRUESO (mes, no día): el motor lo dice en su propio
 *  comentario — «el asunto es que las revisiones se mueven». Se muestra tal cual, sin
 *  formatearlo como una fecha exacta que no es. */
export interface EmergingStandardsResponse {
  standards: EmergingStandard[]
  verified_at: string
  /** Texto del MOTOR, literal: «Design-toward only (IDN-12) …». */
  disclaimer: string
}
