// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { ReactNode } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { renderIntel, screen } from '@/test/intel'
import '@/features/_intel' // register the shared `intel` namespace for badges/notices
import './i18n'
import type { Run } from '@/features/redteam/types'
import type { ListResponse } from '@/lib/api/types'
import {
  deriveCompliance,
  deriveCost,
  deriveHealth,
  deriveRisk,
  deriveUsage,
} from './derive'
import {
  KpiTiles,
  ComplianceSection,
  ReliabilitySection,
  RiskSection,
  SpendSection,
} from './components'
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
  sessionsLiveFixture,
} from './fixtures'

// Render TanStack Router <Link> as a plain anchor (no RouterProvider in jsdom) — the
// established pattern across the view tests.
vi.mock('@tanstack/react-router', () => ({
  //useUrlState follows the location, so the mock has to answer it.
  useRouterState: () => '',
  Link: ({ children, to }: { children: ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
}))

const list = <T,>(items: T[]): ListResponse<T> => ({ items, has_more: false })
const run = (status: string, score: number, finished_at: string): Run =>
  ({ status, score, finished_at }) as unknown as Run

const cost = deriveCost(
  finopsSummaryFixture,
  finopsTrendFixture,
  finopsForecastFixture,
  modelsFixture,
)
const usage = deriveUsage(inventorySummaryFixture, sessionsLiveFixture)
const risk = deriveRisk(
  securityFindingsFixture,
  list([run('completed', 78, '2026-06-03T00:00:00Z')]),
  accessDriftFixture,
)
const compliance = deriveCompliance(complianceSummary, complianceRiskFixture)
const health = deriveHealth(healthStatusFixture, healthIncidentsFixture)

describe('KpiTiles (headline pillars)', () => {
  it('renders the four leadership figures from the rollups', () => {
    renderIntel(
      <KpiTiles
        cost={cost}
        usage={usage}
        risk={risk}
        compliance={compliance}
      />,
    )
    // total spend 48_280_000_000 µUSD = $48,280 → compact "$48.3K"
    expect(screen.getByText(/\$48\.3K/)).toBeInTheDocument()
    // open risk = 3 (low finding is resolved → excluded)
    expect(screen.getByText('Open risk')).toBeInTheDocument()
    expect(screen.getByText('Control coverage')).toBeInTheDocument()
  })

  it('omits a pillar whose source the role cannot read (so the PDF cannot leak it)', () => {
    renderIntel(
      <KpiTiles
        cost={null}
        usage={usage}
        risk={null}
        compliance={compliance}
      />,
    )
    expect(screen.queryByText('Spend')).not.toBeInTheDocument()
    expect(screen.queryByText('Open risk')).not.toBeInTheDocument()
    expect(screen.getByText('Control coverage')).toBeInTheDocument()
  })

  it('renders no secret', () => {
    renderIntel(
      <KpiTiles
        cost={cost}
        usage={usage}
        risk={risk}
        compliance={compliance}
      />,
    )
    expect(
      screen.queryByText(/sk-ant|secret|password/i),
    ).not.toBeInTheDocument()
  })
})

describe('RiskSection — honesty about a non-completed run', () => {
  it('never reads a degraded run as a pass — shows "pending", not a score', () => {
    const degraded = deriveRisk(
      securityFindingsFixture,
      list([run('degraded', 0, '2026-06-03T00:00:00Z')]),
      accessDriftFixture,
    )!
    renderIntel(<RiskSection risk={degraded} />)
    expect(screen.getByText(/Run pending/i)).toBeInTheDocument()
    // the coverage caveat is shown because the drift fixture has approximate/opaque edges
    expect(
      screen.getByText(/observed with partial coverage/i),
    ).toBeInTheDocument()
  })

  it('shows the robustness figure for a completed run', () => {
    renderIntel(<RiskSection risk={risk!} />)
    // RadialGauge renders the score 78 as its centered label.
    expect(screen.getByText('78')).toBeInTheDocument()
  })
})

describe('ComplianceSection — control status, never a compliance claim', () => {
  it('always renders the disclaimer and never says "certified"', () => {
    renderIntel(<ComplianceSection compliance={compliance!} />)
    expect(screen.getByText('Control coverage')).toBeInTheDocument()
    expect(screen.getByText(compliance!.disclaimer)).toBeInTheDocument()
    expect(screen.queryByText(/\bcertified\b/i)).not.toBeInTheDocument()
  })
})

describe('SpendSection — cost trend', () => {
  it('names the projection method and disclaims a model', () => {
    renderIntel(<SpendSection cost={cost!} />)
    expect(screen.getByText(/trailing-window daily trend/i)).toBeInTheDocument()
    expect(screen.getByText(/not a forecasting model/i)).toBeInTheDocument()
  })
})

describe('ReliabilitySection', () => {
  it('renders the health state mix and the point-in-time honesty note', () => {
    renderIntel(<ReliabilitySection health={health!} />)
    expect(screen.getByText(/point-in-time snapshot/i)).toBeInTheDocument()
    expect(screen.getByText('Open incidents')).toBeInTheDocument()
  })
})

// --- container RBAC gating ---------------------------------------------------

const auth = vi.hoisted(() => ({
  value: { activeTenant: 't1', can: (_p: string) => true } as {
    activeTenant: string | null
    can: (p: string) => boolean
  },
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => auth.value }))

afterEach(() => {
  auth.value = { activeTenant: 't1', can: () => true }
  vi.restoreAllMocks()
})

describe('ExecutiveView — RBAC gating', () => {
  it('shows the empty state when the role can read no source', async () => {
    auth.value = { activeTenant: 't1', can: () => false }
    const { ExecutiveView } = await import('./executive-view')
    renderIntel(<ExecutiveView />)
    expect(screen.getByText('No executive KPIs available')).toBeInTheDocument()
  })

  it('renders only the pillars/sections the role may read', async () => {
    auth.value = {
      activeTenant: 't1',
      // The permission the compliance pillar's own request requires: the view calls
      // complianceApi.summary(), and GET /v1/m/compliance/summary is gated on
      // compliance:framework:read. `compliance:read` was never a permission the
      // engine declared — it only worked here because this stub answers whatever
      // string the view happens to ask for.
      can: (p) => p === 'compliance:framework:read',
    }
    const { complianceApi } = await import('@/features/compliance/api')
    vi.spyOn(complianceApi, 'summary').mockResolvedValue(complianceSummary)
    vi.spyOn(complianceApi, 'risk').mockResolvedValue(complianceRiskFixture)
    const { ExecutiveView } = await import('./executive-view')

    renderIntel(<ExecutiveView />)

    // compliance renders (section title + its drill-down link)…
    expect((await screen.findAllByText('Compliance')).length).toBeGreaterThan(0)
    // …but cost / risk / reliability are gated out (never fetched, never shown).
    expect(screen.queryByText('Spend trend')).not.toBeInTheDocument()
    expect(screen.queryByText('Risk posture')).not.toBeInTheDocument()
    expect(screen.queryByText('Reliability')).not.toBeInTheDocument()
  })
})
