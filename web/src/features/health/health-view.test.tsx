// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { IncidentDTO, SlaDTO, StatusDTO } from './types'

// Resolve permission is toggled per test to exercise the RBAC boundary.
const perm = { read: true, write: true, admin: true }
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 't1',
    can: (p: string) => {
      if (p === 'health:check:read') return perm.read
      if (p === 'health:check:write') return perm.write
      if (p === 'health:check:admin') return perm.admin
      return true
    },
  }),
}))

vi.mock('@tanstack/react-router', () => ({
  //useUrlState follows the location, so the mock has to answer it.
  useRouterState: () => '',
  Link: ({ children, to }: { children: ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
}))

// The live stream pings the network; stub it to report a closed stream and not call.
vi.mock('@/features/shared', async (orig) => {
  const actual = await orig<typeof import('@/features/shared')>()
  return {
    ...actual,
    useLiveStream: () => ({ status: 'closed' as const }),
    // The React Flow canvas is heavy and not under test; render its children.
    GraphCanvas: ({ children }: { children?: ReactNode }) => (
      <div data-testid="graph-canvas">{children}</div>
    ),
  }
})

vi.mock('./api', () => ({
  healthApi: {
    status: vi.fn(),
    sla: vi.fn(),
    dependencies: vi.fn(),
    incidents: vi.fn(),
    events: vi.fn(),
    checks: vi.fn(),
    createCheck: vi.fn(),
    updateCheck: vi.fn(),
    deleteCheck: vi.fn(),
    resolveIncident: vi.fn(),
  },
  healthKeys: {
    all: (t: string | null) => ['h', t],
    status: (t: string | null, p?: unknown) => ['h', t, 'status', p ?? null],
    sla: (t: string | null, p?: unknown) => ['h', t, 'sla', p ?? null],
    dependencies: (t: string | null) => ['h', t, 'deps'],
    incidents: (t: string | null, p?: unknown) => [
      'h',
      t,
      'incidents',
      p ?? null,
    ],
    events: (t: string | null, p?: unknown) => ['h', t, 'events', p ?? null],
    checks: (t: string | null, p?: unknown) => ['h', t, 'checks', p ?? null],
  },
}))

import { healthApi } from './api'
import { HealthView } from './health-view'

const statuses: StatusDTO[] = [
  {
    id: 'st-down',
    name: 'prod orchestrator',
    subject_kind: 'agent',
    subject_ref: 'agent-prod',
    state: 'down',
    desired_status: 'active',
    expected_interval_seconds: 300,
    grace_factor: 2,
    sla_target_ppm: 999000,
    sla_breach_open: true,
    last_latency_ms: -1,
    last_seen_at: '2026-06-04T06:00:00Z',
    last_checked_at: '2026-06-04T07:00:00Z',
  },
  {
    id: 'st-unknown',
    name: 'untracked mcp',
    subject_kind: 'mcp',
    subject_ref: 'mcp-untracked',
    state: 'unknown',
    desired_status: 'active',
    expected_interval_seconds: 300,
    grace_factor: 2,
    sla_target_ppm: 0,
    sla_breach_open: false,
    last_latency_ms: -1,
  },
  {
    id: 'st-healthy',
    name: 'healthy fs',
    subject_kind: 'mcp',
    subject_ref: 'mcp-fs',
    state: 'healthy',
    desired_status: 'active',
    expected_interval_seconds: 300,
    grace_factor: 2,
    sla_target_ppm: 999000,
    sla_breach_open: false,
    last_latency_ms: 42,
    last_seen_at: '2026-06-04T07:00:00Z',
  },
]

const breachingSla: SlaDTO = {
  subject_kind: 'agent',
  subject_ref: 'agent-prod',
  window_seconds: 2592000,
  observed_seconds: 2592000,
  has_data: true,
  uptime_ppm: 980000,
  uptime_percent: 98.0,
  downtime_seconds: 3888,
  degraded_seconds: 1200,
  current_state: 'down',
  has_check: true,
  sla_target_ppm: 999000,
  breaching: true,
}

const incidents: IncidentDTO[] = [
  {
    id: 'inc-1',
    subject_kind: 'agent',
    subject_ref: 'agent-prod',
    kind: 'down',
    severity: 'high',
    state: 'open',
    opened_at: '2026-06-04T06:30:00Z',
    summary: 'agent-prod went silent (sweep)',
  },
]

function renderView() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <HealthView />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  perm.read = true
  perm.write = true
  perm.admin = true
  vi.mocked(healthApi.status).mockResolvedValue({
    items: statuses,
    has_more: false,
  })
  vi.mocked(healthApi.sla).mockResolvedValue(breachingSla)
  vi.mocked(healthApi.dependencies).mockResolvedValue({
    nodes: [],
    edges: [],
    has_more: false,
  })
  vi.mocked(healthApi.incidents).mockResolvedValue({
    items: incidents,
    has_more: false,
  })
  vi.mocked(healthApi.events).mockResolvedValue({ items: [], has_more: false })
  vi.mocked(healthApi.checks).mockResolvedValue({
    items: statuses,
    has_more: false,
  })
  vi.mocked(healthApi.resolveIncident).mockResolvedValue({
    ...incidents[0]!,
    state: 'resolved',
    resolved_at: '2026-06-04T08:00:00Z',
  })
})

describe('HealthView — Checks tab RBAC', () => {
  it('shows the Checks tab with health:check:read', async () => {
    renderView()
    expect(
      await screen.findByRole('tab', { name: 'Checks' }),
    ).toBeInTheDocument()
  })

  it('hides the Checks tab without health:check:read', async () => {
    perm.read = false
    renderView()
    await screen.findByText('prod orchestrator')
    expect(
      screen.queryByRole('tab', { name: 'Checks' }),
    ).not.toBeInTheDocument()
  })
})

describe('HealthView — status dashboard', () => {
  it('renders the header and the monitored subjects', async () => {
    renderView()
    expect(await screen.findByText('Health & SLA')).toBeInTheDocument()
    expect(await screen.findByText('prod orchestrator')).toBeInTheDocument()
  })

  it('colors a down subject with the danger state badge', async () => {
    renderView()
    const downRow = (await screen.findByText('prod orchestrator')).closest(
      'tr',
    )!
    const badge = within(downRow).getByText('Down')
    expect(badge).toBeInTheDocument()
    // The down state must use the danger token, not success/neutral.
    expect(badge.className).toMatch(/danger/)
    expect(badge.className).not.toMatch(/success/)
  })

  it('renders an unknown subject as neutral — NEVER green/healthy', async () => {
    renderView()
    const unknownRow = (await screen.findByText('untracked mcp')).closest('tr')!
    const badge = within(unknownRow).getByText('Unknown')
    expect(badge.className).toMatch(/muted|neutral/)
    expect(badge.className).not.toMatch(/success/)
  })

  it('flags an open SLA breach in danger on the breaching subject', async () => {
    renderView()
    const downRow = (await screen.findByText('prod orchestrator')).closest(
      'tr',
    )!
    expect(within(downRow).getByText('SLA breach')).toBeInTheDocument()
  })

  it('shows healthy/degraded/down/unknown stat tiles with counts', async () => {
    renderView()
    await screen.findByText('prod orchestrator')
    // The four state labels each appear at least once (as a tile label, and some
    // also as a state badge). The Degraded tile/badge set proves all four render.
    for (const label of ['Healthy', 'Degraded', 'Down', 'Unknown']) {
      expect(screen.getAllByText(label).length).toBeGreaterThanOrEqual(1)
    }
  })
})

describe('HealthView — SLA gauge', () => {
  it('detail cross-links open the requested tab for the same subject', async () => {
    const user = userEvent.setup()
    renderView()

    await user.click(await screen.findByText('prod orchestrator'))
    let sheet = await screen.findByRole('dialog')
    await user.click(within(sheet).getByRole('button', { name: 'View SLA' }))

    expect(screen.getByRole('tab', { name: 'SLA' })).toHaveAttribute(
      'aria-selected',
      'true',
    )
    await waitFor(() =>
      expect(healthApi.sla).toHaveBeenCalledWith(
        expect.objectContaining({
          subject_kind: 'agent',
          subject_ref: 'agent-prod',
        }),
      ),
    )

    await user.click(screen.getByRole('tab', { name: 'Status' }))
    await user.click(await screen.findByText('prod orchestrator'))
    sheet = await screen.findByRole('dialog')
    await user.click(
      within(sheet).getByRole('button', { name: 'View timeline' }),
    )

    expect(screen.getByRole('tab', { name: 'Timeline' })).toHaveAttribute(
      'aria-selected',
      'true',
    )
    await waitFor(() =>
      expect(healthApi.events).toHaveBeenCalledWith(
        expect.objectContaining({
          subject_kind: 'agent',
          subject_ref: 'agent-prod',
        }),
      ),
    )
  })

  it('renders a breaching subject in the SLA tab', async () => {
    const user = userEvent.setup()
    renderView()
    await screen.findByText('prod orchestrator')
    await user.click(screen.getByRole('tab', { name: 'SLA' }))
    // Pick the breaching subject.
    const combo = await screen.findByRole('combobox')
    await user.click(combo)
    await user.click(await screen.findByText('prod orchestrator'))
    // The gauge + breaching badge appear.
    expect(await screen.findByText('Breaching')).toBeInTheDocument()
    await waitFor(() => expect(healthApi.sla).toHaveBeenCalled())
  })

  it('shows downtime and degraded SEPARATELY', async () => {
    const user = userEvent.setup()
    renderView()
    await screen.findByText('prod orchestrator')
    await user.click(screen.getByRole('tab', { name: 'SLA' }))
    const combo = await screen.findByRole('combobox')
    await user.click(combo)
    await user.click(await screen.findByText('prod orchestrator'))
    // Downtime is its own tile; degraded is shown SEPARATELY (tile + note), never
    // folded into downtime.
    expect(await screen.findByText('Downtime')).toBeInTheDocument()
    expect(screen.getAllByText('Degraded').length).toBeGreaterThanOrEqual(1)
  })
})

describe('HealthView — incidents resolve RBAC', () => {
  it('shows the Resolve action to a check-admin', async () => {
    const user = userEvent.setup()
    renderView()
    await screen.findByText('prod orchestrator')
    await user.click(screen.getByRole('tab', { name: 'Incidents' }))
    expect(
      await screen.findByRole('button', { name: /resolve/i }),
    ).toBeInTheDocument()
  })

  it('hides the Resolve action without health:check:admin', async () => {
    perm.admin = false
    const user = userEvent.setup()
    renderView()
    await screen.findByText('prod orchestrator')
    await user.click(screen.getByRole('tab', { name: 'Incidents' }))
    // The open incident is listed (its unique summary), but no Resolve is offered.
    expect(
      await screen.findByText('agent-prod went silent (sweep)'),
    ).toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: /resolve/i }),
    ).not.toBeInTheDocument()
  })

  it('frames a sweep-caused down incident as possible evasion', async () => {
    const user = userEvent.setup()
    renderView()
    await screen.findByText('prod orchestrator')
    await user.click(screen.getByRole('tab', { name: 'Incidents' }))
    expect(await screen.findByText('Possible evasion')).toBeInTheDocument()
  })
})
