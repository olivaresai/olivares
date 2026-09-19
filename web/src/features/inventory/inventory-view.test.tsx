// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { CatalogEntry, EntityDetail, InventorySummary } from './types'

const auth = vi.hoisted(() => ({ denied: new Set<string>() }))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 't1',
    can: (permission: string) => !auth.denied.has(permission),
  }),
}))

// What a session WAS DOING: the live page the front door already reads.
const live = vi.hoisted(() => ({ live: vi.fn() }))
vi.mock('@/features/sessions/api', async (importOriginal) => {
  const actual =
    await importOriginal<typeof import('@/features/sessions/api')>()
  return { ...actual, sessionsApi: { ...actual.sessionsApi, ...live } }
})

vi.mock('@tanstack/react-router', () => ({
  //useUrlState follows the location, so the mock has to answer it.
  useRouterState: () => '',
  Link: ({ children, to }: { children: ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
  // No RouterProvider in this test: the shared Tabs strip consults useRouter, and the real
  // hook answers undefined here (console-tab-scroll-restoration R2, 2026-09-06).
  useRouter: () => undefined,
}))

vi.mock('./api', () => ({
  inventoryApi: {
    summary: vi.fn(),
    entities: vi.fn(),
    detail: vi.fn(),
    observations: vi.fn(),
  },
  inventoryKeys: {
    all: (t: string | null) => ['inv', t],
    summary: (t: string | null) => ['inv', t, 's'],
    entities: (t: string | null, p?: unknown) => ['inv', t, 'e', p ?? null],
    detail: (t: string | null, k: string, id: string) => ['inv', t, 'd', k, id],
    observations: (t: string | null, k: string, id: string) => [
      'inv',
      t,
      'o',
      k,
      id,
    ],
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
  auth.denied = new Set()
  live.live.mockReset()
  live.live.mockResolvedValue({
    items: [
      {
        session_ref: 'sess-coder-7a3f',
        cc_state: 'idle',
        current_action: 'create_issue',
        current_resource: 'github/create_issue',
        input_tokens: 0,
        output_tokens: 0,
        cost_micro_usd: 0,
        event_count: 0,
        tool_call_count: 0,
        first_event_at: '2026-09-18T10:00:00Z',
        last_event_at: '2026-09-18T10:00:10Z',
        duration_seconds: 10,
        live_ref: 'lr-1',
        attribution: 'legacy',
      },
    ],
    has_more: false,
  })
  vi.mocked(inventoryApi.summary).mockResolvedValue(summary)
  vi.mocked(inventoryApi.entities).mockResolvedValue({
    items: entries,
    has_more: false,
  })
  vi.mocked(inventoryApi.detail).mockResolvedValue(detail)
  vi.mocked(inventoryApi.observations).mockResolvedValue({
    items: [],
    has_more: false,
  })
})

describe('InventoryView', () => {
  it('shows estate summary tiles', async () => {
    renderView()
    // Total 4, active 3, stale 1 derived from the by-kind summary. The figures now
    // render only once the summary has ANSWERED (AsyncSection), so by then the
    // catalog rows — and their own "Stale" chip — are on the page too: the label is
    // read beside the figure it labels.
    expect(await screen.findByText('Entities')).toBeInTheDocument()
    const summary = screen.getByText('Entities').closest('ul') as HTMLElement
    expect(within(summary).getByText('4')).toBeInTheDocument()
    expect(within(summary).getByText('3')).toBeInTheDocument()
    expect(within(summary).getByText('1')).toBeInTheDocument()
  })

  it('paints each catalog name as one line and keeps the ref off the cell text', async () => {
    renderView()
    const name = await screen.findByText('prod-orchestrator')
    const cell = name.closest('td')!
    expect(cell.textContent).not.toMatch(/sess-orch/)
    expect(name.closest('[title]')?.getAttribute('title')).toMatch(/sess-orch/)
  })

  it('offers the next action when the catalog is empty', async () => {
    vi.mocked(inventoryApi.entities).mockResolvedValue({
      items: [],
      has_more: false,
    })
    renderView()
    expect(
      await screen.findByRole('link', { name: 'Open capabilities' }),
    ).toHaveAttribute('href', '/capabilities')
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

/**
 * A SESSION ENTITY IS NOT ITS REFERENCE.
 *
 * Every other entity in this catalog carries a name a person chose. A session
 * materialises from the ingest stream with none, so the column painted
 * `sess-coder-7a3f` — one of the two raw identifiers the census measured here. What a
 * session has is what it was doing, and the live page reports it.
 */
describe('InventoryView — a session in the catalog', () => {
  const sessions: CatalogEntry[] = [
    {
      kind: 'session',
      entity_id: 's1',
      name: 'sess-coder-7a3f',
      ref: 'sess-coder-7a3f',
      status: 'active',
      signal_sources: ['otel'],
      first_seen: '2026-09-18T10:00:00Z',
      last_seen: '2026-09-18T10:00:10Z',
      occurrence_count: 3,
    },
    {
      kind: 'session',
      entity_id: 's2',
      name: 'sess-coder-9c21',
      ref: 'sess-coder-9c21',
      status: 'active',
      signal_sources: ['otel'],
      first_seen: '2026-09-18T09:00:00Z',
      last_seen: '2026-09-18T09:01:00Z',
      occurrence_count: 1,
    },
  ]

  it('paints what the session was doing, with the reference reachable', async () => {
    vi.mocked(inventoryApi.entities).mockResolvedValue({
      items: sessions,
      has_more: false,
    })
    renderView()
    const label = await screen.findByText('create_issue · github/create_issue')
    expect(label.closest('[title]')?.getAttribute('title')).toContain(
      'sess-coder-7a3f',
    )
  })

  it('says a session has no title rather than printing its reference as one', async () => {
    // `sess-coder-9c21` is seeded with no action, goal or summary. The honest line is
    // "Untitled session" with the reference beside it — not the reference alone.
    vi.mocked(inventoryApi.entities).mockResolvedValue({
      items: sessions,
      has_more: false,
    })
    renderView()
    const untitled = await screen.findByText('Untitled session')
    const cell = untitled.closest('td')!
    expect(cell.textContent?.startsWith('Untitled session')).toBe(true)
    expect(within(cell).getByText('sess-coder-9c21')).toBeInTheDocument()
  })

  it('opens no cell with a raw reference, with or without the live read', async () => {
    auth.denied = new Set(['sessions:live:read'])
    vi.mocked(inventoryApi.entities).mockResolvedValue({
      items: sessions,
      has_more: false,
    })
    renderView()
    await screen.findAllByText('Untitled session')
    expect(live.live).not.toHaveBeenCalled()
    const raw = /^[0-9a-f]{8}-|^ppf_|^sess-/
    for (const cell of document.querySelectorAll('tbody td')) {
      expect((cell.textContent ?? '').trim()).not.toMatch(raw)
    }
  })
})
