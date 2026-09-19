// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The front door's INVENTORY and LIVE SESSIONS tiles when one source is not
// established. Both tiles read the same `deriveUsage` rollup as the executive pillar,
// each printing only its own half, and each already answers to its own query state
// (loading skeleton, "—" + retry hint on error, not mounted without the permission).
// What these cases pin is the model underneath: a half the view did not have comes
// back `null` and prints "—", never a fabricated 0 that a later tile change could
// surface; a valid half keeps its number beside a failed one; a successful empty
// answer is a real 0; and leaving a successful state — failure, permission withdrawn,
// tenant change — never repaints the retired figure nor a 0.
//
// Since 2026-09-08 (the residual cut of dashboard-coverage-and-pending-states) they
// also pin the two states that used to fall through: a permitted source that is
// `pending` but NOT fetching — paused, or not started — names that reason beside its
// "—" instead of a caption of dashes, and a screen-reader-only live region per view
// announces availability and coverage changes of the two usage sources without
// carrying a number. The pending states are exercised two ways, on purpose: through
// the REAL QueryClient (`onlineManager` pause/resume, `cancelQueries` to idle) and
// through a PURE FLAG FIXTURE that only rewrites the flags `useQuery` returns — the
// first proves the transitions, the second that the view reads the flags and nothing
// else (not `navigator.onLine`, not the error). A DOM assertion on `aria-live` pins
// the text and when it changes; it does not certify any particular screen reader.
import type { ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { onlineManager } from '@tanstack/react-query'
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
import type { InventorySummary } from '@/features/inventory/types'
import type { LiveDTO } from '@/features/sessions/types'
import { sessionsApi, sessionsKeys } from '@/features/sessions/api'
import { inventoryApi, inventoryKeys } from '@/features/inventory/api'
import { useWorkspaceStore } from '@/stores/workspace'
import {
  inventorySummaryFixture,
  sessionsLiveFixture,
} from '@/features/executive/fixtures'
import { HomeView } from './home-view'
import './i18n'

vi.mock('@tanstack/react-router', () => ({
  useRouterState: () => '',
  // Home mounts `WorkComposer`, which navigates to the started run.
  useNavigate: () => () => {},
  Link: ({ children, to }: { children: ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
}))

const authState = vi.hoisted(() => ({
  can: (_p: string): boolean => true,
  activeTenant: 'demo' as string | null,
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))

/** THE PURE FLAG FIXTURE. `useQuery` stays the real hook against the real client; for
 *  a query whose key starts with a registered segment (`'sessions'`, `'inventory'`)
 *  the RESULT it returns is overlaid with these flags and nothing else happens: no
 *  pause, no cancel, no network state. Empty by default, so every other case in this
 *  file runs the untouched hook. */
const flagFixture = vi.hoisted(() => ({
  overrides: new Map<string, Record<string, unknown>>(),
}))
vi.mock('@tanstack/react-query', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@tanstack/react-query')>()
  return {
    ...actual,
    useQuery: (...args: Parameters<typeof actual.useQuery>) => {
      const result = actual.useQuery(...args)
      const override = flagFixture.overrides.get(String(args[0].queryKey[0]))
      return override ? { ...result, ...override } : result
    },
  }
})
/** `status: 'pending'` with `fetchStatus: 'idle'` — no answer, nothing in flight. */
const PENDING_IDLE_FLAGS = {
  status: 'pending',
  fetchStatus: 'idle',
  isPending: true,
  isLoading: false,
  isInitialLoading: false,
  isFetching: false,
  isPaused: false,
  isSuccess: false,
  isError: false,
  data: undefined,
  error: null,
}
/** `status: 'pending'` with `fetchStatus: 'paused'` — dispatched, waiting. */
const PENDING_PAUSED_FLAGS = {
  ...PENDING_IDLE_FLAGS,
  fetchStatus: 'paused',
  isPaused: true,
}

const BOTH = (p: string) =>
  p === 'sessions:live:read' || p === 'inventory:catalog:read'

/** Three active rows → the sessions tile reads "3". */
const LIVE_PAGE: ListResponse<LiveDTO> = {
  items: sessionsLiveFixture.items.map((row, i) => ({
    ...row,
    session_ref: `row-${i}`,
    live_ref: `lr-row-${i}`,
    cc_state: 'active',
  })),
  has_more: false,
}
const EMPTY_PAGE: ListResponse<LiveDTO> = { items: [], has_more: false }
const EMPTY_INVENTORY: InventorySummary = {
  by_kind: {},
  by_source: {},
  total: 0,
}

function deferred<T>() {
  let resolve!: (v: T) => void
  const promise = new Promise<T>((res) => {
    resolve = res
  })
  return { promise, resolve }
}

/** A tile by its label; `closest('a')` is null while it is loading (no drill-down). */
function tile(label: string) {
  const el = screen.getByText(label)
  return el.closest('a') ?? el.parentElement!.parentElement!.parentElement!
}

let live: ReturnType<typeof vi.spyOn>
let summary: ReturnType<typeof vi.spyOn>

beforeEach(() => {
  authState.activeTenant = 'demo'
  authState.can = BOTH
  useWorkspaceStore.setState({
    activeWorkspace: null,
    activeWorkspaceName: null,
  })
  live = vi.spyOn(sessionsApi, 'live').mockResolvedValue(LIVE_PAGE)
  summary = vi
    .spyOn(inventoryApi, 'summary')
    .mockResolvedValue(inventorySummaryFixture)
})

afterEach(() => {
  vi.restoreAllMocks()
  flagFixture.overrides.clear()
  // A case that took the client offline must not leave the next one paused.
  onlineManager.setOnline(true)
})

/** The screen-reader-only availability region of the view: one per page, never inside
 *  a link or a disclosure, no control inside it, dropped from print. */
function liveRegion() {
  const region = screen.getByTestId('home-usage-availability-live')
  expect(region).toHaveAttribute('role', 'status')
  expect(region).toHaveAttribute('aria-live', 'polite')
  expect(region.closest('a, details, summary, button')).toBeNull()
  expect(
    region.querySelector('a, button, details, summary, input, [tabindex]'),
  ).toBeNull()
  expect(region.className).toContain('sr-only')
  expect(region.className).toContain('print:hidden')
  return region
}

/** Records every DOM change under the live region until disconnected. */
function observeRegion(region: HTMLElement) {
  const records: MutationRecord[] = []
  const observer = new MutationObserver((list) => records.push(...list))
  observer.observe(region, {
    childList: true,
    characterData: true,
    subtree: true,
  })
  return {
    mutations: () => {
      records.push(...observer.takeRecords())
      return records.length
    },
    disconnect: () => observer.disconnect(),
  }
}

describe('HomeView usage tiles — one source not established', () => {
  it('Sessions 500 beside a valid Inventory: "25" stays, the sessions tile is "—" with the retry hint, never 0', async () => {
    live.mockRejectedValue(new ApiError(500, 'server_error', 'boom'))

    renderIntel(<HomeView />)
    await waitFor(() =>
      expect(within(tile('Inventory')).getByText('25')).toBeInTheDocument(),
    )
    await waitFor(() =>
      expect(within(tile('Live sessions')).getByText('—')).toBeInTheDocument(),
    )

    const sessions = tile('Live sessions')
    expect(within(sessions).getByText(/Couldn't load/i)).toBeInTheDocument()
    expect(within(sessions).queryByText('0')).toBeNull()
    expect(within(sessions).queryByText(/0 idle/)).toBeNull()
    // The valid half keeps its own caption too.
    expect(
      within(tile('Inventory')).getByText('4 agents · 3 active'),
    ).toBeInTheDocument()
  })

  it('Inventory 403 beside valid Sessions: "3" stays, the inventory tile is "—", never 0', async () => {
    summary.mockRejectedValue(new ApiError(403, 'forbidden', 'no'))

    renderIntel(<HomeView />)
    await waitFor(() =>
      expect(within(tile('Live sessions')).getByText('3')).toBeInTheDocument(),
    )
    await waitFor(() =>
      expect(within(tile('Inventory')).getByText('—')).toBeInTheDocument(),
    )

    const inventory = tile('Inventory')
    expect(within(inventory).queryByText('0')).toBeNull()
    expect(within(inventory).queryByText(/0 agents/)).toBeNull()
    expect(
      within(tile('Live sessions')).getByText('0 idle now'),
    ).toBeInTheDocument()
  })

  it('Sessions pending beside a valid Inventory: a skeleton, no interim 0', async () => {
    const pending = deferred<ListResponse<LiveDTO>>()
    live.mockImplementation(() => pending.promise)

    renderIntel(<HomeView />)
    await waitFor(() =>
      expect(within(tile('Inventory')).getByText('25')).toBeInTheDocument(),
    )
    expect(screen.queryByRole('link', { name: /Live sessions/i })).toBeNull()
    expect(within(tile('Live sessions')).queryByText('0')).toBeNull()

    pending.resolve(LIVE_PAGE)
    await waitFor(() =>
      expect(within(tile('Live sessions')).getByText('3')).toBeInTheDocument(),
    )
  })

  it('a source the role may not read is not mounted; the other tile keeps its number', async () => {
    authState.can = (p) => p === 'inventory:catalog:read'

    renderIntel(<HomeView />)
    await waitFor(() =>
      expect(within(tile('Inventory')).getByText('25')).toBeInTheDocument(),
    )
    expect(screen.queryByText('Live sessions')).toBeNull()
    expect(live).not.toHaveBeenCalled()
  })

  it('both valid and genuinely empty: real zeros on both tiles', async () => {
    live.mockResolvedValue(EMPTY_PAGE)
    summary.mockResolvedValue(EMPTY_INVENTORY)

    renderIntel(<HomeView />)
    await waitFor(() =>
      expect(within(tile('Inventory')).getByText('0')).toBeInTheDocument(),
    )
    await waitFor(() =>
      expect(within(tile('Live sessions')).getByText('0')).toBeInTheDocument(),
    )
    expect(
      within(tile('Inventory')).getByText('0 agents · 0 active'),
    ).toBeInTheDocument()
    expect(
      within(tile('Live sessions')).getByText('0 idle now'),
    ).toBeInTheDocument()
  })
})

describe('HomeView usage tiles — leaving a successful state', () => {
  it('success → 500 on refetch: the sessions tile becomes "—", not the retired 3 and not 0', async () => {
    const queryClient = createTestQueryClient()
    renderIntel(<HomeView />, { queryClient })
    await waitFor(() =>
      expect(within(tile('Live sessions')).getByText('3')).toBeInTheDocument(),
    )

    live.mockRejectedValue(new ApiError(500, 'server_error', 'boom'))
    await queryClient.refetchQueries({ queryKey: ['sessions'] })

    await waitFor(() =>
      expect(within(tile('Live sessions')).getByText('—')).toBeInTheDocument(),
    )
    const sessions = tile('Live sessions')
    expect(within(sessions).queryByText('3')).toBeNull()
    expect(within(sessions).queryByText('0')).toBeNull()
    // The inventory tile is untouched by its neighbour's failure.
    expect(within(tile('Inventory')).getByText('25')).toBeInTheDocument()
  })

  it('success → permission withdrawn: the sessions tile is unmounted; the cached page is not shown anywhere', async () => {
    const { rerender } = renderIntel(<HomeView />)
    await waitFor(() =>
      expect(within(tile('Live sessions')).getByText('3')).toBeInTheDocument(),
    )

    authState.can = (p) => p === 'inventory:catalog:read'
    rerender(<HomeView />)

    await waitFor(() => expect(screen.queryByText('Live sessions')).toBeNull())
    expect(screen.queryByText('3')).toBeNull()
    expect(within(tile('Inventory')).getByText('25')).toBeInTheDocument()
    expect(live).toHaveBeenCalledTimes(1)
  })

  it('tenant change: the old tenant’s figure is not shown; a pending new read is a skeleton, then its own answer', async () => {
    const { rerender } = renderIntel(<HomeView />)
    await waitFor(() =>
      expect(within(tile('Live sessions')).getByText('3')).toBeInTheDocument(),
    )

    const pending = deferred<ListResponse<LiveDTO>>()
    live.mockImplementation(() => pending.promise)
    authState.activeTenant = 'other-tenant'
    rerender(<HomeView />)

    await waitFor(() => expect(live).toHaveBeenCalledTimes(2))
    await waitFor(() =>
      expect(screen.queryByRole('link', { name: /Live sessions/i })).toBeNull(),
    )
    expect(within(tile('Live sessions')).queryByText('3')).toBeNull()

    pending.resolve(EMPTY_PAGE)
    await waitFor(() =>
      expect(within(tile('Live sessions')).getByText('0')).toBeInTheDocument(),
    )
  })
})

// --- the inventory tile's figures are a FLOOR when the engine truncated them ---
//
// `deriveUsage` has always carried `inventory.truncated`, and the Inventory view has
// always flagged it (inventory-view.tsx:189) — but the front door printed the same
// counts with nothing said, so a bounded scan read as the estate. These cases pin the
// disclosure inside the tile it qualifies: it names the source, it does not hide the
// counts (a floor is a real answer), it never appears on the Sessions tile, and it
// exists only while that truncated summary is the current, permitted, successful read.
const TRUNCATED_INVENTORY: InventorySummary = {
  ...inventorySummaryFixture,
  truncated: true,
}

describe('HomeView inventory tile — a truncated summary', () => {
  it('truncated: the counts stay, the tile says which source is partial, and Sessions is untouched', async () => {
    summary.mockResolvedValue(TRUNCATED_INVENTORY)

    renderIntel(<HomeView />)
    await waitFor(() =>
      expect(within(tile('Inventory')).getByText('25')).toBeInTheDocument(),
    )

    const inventory = tile('Inventory')
    // A floor is still an answer: the caption keeps its figures.
    expect(
      within(inventory).getByText('4 agents · 3 active'),
    ).toBeInTheDocument()
    const partial = within(inventory).getByTestId('home-inventory-partial-note')
    expect(partial).toHaveTextContent('Inventory')
    expect(partial).toHaveTextContent('Partial data')
    // The caption is the compact line: the mechanism in a few words, on screen.
    expect(partial).toHaveTextContent(/scan ceiling reached/i)
    expect(partial).not.toHaveTextContent(/not an exact total/i)
    expect(partial).not.toHaveAttribute('title')
    // The complete sentence is in the disclosure OUTSIDE the tile link, beside it.
    const details = screen.getByTestId('home-inventory-partial-details')
    expect(details.tagName).toBe('DETAILS')
    expect(details.closest('a')).toBeNull()
    expect(details.parentElement).toBe(inventory.parentElement)
    expect(within(details).getByText('Why partial data?')).toBeInTheDocument()
    expect(
      within(details).getByTestId('home-inventory-partial-note-explanation'),
    ).toHaveTextContent(
      'Inventory — Partial data This aggregate reached the scan ceiling — it is partial, not an exact total.',
    )
    expect(within(inventory).queryByRole('button')).toBeNull()
    expect(inventory.querySelector('details, summary, button')).toBeNull()
    // The print-only copy of the same row sits beside the disclosure, outside it and
    // outside the link, fed by the same current answer (print preservation, F1).
    const print = screen.getByTestId('home-inventory-partial-details-print')
    expect(print.parentElement).toBe(inventory.parentElement)
    expect(print.closest('a, details')).toBeNull()
    expect(
      within(print).getByTestId(
        'home-inventory-partial-note-print-explanation',
      ),
    ).toHaveTextContent(
      'Inventory — Partial data This aggregate reached the scan ceiling — it is partial, not an exact total.',
    )
    expect(print).not.toHaveTextContent(/one page/i)
    // The scope disclosure is a different statement and stays exactly as accepted.
    expect(
      within(inventory).getByTestId('home-inventory-scope-note'),
    ).toHaveTextContent('Tenant-wide — not filtered by workspace')
    // Sessions is another source, with its own read: it is never marked partial.
    expect(screen.getAllByTestId('home-inventory-partial-note')).toHaveLength(1)
    expect(screen.queryByTestId('home-sessions-partial-details')).toBeNull()
    expect(
      screen.queryByTestId('home-sessions-partial-details-print'),
    ).toBeNull()
    expect(within(tile('Live sessions')).getByText('3')).toBeInTheDocument()
  })

  it('the flag absent (the DTO makes it optional) and the flag false: figures remain, no partial line', async () => {
    // The fixture carries no `truncated` key at all — the legacy shape.
    const first = renderIntel(<HomeView />)
    await waitFor(() =>
      expect(within(tile('Inventory')).getByText('25')).toBeInTheDocument(),
    )
    expect(screen.queryByTestId('home-inventory-partial-note')).toBeNull()
    expect(screen.queryByTestId('home-inventory-partial-details')).toBeNull()
    expect(
      screen.queryByTestId('home-inventory-partial-details-print'),
    ).toBeNull()
    expect(screen.queryByText('Why partial data?')).toBeNull()
    first.unmount()

    summary.mockResolvedValue({ ...inventorySummaryFixture, truncated: false })
    renderIntel(<HomeView />)
    await waitFor(() =>
      expect(within(tile('Inventory')).getByText('25')).toBeInTheDocument(),
    )
    expect(screen.queryByTestId('home-inventory-partial-note')).toBeNull()
  })

  it('success → 500 on refetch: the tile is "—" and the stale floor marker goes with the figure', async () => {
    const queryClient = createTestQueryClient()
    summary.mockResolvedValue(TRUNCATED_INVENTORY)

    renderIntel(<HomeView />, { queryClient })
    await waitFor(() =>
      expect(
        screen.getByTestId('home-inventory-partial-note'),
      ).toBeInTheDocument(),
    )

    summary.mockRejectedValue(new ApiError(500, 'server_error', 'boom'))
    await queryClient.refetchQueries({ queryKey: ['inventory'] })

    await waitFor(() =>
      expect(within(tile('Inventory')).getByText('—')).toBeInTheDocument(),
    )
    const inventory = tile('Inventory')
    expect(screen.queryByTestId('home-inventory-partial-note')).toBeNull()
    expect(screen.queryByTestId('home-inventory-partial-details')).toBeNull()
    // …and the print copy retires with the same answer: no stale printed row.
    expect(
      screen.queryByTestId('home-inventory-partial-details-print'),
    ).toBeNull()
    expect(screen.queryByText(/not an exact total/i)).toBeNull()
    expect(within(inventory).getByText(/Couldn't load/i)).toBeInTheDocument()
    expect(within(inventory).queryByText('25')).toBeNull()
    // The scope line describes the tile in every state; the floor marker does not.
    expect(
      within(inventory).getByTestId('home-inventory-scope-note'),
    ).toBeInTheDocument()
  })

  it('a role without the inventory read: no tile, no read, no marker anywhere', async () => {
    authState.can = (p) => p === 'sessions:live:read'
    summary.mockResolvedValue(TRUNCATED_INVENTORY)

    renderIntel(<HomeView />)
    await waitFor(() =>
      expect(within(tile('Live sessions')).getByText('3')).toBeInTheDocument(),
    )
    expect(summary).not.toHaveBeenCalled()
    expect(screen.queryByText('Inventory')).toBeNull()
    expect(screen.queryByTestId('home-inventory-partial-note')).toBeNull()
    expect(
      screen.queryByTestId('home-inventory-partial-details-print'),
    ).toBeNull()
    expect(screen.queryByText(/not an exact total/i)).toBeNull()
  })
})

// --- the sessions tile's figures are a FLOOR when the engine returned one page ---
//
// `GET /v1/m/sessions/live` answers with ONE most-recent page: the store reads
// limit+1 rows (default 100, ceiling 1000) and reports `has_more` when a row existed
// beyond it (modules/sessions/api.go handleListLive → page.HasMore). `deriveUsage`
// counts `live.items`, so the active and idle figures on this tile are floors on such
// a page — and until now the tile printed them exactly like a complete population.
// These cases pin the disclosure inside the tile it qualifies: it names SESSIONS
// (Inventory's marker is another source's, and neither borrows the other's), its hint
// is the page sentence and not the scan-ceiling one, it never hides the counts, and it
// exists only while that page is the current, permitted, successful read.
const MORE_PAGE: ListResponse<LiveDTO> = { ...LIVE_PAGE, has_more: true }
/** The contract always carries `has_more`; this is the shape a consumer would see if
 *  it did not — the legacy behaviour must hold, without a completeness claim. */
const FLAGLESS_PAGE = { items: LIVE_PAGE.items } as ListResponse<LiveDTO>

describe('HomeView sessions tile — a page with more rows', () => {
  it('has_more: the counts stay, the tile says Sessions is partial with the page hint, and Inventory is untouched', async () => {
    live.mockResolvedValue(MORE_PAGE)

    renderIntel(<HomeView />)
    await waitFor(() =>
      expect(within(tile('Live sessions')).getByText('3')).toBeInTheDocument(),
    )

    const sessions = tile('Live sessions')
    // A floor is still an answer: the figure and its caption keep their numbers.
    expect(within(sessions).getByText('0 idle now')).toBeInTheDocument()
    const partial = within(sessions).getByTestId('home-sessions-partial-note')
    expect(partial).toHaveTextContent('Sessions')
    expect(partial).toHaveTextContent('Partial data')
    expect(partial).not.toHaveTextContent('Inventory')
    // The mechanism is a bounded PAGE, not an aggregate's scan ceiling: the compact
    // line names it in a few words, and the complete sentence is in the disclosure
    // OUTSIDE the tile link, beside it.
    expect(partial).toHaveTextContent(/one page loaded; there are more/i)
    expect(partial).not.toHaveTextContent(/not the whole set/i)
    expect(partial).not.toHaveTextContent(/scan ceiling/i)
    expect(partial).not.toHaveAttribute('title')
    const details = screen.getByTestId('home-sessions-partial-details')
    expect(details.tagName).toBe('DETAILS')
    expect(details.closest('a')).toBeNull()
    expect(details.parentElement).toBe(sessions.parentElement)
    expect(
      within(details).getByTestId('home-sessions-partial-note-explanation'),
    ).toHaveTextContent(
      'Sessions — Partial data The engine returned one page, not the whole set. The total CANNOT be inferred from the rows loaded, and rows that are not shown may exist.',
    )
    expect(details).not.toHaveTextContent(/scan ceiling/i)
    expect(sessions.querySelector('details, summary, button')).toBeNull()
    // The print-only copy of the same row, beside the disclosure and outside it.
    const print = screen.getByTestId('home-sessions-partial-details-print')
    expect(print.parentElement).toBe(sessions.parentElement)
    expect(print.closest('a, details')).toBeNull()
    expect(
      within(print).getByTestId('home-sessions-partial-note-print-explanation'),
    ).toHaveTextContent(
      'Sessions — Partial data The engine returned one page, not the whole set. The total CANNOT be inferred from the rows loaded, and rows that are not shown may exist.',
    )
    expect(print).not.toHaveTextContent(/scan ceiling/i)
    // The scope disclosure is a different statement and stays exactly as accepted.
    expect(
      within(sessions).getByTestId('home-sessions-scope-note'),
    ).toHaveTextContent('Tenant-wide — not filtered by workspace')
    // Inventory is another source with its own read: it is never marked by this page.
    expect(within(tile('Inventory')).getByText('25')).toBeInTheDocument()
    expect(screen.queryByTestId('home-inventory-partial-note')).toBeNull()
    expect(screen.queryByTestId('home-inventory-partial-details')).toBeNull()
    expect(
      screen.queryByTestId('home-inventory-partial-details-print'),
    ).toBeNull()
    expect(screen.getAllByTestId('home-sessions-partial-note')).toHaveLength(1)
  })

  it('has_more false (the contract) and the flag absent (legacy shape): figures remain, no partial line, no completeness claim', async () => {
    const first = renderIntel(<HomeView />)
    await waitFor(() =>
      expect(within(tile('Live sessions')).getByText('3')).toBeInTheDocument(),
    )
    expect(screen.queryByTestId('home-sessions-partial-note')).toBeNull()
    first.unmount()

    live.mockResolvedValue(FLAGLESS_PAGE)
    renderIntel(<HomeView />)
    await waitFor(() =>
      expect(within(tile('Live sessions')).getByText('3')).toBeInTheDocument(),
    )
    expect(screen.queryByTestId('home-sessions-partial-note')).toBeNull()
    // Nothing on the tile claims the page is complete either: the caption is unchanged.
    expect(
      within(tile('Live sessions')).getByText('0 idle now'),
    ).toBeInTheDocument()
  })

  it('both sources partial at once: each tile names its own source and neither borrows the other’s marker', async () => {
    live.mockResolvedValue(MORE_PAGE)
    summary.mockResolvedValue(TRUNCATED_INVENTORY)

    renderIntel(<HomeView />)
    await waitFor(() =>
      expect(within(tile('Live sessions')).getByText('3')).toBeInTheDocument(),
    )
    await waitFor(() =>
      expect(within(tile('Inventory')).getByText('25')).toBeInTheDocument(),
    )

    const sessionsNote = within(tile('Live sessions')).getByTestId(
      'home-sessions-partial-note',
    )
    const inventoryNote = within(tile('Inventory')).getByTestId(
      'home-inventory-partial-note',
    )
    expect(sessionsNote).toHaveTextContent('Sessions')
    expect(sessionsNote).not.toHaveTextContent('Inventory')
    expect(inventoryNote).toHaveTextContent('Inventory')
    expect(inventoryNote).not.toHaveTextContent('Sessions')
    // Each carries the brief of its own mechanism, on screen.
    expect(sessionsNote).toHaveTextContent(/one page/i)
    expect(inventoryNote).toHaveTextContent(/scan ceiling/i)
    expect(sessionsNote).not.toHaveAttribute('title')
    expect(inventoryNote).not.toHaveAttribute('title')
    // And each tile has its OWN disclosure beside it with its own full sentence.
    const sessionsDetails = screen.getByTestId('home-sessions-partial-details')
    const inventoryDetails = screen.getByTestId(
      'home-inventory-partial-details',
    )
    expect(sessionsDetails.parentElement).toBe(
      tile('Live sessions').parentElement,
    )
    expect(inventoryDetails.parentElement).toBe(tile('Inventory').parentElement)
    expect(sessionsDetails).toHaveTextContent(/not the whole set/i)
    expect(sessionsDetails).not.toHaveTextContent(/scan ceiling/i)
    expect(inventoryDetails).toHaveTextContent(/not an exact total/i)
    expect(inventoryDetails).not.toHaveTextContent(/one page/i)
    // Each tile's print copy carries its own sentence only, beside its own tile.
    const sessionsPrint = screen.getByTestId(
      'home-sessions-partial-details-print',
    )
    const inventoryPrint = screen.getByTestId(
      'home-inventory-partial-details-print',
    )
    expect(sessionsPrint.parentElement).toBe(
      tile('Live sessions').parentElement,
    )
    expect(inventoryPrint.parentElement).toBe(tile('Inventory').parentElement)
    expect(sessionsPrint).toHaveTextContent(/not the whole set/i)
    expect(sessionsPrint).not.toHaveTextContent(/scan ceiling/i)
    expect(inventoryPrint).toHaveTextContent(/not an exact total/i)
    expect(inventoryPrint).not.toHaveTextContent(/one page/i)
    expect(
      within(tile('Live sessions')).queryByTestId(
        'home-inventory-partial-note',
      ),
    ).toBeNull()
    expect(
      within(tile('Inventory')).queryByTestId('home-sessions-partial-note'),
    ).toBeNull()
  })

  it('success → 500 on refetch: the tile is "—" and the stale page marker goes with the figure', async () => {
    const queryClient = createTestQueryClient()
    live.mockResolvedValue(MORE_PAGE)

    renderIntel(<HomeView />, { queryClient })
    await waitFor(() =>
      expect(
        screen.getByTestId('home-sessions-partial-note'),
      ).toBeInTheDocument(),
    )

    live.mockRejectedValue(new ApiError(500, 'server_error', 'boom'))
    await queryClient.refetchQueries({ queryKey: ['sessions'] })

    await waitFor(() =>
      expect(within(tile('Live sessions')).getByText('—')).toBeInTheDocument(),
    )
    const sessions = tile('Live sessions')
    expect(screen.queryByTestId('home-sessions-partial-note')).toBeNull()
    expect(screen.queryByTestId('home-sessions-partial-details')).toBeNull()
    expect(
      screen.queryByTestId('home-sessions-partial-details-print'),
    ).toBeNull()
    expect(screen.queryByText(/not the whole set/i)).toBeNull()
    expect(within(sessions).getByText(/Couldn't load/i)).toBeInTheDocument()
    expect(within(sessions).queryByText('3')).toBeNull()
    // The scope line describes the tile in every state; the page marker does not.
    expect(
      within(sessions).getByTestId('home-sessions-scope-note'),
    ).toBeInTheDocument()
    // The neighbour is untouched by this source's failure.
    expect(within(tile('Inventory')).getByText('25')).toBeInTheDocument()
  })

  it('success → permission withdrawn: the tile is unmounted and the cached page — figure and marker — is not shown anywhere', async () => {
    live.mockResolvedValue(MORE_PAGE)
    const { rerender } = renderIntel(<HomeView />)
    await waitFor(() =>
      expect(
        screen.getByTestId('home-sessions-partial-note'),
      ).toBeInTheDocument(),
    )

    authState.can = (p) => p === 'inventory:catalog:read'
    rerender(<HomeView />)

    await waitFor(() => expect(screen.queryByText('Live sessions')).toBeNull())
    expect(screen.queryByTestId('home-sessions-partial-note')).toBeNull()
    expect(screen.queryByText('3')).toBeNull()
    expect(live).toHaveBeenCalledTimes(1)
    expect(within(tile('Inventory')).getByText('25')).toBeInTheDocument()
  })

  it('tenant change: the old tenant’s page marker is not shown under the new one; a pending read is a skeleton, then the new answer decides', async () => {
    live.mockResolvedValue(MORE_PAGE)
    const { rerender } = renderIntel(<HomeView />)
    await waitFor(() =>
      expect(
        screen.getByTestId('home-sessions-partial-note'),
      ).toBeInTheDocument(),
    )

    const pending = deferred<ListResponse<LiveDTO>>()
    live.mockImplementation(() => pending.promise)
    authState.activeTenant = 'other-tenant'
    rerender(<HomeView />)

    await waitFor(() => expect(live).toHaveBeenCalledTimes(2))
    await waitFor(() =>
      expect(screen.queryByRole('link', { name: /Live sessions/i })).toBeNull(),
    )
    expect(screen.queryByTestId('home-sessions-partial-note')).toBeNull()
    expect(within(tile('Live sessions')).queryByText('3')).toBeNull()

    // The new tenant's own answer is a complete empty page: a real 0, no marker.
    pending.resolve(EMPTY_PAGE)
    await waitFor(() =>
      expect(within(tile('Live sessions')).getByText('0')).toBeInTheDocument(),
    )
    expect(screen.queryByTestId('home-sessions-partial-note')).toBeNull()
  })
})

// --- a permitted source that is pending but NOT fetching --------------------------
//
// `tileState` told error from loading and nothing else, so a query that was `pending`
// with `fetchStatus` idle or paused rendered as `ready`: "—" over a caption of dashes,
// no reason, and a live region that did not exist. These cases pin the two reasons and
// their edges: a pause is not an outage, a refusal or a lost connection; nothing here is
// a 0; the neighbour keeps its figure and its markers; a SUCCESSFUL answer whose refetch
// is paused keeps everything; and the region says which source changed to what, never
// how many.
const INITIAL_ANNOUNCEMENT =
  'Inventory: figures available. Sessions: figures available.'

describe('HomeView usage tiles — pending reasons (pure flag fixture)', () => {
  it('flags alone say idle: "—" with "not started", a link, no skeleton, no retry hint, and Inventory untouched', async () => {
    flagFixture.overrides.set('sessions', PENDING_IDLE_FLAGS)

    renderIntel(<HomeView />)
    await waitFor(() =>
      expect(within(tile('Inventory')).getByText('25')).toBeInTheDocument(),
    )

    const sessions = tile('Live sessions')
    expect(sessions.tagName).toBe('A')
    expect(within(sessions).getByText('—')).toBeInTheDocument()
    expect(
      within(sessions).getByTestId('home-sessions-pending-reason'),
    ).toHaveTextContent('Query not started — open to load')
    expect(within(sessions).queryByText(/Couldn't load/i)).toBeNull()
    expect(within(sessions).queryByText(/paused/i)).toBeNull()
    expect(within(sessions).queryByText(/idle now/)).toBeNull()
    expect(within(sessions).queryByText('0')).toBeNull()
    expect(screen.queryByTestId('home-sessions-partial-note')).toBeNull()
    expect(
      within(sessions).getByTestId('home-sessions-scope-note'),
    ).toHaveTextContent('Tenant-wide — not filtered by workspace')
    expect(
      within(tile('Inventory')).getByText('4 agents · 3 active'),
    ).toBeInTheDocument()
    expect(screen.queryByTestId('home-inventory-pending-reason')).toBeNull()
    expect(liveRegion()).toHaveTextContent(
      'Inventory: figures available. Sessions: query not started.',
    )
  })

  it('flags alone say paused: "—" with "paused", not offline, not denied, not failed; Sessions untouched', async () => {
    flagFixture.overrides.set('inventory', PENDING_PAUSED_FLAGS)

    renderIntel(<HomeView />)
    await waitFor(() =>
      expect(within(tile('Live sessions')).getByText('3')).toBeInTheDocument(),
    )

    const inventory = tile('Inventory')
    expect(within(inventory).getByText('—')).toBeInTheDocument()
    expect(
      within(inventory).getByTestId('home-inventory-pending-reason'),
    ).toHaveTextContent('Query paused — waiting to resume')
    expect(within(inventory).queryByText(/Couldn't load/i)).toBeNull()
    expect(within(inventory).queryByText(/offline/i)).toBeNull()
    expect(within(inventory).queryByText(/restricted/i)).toBeNull()
    expect(within(inventory).queryByText(/agents/)).toBeNull()
    expect(within(inventory).queryByText('0')).toBeNull()
    expect(screen.queryByTestId('home-inventory-partial-note')).toBeNull()
    expect(
      within(tile('Live sessions')).getByText('0 idle now'),
    ).toBeInTheDocument()
    expect(liveRegion()).toHaveTextContent(
      'Inventory: query paused, waiting to resume. Sessions: figures available.',
    )
  })
})

describe('HomeView usage tiles — a real pause and resume (QueryClient onlineManager)', () => {
  it('offline at mount: Sessions is paused with its reason and no read; the accepted Inventory answer and its marker survive their paused refetch; online resumes both', async () => {
    const queryClient = createTestQueryClient()
    // Inventory already has an accepted, truncated answer; with staleTime 0 the mount
    // refetches it — and offline, that refetch is PAUSED, not lost.
    queryClient.setQueryData(inventoryKeys.summary('demo'), TRUNCATED_INVENTORY)
    summary.mockResolvedValue(TRUNCATED_INVENTORY)
    onlineManager.setOnline(false)

    renderIntel(<HomeView />, { queryClient })
    await waitFor(() =>
      expect(
        screen.getByTestId('home-sessions-pending-reason'),
      ).toBeInTheDocument(),
    )

    // Real query-core state, not a fixture: dispatched and paused, the queryFn unrun.
    expect(
      queryClient.getQueryState(sessionsKeys.live('demo'))?.fetchStatus,
    ).toBe('paused')
    expect(queryClient.getQueryState(sessionsKeys.live('demo'))?.status).toBe(
      'pending',
    )
    expect(live).not.toHaveBeenCalled()
    const sessions = tile('Live sessions')
    expect(within(sessions).getByText('—')).toBeInTheDocument()
    expect(
      within(sessions).getByTestId('home-sessions-pending-reason'),
    ).toHaveTextContent('Query paused — waiting to resume')
    expect(within(sessions).queryByText(/Couldn't load/i)).toBeNull()
    expect(within(sessions).queryByText('0')).toBeNull()
    expect(screen.queryByTestId('home-sessions-partial-note')).toBeNull()

    // The neighbour: success + paused refetch = the figure AND its floor marker stay.
    const inventoryState = queryClient.getQueryState(
      inventoryKeys.summary('demo'),
    )
    expect(inventoryState?.status).toBe('success')
    expect(inventoryState?.fetchStatus).toBe('paused')
    expect(summary).not.toHaveBeenCalled()
    expect(within(tile('Inventory')).getByText('25')).toBeInTheDocument()
    expect(
      within(tile('Inventory')).getByText('4 agents · 3 active'),
    ).toBeInTheDocument()
    expect(screen.getByTestId('home-inventory-partial-note')).toHaveTextContent(
      /scan ceiling reached/i,
    )
    expect(screen.queryByTestId('home-inventory-pending-reason')).toBeNull()
    expect(liveRegion()).toHaveTextContent(
      'Inventory: figures available, partial data. Sessions: query paused, waiting to resume.',
    )

    // Back online: query-core resumes the paused dispatch; the page arrives.
    act(() => onlineManager.setOnline(true))
    await waitFor(() =>
      expect(within(tile('Live sessions')).getByText('3')).toBeInTheDocument(),
    )
    expect(live).toHaveBeenCalledTimes(1)
    expect(summary).toHaveBeenCalledTimes(1)
    expect(screen.queryByTestId('home-sessions-pending-reason')).toBeNull()
    expect(
      within(tile('Live sessions')).getByText('0 idle now'),
    ).toBeInTheDocument()
    expect(
      screen.getByTestId('home-inventory-partial-note'),
    ).toBeInTheDocument()
    expect(liveRegion()).toHaveTextContent(
      'Inventory: figures available, partial data. Sessions: figures available.',
    )
  })

  it('paused → resumed into an EMPTY page: a real 0, not "—" and not a leftover reason', async () => {
    const queryClient = createTestQueryClient()
    queryClient.setQueryData(
      inventoryKeys.summary('demo'),
      inventorySummaryFixture,
    )
    live.mockResolvedValue(EMPTY_PAGE)
    onlineManager.setOnline(false)

    renderIntel(<HomeView />, { queryClient })
    await waitFor(() =>
      expect(
        screen.getByTestId('home-sessions-pending-reason'),
      ).toBeInTheDocument(),
    )
    expect(within(tile('Live sessions')).queryByText('0')).toBeNull()

    act(() => onlineManager.setOnline(true))
    await waitFor(() =>
      expect(within(tile('Live sessions')).getByText('0')).toBeInTheDocument(),
    )
    expect(screen.queryByTestId('home-sessions-pending-reason')).toBeNull()
    expect(
      within(tile('Live sessions')).getByText('0 idle now'),
    ).toBeInTheDocument()
    expect(screen.queryByTestId('home-sessions-partial-note')).toBeNull()
    expect(liveRegion()).toHaveTextContent(INITIAL_ANNOUNCEMENT)
  })

  it('paused → resumed into a page with MORE rows: the counts, the Sessions marker, and a "partial" announcement', async () => {
    const queryClient = createTestQueryClient()
    queryClient.setQueryData(
      inventoryKeys.summary('demo'),
      inventorySummaryFixture,
    )
    live.mockResolvedValue(MORE_PAGE)
    onlineManager.setOnline(false)

    renderIntel(<HomeView />, { queryClient })
    await waitFor(() =>
      expect(
        screen.getByTestId('home-sessions-pending-reason'),
      ).toBeInTheDocument(),
    )
    // While paused nothing is known: no marker for a figure that is not there.
    expect(screen.queryByTestId('home-sessions-partial-note')).toBeNull()

    act(() => onlineManager.setOnline(true))
    await waitFor(() =>
      expect(within(tile('Live sessions')).getByText('3')).toBeInTheDocument(),
    )
    expect(screen.getByTestId('home-sessions-partial-note')).toHaveTextContent(
      /one page loaded; there are more/i,
    )
    expect(screen.queryByTestId('home-sessions-pending-reason')).toBeNull()
    expect(liveRegion()).toHaveTextContent(
      'Inventory: figures available. Sessions: figures available, partial data.',
    )
  })

  it('success → refetch paused: the accepted page, its marker and its caption stay; no reason, no announcement change; online completes the refetch', async () => {
    const queryClient = createTestQueryClient()
    live.mockResolvedValue(MORE_PAGE)
    renderIntel(<HomeView />, { queryClient })
    await waitFor(() =>
      expect(within(tile('Live sessions')).getByText('3')).toBeInTheDocument(),
    )
    const region = liveRegion()
    expect(region).toHaveTextContent(
      'Inventory: figures available. Sessions: figures available, partial data.',
    )
    const watched = observeRegion(region)

    onlineManager.setOnline(false)
    // Not awaited on purpose: a paused refetch resolves only when it resumes.
    void queryClient.refetchQueries({ queryKey: sessionsKeys.live('demo') })
    await waitFor(() =>
      expect(
        queryClient.getQueryState(sessionsKeys.live('demo'))?.fetchStatus,
      ).toBe('paused'),
    )
    expect(queryClient.getQueryState(sessionsKeys.live('demo'))?.status).toBe(
      'success',
    )

    const sessions = tile('Live sessions')
    expect(within(sessions).getByText('3')).toBeInTheDocument()
    expect(within(sessions).getByText('0 idle now')).toBeInTheDocument()
    expect(screen.getByTestId('home-sessions-partial-note')).toBeInTheDocument()
    expect(screen.queryByTestId('home-sessions-pending-reason')).toBeNull()
    expect(within(sessions).queryByText('—')).toBeNull()
    expect(watched.mutations()).toBe(0)

    act(() => onlineManager.setOnline(true))
    await waitFor(() => expect(live).toHaveBeenCalledTimes(2))
    await waitFor(() =>
      expect(
        queryClient.getQueryState(sessionsKeys.live('demo'))?.fetchStatus,
      ).toBe('idle'),
    )
    expect(within(tile('Live sessions')).getByText('3')).toBeInTheDocument()
    expect(watched.mutations()).toBe(0)
    watched.disconnect()
  })
})

describe('HomeView usage tiles — a real idle (QueryClient cancel)', () => {
  it('a fetching read cancelled and reverted is "not started": "—" with the reason and a link, Inventory intact; a refetch starts it', async () => {
    const queryClient = createTestQueryClient()
    const pending = deferred<ListResponse<LiveDTO>>()
    live.mockImplementation(() => pending.promise)

    renderIntel(<HomeView />, { queryClient })
    await waitFor(() =>
      expect(within(tile('Inventory')).getByText('25')).toBeInTheDocument(),
    )
    // Fetching: the skeleton, as before — no link, no reason.
    expect(screen.queryByRole('link', { name: /Live sessions/i })).toBeNull()
    expect(screen.queryByTestId('home-sessions-pending-reason')).toBeNull()
    expect(liveRegion()).toHaveTextContent(
      'Inventory: figures available. Sessions: loading.',
    )

    await queryClient.cancelQueries({ queryKey: sessionsKeys.live('demo') })
    await waitFor(() =>
      expect(
        screen.getByTestId('home-sessions-pending-reason'),
      ).toHaveTextContent('Query not started — open to load'),
    )
    const state = queryClient.getQueryState(sessionsKeys.live('demo'))
    expect(state?.status).toBe('pending')
    expect(state?.fetchStatus).toBe('idle')
    const sessions = tile('Live sessions')
    expect(sessions.tagName).toBe('A')
    expect(within(sessions).getByText('—')).toBeInTheDocument()
    expect(within(sessions).queryByText(/Couldn't load/i)).toBeNull()
    expect(within(sessions).queryByText('0')).toBeNull()
    expect(within(tile('Inventory')).getByText('25')).toBeInTheDocument()
    expect(liveRegion()).toHaveTextContent(
      'Inventory: figures available. Sessions: query not started.',
    )

    live.mockResolvedValue(LIVE_PAGE)
    await queryClient.refetchQueries({ queryKey: sessionsKeys.live('demo') })
    await waitFor(() =>
      expect(within(tile('Live sessions')).getByText('3')).toBeInTheDocument(),
    )
    expect(screen.queryByTestId('home-sessions-pending-reason')).toBeNull()
    expect(liveRegion()).toHaveTextContent(INITIAL_ANNOUNCEMENT)
  })
})

describe('HomeView availability region — failure, recovery, coverage, polling', () => {
  it('announces failure, recovery and a coverage change once each; stays silent on polls that only change numbers; moves no focus', async () => {
    const queryClient = createTestQueryClient()
    renderIntel(<HomeView />, { queryClient })
    await waitFor(() =>
      expect(within(tile('Live sessions')).getByText('3')).toBeInTheDocument(),
    )
    const region = liveRegion()
    expect(region).toHaveTextContent(INITIAL_ANNOUNCEMENT)
    expect(region).not.toHaveTextContent(/\d/)
    expect(screen.getAllByRole('status')).toHaveLength(1)

    // Focus rests on a control outside the tiles; nothing below may move it.
    const report = screen.getByRole('link', { name: /Executive report/i })
    report.focus()
    expect(document.activeElement).toBe(report)
    const watched = observeRegion(region)

    // Two polls with the same state: one returns the same page, the other a page
    // with a DIFFERENT count. The tile follows the number; the region does not.
    await queryClient.refetchQueries({ queryKey: sessionsKeys.live('demo') })
    const fourActive: ListResponse<LiveDTO> = {
      ...LIVE_PAGE,
      items: [
        ...LIVE_PAGE.items,
        { ...LIVE_PAGE.items[0]!, session_ref: 'row-3', live_ref: 'lr-row-3' },
      ],
    }
    live.mockResolvedValue(fourActive)
    await queryClient.refetchQueries({ queryKey: sessionsKeys.live('demo') })
    await waitFor(() =>
      expect(within(tile('Live sessions')).getByText('4')).toBeInTheDocument(),
    )
    expect(live).toHaveBeenCalledTimes(3)
    expect(watched.mutations()).toBe(0)
    expect(region).toHaveTextContent(INITIAL_ANNOUNCEMENT)

    // Failure: the visible hint explains the "—"; the region says which source failed.
    live.mockRejectedValue(new ApiError(500, 'server_error', 'boom'))
    await queryClient.refetchQueries({ queryKey: sessionsKeys.live('demo') })
    await waitFor(() =>
      expect(within(tile('Live sessions')).getByText('—')).toBeInTheDocument(),
    )
    expect(
      within(tile('Live sessions')).getByText(/Couldn't load/i),
    ).toBeInTheDocument()
    expect(region).toHaveTextContent(
      'Inventory: figures available. Sessions: could not be loaded.',
    )
    expect(region).not.toHaveTextContent('4')
    const afterFailure = watched.mutations()
    expect(afterFailure).toBeGreaterThan(0)

    // An identical failed poll: same text, no mutation.
    await queryClient.refetchQueries({ queryKey: sessionsKeys.live('demo') })
    expect(watched.mutations()).toBe(afterFailure)

    // Recovery into a page with more rows: the figure returns WITH its marker, and the
    // region says "partial" — a coverage change is a state change.
    live.mockResolvedValue(MORE_PAGE)
    await queryClient.refetchQueries({ queryKey: sessionsKeys.live('demo') })
    await waitFor(() =>
      expect(within(tile('Live sessions')).getByText('3')).toBeInTheDocument(),
    )
    expect(screen.getByTestId('home-sessions-partial-note')).toBeInTheDocument()
    expect(region).toHaveTextContent(
      'Inventory: figures available. Sessions: figures available, partial data.',
    )

    // Coverage back to a complete page: the marker leaves and the region says so.
    live.mockResolvedValue(LIVE_PAGE)
    await queryClient.refetchQueries({ queryKey: sessionsKeys.live('demo') })
    await waitFor(() =>
      expect(screen.queryByTestId('home-sessions-partial-note')).toBeNull(),
    )
    expect(region).toHaveTextContent(INITIAL_ANNOUNCEMENT)

    expect(document.activeElement).toBe(report)
    expect(screen.getAllByRole('status')).toHaveLength(1)
    watched.disconnect()
  })

  it('a role that reads neither usage source gets an empty region, and the other tiles are unaffected', async () => {
    authState.can = (p) => p === 'health:status:read'
    renderIntel(<HomeView />)
    await waitFor(() =>
      expect(screen.getByText('Health & SLA')).toBeInTheDocument(),
    )
    expect(liveRegion()).toHaveTextContent('')
    expect(live).not.toHaveBeenCalled()
    expect(summary).not.toHaveBeenCalled()
  })
})

describe('HomeView usage tiles — tenant and permission transitions while pending', () => {
  it('paused → Sessions permission withdrawn → online: the tile, its reason and its sentence go; the withdrawn read never goes out; a re-grant starts it afresh', async () => {
    const queryClient = createTestQueryClient()
    queryClient.setQueryData(
      inventoryKeys.summary('demo'),
      inventorySummaryFixture,
    )
    onlineManager.setOnline(false)

    const { rerender } = renderIntel(<HomeView />, { queryClient })
    await waitFor(() =>
      expect(
        screen.getByTestId('home-sessions-pending-reason'),
      ).toBeInTheDocument(),
    )
    expect(live).not.toHaveBeenCalled()
    expect(liveRegion()).toHaveTextContent(
      'Inventory: figures available. Sessions: query paused, waiting to resume.',
    )

    authState.can = (p) => p === 'inventory:catalog:read'
    rerender(<HomeView />)
    await waitFor(() => expect(screen.queryByText('Live sessions')).toBeNull())
    expect(screen.queryByTestId('home-sessions-pending-reason')).toBeNull()
    const region = liveRegion()
    expect(region).toHaveTextContent('Inventory: figures available.')
    expect(region).not.toHaveTextContent('Sessions')
    // The paused dispatch was cancelled and reverted, not left waiting for the network.
    await waitFor(() =>
      expect(
        queryClient.getQueryState(sessionsKeys.live('demo'))?.fetchStatus,
      ).toBe('idle'),
    )

    act(() => onlineManager.setOnline(true))
    // The permitted half's paused refetch resumes; the withdrawn half's does not exist.
    await waitFor(() => expect(summary).toHaveBeenCalledTimes(1))
    expect(live).not.toHaveBeenCalled()
    expect(within(tile('Inventory')).getByText('25')).toBeInTheDocument()
    expect(screen.queryByText('Live sessions')).toBeNull()

    authState.can = BOTH
    rerender(<HomeView />)
    await waitFor(() =>
      expect(within(tile('Live sessions')).getByText('3')).toBeInTheDocument(),
    )
    expect(live).toHaveBeenCalledTimes(1)
    expect(liveRegion()).toHaveTextContent(INITIAL_ANNOUNCEMENT)
  })

  it('tenant change while paused: nothing of the previous tenant remains — no figure, no marker, no sentence — and only the new tenant is read when online, with real zeros', async () => {
    const queryClient = createTestQueryClient()
    queryClient.setQueryData(inventoryKeys.summary('demo'), TRUNCATED_INVENTORY)
    onlineManager.setOnline(false)

    const { rerender } = renderIntel(<HomeView />, { queryClient })
    await waitFor(() =>
      expect(
        screen.getByTestId('home-inventory-partial-note'),
      ).toBeInTheDocument(),
    )
    expect(
      screen.getByTestId('home-sessions-pending-reason'),
    ).toHaveTextContent('Query paused — waiting to resume')

    authState.activeTenant = 'other-tenant'
    rerender(<HomeView />)
    await waitFor(() =>
      expect(
        screen.getByTestId('home-inventory-pending-reason'),
      ).toHaveTextContent('Query paused — waiting to resume'),
    )
    expect(within(tile('Inventory')).getByText('—')).toBeInTheDocument()
    expect(screen.queryByText('25')).toBeNull()
    expect(screen.queryByText('4 agents · 3 active')).toBeNull()
    expect(screen.queryByTestId('home-inventory-partial-note')).toBeNull()
    expect(
      screen.getByTestId('home-sessions-pending-reason'),
    ).toHaveTextContent('Query paused — waiting to resume')
    expect(liveRegion()).toHaveTextContent(
      'Inventory: query paused, waiting to resume. Sessions: query paused, waiting to resume.',
    )
    expect(live).not.toHaveBeenCalled()
    expect(summary).not.toHaveBeenCalled()

    live.mockResolvedValue(EMPTY_PAGE)
    summary.mockResolvedValue(EMPTY_INVENTORY)
    act(() => onlineManager.setOnline(true))
    await waitFor(() =>
      expect(within(tile('Live sessions')).getByText('0')).toBeInTheDocument(),
    )
    await waitFor(() =>
      expect(within(tile('Inventory')).getByText('0')).toBeInTheDocument(),
    )
    // Sessions: ONE read, for the new tenant. The old tenant's dispatch was PENDING
    // and paused, and query-core cancelled and reverted it when its only observer
    // moved to the new key (`Query.removeObserver`).
    expect(live).toHaveBeenCalledTimes(1)
    // Inventory: TWO reads, and the difference is query-core's, measured here rather
    // than hidden. The old tenant's paused fetch was a REFETCH of an already accepted
    // answer (status success), and for that case `removeObserver` only cancels the
    // retry, so the paused refresh completes in the background under the OLD key when
    // the client comes back online. It is a read the role still holds for that tenant,
    // it lands in that tenant's cache entry, and — the point of this case — it is
    // never shown under the new tenant: the screen has the new tenant's real zeros.
    expect(summary).toHaveBeenCalledTimes(2)
    expect(screen.queryByText('25')).toBeNull()
    expect(screen.queryByTestId('home-inventory-partial-note')).toBeNull()
    expect(
      within(tile('Inventory')).getByText('0 agents · 0 active'),
    ).toBeInTheDocument()
    expect(screen.queryByTestId('home-inventory-pending-reason')).toBeNull()
    expect(screen.queryByTestId('home-sessions-pending-reason')).toBeNull()
    expect(liveRegion()).toHaveTextContent(INITIAL_ANNOUNCEMENT)
  })
})
