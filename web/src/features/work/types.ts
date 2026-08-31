// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The K1 cross-session work kernel's wire shapes, mirrored BY HAND from the engine.
//
// Hand-written because the generated client cannot reach this surface and that is by
// design, not an oversight: web/src/lib/api/openapi.gen.ts is generated from the STABLE
// openapi.json, which carries zero `/v1/m/` routes (Taskfile.yml:1771 — "consumes only
// the STABLE openapi.json — the beta document feeds the SDK generator"). The module
// routes are specified, with x-required-permission, in web/openapi/openapi.beta.json,
// and that document does not feed the console.
//
// A hand-written mirror can drift from the engine in silence, so it is anchored:
// api.contract.test.ts asserts these shapes against modules/sessions/work_model.go by
// reading the Go source, and fails when a field is added, removed or renamed on either
// side. Every type below cites the Go struct it mirrors.

/** modules/sessions/work_model.go AssessmentVerdict.
 *
 * THREE outcomes, never two. NO_HE_PODIDO_MIRAR is a first-class result — corrupt or
 * unreadable evidence, or an unwired port (contract §"Puertos y fronteras": "Un puerto
 * no cableado produce un desenlace NO_HE_PODIDO_MIRAR, nunca allow ni descarte"). It is
 * not an empty state, not "no results", and not a success. */
export type Verdict = 'LIMPIO' | 'ROTO' | 'NO_HE_PODIDO_MIRAR'

/** The mandatory command phase (work_model.go ExecutionMode). Validate and plan write
 * ZERO rows; plan additionally returns a canonical hash. */
export type WorkMode = 'validate' | 'plan' | 'apply'

/** work_model.go WorkCheck. */
export interface WorkCheck {
  name: string
  verdict: Verdict
  evidence_ref?: string
}

/** work_model.go Assessment — the body of a `mode=validate` response. */
export interface Assessment {
  verdict: Verdict
  code: string
  observed_at: string
  checks: WorkCheck[]
  /** Cleared by the engine in validate mode (work_api.go:214); only plan returns it. */
  plan_hash: string
  resource?: unknown
}

/** work_model.go Plan — the body of a `mode=plan` response. Embeds Assessment. */
export interface Plan extends Assessment {
  command: string
  expected_etag?: string
  row_effects: string[]
  event_type: string
  /** Present when one command appends more than the primary event, in delivery order. */
  event_types?: string[]
  audit_action: string
  permission: string
  external_calls: string[]
}

/** work_model.go CommandResult — the body of a `mode=apply` response.
 *
 * `Replayed` is deliberately `json:"-"` in Go: the replayed body is byte-identical to
 * the original apply's, so replay is observable ONLY through the
 * `Idempotency-Replayed` response header. It is carried in ApplyOutcome, not here. */
export interface CommandResult {
  verdict: Verdict
  code: string
  command_id: string
  result_kind: string
  result_id?: string
  version?: number
  status?: string
  event_id: string
  event_seq: number
  owner_epoch: number
  lease_fence?: number
  plan_hash: string
  audit_seq: number
}

/** work_model.go WorkItem. */
export interface WorkItem {
  id: string
  workspace_id: string
  version: number
  created_at: string
  updated_at: string
  work_kind: string
  title: string
  brief_md: string
  brief_hash: string
  context_refs: unknown
  status: WorkStatus
  priority: WorkPriority
  owner_kind: string
  owner_ref: string
  owner_epoch: number
  provenance_kind: string
  provenance_ref: string
  provenance_hash?: string
  parent_id?: string
  supersedes_id?: string
  acceptance_revision: number
  blocked_code?: string
  blocked_reason?: string
  terminal_code?: string
  terminal_reason?: string
  due_at?: string
  ready_at?: string
  started_at?: string
  review_at?: string
  terminal_at?: string
  /** Present iff the item is archived. The engine's `archived` filter is a TRI-STATE
   * over exactly this column: absent ≠ false. See api.ts listWorkItems. */
  archived_at?: string
  last_event_seq: number
  dependency_blocked: boolean
  claimable: boolean
  /** K2 projections derived by the engine without exposing the lease authority row. */
  leased: boolean
  orphaned: boolean
}

/** modules/sessions/work_state.go workStatuses. */
export type WorkStatus =
  | 'draft'
  | 'ready'
  | 'active'
  | 'blocked'
  | 'review'
  | 'completed'
  | 'failed'
  | 'canceled'

/** work_state.go terminalWorkStatuses — no transition leaves these. */
export const TERMINAL_STATUSES: readonly WorkStatus[] = [
  'completed',
  'failed',
  'canceled',
]

/** work_state.go workPriorities. */
export type WorkPriority = 'p0' | 'p1' | 'p2' | 'p3'

/** work_state.go workOwnerKinds. */
export type OwnerKind = 'user' | 'agent' | 'session'

/** work_state.go workProvenanceKinds. */
export type ProvenanceKind =
  'human' | 'workflow' | 'a2a' | 'mcp' | 'migration' | 'system'

/** work_state.go acceptanceStates. */
export type AcceptanceState = 'pending' | 'passed' | 'failed' | 'waived'

/** work_model.go WorkSnapshot — the GET /work-items/{id} body. */
export interface WorkSnapshot {
  item: WorkItem
  acceptance: AcceptanceCriterion[]
  dependencies: WorkDependency[]
}

/** A row of sessions.work_acceptance. The criterion KEY is durable and immutable
 * identity (contract: "La clave del criterio es identidad durable e inmutable"), so the
 * UI never offers to reassign it. */
export interface AcceptanceCriterion {
  id: string
  work_item_id: string
  criterion_key: string
  ordinal: number
  statement: string
  required: boolean
  state: AcceptanceState
  evidence_ref?: string
  evidence_hash?: string
  waiver_decision_id?: string
}

/** A row of sessions.work_dependency. */
export interface WorkDependency {
  id: string
  work_item_id: string
  depends_on_id: string
  created_at?: string
}

/** A row of sessions.work_decision, field-for-field against
 * modules/sessions/work_schema.go:271-288.
 *
 * `state` is projected ONLY by the DecisionHead view — listCurrentWorkDecisions copies
 * the head's `state` onto the decision row (work_api.go:666) and it is ABSENT from the
 * append-only history rows. Its absence is the machine-readable form of "the engine
 * declines to attribute current state to this row"; see api.ts listDecisions.
 *
 * ⚠ TWO FIELDS HERE WERE WRONG ON THE FIRST PASS, and the way they were wrong is worth
 * keeping: both came from reading the Go CONSTANT's name instead of resolving its VALUE.
 * `colDecisionHeadState` is "state", not "head_state" (work_schema.go:116), and
 * `colDecisionEffectiveAt` is "effective_at", not "decided_at" (:112). The first meant
 * `state` was never populated, so every row — including in the effective view —
 * rendered "this view does not attribute state" and the revoke button was permanently
 * hidden. The second left the timestamp blank on every row. Neither produced an error,
 * a warning or a failing test: the fields simply arrived undefined.
 *
 * That is exactly the silent drift a hand-written client is exposed to, and it is why
 * this interface is now IN the contract test (api.contract.test.ts) rather than trusted.
 * There is no `state` column on the decision table itself, so the projection cannot
 * collide with a real one — checked, because an overwritten domain field would have
 * been a far quieter bug. */
export interface WorkDecision {
  id: string
  workspace_id: string
  work_item_id: string
  decision_key: string
  decision_seq: number
  subject_kind: string
  subject_ref: string
  operation: string
  statement_md: string
  rationale_md: string
  decided_by_kind: string
  decided_by_ref: string
  authority_ref: string
  supersedes_id?: string
  revokes_id?: string
  effective_at: string
  decision_hash: string
  /** Present ONLY in the DecisionHead projection. Never on a history row. */
  state?: 'effective' | 'revoked'
}

/** A row of sessions.work_event, as GET /work-items/{id}/events returns it. */
export interface WorkEventRow {
  id: string
  workspace_id: string
  aggregate_kind: string
  aggregate_id: string
  seq: number
  type: string
  occurred_at: string
  payload?: string
}

/** The SSE frame from GET /work-stream (work_api.go:781-786). Note the field is
 * `event_id`, not `id`, and `seq` is a number. */
export interface WorkStreamEvent {
  event_id: string
  workspace_id: string
  aggregate_kind: string
  aggregate_id: string
  seq: number
  type: string
  occurred_at: string
  payload?: unknown
}

/** The work kernel's page envelope. It is NOT lib/api/types.ts ListResponse: that one
 * names the continuation token `cursor`, the kernel names it `next_cursor`
 * (work_model.go WorkPage, and the map literals at work_api.go:480 and :572).
 * Reusing the shared type here would read `undefined` and silently paginate once. */
export interface WorkPage<T> {
  items: T[]
  next_cursor: string
  has_more: boolean
}

/** The engine's error body for this surface (work_api.go writeWorkError:357-360). It
 * carries the VERDICT alongside the code, which the generic error envelope does not. */
export interface WorkErrorBody {
  verdict: Verdict
  code: string
  error: { code: string; message: string }
}

/** ── K2 leases ──────────────────────────────────────────────────────────────────────────
 * `modules/sessions/work_model.go:289` WorkLease. The console painted only the derived
 * `leased: boolean` above and had no type for the row itself, which is why it could show
 * THAT an item is leased and nothing about WHO holds it, until when, or whether it is live.
 */
export interface WorkLease {
  id?: string
  workspace_id: string
  work_item_id: string
  /** ⛔ The LEASE ROW's version. NOT what If-Match takes — see getLease in api.ts. */
  version?: number
  holder_sid?: string
  holder_run_ref?: string
  holder_agent_ref?: string
  fence: number
  state: string
  acquired_at?: string
  renewed_at?: string
  expires_at?: string
  ended_at?: string
  end_reason?: string
  renewal_count: number
  live: boolean
  /** Three outcomes, never two — the same Verdict the rest of this kernel uses. */
  liveness_verdict: Verdict
  liveness_code: string
}

export interface WorkLeasePage {
  items: WorkLease[]
  next_cursor: string
  has_more: boolean
}

/** The filters the engine ACCEPTS, and it is a STRICT allowlist: any other key, or a repeated
 *  one, is `400 invalid_command` (`work_api.go:649-668`). The type is closed on purpose so the
 *  query is built FROM it rather than forwarded from a caller — one stray `?q=` does not
 *  degrade the list, it fails it. */
export interface LeaseFilters {
  limit?: number
  cursor?: string
  work_item_id?: string
  holder_sid?: string
  state?: string
  expires_before?: string
}

/* ⛔ AQUÍ VIVÍAN `LeaseCommand` y `LeaseCommandBody`, y se retiraron el 2026-08-16 con la función
 * que los usaba. Un contraste `sol max` encontró que `commandLease` posteaba sin `mode` —el motor
 * rechaza sus seis rutas— y que sus ÚNICOS llamantes eran dos celdas de prueba: producción había
 * pasado a levantar intenciones y nadie la llamaba. Es decir, dos pruebas mantenían viva una
 * función rota. Los comandos de lease son `WorkCommandName` como los demás; su cuerpo lo componen
 * `requiredFieldsFor` y `foldFields`. Si vuelves a necesitar un tipo aquí, pregunta antes por qué
 * la ceremonia no te sirve. */
