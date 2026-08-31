// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// DTOs for the Claude Code governance console. Two provenance classes:
//  • REAL — backed by an endpoint that exists today (drift Findings from
//    /v1/m/security, emitted by read-only connectors).
//  • DECLARED — the authoring contract EXPOSES for to implement
//    (validate/dry-run/publish/version/diff, Cedar/OPA explain). These are wired
//    so the console flips live the moment the backend lands; until then the UI
//    shows an honest "pending" seam and NEVER fakes success.
import type { PolicySurface } from './schema'

// --- REAL: drift Findings (→ /v1/m/security/findings) ------------------------

/** A drift/posture Finding as emitted by the read-only Claude connectors.
 *  Mirrors the security module findingDTO; carries a redacted detail_hash, never a
 *  payload (docs/SECURITY-HARDENING.md).*/
export interface PolicyDriftFinding {
  id: string
  /** policy_drift | posture | enforcement | drift | self_audit */
  kind: string
  severity: string
  status: string
  /** WHAT the finding is about (e.g. managed-settings, managed-mcp, sandbox). */
  subject_kind?: string
  subject_ref?: string
  title?: string
  /** Redacted fingerprint — render as a hash, never expand to content. */
  detail_hash?: string
  occurred_at?: string
  metadata?: Record<string, unknown>
}

// --- DECLARED: authoring contract (managed-settings / hooks / mcp / sandbox) ------

/** A stored revision of an authored policy (declared version history). */
export interface PolicyVersion {
  revision: number
  surface: PolicySurface
  author?: string
  created_at?: string
  /** The authored document at this revision (JSON text). */
  content?: string
  /** Whether this revision passed its validation/tests when stored. */
  validated?: boolean
}

/** Dry-run result: the resolved effect/precedence of a change BEFORE publishing.
 *  No host is touched. */
export interface DryRunResult {
  surface: PolicySurface
  /** Human-readable precedence/merge resolution lines (Managed > CLI > …). */
  resolved?: { scope: string; note: string }[]
  /** Keys that would change vs the currently-observed config. */
  changes?: { path: string; before?: unknown; after?: unknown }[]
  notes?: string[]
}

/** The signed distribution artifact summary a publish mints: the operator
 *  pins key_fingerprint and hands it to the pull agents out-of-band. */
export interface PolicyArtifactMeta {
  revision: number
  artifact_sha256: string
  key_fingerprint: string
  signed_at?: string
}

/** Publish result: the backend distributed the policy and ran drift verification.
 *  The UI shows the PERMITTED-policy-vs-OBSERVED-config Findings; it never writes
 *  host files itself. */
export interface PublishResult {
  surface: PolicySurface
  revision?: number
  /** Drift verification Findings produced by distribution (PERMITTED vs OBSERVED). */
  drift?: PolicyDriftFinding[]
  /** False = drift was NOT computed (no observation) — an honest unknown the UI
   *  must never render as "no drift". */
  drift_computed?: boolean
  /** distributed | seam-pending | enqueue-failed (deny-closed: "distributed" only
   *  after the signed artifact record committed). */
  distribution?: string
  artifact?: PolicyArtifactMeta
  notes?: string[]
}

/** One scope's (host / org-distribution) truth-loop state.*/
export interface PolicyScopeStatus {
  scope: string
  checked_in_at?: string
  /** Audit actor of the last check-in (the scope is SELF-asserted by the agent). */
  reporter?: string
  reported_revision?: number
  verified: boolean
  /** verified AND on the newest signed artifact. */
  current: boolean
  /** False = the scope only ever attested; its content drift is UNKNOWN. */
  content_reported: boolean
  observed_sha256?: string
  /** Result of the LAST content-drift computation (stamped only when it ran). */
  drift_count: number
  last_drift_at?: string
  /** LIVE count of open policy_drift findings for this scope — the number to trust. */
  open_findings: number
}

/** The per-surface distribution truth view: published vs signed vs what
 *  every scope reports — real state only, every absence named in notes. */
export interface PolicyDistributionView {
  surface: PolicySurface
  latest_revision?: number
  artifact?: PolicyArtifactMeta
  scopes: PolicyScopeStatus[]
  notes?: string[]
}

// --- DECLARED: Cedar/OPA policy-as-code contract (PDP) ------------------------

export type PdpEngine = 'cedar' | 'opa'

/** Validation result for a Cedar/Rego source (declared). */
export interface PdpValidateResult {
  ok: boolean
  diagnostics: {
    line?: number
    column?: number
    message: string
    severity: 'error' | 'warning'
  }[]
}

/** An example authorization request to dry-run a policy against (mirrors
 *  auth.Request / ResourceAttrs). */
export interface PdpExampleRequest {
  principal: { kind: string; id?: string }
  permission: string
  tenant?: string
  resource: {
    kind: string
    id?: string
    sensitivity?: string
    extra?: Record<string, string>
  }
}

/** Decision-explain. The engine answers three-valued for Cedar: a matched permit
 *  GRANTS within its resolved scope tree, a matched forbid RESTRICTS and overrides
 *  a grant, and no match ABSTAINS so the RBAC decision stands. `allow` is
 *  `effect != forbid`, so an allow is NOT by itself proof that the policy granted
 *  anything — read `reason`/`chain` for which of the three happened. */
export interface PdpDecision {
  /** Whether a decision was actually COMPUTED from the source. False for OPA,
   *  where nothing can be evaluated in this process: `allow` is then `true`
   *  because the PDP layer imposes no restriction, NOT because the policy
   *  granted anything. Optional because an engine older than this field omits
   *  it — and `undefined` must render as "not evaluated", never as a grant. */
  evaluated?: boolean
  allow: boolean
  reason: string
  engine: PdpEngine
  /** Optional richer chain if/when exposes it (declared, may be absent).*/
  chain?: {
    rule: string
    effect: 'permit' | 'forbid' | 'base'
    matched: boolean
  }[]
}

// --- DECLARED: Managed-Agents HITL (ANT2-14 signal → declared bridge) --------

/** A thread event read to find the concrete tool awaiting confirmation. The primary
 *  thread only sees sub-agent start/end, so the tool detail lives here.
 *
 *  HONESTY: the live docs expose events at /v1/sessions/{id}/events
 *  (+ /events/stream); the connector currently reads the per-thread path. Neither is
 *  re-exposed by an engine /v1/m route yet — this is DECLARED. The "primary thread
 *  only sees start/end" detail is connector-modelled, NOT a documented Anthropic
 *  guarantee. */
export interface ThreadEvent {
  id?: string
  type: string
  agent_ref?: string
  peer_ref?: string
  /** The tool name for an agent.tool_use / agent.mcp_tool_use event. */
  tool_name?: string
  /** The event id to pass as tool_use_id when confirming. */
  tool_use_id?: string
  created_at?: string
}

/** A client-emitted tool confirmation (user.tool_confirmation). */
export interface ToolConfirmationInput {
  tool_use_id: string
  result: 'allow' | 'deny'
  deny_message?: string
}

/** One stored gate result for a revision. Today the engine stores exactly ONE,
 *  named `publish_compile_validate` — the compile/validation gate that ran before
 *  the immutable revision was committed. There is no behavioral policy suite, so
 *  the UI must render name+detail, never a "passed/total" suite summary. */
export interface PdpTestResult {
  name: string
  passed: boolean
  detail: string
}

/** Stored compile/validate-gate status for ONE revision (mirrors pdpTestStatus).
 *  The engine selects the NEWEST revision when `revision` is omitted from the
 *  request — after a rollback that is not the active one, so the console always
 *  asks for the active revision explicitly. */
export interface PdpTestStatus {
  engine: PdpEngine
  /** The revision this artifact belongs to (echoes what was asked for). */
  revision?: number
  /** Whether a stored artifact exists at all (honest seam — absence is a 200). */
  available: boolean
  /** Why nothing is available. Render VERBATIM; never alongside counters. */
  reason?: string
  passed?: number
  failed?: number
  total?: number
  results?: PdpTestResult[]
}

// --- Cedar/OPA revision lifecycle (publish / rollback / active) -------------------

/** One immutable revision of a Cedar or Rego policy (mirrors governance
 *  revisionDTO).
 *
 *  It is deliberately NOT `PolicyVersion`: that type's `surface` is a
 *  `PolicySurface` ('managed-settings' | 'hooks' | 'managed-mcp' | 'sandbox'),
 *  which can never equal 'cedar' | 'opa'. Reusing it would type-check green while
 *  every engine comparison matched zero rows. */
export interface PdpRevision {
  revision: number
  /** The ENGINE this revision belongs to. Revision numbers are per-surface, so
   *  cedar r1 and opa r1 both exist and are DIFFERENT documents — never key or
   *  look a revision up by its number alone. */
  surface: PdpEngine
  author?: string
  created_at?: string
  /** Only populated by the single-revision read; list pages are metadata-only. */
  content?: string
  validated?: boolean
  /** Go tags this `active,omitempty` on a bool, so the key is ABSENT when false
   *  and literally never `false`: test with `!v.active`, never `=== false`. */
  active?: boolean
  note?: string
}

/** Whether the running evaluator actually took the revision the store selects.
 *  • applied        — compiled and swapped on this process's live evaluator.
 *  • deferred       — committed and selected in the STORE, but the swap FAILED:
 *                     the PREVIOUS policy is still deciding requests. `active`
 *                     is still true here — this is the dangerous state.
 *  • not_applicable — OPA: nothing is enforced from this process at all.
 *  • no_policy      — the store selects NO Cedar surface, so there is nothing to
 *                     be in force. READ-ONLY: a publish always has a revision, so
 *                     this never comes back from publish/rollback. It exists
 *                     because reporting a brand-new tenant as `applied` put a
 *                     green "enforcing" badge on a tenant with no policy at all. */
export type PdpLiveActivation =
  | 'applied'
  | 'deferred'
  | 'not_applicable'
  | 'no_policy'

export interface PdpPublishResult {
  engine: PdpEngine
  revision: number
  /** What the STORE selects — NOT proof the live engine took it. */
  active: boolean
  live_activation: PdpLiveActivation
  note?: string
}

export interface PdpRollbackResult {
  engine: PdpEngine
  /** Absent when nothing was active before this activation. */
  from_revision?: number
  to_revision: number
  active: boolean
  live_activation: PdpLiveActivation
  note?: string
}

/** One contributing surface of the enforced policy. `content` is populated ONLY
 *  for the authored surface: the managed (RBAC projection) and adopted (signed
 *  bundle) surfaces disclose presence/revision/digest but never their source, so
 *  the read permission is not widened. */
export interface PdpActiveSurface {
  present: boolean
  revision?: number
  content?: string
  author?: string
  created_at?: string
  sha256?: string
}

/** What the STORE currently selects for an engine. For Cedar the ENFORCED policy
 *  is the UNION of authored ∪ managed ∪ adopted; publishing only ever replaces
 *  the authored surface. */
export interface PdpActivePolicy {
  engine: PdpEngine
  authored: PdpActiveSurface
  managed: PdpActiveSurface
  adopted: PdpActiveSurface
  union_sha256?: string
  /** Whether the process that served THIS READ is deciding requests with the
   *  revision above — measured by the engine, not remembered by the browser, so
   *  it survives a reload, a second operator and a different replica.
   *
   *  Optional on purpose: an engine older than this field omits it, and that is
   *  a genuine third answer ("this server does not tell me"). It must NEVER be
   *  collapsed into `not_applicable` by an else-branch — see PdpLiveActivation. */
  live_activation?: PdpLiveActivation
  /** This process is past the offline-staleness bound, so the policy's POSITIVE
   *  grants have degraded to abstain while its forbid rules stay enforced. A
   *  SEPARATE axis from live_activation: the engine really does hold the selected
   *  policy — `applied` is true — and half of what it says is still not in force. */
  grants_expired?: boolean
  note?: string
}
