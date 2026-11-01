// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { TooltipProvider } from '@/components/ui/tooltip'
import {
  SidePanelHost,
  SidePanelProvider,
} from '@/components/layout/side-panel'
import { clippingAncestors } from '@/test/clipping'
import { stubViewportWidth } from '@/test/viewport'
import type { AuditEventDTO } from '@/lib/api/types'

const toast = vi.hoisted(() => ({
  success: vi.fn(),
  error: vi.fn(),
  warning: vi.fn(),
  info: vi.fn(),
}))
vi.mock('@/components/ui/toaster', () => ({ toast, Toaster: () => null }))

const auth = vi.hoisted(() => ({
  activeTenant: 't1' as string | null,
  isSuperadmin: false,
  can: (_permission: string): boolean => true,
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => auth }))

const navigate = vi.hoisted(() => vi.fn())
vi.mock('@tanstack/react-router', () => ({
  // useUrlState follows the location, so the mock answers with the REAL URL.
  // A constant '' passed while the effect read window.location; since
  // f9426990b the effect reads this value, and a constant would erase the
  // state the URL just seeded.
  useRouterState: () => window.location.search,
  useNavigate: () => navigate,
}))

vi.mock('@/features/saved-views', () => ({
  SavedViewsMenu: ({
    onApply,
  }: {
    onApply: (params: Record<string, string>) => void
  }) => (
    <button
      type="button"
      onClick={() =>
        onApply({ actor: 'user:saved', action: 'policy.', unknown: 'ignored' })
      }
    >
      Apply saved audit view
    </button>
  ),
}))

// The directories the ledger names a reference from: the people roster and the
// workspace list, each the read the console already makes for its own screen.
const directory = vi.hoisted(() => ({
  listMembers: vi.fn(),
  listWorkspaces: vi.fn(),
}))
vi.mock('@/features/console/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/features/console/api')>()
  return { ...actual, consoleApi: { ...actual.consoleApi, ...directory } }
})

// The pending approvals (the flagged row) and the live sessions (a session's name).
const governance = vi.hoisted(() => ({
  listApprovals: vi.fn(),
  listPolicies: vi.fn(),
}))
vi.mock('@/features/governance/api', async (importOriginal) => {
  const actual =
    await importOriginal<typeof import('@/features/governance/api')>()
  return {
    ...actual,
    governanceApi: { ...actual.governanceApi, ...governance },
  }
})
const sessions = vi.hoisted(() => ({ live: vi.fn() }))
vi.mock('@/features/sessions/api', async (importOriginal) => {
  const actual =
    await importOriginal<typeof import('@/features/sessions/api')>()
  return { ...actual, sessionsApi: { ...actual.sessionsApi, ...sessions } }
})

const api = vi.hoisted(() => ({
  list: vi.fn(),
  recent: vi.fn(),
  systemList: vi.fn(),
  verify: vi.fn(),
  pubkey: vi.fn(),
}))
vi.mock('@/lib/api/endpoints', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api/endpoints')>()
  return { ...actual, auditApi: api }
})

const exp = vi.hoisted(() => ({
  fetchAuditExport: vi.fn(),
  downloadBlob: vi.fn(),
  exportFilename: vi.fn((f: string) => `olivares-audit-${f}.log`),
}))
vi.mock('./export', () => exp)

import { AuditView } from './audit-view'
import { AUDIT_EVIDENCE_AVAILABLE } from './evidence'

/** An entry's sentence button, once the list has drawn it. */
const entry = (name: RegExp) => screen.findByRole('button', { name })

function wrap(ui: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <TooltipProvider delayDuration={0}>
        <SidePanelProvider>
          {ui}
          <SidePanelHost />
        </SidePanelProvider>
      </TooltipProvider>
    </QueryClientProvider>,
  )
}

const ev1: AuditEventDTO = {
  id: 'evt-0000000000000001',
  seq: 12,
  occurred_at: '2026-06-20T10:00:00Z',
  actor: 'user:alice',
  actor_kind: 'user',
  action: 'agent.create',
  target_kind: 'agent',
  target_id: 'ag-1',
  prev_hash: 'aa'.repeat(32),
  hash: 'bb'.repeat(32),
  sig: 'c2lnbmF0dXJl',
}

const ev2: AuditEventDTO = {
  id: 'evt-0000000000000002',
  seq: 13,
  occurred_at: '2026-06-20T10:05:00Z',
  actor: '',
  actor_kind: 'system',
  action: 'audit.read',
  prev_hash: 'bb'.repeat(32),
  hash: 'cc'.repeat(32),
}

beforeEach(() => {
  for (const fn of Object.values(api)) fn.mockReset()
  for (const fn of Object.values(exp)) fn.mockReset?.()
  exp.exportFilename.mockImplementation(
    (f: string) => `olivares-audit-${f}.log`,
  )
  toast.success.mockReset()
  toast.error.mockReset()
  toast.warning.mockReset()
  navigate.mockReset()
  auth.isSuperadmin = false
  auth.activeTenant = 't1'
  auth.can = (() => true) as unknown as typeof auth.can
  directory.listMembers.mockReset()
  directory.listWorkspaces.mockReset()
  directory.listMembers.mockResolvedValue({
    items: [
      {
        user_id: '01a0b580-4f7e-79b5-a98c-32aa681a4502',
        display_name: 'Alice Ng',
        email: 'a@n.test',
      },
    ],
    has_more: false,
  })
  directory.listWorkspaces.mockResolvedValue({
    items: [{ id: 'ws-1', name: 'Billing', slug: 'billing' }],
    has_more: false,
  })
  governance.listApprovals.mockResolvedValue({ items: [], has_more: false })
  governance.listPolicies.mockResolvedValue({ items: [] })
  sessions.live.mockResolvedValue({ items: [], has_more: false })
  window.history.replaceState(null, '', '/audit')
  // The view reads the head first (newest first, acceptance): 100 positions = one window.
  api.recent.mockResolvedValue({ items: [], head_seq: 100 })
  api.list.mockResolvedValue({ items: [], has_more: false })
  api.systemList.mockResolvedValue({ items: [], has_more: false })
  api.pubkey.mockResolvedValue({
    algorithm: 'ed25519',
    public_key: 'QkFTRTY0S0VZ',
  })
})
afterEach(() => {
  vi.clearAllMocks()
  window.history.replaceState(null, '', '/')
})

/** The entry sentences, in the order the list paints them. */
function entryTexts(): string[] {
  return screen
    .queryAllByRole('button')
    .filter((b) => b.dataset.slot === 'entry')
    .map((b) => b.textContent ?? '')
}

describe('AuditView — first work row', () => {
  afterEach(() => stubViewportWidth(1440))

  // WHAT THIS CASE CAN SEE, AND WHAT IT CANNOT. jsdom performs no layout, so no test
  // here can tell whether the name fits: what it holds is the CONTRACT the name
  // declares — the whole string, a heading that refuses to shrink or wrap, and no
  // ancestor that would clip it. Whether the header row then fits at 1920, 1180 and
  // 390 px is a browser measurement, and the browser sweep is where it belongs. The
  // two widths below are the two code paths — desktop and phone; 1180 ran the same
  // path as 1440 and proved it twice.
  it('the screen name declares that it will not shrink, wrap or be clipped', async () => {
    api.list.mockResolvedValue({ items: [], has_more: false })
    for (const width of [1440, 390] as const) {
      stubViewportWidth(width)
      const { unmount } = wrap(<AuditView />)
      const h1 = await screen.findByRole('heading', {
        level: 1,
        name: 'Audit ledger',
      })
      expect(h1.textContent).toBe('Audit ledger')
      expect(h1.className.split(/\s+/)).toEqual(
        expect.arrayContaining(['min-w-0', 'break-words']),
      )
      expect(h1.className.split(/\s+/)).not.toContain('truncate')
      expect(
        (h1.parentElement as HTMLElement).className.split(/\s+/),
      ).not.toContain('overflow-hidden')
      unmount()
    }
  })

  // The filter card + evidence card + self-audit strip sat above the ledger and put the
  // first row at y=569. The filters are the query in the top bar's search now, so nothing
  // but the header and the 48 px strip stands above the entries.
  it('keeps the filters in the query field, not a card above the ledger', async () => {
    api.list.mockResolvedValue({ items: [ev1], has_more: false })
    wrap(<AuditView />)
    const row = await screen.findByRole('button', { name: /^Agent create by/ })
    expect(row.closest('ul[aria-label="Ledger entries"]')).toBeTruthy()
    expect(screen.queryByRole('textbox', { name: 'Actor' })).toBeNull()
    expect(screen.getByRole('textbox', { name: 'Ledger query' })).toBeVisible()
    const chrome = screen
      .getByRole('heading', { level: 1 })
      .closest('[data-slot="work-chrome"]')
    expect(chrome).toBeTruthy()
  })

  // ⛔ AND ON A PHONE EVERY ONE OF THOSE CONTROLS IS BEHIND THE DISCLOSURE, WHICH THE ROW
  //    USED TO CLIP. The row was `h-9 … overflow-hidden` and the panel opens `absolute
  //    top-full` inside it: measured at 390×844, the panel's box ran y 98→244 inside a row
  //    that ended at 100, `elementFromPoint` at its centre answered a `td`, and the scope
  //    select, the filters, the saved views and the export were unreachable. The walk is
  //    over CLASSES because jsdom loads no stylesheet (`@/test/clipping`); the computed
  //    overflow and the hit test are measured by `code-r4/probe/panel.mjs`.
  it('phone: nothing between the open panel and the page clips it', async () => {
    api.list.mockResolvedValue({ items: [ev1], has_more: false })
    stubViewportWidth(390)
    wrap(<AuditView />)
    const toggle = await screen.findByTestId('page-actions-toggle')
    await userEvent.click(toggle)
    expect(toggle).toHaveAttribute('aria-expanded', 'true')
    const panel = document.querySelector(
      '[data-slot="page-actions"]',
    ) as HTMLElement
    // Joined, so a failure NAMES the element that cut the panel on its one line.
    expect(clippingAncestors(panel).join(' | ')).toBe('')
    expect(
      within(panel).getByRole('button', { name: 'Apply saved audit view' }),
    ).toBeInTheDocument()
  })
})

// acceptance / acceptance: the page read as the ledger's own bookkeeping ("authorization_decision.record"
// pairs, reads); a sign-in or a new person had no words of its own.
describe('AuditView — what happened, in words', () => {
  it('names what happened and folds the ledger records until asked', async () => {
    const signIn: AuditEventDTO = {
      ...ev1,
      id: 'evt-signin',
      seq: 14,
      action: 'auth.login',
      actor: 'user:alice',
    }
    const evidence: AuditEventDTO = {
      ...ev2,
      id: 'evt-evidence',
      seq: 15,
      action: 'authorization_decision.record',
    }
    api.list.mockResolvedValue({
      items: [ev1, ev2, signIn, evidence],
      has_more: false,
    })
    wrap(<AuditView />)
    expect(
      await screen.findByRole('button', { name: /^Signed in by/ }),
    ).toBeInTheDocument()
    expect(entryTexts()).toContainEqual(expect.stringMatching(/^Agent create/))
    expect(entryTexts().join()).not.toMatch(/Authorization decision recorded/)
    expect(entryTexts().join()).not.toMatch(/Audit read/)
    expect(screen.getByText('Internal records hidden: 2')).toBeInTheDocument()
    await userEvent.click(
      screen.getByRole('button', { name: 'Show internal records' }),
    )
    expect(entryTexts().join()).toMatch(/Authorization decision recorded/)
    expect(entryTexts().join()).toMatch(/Audit read/)
  })
})

const EMPTY_TITLE = 'No evidence yet'

// Review of the fold: a page of internal records only folded to nothing, and the table said the
// ledger was empty.
describe('AuditView — a page of internal records only', () => {
  it('says the loaded events are internal, offers them and more, and does not claim an empty ledger', async () => {
    // The newest window of a long ledger holds only internal records; older ones exist.
    api.recent.mockResolvedValue({ items: [], head_seq: 500 })
    api.list.mockResolvedValue({ items: [ev2], has_more: false })
    wrap(<AuditView />)
    expect(
      await screen.findByText(
        'Everything loaded so far is an internal record.',
      ),
    ).toBeInTheDocument()
    expect(screen.queryByText(EMPTY_TITLE)).toBeNull()
    expect(
      screen.getByRole('button', { name: 'Show internal records' }),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: /load more/i }),
    ).toBeInTheDocument()
  })

  it('still says the ledger is empty when nothing was loaded', async () => {
    api.list.mockResolvedValue({ items: [], has_more: false })
    wrap(<AuditView />)
    expect(await screen.findByText(EMPTY_TITLE)).toBeInTheDocument()
  })
})

describe('AuditView — ledger list', () => {
  it('renders each entry as a sentence with who acted and its hash', async () => {
    api.list.mockResolvedValue({ items: [ev1, ev2], has_more: false })
    wrap(<AuditView />)

    const row1 = (
      await screen.findByRole('button', { name: 'Agent create by A member' })
    ).closest('li')!
    expect(within(row1).getByText('A member')).toBeInTheDocument()
    // The hash is shown as its short fingerprint; the whole value is on its title.
    expect(within(row1).getByText('bbbbbb')).toHaveAttribute('title', ev1.hash)

    // A system-actor event with no actor string is said by its kind. A read is the
    // ledger's own bookkeeping, folded away until asked for.
    await userEvent.click(
      screen.getByRole('button', { name: 'Show internal records' }),
    )
    expect(
      screen.getByRole('button', { name: 'Audit read by System' }),
    ).toBeInTheDocument()
  })

  it('leads with a readable policy action and keeps its exact event code in the entry', async () => {
    const user = userEvent.setup()
    api.list.mockResolvedValue({
      items: [
        {
          ...ev1,
          action: 'governance.policy.create',
          target_kind: 'core.policy',
        },
      ],
      has_more: false,
    })
    wrap(<AuditView />)
    const action = await screen.findByRole('button', {
      name: /^Policy created by/,
    })
    expect(screen.queryByText('governance.policy.create')).toBeNull()
    await user.click(action)
    const entry = await screen.findByRole('complementary', { name: 'Entry 12' })
    expect(
      within(entry).getByText('governance.policy.create'),
    ).toBeInTheDocument()
  })

  it('lists the newest events first and steps back one window for older ones', async () => {
    api.recent.mockResolvedValue({ items: [], head_seq: 13 })
    api.list.mockResolvedValue({ items: [ev1, ev2], has_more: false })
    wrap(<AuditView />)

    expect(
      await screen.findByRole('button', { name: /^Agent create/ }),
    ).toBeInTheDocument()
    // The last window of the ledger, read forward and shown newest first.
    expect(api.list).toHaveBeenCalledWith({ from: 1, limit: 100 })
    await userEvent.click(
      screen.getByRole('button', { name: 'Show internal records' }),
    )
    const rows = entryTexts()
    expect(rows[0]).toMatch(/Audit read/)
    expect(rows[1]).toMatch(/Agent create/)
  })

  it('sends the typed filters as validated server-side filters and RFC3339 UTC bounds', async () => {
    wrap(<AuditView />)
    const user = userEvent.setup()
    await user.type(
      await screen.findByRole('textbox', { name: 'Ledger query' }),
      'person:user:alice action:agent. since:2026-07-24T12:30{Enter}',
    )

    await waitFor(() =>
      expect(api.list).toHaveBeenLastCalledWith({
        from: 1,
        limit: 100,
        actor: 'user:alice',
        action: 'agent.',
        // A bound without an offset is read as UTC: a link means one window for all.
        since: '2026-07-24T12:30:00.000Z',
      }),
    )
  })

  it('continues a sparse filtered scan with next_from and shows its honest status', async () => {
    window.history.replaceState(null, '', '/audit?action=agent.')
    api.list.mockImplementation(({ from }: { from?: number }) =>
      from === 1
        ? Promise.resolve({
            items: [ev1],
            next_from: 500,
            scan_complete: false,
            has_more: true,
          })
        : Promise.resolve({
            items: [],
            next_from: 700,
            scan_complete: true,
            has_more: false,
          }),
    )
    wrap(<AuditView />)

    expect(
      await screen.findByText(
        /Scanned up to seq 499 — not the full ledger; Load more continues the scan/,
      ),
    ).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Load more' }))

    await waitFor(() =>
      expect(api.list).toHaveBeenCalledWith({
        from: 500,
        limit: 100,
        action: 'agent.',
      }),
    )
  })

  it('slides in only the events that arrive at the head, never the first page', async () => {
    // The newest-first view: the head read refetches when the tab becomes
    // visible again, the window is read again, and the two events recorded since
    // land above the rows already on screen.
    const ev3 = {
      ...ev1,
      id: 'evt-0000000000000003',
      seq: 14,
      action: 'person.invite',
    }
    const ev4 = {
      ...ev1,
      id: 'evt-0000000000000004',
      seq: 15,
      action: 'policy.create',
    }
    api.recent
      .mockResolvedValueOnce({ items: [], head_seq: 13 })
      .mockResolvedValue({ items: [], head_seq: 15 })
    api.list.mockImplementation(() =>
      Promise.resolve({
        items:
          api.recent.mock.calls.length > 1 ? [ev1, ev2, ev3, ev4] : [ev1, ev2],
        has_more: false,
      }),
    )
    wrap(<AuditView />)

    const firstRow = (await entry(/^Agent create/)).closest('li')
    expect(firstRow).not.toHaveClass('feed-enter')

    // The browser's own event, as a tab coming back to the front sends it.
    fireEvent(document, new Event('visibilitychange', { bubbles: true }))

    // The sentences lead with readable action labels: "Person invite", "Policy create".
    const arrived = (await entry(/^Person invite/)).closest('li')
    expect(arrived).toHaveClass('feed-enter')
    expect(
      screen.getByRole('button', { name: /^Policy create/ }).closest('li'),
    ).toHaveClass('feed-enter')
    expect(
      screen.getByRole('button', { name: /^Agent create/ }).closest('li'),
    ).not.toHaveClass('feed-enter')

    // Once the slide has had time to finish, the marks clear: a virtualized row
    // that re-mounts later must not replay its entrance.
    await waitFor(
      () => {
        expect(
          screen.getByRole('button', { name: /^Person invite/ }).closest('li'),
        ).not.toHaveClass('feed-enter')
        expect(
          screen.getByRole('button', { name: /^Policy create/ }).closest('li'),
        ).not.toHaveClass('feed-enter')
      },
      { timeout: 3000 },
    )
  })

  it('never slides the older rows Load more brings', async () => {
    // An engine without head_seq pages FORWARD (the pre-acceptance path): Load more
    // appends the next page below the rows on screen.
    api.recent.mockResolvedValue({ items: [], head_seq: null })
    api.list.mockImplementation(({ from }: { from?: number }) =>
      from && from > 1
        ? Promise.resolve({
            items: [
              {
                ...ev1,
                id: 'evt-0000000000000003',
                seq: 14,
                action: 'person.invite',
              },
            ],
            has_more: false,
          })
        : Promise.resolve({ items: [ev1, ev2], has_more: true }),
    )
    wrap(<AuditView />)
    await entry(/^Agent create/)

    await userEvent.click(screen.getByRole('button', { name: 'Load more' }))
    expect((await entry(/^Person invite/)).closest('li')).not.toHaveClass(
      'feed-enter',
    )
  })

  it('never slides a row the fold reveals, even above the rows on screen', async () => {
    // Newest first: the internal "Audit read" (seq 13) is the newest row,
    // so showing internal records puts it on top of the feed.
    api.recent.mockResolvedValue({ items: [], head_seq: 13 })
    api.list.mockResolvedValue({ items: [ev1, ev2], has_more: false })
    wrap(<AuditView />)
    await entry(/^Agent create/)
    await userEvent.click(
      screen.getByRole('button', { name: 'Show internal records' }),
    )
    expect((await entry(/^Audit read/)).closest('li')).not.toHaveClass(
      'feed-enter',
    )
  })

  it('applies a saved view to URL-backed filter state', async () => {
    wrap(<AuditView />)
    await userEvent.click(
      await screen.findByRole('button', { name: 'Apply saved audit view' }),
    )

    await waitFor(() =>
      expect(api.list).toHaveBeenLastCalledWith({
        from: 1,
        limit: 100,
        actor: 'user:saved',
        action: 'policy.',
      }),
    )
    expect(navigate).toHaveBeenCalledWith(
      expect.objectContaining({ replace: true }),
    )
  })

  it('ignores invalid timestamp and scope values from the URL', async () => {
    window.history.replaceState(
      null,
      '',
      '/audit?actor=user%3Aalice&since=not-a-date&scope=other',
    )
    wrap(<AuditView />)

    await waitFor(() =>
      expect(api.list).toHaveBeenCalledWith({
        from: 1,
        limit: 100,
        actor: 'user:alice',
      }),
    )
    expect(api.systemList).not.toHaveBeenCalled()
  })
})

describe('AuditView — system ledger scope (superadmin)', () => {
  it('hides the scope toggle from non-superadmins', async () => {
    wrap(<AuditView />)
    await waitFor(() => expect(api.list).toHaveBeenCalled())
    expect(screen.queryByRole('combobox', { name: /ledger scope/i })).toBeNull()
  })

  it('lets a superadmin read the system chain (list-only, no verify/export)', async () => {
    auth.isSuperadmin = true
    api.systemList.mockResolvedValue({
      items: [{ ...ev1, action: 'user.create' }],
      has_more: false,
    })
    wrap(<AuditView />)

    await userEvent.click(
      screen.getByRole('combobox', { name: /ledger scope/i }),
    )
    await userEvent.click(
      await screen.findByRole('option', { name: /system ledger/i }),
    )

    expect(
      await screen.findByRole('button', { name: /^Person added by/ }),
    ).toBeInTheDocument()
    await waitFor(() => expect(api.systemList).toHaveBeenCalled())
    // The system chain is read-only here: verify/export controls are gone…
    expect(screen.queryByRole('button', { name: /verify chain/i })).toBeNull()
    // …replaced by the honest caveat.
    expect(screen.getByText(/read-only here/i)).toBeInTheDocument()
  })
})

describe('AuditView — the entry beside the list', () => {
  it('shows the signed fields and the chain-link fingerprints', async () => {
    api.list.mockResolvedValue({ items: [ev1], has_more: false })
    wrap(<AuditView />)
    await userEvent.click(
      await screen.findByRole('button', { name: /^Agent create by/ }),
    )
    const entry = await screen.findByRole('complementary', { name: 'Entry 12' })
    expect(
      within(entry).getByRole('heading', { name: 'Agent create by A member' }),
    ).toBeInTheDocument()
    expect(within(entry).getByText('12')).toBeInTheDocument()
    expect(within(entry).getByText('Previous hash')).toBeInTheDocument()
    expect(within(entry).getByText('Event hash')).toBeInTheDocument()
    // A signed event shows its PER-EVENT signature row. It is never called a
    // checkpoint: the engine separates the two signing domains on purpose
    // (core/audit/eventsig.go — `eventDomain` is "distinct from checkpointDomain by
    // design"), and one screen must not contradict Security > Forensics about it.
    expect(within(entry).getByText('Event signature')).toBeInTheDocument()
    expect(within(entry).queryByText(/checkpoint/i)).not.toBeInTheDocument()
    // prev_hash fingerprint is rendered truncated (head…tail).
    expect(within(entry).getByText(/aaaaaaaa…aaaaaa/)).toBeInTheDocument()
  })
})

describe('AuditView refuses an impossible ledger bound', () => {
  // The third copy of the same defect, in the view the rest of this wave copies
  // from: its own sanitiser accepted 2026-02-30 and queried the 2nd of March.
  // The shared parser existed by then; this view was simply never pointed at it.
  it('does not query the 2nd of March for the 30th of February', async () => {
    window.history.replaceState(null, '', '/audit?since=2026-02-30')
    wrap(<AuditView />)

    await waitFor(() => expect(api.list).toHaveBeenCalled())
    expect(api.list.mock.calls.at(-1)?.[0].since).toBeUndefined()
  })

  it('sends the minute its control shows, not the nanoseconds the URL carried', async () => {
    window.history.replaceState(
      null,
      '',
      '/audit?since=2026-07-24T12%3A30%3A45.123456789Z',
    )
    wrap(<AuditView />)

    await waitFor(() => expect(api.list).toHaveBeenCalled())
    // The control renders minutes; preserving nanosecond precision made two
    // visually identical links query different windows.
    expect(api.list.mock.calls.at(-1)?.[0].since).toBe(
      '2026-07-24T12:30:00.000Z',
    )
  })
})

describe('P3 — the time of an entry does not split', () => {
  /**
   * ⛔ THE DEFECT, measured in Chrome with real data: "6 minutes ago" rendered on THREE
   * lines when its slot narrowed. A relative time is atomic: its container declares
   * `whitespace-nowrap`, which is what the browser respects (jsdom does not lay out).
   *
   * THE MUTANT: remove `whitespace-nowrap` from the entry's time. It fails here.
   */
  it('the relative time of every entry declares that it does not split', async () => {
    api.list.mockResolvedValue({ items: [ev1], has_more: false })
    wrap(<AuditView />)
    const row = (
      await screen.findByRole('button', { name: /^Agent create by/ })
    ).closest('li')!
    const time = row.querySelector('time')?.parentElement as HTMLElement
    expect(time.className.split(/\s+/)).toContain('whitespace-nowrap')
  })
})

/**
 * THE LEDGER READS AS PEOPLE AND KINDS, NOT AS UUIDS.
 *
 * The census measured four raw identifiers on this route: the actor column wrote every
 * principal as `user:<uuid>`, and the target column led with `core.audit_event:` and a
 * whole uuid. Both are evidence and both stay — on `title`, in the short form that
 * matches a log line, and in the detail sheet. What changes is which of the two the
 * reader sees first.
 */
describe('AuditView — the reference a row names', () => {
  const evUser: AuditEventDTO = {
    ...ev1,
    actor: 'user:01a0b580-4f7e-79b5-a98c-32aa681a4502',
    target_kind: 'core.workspace',
    target_id: 'ws-1',
  }
  const evOpaque: AuditEventDTO = {
    ...ev2,
    // An action the view shows by default (a read is folded away, acceptance).
    action: 'evals.suite.update',
    actor: 'user:01a0b580-0000-0000-0000-000000000000',
    actor_kind: 'user',
    target_kind: 'evals.suite',
    target_id: '01a0b580-4d33-7e39-af6e-e35c160f03f3',
  }

  it('names the person who acted and the workspace that was acted on', async () => {
    api.list.mockResolvedValue({
      items: [evUser],
      has_more: false,
      head_seq: 12,
    })
    wrap(<AuditView />)
    expect(await screen.findByText('Alice Ng')).toBeInTheDocument()
    expect(await screen.findByText('Billing')).toBeInTheDocument()
    expect(
      screen.getByText('Alice Ng').closest('[title]')?.getAttribute('title'),
    ).toBe('user:01a0b580-4f7e-79b5-a98c-32aa681a4502')
  })

  it('leads an unnameable target with its kind and keeps the pair on the tooltip', async () => {
    api.list.mockResolvedValue({
      items: [evOpaque],
      has_more: false,
      head_seq: 13,
    })
    wrap(<AuditView />)
    const kind = await screen.findByText('Evals suite')
    expect(kind.closest('[title]')?.getAttribute('title')).toBe(
      'evals.suite: 01a0b580-4d33-7e39-af6e-e35c160f03f3',
    )
    expect(screen.queryByText('01a0b580')).toBeNull()
  })

  it('opens no cell with a raw identifier', async () => {
    api.list.mockResolvedValue({
      items: [evUser, evOpaque],
      has_more: false,
      head_seq: 13,
    })
    wrap(<AuditView />)
    await screen.findByText('Alice Ng')
    // The census rule, asserted where the census measures it: a cell may CARRY an
    // identifier, and may not OPEN with one.
    const raw = /^[0-9a-f]{8}-|^ppf_|^sess-/
    const list = screen.getByRole('list', { name: 'Ledger entries' })
    for (const value of within(list).getAllByRole('button')) {
      expect((value.textContent ?? '').trim()).not.toMatch(raw)
    }
  })

  it('uses a member label and preserves the ledger actor in Details when the roster may not be read', async () => {
    auth.can = ((permission: string) =>
      permission !== 'user:read') as unknown as typeof auth.can
    api.list.mockResolvedValue({
      items: [evUser],
      has_more: false,
      head_seq: 12,
    })
    wrap(<AuditView />)
    expect(await screen.findByText('A member')).toBeInTheDocument()
    expect(screen.getByText('A member')).toHaveAttribute('title', evUser.actor)
    expect(screen.queryByText(evUser.actor)).toBeNull()
    expect(directory.listMembers).not.toHaveBeenCalled()
  })
})

it('hides cached member names in both the Audit row and the entry when roster permission leaves', async () => {
  const event = { ...ev1, actor: 'user:01a0b580-4f7e-79b5-a98c-32aa681a4502' }
  api.list.mockResolvedValue({ items: [event], has_more: false })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const content = () => (
    <QueryClientProvider client={client}>
      <TooltipProvider>
        <SidePanelProvider>
          <AuditView />
          <SidePanelHost />
        </SidePanelProvider>
      </TooltipProvider>
    </QueryClientProvider>
  )
  const mounted = render(content())
  await screen.findByText('Alice Ng')
  auth.can = (permission) => permission !== 'user:read'
  mounted.rerender(content())
  expect(screen.queryByText('Alice Ng')).toBeNull()
  expect(screen.getByText('A member')).toBeVisible()
  await userEvent.click(
    screen.getByRole('button', { name: /^Agent create by/ }),
  )
  expect(
    within(
      await screen.findByRole('complementary', { name: 'Entry 12' }),
    ).queryByText(/Alice Ng/),
  ).toBeNull()
})

describe.skipIf(AUDIT_EVIDENCE_AVAILABLE)(
  'AuditView — Community ledger',
  () => {
    it('offers reads without Business verification or export controls', async () => {
      api.list.mockResolvedValue({ items: [ev1], has_more: false })
      wrap(<AuditView />)
      await waitFor(() => expect(api.list).toHaveBeenCalled())
      expect(screen.queryByRole('button', { name: /verify chain/i })).toBeNull()
      expect(screen.queryByRole('button', { name: /^export$/i })).toBeNull()
      expect(api.verify).not.toHaveBeenCalled()
      expect(exp.fetchAuditExport).not.toHaveBeenCalled()
      await userEvent.click(await entry(/^Agent create/))
      const panel = await screen.findByRole('complementary', {
        name: 'Entry 12',
      })
      expect(
        within(panel).getByRole('group', { name: 'Position in the chain' }),
      ).toBeVisible()
      expect(
        within(panel).queryByRole('button', { name: /verify chain/i }),
      ).toBeNull()
      expect(api.verify).not.toHaveBeenCalled()
    })
  },
)
