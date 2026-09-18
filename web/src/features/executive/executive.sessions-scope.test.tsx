// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The executive dashboard's USAGE pillar and the topbar workspace selector.
//
// The pillar mixes two reads, and both are tenant-wide: the headline agent count and
// the "tracked" total come from `GET /v1/m/inventory/summary`, which reads no request
// filter and whose catalog carries no workspace lineage (an internal design note (not shipped)
// effective-workspace-scope, ratified 2026-09-08); the "live" figure comes from
// `GET /v1/m/sessions/live`, which takes no core-workspace selector and puts none on
// its DTO (an internal design note (not shipped), ratified the same
// day). The live read stopped sending `workspace_id` first; until this change the
// inventory read still sent it and re-fetched on every selection, presenting the same
// tenant-wide summary as the new workspace's count beside a live figure whose note
// already said it was tenant-wide. These cases run the real container against the
// real workspace store and a real QueryClient; only the API modules and auth are
// doubled.
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
import '@/features/_intel'
import { ApiError } from '@/lib/api/errors'
import type { ListResponse } from '@/lib/api/types'
import type { LiveDTO } from '@/features/sessions/types'
import type { InventorySummary } from '@/features/inventory/types'
import { sessionsApi, type LiveListParams } from '@/features/sessions/api'
import { inventoryApi } from '@/features/inventory/api'
import { useWorkspaceStore } from '@/stores/workspace'
import { inventorySummaryFixture, sessionsLiveFixture } from './fixtures'
import { ExecutiveView } from './executive-view'
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
/** One note for the whole tile, naming the sources the role reads:
 *  "<Sessions noun> · <Inventory item> · <tenant-wide>". */
const BOTH_SCOPE_NOTE = `Sessions · Inventory · ${TENANT_WIDE_LABEL}`
/** With sessions alone, the note is the disclosure accepted on 2026-09-08, unchanged. */
const SESSIONS_SCOPE_NOTE = `Sessions · ${TENANT_WIDE_LABEL}`
const INVENTORY_SCOPE_NOTE = `Inventory · ${TENANT_WIDE_LABEL}`

const BOTH = (p: string) =>
  p === 'sessions:live:read' || p === 'inventory:catalog:read'
const SESSIONS_ONLY = (p: string) => p === 'sessions:live:read'
const INVENTORY_ONLY = (p: string) => p === 'inventory:catalog:read'

/** The tenant-wide page: three active rows → "3 live". */
const TENANT_WIDE_PAGE: ListResponse<LiveDTO> = {
  items: sessionsLiveFixture.items.map((row, i) => ({
    ...row,
    session_ref: `wide-${i}`,
    live_ref: `lr-wide-${i}`,
    cc_state: 'active',
  })),
  has_more: false,
}

/** What a SCOPED sessions endpoint would answer — one row → "1 live". The engine has
 *  no such endpoint; this page exists so a filter the view pretends to apply shows up. */
const PRETENDED_SCOPED_PAGE: ListResponse<LiveDTO> = {
  items: [{ ...sessionsLiveFixture.items[0]!, cc_state: 'active' }],
  has_more: false,
}

/** The tenant-wide summary: 3 active agents, 25 tracked → headline "3", "25 tracked". */
const TENANT_WIDE_SUMMARY: InventorySummary = inventorySummaryFixture

/** What a SCOPED summary would answer — 1 active agent, 7 tracked. The engine has no
 *  such answer; this fixture exists so a selector the view still sent shows up. */
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
 *  still answers the pretended summary to ANY argument, so a regression that smuggles
 *  a selector back in is a wrong figure on the tile, not only a wrong call. */
function summaryDouble(...args: unknown[]) {
  return Promise.resolve(
    args.length > 0 ? PRETENDED_SCOPED_SUMMARY : TENANT_WIDE_SUMMARY,
  )
}

/** The usage pillar tile — the one whose caption carries the live figure. */
function usageTile() {
  return screen.getByText('Active agents').closest('a')!
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
  authState.activeTenant = 'demo'
  // Only the usage pillar's two reads are permitted, so the headline block settles on
  // exactly these two queries and no other pillar competes for the grid.
  authState.can = BOTH
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

async function settledOnBoth() {
  await waitFor(() =>
    expect(
      within(usageTile()).getByText('3 live · 25 tracked'),
    ).toBeInTheDocument(),
  )
}

describe('ExecutiveView — the usage pillar pretends the workspace filter for neither Sessions nor Inventory', () => {
  it('reads both tenant-wide pages with no selector at all — W1 selected, "3 live · 25 tracked", never the pretended "1 live" or "7 tracked"', async () => {
    useWorkspaceStore.getState().setActiveWorkspace('ws-a', 'Workspace A')

    renderIntel(<ExecutiveView />)
    await settledOnBoth()

    expect(live).toHaveBeenCalledTimes(1)
    expect(live.mock.calls[0]?.[0] ?? {}).not.toHaveProperty('workspace_id')
    expect(within(usageTile()).queryByText(/^1 live/)).toBeNull()
    expect(summary).toHaveBeenCalledTimes(1)
    expect(summary.mock.calls[0]).toEqual([])
    expect(within(usageTile()).queryByText(/7 tracked$/)).toBeNull()
    // The headline agent count is the tenant-wide "3", not the pretended "1".
    expect(within(usageTile()).getByText('3')).toBeInTheDocument()
  })

  it('W1→W2, then all workspaces: no second Inventory request, no second Sessions request, same keys, same figures', async () => {
    useWorkspaceStore.getState().setActiveWorkspace('ws-a', 'Workspace A')
    const qc = createTestQueryClient()

    renderIntel(<ExecutiveView />, { queryClient: qc })
    await settledOnBoth()
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
    expect(
      within(usageTile()).getByText('3 live · 25 tracked'),
    ).toBeInTheDocument()

    act(() => {
      useWorkspaceStore.getState().setActiveWorkspace(null)
    })
    await act(async () => {
      await new Promise((r) => setTimeout(r, 30))
    })
    expect(summary).toHaveBeenCalledTimes(1)
    expect(live).toHaveBeenCalledTimes(1)
    expect(inventoryCacheKeys(qc)).toEqual(keysBefore)
    expect(
      within(usageTile()).getByText('3 live · 25 tracked'),
    ).toBeInTheDocument()
  })

  it('names both sources in one scope note, once, inside the usage tile, as accessible text', async () => {
    useWorkspaceStore.getState().setActiveWorkspace('ws-a', 'Workspace A')

    renderIntel(<ExecutiveView />)
    await settledOnBoth()

    const note = screen.getByTestId('executive-usage-scope-note')
    expect(note).toHaveTextContent(BOTH_SCOPE_NOTE)
    expect(usageTile()).toContainElement(note)
    // One disclosure on the page — the report header and the other pillars carry none.
    expect(
      screen.getAllByText(TENANT_WIDE_LABEL, { exact: false }),
    ).toHaveLength(1)
    // It travels with the tile's accessible name.
    expect(
      screen.getByRole('link', { name: /Sessions · Inventory · Tenant-wide/i }),
    ).toBe(usageTile())
  })

  it('the note names only the sources the role reads: with inventory alone it names Inventory, not Sessions, and there is no Sessions read', async () => {
    useWorkspaceStore.getState().setActiveWorkspace('ws-a', 'Workspace A')
    authState.can = INVENTORY_ONLY

    renderIntel(<ExecutiveView />)
    await waitFor(() => expect(summary).toHaveBeenCalledTimes(1))
    await waitFor(() =>
      expect(screen.getByText('Active agents')).toBeInTheDocument(),
    )

    expect(live).not.toHaveBeenCalled()
    const note = screen.getByTestId('executive-usage-scope-note')
    expect(note).toHaveTextContent(INVENTORY_SCOPE_NOTE)
    expect(note).not.toHaveTextContent('Sessions')
    expect(usageTile()).toContainElement(note)
  })

  it('the note names only the sources the role reads: with sessions alone it is the accepted Sessions disclosure, unchanged, and there is no Inventory read', async () => {
    useWorkspaceStore.getState().setActiveWorkspace('ws-a', 'Workspace A')
    authState.can = SESSIONS_ONLY

    renderIntel(<ExecutiveView />)
    await waitFor(() => expect(live).toHaveBeenCalledTimes(1))
    await waitFor(() =>
      expect(screen.getByText('Active agents')).toBeInTheDocument(),
    )

    expect(summary).not.toHaveBeenCalled()
    const note = screen.getByTestId('executive-usage-scope-note')
    expect(note).toHaveTextContent(SESSIONS_SCOPE_NOTE)
    expect(note).not.toHaveTextContent('Inventory')
    expect(usageTile()).toContainElement(note)
  })

  it('T1→T2: a real tenant change re-reads both under the new tenant, into a cache keyed by that tenant', async () => {
    const qc = createTestQueryClient()

    const { rerender } = renderIntel(<ExecutiveView />, { queryClient: qc })
    await waitFor(() => expect(live).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(summary).toHaveBeenCalledTimes(1))

    authState.activeTenant = 'other-tenant'
    rerender(<ExecutiveView />)

    await waitFor(() => expect(live).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(summary).toHaveBeenCalledTimes(2))
    expect(live.mock.calls[1]?.[0] ?? {}).not.toHaveProperty('workspace_id')
    expect(summary.mock.calls[1]).toEqual([])
    expect(inventoryCacheKeys(qc)).toContain(
      JSON.stringify(['inventory', 'other-tenant', 'summary']),
    )
  })

  /**
   * Until 2026-09-08 this case asserted that a double failure REMOVED the pillar, the
   * same way a missing permission does. Root's follow-up (dashboard-usage-availability)
   * separates the two: an outage is represented, not hidden. The scope note stays —
   * it describes what the reads cover, and the reads are still the role's to make.
   * The full availability matrix lives in executive.usage-availability.test.tsx.
   */
  it('when both usage reads fail the pillar stays with "—" figures, never a fabricated 0, and the scope note', async () => {
    live.mockRejectedValue(new ApiError(500, 'server_error', 'boom'))
    summary.mockRejectedValue(new ApiError(500, 'server_error', 'boom'))

    renderIntel(<ExecutiveView />)
    await waitFor(() => expect(live).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(summary).toHaveBeenCalledTimes(1))

    await waitFor(() =>
      expect(screen.getByText('Active agents')).toBeInTheDocument(),
    )
    expect(
      within(usageTile()).getByText('— live · — tracked'),
    ).toBeInTheDocument()
    expect(within(usageTile()).queryByText(/\b0\b/)).toBeNull()
    expect(screen.getByTestId('executive-usage-scope-note')).toHaveTextContent(
      BOTH_SCOPE_NOTE,
    )
  })
})
