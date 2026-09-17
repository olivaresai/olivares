// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The executive USAGE pillar when one of its two sources is not established.
//
// The pillar joins the inventory summary (headline agents, "tracked") and the live
// sessions page ("live"). Until 2026-09-08 a half the view did not have was folded into
// the other by `deriveUsage`: a denied, pending or failed live read printed "0 live"
// beside a valid inventory, and the inverse printed "0" agents — the same figure as an
// empty estate. Root confirmed the defect after the tenant-wide correction landed.
//
// These cases run the real container against a real QueryClient; only the two API
// modules and auth are doubled. What they pin: a valid half keeps its number, a half
// that was not established prints "—" with one line saying which half and why
// (restricted to the role, or could not load), a successful empty answer is a real 0,
// and a transition out of success — failure, refusal, permission withdrawn, tenant
// change — never repaints a 0 nor the figure that was current before it.
//
// Since 2026-09-08 (the residual cut of dashboard-coverage-and-pending-states) they
// also pin the two states that used to fall through `usageGap`: a permitted half that
// is `pending` but NOT fetching — paused, or not started — gets its own line beside
// the "—" (neither the skeleton, which is for a read in flight, nor "couldn't load",
// nor "restricted"), and a screen-reader-only live region announces availability and
// coverage changes of the two halves without carrying a number. The pending states
// are exercised two ways, on purpose: through the REAL QueryClient (`onlineManager`
// pause/resume, `cancelQueries` to idle) and through a PURE FLAG FIXTURE that only
// rewrites the flags `useQuery` returns — the first proves the transitions, the
// second that the view reads the flags and nothing else. A DOM assertion on
// `aria-live` pins the text and when it changes; it does not certify any particular
// screen reader.
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
import '@/features/_intel'
import { ApiError } from '@/lib/api/errors'
import type { ListResponse } from '@/lib/api/types'
import type { InventorySummary } from '@/features/inventory/types'
import type { LiveDTO } from '@/features/sessions/types'
import { sessionsApi, sessionsKeys } from '@/features/sessions/api'
import { inventoryApi, inventoryKeys } from '@/features/inventory/api'
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
const INVENTORY_ONLY = (p: string) => p === 'inventory:catalog:read'
const SESSIONS_ONLY = (p: string) => p === 'sessions:live:read'

/** Three active rows → "3 live". */
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

const SERVER_ERROR = () => new ApiError(500, 'server_error', 'boom')
const FORBIDDEN = () => new ApiError(403, 'forbidden', 'no')

function deferred<T>() {
  let resolve!: (v: T) => void
  let reject!: (e: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

function usageTile() {
  return screen.getByText('Active agents').closest('a')!
}

/** The headline block is skeletons while a permitted usage read is pending. */
function headlineIsSkeleton() {
  return screen.queryByText('Active agents') === null
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
  const region = screen.getByTestId('executive-usage-availability-live')
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

async function settledTile() {
  await waitFor(() =>
    expect(screen.getByText('Active agents')).toBeInTheDocument(),
  )
  return usageTile()
}

describe('ExecutiveView usage pillar — Inventory valid, Sessions not established', () => {
  it('Sessions 500: agents and tracked keep their numbers, live is "—" with a could-not-load line, never 0', async () => {
    live.mockRejectedValue(SERVER_ERROR())

    renderIntel(<ExecutiveView />)
    const tile = await settledTile()

    expect(within(tile).getByText('3')).toBeInTheDocument()
    expect(within(tile).getByText('— live · 25 tracked')).toBeInTheDocument()
    expect(within(tile).queryByText(/\b0 live/)).toBeNull()
    expect(screen.getByTestId('executive-usage-live-gap')).toHaveTextContent(
      "Sessions — couldn't load",
    )
    expect(screen.queryByTestId('executive-usage-inventory-gap')).toBeNull()
    // The scope note still describes what the reads cover; the role may make both.
    expect(screen.getByTestId('executive-usage-scope-note')).toHaveTextContent(
      'Sessions · Inventory · Tenant-wide — not filtered by workspace',
    )
  })

  it('Sessions 403 from the engine: live is "—" with the calm restricted line, not an outage', async () => {
    live.mockRejectedValue(FORBIDDEN())

    renderIntel(<ExecutiveView />)
    const tile = await settledTile()

    expect(within(tile).getByText('— live · 25 tracked')).toBeInTheDocument()
    expect(screen.getByTestId('executive-usage-live-gap')).toHaveTextContent(
      'Sessions — restricted to your role',
    )
    expect(within(tile).queryByText(/couldn't load/i)).toBeNull()
  })

  it('Sessions not permitted: no read, live is "—" with the restricted line, and the scope note names Inventory alone', async () => {
    authState.can = INVENTORY_ONLY

    renderIntel(<ExecutiveView />)
    const tile = await settledTile()

    expect(live).not.toHaveBeenCalled()
    expect(within(tile).getByText('3')).toBeInTheDocument()
    expect(within(tile).getByText('— live · 25 tracked')).toBeInTheDocument()
    expect(within(tile).queryByText(/\b0 live/)).toBeNull()
    expect(screen.getByTestId('executive-usage-live-gap')).toHaveTextContent(
      'Sessions — restricted to your role',
    )
    // The note describes only the read the role makes: Inventory, not Sessions.
    const note = screen.getByTestId('executive-usage-scope-note')
    expect(note).toHaveTextContent(
      'Inventory · Tenant-wide — not filtered by workspace',
    )
    expect(note).not.toHaveTextContent('Sessions')
  })

  it('Sessions pending: the headline stays a skeleton — no "0 live" interim — until the page arrives', async () => {
    const pending = deferred<ListResponse<LiveDTO>>()
    live.mockImplementation(() => pending.promise)

    renderIntel(<ExecutiveView />)
    await waitFor(() => expect(summary).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(live).toHaveBeenCalledTimes(1))
    // Inventory has answered; the block still waits for the live half.
    expect(headlineIsSkeleton()).toBe(true)
    expect(screen.queryByText(/0 live/)).toBeNull()

    pending.resolve(LIVE_PAGE)
    const tile = await settledTile()
    expect(within(tile).getByText('3 live · 25 tracked')).toBeInTheDocument()
    expect(screen.queryByTestId('executive-usage-live-gap')).toBeNull()
  })
})

describe('ExecutiveView usage pillar — Sessions valid, Inventory not established', () => {
  it('Inventory 500: live keeps its number, agents and tracked are "—" with a could-not-load line', async () => {
    summary.mockRejectedValue(SERVER_ERROR())

    renderIntel(<ExecutiveView />)
    const tile = await settledTile()

    // The headline figure is the inventory's: an em-dash, not "0".
    expect(within(tile).getByText('—')).toBeInTheDocument()
    expect(within(tile).getByText('3 live · — tracked')).toBeInTheDocument()
    expect(within(tile).queryByText(/\b0\b/)).toBeNull()
    expect(
      screen.getByTestId('executive-usage-inventory-gap'),
    ).toHaveTextContent("Inventory — couldn't load")
    expect(screen.queryByTestId('executive-usage-live-gap')).toBeNull()
  })

  it('Inventory not permitted: no read, agents and tracked are "—" with the restricted line', async () => {
    authState.can = SESSIONS_ONLY

    renderIntel(<ExecutiveView />)
    const tile = await settledTile()

    expect(summary).not.toHaveBeenCalled()
    expect(within(tile).getByText('—')).toBeInTheDocument()
    expect(within(tile).getByText('3 live · — tracked')).toBeInTheDocument()
    expect(
      screen.getByTestId('executive-usage-inventory-gap'),
    ).toHaveTextContent('Inventory — restricted to your role')
  })
})

describe('ExecutiveView usage pillar — both halves', () => {
  it('both failed: the pillar stays, every figure is "—", one line per half', async () => {
    live.mockRejectedValue(SERVER_ERROR())
    summary.mockRejectedValue(FORBIDDEN())

    renderIntel(<ExecutiveView />)
    const tile = await settledTile()

    expect(within(tile).getByText('— live · — tracked')).toBeInTheDocument()
    expect(screen.getByTestId('executive-usage-live-gap')).toHaveTextContent(
      "Sessions — couldn't load",
    )
    expect(
      screen.getByTestId('executive-usage-inventory-gap'),
    ).toHaveTextContent('Inventory — restricted to your role')
    expect(within(tile).queryByText(/\b0\b/)).toBeNull()
  })

  it('neither permitted: the pillar is excluded, as before — no figure, no lines, no read', async () => {
    authState.can = () => false

    renderIntel(<ExecutiveView />)

    expect(screen.getByText('No executive KPIs available')).toBeInTheDocument()
    expect(screen.queryByText('Active agents')).toBeNull()
    expect(screen.queryByTestId('executive-usage-live-gap')).toBeNull()
    expect(live).not.toHaveBeenCalled()
    expect(summary).not.toHaveBeenCalled()
  })

  it('both valid and genuinely empty: real zeros, no lines', async () => {
    live.mockResolvedValue(EMPTY_PAGE)
    summary.mockResolvedValue(EMPTY_INVENTORY)

    renderIntel(<ExecutiveView />)
    const tile = await settledTile()

    expect(within(tile).getByText('0')).toBeInTheDocument()
    expect(within(tile).getByText('0 live · 0 tracked')).toBeInTheDocument()
    expect(screen.queryByTestId('executive-usage-live-gap')).toBeNull()
    expect(screen.queryByTestId('executive-usage-inventory-gap')).toBeNull()
  })

  it('both valid: the numbers, no lines (control)', async () => {
    renderIntel(<ExecutiveView />)
    const tile = await settledTile()

    expect(within(tile).getByText('3')).toBeInTheDocument()
    expect(within(tile).getByText('3 live · 25 tracked')).toBeInTheDocument()
    expect(screen.queryByTestId('executive-usage-live-gap')).toBeNull()
    expect(screen.queryByTestId('executive-usage-inventory-gap')).toBeNull()
  })
})

describe('ExecutiveView usage pillar — leaving a successful state', () => {
  it('success → 500 on refetch: the live figure becomes "—", not the retired 3 and not 0', async () => {
    const queryClient = createTestQueryClient()
    renderIntel(<ExecutiveView />, { queryClient })
    const tile = await settledTile()
    expect(within(tile).getByText('3 live · 25 tracked')).toBeInTheDocument()

    live.mockRejectedValue(SERVER_ERROR())
    await queryClient.refetchQueries({ queryKey: ['sessions'] })

    await waitFor(() =>
      expect(
        within(usageTile()).getByText('— live · 25 tracked'),
      ).toBeInTheDocument(),
    )
    expect(within(usageTile()).queryByText(/3 live/)).toBeNull()
    expect(within(usageTile()).queryByText(/0 live/)).toBeNull()
    expect(screen.getByTestId('executive-usage-live-gap')).toHaveTextContent(
      "Sessions — couldn't load",
    )
  })

  it('success → 403 on refetch: the live figure becomes "—" with the restricted line', async () => {
    const queryClient = createTestQueryClient()
    renderIntel(<ExecutiveView />, { queryClient })
    await settledTile()

    live.mockRejectedValue(FORBIDDEN())
    await queryClient.refetchQueries({ queryKey: ['sessions'] })

    await waitFor(() =>
      expect(
        within(usageTile()).getByText('— live · 25 tracked'),
      ).toBeInTheDocument(),
    )
    expect(screen.getByTestId('executive-usage-live-gap')).toHaveTextContent(
      'Sessions — restricted to your role',
    )
  })

  it('success → permission withdrawn: the cached page is not reused; "—" with the restricted line, no new read', async () => {
    const { rerender } = renderIntel(<ExecutiveView />)
    await settledTile()
    expect(live).toHaveBeenCalledTimes(1)

    authState.can = INVENTORY_ONLY
    rerender(<ExecutiveView />)

    await waitFor(() =>
      expect(
        within(usageTile()).getByText('— live · 25 tracked'),
      ).toBeInTheDocument(),
    )
    expect(within(usageTile()).queryByText(/3 live/)).toBeNull()
    expect(live).toHaveBeenCalledTimes(1)
    expect(screen.getByTestId('executive-usage-live-gap')).toHaveTextContent(
      'Sessions — restricted to your role',
    )
    // The note stops naming Sessions the moment the role can no longer read it.
    const note = screen.getByTestId('executive-usage-scope-note')
    expect(note).toHaveTextContent(
      'Inventory · Tenant-wide — not filtered by workspace',
    )
    expect(note).not.toHaveTextContent('Sessions')
  })

  it('success → Inventory permission withdrawn: the cached summary is not reused', async () => {
    const { rerender } = renderIntel(<ExecutiveView />)
    await settledTile()

    authState.can = SESSIONS_ONLY
    rerender(<ExecutiveView />)

    await waitFor(() =>
      expect(
        within(usageTile()).getByText('3 live · — tracked'),
      ).toBeInTheDocument(),
    )
    expect(within(usageTile()).getByText('—')).toBeInTheDocument()
    expect(within(usageTile()).queryByText(/25 tracked/)).toBeNull()
    expect(summary).toHaveBeenCalledTimes(1)
  })

  it('tenant change: the old tenant’s figures are not shown under the new one; a pending new read is a skeleton, then its own answer', async () => {
    const { rerender } = renderIntel(<ExecutiveView />)
    await settledTile()

    const pending = deferred<ListResponse<LiveDTO>>()
    live.mockImplementation(() => pending.promise)
    summary.mockResolvedValue({ ...EMPTY_INVENTORY, total: 7 })
    authState.activeTenant = 'other-tenant'
    rerender(<ExecutiveView />)

    await waitFor(() => expect(live).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(headlineIsSkeleton()).toBe(true))
    expect(screen.queryByText(/3 live · 25 tracked/)).toBeNull()

    pending.resolve(EMPTY_PAGE)
    await waitFor(() =>
      expect(
        within(usageTile()).getByText('0 live · 7 tracked'),
      ).toBeInTheDocument(),
    )
  })
})

// --- the inventory half is a FLOOR when the engine truncated it ---------------
//
// `deriveUsage` has always carried `inventory.truncated` (derive.ts:191) and the
// derive test pins it, but no KPI consumer ever printed it: the tile showed the
// headline agent count and "tracked" total of a bounded scan as if they were the
// estate. The independent review of the tenant-wide correction recorded exactly that
// debt. These cases pin the disclosure and its edges: it names INVENTORY (the live
// half comes from Sessions, whose completeness this marker does not establish), it
// never hides the counts — a floor is a
// real answer — and it exists only while that truncated answer is the current,
// permitted, successful one.
const TRUNCATED_INVENTORY: InventorySummary = {
  ...inventorySummaryFixture,
  truncated: true,
}

describe('ExecutiveView usage pillar — a truncated inventory summary', () => {
  it('truncated: the counts stay and one line names Inventory as partial, never Sessions', async () => {
    summary.mockResolvedValue(TRUNCATED_INVENTORY)

    renderIntel(<ExecutiveView />)
    const tile = await settledTile()

    // A floor is still an answer: the useful counts are not hidden.
    expect(within(tile).getByText('3')).toBeInTheDocument()
    expect(within(tile).getByText('3 live · 25 tracked')).toBeInTheDocument()

    const partial = screen.getByTestId('executive-usage-inventory-partial')
    expect(partial).toHaveTextContent('Inventory')
    expect(partial).toHaveTextContent('Partial data')
    // The "live" figure is from Sessions; this Inventory marker does not describe it.
    expect(partial).not.toHaveTextContent('Sessions')
    // The caption is the compact line; the complete sentence is in the disclosure
    // OUTSIDE the tile link, beside it, naming Inventory and only Inventory.
    expect(partial).toHaveTextContent(/scan ceiling reached/i)
    expect(partial).not.toHaveTextContent(/not an exact total/i)
    expect(partial).not.toHaveAttribute('title')
    expect(tile.contains(partial)).toBe(true)
    const details = screen.getByTestId('executive-usage-partial-details')
    expect(details.tagName).toBe('DETAILS')
    expect(details.closest('a')).toBeNull()
    expect(details.parentElement).toBe(tile.parentElement)
    expect(within(details).getByText('Why partial data?')).toBeInTheDocument()
    expect(
      within(details).getByTestId(
        'executive-usage-inventory-partial-explanation',
      ),
    ).toHaveTextContent(
      'Inventory — Partial data This aggregate reached the scan ceiling — it is partial, not an exact total.',
    )
    expect(
      screen.queryByTestId('executive-usage-sessions-partial-explanation'),
    ).toBeNull()
    expect(tile.querySelector('details, summary, button')).toBeNull()
    // The print-only copy of the same row, beside the disclosure and outside it,
    // names Inventory and only Inventory (print preservation, F1).
    const print = screen.getByTestId('executive-usage-partial-details-print')
    expect(print.parentElement).toBe(tile.parentElement)
    expect(print.closest('a, details')).toBeNull()
    expect(
      within(print).getByTestId(
        'executive-usage-inventory-partial-print-explanation',
      ),
    ).toHaveTextContent(
      'Inventory — Partial data This aggregate reached the scan ceiling — it is partial, not an exact total.',
    )
    expect(
      screen.queryByTestId(
        'executive-usage-sessions-partial-print-explanation',
      ),
    ).toBeNull()
    expect(print).not.toHaveTextContent('Sessions')
    // Nothing else about the tile moves: no half is missing, the scope note stands.
    expect(screen.queryByTestId('executive-usage-live-gap')).toBeNull()
    expect(screen.queryByTestId('executive-usage-inventory-gap')).toBeNull()
    expect(screen.getByTestId('executive-usage-scope-note')).toHaveTextContent(
      'Sessions · Inventory · Tenant-wide — not filtered by workspace',
    )
  })

  it('the flag absent (the DTO makes it optional) and the flag false: figures remain, no partial line', async () => {
    // The fixture carries no `truncated` key at all — the legacy shape.
    const first = renderIntel(<ExecutiveView />)
    await settledTile()
    expect(
      within(usageTile()).getByText('3 live · 25 tracked'),
    ).toBeInTheDocument()
    expect(screen.queryByTestId('executive-usage-inventory-partial')).toBeNull()
    first.unmount()

    summary.mockResolvedValue({ ...inventorySummaryFixture, truncated: false })
    renderIntel(<ExecutiveView />)
    await settledTile()
    expect(
      within(usageTile()).getByText('3 live · 25 tracked'),
    ).toBeInTheDocument()
    expect(screen.queryByTestId('executive-usage-inventory-partial')).toBeNull()
  })

  it('a Sessions-only role: the summary is never read and Inventory is not announced as anything', async () => {
    authState.can = SESSIONS_ONLY
    summary.mockResolvedValue(TRUNCATED_INVENTORY)

    renderIntel(<ExecutiveView />)
    const tile = await settledTile()

    expect(summary).not.toHaveBeenCalled()
    expect(within(tile).getByText('3 live · — tracked')).toBeInTheDocument()
    expect(screen.queryByTestId('executive-usage-inventory-partial')).toBeNull()
    // The half it cannot read is named as restricted, not as partial.
    expect(
      screen.getByTestId('executive-usage-inventory-gap'),
    ).toHaveTextContent('Inventory — restricted to your role')
    expect(
      screen.getByTestId('executive-usage-scope-note'),
    ).not.toHaveTextContent('Inventory')
  })

  it('pending → truncated → failed refetch: the marker lives exactly as long as the figure it qualifies', async () => {
    const queryClient = createTestQueryClient()
    const pending = deferred<InventorySummary>()
    summary.mockImplementation(() => pending.promise)

    renderIntel(<ExecutiveView />, { queryClient })
    await waitFor(() => expect(summary).toHaveBeenCalledTimes(1))
    // Pending: the headline block is skeletons — nothing to qualify yet.
    expect(headlineIsSkeleton()).toBe(true)
    expect(screen.queryByTestId('executive-usage-inventory-partial')).toBeNull()

    pending.resolve(TRUNCATED_INVENTORY)
    await settledTile()
    expect(
      screen.getByTestId('executive-usage-inventory-partial'),
    ).toBeInTheDocument()

    summary.mockRejectedValue(SERVER_ERROR())
    await queryClient.refetchQueries({ queryKey: ['inventory'] })

    await waitFor(() =>
      expect(
        within(usageTile()).getByText('3 live · — tracked'),
      ).toBeInTheDocument(),
    )
    // The retired figures go, and the stale floor marker goes with them — the
    // disclosure beside the tile too.
    expect(within(usageTile()).queryByText(/25 tracked/)).toBeNull()
    expect(screen.queryByTestId('executive-usage-inventory-partial')).toBeNull()
    expect(screen.queryByTestId('executive-usage-partial-details')).toBeNull()
    // …and the print copy retires with the same answer: no stale printed row.
    expect(
      screen.queryByTestId('executive-usage-partial-details-print'),
    ).toBeNull()
    expect(screen.queryByText(/not an exact total/i)).toBeNull()
    expect(
      screen.getByTestId('executive-usage-inventory-gap'),
    ).toHaveTextContent("Inventory — couldn't load")
  })

  it('success → the Inventory read is withdrawn: the cached truncated summary is not reused', async () => {
    summary.mockResolvedValue(TRUNCATED_INVENTORY)
    const { rerender } = renderIntel(<ExecutiveView />)
    await settledTile()
    expect(
      screen.getByTestId('executive-usage-inventory-partial'),
    ).toBeInTheDocument()

    authState.can = SESSIONS_ONLY
    rerender(<ExecutiveView />)

    await waitFor(() =>
      expect(
        within(usageTile()).getByText('3 live · — tracked'),
      ).toBeInTheDocument(),
    )
    expect(screen.queryByTestId('executive-usage-inventory-partial')).toBeNull()
    expect(
      screen.queryByTestId('executive-usage-partial-details-print'),
    ).toBeNull()
    expect(screen.queryByText(/not an exact total/i)).toBeNull()
    expect(summary).toHaveBeenCalledTimes(1)
  })
})

// --- the live half is a FLOOR when the engine returned one page ------------------
//
// `GET /v1/m/sessions/live` answers with ONE most-recent page: the store reads
// limit+1 rows (default 100, ceiling 1000) and reports `has_more` when a row existed
// beyond it (modules/sessions/api.go handleListLive → page.HasMore). `deriveUsage`
// counts `live.items`, so the "live" figure of such a page is a floor — and until now
// the tile printed it exactly like a complete population, while the Inventory marker
// beside it was explicitly NOT a statement about Sessions. These cases pin the
// Sessions marker and its edges: it names SESSIONS and carries the page sentence (not
// the scan-ceiling one), it never hides the count, Inventory's marker is not borrowed
// in either direction, and it exists only while that page is the current, permitted,
// successful answer.
const MORE_PAGE: ListResponse<LiveDTO> = { ...LIVE_PAGE, has_more: true }
/** The contract always carries `has_more`; this is the shape a consumer would see if
 *  it did not — the legacy behaviour must hold, without a completeness claim. */
const FLAGLESS_PAGE = { items: LIVE_PAGE.items } as ListResponse<LiveDTO>

describe('ExecutiveView usage pillar — a live page with more rows', () => {
  it('has_more: the counts stay and one line names Sessions as partial with the page hint, never Inventory', async () => {
    live.mockResolvedValue(MORE_PAGE)

    renderIntel(<ExecutiveView />)
    const tile = await settledTile()

    // A floor is still an answer: the useful counts are not hidden.
    expect(within(tile).getByText('3')).toBeInTheDocument()
    expect(within(tile).getByText('3 live · 25 tracked')).toBeInTheDocument()

    const partial = screen.getByTestId('executive-usage-sessions-partial')
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
    const details = screen.getByTestId('executive-usage-partial-details')
    expect(details.closest('a')).toBeNull()
    expect(details.parentElement).toBe(tile.parentElement)
    expect(
      within(details).getByTestId(
        'executive-usage-sessions-partial-explanation',
      ),
    ).toHaveTextContent(
      'Sessions — Partial data The engine returned one page, not the whole set. The total CANNOT be inferred from the rows loaded, and rows that are not shown may exist.',
    )
    expect(details).not.toHaveTextContent(/scan ceiling/i)
    // The print-only copy of the same row, beside the disclosure and outside it.
    const print = screen.getByTestId('executive-usage-partial-details-print')
    expect(print.parentElement).toBe(tile.parentElement)
    expect(print.closest('a, details')).toBeNull()
    expect(
      within(print).getByTestId(
        'executive-usage-sessions-partial-print-explanation',
      ),
    ).toHaveTextContent(
      'Sessions — Partial data The engine returned one page, not the whole set. The total CANNOT be inferred from the rows loaded, and rows that are not shown may exist.',
    )
    expect(print).not.toHaveTextContent(/scan ceiling/i)
    // Inventory is complete here: the page does not make it partial.
    expect(screen.queryByTestId('executive-usage-inventory-partial')).toBeNull()
    expect(
      screen.queryByTestId('executive-usage-inventory-partial-explanation'),
    ).toBeNull()
    expect(
      screen.queryByTestId(
        'executive-usage-inventory-partial-print-explanation',
      ),
    ).toBeNull()
    // Nothing else about the tile moves: no half is missing, the scope note stands.
    expect(screen.queryByTestId('executive-usage-live-gap')).toBeNull()
    expect(screen.queryByTestId('executive-usage-inventory-gap')).toBeNull()
    expect(screen.getByTestId('executive-usage-scope-note')).toHaveTextContent(
      'Sessions · Inventory · Tenant-wide — not filtered by workspace',
    )
  })

  it('has_more false (the contract) and the flag absent (legacy shape): figures remain, no partial line, no completeness claim', async () => {
    const first = renderIntel(<ExecutiveView />)
    await settledTile()
    expect(
      within(usageTile()).getByText('3 live · 25 tracked'),
    ).toBeInTheDocument()
    expect(screen.queryByTestId('executive-usage-sessions-partial')).toBeNull()
    first.unmount()

    live.mockResolvedValue(FLAGLESS_PAGE)
    renderIntel(<ExecutiveView />)
    await settledTile()
    expect(
      within(usageTile()).getByText('3 live · 25 tracked'),
    ).toBeInTheDocument()
    expect(screen.queryByTestId('executive-usage-sessions-partial')).toBeNull()
  })

  it('both sources partial at once: two lines, each naming its own source with its own sentence', async () => {
    live.mockResolvedValue(MORE_PAGE)
    summary.mockResolvedValue(TRUNCATED_INVENTORY)

    renderIntel(<ExecutiveView />)
    const tile = await settledTile()

    expect(within(tile).getByText('3 live · 25 tracked')).toBeInTheDocument()
    const sessionsNote = screen.getByTestId('executive-usage-sessions-partial')
    const inventoryNote = screen.getByTestId(
      'executive-usage-inventory-partial',
    )
    expect(sessionsNote).toHaveTextContent('Sessions')
    expect(sessionsNote).not.toHaveTextContent('Inventory')
    expect(inventoryNote).toHaveTextContent('Inventory')
    expect(inventoryNote).not.toHaveTextContent('Sessions')
    expect(sessionsNote).toHaveTextContent(/one page/i)
    expect(inventoryNote).toHaveTextContent(/scan ceiling/i)
    expect(sessionsNote).not.toHaveAttribute('title')
    expect(inventoryNote).not.toHaveAttribute('title')
    // ONE disclosure beside the tile, two labelled rows, live first, each with its
    // own sentence and neither borrowing the other's.
    expect(screen.getAllByText('Why partial data?')).toHaveLength(1)
    const details = screen.getByTestId('executive-usage-partial-details')
    expect(details.parentElement).toBe(tile.parentElement)
    const sessionsRow = within(details).getByTestId(
      'executive-usage-sessions-partial-explanation',
    )
    const inventoryRow = within(details).getByTestId(
      'executive-usage-inventory-partial-explanation',
    )
    expect(sessionsRow).toHaveTextContent(/not the whole set/i)
    expect(sessionsRow).not.toHaveTextContent(/scan ceiling/i)
    expect(inventoryRow).toHaveTextContent(/not an exact total/i)
    expect(inventoryRow).not.toHaveTextContent(/one page/i)
    expect(
      sessionsRow.compareDocumentPosition(inventoryRow) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy()
    // ONE print copy beside it, the same two rows in the same order.
    const print = screen.getByTestId('executive-usage-partial-details-print')
    expect(print.parentElement).toBe(tile.parentElement)
    const printSessions = within(print).getByTestId(
      'executive-usage-sessions-partial-print-explanation',
    )
    const printInventory = within(print).getByTestId(
      'executive-usage-inventory-partial-print-explanation',
    )
    expect(printSessions.textContent).toBe(sessionsRow.textContent)
    expect(printInventory.textContent).toBe(inventoryRow.textContent)
    expect(
      printSessions.compareDocumentPosition(printInventory) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy()
  })

  it('an Inventory-only role: the page is never read and Sessions is named as restricted, not as partial', async () => {
    authState.can = INVENTORY_ONLY
    live.mockResolvedValue(MORE_PAGE)

    renderIntel(<ExecutiveView />)
    const tile = await settledTile()

    expect(live).not.toHaveBeenCalled()
    expect(within(tile).getByText('— live · 25 tracked')).toBeInTheDocument()
    expect(screen.queryByTestId('executive-usage-sessions-partial')).toBeNull()
    expect(screen.getByTestId('executive-usage-live-gap')).toHaveTextContent(
      'Sessions — restricted to your role',
    )
  })

  it('pending → has_more page → failed refetch: the marker lives exactly as long as the figure it qualifies', async () => {
    const queryClient = createTestQueryClient()
    const pending = deferred<ListResponse<LiveDTO>>()
    live.mockImplementation(() => pending.promise)

    renderIntel(<ExecutiveView />, { queryClient })
    await waitFor(() => expect(live).toHaveBeenCalledTimes(1))
    // Pending: the headline block is skeletons — nothing to qualify yet.
    expect(headlineIsSkeleton()).toBe(true)
    expect(screen.queryByTestId('executive-usage-sessions-partial')).toBeNull()

    pending.resolve(MORE_PAGE)
    await settledTile()
    expect(
      screen.getByTestId('executive-usage-sessions-partial'),
    ).toBeInTheDocument()
    expect(
      within(usageTile()).getByText('3 live · 25 tracked'),
    ).toBeInTheDocument()

    live.mockRejectedValue(SERVER_ERROR())
    await queryClient.refetchQueries({ queryKey: ['sessions'] })

    await waitFor(() =>
      expect(
        within(usageTile()).getByText('— live · 25 tracked'),
      ).toBeInTheDocument(),
    )
    // The retired figure goes, and the stale page marker goes with it.
    expect(within(usageTile()).queryByText(/3 live/)).toBeNull()
    expect(screen.queryByTestId('executive-usage-sessions-partial')).toBeNull()
    expect(screen.getByTestId('executive-usage-live-gap')).toHaveTextContent(
      "Sessions — couldn't load",
    )
  })

  it('success → the Sessions read is withdrawn: the cached page is not reused — no figure, no marker, no new read', async () => {
    live.mockResolvedValue(MORE_PAGE)
    const { rerender } = renderIntel(<ExecutiveView />)
    await settledTile()
    expect(
      screen.getByTestId('executive-usage-sessions-partial'),
    ).toBeInTheDocument()

    authState.can = INVENTORY_ONLY
    rerender(<ExecutiveView />)

    await waitFor(() =>
      expect(
        within(usageTile()).getByText('— live · 25 tracked'),
      ).toBeInTheDocument(),
    )
    expect(screen.queryByTestId('executive-usage-sessions-partial')).toBeNull()
    expect(
      screen.queryByTestId('executive-usage-partial-details-print'),
    ).toBeNull()
    expect(screen.queryByText(/not the whole set/i)).toBeNull()
    expect(live).toHaveBeenCalledTimes(1)
    expect(screen.getByTestId('executive-usage-live-gap')).toHaveTextContent(
      'Sessions — restricted to your role',
    )
  })

  it('tenant change: the old tenant’s page marker is not carried over; the new tenant’s own answer decides', async () => {
    live.mockResolvedValue(MORE_PAGE)
    const { rerender } = renderIntel(<ExecutiveView />)
    await settledTile()
    expect(
      screen.getByTestId('executive-usage-sessions-partial'),
    ).toBeInTheDocument()

    const pending = deferred<ListResponse<LiveDTO>>()
    live.mockImplementation(() => pending.promise)
    authState.activeTenant = 'other-tenant'
    rerender(<ExecutiveView />)

    await waitFor(() => expect(live).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(headlineIsSkeleton()).toBe(true))
    expect(screen.queryByTestId('executive-usage-sessions-partial')).toBeNull()
    // The old tenant's printed row is not carried over either.
    expect(
      screen.queryByTestId('executive-usage-partial-details-print'),
    ).toBeNull()
    expect(screen.queryByText(/not the whole set/i)).toBeNull()

    pending.resolve(EMPTY_PAGE)
    await settledTile()
    expect(
      within(usageTile()).getByText('0 live · 25 tracked'),
    ).toBeInTheDocument()
    expect(screen.queryByTestId('executive-usage-sessions-partial')).toBeNull()
    expect(
      screen.queryByTestId('executive-usage-partial-details-print'),
    ).toBeNull()
  })
})

// --- a permitted half that is pending but NOT fetching --------------------------
//
// `usageGap` told a refusal from a failure and nothing else, and the headline is
// skeletons only while a read is in FLIGHT — so a half that was `pending` with
// `fetchStatus` idle or paused rendered "—" with no line, no skeleton and no
// announcement. These cases pin the two new lines and their edges: a pause is not an
// outage, a refusal or a lost connection; nothing here is a 0; the neighbour keeps its
// figure and its markers; a SUCCESSFUL answer whose refetch is paused keeps
// everything; and the region says which half changed to what, never how many.
const INITIAL_ANNOUNCEMENT =
  'Sessions: figures available. Inventory: figures available.'

describe('ExecutiveView usage pillar — pending reasons (pure flag fixture)', () => {
  it('flags alone say idle: no skeleton, "— live · 25 tracked", a "not started" line, not "couldn\'t load", not "restricted"', async () => {
    flagFixture.overrides.set('sessions', PENDING_IDLE_FLAGS)

    renderIntel(<ExecutiveView />)
    const tile = await settledTile()

    expect(within(tile).getByText('3')).toBeInTheDocument()
    expect(within(tile).getByText('— live · 25 tracked')).toBeInTheDocument()
    expect(within(tile).queryByText(/\b0 live/)).toBeNull()
    expect(screen.getByTestId('executive-usage-live-gap')).toHaveTextContent(
      'Sessions — query not started',
    )
    expect(within(tile).queryByText(/couldn't load/i)).toBeNull()
    expect(within(tile).queryByText(/restricted/i)).toBeNull()
    expect(within(tile).queryByText(/paused/i)).toBeNull()
    expect(screen.queryByTestId('executive-usage-inventory-gap')).toBeNull()
    expect(screen.queryByTestId('executive-usage-sessions-partial')).toBeNull()
    expect(screen.getByTestId('executive-usage-scope-note')).toHaveTextContent(
      'Sessions · Inventory · Tenant-wide — not filtered by workspace',
    )
    expect(liveRegion()).toHaveTextContent(
      'Sessions: query not started. Inventory: figures available.',
    )
  })

  it('flags alone say paused: the headline is "—", "3 live · — tracked", a "paused" line, not offline, not denied, not failed', async () => {
    flagFixture.overrides.set('inventory', PENDING_PAUSED_FLAGS)

    renderIntel(<ExecutiveView />)
    const tile = await settledTile()

    expect(within(tile).getByText('—')).toBeInTheDocument()
    expect(within(tile).getByText('3 live · — tracked')).toBeInTheDocument()
    expect(within(tile).queryByText(/\b0\b/)).toBeNull()
    expect(
      screen.getByTestId('executive-usage-inventory-gap'),
    ).toHaveTextContent('Inventory — query paused, waiting to resume')
    expect(within(tile).queryByText(/couldn't load/i)).toBeNull()
    expect(within(tile).queryByText(/offline/i)).toBeNull()
    expect(within(tile).queryByText(/restricted/i)).toBeNull()
    expect(screen.queryByTestId('executive-usage-live-gap')).toBeNull()
    expect(screen.queryByTestId('executive-usage-inventory-partial')).toBeNull()
    expect(liveRegion()).toHaveTextContent(
      'Sessions: figures available. Inventory: query paused, waiting to resume.',
    )
  })
})

describe('ExecutiveView usage pillar — a real pause and resume (QueryClient onlineManager)', () => {
  it('offline at mount: no skeleton, Sessions is paused with its line and no read; the accepted Inventory answer and its marker survive their paused refetch; online resumes both', async () => {
    const queryClient = createTestQueryClient()
    // Inventory already has an accepted, truncated answer; with staleTime 0 the mount
    // refetches it — and offline, that refetch is PAUSED, not lost.
    queryClient.setQueryData(inventoryKeys.summary('demo'), TRUNCATED_INVENTORY)
    summary.mockResolvedValue(TRUNCATED_INVENTORY)
    onlineManager.setOnline(false)

    renderIntel(<ExecutiveView />, { queryClient })
    const tile = await settledTile()

    // Real query-core state, not a fixture: dispatched and paused, the queryFn unrun.
    const liveState = queryClient.getQueryState(sessionsKeys.live('demo'))
    expect(liveState?.status).toBe('pending')
    expect(liveState?.fetchStatus).toBe('paused')
    expect(live).not.toHaveBeenCalled()
    expect(within(tile).getByText('3')).toBeInTheDocument()
    expect(within(tile).getByText('— live · 25 tracked')).toBeInTheDocument()
    expect(within(tile).queryByText(/\b0 live/)).toBeNull()
    expect(screen.getByTestId('executive-usage-live-gap')).toHaveTextContent(
      'Sessions — query paused, waiting to resume',
    )
    expect(within(tile).queryByText(/couldn't load/i)).toBeNull()
    expect(screen.queryByTestId('executive-usage-sessions-partial')).toBeNull()

    // The neighbour: success + paused refetch = the figures AND the floor marker stay.
    const inventoryState = queryClient.getQueryState(
      inventoryKeys.summary('demo'),
    )
    expect(inventoryState?.status).toBe('success')
    expect(inventoryState?.fetchStatus).toBe('paused')
    expect(summary).not.toHaveBeenCalled()
    expect(
      screen.getByTestId('executive-usage-inventory-partial'),
    ).toHaveTextContent(/scan ceiling reached/i)
    expect(screen.queryByTestId('executive-usage-inventory-gap')).toBeNull()
    expect(liveRegion()).toHaveTextContent(
      'Sessions: query paused, waiting to resume. Inventory: figures available, partial data.',
    )

    act(() => onlineManager.setOnline(true))
    await waitFor(() =>
      expect(
        within(usageTile()).getByText('3 live · 25 tracked'),
      ).toBeInTheDocument(),
    )
    expect(live).toHaveBeenCalledTimes(1)
    expect(summary).toHaveBeenCalledTimes(1)
    expect(screen.queryByTestId('executive-usage-live-gap')).toBeNull()
    expect(
      screen.getByTestId('executive-usage-inventory-partial'),
    ).toBeInTheDocument()
    expect(liveRegion()).toHaveTextContent(
      'Sessions: figures available. Inventory: figures available, partial data.',
    )
  })

  it('paused → resumed into an EMPTY page: "0 live", not "—" and not a leftover line', async () => {
    const queryClient = createTestQueryClient()
    queryClient.setQueryData(
      inventoryKeys.summary('demo'),
      inventorySummaryFixture,
    )
    live.mockResolvedValue(EMPTY_PAGE)
    onlineManager.setOnline(false)

    renderIntel(<ExecutiveView />, { queryClient })
    const tile = await settledTile()
    expect(within(tile).getByText('— live · 25 tracked')).toBeInTheDocument()
    expect(screen.getByTestId('executive-usage-live-gap')).toHaveTextContent(
      'Sessions — query paused, waiting to resume',
    )

    act(() => onlineManager.setOnline(true))
    await waitFor(() =>
      expect(
        within(usageTile()).getByText('0 live · 25 tracked'),
      ).toBeInTheDocument(),
    )
    expect(screen.queryByTestId('executive-usage-live-gap')).toBeNull()
    expect(screen.queryByTestId('executive-usage-sessions-partial')).toBeNull()
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

    renderIntel(<ExecutiveView />, { queryClient })
    await settledTile()
    expect(screen.getByTestId('executive-usage-live-gap')).toHaveTextContent(
      'Sessions — query paused, waiting to resume',
    )
    // While paused nothing is known: no marker for a figure that is not there.
    expect(screen.queryByTestId('executive-usage-sessions-partial')).toBeNull()

    act(() => onlineManager.setOnline(true))
    await waitFor(() =>
      expect(
        within(usageTile()).getByText('3 live · 25 tracked'),
      ).toBeInTheDocument(),
    )
    expect(
      screen.getByTestId('executive-usage-sessions-partial'),
    ).toHaveTextContent(/one page loaded; there are more/i)
    expect(screen.queryByTestId('executive-usage-live-gap')).toBeNull()
    expect(liveRegion()).toHaveTextContent(
      'Sessions: figures available, partial data. Inventory: figures available.',
    )
  })

  it('success → refetch paused: the accepted page, its marker and its caption stay; no line, no announcement change; online completes the refetch', async () => {
    const queryClient = createTestQueryClient()
    live.mockResolvedValue(MORE_PAGE)
    renderIntel(<ExecutiveView />, { queryClient })
    await settledTile()
    await waitFor(() =>
      expect(
        screen.getByTestId('executive-usage-sessions-partial'),
      ).toBeInTheDocument(),
    )
    const region = liveRegion()
    expect(region).toHaveTextContent(
      'Sessions: figures available, partial data. Inventory: figures available.',
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

    const tile = usageTile()
    expect(within(tile).getByText('3 live · 25 tracked')).toBeInTheDocument()
    expect(
      screen.getByTestId('executive-usage-sessions-partial'),
    ).toBeInTheDocument()
    expect(screen.queryByTestId('executive-usage-live-gap')).toBeNull()
    expect(within(tile).queryByText(/— live/)).toBeNull()
    expect(watched.mutations()).toBe(0)

    act(() => onlineManager.setOnline(true))
    await waitFor(() => expect(live).toHaveBeenCalledTimes(2))
    await waitFor(() =>
      expect(
        queryClient.getQueryState(sessionsKeys.live('demo'))?.fetchStatus,
      ).toBe('idle'),
    )
    expect(
      within(usageTile()).getByText('3 live · 25 tracked'),
    ).toBeInTheDocument()
    expect(watched.mutations()).toBe(0)
    watched.disconnect()
  })
})

describe('ExecutiveView usage pillar — a real idle (QueryClient cancel)', () => {
  it('a fetching read cancelled and reverted is "not started": the skeleton gives way to "—" with the line; a refetch starts it', async () => {
    const queryClient = createTestQueryClient()
    const pending = deferred<ListResponse<LiveDTO>>()
    live.mockImplementation(() => pending.promise)

    renderIntel(<ExecutiveView />, { queryClient })
    await waitFor(() => expect(summary).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(live).toHaveBeenCalledTimes(1))
    // In flight: the headline block is skeletons, as before.
    expect(headlineIsSkeleton()).toBe(true)
    expect(liveRegion()).toHaveTextContent(
      'Sessions: loading. Inventory: figures available.',
    )

    await queryClient.cancelQueries({ queryKey: sessionsKeys.live('demo') })
    const tile = await settledTile()
    const state = queryClient.getQueryState(sessionsKeys.live('demo'))
    expect(state?.status).toBe('pending')
    expect(state?.fetchStatus).toBe('idle')
    expect(within(tile).getByText('— live · 25 tracked')).toBeInTheDocument()
    expect(screen.getByTestId('executive-usage-live-gap')).toHaveTextContent(
      'Sessions — query not started',
    )
    expect(within(tile).queryByText(/couldn't load/i)).toBeNull()
    expect(within(tile).queryByText(/\b0 live/)).toBeNull()
    expect(liveRegion()).toHaveTextContent(
      'Sessions: query not started. Inventory: figures available.',
    )

    live.mockResolvedValue(LIVE_PAGE)
    await queryClient.refetchQueries({ queryKey: sessionsKeys.live('demo') })
    await waitFor(() =>
      expect(
        within(usageTile()).getByText('3 live · 25 tracked'),
      ).toBeInTheDocument(),
    )
    expect(screen.queryByTestId('executive-usage-live-gap')).toBeNull()
    expect(liveRegion()).toHaveTextContent(INITIAL_ANNOUNCEMENT)
  })
})

describe('ExecutiveView availability region — restriction, recovery, coverage, polling', () => {
  it('announces a 403 restriction, the recovery and a coverage change once each; stays silent on polls that only change numbers; moves no focus', async () => {
    const queryClient = createTestQueryClient()
    renderIntel(<ExecutiveView />, { queryClient })
    await settledTile()
    const region = liveRegion()
    expect(region).toHaveTextContent(INITIAL_ANNOUNCEMENT)
    expect(region).not.toHaveTextContent(/\d/)
    expect(screen.getAllByRole('status')).toHaveLength(1)

    // Focus rests on a control outside the tiles; nothing below may move it.
    const exportButton = screen.getByRole('button', { name: /Export PDF/i })
    exportButton.focus()
    expect(document.activeElement).toBe(exportButton)
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
      expect(
        within(usageTile()).getByText('4 live · 25 tracked'),
      ).toBeInTheDocument(),
    )
    expect(live).toHaveBeenCalledTimes(3)
    expect(watched.mutations()).toBe(0)
    expect(region).toHaveTextContent(INITIAL_ANNOUNCEMENT)

    // The engine refuses: the visible line is the calm boundary; so is the sentence.
    live.mockRejectedValue(FORBIDDEN())
    await queryClient.refetchQueries({ queryKey: sessionsKeys.live('demo') })
    await waitFor(() =>
      expect(
        within(usageTile()).getByText('— live · 25 tracked'),
      ).toBeInTheDocument(),
    )
    expect(screen.getByTestId('executive-usage-live-gap')).toHaveTextContent(
      'Sessions — restricted to your role',
    )
    expect(region).toHaveTextContent(
      'Sessions: restricted to your role. Inventory: figures available.',
    )
    expect(region).not.toHaveTextContent('4')
    const afterRefusal = watched.mutations()
    expect(afterRefusal).toBeGreaterThan(0)

    // An identical refused poll: same text, no mutation.
    await queryClient.refetchQueries({ queryKey: sessionsKeys.live('demo') })
    expect(watched.mutations()).toBe(afterRefusal)

    // Then an outage: another state, another sentence.
    live.mockRejectedValue(SERVER_ERROR())
    await queryClient.refetchQueries({ queryKey: sessionsKeys.live('demo') })
    await waitFor(() =>
      expect(screen.getByTestId('executive-usage-live-gap')).toHaveTextContent(
        "Sessions — couldn't load",
      ),
    )
    expect(region).toHaveTextContent(
      'Sessions: could not be loaded. Inventory: figures available.',
    )

    // Recovery into a page with more rows: the figure returns WITH its marker, and the
    // region says "partial" — a coverage change is a state change.
    live.mockResolvedValue(MORE_PAGE)
    await queryClient.refetchQueries({ queryKey: sessionsKeys.live('demo') })
    await waitFor(() =>
      expect(
        within(usageTile()).getByText('3 live · 25 tracked'),
      ).toBeInTheDocument(),
    )
    expect(
      screen.getByTestId('executive-usage-sessions-partial'),
    ).toBeInTheDocument()
    expect(screen.queryByTestId('executive-usage-live-gap')).toBeNull()
    expect(region).toHaveTextContent(
      'Sessions: figures available, partial data. Inventory: figures available.',
    )

    // Coverage back to a complete page: the marker leaves and the region says so.
    live.mockResolvedValue(LIVE_PAGE)
    await queryClient.refetchQueries({ queryKey: sessionsKeys.live('demo') })
    await waitFor(() =>
      expect(
        screen.queryByTestId('executive-usage-sessions-partial'),
      ).toBeNull(),
    )
    expect(region).toHaveTextContent(INITIAL_ANNOUNCEMENT)

    expect(document.activeElement).toBe(exportButton)
    expect(screen.getAllByRole('status')).toHaveLength(1)
    watched.disconnect()
  })

  it('a role that reads neither half: the pillar is excluded and the region is empty', async () => {
    authState.can = (p) => p === 'health:status:read'
    renderIntel(<ExecutiveView />)
    await waitFor(() =>
      expect(screen.getByText('Executive overview')).toBeInTheDocument(),
    )
    expect(screen.queryByText('Active agents')).toBeNull()
    expect(liveRegion()).toHaveTextContent('')
    expect(live).not.toHaveBeenCalled()
    expect(summary).not.toHaveBeenCalled()
  })
})

describe('ExecutiveView usage pillar — tenant and permission transitions while pending', () => {
  it('paused → Sessions permission withdrawn → online: the line becomes "restricted", the scope note drops Sessions, the withdrawn read never goes out; a re-grant starts it afresh', async () => {
    const queryClient = createTestQueryClient()
    queryClient.setQueryData(
      inventoryKeys.summary('demo'),
      inventorySummaryFixture,
    )
    onlineManager.setOnline(false)

    const { rerender } = renderIntel(<ExecutiveView />, { queryClient })
    await settledTile()
    expect(screen.getByTestId('executive-usage-live-gap')).toHaveTextContent(
      'Sessions — query paused, waiting to resume',
    )
    expect(live).not.toHaveBeenCalled()

    authState.can = INVENTORY_ONLY
    rerender(<ExecutiveView />)
    await waitFor(() =>
      expect(screen.getByTestId('executive-usage-live-gap')).toHaveTextContent(
        'Sessions — restricted to your role',
      ),
    )
    expect(
      within(usageTile()).getByText('— live · 25 tracked'),
    ).toBeInTheDocument()
    const note = screen.getByTestId('executive-usage-scope-note')
    expect(note).toHaveTextContent(
      'Inventory · Tenant-wide — not filtered by workspace',
    )
    expect(note).not.toHaveTextContent('Sessions')
    expect(liveRegion()).toHaveTextContent(
      'Sessions: restricted to your role. Inventory: figures available.',
    )
    // The paused dispatch was cancelled and reverted, not left waiting for the network.
    await waitFor(() =>
      expect(
        queryClient.getQueryState(sessionsKeys.live('demo'))?.fetchStatus,
      ).toBe('idle'),
    )

    act(() => onlineManager.setOnline(true))
    await waitFor(() => expect(summary).toHaveBeenCalledTimes(1))
    expect(live).not.toHaveBeenCalled()
    expect(screen.getByTestId('executive-usage-live-gap')).toHaveTextContent(
      'Sessions — restricted to your role',
    )

    authState.can = BOTH
    rerender(<ExecutiveView />)
    await waitFor(() =>
      expect(
        within(usageTile()).getByText('3 live · 25 tracked'),
      ).toBeInTheDocument(),
    )
    expect(live).toHaveBeenCalledTimes(1)
    expect(screen.queryByTestId('executive-usage-live-gap')).toBeNull()
    expect(liveRegion()).toHaveTextContent(INITIAL_ANNOUNCEMENT)
  })

  it('tenant change while paused: nothing of the previous tenant remains — no figure, no marker, no sentence — and only the new tenant is read when online, with real zeros', async () => {
    const queryClient = createTestQueryClient()
    queryClient.setQueryData(inventoryKeys.summary('demo'), TRUNCATED_INVENTORY)
    onlineManager.setOnline(false)

    const { rerender } = renderIntel(<ExecutiveView />, { queryClient })
    await settledTile()
    expect(
      screen.getByTestId('executive-usage-inventory-partial'),
    ).toBeInTheDocument()
    expect(screen.getByTestId('executive-usage-live-gap')).toHaveTextContent(
      'Sessions — query paused, waiting to resume',
    )

    authState.activeTenant = 'other-tenant'
    rerender(<ExecutiveView />)
    await waitFor(() =>
      expect(
        screen.getByTestId('executive-usage-inventory-gap'),
      ).toHaveTextContent('Inventory — query paused, waiting to resume'),
    )
    const tile = usageTile()
    expect(within(tile).getByText('—')).toBeInTheDocument()
    expect(within(tile).getByText('— live · — tracked')).toBeInTheDocument()
    expect(screen.queryByText(/25 tracked/)).toBeNull()
    expect(screen.queryByTestId('executive-usage-inventory-partial')).toBeNull()
    expect(screen.getByTestId('executive-usage-live-gap')).toHaveTextContent(
      'Sessions — query paused, waiting to resume',
    )
    expect(liveRegion()).toHaveTextContent(
      'Sessions: query paused, waiting to resume. Inventory: query paused, waiting to resume.',
    )
    expect(live).not.toHaveBeenCalled()
    expect(summary).not.toHaveBeenCalled()

    live.mockResolvedValue(EMPTY_PAGE)
    summary.mockResolvedValue(EMPTY_INVENTORY)
    act(() => onlineManager.setOnline(true))
    await waitFor(() =>
      expect(
        within(usageTile()).getByText('0 live · 0 tracked'),
      ).toBeInTheDocument(),
    )
    expect(within(usageTile()).getByText('0')).toBeInTheDocument()
    // Sessions: ONE read, for the new tenant — the old tenant's dispatch was PENDING
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
    expect(screen.queryByText(/25 tracked/)).toBeNull()
    expect(screen.queryByTestId('executive-usage-inventory-partial')).toBeNull()
    expect(screen.queryByTestId('executive-usage-live-gap')).toBeNull()
    expect(screen.queryByTestId('executive-usage-inventory-gap')).toBeNull()
    expect(liveRegion()).toHaveTextContent(INITIAL_ANNOUNCEMENT)
  })
})
