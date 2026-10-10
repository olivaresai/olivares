// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import type { Finding } from '@/features/security/types'

import type { ComplianceSummaryResponse } from '@/features/compliance/types'
import type { StatusDTO, IncidentDTO } from '@/features/health/types'
import type { InventorySummary } from '@/features/inventory/types'
import type { LiveDTO } from '@/features/sessions/types'
import type { ListResponse } from '@/lib/api/types'
import {
  deriveCompliance,
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

describe('deriveUsage', () => {
  it('counts active agents, live states and surfaces silent-evasion', () => {
    const live = list<LiveDTO>([
      { cc_state: 'active' } as LiveDTO,
      { cc_state: 'active' } as LiveDTO,
      { cc_state: 'idle' } as LiveDTO,
      { cc_state: 'silent_evasion' } as LiveDTO,
    ])
    const usage = deriveUsage(inventorySummaryFixture, live)
    expect(usage.activeAgents).toBe(
      inventorySummaryFixture.by_kind.agent.active,
    )
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
    expect(usage.totalEntities).toBe(inventorySummaryFixture.total)
    expect(usage.liveActive).toBeNull()
    expect(usage.liveIdle).toBeNull()
    expect(usage.silentEvasion).toBeNull()
    expect(usage.liveTotal).toBeNull()
  })

  it('an absent inventory leaves every inventory figure null, and keeps the live half', () => {
    const live = list<LiveDTO>([
      { cc_state: 'active' } as LiveDTO,
      { cc_state: 'silent_evasion' } as LiveDTO,
    ])
    const usage = deriveUsage(undefined, live)
    expect(usage.activeAgents).toBeNull()
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

  it('separates firm drift from reconciliation-pending and surfaces coverage limits', () => {
    // accessDriftFixture: 1 firm + 1 pending (approximate/opaque) unexpected, 1 unused.
    const risk = deriveRisk(undefined, undefined, accessDriftFixture)!
    expect(risk.drift.unexpectedFirm).toBe(1)
    expect(risk.drift.unexpectedPending).toBe(1)
    expect(risk.drift.unused).toBe(1)
    // the pending edge is approximate + opaque → counted as a coverage limit.
    expect(risk.coverageLimited).toBeGreaterThanOrEqual(1)
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
