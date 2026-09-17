// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Inventory — real scope and visible failures (2026-09-08).
//
// The engine reads Inventory TENANT-WIDE and ignores `workspace_id` on all three
// routes (modules/inventory/api.go:47,:80,:123; no lineage in schema.go:67). These
// causals pin what the console must therefore do, and each one names whether it
// REPRODUCES A DEFECT of the base (`d700064b7d`) — it goes red there — or is a
// POSITIVE CONTROL that the base already satisfied and must keep satisfying.
//
// Transport: `inventoryApi` is a mock; `inventoryKeys`, the view, the tabs, the
// DataTable, the workspace store and the i18n store are the real modules. A cache
// inspected through the real QueryClient is what proves "same key", not a spy.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, NetworkError } from '@/lib/api/errors'
import { useWorkspaceStore } from '@/stores/workspace'
import type { CatalogEntry, EntityDetail, InventorySummary } from './types'

// Mutable so one test can switch the tenant and prove the key follows it.
const auth = vi.hoisted(() => ({ activeTenant: 't1' as string | null }))

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: auth.activeTenant, can: () => true }),
}))

vi.mock('@tanstack/react-router', () => ({
  useRouterState: () => '',
  Link: ({ children, to }: { children: ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
  useRouter: () => undefined,
}))

// Only the transport is mocked: the real `inventoryKeys` stay, so a key that
// grew a workspace segment would show up in the real cache below.
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return {
    ...actual,
    inventoryApi: {
      summary: vi.fn(),
      entities: vi.fn(),
      detail: vi.fn(),
      observations: vi.fn(),
    },
  }
})

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

const forbidden = () =>
  new ApiError(403, 'forbidden', 'workspace confined', 'req-403')
const unavailable = () =>
  new ApiError(503, 'internal', 'store unavailable', 'req-503')

function renderView() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const tree = (
    <QueryClientProvider client={qc}>
      <InventoryView />
    </QueryClientProvider>
  )
  const utils = render(tree)
  return {
    qc,
    ...utils,
    // A NEW element each time: React skips a re-render for an identical one.
    rerender: () =>
      utils.rerender(
        <QueryClientProvider client={qc}>
          <InventoryView />
        </QueryClientProvider>,
      ),
  }
}

/** The inventory keys currently in the real cache, serialized for comparison.
 *  Two non-reads are left out: the closed detail sheet's disabled placeholder
 *  (`…,'detail','none'`), and the navigation authority's unasked capability
 *  observation (`auth-capabilities` / `unasked`) which EntityDetailSheet now
 *  consults via `useViewAccess` even while closed. Neither is a catalog read. */
const cacheKeys = (qc: QueryClient) =>
  qc
    .getQueryCache()
    .getAll()
    .map((q) => JSON.stringify(q.queryKey))
    .filter((k) => k.startsWith('["inventory"'))
    .filter((k) => !k.includes('"none"'))
    .sort()

/** The Card that holds one summary tile, found by its label INSIDE the tile grid
 *  ("Active"/"Stale" also label status chips in the catalog rows). */
const tile = (label: string) => {
  const grid = screen.getByText('Entities').parentElement!
    .parentElement as HTMLElement
  return within(grid).getByText(label).parentElement as HTMLElement
}

const calls = (fn: unknown) => vi.mocked(fn as () => unknown).mock.calls.length

beforeEach(() => {
  auth.activeTenant = 't1'
  useWorkspaceStore.setState({
    activeWorkspace: null,
    activeWorkspaceName: null,
  })
  vi.mocked(inventoryApi.summary).mockReset().mockResolvedValue(summary)
  vi.mocked(inventoryApi.entities)
    .mockReset()
    .mockResolvedValue({ items: entries, has_more: false })
  vi.mocked(inventoryApi.detail).mockReset().mockResolvedValue(detail)
  vi.mocked(inventoryApi.observations)
    .mockReset()
    .mockResolvedValue({ items: [], has_more: false })
})

describe('Inventory reads tenant-wide: scope of the query', () => {
  it('REPRODUCES A DEFECT: switching the workspace selector W1→W2 neither re-reads the inventory nor changes its keys, and no call carries workspace_id', async () => {
    useWorkspaceStore.setState({
      activeWorkspace: 'w1',
      activeWorkspaceName: 'W1',
    })
    const { qc } = renderView()
    await screen.findByText('prod-orchestrator')
    const summaryCalls = calls(inventoryApi.summary)
    const entitiesCalls = calls(inventoryApi.entities)
    const before = cacheKeys(qc)

    act(() => {
      useWorkspaceStore.setState({
        activeWorkspace: 'w2',
        activeWorkspaceName: 'W2',
      })
    })
    // Give a would-be refetch every chance to fire before asserting it did not.
    await act(async () => {
      await new Promise((r) => setTimeout(r, 30))
    })

    expect(calls(inventoryApi.summary)).toBe(summaryCalls)
    expect(calls(inventoryApi.entities)).toBe(entitiesCalls)
    expect(cacheKeys(qc)).toEqual(before)
    // The key names the tenant and never a workspace — on the base it carried
    // `"w1"` / `"w2"` as a trailing segment.
    for (const k of cacheKeys(qc)) {
      expect(k).toContain('"t1"')
      expect(k).not.toMatch(/"w[12]"|__all__/)
    }
    for (const [arg] of vi.mocked(inventoryApi.entities).mock.calls)
      expect(arg).not.toHaveProperty('workspace_id')
    // The summary wrapper takes no argument at all since 2026-09-08 (its optional
    // `workspace_id` went with its last two callers, Home and Executive): every call
    // is bare, and a bare call is the only thing the type admits.
    for (const call of vi.mocked(inventoryApi.summary).mock.calls)
      expect(call).toEqual([])
  })

  it('REPRODUCES A DEFECT: a workspace switch does not refetch observation history or add a workspace filter', async () => {
    useWorkspaceStore.setState({
      activeWorkspace: 'w1',
      activeWorkspaceName: 'W1',
    })
    const user = userEvent.setup()
    renderView()
    await user.click(await screen.findByText('prod-orchestrator'))
    expect(await screen.findByRole('dialog')).toBeInTheDocument()
    const obsCalls = calls(inventoryApi.observations)
    expect(obsCalls).toBeGreaterThan(0)

    act(() => {
      useWorkspaceStore.setState({
        activeWorkspace: 'w2',
        activeWorkspaceName: 'W2',
      })
    })
    await act(async () => {
      await new Promise((r) => setTimeout(r, 30))
    })

    expect(calls(inventoryApi.observations)).toBe(obsCalls)
    for (const call of vi.mocked(inventoryApi.observations).mock.calls) {
      expect(JSON.stringify(call)).not.toMatch(/workspace/)
    }
  })

  it('POSITIVE CONTROL: a different tenant is a different key and a fresh read', async () => {
    const { qc, rerender } = renderView()
    await screen.findByText('prod-orchestrator')
    const summaryCalls = calls(inventoryApi.summary)
    const entitiesCalls = calls(inventoryApi.entities)

    auth.activeTenant = 't2'
    rerender()

    await waitFor(() => {
      expect(calls(inventoryApi.summary)).toBe(summaryCalls + 1)
      expect(calls(inventoryApi.entities)).toBe(entitiesCalls + 1)
    })
    expect(cacheKeys(qc).some((k) => k.includes('"t2"'))).toBe(true)
  })

  it('REPRODUCES A DEFECT: the view says the workspace selector does not filter it', async () => {
    renderView()
    expect(
      await screen.findByText(
        'Tenant-wide inventory. The workspace selector does not filter this view.',
      ),
    ).toBeInTheDocument()
  })
})

describe('Inventory summary: error, pending, refusal and refetch', () => {
  it('REPRODUCES A DEFECT: an initial 403 renders as not authorized, never as a zero estate nor an empty one', async () => {
    vi.mocked(inventoryApi.summary).mockRejectedValue(forbidden())
    vi.mocked(inventoryApi.entities).mockRejectedValue(forbidden())
    renderView()
    const refusals = await screen.findAllByText('Not authorized')
    expect(refusals.length).toBeGreaterThanOrEqual(1)
    expect(screen.queryByText('Entities')).toBeNull()
    expect(screen.queryAllByText('0')).toHaveLength(0)
    expect(screen.queryByText('No estate entities discovered yet')).toBeNull()
    // A refusal is calm, not a failure: no alert, no retry.
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('REPRODUCES A DEFECT: an initial 503 renders the failure with its request id and a retry, not zeros', async () => {
    vi.mocked(inventoryApi.summary).mockRejectedValueOnce(unavailable())
    const user = userEvent.setup()
    renderView()
    const alert = await screen.findByRole('alert')
    expect(within(alert).getByText('Something went wrong')).toBeInTheDocument()
    expect(within(alert).getByText('req-503')).toBeInTheDocument()
    expect(screen.queryByText('Entities')).toBeNull()
    expect(screen.queryAllByText('0')).toHaveLength(0)

    // Retry reaches the engine again and the figures appear only then.
    await user.click(within(alert).getByRole('button', { name: 'Retry' }))
    expect(await screen.findByText('Entities')).toBeInTheDocument()
    expect(within(tile('Entities')).getByText('4')).toBeInTheDocument()
    expect(calls(inventoryApi.summary)).toBe(2)
  })

  it('REPRODUCES A DEFECT: a network failure is named as such, with a retry', async () => {
    vi.mocked(inventoryApi.summary).mockRejectedValue(
      new NetworkError('fetch failed'),
    )
    renderView()
    const alert = await screen.findByRole('alert')
    expect(
      within(alert).getByText('Control plane unreachable'),
    ).toBeInTheDocument()
    expect(
      within(alert).getByRole('button', { name: 'Retry' }),
    ).toBeInTheDocument()
    expect(screen.queryByText('Entities')).toBeNull()
  })

  it('REPRODUCES A DEFECT: success followed by a withdrawn permission shows the refusal, not the previous figures as current', async () => {
    vi.mocked(inventoryApi.summary)
      .mockResolvedValueOnce(summary)
      .mockRejectedValue(forbidden())
    const user = userEvent.setup()
    renderView()
    expect(await screen.findByText('Entities')).toBeInTheDocument()
    expect(within(tile('Entities')).getByText('4')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Refresh' }))

    expect(await screen.findByText('Not authorized')).toBeInTheDocument()
    expect(screen.queryByText('Entities')).toBeNull()
    expect(screen.queryByText('4')).toBeNull()
  })

  it('REPRODUCES A DEFECT: success followed by a failed refetch shows the failure, not stale figures', async () => {
    vi.mocked(inventoryApi.summary)
      .mockResolvedValueOnce(summary)
      .mockRejectedValue(unavailable())
    const user = userEvent.setup()
    renderView()
    expect(await screen.findByText('Entities')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Refresh' }))

    const alert = await screen.findByRole('alert')
    expect(within(alert).getByText('req-503')).toBeInTheDocument()
    expect(screen.queryByText('Entities')).toBeNull()
    expect(screen.queryByText('4')).toBeNull()
  })

  it('POSITIVE CONTROL: while pending no figure is shown — neither zeros nor a stale total', async () => {
    vi.mocked(inventoryApi.summary).mockReturnValue(new Promise(() => {}))
    renderView()
    await screen.findByText('prod-orchestrator')
    // The base already drew skeletons here; what must never appear is a number.
    expect(screen.queryAllByText('0')).toHaveLength(0)
    expect(screen.queryByText('4')).toBeNull()
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('POSITIVE CONTROL: an admitted empty estate is data and renders its zeros, not an error', async () => {
    vi.mocked(inventoryApi.summary).mockResolvedValue({
      by_kind: {},
      by_source: {},
      total: 0,
    })
    vi.mocked(inventoryApi.entities).mockResolvedValue({
      items: [],
      has_more: false,
    })
    renderView()
    expect(await screen.findAllByText('0')).toHaveLength(4)
    expect(screen.getByText('Entities')).toBeInTheDocument()
    expect(screen.queryByRole('alert')).toBeNull()
    expect(screen.queryByText('Not authorized')).toBeNull()
    expect(
      await screen.findByText('No estate entities discovered yet'),
    ).toBeInTheDocument()
  })
})

describe('Inventory summary: truncation', () => {
  const truncated: InventorySummary = {
    by_kind: { agent: { active: 900, stale: 100, total: 1000 } },
    by_source: { otel: 1000 },
    total: 1000,
    truncated: true,
  }

  it('REPRODUCES A DEFECT: a truncated summary is flagged partial and keeps every kind selectable', async () => {
    vi.mocked(inventoryApi.summary).mockResolvedValue(truncated)
    const user = userEvent.setup()
    renderView()
    expect(await screen.findByText('Partial data')).toBeInTheDocument()
    expect(within(tile('Entities')).getByText('1,000')).toBeInTheDocument()

    await user.click(screen.getByRole('combobox', { name: 'All kinds' }))
    await screen.findByRole('option', { name: 'Agent' })
    // The base offered only "Agent": a class absent from the first 1,000 entries
    // lost its option, i.e. the sample was read as the whole estate.
    for (const name of ['Session', 'Model', 'Provider', 'Resource'])
      expect(screen.getByRole('option', { name })).toBeInTheDocument()
  })

  it('REPRODUCES A DEFECT: a failed summary keeps every kind selectable rather than retiring options', async () => {
    vi.mocked(inventoryApi.summary).mockRejectedValue(unavailable())
    const user = userEvent.setup()
    renderView()
    await screen.findByRole('alert')
    await user.click(screen.getByRole('combobox', { name: 'All kinds' }))
    expect(
      await screen.findByRole('option', { name: 'Model' }),
    ).toBeInTheDocument()
    expect(screen.getByRole('option', { name: 'Session' })).toBeInTheDocument()
  })

  it('POSITIVE CONTROL: a complete summary is not flagged partial and narrows the kinds to those present', async () => {
    const user = userEvent.setup()
    renderView()
    expect(await screen.findByText('Entities')).toBeInTheDocument()
    expect(screen.queryByText('Partial data')).toBeNull()
    await user.click(screen.getByRole('combobox', { name: 'All kinds' }))
    expect(
      await screen.findByRole('option', { name: 'Agent' }),
    ).toBeInTheDocument()
    expect(screen.getByRole('option', { name: 'Model' })).toBeInTheDocument()
    expect(screen.queryByRole('option', { name: 'Session' })).toBeNull()
  })
})

describe('Inventory point: the detail sheet', () => {
  const openSheet = async (user: ReturnType<typeof userEvent.setup>) => {
    await user.click(await screen.findByText('prod-orchestrator'))
    return screen.findByRole('dialog')
  }

  it('REPRODUCES A DEFECT: a failed point read is visible in the sheet, with its request id and a retry', async () => {
    vi.mocked(inventoryApi.detail).mockRejectedValueOnce(
      new ApiError(503, 'internal', 'core getter failed', 'req-point'),
    )
    const user = userEvent.setup()
    renderView()
    const sheet = await openSheet(user)
    const alert = await within(sheet).findByRole('alert')
    expect(within(alert).getByText('req-point')).toBeInTheDocument()
    expect(within(sheet).queryByText('Relations')).toBeNull()

    await user.click(within(alert).getByRole('button', { name: 'Retry' }))
    expect(await within(sheet).findByText('Relations')).toBeInTheDocument()
    expect(within(sheet).getByText(/identity-7/)).toBeInTheDocument()
  })

  it('REPRODUCES A DEFECT: a refused point read is shown as not authorized, calmly', async () => {
    vi.mocked(inventoryApi.detail).mockRejectedValue(forbidden())
    const user = userEvent.setup()
    renderView()
    const sheet = await openSheet(user)
    expect(await within(sheet).findByText('Not authorized')).toBeInTheDocument()
    expect(within(sheet).queryByRole('alert')).toBeNull()
    // The catalog fields the row already carried stay visible: the refusal is
    // about the projection, not about the entry.
    expect(within(sheet).getByText('sess-orch')).toBeInTheDocument()
  })

  it('REPRODUCES A DEFECT: a point that is no longer in the catalog says so', async () => {
    vi.mocked(inventoryApi.detail).mockRejectedValue(
      new ApiError(404, 'not_found', 'not found', 'req-404'),
    )
    const user = userEvent.setup()
    renderView()
    const sheet = await openSheet(user)
    expect(
      await within(sheet).findByText('This entry is no longer in the catalog.'),
    ).toBeInTheDocument()
    expect(within(sheet).queryByRole('alert')).toBeNull()
  })

  it('REPRODUCES A DEFECT: nothing displayable — omitted detail OR present-but-unprintable — gets one neutral sentence, while printable false/0 still render', async () => {
    // INV-R1 (independent review, 2026-09-08): the empty branch is reached by an
    // OMITTED `detail` and by a PRESENT one whose values are all empty strings, null
    // or structured, so the sentence must not claim the engine returned nothing.
    // The base rendered nothing at all in the first two cases.
    const nothing =
      'No additional details are available to display for this entry. This does not establish whether it has relations.'
    const third: CatalogEntry = {
      ...entries[1]!,
      entity_id: 'a3',
      name: 'batch-runner',
      ref: 'sess-batch',
    }
    vi.mocked(inventoryApi.entities).mockResolvedValue({
      items: [...entries, third],
      has_more: false,
    })
    const projections: Record<string, EntityDetail> = {
      // (1) omitted: the engine dropped `detail` (core getter failed).
      a1: { entry: entries[0]! },
      // (2) present but nothing printable: empty strings, null, a structured value.
      a2: {
        entry: entries[1]!,
        detail: { name: '', family: '', owner: null, labels: { team: 'x' } },
      },
      // (3) printable falsy primitives are real values and must be shown.
      a3: { entry: third, detail: { enabled: false, retries: 0 } },
    }
    vi.mocked(inventoryApi.detail).mockImplementation(
      async (_kind: string, id: string) => projections[id]!,
    )
    const user = userEvent.setup()
    renderView()

    for (const [name, expectNothing] of [
      ['prod-orchestrator', true],
      ['idle-agent', true],
      ['batch-runner', false],
    ] as const) {
      await user.click(await screen.findByText(name))
      const sheet = await screen.findByRole('dialog')
      if (expectNothing) {
        expect(await within(sheet).findByText(nothing)).toBeInTheDocument()
        expect(within(sheet).queryByText('Relations')).toBeNull()
        // Neither the internal term nor a claim about what the engine returned.
        expect(within(sheet).queryByText(/projection|engine/i)).toBeNull()
      } else {
        expect(await within(sheet).findByText('Relations')).toBeInTheDocument()
        expect(within(sheet).getByText('false')).toBeInTheDocument()
        expect(within(sheet).getByText('0')).toBeInTheDocument()
        expect(within(sheet).queryByText(nothing)).toBeNull()
      }
      await user.keyboard('{Escape}')
      await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    }
    expect(vi.mocked(inventoryApi.detail)).toHaveBeenCalledTimes(3)
  })

  it('POSITIVE CONTROL: a projection renders its relations and the authorized cross-links', async () => {
    const user = userEvent.setup()
    renderView()
    const sheet = await openSheet(user)
    expect(await within(sheet).findByText('Relations')).toBeInTheDocument()
    expect(within(sheet).getByText(/identity-7/)).toBeInTheDocument()
    expect(within(sheet).getByText('Access map')).toBeInTheDocument()
    expect(within(sheet).getByText('Sessions')).toBeInTheDocument()
  })
})

describe('Inventory catalog and topology: what stays', () => {
  it('POSITIVE CONTROL: the kind facet is a server filter and search narrows the loaded rows', async () => {
    const user = userEvent.setup()
    renderView()
    await screen.findByText('prod-orchestrator')

    await user.click(screen.getByRole('combobox', { name: 'All kinds' }))
    await user.click(await screen.findByRole('option', { name: 'Model' }))
    await waitFor(() =>
      expect(vi.mocked(inventoryApi.entities)).toHaveBeenLastCalledWith(
        expect.objectContaining({ kind: 'model', limit: 50 }),
      ),
    )

    await user.type(
      screen.getByPlaceholderText('Search name, ref, signal…'),
      'idle',
    )
    expect(await screen.findByText('idle-agent')).toBeInTheDocument()
    await waitFor(() =>
      expect(screen.queryByText('prod-orchestrator')).toBeNull(),
    )
  })

  it('POSITIVE CONTROL: the status facet is a server filter', async () => {
    const user = userEvent.setup()
    renderView()
    await screen.findByText('prod-orchestrator')
    await user.click(screen.getByRole('combobox', { name: 'All status' }))
    await user.click(await screen.findByRole('option', { name: 'Stale' }))
    await waitFor(() =>
      expect(vi.mocked(inventoryApi.entities)).toHaveBeenLastCalledWith(
        expect.objectContaining({ status: 'stale' }),
      ),
    )
  })

  it('POSITIVE CONTROL: cursor paging continues from the returned cursor', async () => {
    vi.mocked(inventoryApi.entities)
      .mockResolvedValueOnce({
        items: [entries[0]!],
        has_more: true,
        cursor: 'c1',
      })
      .mockResolvedValueOnce({ items: [entries[1]!], has_more: false })
    const user = userEvent.setup()
    renderView()
    await screen.findByText('prod-orchestrator')
    await user.click(await screen.findByRole('button', { name: 'Load more' }))
    expect(await screen.findByText('idle-agent')).toBeInTheDocument()
    expect(vi.mocked(inventoryApi.entities)).toHaveBeenLastCalledWith(
      expect.objectContaining({ cursor: 'c1', limit: 50 }),
    )
  })

  it('POSITIVE CONTROL: the topology reads the same tenant-wide list with its own limit and declares that limit when there is more', async () => {
    vi.mocked(inventoryApi.entities).mockResolvedValue({
      items: entries,
      has_more: true,
      cursor: 'c1',
    })
    const user = userEvent.setup()
    renderView()
    await screen.findByText('prod-orchestrator')
    await user.click(screen.getByRole('tab', { name: 'Topology' }))
    expect(await screen.findByText('Who acts')).toBeInTheDocument()
    expect(screen.getByText('Showing first 200 entities')).toBeInTheDocument()
    expect(
      vi
        .mocked(inventoryApi.entities)
        .mock.calls.some(([a]) => a?.limit === 200),
    ).toBe(true)
  })

  it('REPRODUCES A DEFECT: the topology read carries no workspace_id and a W1→W2 switch does not re-read it', async () => {
    useWorkspaceStore.setState({
      activeWorkspace: 'w1',
      activeWorkspaceName: 'W1',
    })
    const user = userEvent.setup()
    const { qc } = renderView()
    await screen.findByText('prod-orchestrator')
    await user.click(screen.getByRole('tab', { name: 'Topology' }))
    expect(await screen.findByText('Who acts')).toBeInTheDocument()
    const topoCall = vi
      .mocked(inventoryApi.entities)
      .mock.calls.find(([a]) => a?.limit === 200)
    expect(topoCall).toBeDefined()
    expect(topoCall![0]).not.toHaveProperty('workspace_id')

    const entitiesCalls = calls(inventoryApi.entities)
    const before = cacheKeys(qc)
    act(() => {
      useWorkspaceStore.setState({
        activeWorkspace: 'w2',
        activeWorkspaceName: 'W2',
      })
    })
    await act(async () => {
      await new Promise((r) => setTimeout(r, 30))
    })
    expect(calls(inventoryApi.entities)).toBe(entitiesCalls)
    expect(cacheKeys(qc)).toEqual(before)
  })
})
