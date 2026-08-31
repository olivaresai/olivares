// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { CatalogEntry, EntityDetail, InventorySummary } from './types'

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

vi.mock('./api', () => ({
  inventoryApi: { summary: vi.fn(), entities: vi.fn(), detail: vi.fn() },
  inventoryKeys: {
    all: (t: string | null) => ['inv', t],
    summary: (t: string | null) => ['inv', t, 's'],
    entities: (t: string | null, p?: unknown) => ['inv', t, 'e', p ?? null],
    detail: (t: string | null, k: string, id: string) => ['inv', t, 'd', k, id],
  },
}))

import { inventoryApi } from './api'
import { InventoryView } from './inventory-view'

const summary: InventorySummary = {
  by_kind: {
    agent: { active: 2, stale: 1, total: 3 },
    model: { active: 1, stale: 0, total: 1 },
  },
  by_source: { otel: 4 },
  total: 4,
}

const entries: CatalogEntry[] = [
  {
    kind: 'agent',
    entity_id: 'a1',
    name: 'prod-orchestrator',
    ref: 'sess-orch',
    status: 'active',
    signal_sources: ['otel'],
    hosts: ['host-1'],
    first_seen: '2026-06-03T10:00:00Z',
    last_seen: '2026-06-04T07:00:00Z',
    occurrence_count: 42,
  },
  {
    kind: 'agent',
    entity_id: 'a2',
    name: 'idle-agent',
    ref: 'sess-idle',
    status: 'stale',
    signal_sources: ['pg_audit'],
    first_seen: '2026-06-01T10:00:00Z',
    last_seen: '2026-06-02T10:00:00Z',
    occurrence_count: 3,
  },
]

const detail: EntityDetail = {
  entry: entries[0]!,
  detail: { identity_id: 'identity-7', kind: 'claude_code' },
}

function renderView() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <InventoryView />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.mocked(inventoryApi.summary).mockResolvedValue(summary)
  vi.mocked(inventoryApi.entities).mockResolvedValue({
    items: entries,
    has_more: false,
  })
  vi.mocked(inventoryApi.detail).mockResolvedValue(detail)
})

describe('InventoryView', () => {
  it('shows estate summary tiles', async () => {
    renderView()
    // Total 4, active 3, stale 1 derived from the by-kind summary.
    expect(await screen.findByText('Entities')).toBeInTheDocument()
    expect(screen.getByText('Stale')).toBeInTheDocument()
  })

  it('lists catalog entries and flags a stale one', async () => {
    renderView()
    expect(await screen.findByText('prod-orchestrator')).toBeInTheDocument()
    const staleRow = screen.getByText('idle-agent').closest('tr')!
    expect(within(staleRow).getByText('Stale')).toBeInTheDocument()
  })

  it('opens the detail sheet with relations and cross-links on row click', async () => {
    const user = userEvent.setup()
    renderView()
    await user.click(await screen.findByText('prod-orchestrator'))
    // The detail sheet surfaces the core relation and the access-map cross-link.
    expect(await screen.findByText('Access map')).toBeInTheDocument()
    expect(await screen.findByText(/identity-7/)).toBeInTheDocument()
  })

  it('loads the catalog page from the inventory endpoint', async () => {
    renderView()
    await screen.findByText('prod-orchestrator')
    expect(vi.mocked(inventoryApi.entities)).toHaveBeenCalledWith(
      expect.objectContaining({ limit: expect.any(Number) }),
    )
  })
})
