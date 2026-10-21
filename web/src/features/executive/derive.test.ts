// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import type { Finding } from '@/features/security/types'

import type { ComplianceSummaryResponse } from '@/features/compliance/types'
import type { StatusDTO, IncidentDTO } from '@/features/health/types'
import type { InventorySummary } from '@/features/inventory/types'
import type { RunDTO } from '@/features/agentops/types'
import type { LiveDTO } from '@/features/sessions/types'
import { groupOf } from '@/features/sessions/session-groups'
import { mergeSessions } from '@/features/sessions/provenance'
import type { ListResponse } from '@/lib/api/types'
import {
  deriveCompliance,
  deriveDrift,
  deriveHealth,
  deriveRisk,
  deriveUsage,
  currentAnswer,
} from './derive'
import {
  accessDriftFixture,
  complianceRiskFixture,
  complianceSummary,
  healthIncidentsFixture,
  healthStatusFixture,
  inventorySummaryFixture,
  securityFindingsFixture,
} from './fixtures'

function list<T>(items: T[]): ListResponse<T> {
  return { items, has_more: false }
}
const finding = (severity: string, status = 'open'): Finding =>
  ({ severity, status }) as unknown as Finding

// --- cost --------------------------------------------------------------------

// --- usage -------------------------------------------------------------------

// Rows are built whole: `mergeSessions` keys a row by its reference, so a bare
// `{ cc_state }` would collapse every row into one.
function liveRow(ref: string, over: Partial<LiveDTO> = {}): LiveDTO {
  return {
    session_ref: ref,
    live_ref: `lr-${ref}`,
    attribution: 'legacy',
    cc_state: 'active',
    input_tokens: 0,
    output_tokens: 0,
    cost_micro_usd: 0,
    event_count: 1,
    tool_call_count: 0,
    first_event_at: '2026-10-15T09:00:00Z',
    last_event_at: '2026-10-15T09:01:00Z',
    duration_seconds: 60,
    ...over,
  }
}

function runRow(ref: string, over: Partial<RunDTO> = {}): RunDTO {
  return {
    run_ref: ref,
    transport: 'stream-json',
    permission_mode: 'default',
    isolation: 'native',
    state: 'running',
    last_event_seq: 0,
    pep_provisioned: true,
    record_io: false,
    critical: false,
    ...over,
  }
}

describe('deriveUsage', () => {
  it('counts active agents, live states and surfaces silent-evasion', () => {
    const live = list<LiveDTO>([
      liveRow('s1', { cc_state: 'active' }),
      liveRow('s2', { cc_state: 'active' }),
      liveRow('s3', { cc_state: 'idle' }),
      liveRow('s4', { cc_state: 'silent_evasion' }),
    ])
    const usage = deriveUsage(inventorySummaryFixture, live)
    expect(usage.activeAgents).toBe(
      inventorySummaryFixture.by_kind.agent.active,
    )
    // The headline is the Working set, not the inventory's catalog count.
    expect(usage.working).toBe(2)
    expect(usage.liveActive).toBe(2)
    expect(usage.silentEvasion).toBe(1)
    expect(usage.totalEntities).toBe(inventorySummaryFixture.total)
  })

  it('tolerates a missing inventory kind', () => {
    const inv: InventorySummary = { by_kind: {}, by_source: {}, total: 0 }
    const usage = deriveUsage(inv, list<LiveDTO>([]))
    expect(usage.activeAgents).toBe(0)
    expect(usage.totalAgents).toBe(0)
  })

  // ⛔ UNKNOWN IS NOT ZERO. Until 2026-09-08 a half the caller did not hand in was
  //    folded into the other: no live page → an empty list → "0 live"; no inventory →
  //    "0 agents". A denied, pending or failed read printed the same figure as an
  //    empty estate. Each half now answers for itself, and `null` is "not established".
  it('an absent live page leaves every live figure null, and keeps the inventory half', () => {
    const usage = deriveUsage(inventorySummaryFixture, undefined)
    expect(usage.activeAgents).toBe(
      inventorySummaryFixture.by_kind.agent.active,
    )
    // Who is working now is the live half's fact; without it the figure is unknown.
    expect(usage.working).toBeNull()
    expect(usage.totalEntities).toBe(inventorySummaryFixture.total)
    expect(usage.liveActive).toBeNull()
    expect(usage.liveIdle).toBeNull()
    expect(usage.silentEvasion).toBeNull()
    expect(usage.liveTotal).toBeNull()
  })

  it('an absent inventory leaves every inventory figure null, and keeps the live half', () => {
    const live = list<LiveDTO>([
      liveRow('s1', { cc_state: 'active' }),
      liveRow('s2', { cc_state: 'silent_evasion' }),
    ])
    const usage = deriveUsage(undefined, live)
    expect(usage.activeAgents).toBeNull()
    expect(usage.working).toBe(1)
    expect(usage.totalAgents).toBeNull()
    expect(usage.totalEntities).toBeNull()
    expect(usage.liveActive).toBe(1)
    expect(usage.silentEvasion).toBe(1)
    expect(usage.liveTotal).toBe(2)
    // Nothing to qualify: an unknown inventory is not a truncated one.
    expect(usage.truncated).toBe(false)
  })

  it('both halves absent: every figure null, still an object the view can print as "—"', () => {
    const usage = deriveUsage(undefined, undefined)
    expect(usage).toEqual({
      activeAgents: null,
      totalAgents: null,
      totalEntities: null,
      working: null,
      liveActive: null,
      liveIdle: null,
      liveNow: null,
      silentEvasion: null,
      liveTotal: null,
      truncated: false,
      livePartial: false,
    })
  })

  it('a successful EMPTY answer is a real zero on both halves', () => {
    const inv: InventorySummary = { by_kind: {}, by_source: {}, total: 0 }
    const usage = deriveUsage(inv, list<LiveDTO>([]))
    expect(usage).toEqual({
      activeAgents: 0,
      totalAgents: 0,
      totalEntities: 0,
      working: 0,
      liveActive: 0,
      liveIdle: 0,
      liveNow: 0,
      silentEvasion: 0,
      liveTotal: 0,
      truncated: false,
      livePartial: false,
    })
  })

  it('keeps the inventory truncation floor when that half is present', () => {
    const usage = deriveUsage(
      { ...inventorySummaryFixture, truncated: true },
      undefined,
    )
    expect(usage.truncated).toBe(true)
    expect(usage.liveActive).toBeNull()
  })

  // ⛔ A PAGE IS NOT THE POPULATION. `GET /v1/m/sessions/live` answers with ONE
  //    most-recent page — the store reads limit+1 rows (default 100, ceiling 1000,
  //    core/internal/store/sqlstore/generic.go) and reports `has_more` when a row
  //    existed beyond it (modules/sessions/api.go handleListLive). Every live figure
  //    here is counted over `live.items`, so on such a page each one is a FLOOR. The
  //    rollup says so on its own key, for Sessions alone: Inventory's `truncated` is
  //    another source's marker and neither borrows the other's.
  it('a live page that reports has_more marks the LIVE half partial, keeps its counts, and leaves the inventory marker alone', () => {
    const page: ListResponse<LiveDTO> = {
      items: [
        { cc_state: 'active' } as LiveDTO,
        { cc_state: 'active' } as LiveDTO,
        { cc_state: 'idle' } as LiveDTO,
      ],
      has_more: true,
    }
    const usage = deriveUsage(inventorySummaryFixture, page)
    expect(usage.livePartial).toBe(true)
    // The observed numbers are real answers (floors), not suppressed.
    expect(usage.liveActive).toBe(2)
    expect(usage.liveIdle).toBe(1)
    expect(usage.liveNow).toBe(3)
    expect(usage.liveTotal).toBe(3)
    // Inventory is complete here; the Sessions page does not make it partial.
    expect(usage.truncated).toBe(false)
  })

  it('has_more false, an absent live half, or a non-boolean flag: the live half is not marked partial (the badge rule, `=== true`)', () => {
    expect(
      deriveUsage(inventorySummaryFixture, list<LiveDTO>([])).livePartial,
    ).toBe(false)
    expect(deriveUsage(inventorySummaryFixture, undefined).livePartial).toBe(
      false,
    )
    // The cast is a TypeScript assertion, not a runtime check: a transport that
    // says "true" as a string must not read as a truncated page.
    const malformed = {
      items: [{ cc_state: 'active' }],
      has_more: 'true',
    } as unknown as ListResponse<LiveDTO>
    expect(deriveUsage(inventorySummaryFixture, malformed).livePartial).toBe(
      false,
    )
  })

  it('a truncated inventory does not mark the live half partial: each source keeps its own marker', () => {
    const usage = deriveUsage(
      { ...inventorySummaryFixture, truncated: true },
      list<LiveDTO>([{ cc_state: 'active' } as LiveDTO]),
    )
    expect(usage.truncated).toBe(true)
    expect(usage.livePartial).toBe(false)
    expect(usage.liveActive).toBe(1)
  })
})

describe('currentAnswer', () => {
  const page = list<LiveDTO>([{ cc_state: 'active' } as LiveDTO])

  it('hands in the data only for a permitted, currently successful query', () => {
    expect(currentAnswer(true, { isSuccess: true, data: page })).toBe(page)
  })

  it('withholds cached data once the read is no longer permitted', () => {
    // A disabled query keeps its last answer on the object; the role may not see it.
    expect(
      currentAnswer(false, { isSuccess: true, data: page }),
    ).toBeUndefined()
  })

  it('withholds retired data after a failure, and nothing while pending', () => {
    expect(
      currentAnswer(true, { isSuccess: false, data: page }),
    ).toBeUndefined()
    expect(
      currentAnswer(true, { isSuccess: false, data: undefined }),
    ).toBeUndefined()
  })
})

// ⛔ THE CARD AND THE LIST AGREE (#2268). The headline used to read the inventory
//    catalog (agents seen recently), so a dashboard could say "3 active agents" over a
//    Sessions list showing 9 Working, or the other way round. It is now the Working
//    set itself: the sessions the Sessions view files under Working, launched or
//    observed, from the same join and the same rule (`mergeSessions` + `groupOf`).
describe('deriveUsage — the Active agents card is the Sessions Working set', () => {
  const workingInSessionsView = (live: LiveDTO[], runs: RunDTO[]) =>
    mergeSessions(live, runs).filter((s) => groupOf(s) === 'active').length

  it('equals the Working count of the Sessions view for observed and launched sessions', () => {
    const live = [
      liveRow('obs-working', { cc_state: 'active' }),
      liveRow('obs-idle', { cc_state: 'idle' }),
      liveRow('obs-ended', { cc_state: 'ended' }),
      liveRow('obs-evasion', { cc_state: 'silent_evasion' }),
      // A launched session: its observed row says active, its run says it answered
      // and waits for its person. The run decides, so it is NOT working.
      liveRow('launched-idle', { cc_state: 'active' }),
      liveRow('launched-working', { cc_state: 'active' }),
    ]
    const runs = [
      runRow('run-a', { state: 'idle', claude_session_id: 'launched-idle' }),
      runRow('run-b', {
        state: 'running',
        claude_session_id: 'launched-working',
      }),
      // A launched session whose telemetry has not arrived is still working.
      runRow('run-c', { state: 'running' }),
    ]
    const usage = deriveUsage(inventorySummaryFixture, list(live), list(runs))
    expect(usage.working).toBe(workingInSessionsView(live, runs))
    expect(usage.working).toBe(3)
  })

  it('does not read the inventory catalog for the headline', () => {
    const inv: InventorySummary = {
      by_kind: { agent: { total: 9, active: 9, stale: 0 } },
      by_source: {},
      total: 9,
    }
    const usage = deriveUsage(
      inv,
      list([liveRow('only', { cc_state: 'idle' })]),
    )
    expect(usage.working).toBe(0)
    expect(usage.activeAgents).toBe(9)
  })

  it('counts a launched session once, not once per half', () => {
    const live = [liveRow('same', { cc_state: 'active' })]
    const runs = [
      runRow('run-1', { state: 'running', claude_session_id: 'same' }),
    ]
    expect(deriveUsage(undefined, list(live), list(runs)).working).toBe(1)
  })

  it('a run page with more behind it floors the figure like the observed page does', () => {
    const live = [liveRow('a')]
    const runs: ListResponse<RunDTO> = {
      items: [runRow('r1', { claude_session_id: 'a' })],
      has_more: true,
    }
    expect(deriveUsage(undefined, list(live), runs).livePartial).toBe(true)
    // Nothing to qualify without the live half it joins.
    expect(deriveUsage(undefined, undefined, runs).livePartial).toBe(false)
  })

  it('is unknown while the run half is in scope and not established', () => {
    // A launched session's state is its run's: counting it by the observed row would
    // print a number the Sessions view does not.
    const live = [liveRow('a'), liveRow('b')]
    expect(deriveUsage(undefined, list(live), null).working).toBeNull()
  })

  it('without the run half it counts what the Sessions view lists without it', () => {
    const live = [liveRow('a'), liveRow('b', { cc_state: 'idle' })]
    expect(deriveUsage(undefined, list(live), undefined).working).toBe(
      workingInSessionsView(live, []),
    )
  })
})

// --- risk --------------------------------------------------------------------

describe('deriveRisk', () => {
  it('counts only OPEN findings by severity (resolved/dismissed are not a posture)', () => {
    const findings = list<Finding>([
      finding('critical', 'open'),
      finding('high', 'triaged'),
      finding('medium', 'open'),
      finding('low', 'resolved'), // excluded
      finding('high', 'dismissed'), // excluded
    ])
    const risk = deriveRisk(findings)!
    expect(risk.openFindings).toBe(3)
    expect(risk.bySeverity.critical).toBe(1)
    expect(risk.bySeverity.high).toBe(1)
    expect(risk.bySeverity.low).toBe(0)
    expect(risk.criticalHigh).toBe(2)
  })

  it('matches the real findings fixture (low is resolved → excluded)', () => {
    const risk = deriveRisk(securityFindingsFixture)!
    expect(risk.openFindings).toBe(3)
    expect(risk.bySeverity.low).toBe(0)
  })
})

describe('deriveDrift', () => {
  it('is null until the access-map diff is read', () => {
    expect(deriveDrift(undefined)).toBeNull()
  })

  it('separates firm drift from reconciliation-pending and surfaces coverage limits', () => {
    // accessDriftFixture: 1 firm + 1 pending (approximate/opaque) unexpected, 1 unused.
    const drift = deriveDrift(accessDriftFixture)!
    expect(drift.unexpectedFirm).toBe(1)
    expect(drift.unexpectedPending).toBe(1)
    expect(drift.unused).toBe(1)
    // the pending edge is approximate + opaque, so it counts as a coverage limit.
    expect(drift.coverageLimited).toBeGreaterThanOrEqual(1)
  })

  it('never presents a truncated diff as an authoritative figure', () => {
    expect(deriveDrift(accessDriftFixture)!.truncated).toBe(false)
    expect(
      deriveDrift({ ...accessDriftFixture, truncated: true })!.truncated,
    ).toBe(true)
  })
})

// --- compliance --------------------------------------------------------------

describe('deriveCompliance', () => {
  it('sums controls across frameworks; coverage keeps unmapped in the denominator', () => {
    const summary: ComplianceSummaryResponse = {
      disclaimer: 'Control status, not a compliance claim.',
      frameworks: [
        {
          framework: 'a',
          name: 'A',
          version: '1',
          summary: {
            total: 10,
            satisfied: 4,
            by_design: 2,
            partial: 2,
            gap: 1,
            unmapped: 1,
          },
        },
        {
          framework: 'b',
          name: 'B',
          version: '1',
          summary: {
            total: 10,
            satisfied: 3,
            by_design: 1,
            partial: 3,
            gap: 2,
            unmapped: 1,
          },
        },
      ],
    }
    const c = deriveCompliance(summary)!
    expect(c.total).toBe(20)
    expect(c.satisfied).toBe(7)
    expect(c.byDesign).toBe(3)
    // (7 satisfied + 3 by_design) / 20 total = 50% — unmapped not removed from denom.
    expect(c.coveredPct).toBe(50)
    expect(c.disclaimer).toContain('not a compliance claim')
  })

  it('counts agent risk tiers from the classifications', () => {
    const summary: ComplianceSummaryResponse = {
      disclaimer: 'd',
      frameworks: [
        {
          framework: 'a',
          name: 'A',
          version: '1',
          summary: {
            total: 1,
            satisfied: 1,
            by_design: 0,
            partial: 0,
            gap: 0,
            unmapped: 0,
          },
        },
      ],
    }
    const c = deriveCompliance(summary, complianceRiskFixture)!
    const totalTiers = Object.values(c.riskTiers).reduce((s, n) => s + n, 0)
    expect(totalTiers).toBe(complianceRiskFixture.items.length)
  })

  it('works on the real summary fixture and always carries a disclaimer', () => {
    const c = deriveCompliance(complianceSummary)!
    expect(c.frameworks.length).toBeGreaterThan(0)
    expect(c.disclaimer.length).toBeGreaterThan(0)
  })
})

// --- health ------------------------------------------------------------------

describe('deriveHealth', () => {
  it('buckets states (unknown is its own bucket, not healthy) and counts breaches', () => {
    const status = list<StatusDTO>([
      { state: 'healthy', sla_breach_open: false } as StatusDTO,
      { state: 'degraded', sla_breach_open: false } as StatusDTO,
      { state: 'down', sla_breach_open: true } as StatusDTO,
      { state: 'unknown', sla_breach_open: false } as StatusDTO,
    ])
    const incidents = list<IncidentDTO>([
      { state: 'open' } as IncidentDTO,
      { state: 'resolved' } as IncidentDTO,
    ])
    const h = deriveHealth(status, incidents)!
    expect(h.healthy).toBe(1)
    expect(h.unknown).toBe(1)
    expect(h.down).toBe(1)
    expect(h.slaBreaches).toBe(1)
    expect(h.openIncidents).toBe(1)
    expect(h.total).toBe(4)
  })

  it('rolls up the real health fixtures', () => {
    const h = deriveHealth(healthStatusFixture, healthIncidentsFixture)!
    expect(h.total).toBe(3)
    expect(h.down).toBe(1)
    expect(h.openIncidents).toBe(1)
  })
})

// --- shared ------------------------------------------------------------------
