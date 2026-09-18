// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The front door's LIVE SESSIONS and INVENTORY tiles and the topbar workspace selector.
//
// Both reads are tenant-wide. `GET /v1/m/sessions/live` takes no core-workspace
// selector and its DTO carries none (an internal design note (not shipped)
// contract, ratified 2026-09-08); `GET /v1/m/inventory/summary` reads no request
// filter and the catalog carries no workspace lineage (an internal design note (not shipped)
// effective-workspace-scope, ratified the same day). The Sessions tile stopped sending
// `workspace_id` first; until this change the Inventory tile still sent it and keyed
// its query on the selection, so a W1→W2 switch re-fetched the SAME tenant-wide
// summary and presented it as the new workspace's estate — beside a Sessions tile that
// already said it was tenant-wide. These cases run the real view against the real
// workspace store and a real QueryClient; only the API modules and auth are doubled.
//
// CONTROLLED FIXTURES, NOT BACKEND PROOF. Each double deliberately answers a DIFFERENT
// page when any selector is sent, so a pretended filter is measurable as a wrong
// number, not only as a wrong argument. The engine has no such scoped answer: what the
// engine does is the ratified static reading cited above, not anything in this file.
import type { ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { QueryClient } from '@tanstack/react-query'
import {
  act,
  createTestQueryClient,
  renderIntel,
  screen,
  waitFor,
  within,
} from '@/test/intel'
import { ApiError } from '@/lib/api/errors'
import type { ListResponse } from '@/lib/api/types'
import type { LiveDTO } from '@/features/sessions/types'
import type { InventorySummary } from '@/features/inventory/types'
import { sessionsApi, type LiveListParams } from '@/features/sessions/api'
import { inventoryApi } from '@/features/inventory/api'
import { useWorkspaceStore } from '@/stores/workspace'
import {
  inventorySummaryFixture,
  sessionsLiveFixture,
} from '@/features/executive/fixtures'
import { HomeView } from './home-view'
import './i18n'

vi.mock('@tanstack/react-router', () => ({
  useRouterState: () => '',
  Link: ({ children, to }: { children: ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
}))

const authState = vi.hoisted(() => ({
  can: (_p: string): boolean => true,
  activeTenant: 'demo' as string | null,
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))

const TENANT_WIDE_LABEL = 'Tenant-wide — not filtered by workspace'

const BOTH = (p: string) =>
  p === 'sessions:live:read' || p === 'inventory:catalog:read'
const SESSIONS_ONLY = (p: string) => p === 'sessions:live:read'
const INVENTORY_ONLY = (p: string) => p === 'inventory:catalog:read'

/** The tenant-wide page: three active rows → the Sessions tile reads "3". */
const TENANT_WIDE_PAGE: ListResponse<LiveDTO> = {
  items: sessionsLiveFixture.items.map((row, i) => ({
    ...row,
    session_ref: `wide-${i}`,
    live_ref: `lr-wide-${i}`,
    cc_state: 'active',
  })),
  has_more: false,
}

/** What a SCOPED sessions endpoint would answer — one row. The engine has no such
 *  endpoint; this page exists so that a filter the view pretends to apply shows up
 *  as "1". */
const PRETENDED_SCOPED_PAGE: ListResponse<LiveDTO> = {
  items: [{ ...sessionsLiveFixture.items[0]!, cc_state: 'active' }],
  has_more: false,
}

/** The tenant-wide summary: 25 tracked entities → the Inventory tile reads "25". */
const TENANT_WIDE_SUMMARY: InventorySummary = inventorySummaryFixture

/** What a SCOPED summary would answer — 7 tracked. The engine has no such answer;
 *  this fixture exists so that a selector the view still sent shows up as "7". */
const PRETENDED_SCOPED_SUMMARY: InventorySummary = {
  by_kind: {
    agent: { active: 1, stale: 0, total: 1 },
    resource: { active: 6, stale: 0, total: 6 },
  },
  by_source: { otel: 7 },
  total: 7,
}

function liveDouble(params?: LiveListParams) {
  return Promise.resolve(
    params && 'workspace_id' in params && params.workspace_id !== undefined
      ? PRETENDED_SCOPED_PAGE
      : TENANT_WIDE_PAGE,
  )
}

/** The summary wrapper takes no argument any more (the type forbids one); the double
 *  still answers the pretended page to ANY argument, so a regression that smuggles a
 *  selector back in is a wrong figure on the tile, not only a wrong call. */
function summaryDouble(...args: unknown[]) {
  return Promise.resolve(
    args.length > 0 ? PRETENDED_SCOPED_SUMMARY : TENANT_WIDE_SUMMARY,
  )
}

function sessionsTile() {
  return screen.getByText('Live sessions').closest('a')!
}

function inventoryTile() {
  return screen.getByText('Inventory').closest('a')!
}

/** The inventory keys currently in the REAL cache — the proof of "same key" is the
 *  cache, not a spy. */
const inventoryCacheKeys = (qc: QueryClient) =>
  qc
    .getQueryCache()
    .getAll()
    .map((q) => q.queryKey)
    .filter((k) => k[0] === 'inventory')
    .map((k) => JSON.stringify(k))
    .sort()

let live: ReturnType<typeof vi.spyOn>
let summary: ReturnType<typeof vi.spyOn>

beforeEach(() => {
  authState.can = BOTH
  authState.activeTenant = 'demo'
  useWorkspaceStore.setState({
    activeWorkspace: null,
    activeWorkspaceName: null,
  })
  live = vi.spyOn(sessionsApi, 'live').mockImplementation(liveDouble)
  summary = vi.spyOn(inventoryApi, 'summary').mockImplementation(summaryDouble)
})

afterEach(() => {
  vi.restoreAllMocks()
})

async function settled() {
  await waitFor(() =>
    expect(within(sessionsTile()).getByText('3')).toBeInTheDocument(),
  )
  await waitFor(() =>
    expect(within(inventoryTile()).getByText('25')).toBeInTheDocument(),
  )
}

describe('HomeView — neither the Sessions nor the Inventory tile pretends the workspace filter', () => {
  it('reads both tenant-wide pages with no selector at all — W1 selected, the tiles read "3" and "25", never the pretended "1" and "7"', async () => {
    useWorkspaceStore.getState().setActiveWorkspace('ws-a', 'Workspace A')

    renderIntel(<HomeView />)
    await settled()

    // No workspace went out with either read: both endpoints ignore it, and sending
    // it anyway is how the tiles below read "1" and "7" before this change.
    expect(live).toHaveBeenCalledTimes(1)
    expect(live.mock.calls[0]?.[0] ?? {}).not.toHaveProperty('workspace_id')
    expect(within(sessionsTile()).queryByText('1')).toBeNull()
    expect(summary).toHaveBeenCalledTimes(1)
    expect(summary.mock.calls[0]).toEqual([])
    expect(within(inventoryTile()).queryByText('7')).toBeNull()
  })

  it('W1→W2, then all workspaces: no second Inventory request, no second Sessions request, same keys, same figures', async () => {
    useWorkspaceStore.getState().setActiveWorkspace('ws-a', 'Workspace A')
    const qc = createTestQueryClient()

    renderIntel(<HomeView />, { queryClient: qc })
    await settled()
    // One inventory entry, keyed by tenant and nothing else — the same entry the
    // Inventory sheet reads. On the base it ended in `"ws-a"`.
    const keysBefore = inventoryCacheKeys(qc)
    expect(keysBefore).toEqual([
      JSON.stringify(['inventory', 'demo', 'summary']),
    ])

    act(() => {
      useWorkspaceStore.getState().setActiveWorkspace('ws-b', 'Workspace B')
    })
    // Give a would-be refetch every chance to fire before asserting it did not.
    await act(async () => {
      await new Promise((r) => setTimeout(r, 30))
    })
    expect(summary).toHaveBeenCalledTimes(1)
    expect(live).toHaveBeenCalledTimes(1)
    expect(inventoryCacheKeys(qc)).toEqual(keysBefore)
    expect(within(inventoryTile()).getByText('25')).toBeInTheDocument()
    expect(within(sessionsTile()).getByText('3')).toBeInTheDocument()

    // Clearing the selection ("all workspaces") is equally quiet for both.
    act(() => {
      useWorkspaceStore.getState().setActiveWorkspace(null)
    })
    await act(async () => {
      await new Promise((r) => setTimeout(r, 30))
    })
    expect(summary).toHaveBeenCalledTimes(1)
    expect(live).toHaveBeenCalledTimes(1)
    expect(inventoryCacheKeys(qc)).toEqual(keysBefore)
    expect(within(inventoryTile()).getByText('25')).toBeInTheDocument()
    expect(within(sessionsTile()).getByText('3')).toBeInTheDocument()
  })

  it('states the scope on both tiles — once each, inside the tile, and in the link’s accessible name', async () => {
    useWorkspaceStore.getState().setActiveWorkspace('ws-a', 'Workspace A')

    renderIntel(<HomeView />)
    await settled()

    // Exactly two disclosures on the page, one inside each tenant-wide tile; no other
    // tile carries one.
    const notes = screen.getAllByText(TENANT_WIDE_LABEL)
    expect(notes).toHaveLength(2)
    expect(screen.getByTestId('home-sessions-scope-note')).toHaveTextContent(
      TENANT_WIDE_LABEL,
    )
    expect(screen.getByTestId('home-inventory-scope-note')).toHaveTextContent(
      TENANT_WIDE_LABEL,
    )
    expect(sessionsTile()).toContainElement(
      screen.getByTestId('home-sessions-scope-note'),
    )
    expect(inventoryTile()).toContainElement(
      screen.getByTestId('home-inventory-scope-note'),
    )
    // Each is part of its link's accessible name, so assistive technology reads the
    // scope with the figure, not as a stray footnote.
    const links = screen.getAllByRole('link', {
      name: /not filtered by workspace/i,
    })
    expect(links.map((a) => a.getAttribute('href')).sort()).toEqual([
      '/inventory',
      '/sessions',
    ])
  })

  it('the label is a description, not a grant: with only inventory:catalog:read there is one tile, one label and no Sessions read', async () => {
    useWorkspaceStore.getState().setActiveWorkspace('ws-a', 'Workspace A')
    authState.can = INVENTORY_ONLY

    renderIntel(<HomeView />)
    await waitFor(() =>
      expect(within(inventoryTile()).getByText('25')).toBeInTheDocument(),
    )

    expect(screen.queryByText('Live sessions')).toBeNull()
    expect(live).not.toHaveBeenCalled()
    const notes = screen.getAllByText(TENANT_WIDE_LABEL)
    expect(notes).toHaveLength(1)
    expect(inventoryTile()).toContainElement(notes[0]!)
  })

  it('the label is a description, not a grant: with only sessions:live:read there is one tile, one label and no Inventory read', async () => {
    useWorkspaceStore.getState().setActiveWorkspace('ws-a', 'Workspace A')
    authState.can = SESSIONS_ONLY

    renderIntel(<HomeView />)
    await waitFor(() =>
      expect(within(sessionsTile()).getByText('3')).toBeInTheDocument(),
    )

    expect(screen.queryByText('Inventory')).toBeNull()
    expect(summary).not.toHaveBeenCalled()
    const notes = screen.getAllByText(TENANT_WIDE_LABEL)
    expect(notes).toHaveLength(1)
    expect(sessionsTile()).toContainElement(notes[0]!)
  })

  it('T1→T2: a real tenant change re-reads both under the new tenant, into a cache keyed by that tenant', async () => {
    const qc = createTestQueryClient()

    const { rerender } = renderIntel(<HomeView />, { queryClient: qc })
    await settled()
    expect(live).toHaveBeenCalledTimes(1)
    expect(summary).toHaveBeenCalledTimes(1)

    authState.activeTenant = 'other-tenant'
    rerender(<HomeView />)

    // The tenant is part of the key; a new tenant is a new read (unchanged rule) —
    // still bare, still keyed by the tenant alone.
    await waitFor(() => expect(live).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(summary).toHaveBeenCalledTimes(2))
    expect(live.mock.calls[1]?.[0] ?? {}).not.toHaveProperty('workspace_id')
    expect(summary.mock.calls[1]).toEqual([])
    expect(inventoryCacheKeys(qc)).toContain(
      JSON.stringify(['inventory', 'other-tenant', 'summary']),
    )
  })

  it.each([
    ['500', new ApiError(500, 'server_error', 'boom')],
    ['403', new ApiError(403, 'forbidden', 'no')],
  ])(
    'a %s from the Sessions read is unavailable, never a 0 — and the scope label still describes the tile',
    async (_status, error) => {
      authState.can = SESSIONS_ONLY
      live.mockRejectedValue(error)

      renderIntel(<HomeView />)

      expect(await screen.findByText(/Couldn't load/i)).toBeInTheDocument()
      const tile = sessionsTile()
      expect(within(tile).getByText('—')).toBeInTheDocument()
      expect(within(tile).queryByText('0')).toBeNull()
      expect(within(tile).getByText(TENANT_WIDE_LABEL)).toBeInTheDocument()
    },
  )

  it.each([
    ['500', new ApiError(500, 'server_error', 'boom')],
    ['403', new ApiError(403, 'forbidden', 'workspace confined')],
  ])(
    'a %s from the Inventory read is unavailable, never a 0 — and the scope label still describes the tile',
    async (_status, error) => {
      authState.can = INVENTORY_ONLY
      summary.mockRejectedValue(error)

      renderIntel(<HomeView />)

      expect(await screen.findByText(/Couldn't load/i)).toBeInTheDocument()
      const tile = inventoryTile()
      expect(within(tile).getByText('—')).toBeInTheDocument()
      expect(within(tile).queryByText('0')).toBeNull()
      expect(within(tile).getByText(TENANT_WIDE_LABEL)).toBeInTheDocument()
    },
  )

  it('while the Sessions read is pending the tile has no figure and no link, but already states its scope', () => {
    authState.can = SESSIONS_ONLY
    live.mockImplementation(() => new Promise(() => {}))

    renderIntel(<HomeView />)

    expect(screen.getByText('Live sessions')).toBeInTheDocument()
    // No drill-down yet (the page's own "open report" link is not the tile's).
    expect(screen.queryByRole('link', { name: /Live sessions/i })).toBeNull()
    expect(screen.queryByText('3')).toBeNull()
    expect(screen.getByText(TENANT_WIDE_LABEL)).toBeInTheDocument()
  })

  it('while the Inventory read is pending the tile has no figure and no link, but already states its scope', () => {
    authState.can = INVENTORY_ONLY
    summary.mockImplementation(() => new Promise(() => {}))

    renderIntel(<HomeView />)

    expect(screen.getByText('Inventory')).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: /Inventory/i })).toBeNull()
    expect(screen.queryByText('25')).toBeNull()
    expect(screen.getByText(TENANT_WIDE_LABEL)).toBeInTheDocument()
  })
})
