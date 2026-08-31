// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import type { Finding } from '@/features/security/types'
import type { Run } from '@/features/redteam/types'
import type { ComplianceSummaryResponse } from '@/features/compliance/types'
import type { StatusDTO, IncidentDTO } from '@/features/health/types'
import type { InventorySummary } from '@/features/inventory/types'
import type { LiveDTO } from '@/features/sessions/types'
import type { ListResponse } from '@/lib/api/types'
import {
  deriveCompliance,
  deriveCost,
  deriveHealth,
  deriveRisk,
  deriveUsage,
  latestRun,
  topBuckets,
  trendDeltaPct,
} from './derive'
import {
  accessDriftFixture,
  complianceRiskFixture,
  complianceSummary,
  finopsForecastFixture,
  finopsSummaryFixture,
  finopsTrendFixture,
  healthIncidentsFixture,
  healthStatusFixture,
  inventorySummaryFixture,
  modelsFixture,
  securityFindingsFixture,
} from './fixtures'

function list<T>(items: T[]): ListResponse<T> {
  return { items, has_more: false }
}
const finding = (severity: string, status = 'open'): Finding =>
  ({ severity, status }) as unknown as Finding
const run = (status: string, score: number, finished_at: string): Run =>
  ({ status, score, finished_at }) as unknown as Run

// --- cost --------------------------------------------------------------------

describe('deriveCost', () => {
  it('returns null without a summary (the headline can be gated/omitted)', () => {
    expect(deriveCost(undefined)).toBeNull()
  })

  it('rolls up spend, trend, run-rate-over flag and active-model count', () => {
    const cost = deriveCost(
      finopsSummaryFixture,
      finopsTrendFixture,
      finopsForecastFixture,
      modelsFixture,
    )!
    expect(cost.totalMicroUsd).toBe(finopsSummaryFixture.total_micro_usd)
    expect(cost.trend.length).toBe(finopsTrendFixture.days.length)
    // projected ($93K) > spend-so-far ($12.4K) → heads over this period.
    expect(cost.projectedOver).toBe(true)
    // governedModelsFixture: three active models.
    expect(cost.activeModels).toBe(3)
  })

  it('propagates a truncated aggregate (the figure is an honest floor)', () => {
    const cost = deriveCost(
      { ...finopsSummaryFixture, truncated: true },
      finopsTrendFixture,
    )!
    expect(cost.truncated).toBe(true)
  })
})

describe('trendDeltaPct', () => {
  it('is null without enough history', () => {
    expect(
      trendDeltaPct([{ cost_micro_usd: 1 }, { cost_micro_usd: 2 }]),
    ).toBeNull()
  })
  it('is null when the prior half is empty (no fabricated %)', () => {
    expect(
      trendDeltaPct([
        { cost_micro_usd: 0 },
        { cost_micro_usd: 0 },
        { cost_micro_usd: 5 },
        { cost_micro_usd: 5 },
      ]),
    ).toBeNull()
  })
  it('compares recent vs prior halves', () => {
    // prior=[10,10]=20, recent=[15,15]=30 → +50%
    expect(
      trendDeltaPct([
        { cost_micro_usd: 10 },
        { cost_micro_usd: 10 },
        { cost_micro_usd: 15 },
        { cost_micro_usd: 15 },
      ]),
    ).toBe(50)
  })
})

// --- usage -------------------------------------------------------------------

describe('deriveUsage', () => {
  it('counts active agents, live states and surfaces silent-evasion', () => {
    const live = list<LiveDTO>([
      { cc_state: 'active' } as LiveDTO,
      { cc_state: 'active' } as LiveDTO,
      { cc_state: 'idle' } as LiveDTO,
      { cc_state: 'silent_evasion' } as LiveDTO,
    ])
    const usage = deriveUsage(inventorySummaryFixture, live)!
    expect(usage.activeAgents).toBe(
      inventorySummaryFixture.by_kind.agent.active,
    )
    expect(usage.liveActive).toBe(2)
    expect(usage.silentEvasion).toBe(1)
    expect(usage.totalEntities).toBe(inventorySummaryFixture.total)
  })

  it('tolerates a missing inventory kind', () => {
    const inv: InventorySummary = { by_kind: {}, by_source: {}, total: 0 }
    const usage = deriveUsage(inv, list<LiveDTO>([]))!
    expect(usage.activeAgents).toBe(0)
    expect(usage.totalAgents).toBe(0)
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

  it('NEVER reads a degraded run as a pass (score is withheld, degraded flagged)', () => {
    const runs = list<Run>([
      run('completed', 80, '2026-06-01T00:00:00Z'),
      run('degraded', 0, '2026-06-03T00:00:00Z'), // latest, but pending
    ])
    const risk = deriveRisk(undefined, runs)!
    expect(risk.robustness.score).toBeNull()
    expect(risk.robustness.degraded).toBe(true)
    expect(risk.robustness.status).toBe('degraded')
  })

  it('reports a completed run score and is not degraded', () => {
    const runs = list<Run>([run('completed', 78, '2026-06-03T00:00:00Z')])
    const risk = deriveRisk(undefined, runs)!
    expect(risk.robustness.score).toBe(78)
    expect(risk.robustness.degraded).toBe(false)
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

describe('latestRun', () => {
  it('picks the most recent by finish time', () => {
    const a = run('completed', 1, '2026-06-01T00:00:00Z')
    const b = run('completed', 2, '2026-06-05T00:00:00Z')
    expect(latestRun([a, b])?.score).toBe(2)
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

describe('topBuckets', () => {
  it('sorts by cost descending and slices to N', () => {
    const buckets = [
      {
        key: 'a',
        cost_micro_usd: 1,
        input_tokens: 0,
        output_tokens: 0,
        samples: 0,
      },
      {
        key: 'b',
        cost_micro_usd: 3,
        input_tokens: 0,
        output_tokens: 0,
        samples: 0,
      },
      {
        key: 'c',
        cost_micro_usd: 2,
        input_tokens: 0,
        output_tokens: 0,
        samples: 0,
      },
    ]
    expect(topBuckets(buckets, 2).map((b) => b.key)).toEqual(['b', 'c'])
  })
})

describe('forecast field parity between Home and FinOps', () => {
  // ⛔ THE WITNESS P ASKED FOR, and it is the defect that already shipped: Home
  // read `projected_micro_usd` (the legacy naive elapsed-fraction projection)
  // while FinOps reads `trend_projected_micro_usd` (the trailing-window
  // run-rate), so the two screens showed two different numbers for the SAME
  // spend and period — $5,151 vs $5,039 in P's reproduction.
  //
  // Worse than a mismatch: Home's own caption says "at current run-rate"
  // (features/executive/i18n/en.json), which the naive field is NOT. The card
  // was making a claim about a method it did not use.
  //
  // This test discriminates because the fixture's two fields DIFFER
  // (93.0e9 vs 90.9e9). The pre-existing deriveCost test could not catch it:
  // it only asserted `projectedOver === true`, which holds for BOTH fields.
  it('Home derives the same projection field FinOps displays', () => {
    const cost = deriveCost(
      finopsSummaryFixture,
      finopsTrendFixture,
      finopsForecastFixture,
      modelsFixture,
    )
    expect(cost).not.toBeNull()
    expect(cost?.projectedMicroUsd).toBe(
      finopsForecastFixture.trend_projected_micro_usd,
    )
    // And explicitly NOT the naive field, so a revert cannot pass silently.
    expect(cost?.projectedMicroUsd).not.toBe(
      finopsForecastFixture.projected_micro_usd,
    )
  })

  // Control on the fixture itself: if both fields ever became equal, the
  // assertion above would pass for the wrong reason and stop protecting
  // anything.
  it('control: the fixture can tell the two projections apart', () => {
    expect(finopsForecastFixture.trend_projected_micro_usd).not.toBe(
      finopsForecastFixture.projected_micro_usd,
    )
  })

  // The over-budget badge must compare the SAME field the card shows.
  it('the projectedOver badge uses the displayed field', () => {
    const belowTrend = {
      ...finopsForecastFixture,
      spend_micro_usd: finopsForecastFixture.trend_projected_micro_usd + 1,
    }
    const cost = deriveCost(
      finopsSummaryFixture,
      finopsTrendFixture,
      belowTrend,
      modelsFixture,
    )
    expect(cost?.projectedOver).toBe(false)
  })
})

describe('honest labelling: the caption names the method it paints', () => {
  // ⛔ THE SECOND WITNESS PLAN RATIFIED, and it is the one that would have caught
  // MY OWN first commit. Unifying the field (witness 1) left both cards painting
  // the trailing-window trend under a caption that said "at current run-rate" —
  // and the sources disagree about what that phrase even means:
  //
  //   modules/finops/analytics.go:613  "the naive elapsed-fraction RUN-RATE
  //                                     (ProjectedMicroUSD, kept for continuity)"
  //   web/src/features/finops/types.ts:162  the trailing-window projection is
  //                                     "a projection AT THE CURRENT RUN-RATE"
  //
  // Both files attach "run-rate" to a DIFFERENT field, so the caption could never
  // identify which one was on screen. A label that cannot be wrong cannot be
  // right either. The fix is not a better adjective: it is naming the method.
  //
  // This asserts the property across EVERY locale, because the defect does not
  // speak English: the same caption shipped in seven languages.
  const locales = ['en', 'es', 'de', 'fr', 'ja', 'ru', 'zh'] as const

  // The method word, per locale, that a caption over trend_projected_micro_usd
  // must carry. If the displayed field ever changes, these change with it —
  // that coupling is the point.
  const methodWord: Record<string, string> = {
    en: 'trailing-window',
    es: 'ventana móvil',
    de: 'gleitenden Fenster',
    fr: 'fenêtre glissante',
    ja: '移動ウィンドウ',
    ru: 'скользящему окну',
    zh: '滑动窗口',
  }

  for (const loc of locales) {
    it(`${loc}: both captions name the trailing-window method`, async () => {
      const exec = (await import(`./i18n/${loc}.json`)).default
      const fin = (await import(`../finops/i18n/${loc}.json`)).default
      const pick = (o: unknown, key: string): string | null => {
        if (o && typeof o === 'object') {
          for (const [k, v] of Object.entries(o as Record<string, unknown>)) {
            if (k === key && typeof v === 'string') return v
            const r = pick(v, key)
            if (r !== null) return r
          }
        }
        return null
      }
      const execCaption = pick(exec, 'atRunRate')
      const finCaption = pick(fin, 'atRunRate')
      expect(execCaption).not.toBeNull()
      expect(finCaption).not.toBeNull()
      expect(execCaption).toContain(methodWord[loc])
      expect(finCaption).toContain(methodWord[loc])
    })
  }

  // Control: the assertion must be capable of failing. "run-rate" is the word
  // that was there before and describes BOTH fields, so no caption may keep it.
  it('control: no caption still carries the ambiguous run-rate wording', async () => {
    const en = (await import('./i18n/en.json')).default as Record<
      string,
      unknown
    >
    const flat = JSON.stringify(en)
    expect(flat).not.toContain('at current run-rate')
  })
})

describe('trendDeltaPct — the two halves must span the same number of days', () => {
  const flat = (n: number) =>
    Array.from({ length: n }, () => ({ cost_micro_usd: 1_000_000 }))

  // The defect: with an odd-length series the floor-split handed the recent half
  // one extra day, so the extra DAY was read as extra SPEND. Perfectly flat spend
  // over five days was reported as "+50% vs the prior period" — a number the
  // console showed to an operator whose spend had not moved at all.
  it('reports no change for flat spend over an ODD number of days', () => {
    expect(trendDeltaPct(flat(5))).toBe(0)
    expect(trendDeltaPct(flat(7))).toBe(0)
    expect(trendDeltaPct(flat(31))).toBe(0)
  })

  it('reports no change for flat spend over an EVEN number of days', () => {
    expect(trendDeltaPct(flat(4))).toBe(0)
    expect(trendDeltaPct(flat(30))).toBe(0)
  })

  // Control: it must still SEE a real change, or the fix above could be "return 0".
  it('still measures a real doubling, and does not invent one', () => {
    const doubled = [
      { cost_micro_usd: 100 },
      { cost_micro_usd: 100 },
      { cost_micro_usd: 200 },
      { cost_micro_usd: 200 },
    ]
    expect(trendDeltaPct(doubled)).toBe(100)
  })

  // With an odd count the OLDEST day is dropped, not shared: the window keeps
  // ending at today. 5 days [ignored, 100, 100, 200, 200] -> +100%, not +66.7%.
  it('drops the oldest day when the count is odd, keeping the window at today', () => {
    const odd = [
      { cost_micro_usd: 999_999 },
      { cost_micro_usd: 100 },
      { cost_micro_usd: 100 },
      { cost_micro_usd: 200 },
      { cost_micro_usd: 200 },
    ]
    expect(trendDeltaPct(odd)).toBe(100)
  })
})
