// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, within } from '@testing-library/react'
import { act } from 'react'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { UseLiveStreamOptions } from '@/features/shared'
import type { StatusDTO } from './types'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))

vi.mock('@tanstack/react-router', () => ({
  //useUrlState follows the location, so the mock has to answer it.
  useRouterState: () => '',
  Link: ({ children, to }: { children: ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
}))

// Capture the onSnapshot callback so the test can push a live frame manually.
let capturedOnSnapshot: ((s: StatusDTO, event: string) => void) | null = null
vi.mock('@/features/shared', async (orig) => {
  const actual = await orig<typeof import('@/features/shared')>()
  return {
    ...actual,
    useLiveStream: (opts: UseLiveStreamOptions<StatusDTO>) => {
      capturedOnSnapshot = opts.onSnapshot
      return { status: 'open' as const }
    },
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
  },
}))

import { healthApi } from './api'
import { HealthView } from './health-view'

const base: StatusDTO = {
  id: 'st-1',
  name: 'streaming agent',
  subject_kind: 'agent',
  subject_ref: 'agent-stream',
  state: 'healthy',
  desired_status: 'active',
  expected_interval_seconds: 300,
  grace_factor: 2,
  sla_target_ppm: 999000,
  sla_breach_open: false,
  last_latency_ms: 10,
  last_seen_at: '2026-06-04T07:00:00Z',
}

beforeEach(() => {
  capturedOnSnapshot = null
  vi.mocked(healthApi.status).mockResolvedValue({
    items: [base],
    has_more: false,
  })
  vi.mocked(healthApi.dependencies).mockResolvedValue({
    nodes: [],
    edges: [],
    has_more: false,
  })
  vi.mocked(healthApi.incidents).mockResolvedValue({
    items: [],
    has_more: false,
  })
  vi.mocked(healthApi.events).mockResolvedValue({ items: [], has_more: false })
})

function renderView() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <HealthView />
    </QueryClientProvider>,
  )
}

describe('HealthView — live stream patches a row in place', () => {
  it('updates the subject state by id when a frame arrives', async () => {
    renderView()
    const row = (await screen.findByText('streaming agent')).closest('tr')!
    expect(within(row).getByText('Healthy')).toBeInTheDocument()
    expect(capturedOnSnapshot).toBeTruthy()

    // A live frame transitions the same id to "down" — reconcile by id.
    act(() => {
      capturedOnSnapshot!({ ...base, state: 'down' }, 'health')
    })

    const updated = (await screen.findByText('streaming agent')).closest('tr')!
    expect(within(updated).getByText('Down')).toBeInTheDocument()
    expect(within(updated).queryByText('Healthy')).not.toBeInTheDocument()
  })
})
