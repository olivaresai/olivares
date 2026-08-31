// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// WHAT PUT THIS EDGE HERE, AND WHERE THAT IS CHANGED.
//
// The map explained the drift and led nowhere: the edge sheet ended at "only metadata is
// shown" with no action, while five other features link INTO the map and it links out to
// none. This module is the seam that closes the loop — and it is deliberately a LOOKUP,
// never a derivation, and deliberately SILENT wherever the contract cannot support a claim.
//
// THE ENGINE ALREADY NAMES THE SIGNAL, so the client must not re-decide it. An AccessEdge's
// permitted side is written by exactly two signal sources
// (modules/access-map/reactor.go:128-143 deriveObservedPermitted):
//   • `policy`       (sdkmodel.SignalPolicy)      — "a declared/permitted grant"
//   • `scoped_grant` (sdkmodel.SignalScopedGrant) — an source→scope binding
// every other source is an OBSERVATION (otel, pg_audit, cloudtrail, github …), and
// `mcp_annotation` is an untrusted declared capability that is NEITHER — it maps to
// (observed=false, permitted=false).
//
// ⚠ WHAT THE FIRST VERSION OF THIS FILE GOT WRONG, all six found by the the model
// contrast and all of them the same mistake — claiming more than the signal supports:
//
//  1. It routed `policy` to Cedar/OPA policy-as-code and said the edge came "from your IdP
//     or a deployment". `SignalPolicy` is a GENERIC declared permit (sdk/model/enums.go:63-65):
//     GitHub repo ACLs ride it (enums.go:98-103 says so outright, and connectors/github/acl.go),
//     so does Vault. The owning system is whatever emitted it, and the edge does not carry
//     which — `Event.Source` exists (sdk/event/event.go:49-67) but the access-map subscriber
//     discards it (modules/access-map/module.go:122-132). So: NO owner link for `policy`.
//  2. It routed `scoped_grant` to /console?tab=roles — the RBAC delegation grants. That
//     is a DIFFERENT system that merely shares the word "grant": SignalScopedGrant is a
//     source→scope binding (enums.go:90-97), whose surface is the Source bindings tab.
//  3. It sent a `reconciliation_pending` edge to the source-bindings tab as though the cause
//     were always an unresolved agent↔identity link. The engine also marks pending for an
//     unknown grant mode and an undecidable observed mode (query.go:216-225, 290-329), and
//     the boolean does not say which. An undecided finding gets NO remedy — which was this
//     module's own stated principle, broken by that link.
//  4. It said `unattributed` refuses to guess and then guessed the grants surface.
//  5. It called every non-permitted edge "observed", but (observed=false, permitted=false)
//     is a real engine state (mcp_annotation), and `Permitted == false` means NOT KNOWN to
//     be permitted, never "forbidden" — source-scope bindings for user_group/role/folder
//     enforce live while projecting no permit at all (modules/sourcescope/accessmap.go:47-81).
//  6. It promised "change the grant and this access changes with it". The map is MONOTONIC:
//     edges OR-merge observed/permitted forever (sqlstore/accessgraph.go:132-168), fusion
//     accumulates every source ever seen (fusion.go:79-111), and unbinding publishes no
//     inverse edge (sourcescope/accessmap.go:23-30, binding.go:587-633). Removing the
//     authority does NOT retract the edge, so the copy is historical: a permit signal was
//     RECORDED.
//
// WHY NOT POST /v1/m/governance/pdp/explain, which the brief asked for — and the contrast
// confirmed the refusal is correct. It evaluates a CANDIDATE source the caller supplies
// (pdp_authoring.go:374-400); /pdp/active discloses source for the AUTHORED surface only,
// not the managed (projection) or adopted ones (pdp_authoring.go:228-250), so an
// explain over it answers from a fraction of the enforced policy; and it is explicitly
// scope-blind (pdp_authoring.go:401-405), which is exactly the case that produces
// `scoped_grant` edges. It is already consumed at claude-policy/api.ts:90, where a candidate
// source legitimately exists. The brief's intent ("do not reimplement the reasoning in the
// client") is honoured in full: nothing here is reasoned.
//
// ⚠ WHAT FOUND STILL OPEN after that pass, and both are the SAME mistake one level up
// — the module stopped over-claiming about the EDGE and kept over-claiming about its own
// KNOWLEDGE of the edge:
//
//  A. THE DRIFT LOOKUP HAD TWO ANSWERS WHERE THE ENGINE HAS THREE. `drift` arrived as
//     `DriftEntry | null`, and the view can only produce a non-null one while the drift
//     overlay is open (access-map-view.tsx). So "I did not read the drift set" and "I read
//     it and this edge is not pending" were the SAME value, and an edge the engine would
//     have flagged reconciliation_pending was asserted as a firm finding — with remedies —
//     to anyone who selected it from the graph with the overlay off, which is the DEFAULT,
//     and to every reader who lacks accessmap:drift:read at all. That is canon §1.5's
//     third answer collapsed into the first, inside the very module written to stop doing
//     that. The lookup is now a discriminated `DriftLookup`: `unread` cannot be spelled the
//     same way as `read`-with-nothing-found, and there is no default, so a caller has to say.
//  B. STEP 4 PROMISED A LOOP THAT ONE HALF OF THE FINDINGS CANNOT CLOSE. Measured, not
//     inferred: the store OR-merges observed and permitted forever
//     (core/internal/store/sqlstore/accessgraph.go:162-163) and drift is exactly
//     `permitted <> observed` (accessgraph.go:82). So the two halves behave OPPOSITELY —
//       • unexpected access (observed, not permitted): recording a permit sets permitted and
//         the finding leaves the set — though the permit may land on ANOTHER edge and clear
//         it through reconcileDrift instead (query.go:290-319). Stopping the access does
//         NOT clear it: observed never retracts either.
//       • unused grant (permitted, never observed): DELETING THE GRANT cannot clear
//         permitted, so that operation never removes the finding. (It can still leave the
//         diff for an unrelated reason — `observed` OR-merges too.)
//     Both statements are about the OPERATION, not about the future of /drift; the first
//     version of this comment and its copy over-reached in both directions and the second
//     the model contrast was right to say so.
//     `noRetraction` said only that the edge "can persist", which reads as sometimes when
//     it is always — and an operator who deletes a grant, re-checks, and is told it is
//     still there goes hunting for a deletion that in fact worked. `closure` names which
//     half you are on, so the screen can say it BEFORE the button is pressed.
//  C. AND ONE THE CONTRAST DID NOT RAISE, found reviewing the above: an edge the engine
//     RECONCILED was still called a finding. `reconcileDrift` drops an observed edge with
//     `continue // reconciled, not a drift` when a grant on the identity the origin runs as
//     covers the mode (query.go:290-319) — that cross-origin reconciliation is the C2 /
//     Case this module exists to fix — while /graph keeps returning the row as
//     (observed, not permitted) untouched (query.go:36). So selecting it said "no permit
//     signal is present… that is what makes it a finding" about an access the engine had
//     just decided is permitted, with the permit recorded on a DIFFERENT edge. With a
//     complete window that absence is determinate (every observed edge is either appended
//     or continued), so it earns its own class; out of a TRUNCATED window it proves nothing
//     and degrades to `unchecked`, the same call recheckVerdict already makes.
import type { AccessEdge, DiffResponse, DriftEntry } from './types'

/**
 * The closed set of PERMITTED-side signal sources, mirroring
 * modules/access-map/reactor.go:130. A source outside this set never establishes a permit —
 * it is an observation, or an untrusted annotation.
 */
// ⚠ THESE ARE THE ENGINE'S WIRE VALUES, RESOLVED — not the Go identifier names. Lost a
// badge to exactly that slip: it read the NAME of a Go constant instead of its VALUE
// (`colDecisionHeadState` is "state", not "head_state"), and its tests stayed green because
// a cast made them agree with the wrong type. Resolved here on 2026-08-10:
//   sdk/model/enums.go:65  SignalPolicy        SignalSource = "policy"
//   sdk/model/enums.go:97  SignalScopedGrant   SignalSource = "scoped_grant"
//   sdk/model/enums.go:56  SignalMCPAnnotation SignalSource = "mcp_annotation"
// and the other engine strings this module compares against:
//   modules/access-map/bridge.go:20  originIdentity  = "identity"
//   modules/access-map/dto.go:144    driftUnexpected = "unexpected_access"
//   modules/access-map/dto.go:145    driftUnused     = "unused_grant"
//   modules/access-map/dto.go:154    json tag         `reconciliation_pending`
export const PERMITTED_SIGNALS = ['policy', 'scoped_grant'] as const
export type PermittedSignal = (typeof PERMITTED_SIGNALS)[number]

/**
 * How this edge came to exist, in the only terms the contract supports.
 *
 * `sourceScope`   — permitted, and a `scoped_grant` signal is present. This is the ONE case
 *                   where the owning surface is establishable: the source→scope binding plane.
 * `declared`      — permitted by a `policy` signal only. The emitting system owns it and the
 *                   edge does not carry which one, so no owner link is offered.
 * `unattributed`  — permitted with NO permitted-side signal present. No owner, and no guess.
 * `unpermitted`   — observed with no permitted signal in this map. Note the wording: this is
 *                   NOT "nothing permits it". `permitted=false` means not known to be
 *                   permitted (core/model/accessedge.go), and a live resolver can still allow it.
 * `undetermined`  — neither observed nor permitted. Not a finding, and nothing to remediate.
 *                   ⚠ NO IN-TREE PRODUCER MAKES THIS EDGE, measured: the only emitter of
 *                   `mcp_annotation` sends origin=mcp_server (connectors/mcp/mapping.go:55),
 *                   and Ingest drops an unattributable origin before writing anything
 *                   (reactor.go:54-59, bridge.go:129-132) — there is an executable test for
 *                   it (module_test.go:73-99). It is still REACHABLE, which is why this stays:
 *                   SignalSource is an open string by design (sdk/model/enums.go:47-50) and the
 *                   plugin bridge casts it unvalidated (sdk/plugin/convert.go:137) — as does
 *                   the remote collector (core/api/ingest.go:75, :81) — so a producer emitting
 *                   that source with an attributable origin produces exactly this pair. Deny-closed guard, declared — not a case any
 *                   of our connectors has been measured producing.
 * `undecided`     — the drift entry is reconciliation_pending: permitted-ness is not yet
 *                   decidable, and the boolean does not say why, so no cause-specific remedy.
 * `unchecked`     — observed with no permit signal, and this edge's drift state was NOT
 *                   ESTABLISHED. The engine may hold it reconciliation_pending, and only the
 *                   drift response carries that; without it, calling this a finding — and
 *                   offering a remedy for it — asserts a decision nobody made. Not a verdict:
 *                   the absence of one.
 *
 *                   ⚠ THE USER-FACING COPY NAMES NO CAUSE, deliberately, and that is a
 *                   correction applied four times before it stuck: every attempt to list why
 *                   the state arises left one out. For maintainers, the set is every way of
 *                   not having a successful read — not requested (overlay closed), not
 *                   permitted, IN FLIGHT, FAILED (access-map-view.tsx gates on isSuccess) —
 *                   plus a successful TRUNCATED read this edge is absent from. Enumerating
 *                   that on screen is a claim about which one happened, and the sheet does
 *                   not know.
 * `reconciled`    — observed with no permit signal ON THIS EDGE, and a COMPLETE drift read
 *                   did not list it. The engine looked and did not call it drift, so calling
 *                   it a finding contradicts its own reconciliation. It does NOT establish
 *                   WHY: a covering grant on the resolved identity is one way
 *                   (modules/access-map/query.go:290-319), but /graph and /drift are separate
 *                   reads, so a permit may also have landed on this very edge between them.
 *                   The class says "not a finding"; it must not say what made it so.
 */
export type AuthorityClass =
  | 'sourceScope'
  | 'declared'
  | 'unattributed'
  | 'unpermitted'
  | 'undetermined'
  | 'undecided'
  | 'unchecked'
  | 'reconciled'

/**
 * What is known about this edge's DRIFT state — three answers, because the engine has three.
 *
 * `unread` is NOT "not in the drift set": the drift read is a separate, permissioned, audited
 * call the view only makes while the overlay is open, so most selections have never asked.
 * Making that its own shape is the whole point — as an optional `DriftEntry | null` the two
 * collapsed, and "I never looked" was rendered as a confirmed violation.
 *
 * `truncated` rides along because ABSENCE from a partial window is not absence — the same
 * distinction `recheckVerdict` already makes below, and for the same reason.
 */
export type DriftLookup =
  | { status: 'unread' }
  | { status: 'read'; entry: DriftEntry | null; truncated: boolean }

/** The drift set was not read for this edge. */
export const DRIFT_UNREAD: DriftLookup = { status: 'unread' }

/**
 * The permission that gates the diff read. Named ONCE: the view gates the query on it and
 * the sheet prints it to a principal who lacks it, and two copies of a permission string are
 * two chances for the screen to name a permission the engine does not have.
 */
export const DRIFT_PERMISSION = 'accessmap:drift:read'

/**
 * The drift set WAS read: `entry` is the finding, or null if this edge is not in it.
 * `truncated` is the engine's own flag that the diff covered only part of the estate.
 */
export function driftRead(
  entry: DriftEntry | null,
  truncated = false,
): DriftLookup {
  return { status: 'read', entry, truncated }
}

/**
 * Whether step 4 can ever answer `clear` for this finding, once the operator does the
 * obvious thing. It is a property of the ENGINE's storage, not of this screen:
 *
 * `closes`      — an unexpected access. Recording a permit takes the row out of the diff
 *                 (accessgraph.go:82, :162-163) — though not necessarily on THIS edge: a
 *                 grant on the identity the access resolves to clears it from another one,
 *                 via reconcileDrift (query.go:290-319).
 * `cannotClose` — an unused grant. `permitted` only ever OR-merges, so DELETING THE GRANT
 *                 cannot clear it. Narrow on purpose: the row can still leave the diff for
 *                 an unrelated reason, because `observed` OR-merges too and the access
 *                 finally being seen removes it. The claim is about the operation, not the
 *                 future of /drift.
 * `unknown`     — no drift entry to reason from, so no promise either way.
 */
export type LoopClosure = 'closes' | 'cannotClose' | 'unknown'

/** A console surface that OWNS the authority, addressed by its registered route. */
export interface AuthorityTarget {
  /** Stable key — also the i18n key for label + hint. */
  key: 'sourceBindings' | 'nhiRoster'
  /** Registered path (registry.tsx). */
  to: string
  /** Query merged into the link. Every key here MUST be read by the destination. */
  search: Record<string, string>
  /**
   * The permission of the ROUTE we are sending them to, not of the data behind it:
   * `can()` is membership of the effective set from /v1/auth/whoami, so gating on
   * anything else offers a link that lands on a Forbidden page.
   *
   * ⚠ Declared limit, inherited from #671 and NOT compensated here: a grant scoped to a
   * WORKSPACE does not enter that flat set, so a workspace-delegated operator can be shown
   * no link for an action the engine would in fact allow. Hiding a link is the safe
   * direction of that error and the client must not second-guess it.
   */
  permission: string
}

export interface Authority {
  cls: AuthorityClass
  /** The permitted-side sources actually present on the edge (may be empty). */
  signals: PermittedSignal[]
  /**
   * Whether `signals` was read from the FUSED corroborating set or from the scalar alone.
   *
   * `signal_sources` is `omitempty` on the wire (modules/access-map/dto.go:30), and the UI
   * contract is explicit about what the two fields are: `signal_source` is "señal escalar
   * (última)" and `signal_sources` "TODAS las señales que corroboran (fusión)"
   * So when the plural is absent this module is
   * looking at the LAST writer, not the set — and absent is not "nothing corroborates it",
   * it is "you were not told". Anything said about the ABSENCE of a permit signal, or about
   * there being only ONE, is unsupported in that case and the sheet says so.
   */
  signalsFused: boolean
  /**
   * True when MORE THAN ONE independent permitted-side signal is fused onto this edge.
   * Because the map never retracts, changing one of them cannot remove the permit the
   * other supplies — so the copy has to say every permitter must be addressed.
   */
  multiple: boolean
  /** Whether the remediation loop can ever be seen to close for this finding. */
  closure: LoopClosure
  /** Where to go, when the owner is establishable. Empty whenever it is not. */
  targets: AuthorityTarget[]
}

/**
 * The source→scope binding plane that emits `scoped_grant` permits
 * (modules/sourcescope/accessmap.go). Its console surface is the Source bindings tab —
 * NOT "Roles & delegation", which is the unrelated RBAC delegation system.
 */
const SOURCE_BINDINGS_TARGET: AuthorityTarget = {
  key: 'sourceBindings',
  to: '/console',
  search: { tab: 'bindings' },
  permission: 'tenant:admin',
}

/**
 * The NHI roster. `?tab=inventory` is read by identity-view; it does NOT pre-select an
 * identity, so the label promises the roster and nothing more. (A focused seam would need
 * the roster's DataTable search to accept an initial value — declared, not built.)
 */
const NHI_ROSTER_TARGET: AuthorityTarget = {
  key: 'nhiRoster',
  to: '/identity',
  search: { tab: 'inventory' },
  permission: 'governance:identity:read',
}

/** Split the fused CSV of corroborating signals; fall back to the scalar last-writer. */
export function edgeSignals(edge: AccessEdge): string[] {
  return (edge.signal_sources || edge.signal_source || '')
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean)
}

function permittedSignalsOf(edge: AccessEdge): PermittedSignal[] {
  const present = new Set(edgeSignals(edge))
  return PERMITTED_SIGNALS.filter((s) => present.has(s))
}

/**
 * True when the edge carried the FUSED corroborating set rather than only the scalar.
 * Empty string counts as absent: `omitempty` drops the field, and a producer that sent an
 * empty one told us no more than a producer that sent none.
 */
function hasFusedSignals(edge: AccessEdge): boolean {
  return Boolean(edge.signal_sources)
}

/**
 * Classify one edge against what is known about its drift state.
 *
 * The drift entry is what carries `reconciliation_pending`; when the edge was picked off the
 * graph instead of the drift list the caller looks it up in the SAME /drift response rather
 * than recomputing anything (access-map-view.tsx). No state is kept: this is a pure read of
 * the engine's contract, which is what keeps the map a reflection and not a second store of
 * who may do what.
 *
 * `drift` is REQUIRED and has no default on purpose. An optional parameter would let a call
 * site say nothing and be read as "I looked and found nothing" — the exact collapse this
 * signature exists to prevent.
 */
export function classifyAuthority(
  edge: AccessEdge,
  drift: DriftLookup,
): Authority {
  const signals = permittedSignalsOf(edge)
  const signalsFused = hasFusedSignals(edge)
  // `multiple` is a claim about the SET, so it can only be made from the set. With the
  // scalar alone a second permit is invisible, and reporting `false` there would be an
  // assertion drawn from a field the producer simply did not send.
  const multiple = signalsFused && signals.length > 1
  const entry = drift.status === 'read' ? drift.entry : null
  const base = {
    signals,
    signalsFused,
    multiple,
    targets: [] as AuthorityTarget[],
  }

  // Undecided FIRST: pending reconciliation means permitted-ness is not yet knowable, so
  // neither "a permit exists" nor "none is present" may be asserted as a finding. And the
  // boolean does not carry WHY (unresolved identity, unknown grant mode, undecidable
  // observed mode — query.go:216-225, 290-329), so no cause-specific remedy is offered.
  //
  // Nothing is promised about the loop here either: the engine has not decided what this
  // edge IS, so it cannot be told what will make it go away.
  if (entry?.reconciliation_pending) {
    return { ...base, cls: 'undecided', closure: 'unknown' }
  }

  if (!edge.permitted) {
    // Neither observed nor permitted. Never drift (the store selects permitted <> observed,
    // accessgraph.go:82) and nothing to remediate; calling it "observed" would be false for
    // the very DTO on screen. No IN-TREE producer emits it — see the class docs above for the
    // measurement and for the third-party path that does.
    if (!edge.observed) {
      return { ...base, cls: 'undetermined', closure: 'unknown' }
    }
    // Observed without a permit is the ONLY shape the engine ever flags pending — Pending
    // rides only on UnexpectedAccesses (modules/access-map/dto.go:177 vs :181) and markPending
    // is called only on observed edges (query.go:324, :329). So on exactly this branch, not
    // having read the drift set is the difference between a confirmed finding and an
    // undecided one, and the map cannot tell which without asking.
    if (drift.status === 'unread') {
      return { ...base, cls: 'unchecked', closure: 'unknown' }
    }
    // Read, and this edge is NOT in the diff. With a COMPLETE window that is determinate:
    // every observed edge in the drift window is either appended to UnexpectedAccesses or
    // `continue`d as "reconciled, not a drift" (modules/access-map/query.go:318-319), while
    // /graph still returns the row untouched (query.go:36). So the engine LOOKED and said
    // this is not drift — the cross-origin reconciliation this module exists to do — and
    // calling it a finding contradicts it. Out of a TRUNCATED window the same absence proves
    // nothing, so it degrades to "not established", exactly as recheckVerdict does.
    if (!entry) {
      return {
        ...base,
        cls: drift.truncated ? 'unchecked' : 'reconciled',
        closure: 'unknown',
      }
    }
    // Read, and not pending: a firm finding. The roster is where the origin identity is
    // reviewed; the source→scope plane is where a permit is recorded. Recording one clears
    // the finding — this is the half of the drift set where the loop can be seen to close.
    const targets: AuthorityTarget[] = []
    if (edge.origin_kind === 'identity') targets.push(NHI_ROSTER_TARGET)
    targets.push(SOURCE_BINDINGS_TARGET)
    return { ...base, cls: 'unpermitted', closure: 'closes', targets }
  }

  // Permitted. If the drift read put this edge in the unused-grant half, the loop CANNOT be
  // seen to close: `permitted` never retracts, so removing the grant leaves the row exactly
  // as it is (accessgraph.go:82, :162-163).
  const closure: LoopClosure =
    entry?.kind === 'unused_grant' ? 'cannotClose' : 'unknown'

  // Name an owner ONLY for scoped_grant, the one signal whose emitting plane the contract
  // pins. A bare `policy` permit could have come from an IdP, a deployment, a GitHub or
  // Vault ACL — and the edge does not say which.
  if (signals.includes('scoped_grant')) {
    return {
      ...base,
      cls: 'sourceScope',
      closure,
      targets: [SOURCE_BINDINGS_TARGET],
    }
  }
  if (signals.includes('policy')) {
    return { ...base, cls: 'declared', closure }
  }
  return { ...base, cls: 'unattributed', closure }
}

/** Find the drift entry for an edge in a /drift response, or null. */
export function findDriftEntry(
  diff: DiffResponse | null,
  edgeId: string,
): DriftEntry | null {
  if (!diff) return null
  return (
    diff.unexpected_accesses.find((e) => e.edge.id === edgeId) ??
    diff.unused_grants.find((e) => e.edge.id === edgeId) ??
    null
  )
}

/** Verdict of a re-check against a freshly fetched /drift (step 4 of the loop). */
export type RecheckVerdict = 'clear' | 'present' | 'unknown'

/**
 * Decide whether an edge is STILL in the drift set of a freshly fetched response.
 *
 * Three answers, never two: `unknown` covers BOTH ways of not having looked properly.
 * Collapsing either into `clear` would report a fix that was never measured — the most
 * expensive defect class in this repository.
 *
 *  • No response at all → `unknown`.
 *  • A response the engine flagged `truncated` in which the edge does NOT appear →
 *    `unknown`, because the diff was reconciled over a PARTIAL drift window and the edge may
 *    simply be past the page bound. The engine emits the flag for exactly this: "a consumer
 *    must label a truncated diff as partial, not authoritative" (query.go:83-87). FINDING the
 *    edge is still positive evidence, so `present` stands whether or not the window was partial.
 */
export function recheckVerdict(
  diff: DiffResponse | null,
  edgeId: string,
): RecheckVerdict {
  if (!diff) return 'unknown'
  if (findDriftEntry(diff, edgeId)) return 'present'
  return diff.truncated ? 'unknown' : 'clear'
}
