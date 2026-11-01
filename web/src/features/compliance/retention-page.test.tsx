// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The Community Retention page: retention schedules are a Community capability
// (docs/editions.md, "Records"), and the Compliance page that used to hold their tab is Business.
// The page must read only the retention routes, which a Community engine answers.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { DEFAULT_AUTH, renderIntel, screen, waitFor } from '@/test/intel'
import '@/features/_intel'

const api = {
  dataClasses: vi.fn(),
  retentionPolicies: vi.fn(),
  retentionRuns: vi.fn(),
  putRetentionPolicy: vi.fn(),
  deleteRetentionPolicy: vi.fn(),
  runRetentionSweep: vi.fn(),
  summary: vi.fn(),
  frameworks: vi.fn(),
  holds: vi.fn(),
  erasures: vi.fn(),
  calendar: vi.fn(),
  risk: vi.fn(),
  residency: vi.fn(),
  evidence: vi.fn(),
}

const granted = { can: (_permission: string) => true }
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ ...DEFAULT_AUTH, can: granted.can }),
}))
vi.mock('./api', async () => {
  const actual = await vi.importActual<typeof import('./api')>('./api')
  return { ...actual, complianceApi: api }
})

const { RetentionView } = await import('./retention-page')
await import('./i18n')

describe('RetentionView', () => {
  beforeEach(() => {
    granted.can = () => true
    for (const fn of Object.values(api)) fn.mockReset()
    api.dataClasses.mockResolvedValue({
      items: [
        {
          id: 'agent.memory',
          label: 'Agent memory',
          can_purge: true,
          retention_floor_days: 0,
        },
      ],
    })
    api.retentionPolicies.mockResolvedValue({ items: [] })
    api.retentionRuns.mockResolvedValue({ items: [] })
  })

  it('paints the engine class registry and asks for no other compliance route', async () => {
    renderIntel(<RetentionView />)

    expect(
      await screen.findByRole('heading', { level: 1, name: 'Retention' }),
    ).toBeInTheDocument()
    await waitFor(() =>
      expect(screen.getByText('agent.memory')).toBeInTheDocument(),
    )
    expect(api.dataClasses).toHaveBeenCalled()
    expect(api.retentionPolicies).toHaveBeenCalled()
    for (const businessRead of [
      api.summary,
      api.frameworks,
      api.holds,
      api.erasures,
      api.calendar,
      api.risk,
      api.residency,
      api.evidence,
    ])
      expect(businessRead).not.toHaveBeenCalled()
  })

  it('offers the sweep to an admin of retention', async () => {
    renderIntel(<RetentionView />)

    expect(
      await screen.findByRole('button', { name: /run sweep/i }),
    ).toBeInTheDocument()
  })

  it('shows a reader the schedules but not the destructive sweep', async () => {
    granted.can = (permission) => permission === 'compliance:retention:read'
    renderIntel(<RetentionView />)

    expect(await screen.findByText('agent.memory')).toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: /run sweep/i }),
    ).not.toBeInTheDocument()
  })

  it('reads nothing without the retention read permission', async () => {
    granted.can = () => false
    renderIntel(<RetentionView />)

    expect(
      await screen.findByText('You do not have access to retention schedules'),
    ).toBeInTheDocument()
    expect(api.dataClasses).not.toHaveBeenCalled()
  })
})
