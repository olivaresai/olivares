// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The Retention page: reading retention schedules is Community, authoring one and the sweep are
// Business (docs/editions.md, "Records"), and the Compliance page that used to hold their tab is
// Business. The page must read only the retention routes, which a Community engine answers.
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
const build = { community: false as boolean | undefined }
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ ...DEFAULT_AUTH, can: granted.can }),
}))
vi.mock('@/lib/hooks/use-server-info', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/lib/hooks/use-server-info')>()),
  useCommunityBuild: () => build.community,
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
    build.community = false
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

  it('shows a Community admin the schedules but neither the sweep nor authoring', async () => {
    build.community = true
    renderIntel(<RetentionView />)

    expect(await screen.findByText('agent.memory')).toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: /run sweep/i }),
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: /add schedule|edit schedule/i }),
    ).not.toBeInTheDocument()
  })

  it('promises no deletion window for a stored schedule Community does not apply', async () => {
    build.community = true
    api.dataClasses.mockResolvedValue({
      items: [{ id: 'agent.memory', purgeable: true, model_io: true }],
    })
    api.retentionPolicies.mockResolvedValue({
      items: [
        {
          id: 'p1',
          data_class: 'agent.memory',
          retention_days: 30,
          disposition: 'purge',
          enabled: false,
          model_io: true,
          provider_floor_known: true,
          effective_disclosure_days: 30,
        },
      ],
    })
    renderIntel(<RetentionView />)

    expect(await screen.findByText('agent.memory')).toBeInTheDocument()
    expect(screen.queryByText(/Gone everywhere/i)).not.toBeInTheDocument()
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
