// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// the authority classification decides both WHAT the operator is told about a
// least-privilege finding and WHERE they are sent, so each case here pins a claim that
// would exceed the engine contract, or a link that would land on the wrong system.
//
// The fixtures are ENGINE-SHAPED on purpose. The first version of this suite asserted
// mcp_annotation with `permitted: true`, a combination deriveObservedPermitted can never
// produce (reactor.go:128-142) — a test green because the double could do what production
// cannot. The the model contrast caught it.
//
// and then the SAME defect survived one fixture over. `pending outranks permitted`
// was asserted with `{reconciliation_pending: true, edge: {permitted: true}}`, which the
// engine cannot emit either: Pending is serialised only onto UnexpectedAccesses
// (modules/access-map/dto.go:177 vs the unused-grant loop at :181), and markPending
// (query.go:414-419) is called only on the observed slice (query.go:324, :329). So
// `reconciliation_pending: true` implies `observed && !permitted`, and the precedence that
// fixture "proved" is REDUNDANT with the !permitted branch below it — a mutant on either
// alone measures nothing. That is asserted here as redundancy, not dressed up as coverage.
import { describe, expect, it } from 'vitest'
import {
  classifyAuthority,
  DRIFT_UNREAD,
  driftRead,
  findDriftEntry,
  recheckVerdict,
} from './authority'
import type { AccessEdge, DiffResponse, DriftEntry } from './types'

function edge(over: Partial<AccessEdge> = {}): AccessEdge {
  return {
    id: 'e1',
    origin_kind: 'agent',
    origin_id: 'A1',
    origin_ref: 'agent-1',
    resource_id: 'R1',
    resource_kind: 'postgres.table',
    resource_ref: 'appdb.public.orders',
    mode: 'read',
    signal_source: 'otel',
    confidence: 'attributed',
    bridged: true,
    observed: true,
    permitted: false,
    occurrence_count: 1,
    first_seen: '2026-08-10T10:00:00Z',
    last_seen: '2026-08-10T10:00:00Z',
    ...over,
  }
}

/**
 * The drift set was read COMPLETELY and this edge is not in it. For a permitted edge that is
 * the ordinary healthy case; for an observed-without-permit edge it means the engine
 * RECONCILED it (see the `reconciled` block below), so cases about a real finding must hand
 * over the entry instead — that is what `readAsFinding` is for.
 */
const READ_ABSENT = driftRead(null)

/** The drift set was read and this edge IS in it, as a firm unexpected access. */
function readAsFinding(e: AccessEdge) {
  return driftRead({
    kind: 'unexpected_access',
    reconciliation_pending: false,
    edge: e,
  })
}

describe('classifyAuthority — an owner is named only when the contract pins one', () => {
  it('routes a scoped_grant edge to SOURCE BINDINGS, not to RBAC roles', () => {
    // SignalScopedGrant is a source→scope binding (sdk/model/enums.go:90-97). Roles &
    // delegation is the unrelated RBAC system that merely shares the word "grant";
    // sending an operator there would be the wrong system entirely.
    const a = classifyAuthority(
      edge({ permitted: true, signal_sources: 'scoped_grant' }),
      READ_ABSENT,
    )
    expect(a.cls).toBe('sourceScope')
    expect(a.targets).toHaveLength(1)
    expect(a.targets[0]).toMatchObject({
      key: 'sourceBindings',
      to: '/console',
      search: { tab: 'bindings' },
      permission: 'tenant:admin',
    })
  })

  it('offers NO owner link for a bare `policy` permit', () => {
    // SignalPolicy is a GENERIC declared permit (enums.go:63-65): GitHub repo ACLs ride it
    // (enums.go:98-103), so do Vault ACLs. Routing it to Cedar/OPA authoring would send an
    // operator to a screen that does not own the edge.
    const a = classifyAuthority(
      edge({ permitted: true, signal_sources: 'policy' }),
      READ_ABSENT,
    )
    expect(a.cls).toBe('declared')
    expect(a.signals).toEqual(['policy'])
    expect(a.targets).toEqual([])
  })

  it('flags a fused edge as having MORE THAN ONE independent permit', () => {
    // Fusion accumulates sources (fusion.go:79-111) and the map never retracts, so
    // addressing one permit leaves the other standing. Saying otherwise promises a fix
    // that cannot work.
    const a = classifyAuthority(
      edge({ permitted: true, signal_sources: 'policy,scoped_grant,otel' }),
      READ_ABSENT,
    )
    expect(a.signals).toEqual(['policy', 'scoped_grant'])
    expect(a.multiple).toBe(true)
    expect(a.cls).toBe('sourceScope')
  })

  it('does not flag `multiple` for a single permit', () => {
    // Non-firing direction: a banner shown on every permitted edge would be noise.
    expect(
      classifyAuthority(
        edge({ permitted: true, signal_sources: 'policy' }),
        READ_ABSENT,
      ).multiple,
    ).toBe(false)
  })

  it('falls back to the scalar signal_source when the fused CSV is absent', () => {
    expect(
      classifyAuthority(
        edge({ permitted: true, signal_source: 'scoped_grant' }),
        READ_ABSENT,
      ).cls,
    ).toBe('sourceScope')
  })

  it('says UNATTRIBUTED and offers NOTHING when no permit signal is present', () => {
    // The first version claimed it refused to guess and then guessed the grants surface.
    const a = classifyAuthority(
      edge({ permitted: true, signal_sources: 'otel,pg_audit' }),
      READ_ABSENT,
    )
    expect(a.cls).toBe('unattributed')
    expect(a.signals).toEqual([])
    expect(a.targets).toEqual([])
  })
})

describe('classifyAuthority — what an absent permit does and does not mean', () => {
  it('classifies an observed edge the diff LISTED as `unpermitted`', () => {
    const e = edge({ observed: true, permitted: false })
    expect(classifyAuthority(e, readAsFinding(e)).cls).toBe('unpermitted')
  })

  it('treats a (not observed, not permitted) edge as UNDETERMINED, never as observed', () => {
    // deriveObservedPermitted maps SignalMCPAnnotation to (observed=false, permitted=false)
    // — an untrusted DECLARED capability. Calling it observed would be false for the exact
    // DTO on screen, and there is nothing to remediate.
    //
    // ⚠ NO IN-TREE PRODUCER SENDS THIS, and saying otherwise was a defect in its own right:
    // the only emitter uses origin=mcp_server, which Ingest drops before writing
    // (reactor.go:54-59, bridge.go:129-132; module_test.go:73-99 asserts zero rows). The
    // fixture is a DECLARED defensive case, not an engine-shaped one — it is reachable only
    // through a third-party connector, because SignalSource is an open string
    // (sdk/model/enums.go:47-50) cast unvalidated at the plugin bridge (convert.go:137).
    const a = classifyAuthority(
      edge({
        observed: false,
        permitted: false,
        signal_source: 'mcp_annotation',
        signal_sources: 'mcp_annotation',
      }),
      READ_ABSENT,
    )
    expect(a.cls).toBe('undetermined')
    expect(a.targets).toEqual([])
  })

  it('offers the roster first for an identity origin, then the source-binding plane', () => {
    const e = edge({ origin_kind: 'identity', origin_ref: 'svc_pool' })
    const a = classifyAuthority(e, readAsFinding(e))
    expect(a.targets.map((x) => x.key)).toEqual(['nhiRoster', 'sourceBindings'])
    expect(a.targets[0]).toMatchObject({
      to: '/identity',
      // `tab` is read by identity-view. It carries NO `focus`: the roster does not consume
      // one, and a parameter the destination ignores is a link that pretends to lead
      // somewhere — the defect this whole change removes.
      search: { tab: 'inventory' },
      permission: 'governance:identity:read',
    })
  })

  it('omits the roster target when the origin is not an identity', () => {
    const e = edge({ origin_kind: 'agent' })
    expect(
      classifyAuthority(e, readAsFinding(e)).targets.map((x) => x.key),
    ).toEqual(['sourceBindings'])
  })
})

describe('classifyAuthority — NOT HAVING LOOKED is its own answer', () => {
  // The engine flags reconciliation_pending ONLY on observed-without-permit edges, and only
  // the /drift response carries the flag. The view reads /drift solely while the overlay is
  // open, so the default way to reach this sheet — clicking an edge in the graph — has never
  // asked. With the lookup collapsed to `DriftEntry | null`, that silence was rendered as a
  // confirmed violation complete with remedies.

  it('says UNCHECKED, not `unpermitted`, when the drift set was never read', () => {
    const a = classifyAuthority(
      edge({ observed: true, permitted: false }),
      DRIFT_UNREAD,
    )
    expect(a.cls).toBe('unchecked')
    // And no remedy: a fix offered for a finding nobody confirmed is the claim, not the link.
    expect(a.targets).toEqual([])
    expect(a.closure).toBe('unknown')
  })

  it('still says UNCHECKED for an identity origin, which otherwise gets two targets', () => {
    // Guards against a fix that only covered the shorter target list.
    const a = classifyAuthority(
      edge({ origin_kind: 'identity', origin_ref: 'svc_pool' }),
      DRIFT_UNREAD,
    )
    expect(a.cls).toBe('unchecked')
    expect(a.targets).toEqual([])
  })

  it('NON-FIRING: the same edge, once the diff is read and LISTS it, is a finding', () => {
    // The direction that stops `unchecked` from swallowing everything. A classifier that
    // answered `unchecked` always would pass both cases above and never let an operator act.
    const e = edge({ observed: true, permitted: false })
    const a = classifyAuthority(e, readAsFinding(e))
    expect(a.cls).toBe('unpermitted')
    expect(a.targets.map((x) => x.key)).toEqual(['sourceBindings'])
  })

  it('does not gate what the EDGE itself says: a permitted edge classifies either way', () => {
    // The unread guard belongs on the observed-without-permit branch alone. `signal_sources`
    // is on the edge DTO; no drift read is needed to say a scoped grant recorded the permit,
    // and suppressing that would be the opposite over-correction.
    for (const lookup of [DRIFT_UNREAD, READ_ABSENT]) {
      expect(
        classifyAuthority(
          edge({ permitted: true, signal_sources: 'scoped_grant' }),
          lookup,
        ).cls,
      ).toBe('sourceScope')
    }
  })

  it('an mcp_annotation edge is UNDETERMINED whether or not the drift set was read', () => {
    // (observed=false, permitted=false) never satisfies `permitted <> observed`
    // (sqlstore/accessgraph.go:82), so it is never in the drift set and the read adds nothing.
    for (const lookup of [DRIFT_UNREAD, READ_ABSENT]) {
      expect(
        classifyAuthority(
          edge({ observed: false, permitted: false }),
          lookup,
        ).cls,
      ).toBe('undetermined')
    }
  })
})

describe('classifyAuthority — the engine reconciled it, so it is not a finding', () => {
  // reconcileDrift drops an observed edge with `continue // reconciled, not a drift` when a
  // grant on the identity the origin runs as covers the mode (modules/access-map/query.go:318),
  // while /graph keeps returning the row as (observed, not permitted) (query.go:36). That
  // cross-origin reconciliation is the case module III exists to handle, so this is not a
  // corner: it is the headline path.
  const observedNoPermit = edge({ observed: true, permitted: false })

  it('does not call an access a finding when a COMPLETE diff omitted it', () => {
    const a = classifyAuthority(observedNoPermit, driftRead(null, false))
    expect(a.cls).toBe('reconciled')
    expect(a.targets).toEqual([])
    expect(a.closure).toBe('unknown')
  })

  it('degrades to UNCHECKED when the window that omitted it was truncated', () => {
    // Absence out of a partial window is not absence — the same call recheckVerdict makes.
    expect(classifyAuthority(observedNoPermit, driftRead(null, true)).cls).toBe(
      'unchecked',
    )
  })

  it('NON-FIRING: an edge the diff DID list is still a finding with its remedy', () => {
    // A classifier answering `reconciled` whenever it felt like it would pass both cases
    // above while silencing every real violation on the screen.
    const a = classifyAuthority(
      observedNoPermit,
      driftRead({
        kind: 'unexpected_access',
        reconciliation_pending: false,
        edge: observedNoPermit,
      }),
    )
    expect(a.cls).toBe('unpermitted')
    expect(a.targets.map((x) => x.key)).toEqual(['sourceBindings'])
  })
})

describe('classifyAuthority — an absent corroborating set is not an empty one', () => {
  // signal_sources is omitempty (modules/access-map/dto.go:30) and the UI contract names the
  // two fields apart: the scalar is the LAST signal, the plural is EVERY signal that
  // corroborates. Absent means "you were not told".

  it('reports that the set was not fused when only the scalar arrived', () => {
    const a = classifyAuthority(
      edge({ permitted: true, signal_source: 'scoped_grant' }),
      READ_ABSENT,
    )
    expect(a.signalsFused).toBe(false)
    expect(a.cls).toBe('sourceScope')
  })

  it('treats an EMPTY plural as absent, because omitempty makes them the same statement', () => {
    expect(
      classifyAuthority(
        edge({ permitted: true, signal_source: 'policy', signal_sources: '' }),
        READ_ABSENT,
      ).signalsFused,
    ).toBe(false)
  })

  it('never claims `multiple` from the scalar alone — and the guard is REDUNDANT, said plainly', () => {
    // `multiple` is computed as `signalsFused && signals.length > 1`, and the `signalsFused &&`
    // half cannot be killed by any production-shaped fixture: the contract defines
    // signal_source as the scalar LAST signal, so it
    // never holds two, so `signals.length > 1` is already false whenever the plural is
    // absent. Removing the guard leaves this suite green — measured, not assumed. It stays
    // as defence against a producer that ever puts a CSV in the scalar, and it is named here
    // as redundancy rather than counted as coverage. What IS load-bearing is `signalsFused`
    // being reported at all, which the two cases above kill.
    const a = classifyAuthority(
      edge({ permitted: true, signal_source: 'policy' }),
      READ_ABSENT,
    )
    expect(a.multiple).toBe(false)
    expect(a.signalsFused).toBe(false)
  })

  it('NON-FIRING: a fused set says so, and still reports `multiple` when it holds two', () => {
    const a = classifyAuthority(
      edge({ permitted: true, signal_sources: 'policy,scoped_grant' }),
      READ_ABSENT,
    )
    expect(a.signalsFused).toBe(true)
    expect(a.multiple).toBe(true)
  })
})

describe('classifyAuthority — what step 4 can ever answer', () => {
  // Measured, not inferred: the store OR-merges observed and permitted forever
  // (core/internal/store/sqlstore/accessgraph.go:162-163) and drift is exactly
  // `permitted <> observed` (accessgraph.go:82). The two halves behave OPPOSITELY.

  it('an unexpected access CLOSES: recording a permit takes the row out of the diff', () => {
    const a = classifyAuthority(
      edge({ observed: true, permitted: false }),
      driftRead({
        kind: 'unexpected_access',
        reconciliation_pending: false,
        edge: edge(),
      }),
    )
    expect(a.cls).toBe('unpermitted')
    expect(a.closure).toBe('closes')
  })

  it('an unused grant CANNOT CLOSE: deleting the grant never clears `permitted`', () => {
    // This is the promise the screen used to make anyway. An operator who deletes the grant,
    // re-checks and is told "still in the drift set" concludes the deletion failed.
    const grant = edge({ observed: false, permitted: true, signal_sources: 'scoped_grant' })
    const a = classifyAuthority(
      grant,
      driftRead({ kind: 'unused_grant', edge: grant }),
    )
    expect(a.cls).toBe('sourceScope')
    expect(a.closure).toBe('cannotClose')
  })

  it('promises NOTHING when the drift read did not name which half this is', () => {
    // Non-firing direction: a classifier hard-coding either verdict would pass one case
    // above and lie on every edge selected without a drift entry to reason from.
    expect(
      classifyAuthority(
        edge({ permitted: true, signal_sources: 'scoped_grant' }),
        READ_ABSENT,
      ).closure,
    ).toBe('unknown')
    expect(
      classifyAuthority(
        edge({ permitted: true, signal_sources: 'scoped_grant' }),
        DRIFT_UNREAD,
      ).closure,
    ).toBe('unknown')
  })

  it('promises nothing for an UNDECIDED edge, which has not been classified at all', () => {
    const a = classifyAuthority(
      edge(),
      driftRead({
        kind: 'unexpected_access',
        reconciliation_pending: true,
        edge: edge(),
      }),
    )
    expect(a.cls).toBe('undecided')
    expect(a.closure).toBe('unknown')
  })
})

describe('classifyAuthority — an undecided finding gets NO remedy', () => {
  /** Engine-shaped: pending rides only on an unexpected access, so observed && !permitted. */
  const pending: DriftEntry = {
    kind: 'unexpected_access',
    reconciliation_pending: true,
    edge: edge({ observed: true, permitted: false }),
  }

  it('offers no target at all for a reconciliation_pending entry', () => {
    // The boolean does not say WHY it is pending — unresolved agent↔identity link, unknown
    // grant mode, or undecidable observed mode (query.go:216-225, 290-329). The first
    // version sent all three to the source-bindings tab as if the cause were always the
    // first, which is both the wrong system and a claim the engine never made.
    const a = classifyAuthority(pending.edge, driftRead(pending))
    expect(a.cls).toBe('undecided')
    expect(a.targets).toEqual([])
  })

  it('REDUNDANCY, asserted rather than dressed up as coverage: pending implies !permitted', () => {
    // The pending check runs before the !permitted branch, and on every input the engine can
    // actually produce BOTH would refuse the remedy — so a mutant on either one alone
    // survives, and neither is measured by the case above. What IS load-bearing is the
    // precedence over `unpermitted`: without it, a pending edge would be handed the two
    // remediation targets. That is what this pins, and the ordering stays as defence in
    // depth against a future DTO that serialises Pending on the unused-grant side too.
    const pendingCls = classifyAuthority(pending.edge, driftRead(pending)).cls
    const withoutPending = classifyAuthority(
      pending.edge,
      driftRead({ ...pending, reconciliation_pending: false }),
    )
    expect(pendingCls).toBe('undecided')
    expect(withoutPending.cls).toBe('unpermitted')
    expect(withoutPending.targets).not.toEqual([])
  })

  it('is NOT undecided when the entry exists but is not pending', () => {
    // Non-firing direction: a control answering "undecided" for every drift entry would
    // pass the case above while telling the operator nothing is ever actionable.
    expect(
      classifyAuthority(
        edge(),
        driftRead({
          kind: 'unexpected_access',
          reconciliation_pending: false,
          edge: edge(),
        }),
      ).cls,
    ).toBe('unpermitted')
  })
})

describe('recheckVerdict — three answers, never two', () => {
  const target = edge({ id: 'e-target' })
  const diff: DiffResponse = {
    unexpected_accesses: [{ kind: 'unexpected_access', edge: target }],
    unused_grants: [],
    unexpected_count: 1,
    unused_count: 0,
  }

  it('reports `present` while the edge is still in the drift set', () => {
    expect(recheckVerdict(diff, 'e-target')).toBe('present')
  })

  it('reports `clear` once the edge is gone from a COMPLETE response', () => {
    expect(
      recheckVerdict(
        { ...diff, unexpected_accesses: [], unexpected_count: 0 },
        'e-target',
      ),
    ).toBe('clear')
  })

  it('reports `unknown` — not `clear` — when there is no response to read', () => {
    // "Could not look" collapsing into "clean" is the most expensive defect class in this
    // repo; a null response must never grade as a fix.
    expect(recheckVerdict(null, 'e-target')).toBe('unknown')
  })

  it('reports `unknown` when the edge is absent from a TRUNCATED window', () => {
    // The engine reconciled over a partial drift window and says so precisely so a consumer
    // does not treat it as authoritative (query.go:83-87). Absence past the page bound is
    // not absence. Found by the the model contrast.
    expect(
      recheckVerdict(
        {
          unexpected_accesses: [],
          unused_grants: [],
          unexpected_count: 0,
          unused_count: 0,
          truncated: true,
        },
        'e-target',
      ),
    ).toBe('unknown')
  })

  it('still reports `present` from a truncated window when the edge IS found', () => {
    // Non-firing direction: truncation must not swallow a POSITIVE sighting.
    expect(recheckVerdict({ ...diff, truncated: true }, 'e-target')).toBe(
      'present',
    )
  })

  it('finds an edge on the unused-grant side too', () => {
    expect(
      recheckVerdict(
        {
          unexpected_accesses: [],
          unused_grants: [{ kind: 'unused_grant', edge: target }],
          unexpected_count: 0,
          unused_count: 1,
        },
        'e-target',
      ),
    ).toBe('present')
  })
})

describe('findDriftEntry', () => {
  it('returns null for an edge that is not in the drift set', () => {
    expect(
      findDriftEntry(
        {
          unexpected_accesses: [],
          unused_grants: [],
          unexpected_count: 0,
          unused_count: 0,
        },
        'nope',
      ),
    ).toBeNull()
  })

  it('returns null when there is no diff at all', () => {
    expect(findDriftEntry(null, 'e1')).toBeNull()
  })
})
