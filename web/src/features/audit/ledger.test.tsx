// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE LEDGER PLACE (issue #2554): the query as tokens over the audit API's own filters,
// the histogram strip, the entries as sentences grouped by session with the flagged one
// first, and the entry with its place in the chain.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  act,
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
import {
  TopbarSearchProvider,
  TopbarSearchSlot,
} from '@/components/layout/topbar-search'
import type { AuditEventDTO } from '@/lib/api/types'

// Hours are painted in local time; the test reads them in UTC.
process.env.TZ = 'UTC'

vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
  Toaster: () => null,
}))

const auth = vi.hoisted(() => ({
  activeTenant: 't1' as string | null,
  isSuperadmin: false,
  can: (_permission: string): boolean => true,
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => auth }))

vi.mock('@tanstack/react-router', () => ({
  useRouterState: () => window.location.search,
  useNavigate: () => vi.fn(),
}))
vi.mock('@/features/saved-views', () => ({ SavedViewsMenu: () => null }))

const MARC = 'user:01a0b580-4f7e-79b5-a98c-32aa681a4502'
const directory = vi.hoisted(() => ({
  listMembers: vi.fn(),
  listWorkspaces: vi.fn(),
}))
vi.mock('@/features/console/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/features/console/api')>()
  return { ...actual, consoleApi: { ...actual.consoleApi, ...directory } }
})

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

import { AuditView } from './audit-view'
import { bucketsOf } from './ledger'

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

const hash = (c: string) => c.repeat(64)
function entry(seq: number, over: Partial<AuditEventDTO>): AuditEventDTO {
  return {
    id: `evt-${seq}`,
    seq,
    occurred_at: '2026-10-20T16:00:00Z',
    actor: MARC,
    actor_kind: 'user',
    action: 'sessions.run.launched',
    target_kind: 'sessions.run',
    target_id: 'run-a',
    prev_hash: hash(String(seq - 1).slice(-1)),
    hash: hash(String(seq).slice(-1)),
    sig: 'c2ln',
    ...over,
  }
}
const launchedA = entry(41, { occurred_at: '2026-10-20T15:10:00Z' })
const stoppedA = entry(43, {
  action: 'sessions.run.stopping',
  occurred_at: '2026-10-20T16:20:00Z',
})
const launchedB = entry(42, {
  target_id: 'run-b',
  occurred_at: '2026-10-20T15:50:00Z',
})
const asked = entry(44, {
  action: 'governance.approval.create',
  target_kind: 'governance.approval',
  target_id: 'apr-1',
  occurred_at: '2026-10-20T16:30:00Z',
})
const MATCHING = [launchedA, launchedB, stoppedA, asked]

const NOW = new Date('2026-10-20T16:42:31Z')

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['Date'] })
  vi.setSystemTime(NOW)
  for (const fn of Object.values(api)) fn.mockReset()
  auth.can = () => true
  directory.listMembers.mockResolvedValue({
    items: [
      { user_id: MARC.slice(5), display_name: 'Marc Vidal', email: 'm@n.test' },
    ],
    has_more: false,
  })
  directory.listWorkspaces.mockResolvedValue({ items: [], has_more: false })
  governance.listApprovals.mockResolvedValue({
    items: [{ id: 'apr-1', status: 'pending' }],
    has_more: false,
  })
  governance.listPolicies.mockResolvedValue({ items: [] })
  sessions.live.mockResolvedValue({ items: [], has_more: false })
  window.history.replaceState(null, '', '/audit')
  api.recent.mockResolvedValue({ items: [], head_seq: 44 })
  api.list.mockResolvedValue({ items: MATCHING, has_more: false })
  api.verify.mockResolvedValue({
    ok: true,
    chain: { ok: true, checked: 44, break_at: 0, reason: '' },
    checkpoints: {
      ok: true,
      status: 'ok',
      count: 1,
      latest_attested_seq: 40,
      first_bad_seq: 0,
      reason: '',
    },
  })
})
afterEach(() => {
  vi.useRealTimers()
  vi.clearAllMocks()
  window.history.replaceState(null, '', '/')
})

/** The entry sentences in the order the list paints them. */
function sentences() {
  return within(screen.getByRole('list', { name: 'Ledger entries' }))
    .getAllByRole('button')
    .filter((b) => b.dataset.slot === 'entry')
    .map((b) => b.textContent)
}

describe('the Ledger query', () => {
  it('keeps the word an IME Enter commits, and reads nothing yet', async () => {
    wrap(<AuditView />)
    const query = await screen.findByRole('textbox', { name: 'Ledger query' })
    await screen.findAllByRole('button', { name: /Session started/ })
    const calls = api.list.mock.calls.length
    fireEvent.change(query, { target: { value: 'person:マルク' } })
    fireEvent.keyDown(query, { key: 'Enter', isComposing: true })
    expect(query).toHaveValue('person:マルク')
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(api.list.mock.calls.length).toBe(calls)
  })

  // The field stands where the shell search stood: its hint reads the same key (topbar.layout.test.tsx).
  it.each([
    ['Linux x86_64', 'Ctrl+K'],
    ['Win32', 'Ctrl+K'],
    ['MacIntel', '⌘K'],
  ])('on %s reads the palette key as %s', async (platform, hint) => {
    Object.defineProperty(window.navigator, 'platform', {
      value: platform,
      configurable: true,
    })
    try {
      wrap(<AuditView />)
      const commands = await screen.findByRole('button', {
        name: 'Search and commands',
      })
      // Drawn, not text, as the shell search's hint (WCAG 2.5.3).
      expect(commands.querySelector('kbd')).toHaveAttribute('data-keys', hint)
      expect(commands.textContent).toBe('')
    } finally {
      delete (window.navigator as { platform?: string }).platform
    }
  })

  it('asks the API the same filters, and shows the same entries, typed or from the URL', async () => {
    // The engine's own matching (handlers_audit.go): actor exact, action a prefix.
    api.list.mockImplementation(
      async (p: { actor?: string; action?: string }) => ({
        items: MATCHING.filter(
          (e) =>
            (!p.actor || e.actor === p.actor) &&
            (!p.action || e.action.startsWith(p.action)),
        ),
        has_more: false,
      }),
    )
    window.history.replaceState(
      null,
      '',
      `/audit?actor=${encodeURIComponent(MARC)}&action=sessions.&since=7d`,
    )
    const fromUrl = wrap(<AuditView />)
    await screen.findAllByRole('button', { name: /Session started/ })
    const urlCall = api.list.mock.calls.at(-1)?.[0]
    const urlRows = sentences()
    // The filter took effect: the approval request is not a `sessions.` action.
    expect(urlRows).toHaveLength(3)
    fromUrl.unmount()

    api.list.mockClear()
    window.history.replaceState(null, '', '/audit')
    wrap(<AuditView />)
    const user = userEvent.setup()
    const query = await screen.findByRole('textbox', { name: 'Ledger query' })
    await user.type(query, 'person:marc action:sessions. since:7d{Enter}')
    await waitFor(() =>
      expect(api.list).toHaveBeenLastCalledWith({
        from: 1,
        limit: 100,
        actor: MARC,
        action: 'sessions.',
        since: '2026-10-13T16:42:00.000Z',
      }),
    )
    expect(api.list.mock.calls.at(-1)?.[0]).toEqual(urlCall)
    await waitFor(() => expect(sentences()).toEqual(urlRows))
  })

  it('asks every filter the same way, typed or from the URL, and paints each as its token', async () => {
    // All seven API filters at once. Both routes are held to the one request, so a
    // filter lost on both sides (a key missing from FILTER_KEYS) fails too.
    const request = {
      from: 1,
      limit: 100,
      actor: MARC,
      action: 'sessions.',
      target_kind: 'sessions.run',
      target_id: 'run-a',
      since: '2026-10-13T16:42:00.000Z',
      until: '2026-10-20T16:00:00.000Z',
      q: 'launched',
    }
    window.history.replaceState(
      null,
      '',
      `/audit?actor=${encodeURIComponent(MARC)}&action=sessions.&target_kind=sessions.run&target_id=run-a&since=7d&until=2026-10-20T16%3A00%3A00Z&q=launched`,
    )
    const fromUrl = wrap(<AuditView />)
    await waitFor(() => expect(api.list).toHaveBeenLastCalledWith(request))
    for (const token of [
      'person:Marc Vidal',
      'action:sessions.',
      'kind:sessions.run',
      'target:run-a',
      'since:7d',
      'until:2026-10-20T16:00:00.000Z',
      'launched',
    ]) {
      expect(
        await screen.findByRole('button', { name: `Remove ${token}` }),
      ).toBeVisible()
    }
    fromUrl.unmount()

    api.list.mockClear()
    window.history.replaceState(null, '', '/audit')
    wrap(<AuditView />)
    const user = userEvent.setup()
    const query = await screen.findByRole('textbox', { name: 'Ledger query' })
    await user.type(
      query,
      'person:marc action:sessions. kind:sessions.run target:run-a since:7d until:2026-10-20T16:00:00Z launched{Enter}',
    )
    await waitFor(() => expect(api.list).toHaveBeenLastCalledWith(request))
  })

  it('reads a day on the URL as the whole day, and drops a day that does not exist', async () => {
    window.history.replaceState(
      null,
      '',
      '/audit?since=2026-10-19&until=2026-10-20',
    )
    const days = wrap(<AuditView />)
    await waitFor(() =>
      expect(api.list).toHaveBeenLastCalledWith({
        from: 1,
        limit: 100,
        since: '2026-10-19T00:00:00.000Z',
        until: '2026-10-21T00:00:00.000Z',
      }),
    )
    expect(
      await screen.findByRole('button', { name: 'Remove until:2026-10-20' }),
    ).toBeVisible()
    days.unmount()

    api.list.mockClear()
    window.history.replaceState(null, '', '/audit?until=2026-02-30')
    wrap(<AuditView />)
    await waitFor(() => expect(api.list).toHaveBeenCalled())
    expect(api.list.mock.lastCall?.[0]).not.toHaveProperty('until')
    expect(
      screen.queryByRole('button', { name: /^Remove until:/ }),
    ).not.toBeInTheDocument()
  })

  it('shows each filter as a token that removes itself', async () => {
    window.history.replaceState(
      null,
      '',
      `/audit?actor=${encodeURIComponent(MARC)}&since=7d`,
    )
    wrap(<AuditView />)
    const user = userEvent.setup()
    // The key and the value are two spans, so the key can give way first (the room
    // itself is a layout the live journey measures, not jsdom).
    const search = await screen.findByRole('search')
    expect(await within(search).findByText('Marc Vidal')).toBeVisible()
    expect(within(search).getByText('person:')).toBeVisible()
    expect(within(search).getByText('7d')).toBeVisible()
    await user.click(
      await screen.findByRole('button', { name: 'Remove person:Marc Vidal' }),
    )
    await waitFor(() =>
      expect(api.list).toHaveBeenLastCalledWith({
        from: 1,
        limit: 100,
        since: '2026-10-13T16:42:00.000Z',
      }),
    )
    expect(
      screen.getByRole('button', { name: 'Remove since:7d' }),
    ).toBeVisible()
    // The removed token's button is gone; the keyboard stays in the query.
    expect(screen.getByRole('textbox', { name: 'Ledger query' })).toHaveFocus()
  })

  it('adds a value from a row to the query', async () => {
    wrap(<AuditView />)
    const user = userEvent.setup()
    const add = await screen.findAllByRole('button', {
      name: 'Add person:Marc Vidal to the query',
    })
    await user.click(add[0]!)
    await waitFor(() =>
      expect(api.list).toHaveBeenLastCalledWith({
        from: 1,
        limit: 100,
        actor: MARC,
      }),
    )
  })

  it('says what it cannot filter and asks nothing new', async () => {
    wrap(<AuditView />)
    const user = userEvent.setup()
    const query = await screen.findByRole('textbox', { name: 'Ledger query' })
    await waitFor(() => expect(api.list).toHaveBeenCalled())
    const calls = api.list.mock.calls.length
    // The person next to the refused key is not applied alone either.
    await user.type(query, 'person:marc tool:codex{Enter}')
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'tool:codex is not a ledger filter',
    )
    expect(query).toHaveValue('person:marc tool:codex')
    expect(api.list.mock.calls.length).toBe(calls)
  })

  it('says a name fits several people, not that it fits nobody', async () => {
    directory.listMembers.mockResolvedValue({
      items: [
        {
          user_id: MARC.slice(5),
          display_name: 'Marc Vidal',
          email: 'm@n.test',
        },
        { user_id: 'u-marc-2', display_name: 'Marc Soto', email: 'ms@n.test' },
      ],
      has_more: false,
    })
    wrap(<AuditView />)
    const user = userEvent.setup()
    const query = await screen.findByRole('textbox', { name: 'Ledger query' })
    await screen.findAllByRole('button', { name: /by Marc Vidal/ })
    const calls = api.list.mock.calls.length
    await user.type(query, 'person:marc{Enter}')
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'person:marc fits several people. Use the full name, an email or user:<id>.',
    )
    expect(api.list.mock.calls.length).toBe(calls)
  })

  it('says names cannot be looked up here where the people list cannot be read', async () => {
    const can = auth.can
    auth.can = (permission: string) => permission !== 'user:read'
    try {
      wrap(<AuditView />)
      const user = userEvent.setup()
      const query = await screen.findByRole('textbox', {
        name: 'Ledger query',
      })
      await screen.findAllByRole('button', { name: /Session started/ })
      const calls = api.list.mock.calls.length
      await user.type(query, 'person:marc{Enter}')
      expect(await screen.findByRole('alert')).toHaveTextContent(
        'person:marc cannot be looked up here. Use user:<id>.',
      )
      expect(api.list.mock.calls.length).toBe(calls)
    } finally {
      auth.can = can
    }
  })

  it('says the same where the people list is longer than one page', async () => {
    directory.listMembers.mockResolvedValue({
      items: [
        {
          user_id: MARC.slice(5),
          display_name: 'Marc Vidal',
          email: 'm@n.test',
        },
      ],
      has_more: true,
    })
    wrap(<AuditView />)
    const user = userEvent.setup()
    const query = await screen.findByRole('textbox', { name: 'Ledger query' })
    await screen.findAllByRole('button', { name: /by Marc Vidal/ })
    const calls = api.list.mock.calls.length
    await user.type(query, 'person:marc{Enter}')
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'person:marc cannot be looked up here. Use user:<id>.',
    )
    expect(api.list.mock.calls.length).toBe(calls)
  })
})

describe('where the Ledger query sits', () => {
  function shell() {
    return wrap(
      <TopbarSearchProvider>
        <header>
          <TopbarSearchSlot fallback={<button type="button">Search</button>} />
        </header>
        <main>
          <AuditView />
        </main>
      </TopbarSearchProvider>,
    )
  }
  let media: { mockRestore: () => void } | undefined
  afterEach(() => media?.mockRestore())
  const wide = (on: boolean) =>
    (media = vi.spyOn(window, 'matchMedia').mockImplementation(
      (query: string) =>
        ({
          matches: on && query === '(min-width: 1100px)',
          media: query,
          addEventListener: () => {},
          removeEventListener: () => {},
        }) as unknown as MediaQueryList,
    ))

  it('is the top bar search where the bar has the room of one', async () => {
    wide(true)
    shell()
    const query = await screen.findByRole('textbox', { name: 'Ledger query' })
    expect(query.closest('header')).not.toBeNull()
    expect(screen.queryByRole('button', { name: 'Search' })).toBeNull()
  })

  it('moves into the page below 1100 px, where the bar keeps its search', async () => {
    wide(false)
    shell()
    const query = await screen.findByRole('textbox', { name: 'Ledger query' })
    expect(query.closest('main')).not.toBeNull()
    expect(screen.getByRole('button', { name: 'Search' })).toBeVisible()
  })

  it('keeps what was typed, and what it said, when the window crosses 1100 px', async () => {
    let on = true
    const changes = new Set<() => void>()
    media = vi.spyOn(window, 'matchMedia').mockImplementation(
      (query: string) =>
        ({
          get matches() {
            return on && query === '(min-width: 1100px)'
          },
          media: query,
          addEventListener: (_: string, f: () => void) => changes.add(f),
          removeEventListener: (_: string, f: () => void) => changes.delete(f),
        }) as unknown as MediaQueryList,
    )
    shell()
    const user = userEvent.setup()
    const inBar = await screen.findByRole('textbox', { name: 'Ledger query' })
    await user.type(inBar, 'tool:codex{Enter}')
    await screen.findByRole('alert')
    // Zoomed in, or a tablet turned: the field moves from the bar into the page.
    act(() => {
      on = false
      changes.forEach((f) => f())
    })
    const inPage = screen.getByRole('textbox', { name: 'Ledger query' })
    expect(inPage.closest('main')).not.toBeNull()
    expect(inPage).toHaveValue('tool:codex')
    expect(screen.getByRole('alert')).toHaveTextContent(
      'tool:codex is not a ledger filter',
    )
  })
})

describe('the Ledger entries', () => {
  it('reads as sentences grouped by session, the request that needs a person first', async () => {
    wrap(<AuditView />)
    const list = await screen.findByRole('list', { name: 'Ledger entries' })
    await waitFor(() =>
      expect(within(list).getAllByText('Needs you')[0]).toBeVisible(),
    )
    const groups = within(list)
      .getAllByRole('heading', { level: 2 })
      .map((h) => h.textContent)
    expect(groups[0]).toMatch(/^Needs you/)
    // Session a holds two entries (its newest is newer than b's), then session b.
    expect(groups.slice(1)).toEqual([
      expect.stringMatching(/run-a.*2 entries/),
      expect.stringMatching(/run-b.*1 entry/),
    ])
    expect(sentences()[0]).toMatch(/Approval requested by Marc Vidal/)
  })

  it('lists every entry newest first when grouping is off', async () => {
    wrap(<AuditView />)
    const user = userEvent.setup()
    await user.click(await screen.findByRole('radio', { name: 'Every entry' }))
    expect(sentences()).toEqual([
      expect.stringMatching(/Approval requested/),
      expect.stringMatching(/Session stop requested/),
      expect.stringMatching(/Session started/),
      expect.stringMatching(/Session started/),
    ])
  })

  it('paints one orange mark however many requests wait for a person', async () => {
    const askedAgain = entry(45, {
      action: 'governance.approval.create',
      target_kind: 'governance.approval',
      target_id: 'apr-2',
      occurred_at: '2026-10-20T16:35:00Z',
    })
    api.list.mockResolvedValue({
      items: [...MATCHING, askedAgain],
      has_more: false,
    })
    governance.listApprovals.mockResolvedValue({
      items: [
        { id: 'apr-1', status: 'pending' },
        { id: 'apr-2', status: 'pending' },
      ],
      has_more: false,
    })
    wrap(<AuditView />)
    const list = await screen.findByRole('list', { name: 'Ledger entries' })
    await waitFor(() =>
      expect(sentences().slice(0, 2)).toEqual([
        expect.stringMatching(/Approval requested/),
        expect.stringMatching(/Approval requested/),
      ]),
    )
    // The accent is the one thing that needs a person: the group, once.
    const orange = [...list.querySelectorAll('*')].filter((el) =>
      /\b(bg|text|border)-accent/.test(el.getAttribute('class') ?? ''),
    )
    expect(orange).toHaveLength(1)
    // That mark is the list's own, so every list holds items only (axe `list`).
    for (const ul of list.querySelectorAll('ul'))
      for (const child of ul.children) expect(child.tagName).toBe('LI')
  })
})

describe('the Ledger histogram', () => {
  it('filters to the hour of a bar', async () => {
    wrap(<AuditView />)
    const user = userEvent.setup()
    const strip = await screen.findByRole('group', {
      name: /Entries per hour/,
    })
    await user.click(
      await within(strip).findByRole('button', { name: /15:00.*2 entries/ }),
    )
    await waitFor(() =>
      expect(api.list).toHaveBeenLastCalledWith({
        from: 1,
        limit: 100,
        since: '2026-10-20T15:00:00.000Z',
        until: '2026-10-20T16:00:00.000Z',
      }),
    )
    // The window now ends at that hour, and the strip's end says so instead of "Now".
    const figure = (
      await screen.findByRole('group', { name: /Entries per hour/ })
    ).closest('figure')!
    await waitFor(() =>
      expect(within(figure).queryByText('Now')).not.toBeInTheDocument(),
    )
    // The picked bar starts the new window under the same key: the keyboard stays on it.
    expect(figure).toContainElement(document.activeElement as HTMLElement)
  })

  it('counts every loaded entry, the ones older than a day included', async () => {
    api.list.mockResolvedValue({
      items: [...MATCHING, entry(40, { occurred_at: '2026-10-17T09:00:00Z' })],
      has_more: false,
    })
    wrap(<AuditView />)
    const strip = await screen.findByRole('group', { name: /Entries per/ })
    await waitFor(() => {
      const counted = within(strip)
        .getAllByRole('button')
        .reduce(
          (n, b) =>
            n + Number(/(\d+) entr/.exec(b.getAttribute('aria-label')!)![1]),
          0,
        )
      expect(counted).toBe(5)
    })
    // The bar of the current hour says so; colour is not its only mark.
    expect(
      within(strip).getAllByRole('button', { name: /, Now$/ }),
    ).toHaveLength(1)
  })

  it('draws a window that only ends, until: alone', async () => {
    // Two days back: a window counted from now would end before it starts.
    window.history.replaceState(null, '', '/audit?until=2026-10-18T00:00')
    wrap(<AuditView />)
    const strip = await screen.findByRole('group', { name: /Entries per hour/ })
    expect(within(strip).getAllByRole('button').length).toBeGreaterThan(0)
  })

  it('starts a window of years at the oldest entry loaded, not at its first week', () => {
    const at = (iso: string) =>
      ({ occurred_at: iso }) as unknown as AuditEventDTO
    const { unit, buckets } = bucketsOf(
      [at('2026-10-20T15:10:00Z'), at('2026-10-19T09:00:00Z')],
      new Date('2000-01-01T00:00:00Z'),
      new Date('2026-10-20T16:42:00Z'),
    )
    expect(unit).toBe('hour')
    expect(buckets[0]!.count).toBe(1)
    expect(buckets.reduce((n, b) => n + b.count, 0)).toBe(2)
  })

  it('draws at most 200 bars for a window of centuries with nothing loaded yet', () => {
    // since:0001-01-01 typed or linked: one button per week would be about 105,000.
    const to = new Date('2026-10-20T16:42:00Z')
    const { unit, buckets } = bucketsOf(
      [],
      new Date('0001-01-01T00:00:00Z'),
      to,
    )
    expect(unit).toBe('week')
    expect(buckets.length).toBeLessThanOrEqual(200)
    expect(buckets.at(-1)!.end.getTime()).toBeGreaterThan(to.getTime())
  })

  it('ends the strip now when until: lies ahead', async () => {
    // Nothing can be recorded after now; bars there would only stretch the window.
    window.history.replaceState(null, '', '/audit?until=2027-06-01T00:00')
    wrap(<AuditView />)
    const strip = await screen.findByRole('group', { name: /Entries per hour/ })
    expect(
      within(strip).getAllByRole('button', { name: /, Now$/ }),
    ).toHaveLength(1)
  })
})

describe('the Ledger entry', () => {
  it('shows its place in the chain: the previous, this and the next hash', async () => {
    wrap(<AuditView />)
    const user = userEvent.setup()
    await screen.findAllByRole('button', {
      name: /Session started by Marc Vidal/,
    })
    // launchedB is entry 42: its neighbours 41 and 43 are loaded.
    const b = screen
      .getAllByRole('button', { name: /Session started by Marc Vidal/ })
      .find((el) => el.closest('li')?.dataset.seq === '42')
    await user.click(b!)
    const panel = await screen.findByRole('complementary', {
      name: 'Entry 42',
    })
    const chain = within(panel).getByRole('group', {
      name: 'Position in the chain',
    })
    expect(chain).toHaveTextContent(/Previous · 41.*111111/)
    expect(chain).toHaveTextContent(/This entry · 42.*222222/)
    expect(chain).toHaveTextContent(/Next · 43.*333333/)
    await user.click(
      within(panel).getByRole('button', { name: 'Verify chain' }),
    )
    expect(await within(panel).findByText('Chain · 44 links')).toBeVisible()
  })
  it('opens again on the same entry after the panel was closed', async () => {
    wrap(<AuditView />)
    const user = userEvent.setup()
    const entryButton = () =>
      screen
        .getAllByRole('button', { name: /Session started by Marc Vidal/ })
        .find((el) => el.closest('li')?.dataset.seq === '42')!
    await screen.findAllByRole('button', { name: /Session started/ })
    await user.click(entryButton())
    await screen.findByRole('complementary', { name: 'Entry 42' })
    await user.click(screen.getByRole('button', { name: 'Close the panel' }))
    await waitFor(() =>
      expect(screen.queryByRole('complementary')).not.toBeInTheDocument(),
    )
    await user.click(entryButton())
    expect(
      await screen.findByRole('complementary', { name: 'Entry 42' }),
    ).toBeVisible()
  })

  it('stays closed when a new filter reloads a list that still holds the entry', async () => {
    wrap(<AuditView />)
    const user = userEvent.setup()
    const picked = (
      await screen.findAllByRole('button', { name: /Session started by/ })
    ).find((el) => el.closest('li')?.dataset.seq === '42')!
    await user.click(picked)
    await screen.findByRole('complementary', { name: 'Entry 42' })
    await user.click(screen.getByRole('button', { name: 'Close the panel' }))
    await waitFor(() =>
      expect(screen.queryByRole('complementary')).not.toBeInTheDocument(),
    )
    await user.click(
      screen.getAllByRole('button', {
        name: 'Add person:Marc Vidal to the query',
      })[0]!,
    )
    await waitFor(() =>
      expect(api.list).toHaveBeenLastCalledWith({
        from: 1,
        limit: 100,
        actor: MARC,
      }),
    )
    // Entry 42 is listed again, and the panel the person closed stays closed.
    await screen.findAllByRole('button', { name: /Session started by/ })
    await new Promise((r) => setTimeout(r, 50))
    expect(screen.queryByRole('complementary')).not.toBeInTheDocument()
  })
})

describe('the Ledger entry, newest', () => {
  it('reads again what follows the newest entry when it opens again', async () => {
    // Entry 44 is the newest loaded. At first nothing follows it; then entry 45 does.
    let after: AuditEventDTO[] = []
    api.list.mockImplementation(async ({ from }: { from?: number }) =>
      from === 45
        ? { items: after, has_more: false }
        : { items: MATCHING, has_more: false },
    )
    wrap(<AuditView />)
    const user = userEvent.setup()
    const newest = async () =>
      (await screen.findAllByRole('button')).find(
        (el) =>
          el.dataset.slot === 'entry' && el.closest('li')?.dataset.seq === '44',
      )!
    await screen.findAllByRole('button', { name: /Session started/ })
    await user.click(await newest())
    const chain = async () =>
      within(
        await screen.findByRole('complementary', { name: 'Entry 44' }),
      ).getByRole('group', { name: 'Position in the chain' })
    await waitFor(async () =>
      expect(await chain()).toHaveTextContent('Newest entry'),
    )
    await user.click(screen.getByRole('button', { name: 'Close the panel' }))
    await waitFor(() =>
      expect(screen.queryByRole('complementary')).not.toBeInTheDocument(),
    )
    after = [entry(45, { action: 'audit.read' })]
    await user.click(await newest())
    await waitFor(async () =>
      expect(await chain()).toHaveTextContent(/Next · 45/),
    )
  })
})

describe('the Ledger, when a read fails', () => {
  it('drops the failed head read once a filter no longer asks for it', async () => {
    api.recent.mockRejectedValue(new Error('engine down'))
    wrap(<AuditView />)
    const user = userEvent.setup()
    await screen.findByRole('button', { name: /retry/i })
    const query = await screen.findByRole('textbox', { name: 'Ledger query' })
    await user.type(query, 'person:marc{Enter}')
    // The filtered list loaded; the head the filter no longer reads says nothing.
    expect(
      await screen.findAllByRole('button', { name: /Session started/ }),
    ).not.toHaveLength(0)
    expect(screen.queryByRole('button', { name: /retry/i })).toBeNull()
  })

  it('retries the read that failed, the head included', async () => {
    api.recent.mockRejectedValueOnce(new Error('engine down'))
    wrap(<AuditView />)
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: /retry/i }))
    await waitFor(() => expect(api.recent).toHaveBeenCalledTimes(2))
    expect(
      await screen.findAllByRole('button', { name: /Session started/ }),
    ).not.toHaveLength(0)
  })
})
