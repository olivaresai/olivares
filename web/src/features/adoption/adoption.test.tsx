// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it, vi } from 'vitest'
import { DEFAULT_AUTH, renderIntel, screen } from '@/test/intel'
import '@/features/_intel' // register the shared `intel` namespace for notices
import {
  AcceptanceBreakdown,
  BoundaryBanner,
  DeveloperTable,
  LensStats,
  ModelMix,
  OfficialObservedComparison,
  TeamTable,
} from './components'
import {
  discrepancyFixture,
  developersFixture,
  summaryFixture,
  teamsFixture,
} from './fixtures'
import './i18n'

describe('BoundaryBanner', () => {
  it('renders the honest Claude-API-only note naming the excluded providers', () => {
    renderIntel(<BoundaryBanner excludes={summaryFixture.boundary.excludes} />)
    expect(screen.getByText(/only Claude API traffic/i)).toBeInTheDocument()
    expect(screen.getByText(/Amazon Bedrock/)).toBeInTheDocument()
    expect(screen.getByText(/Vertex AI/)).toBeInTheDocument()
  })
})

describe('LensStats', () => {
  it('renders the productivity headline from integer counts', () => {
    renderIntel(<LensStats totals={summaryFixture.analytics.totals} />)
    expect(screen.getByText('412')).toBeInTheDocument() // sessions
    expect(screen.getByText('1,240')).toBeInTheDocument() // commits
    expect(screen.getByText('318')).toBeInTheDocument() // PRs
    // acceptance 0.818 -> 82%
    expect(screen.getByText('82%')).toBeInTheDocument()
  })

  it('omits active time for the analytics lens (the feed carries none)', () => {
    renderIntel(<LensStats totals={summaryFixture.analytics.totals} />)
    expect(screen.queryByText(/Active time/i)).toBeNull()
  })
})

describe('ModelMix', () => {
  it('renders the per-model token split', () => {
    renderIntel(<ModelMix byModel={summaryFixture.analytics.by_model} />)
    expect(screen.getAllByText(/claude-opus-4-8/).length).toBeGreaterThan(0)
    expect(screen.getAllByText(/claude-sonnet-4-6/).length).toBeGreaterThan(0)
  })

  it('shows an honest empty state with no model usage', () => {
    renderIntel(<ModelMix byModel={[]} />)
    expect(screen.getByText(/No model usage/i)).toBeInTheDocument()
  })
})

describe('AcceptanceBreakdown', () => {
  it('renders per-tool accept/reject and the acceptance rate', () => {
    renderIntel(
      <AcceptanceBreakdown byTool={summaryFixture.analytics.by_tool} />,
    )
    expect(screen.getByText('Edit')).toBeInTheDocument()
    expect(screen.getByText('NotebookEdit')).toBeInTheDocument()
    // Edit rate 0.835 -> 84%
    expect(screen.getByText('84%')).toBeInTheDocument()
  })
})

describe('OfficialObservedComparison', () => {
  it('renders material divergence badges and the 3P-surface boundary note', () => {
    renderIntel(<OfficialObservedComparison discrepancy={discrepancyFixture} />)
    expect(
      screen.getAllByText(/official \(Analytics API\)/i).length,
    ).toBeGreaterThan(0)
    expect(screen.getAllByText(/observed \(OTLP\)/i).length).toBeGreaterThan(0)
    expect(
      screen.getByText(/Observed exceeds official by 60%/i),
    ).toBeInTheDocument()
    expect(
      screen.getByText(/official Analytics API does not cover 3P surfaces/i),
    ).toBeInTheDocument()
  })
})

describe('TeamTable', () => {
  it('renders teams and labels the team-less bucket as unassigned', () => {
    renderIntel(<TeamTable teams={teamsFixture.teams} />)
    expect(screen.getByText('payments')).toBeInTheDocument()
    expect(screen.getByText(/\(unassigned\)/i)).toBeInTheDocument()
  })
})

describe('DeveloperTable', () => {
  it('renders the per-developer rows (the gated drill-down)', () => {
    renderIntel(<DeveloperTable developers={developersFixture.developers} />)
    expect(screen.getByText('ada@corp.example')).toBeInTheDocument()
    expect(screen.getByText('lin@corp.example')).toBeInTheDocument()
  })
})

// RBAC: a user without adoption:developer:read sees the locked notice, not the table.
describe('AdoptionView — per-developer gating', () => {
  it('locks the per-developer drill-down when the permission is denied', async () => {
    vi.resetModules()
    vi.doMock('@/lib/auth/context', () => ({
      useAuth: () => ({
        ...DEFAULT_AUTH,
        can: (perm: string) => perm !== 'adoption:developer:read',
      }),
    }))
    const { AdoptionView } = await import('./adoption-view')
    const user = (await import('@testing-library/user-event')).default.setup()
    renderIntel(<AdoptionView />)
    await user.click(screen.getByRole('tab', { name: /Developers/i }))
    expect(
      await screen.findByText(/Per-developer view restricted/i),
    ).toBeInTheDocument()
    vi.doUnmock('@/lib/auth/context')
  })

  it('shows the per-developer tab content when the permission is granted', async () => {
    vi.resetModules()
    vi.doMock('@/lib/auth/context', () => ({
      useAuth: () => ({ ...DEFAULT_AUTH, can: () => true }),
    }))
    const { AdoptionView } = await import('./adoption-view')
    const user = (await import('@testing-library/user-event')).default.setup()
    renderIntel(<AdoptionView />)
    await user.click(screen.getByRole('tab', { name: /Developers/i }))
    // The locked notice must NOT appear; the developers section card renders instead.
    expect(screen.queryByText(/Per-developer view restricted/i)).toBeNull()
    const heading = await screen.findAllByText(/By developer/i)
    expect(heading.length).toBeGreaterThan(0)
    vi.doUnmock('@/lib/auth/context')
  })
})
