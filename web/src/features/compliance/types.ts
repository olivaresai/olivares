// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// DTOs for module XIII (Compliance), mirroring the UI data contract
// (docs/UI-CONTRACT-COMPLIANCE.md). The product rule (docs/SECURITY-HARDENING.md) is encoded in the
// shape: a control has a `status` (satisfied | by_design | partial | gap | unmapped),
// NEVER "compliant"/"certified"; `by_design` (a design guarantee, no telemetry) is a
// DISTINCT value from `satisfied` (operational evidence). Every reporting payload
// carries a `disclaimer` the view ALWAYS renders. No payloads/secrets cross the wire —
// only control status, capability state, and tamper-evidence fingerprints.

/** A control's evaluated state. `unmapped`/`gap` are honest VALUE for the auditor
 *  (what the platform does not yet evidence), not load errors. */
export type ControlStatus =
  'satisfied' | 'by_design' | 'partial' | 'gap' | 'unmapped'

/** A capability is either operational (live telemetry) or architectural (provided by
 *  design, cited) — the view shows them with a different icon. Widened with `| string`
 *  because the catalog (modules/compliance/capabilities.go) can add classes. */
export type CapabilityClass = 'operational' | 'architectural' | string

/** Whether the capability is observed present, observed absent, or not yet known. */
export type CapabilityState = 'present' | 'absent' | 'unknown'

/** EU AI Act effective tier. `unacceptable` only appears after human review — the
 *  heuristic NEVER assigns it. */
export type RiskTier = 'unacceptable' | 'high' | 'limited' | 'minimal'

/** Review lifecycle of a risk classification. */
export type RiskState = 'suggested' | 'approved' | 'overridden'

/** The five-way status roll-up rendered as a StatusBar. */
export interface StatusSummary {
  total: number
  satisfied: number
  by_design: number
  partial: number
  gap: number
  unmapped: number
}

/** A reference behind a capability's evidence (a ledger anchor, a config citation). */
export interface CapabilityRef {
  kind: string
  detail: string
}

/** One capability the control depends on, with its live state for the tenant. */
export interface ControlCapability {
  key: string
  class: CapabilityClass
  state: CapabilityState
  detail: string
  count?: number
  refs?: CapabilityRef[]
}

/** A single control's assessment within a framework. */
export interface ControlAssessment {
  control_id: string
  title: string
  requirement: string
  criterion: string
  status: ControlStatus
  /** Honest coverage caveat, if any — ALWAYS rendered when present. */
  note: string
  capabilities: ControlCapability[]
  /** Only present on the gap analysis: which capabilities to turn on to close it. */
  missing_capabilities?: string[]
}

// --- framework grouping (client-side, NOT recomputed from the backend) -------

/** How a framework relates to conformance. A REGULATORY framework / standard can be
 *  evidenced against; a DESIGN-TOWARD crosswalk is a positioning/threat-model map the
 *  platform signals against but makes NO conformance claim (docs/SECURITY-HARDENING.md). This split is
 *  a fixed, in-repo PRESENTATION grouping of the framework ids the backend returns — it
 *  derives nothing from per-tenant data, so it is not a recompute. The authoritative
 *  no-conformance-claim wording stays the framework's own `disclaimer` from the engine. */
export type FrameworkGroup = 'regulatory' | 'crosswalk'

/** The five design-toward crosswalks (modules/compliance/frameworks.go) — threat
 *  models / guidance / in-development overlays, never a conformance target. Every other
 *  framework id the catalog returns is treated as `regulatory`. */
export const CROSSWALK_FRAMEWORK_IDS = [
  'nist_ai_600_1',
  'csa_maestro',
  'owasp_agentic_tm',
  'cisa_agentic_adoption',
  'nist_cosais',
] as const

/** Frameworks whose mapping is explicitly IN DEVELOPMENT (no final standard) — the
 *  most prominent honesty caveat. nist_cosais is a concept paper (Aug 2025) and its
 *  AIIM control produces no specifications (frameworks.go nist_cosais). */
export const IN_DEVELOPMENT_FRAMEWORK_IDS = ['nist_cosais'] as const

/** Pure classifier — fixed id set, no per-tenant input. */
export function frameworkGroup(id: string): FrameworkGroup {
  return (CROSSWALK_FRAMEWORK_IDS as readonly string[]).includes(id)
    ? 'crosswalk'
    : 'regulatory'
}

export function isInDevelopmentFramework(id: string): boolean {
  return (IN_DEVELOPMENT_FRAMEWORK_IDS as readonly string[]).includes(id)
}

// --- (1) framework catalog ---------------------------------------------------

/** A framework as listed for the selector. `controls` is a count, not the list. */
export interface FrameworkSummary {
  id: string
  name: string
  version: string
  authority: string
  controls: number
}

/** GET /frameworks */
export interface FrameworkListResponse {
  items: FrameworkSummary[]
  disclaimer: string
}

// --- (2) framework status ----------------------------------------------------

export interface FrameworkAssessment {
  framework: string
  name: string
  version: string
  /** The framework's OWN disclaimer (modules/compliance/frameworks.go Framework.Disclaimer,
   *  carried by assessFramework — assess.go:73). For the design-toward crosswalks this
   *  is the explicit no-conformance-claim banner the view renders PROMINENTLY (docs/SECURITY-HARDENING.md).
   *  Distinct from FrameworkStatusResponse.disclaimer (the generic reportDisclaimer). */
  disclaimer: string
  summary: StatusSummary
  controls: ControlAssessment[]
}

/** GET /frameworks/{id}/status */
export interface FrameworkStatusResponse {
  assessment: FrameworkAssessment
  disclaimer: string
}

// --- (3) gap analysis --------------------------------------------------------

/** GET /frameworks/{id}/gaps */
export interface GapAnalysisResponse {
  framework: string
  name: string
  summary: StatusSummary
  gaps: ControlAssessment[]
  disclaimer: string
}

// --- (5) cross-framework summary (dashboard header) --------------------------

export interface FrameworkRollup {
  framework: string
  name: string
  version: string
  summary: StatusSummary
}

/** GET /summary */
export interface ComplianceSummaryResponse {
  frameworks: FrameworkRollup[]
  disclaimer: string
}

// --- (5b) capability catalog -------------------------------------------------

// `CapabilityClass` and `CapabilityState` are declared at the top of this file and
// reused here on purpose — they existed before any screen consumed them (measured:
// zero references outside this file), and re-declaring them locally would have made
// the console disagree with itself about the same wire values. Note `CapabilityClass`
// is deliberately widened with `| string`: the engine catalog can add a class, so the
// view must survive one it has no label for.

/** Points at the underlying evidence without copying it — minimal-data by design
 *  (docs/SECURITY-HARDENING.md). `kind` is "audit_chain" | "entity" | "design" | "attestation".*/
export interface EvidenceRef {
  kind: string
  detail: string
}

/** One capability evaluated against live tenant evidence.
 *
 *  ⛔ `count` and `more` are `omitempty` in Go (`types.go:121-134`), so **absent and
 *  zero are the same bytes on the wire**. That is why `count` is optional here and
 *  why the view keys the count off `class`, never off the field being present: for
 *  an architectural capability there is nothing to count, and `0` would be a claim.
 *
 *  ⛔ `more: true` means the count was TRUNCATED at the page cap — the number is a
 *  FLOOR, not a total. Rendered without that, a truncated count reads as exact. */
export interface CapabilityEvidence {
  key: string
  class: CapabilityClass
  state: CapabilityState
  detail: string
  count?: number
  more?: boolean
  refs?: EvidenceRef[]
}

/** `GET /v1/m/compliance/capabilities` — the catalog with the tenant's live evidence
 *  state. The engine emits it as a `map[string]any` (`report.go:121-137`), not a
 *  struct, so a struct-tag sweep does not see this shape at all. */
export interface CapabilityCatalogResponse {
  capabilities: CapabilityEvidence[]
  disclaimer: string
}

// --- (6) audit evidence ------------------------------------------------------

/** A sealed, auto-audited evidence package. It is control-status + tamper-evidence,
 *  NEVER a "certificate". `integrity_ok` drives the IntegrityBadge. */
export interface EvidencePackage {
  id: string
  framework: string
  framework_version: string
  generated_at: string
  generated_by: string
  ledger_seq: number
  ledger_hash: string
  integrity_ok: boolean
  integrity_checked: number
  integrity_reason: string
  summary: StatusSummary
  manifest_hash: string
  scope_note: string
  disclaimer: string
}

/** GET /evidence */
export interface EvidenceListResponse {
  items: EvidencePackage[]
  disclaimer: string
}

/** POST /frameworks/{id}/evidence body. */
export interface EvidenceInput {
  scope_note?: string
}

// --- evidence export (FIN-10) ------------------------------------------------

/** Wire format of GET /evidence/{id}/export?format=. `json` is the full package +
 *  manifest + integrity proof; `csv` is a flat control table (raw text/csv); `oscal`
 *  is a NIST OSCAL v1.2.2 bundle (a JSON object: component-definition + assessment-results
 *  + control-mapping). */
export type EvidenceExportFormat = 'json' | 'csv' | 'oscal'

/** OSCAL finding status enum (NIST OSCAL v1.2.2 — modules/compliance/oscal.go:39).
 *  It is a 2-VALUE enum: ONLY a control with live operational evidence is `satisfied`;
 *  by_design/partial/gap/unmapped all map to `not-satisfied`. The real product status
 *  is preserved in `status.reason` so a by_design control is NEVER laundered to
 *  "satisfied" — the view renders e.g. "not-satisfied (by_design)" (docs/SECURITY-HARDENING.md).*/
export type OscalFindingState = 'satisfied' | 'not-satisfied'

/** One OSCAL finding's status block (findings[].target.status). `reason` carries the
 *  precise ControlStatus the export must show alongside the laundered-proof OSCAL state. */
export interface OscalFindingStatus {
  state: OscalFindingState
  /** The precise product ControlStatus (satisfied|by_design|partial|gap|unmapped). */
  reason: string
  remarks?: string
}

export interface OscalFinding {
  uuid: string
  title: string
  description: string
  target: {
    type: string
    'target-id': string
    status: OscalFindingStatus
  }
  props?: Array<{ name: string; ns?: string; value: string }>
}

/** The OSCAL bundle returned for format=oscal. Hand-authored to the subset the view
 *  reads (oscal.go oscalDocument) — the engine emits the full OSCAL component-definition
 *  + assessment-results; widened with index signatures kept off so unknown keys are
 *  ignored, not mis-typed. */
export interface OscalExport {
  oscal_version: string
  'assessment-results': {
    results: Array<{
      uuid: string
      title: string
      findings: OscalFinding[]
    }>
  }
  disclaimer: string
}

/** The result of an evidence export. `text` is the EXACT server output (for download /
 *  copy — never recomputed client-side); `oscal` is the parsed bundle for json/oscal so
 *  the view can render the OSCAL finding-status honesty. CSV carries only `text`. */
export interface EvidenceExportResult {
  format: EvidenceExportFormat
  /** Suggested download filename (built from the package id + format). */
  filename: string
  /** MIME type the server returned. */
  content_type: string
  /** Raw response body, byte-for-byte as the server produced it. */
  text: string
  /** Parsed OSCAL bundle when format=oscal (so findings/status are renderable). */
  oscal?: OscalExport
}

// --- (7) agent risk register -------------------------------------------------

/** Why a tier was assigned — the operator-readable signals behind the heuristic. */
export interface RiskSignals {
  rw_edges: number
  total_edges: number
  distinct_resources: number
  high_severity_findings: number
  scheduled: boolean
  autonomous: boolean
}

/** GET /risk item. */
export interface RiskClassification {
  id: string
  subject_kind: string
  subject_ref: string
  agent_id: string
  /** Effective tier (after any review). */
  tier: RiskTier
  /** Heuristic suggestion — NEVER `unacceptable`. */
  suggested_tier: RiskTier
  state: RiskState
  rationale: string
  nist_functions: string[]
  signals: RiskSignals
  reviewed_by: string
  classified_at: string
  disclaimer: string
}

/** POST /risk/classify body. */
export interface RiskClassifyInput {
  subject_ref: string
  subject_kind?: string
  agent_id?: string
}

/** POST /risk/{id}/review body. */
export interface RiskReviewInput {
  tier: RiskTier
  note?: string
}

// --- (8) data residency ------------------------------------------------------

/** GET /residency item. */
export interface ResidencyAttestation {
  id: string
  region: string
  perimeter: string
  self_hosted: boolean
  encryption_at_rest: boolean
  data_classes: string[]
  attested_by: string
  attested_at: string
  violations_observed: number
  last_checked: string
  note: string
}

/** One egress signal a scan observed. */
export interface ResidencyScanSignal {
  source: string
  ref: string
  detail: string
}

/** POST /residency/scan report. */
export interface ResidencyScanReport {
  regions_checked: number
  egress_signals: number
  violations: number
  findings_emitted: number
  signals: ResidencyScanSignal[]
  disclaimer: string
}

/** POST /residency body. */
export interface ResidencyInput {
  region: string
  perimeter?: string
  self_hosted?: boolean
  encryption_at_rest?: boolean
  data_classes?: string[]
  note?: string
}

// --- (9) data classes (§2 registry) ------------------------------------------

/** GET /retention/classes item (modules/compliance/retention.go:69 dataClassDTO).
 *  The console needs the registry for ONE reason above display: a data_class-scope
 *  legal hold is rejected unless `data_class` equals a registered id EXACTLY
 *  (holds.go:309). Free-texting that field is how an operator believes they
 *  preserved something they did not, so the console offers the registry, never a
 *  text box. */
export interface DataClassEntry {
  id: string
  ext_kinds?: string[]
  age_column?: string
  purgeable: boolean
  model_io: boolean
  recommended_days?: number
  subject_kinds?: string[]
  note?: string
  provider_floor_days?: number
  provider_floor_known?: boolean
  provider_floor_source?: string
  /** enterprise regulatory floor in force for this class. Absent in the
   *  open-core build (the governor is nil, retention.go:196). */
  regulatory_floor?: RetentionFloor
}

/** GET /retention/classes */
export interface DataClassListResponse {
  items: DataClassEntry[]
  disclaimer?: string
}

// --- (9-bis) retention schedules, sweeps and certificates — ---
//
// Five of the six engine routes registered at compliance.go:575-580 had no console
// at all; only GET /retention/classes did, as the class dropdown of the holds
// dialog. The DTOs below mirror modules/compliance/retention.go 1:1 — field for
// field, name for name — because everything on this screen is a claim about
// DESTRUCTION and a console that renames a field ends up paraphrasing one.

/** A schedule's disposition (retention.go:34-35). `retain` documents a window and
 *  destroys nothing; `purge` is the one that deletes, and enabling it is gated. */
export type RetentionDisposition = 'retain' | 'purge'

/** A named regulatory minimum (retentiongovernor.go:53-64). A FLOOR, never a cap:
 *  the sweep will not delete rows younger than `min_days`, and a purge schedule
 *  shorter than it is refused at author time (retention.go:262-267). */
export interface RetentionFloor {
  class: string
  min_days: number
  basis: string
  /** `compliance` = locked (the schedule cannot even be deleted, retention.go:405);
   *  `governance` = break-glass. */
  mode: string
}

/** GET /retention/policies item (retention.go:87-105 retentionPolicyDTO). */
export interface RetentionPolicy {
  id: string
  data_class: string
  retention_days: number
  disposition: RetentionDisposition
  enabled: boolean
  basis?: string
  /** Present only when a human approval enabled a purge (retention.go:300). */
  approval_ref?: string
  model_io: boolean
  provider_floor_days?: number
  /** Always serialized: the embedded providerFloor carries no omitempty on this one
   *  (retention.go:66), so absent here would be a shape the engine cannot produce. */
  provider_floor_known: boolean
  provider_floor_source?: string
  /** max(retention_days, provider floor, regulatory floor) — the ONLY number a
   *  tenant may honestly disclose as "gone everywhere after" (retention.go:97-100).
   *  Omitted when the floor is unknown: absent means "we cannot say", never 0. */
  effective_disclosure_days?: number
  regulatory_floor?: RetentionFloor
}

/** PUT /retention/policies/{class} body (retention.go:225-230 putPolicyRequest).
 *
 *  ⛔ THESE FOUR KEYS AND NO OTHERS. The engine decodes with
 *  DisallowUnknownFields (helpers.go:99), so one extra field — `data_class`, which
 *  the path already carries, is the obvious mistake — is a flat 400 for a request
 *  that is otherwise perfectly formed. s696_test.go sends this exact payload
 *  through the real router to keep the two ends from drifting. */
export interface RetentionPolicyInput {
  retention_days: number
  disposition: RetentionDisposition
  basis?: string
  /** Omitted ⇒ the engine defaults to true (retention.go:270). */
  enabled?: boolean
}

/** The 202 body of a PUT that opened an approval instead of enabling a purge
 *  (retention.go:315). The schedule IS persisted — with enabled=false. */
export interface RetentionPolicyPending {
  status: 'pending_approval'
  approval_ref?: string
}

/** GET /retention/runs item (retention.go:107-123 retentionRunDTO): one sealed,
 *  append-only destruction certificate. This is the log of destruction an auditor
 *  asks for, so the console renders its counts and anchors verbatim. */
export interface RetentionRun {
  id: string
  data_class: string
  /** `sweep` = the engine loop, `manual` = a console/API sweep (retention.go:37-38). */
  trigger: string
  cutoff: string
  examined: number
  purged: number
  excluded_held: number
  skipped_class_hold: boolean
  /** The run hit the per-class iteration cap and did NOT finish (retention.go:591). */
  truncated: boolean
  policy_id: string
  approval_ref?: string
  ledger_seq: number
  ledger_hash?: string
  manifest_hash: string
  occurred_at: string
}

/** One class's outcome inside a sweep pass (retention.go:509-518). */
export interface RetentionClassResult {
  data_class: string
  cutoff: string
  examined: number
  purged: number
  excluded_held: number
  skipped_class_hold: boolean
  truncated: boolean
  run_id?: string
}

/** POST /retention/sweep 200 body (retention.go:498-506). */
export interface RetentionSummary {
  trigger: string
  classes: RetentionClassResult[] | null
  examined: number
  purged: number
  excluded_held: number
  skipped_class_holds: number
  truncated: boolean
}

// --- (10) legal holds — Preservation plane (E1) --------------------------

/** A hold's matching scope (modules/compliance/holds.go:34-37). The three are
 *  MUTUALLY EXCLUSIVE in what they carry, and the engine rejects a mismatch
 *  (holds.go:302-329): tenant takes nothing, data_class takes a registered class
 *  and no subject, subject takes (kind, ref) and no class. */
export type HoldScopeKind = 'tenant' | 'data_class' | 'subject'

/** Lifecycle (holds.go:39-40). Setting is immediate; releasing is dual-control. */
export type HoldStatus = 'active' | 'released'

/** Custody-trail vocabulary (holds.go:42-44). Every transition seals one
 *  append-only, ledger-anchored event — this is the chain of custody an auditor
 *  reads, so the console renders it verbatim rather than summarising it. */
export type HoldEventKind = 'set' | 'release_requested' | 'released'

/** GET /holds item (holds.go:203 legalHoldDTO). */
export interface LegalHold {
  id: string
  matter_ref: string
  title?: string
  scope_kind: HoldScopeKind
  data_class?: string
  subject_kind?: string
  subject_ref?: string
  reason: string
  status: HoldStatus
  created_by: string
  created_at: string
  released_by?: string
  released_at?: string
  release_approval_ref?: string
}

/** One custody event (holds.go:239 holdEventDTO). `ledger_seq`/`ledger_hash`
 *  anchor the event to the audit ledger head at the time it was sealed. */
export interface HoldEvent {
  hold_id: string
  event: HoldEventKind
  actor: string
  actor_kind: string
  on_behalf_of?: string
  note?: string
  approval_ref?: string
  approvers?: string[]
  ledger_seq: number
  ledger_hash?: string
  occurred_at: string
}

/** POST /holds body (holds.go:255 createHoldRequest). */
export interface CreateHoldInput {
  matter_ref: string
  title?: string
  scope_kind: HoldScopeKind
  data_class?: string
  subject_kind?: string
  subject_ref?: string
  reason: string
  on_behalf_of?: string
}

/** One hold covering a queried subject (holds.go:76 HoldRef). */
export interface HoldRef {
  id: string
  matter_ref: string
  scope_kind: HoldScopeKind
}

/** GET /holds/check answer (holds.go:85 HoldDecision) — the SAME §4 matching rule
 *  the erasure path runs before destroying anything. The console uses it as a
 *  PREVIEW: it is the only way to show an operator what a hold already covers
 *  before they confirm one, without asking the engine to simulate anything. */
export interface HoldDecision {
  held: boolean
  holds?: HoldRef[]
}

/** POST /holds/{id}/release body (holds.go:499). */
export interface ReleaseHoldInput {
  reason?: string
  on_behalf_of?: string
}

/** The 202 envelope while dual control is pending (holds.go:505). A release that
 *  returns this has NOT lifted the hold — preservation is still in force. */
export interface ReleaseHoldResult {
  status: string
  approval_ref?: string
  detail?: string
}

// --- (11) right to erasure / DSAR — (E2) --------------------------------

/** Erasure lifecycle (erasure.go:69-76). `completed`/`completed_with_gaps` are
 *  TERMINAL and immutable; the rest are re-executable. `blocked_hold` means the
 *  legal-hold gate vetoed it — that is the system working, not a failure.
 *
 *  Declared as a VALUE, with the type derived from it, so the console's allowlist
 *  guard can walk the whole vocabulary at runtime. A bare union cannot be
 *  enumerated, which is why the console guard could only ever test examples while
 *  the Go one tested the set — measured as a false green in the fifth round. */
export const ERASURE_STATUSES = [
  'received',
  'pending_approval',
  'executing',
  'blocked_hold',
  'denied',
  'failed',
  'completed',
  'completed_with_gaps',
] as const

export type ErasureStatus = (typeof ERASURE_STATUSES)[number]

/** The ONE status that means a clean erasure. Everything else warns. */
export const ERASURE_STATUS_CLEAN: ErasureStatus = 'completed'

/** Compile-time guard that `ErasureStatus` stays DERIVED from the value above.
 *
 *  Deriving it today is not the same as it staying derived: widening the alias
 *  by hand (`ErasureStatus = ... | 'something_new'`) type-checks fine and leaves
 *  every runtime test green, while the allowlist guard walks a vocabulary that no
 *  longer matches the type. Measured in the sixth-round contrast.
 *
 *  This asserts mutual assignability, so widening EITHER side stops compiling. */
type MutuallyAssignable<A, B> = [A] extends [B]
  ? [B] extends [A]
    ? true
    : { error: 'ErasureStatus is wider than ERASURE_STATUSES' }
  : { error: 'ERASURE_STATUSES is wider than ErasureStatus' }

const _erasureStatusStaysDerived: MutuallyAssignable<
  ErasureStatus,
  (typeof ERASURE_STATUSES)[number]
> = true
void _erasureStatusStaysDerived

/** Custody vocabulary for an erasure (erasure.go:83-95). */
export type ErasureEventKind =
  | 'received'
  | 'hold_blocked'
  | 'coordinator_blocked'
  | 'approval_requested'
  | 'executed'
  | 'account'
  | 'provider'
  | 'files_store'
  | 'key_shredded'
  | 'sealed'
  | 'failed'

/** GET /erasure item (erasure.go:134 erasureRequestDTO). `subject` is
 *  detokenized while the subject key lives and becomes the permanent "[ERASED]"
 *  stand-in after the crypto-shred — the console shows that transition rather
 *  than hiding it, because it is the visible proof the shred happened. */
/** Un fichero del almacén de Anthropic, tal como el motor lo enumera (`ports.go:244`).
 *  Deliberadamente pobre: el almacén NO lleva metadatos de sujeto, y por eso el borrado
 *  por sujeto no puede seleccionar desde aquí. */
export interface ClaudeFileRef {
  id: string
  mime_type?: string
  size_bytes?: number
  created_at?: string
  scope_id?: string
}

/** El inventario (`claudefiles.go` `filesInventoryDTO`).
 *
 *  ⛔ `wired` es lo que separa «no hay ficheros» de «la costura no está conectada»: con el
 *  borrador sin cablear el motor devuelve el DTO con `wired:false`, `count:0` y la
 *  divulgación intacta — NUNCA un «0 ficheros» que se leería como inventario limpio. */
export interface ClaudeFilesInventory {
  wired: boolean
  count: number
  total_bytes: number
  /** Texto del MOTOR, literal. Dice que el almacén es compartido entre las claves del
   *  workspace, persistente y NO zero-data-retention, y que sin metadatos de sujeto el
   *  RTBF por sujeto no puede seleccionar desde el almacén. Se muestra tal cual. */
  disclosure: string
  files?: ClaudeFileRef[]
}

/** El resultado del borrado gobernado (`claudefiles.go` `fileEraseResultDTO`).
 *
 *  ⛔ SIETE estados sobre SEIS códigos HTTP: 503 vale para `not_wired` y para `error`, y 403
 *  para tres denegaciones distintas. El código NO identifica el resultado — se lee `status`
 *  del cuerpo, que en un no-2xx viaja en `ApiError.body` (el cliente lo conserva a propósito). */
export type ClaudeFileEraseStatus =
  'deleted' | 'held' | 'pending' | 'denied' | 'failed' | 'not_wired' | 'error'

export interface ClaudeFileEraseResult {
  status: ClaudeFileEraseStatus
  file_id: string
  confirmation_id?: string
  approval_ref?: string
  holds?: { id?: string; name?: string; reason?: string }[]
  detail?: string
}

export interface ErasureRequest {
  id: string
  subject_kind: string
  subject_token: string
  subject?: string
  aliases?: string[]
  data_classes: string[]
  case_ref: string
  reason: string
  requested_by: string
  status: ErasureStatus
  approval_ref?: string
  created_at: string
}

/** One erasure custody event (erasure.go:149 erasureEventDTO). */
export interface ErasureEvent {
  erasure_id: string
  event: ErasureEventKind
  actor: string
  actor_kind: string
  note?: string
  approval_ref?: string
  approvers?: string[]
  ledger_seq: number
  ledger_hash?: string
  occurred_at: string
}

/** What one erasure target reported (erasure.go targetOutcome). */
export interface ErasureTargetOutcome {
  target?: string
  label?: string
  rows?: number
  ok?: boolean
  note?: string
  reason?: string
}

/** One deliberately retained record class + its legal basis (erasure.go:102).
 *  This is the erasure↔retention reconciliation: what SURVIVES the erasure and
 *  why that is lawful. An RTBF receipt that did not say this would be the
 *  dishonest kind. */
export interface RetainedRecord {
  records: string
  basis: string
}

/** GET /erasure/{id}/receipt (erasure.go:162 erasureReceiptDTO) — the verifiable
 *  artefact of an executed erasure: ledger-anchored, manifest-hashed, and honest
 *  about the provider floor it cannot delete. */
export interface ErasureReceipt {
  erasure_id: string
  subject_kind: string
  subject_token: string
  targets: ErasureTargetOutcome[]
  account_outcome: string
  provider_outcome: string
  key_shredded: boolean
  verify_ok: boolean
  verify_checked: number
  verify_reason?: string
  retained: RetainedRecord[]
  case_ref: string
  approval_ref?: string
  ledger_seq: number
  ledger_hash?: string
  manifest_hash: string
  occurred_at: string
  provider_floor_days?: number
  provider_floor_known?: boolean
  provider_floor_source?: string
}

/** POST /erasure body (erasure.go:223 createErasureRequest). */
export interface CreateErasureInput {
  subject_kind: string
  subject_ref: string
  aliases?: string[]
  data_classes?: string[]
  case_ref: string
  reason?: string
}

/** POST /erasure/{id}/execute body (erasure.go:556). */
export interface ExecuteErasureInput {
  reason?: string
  provider_user_ids?: string[]
}

/** The 202 envelope for an execute that did NOT finish.
 *
 *  It represents BOTH 202s and they are not the same news: `pending_approval`
 *  means nothing was destroyed, while `provider_pending` (erasure.go:1233) comes
 *  back after local targets and the account leg already ran. Do not read this
 *  type as "nothing was erased" — read `status`. */
export interface ExecuteErasureResult {
  status: string
  approval_ref?: string
  detail?: string
}

/** GET /data-subjects/{id}/erasure-status (erasure.go:577). The per-subject view
 *  a DPO answers an Art. 15/17 request with. */
export interface DataSubjectErasureStatus {
  subject_id: string
  subject_kind: string
  state: string
  request?: ErasureRequest
  receipt?: ErasureReceipt
  key_shredded: boolean
  verified: boolean
  verify_reason?: string
  approval_ref?: string
  disclaimer: string
}

/** POST /data-subjects/{id}/erase body (erasure.go:568). */
export interface DataSubjectEraseInput {
  subject_kind?: string
  aliases?: string[]
  data_classes?: string[]
  case_ref?: string
  reason?: string
  provider_user_ids?: string[]
}

/** The engine's 423 body when a legal hold vetoes an erasure
 *  (erasure.go:675-679) — adopted VERBATIM from the contract §2.4. The
 *  console renders the covering holds, because "blocked" without "by what" sends
 *  the operator back to curl. */
export interface LegalHoldBlockedError {
  code: 'legal_hold'
  message: string
  holds: HoldRef[]
}

// --- (12) DORA register + incidents, OSCAL profiles (E3) ---------------------

/** A validation/reconciliation issue on a DORA register (regpackage.go:126). */
export interface RegisterIssue {
  severity: 'error' | 'warning' | 'info'
  template?: string
  field?: string
  message: string
}

/** GET /dora/register item (regpackage.go:181 registeredRegisterDTO). */
export interface DoraRegister {
  id: string
  regulation: string
  entity_lei: string
  entity_name?: string
  reference_date?: string
  templates?: Record<string, unknown>
  validation?: RegisterIssue[]
  reconciliation?: RegisterIssue[]
  counts?: Record<string, number>
  error_count: number
  note?: string
  doc_sha256: string
  generated_by: string
  generated_at: string
  ledger_anchor?: Record<string, unknown>
  disclaimer: string
}

/** GET /dora/incidents item (doraincident.go:34 classifiedIncidentDTO). */
export interface DoraIncident {
  id: string
  reference: string
  finding_id?: string
  major: boolean
  provisional: boolean
  critical_services: boolean
  criteria_met?: string[]
  rationale?: string
  report?: Record<string, unknown>
  deadlines?: Record<string, unknown>
  basis?: Array<Record<string, string>>
  note?: string
  doc_sha256: string
  classified_by: string
  classified_at: string
  ledger_anchor?: Record<string, unknown>
  disclaimer: string
}

// --- (12b) NIS 2 significant-incident classification ------------------

/** The Art 23(4) reporting phases, in the engine's own order. Transitions are
 *  FORWARD-ONLY: the engine compares ordinals and answers 409 on a backward or
 *  same-phase move (nis2incident.go:295, nis2seam.go:99-101). The console offers
 *  only the phases ahead of the current one, so the rule is visible rather than
 *  discovered through a rejected request. */
export const NIS2_PHASES = [
  'early_warning',
  'notification',
  'intermediate',
  'final',
] as const

export type Nis2Phase = (typeof NIS2_PHASES)[number]

/** Is this a phase this build can ORDER? Anything else — a value from a newer
 *  engine vocabulary, or a hand-written row — cannot be placed on the timeline, and
 *  the console has to say so rather than guess. */
export function isKnownNis2Phase(phase: string): phase is Nis2Phase {
  return (NIS2_PHASES as readonly string[]).includes(phase)
}

/** The phases a classification may legally move to from `current` — the console
 *  side of the engine's forward-only rule.
 *
 *  ⛔ AN UNKNOWN PHASE OFFERS NOTHING. The first version returned the whole
 *  vocabulary, reasoning that the engine rejects a bad move anyway. The Codex sol
 *  max contrast refuted that on both halves (F4/F6/F8):
 *
 *    - If the stored value comes from a NEWER vocabulary, every phase this build
 *      knows is BEHIND it, so the menu offered was guaranteed-409 from top to
 *      bottom — the opposite of the rule it exists to render.
 *    - And the engine does not catch it either: nis2PhaseIndex returns -1 for an
 *      unknown current phase (nis2seam.go:113-120), so `-1 < any` and the
 *      forward-only check at nis2incident.go:295 PASSES for every target. The
 *      column is unconstrained text (schema.go:982-998). Both sides fail open, so
 *      "the engine is the authority" was not a fallback at all.
 *
 *  Empty is the honest answer: the caller renders "this build cannot order that
 *  phase" instead of a menu whose every option is wrong. */
export function nis2PhasesAfter(current: string): Nis2Phase[] {
  const i = (NIS2_PHASES as readonly string[]).indexOf(current)
  return i < 0 ? [] : NIS2_PHASES.slice(i + 1)
}

/** The engine REJECTS an over-length incident reference rather than truncating it
 *  (nis2incident.go:132-135): a clamped reference would persist as a DIFFERENT
 *  identity that exact matching can never reach again. The console mirrors the
 *  rule so the operator learns it while typing, not after losing a filled-in
 *  impact document to a 400. */
export const NIS2_MAX_REFERENCE_RUNES = 1024

/** Is this reference longer than the engine will accept?
 *
 *  Counted in RUNES, because that is what the engine counts: `tooLong` measures
 *  `len([]rune(s))` (helpers.go:212-214). JavaScript's `.length` counts UTF-16 code
 *  units, so every astral character — an emoji, a rarer CJK ideograph — counts
 *  twice, and a 600-character reference the engine would take gets refused here.
 *  Spreading into an array iterates code POINTS, which is the same unit as a Go
 *  rune. */
export function nis2ReferenceTooLong(reference: string): boolean {
  return [...reference].length > NIS2_MAX_REFERENCE_RUNES
}

/** GET /nis2/incidents item — a 1:1 mirror of the engine's nis2IncidentDTO
 *  (nis2incident.go:44-64). `report_drafts`, `deadlines` and `basis` are the
 *  classification BODY: present on get/export, ABSENT on the list, because the
 *  list builds its DTOs with includeBody=false (nis2incident.go:224).
 *
 *  `provisional` is not a field the classifier decides — the engine hardcodes it
 *  true (`:78`). It is the honesty invariant of this whole plane: the verdict is
 *  decision support, never the legal classification (docs/SECURITY-HARDENING.md).*/
export interface Nis2Incident {
  id: string
  reference: string
  finding_id?: string
  significant: boolean
  provisional: boolean
  cross_border: boolean
  suspected_crime: boolean
  criteria_met?: string[]
  rationale?: string
  report_drafts?: Record<string, unknown>
  deadlines?: Record<string, unknown>
  basis?: Array<Record<string, string>>
  phase: string
  note?: string
  doc_sha256: string
  classified_by: string
  classified_at: string
  ledger_anchor?: Record<string, unknown>
  disclaimer: string
}

/** PUT /nis2/incidents/{id} body — and it is EXACTLY these two optional fields.
 *
 *  The engine decodes this route with DisallowUnknownFields (helpers.go:97-116)
 *  into an anonymous `struct{ Phase, Note string }` (nis2incident.go:270-273), so
 *  echoing the DTO back — the obvious way to write an edit form — is a 400
 *  "invalid JSON body", not a partial update. The type is narrow on purpose. */
export interface Nis2UpdateInput {
  phase?: Nis2Phase
  note?: string
}

/** GET /oscal/profiles item (oscalprofile.go:182 registeredProfileDTO). */
export interface OscalProfile {
  id: string
  framework: string
  doc_kind: string
  profile_uuid?: string
  ssp_uuid?: string
  import_profile_href?: string
  source_href?: string
  selected_control_ids: string[]
  dropped_control_ids?: string[]
  selected_count: number
  oscal_version?: string
  doc_sha256: string
  title?: string
  note?: string
  scope_note?: string
  registered_by: string
  registered_at: string
  disclaimer: string
}

// --- (13) compliance-depth packs (E4) ----------------------------------------

/** A validation issue on a depth pack (depthseam.go DepthIssue). */
export interface DepthIssue {
  severity?: string
  section?: string
  field?: string
  message: string
}

/** GET /depth/us-law and /depth/sector item (depthseam.go:272 depthPackDTO). */
/**
 * Lo que TODAS las familias de profundidad comparten por el cable, y nada más.
 *
 * ⛔ POR QUÉ HAY UNA BASE Y NO UN SOLO TIPO. Las cuatro familias que el motor sirve NO tienen la
 * misma forma: los packs de ley estatal y de sector llevan `pack_type` y `regulation`
 * (depthseam.go), el de FedRAMP lleva `system_name` e `impact_level` (`:316-331`) y el de AIMS
 * lleva `standard` y `organisation_name` (aimspack.go:143-161). **Ninguno de los dos últimos
 * tiene `pack_type`.** Declararlos como `DepthPack` compilaría —el campo simplemente vendría
 * `undefined`— y la fila pintaría un hueco donde va el nombre del sistema. Ésa es exactamente la
 * clase de defecto que este mismo día costó trece rutas caídas: un tipo que promete un campo que
 * la respuesta no trae.
 */
export interface DepthPackBase {
  id: string
  validation?: DepthIssue[]
  error_count: number
  doc_sha256: string
  scope_note?: string
  generated_by: string
  generated_at: string
  ledger_anchor?: Record<string, unknown>
  disclaimer: string
}

export interface DepthPack extends DepthPackBase {
  pack_type: string
  regulation?: string
  sections?: Record<string, unknown>
  note?: string
}

/** GET /depth/fedramp item (depthseam.go:316 fedRAMPKSIDTO). El generador contesta 501 sin el
 *  add-on `compliancedepth` (depthhandlers.go:1161), que es la frontera open-core, no un fallo. */
export interface FedrampKsiPack extends DepthPackBase {
  system_name: string
  impact_level: string
  ksis?: Record<string, unknown>
  oscal_version: string
  authorization_package?: Record<string, unknown>
  note?: string
}

/** GET /aims/pack item (aimspack.go:143 aimsPackDTO). Su generador exige el add-on `iso42001`,
 *  que es OTRO add-on que el de las tres familias `/depth/*` — se comprueba por separado. */
/** La incidencia de validación de un pack AIMS (`aimspack.go:135 AIMSIssue`).
 *
 *  ⛔ NO ES UNA `DepthIssue`, aunque las dos se llamen «validation» y viajen en el mismo sitio:
 *  `AIMSIssue` **no tiene `section`** y su `field` **no es `omitempty`**, así que llega SIEMPRE.
 *  `DepthIssue` es lo contrario en las dos cosas. Heredar la hermana compilaba y mentía: hacía
 *  opcional un campo que el motor manda siempre y ofrecía uno que nunca manda. */
export interface AimsIssue {
  severity: string
  field: string
  message: string
}

export interface AimsPack extends DepthPackBase {
  /** Estrecha `DepthPackBase.validation`: esta familia manda `AIMSIssue`, no `DepthIssue`. */
  validation?: AimsIssue[]
  standard: string
  organisation_name: string
  soa?: Record<string, unknown>
  policy?: Record<string, unknown>
  risk_register?: Record<string, unknown>
  impact_assessments?: Record<string, unknown>
  lifecycle_controls?: Record<string, unknown>
  supplier_governance?: Record<string, unknown>
}

/** GET /depth/ccm/snapshots item (depthseam.go:289 ccmSnapshotDTO). */
export interface CcmSnapshot {
  id: string
  snapshot_at: string
  frameworks?: Record<string, unknown>
  summary?: Record<string, unknown>
  note?: string
  doc_sha256?: string
  generated_by?: string
  generated_at?: string
  disclaimer?: string
}

/** GET|POST /depth/ccm/drift item — a detected control-drift finding, as a 1:1
 *  mirror of the engine's `driftFindingDTO` (depthseam.go:301-313, populated at
 *  depthhandlers.go:142-157).
 *
 *  ⚠ FOUR OF THESE FIELDS USED TO BE INVENTED, and the console rendered every real
 *  finding as `? → ?` with no framework and no explanation. It read `framework`,
 *  `from_status`, `to_status` and `note`; the engine has always sent `framework_id`,
 *  `prev_status`, `curr_status` and `detail`. Nothing failed and nothing 404'd — the
 *  fields were simply `undefined`, so the panel drew its placeholders over a
 *  well-formed answer. Raised by the the model contrast of (F1); the mismatch
 *  predates it (`a0be33de9c`) and stayed invisible because every drift fixture in the
 *  suite was an EMPTY list, which is the wider lesson: an empty-collection fixture
 *  cannot see a field-name mismatch.
 *
 *  `title`, `direction` and `snapshot_ref` are the three the console never had at
 *  all. They are typed here because the engine sends them, not because a panel
 *  renders each one today. */
export interface CcmDriftFinding {
  id: string
  snapshot_ref?: string
  framework_id?: string
  control_id?: string
  title?: string
  prev_status?: string
  curr_status?: string
  /** improved | regressed | … — the engine's own word for which way it moved. */
  direction?: string
  detail?: string
  detected_at?: string
}

/** POST /depth/ccm/snapshot body — EXACTLY the two keys the handler declares
 *  (depthhandlers.go:807-810), because it decodes with `DisallowUnknownFields`
 *  (helpers.go:99) and a third key is a 400, not an ignored field.
 *
 *  An empty/absent `frameworks` means EVERY catalog framework (`:821-826`), so
 *  the distinction between "none selected" and "these three" is a real difference
 *  in what gets snapshotted, not a formatting detail. */
export interface CcmSnapshotInput {
  frameworks?: string[]
  scope_note?: string
}

// --- shared engine limits (module XIII) --------------------------------------

/** The engine's request-body cap: `maxReqBytes = 1 << 20` (helpers.go:33), and
 *  `readBoundedBody` REJECTS over it with 413 rather than truncating
 *  (oscalprofile.go:503-507) — deliberately, because "a shortened evidence
 *  document would be hashed and classified as if it were the whole thing".
 *
 *  Mirrored console-side so an operator who pastes a large document learns before
 *  submitting instead of losing the round trip, and measured in BYTES because
 *  that is what the engine counts: a UTF-8 document of 600k characters can be
 *  well over a million bytes. */
export const COMPLIANCE_MAX_DOCUMENT_BYTES = 1024 * 1024

/** Byte length of a string as the engine will receive it. */
export function utf8ByteLength(s: string): number {
  return new TextEncoder().encode(s).length
}

/** Is this document larger than the engine will accept (413, never truncated)? */
export function documentTooLarge(s: string): boolean {
  return utf8ByteLength(s) > COMPLIANCE_MAX_DOCUMENT_BYTES
}

/** The engine REJECTS an over-length identity reference rather than truncating it
 *  — a clamped reference would persist as a DIFFERENT row that exact matching can
 *  never reach again (doraincident.go:102-107 for the incident reference,
 *  regpackage.go:289-291 for the register's maintaining-entity LEI).
 *
 *  `maxRefLen = 1024`, `maxNameLen = 200` (helpers.go:30-31), counted in RUNES:
 *  `tooLong` measures `len([]rune(s))` (helpers.go:212-214). JavaScript's
 *  `.length` counts UTF-16 code units, so an astral character counts twice and a
 *  reference the engine would accept gets refused here. Spreading into an array
 *  iterates code POINTS, which is the same unit as a Go rune. */
export const COMPLIANCE_MAX_REF_RUNES = 1024

/** Runes, not UTF-16 code units — see `COMPLIANCE_MAX_REF_RUNES`. */
export function refTooLong(reference: string): boolean {
  return [...reference].length > COMPLIANCE_MAX_REF_RUNES
}

// --- (14) regulatory calendar (E6) -------------------------------------------

/** The primary source behind a calendar entry (calendar.go:34 SourceRef). Every
 *  date in this calendar is DATA with a citation and a verification date — that
 *  is the whole point of shipping it, so the console never renders one without
 *  its source. */
export interface SourceRef {
  url: string
  title: string
  publisher: string
}

/** One dated regulatory milestone (calendar.go:64 RegulatoryMilestone).
 *  `status` distinguishes in-force law from a provisional agreement or a text
 *  adopted pending Official Journal publication — the calendar's own disclaimer
 *  says those are NOT in-force law, and the console must not flatten them into
 *  one list of "deadlines". */
export interface RegulatoryMilestone {
  id: string
  regime: string
  framework_id?: string
  date: string
  title: string
  effect: string
  status: string
  source: SourceRef
  verified_on: string
  note?: string
}

/** One tracked-but-undated item (calendar.go:84 WatchlistItem) — a rulemaking in
 *  progress, a draft standard, a beta project. It has no date BECAUSE there is
 *  none to cite. */
export interface WatchlistItem {
  id: string
  name: string
  framework_id?: string
  status: string
  expected?: string
  source: SourceRef
  verified_on: string
  note?: string
}

/** GET /calendar (calendar.go:600 handleCalendar). Registered at
 *  compliance.go:460 and, until this session, consumed by nothing outside
 *  s168_test.go — 30 milestones and 8 watchlist entries the product had already
 *  researched, verified and dated, that no operator could reach. */
export interface RegulatoryCalendarResponse {
  milestones: RegulatoryMilestone[]
  watchlist: WatchlistItem[]
  disclaimer: string
}

// ── (n) HIPAA Security Rule — Technical Safeguards (GET /hipaa/gap-report) ───────────
//
// ⛔ ESTE INFORME NO ES EL FRAMEWORK «hipaa_clinical_ai» QUE LA CONSOLA YA LISTA, y confundirlos
// es fácil porque un `grep hipaa` sobre la consola devuelve trece ficheros. Son dos documentos
// distintos con autoridades distintas:
//
//   hipaa_clinical_ai          «HIPAA Clinical AI Overlay» — está en el catálogo genérico
//                              (modules/compliance/frameworks.go:2136) y la consola SÍ lo alcanza
//   hipaa_technical_safeguards «HIPAA Security Rule — Technical Safeguards», 45 CFR §164.312,
//                              con CITA por control y descargo legal propio. Lo devuelve SÓLO la
//                              ruta dedicada; `hipaaTechnicalFramework()` se usa en un único
//                              sitio (hipaa.go:59) y NO está en el catálogo, así que no llega
//                              por /frameworks/{id}.
//
// ⛔ Y EL `disclaimer` SE PINTA SIEMPRE. No es cortesía: el texto dice, literal, «NOT a HIPAA
// compliance certification and NOT legal advice». Un informe de brecha regulatoria sin esa línea
// afirma exactamente lo que la línea niega.

/** One control of 45 CFR §164.312, with its citation and what evidences it. */
export interface HipaaControlGap {
  control_id: string
  /** The CFR reference — the thing this report has and the generic framework view does not. */
  citation: string
  title: string
  status: ControlStatus
  requirement: string
  criterion: string
  /** Partitioned with append server-side; `JSONArray` keeps the empty side `[]`, never null. */
  present_capabilities: string[]
  missing_capabilities: string[]
  evidence: CapabilityEvidence[]
  /** `omitempty`: ABSENT means there is no gap. It is not an unset field to render as a blank. */
  gap?: string
  recommended_action?: string
}

/** `GET /v1/m/compliance/hipaa/gap-report`. */
export interface HipaaGapReport {
  framework: string
  name: string
  authority: string
  generated_at: string
  summary: StatusSummary
  controls: HipaaControlGap[]
  disclaimer: string
}
