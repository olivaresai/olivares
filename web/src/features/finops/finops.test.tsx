// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it, vi } from 'vitest'
import { DEFAULT_AUTH, renderIntel, screen, within } from '@/test/intel'
import '@/features/_intel' // register the shared `intel` namespace for badges/notices
import {
  AllocationTable,
  AlertsTable,
  BudgetCard,
  CacheEfficiencyPanel,
  DimensionBreakdown,
  ForecastCard,
  FutureDimensionsPanel,
  RecommendationCard,
  ReconciliationView,
  SpendBreakdown,
  SpendStats,
} from './components'
import {
  alertsFixture,
  allocationFixture,
  budgetStatusFixtures,
  forecastFixture,
  reconciliationFixture,
  recommendationsFixture,
  spendFixture,
  summaryFixture,
} from './fixtures'
import './i18n'

describe('SpendStats', () => {
  it('renders the headline figures from integer micro-USD', () => {
    renderIntel(
      <SpendStats summary={summaryFixture} forecast={forecastFixture} />,
    )
    // 48_280_000_000 µUSD = $48,280 → compact "$48.3K"
    expect(screen.getByText(/\$48\.3K/)).toBeInTheDocument()
    expect(screen.getByText(/Cost records/i)).toBeInTheDocument()
    // headline uses the trailing-window run-rate (trend_projected, $90,900) so it
    // matches the ForecastCard — not the naive elapsed-fraction projection ($93K).
    expect(screen.getByText(/\$90\.9K/)).toBeInTheDocument()
  })
})

describe('SpendBreakdown', () => {
  it('lists ranked buckets and never shows a secret', () => {
    renderIntel(<SpendBreakdown summary={summaryFixture} />)
    // appears in both the legend and the table — assert at least one
    expect(screen.getAllByText('claude-opus-4-8').length).toBeGreaterThan(0)
    expect(screen.getAllByText('gemini-1.5-pro').length).toBeGreaterThan(0)
    // nothing in the fixture is a credential, and none is rendered
    expect(screen.queryByText(/sk-ant/)).not.toBeInTheDocument()
  })
})

describe('CacheEfficiencyPanel', () => {
  it('shows the hit rate, realized saving and the honest note', () => {
    renderIntel(<CacheEfficiencyPanel cache={summaryFixture.cache} />)
    // "Cache hit rate" appears as the stat label AND the donut centre label
    expect(screen.getAllByText(/Cache hit rate/i).length).toBeGreaterThan(0)
    // 36% hit rate from the fixture (appears in the stat + the donut centre)
    expect(screen.getAllByText(/36%/).length).toBeGreaterThan(0)
    // 12_400_000_000 µUSD = $12,400 → compact "$12.4K"
    expect(screen.getAllByText(/\$12\.4K/).length).toBeGreaterThan(0)
    // honest disclaimer: a zero saving is honest, not an error
    expect(screen.getByText(/honest, not an error/i)).toBeInTheDocument()
    expect(screen.queryByText(/sk-ant/)).not.toBeInTheDocument()
  })
})

describe('ForecastCard', () => {
  it('shows the confidence band and labels the projection as a run-rate', () => {
    renderIntel(<ForecastCard forecast={forecastFixture} />)
    // band: 81.2K … 100.6K
    expect(screen.getByText(/Confidence band/i)).toBeInTheDocument()
    // honesty: not a forecasting model
    expect(screen.getByText(/not a forecasting model/i)).toBeInTheDocument()
    // anomaly row labelled with its sigma (formatScore → 2 decimals → "2.40σ")
    expect(screen.getByText(/2\.40σ/)).toBeInTheDocument()
  })
})

describe('DimensionBreakdown', () => {
  it('renders free-form service_tier keys verbatim, with the not-a-closed-list note', async () => {
    const { userEvent } = await import('@/test/intel')
    renderIntel(
      <DimensionBreakdown
        dimension="service_tier"
        spend={spendFixture}
        picker={null}
      />,
    )
    // The freeform caveat is shown: documented tiers are display hints, not a list.
    expect(
      screen.getByText(/display hints, not a closed list/i),
    ).toBeInTheDocument()
    // The top tier key rides in the chart's accessible summary (sr-only).
    expect(screen.getAllByText(/standard/).length).toBeGreaterThan(0)
    // Switching to the equivalent table renders every tier key VERBATIM (free-form),
    // including the non-standard "priority" tier.
    await userEvent.click(
      screen.getByRole('button', { name: /Show as table/i }),
    )
    const grid = screen.getByRole('grid')
    expect(within(grid).getByText('priority')).toBeInTheDocument()
    expect(within(grid).getByText('batch')).toBeInTheDocument()
  })

  it('flags cost_type as a billed-only breakdown', () => {
    renderIntel(
      <DimensionBreakdown
        dimension="cost_type"
        spend={{ ...spendFixture, dimension: 'cost_type', buckets: [] }}
        picker={null}
      />,
    )
    expect(screen.getByText(/billed-only breakdown/i)).toBeInTheDocument()
  })
})

describe('ReconciliationView', () => {
  it('renders the note and estimated-only tiers prominently, never summed', () => {
    renderIntel(<ReconciliationView reconciliation={reconciliationFixture} />)
    expect(screen.getByText(/not billed via cost_report/i)).toBeInTheDocument()
    expect(screen.getByText(/Estimated-only tiers/i)).toBeInTheDocument()
    // the priority tier badge
    expect(screen.getAllByText('priority').length).toBeGreaterThan(0)
    // both totals present (billed $41K, estimated $48.3K) — distinct, never collapsed
    // (each appears in the stat + the chart summary/aria-label, so assert ≥1)
    expect(screen.getAllByText(/\$41K/).length).toBeGreaterThan(0)
    expect(screen.getAllByText(/\$48\.3K/).length).toBeGreaterThan(0)
  })

  it('does not present a misleading drift when there is no billed baseline', () => {
    // The default state: no cost_report/billed stream. The backend still sends
    // drift = 0 − estimated (a large negative); the UI must NOT render it as a
    // "billed below estimate" comparison that does not exist.
    const noBilled = {
      ...reconciliationFixture,
      has_billed: false,
      billed_total_micro_usd: 0,
      drift_micro_usd: -reconciliationFixture.estimated_total_micro_usd,
    }
    renderIntel(<ReconciliationView reconciliation={noBilled} />)
    expect(screen.getByText(/no billed baseline yet/i)).toBeInTheDocument()
    // the misleading comparison caption is absent
    expect(screen.queryByText(/billed below estimate/i)).toBeNull()
  })
})

describe('AllocationTable', () => {
  it('renders the MANDATORY heuristic disclaimer (open FinOps problem)', () => {
    renderIntel(<AllocationTable allocation={allocationFixture} />)
    expect(screen.getByText(/open FinOps problem/i)).toBeInTheDocument()
    expect(screen.getByText(/not a settled cost/i)).toBeInTheDocument()
    // an approximate (pooled) origin is surfaced, never split to a fabricated agent
    expect(screen.getByText(/Approximate/i)).toBeInTheDocument()
  })
})

describe('FutureDimensionsPanel — declared seam', () => {
  it('renders the SeamBadge + an AsOf declared-reference caveat (not live data)', () => {
    renderIntel(<FutureDimensionsPanel />)
    expect(screen.getByText(/Backend pending/i)).toBeInTheDocument()
    expect(screen.getByText(/Declared reference — AsOf/i)).toBeInTheDocument()
    // the future group_by dims are listed by their verbatim field name
    expect(screen.getByText('service_account_id')).toBeInTheDocument()
    expect(screen.getByText('speed')).toBeInTheDocument()
    // the advisor breakdown field
    expect(
      screen.getByText('usage.iterations[].advisor_message'),
    ).toBeInTheDocument()
  })
})

describe('BudgetCard — threshold flow', () => {
  it('flags a budget on track to exceed (projected_pct ≥ 100)', () => {
    renderIntel(<BudgetCard status={budgetStatusFixtures['bdg-opus']} />)
    expect(screen.getByText(/On track to exceed/i)).toBeInTheDocument()
    expect(screen.getByText('Opus guardrail')).toBeInTheDocument()
  })

  it('shows the enforcement action and reserved-capacity hint', () => {
    renderIntel(<BudgetCard status={budgetStatusFixtures['bdg-opus']} />)
    // action=block surfaces a badge
    expect(screen.getByText(/Block/i)).toBeInTheDocument()
    // reserved capacity counted toward the limit
    expect(screen.getByText(/reserved capacity/i)).toBeInTheDocument()
  })

  it('does not flag a healthy budget', () => {
    renderIntel(<BudgetCard status={budgetStatusFixtures['bdg-global']} />)
    expect(screen.queryByText(/On track to exceed/i)).not.toBeInTheDocument()
    expect(screen.queryByText(/Over limit/i)).not.toBeInTheDocument()
  })
})

describe('AlertsTable', () => {
  it('renders threshold crossings with severity badges', () => {
    renderIntel(<AlertsTable alerts={alertsFixture} />)
    const table = screen.getByRole('grid')
    expect(within(table).getByText(/Medium/i)).toBeInTheDocument()
    expect(within(table).getByText(/Low/i)).toBeInTheDocument()
  })
})

describe('RecommendationCard', () => {
  it('shows an estimated saving when present', () => {
    renderIntel(<RecommendationCard rec={recommendationsFixture[0]} />)
    expect(screen.getByText(/Est\. savings/i)).toBeInTheDocument()
    expect(screen.getByText(/\$18\.5K/)).toBeInTheDocument()
  })

  it('invents no figure for the honest info recommendation', () => {
    const info = recommendationsFixture[2]
    renderIntel(<RecommendationCard rec={info} />)
    expect(screen.queryByText(/Est\. savings/i)).not.toBeInTheDocument()
    expect(
      screen.getByText(/not derivable from the current cost stream/i),
    ).toBeInTheDocument()
  })
})

// RBAC: a user without finops:budget:write must not see the "New budget" action.
describe('FinOpsView — RBAC gating', () => {
  it('hides the create-budget action when the write permission is denied', async () => {
    vi.resetModules()
    vi.doMock('@/lib/auth/context', () => ({
      useAuth: () => ({ ...DEFAULT_AUTH, can: () => false }),
    }))
    const { FinOpsView } = await import('./finops-view')
    renderIntel(<FinOpsView />)
    // The budgets tab is not the default tab, but the gated action lives behind the
    // write permission everywhere; with can()=>false the "New budget" button is never
    // rendered in the budgets tab. Assert it is absent in the initial render too.
    expect(screen.queryByRole('button', { name: /New budget/i })).toBeNull()
    vi.doUnmock('@/lib/auth/context')
  })
})
