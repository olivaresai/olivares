// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', isSuperadmin: true, can: () => true }),
}))

vi.mock('@tanstack/react-router', () => ({
  //useUrlState follows the location, so the mock has to answer it.
  useRouterState: () => '',
  Link: ({ to, children, ...rest }: { to: string; children: ReactNode }) => (
    <a href={to} {...rest}>
      {children}
    </a>
  ),
}))

//the ledger this component reads is seeded ABOVE the bell's page size on
// purpose. With ONE event the old code was indistinguishable from correct (the
// oldest event and the newest one are the same row), which is exactly why the
// defect survived: it works on the first day of an install and breaks on event
// two, silently. A fixture that cannot tell the two apart is not a measurement.
const { LEDGER_SIZE, ledger, listMock, appendEvent, appendReads, resetLedger } =
  vi.hoisted(() => {
    // DEEPER than the component's scan window on purpose. With a ledger shorter than
    // the window, `from` clamps to 1 on every request and the window can never be seen
    // to MOVE — every assertion about it would pass for the wrong reason.
    const size = 150
    // One minute per sequence, as real dates rather than a hand-built string: the
    // component decides "unread" by comparing ISO-8601 timestamps lexically, and a
    // hand-padded minute field silently stops ordering past 59 — which a 150-event
    // ledger reaches.
    const at = (seq: number) =>
      new Date(Date.UTC(2026, 5, 26, 10, 0, 0) + seq * 60_000).toISOString()
    const make = (seq: number) => ({
      id: `ev${seq}`,
      seq,
      occurred_at: at(seq),
      actor: 'admin@test.com',
      actor_kind: 'user',
      action: `event.${seq}`,
      target_kind: 'core.api_token',
      target_id: `tok-${seq}`,
      prev_hash: '',
      hash: `h${seq}`,
    })
    // MUTABLE on purpose: a fixture frozen at mount can only ever test the first
    // render, and "the dot lights when something happens LATER" is the whole point of
    // a notification bell. Appending here is how an event arrives after the fact.
    const events = Array.from({ length: size }, (_, i) => make(i + 1))
    // The engine's contract, reproduced exactly — and the default is the half that
    // matters: /v1/audit takes `from` = 1 when it is omitted and walks FORWARDS
    // (core/api/handlers_audit.go queryInt64(r, "from", 1); sqlstore Walk is
    // ORDER BY seq ASC). So a request that forgets `from` gets the OLDEST page here
    // just as it does in production, which is what makes the mutant below real.
    // And it SELF-AUDITS, which is the one detail that decides F-01: every
    // GET /v1/audit seals its own `audit.read` into the chain it just read, and the
    // head is measured BEFORE that append (core/api/handlers_audit.go). A mock
    // answering from a frozen list cannot tell a bell that goes quiet from one that
    // spends all day announcing its own looking — the two are identical until the
    // reads are real.
    const list = vi.fn(
      async (params?: {
        from?: number
        limit?: number
        exclude_action?: string[]
      }) => {
        const from = params?.from ?? 1
        const limit = params?.limit ?? 100
        const excluded = params?.exclude_action ?? []
        const head = events.length
        const items = events
          .filter((e) => e.seq >= from)
          .filter(
            (e) => !excluded.some((prefix) => e.action.startsWith(prefix)),
          )
          .slice(0, limit)
        const read = make(events.length + 1)
        read.action = 'audit.read'
        events.push(read)
        return { items, cursor: '', has_more: false, head_seq: head }
      },
    )
    const append = () => {
      const event = make(events.length + 1)
      events.push(event)
      return event
    }
    // The bell's own footprint, on demand. Driving it through refetches would work but
    // takes one interval per read; a test that needs the last N positions to be reads
    // wants to say so, not simulate twenty minutes.
    const appendOwnReads = (count: number) => {
      for (let i = 0; i < count; i++) {
        const read = make(events.length + 1)
        read.action = 'audit.read'
        events.push(read)
      }
    }
    const reset = () => {
      events.length = size
    }
    return {
      LEDGER_SIZE: size,
      ledger: events,
      listMock: list,
      appendEvent: append,
      appendReads: appendOwnReads,
      resetLedger: reset,
    }
  })

vi.mock('@/lib/api/endpoints', () => ({ auditApi: { list: listMock } }))

import { NotificationBell } from './notification-bell'

const LAST_SEEN_KEY = 'olivares.notifications.lastSeen'
const NEWEST = ledger[LEDGER_SIZE - 1]
const PREVIOUS = ledger[LEDGER_SIZE - 2]
const OLDEST = ledger[0]

function Wrapper({ children }: { children: React.ReactNode }) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return <QueryClientProvider client={qc}>{children}</QueryClientProvider>
}

/** The request the bell makes for the tail, i.e. the one that carries `from`. */
function tailRequest() {
  const call = listMock.mock.calls
    .map((args) => args[0])
    .find((params) => params?.from !== undefined)
  return call
}

/** The action labels currently on screen, in render order. */
function renderedActions() {
  return screen
    .getAllByText(/^event \d+$/)
    .map((node) => node.textContent ?? '')
}

/** The label the component renders for a seeded event ("event.7" -> "event 7"). */
function labelOf(event: (typeof ledger)[number]) {
  return event.action.replace(/[._]/g, ' ')
}

beforeEach(() => {
  localStorage.clear()
  listMock.mockClear()
  resetLedger()
})

describe('NotificationBell', () => {
  it('renders the bell button', () => {
    render(
      <Wrapper>
        <NotificationBell />
      </Wrapper>,
    )
    expect(screen.getByRole('button')).toBeDefined()
  })

  //THE defect. The ledger pages forwards from `from`, so the bell has to
  // ask for the TAIL: it reads head_seq from a one-row probe and requests
  // `from = head_seq - limit + 1`. Before that it asked for `limit` with no `from`
  // and rendered the ten OLDEST events of the tenant under the word "newest".
  it('shows the LAST events of the ledger, newest first', async () => {
    const user = userEvent.setup()
    render(
      <Wrapper>
        <NotificationBell />
      </Wrapper>,
    )
    await user.click(screen.getByRole('button'))
    await screen.findByText(labelOf(NEWEST))

    // The window is derived from what the component ASKED for, not from a copy of
    // its page-size constant: whatever limit it chose, `from` must be the head
    // minus that limit plus one, or it is not addressing the tail.
    const tail = tailRequest()
    expect(tail).toBeDefined()
    expect(tail).toEqual({
      from: LEDGER_SIZE - tail!.limit! + 1,
      limit: tail!.limit,
      // The bell must not be shown its own footprint (F-01).
      exclude_action: ['audit.read'],
    })

    // EXAMINED and SHOWN are different numbers, and this is the assertion that says
    // so. Collapse SCAN_WINDOW back onto RECENT_LIMIT — one constant for both, which
    // is what this component did until F-04 — and the two counts become equal here.
    const rendered = renderedActions()
    expect(rendered.length).toBeLessThan(tail!.limit!)

    // Derived from the SEEDED events and the window the component asked for — not by
    // re-running the mock's own filter, which would only prove the mock agrees with
    // itself. `slice(0, LEDGER_SIZE)` drops the `audit.read` rows the fixture appends
    // per call: those are exactly what the bell must not be shown. Of the examined
    // window, what reaches the screen is its NEWEST tail.
    const examined = ledger
      .slice(0, LEDGER_SIZE)
      .filter((event) => event.seq >= tail!.from!)
    expect(examined.length).toBeGreaterThan(rendered.length)
    const expected = examined.slice(-rendered.length).reverse().map(labelOf)
    expect(rendered).toEqual(expected)
    expect(screen.queryByText(labelOf(OLDEST))).toBeNull()
  })

  //E4e +: the dot means UNSEEN, and "seen" is anchored to the NEWEST
  // event on screen. Anchoring it to the first row of an ascending page pinned it
  // to the genesis event, whose timestamp never changes — so once the popover had
  // been opened, the dot could never light again, for any number of later events.
  it('lights the dot for a new event, then clears + persists the NEWEST timestamp on open', async () => {
    // The user has seen everything up to the second-newest event; one more arrived.
    localStorage.setItem(LAST_SEEN_KEY, PREVIOUS.occurred_at)
    const user = userEvent.setup()
    const { container } = render(
      <Wrapper>
        <NotificationBell />
      </Wrapper>,
    )
    await waitFor(() =>
      expect(container.querySelector('.bg-primary')).not.toBeNull(),
    )

    await user.click(screen.getByRole('button'))
    expect(container.querySelector('.bg-primary')).toBeNull()
    expect(localStorage.getItem(LAST_SEEN_KEY)).toBe(NEWEST.occurred_at)
  })

  // The bell POLLS, and nothing above measures that: every other test settles on the
  // mount and stops. Delete `refetchInterval` from the component and all of them stay
  // green while the bell silently becomes a thing that only ever tells you what was
  // true when the page loaded. The clock is what makes it a notification.
  //
  // The advance is deliberately far larger than the interval instead of equal to it:
  // hardcoding the exact value here would just be a second copy of a constant, and
  // this way a LONGER interval fails loudly rather than passing by coincidence — while
  // a bell that has stopped polling never fires no matter how far the clock runs.
  it('keeps polling: an event that arrives later lights the dot with no reload', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    try {
      localStorage.setItem(LAST_SEEN_KEY, NEWEST.occurred_at)
      const { container } = render(
        <Wrapper>
          <NotificationBell />
        </Wrapper>,
      )
      // Settled, everything seen, dot off — the state a bell rests in.
      await waitFor(() => expect(tailRequest()).toBeDefined())
      expect(container.querySelector('.bg-primary')).toBeNull()

      appendEvent()
      // advanceTimersByTimeAsync flushes the refetch and its re-render, so the
      // assertions below are direct. NOT waitFor: it polls on a timer that is itself
      // faked here, so it would evaluate once and then wait out its own timeout —
      // which is how this test first failed while the mechanism worked (2 -> 8 calls,
      // dot already on).
      await vi.advanceTimersByTimeAsync(5 * 60_000)

      expect(container.querySelector('.bg-primary')).not.toBeNull()
      // And not merely a lit dot: the head moved, so the WINDOW moved with it by
      // exactly the one event that arrived. A bell that lit without re-addressing the
      // tail would be lighting for something it cannot show.
      const windows = listMock.mock.calls
        .map((args) => args[0]?.from)
        .filter((from): from is number => from !== undefined)
      expect(windows.length).toBeGreaterThan(1)
      // Strictly forward, not by a fixed step: the head advances with the bell's own
      // reads as well as with the event that arrived, so the window tracks it.
      expect(windows[windows.length - 1]).toBeGreaterThan(windows[0])
    } finally {
      vi.useRealTimers()
    }
  })

  // F-01, and it is the test the whole extension exists for. Reading the ledger
  // appends to it, so a bell polling the TAIL used to fill that tail with its own
  // looking and re-light the dot every interval for activity nobody performed. It has
  // to stay dark through repeated polls when nothing outside happened — and the
  // control is what stops that from passing vacuously: the fixture must actually have
  // sealed reads in the meantime, or this only proves the bell was asleep.
  it('stays dark through repeated polls when only its own reads happen', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    try {
      localStorage.setItem(LAST_SEEN_KEY, NEWEST.occurred_at)
      const { container } = render(
        <Wrapper>
          <NotificationBell />
        </Wrapper>,
      )
      await waitFor(() => expect(tailRequest()).toBeDefined())
      expect(container.querySelector('.bg-primary')).toBeNull()

      const ledgerBefore = ledger.length
      for (let round = 0; round < 5; round++) {
        await vi.advanceTimersByTimeAsync(5 * 60_000)
        expect(container.querySelector('.bg-primary')).toBeNull()
      }

      // CONTROL: the ledger really did grow, and every event added was the bell's own
      // read. Without this the assertion above is satisfied by a bell that never
      // polled at all — the exact green that measures nothing.
      const added = ledger.slice(ledgerBefore)
      expect(added.length).toBeGreaterThan(0)
      expect(added.every((event) => event.action === 'audit.read')).toBe(true)
    } finally {
      vi.useRealTimers()
    }
  })

  // The other half of F-04, and it is a LOSS the exclusion does not fix: the window is
  // ten sequence positions, the bell's own reads keep filling them, so an event that
  // lit the dot can slide below `from` before the user opens. The dot must not switch
  // itself off for something nobody looked at.
  it('keeps the dot lit when the event that lit it slides out of the window', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    try {
      localStorage.setItem(LAST_SEEN_KEY, NEWEST.occurred_at)
      const { container } = render(
        <Wrapper>
          <NotificationBell />
        </Wrapper>,
      )
      await waitFor(() => expect(tailRequest()).toBeDefined())
      expect(container.querySelector('.bg-primary')).toBeNull()

      const arrived = appendEvent()
      await vi.advanceTimersByTimeAsync(5 * 60_000)
      expect(container.querySelector('.bg-primary')).not.toBeNull()

      // Now let the bell's own reads push it out. Enough rounds that the window's ten
      // positions cannot still contain it.
      for (let round = 0; round < 12; round++) {
        await vi.advanceTimersByTimeAsync(5 * 60_000)
      }

      // CONTROL: it really is gone from what the bell can show. Without this the
      // assertion below would pass while the event was still on screen, proving
      // nothing about the latch.
      const windows = listMock.mock.calls
        .map((args) => args[0]?.from)
        .filter((from): from is number => from !== undefined)
      expect(windows[windows.length - 1]).toBeGreaterThan(arrived.seq)
      expect(screen.queryByText(labelOf(arrived))).toBeNull()

      // ...and the dot is STILL lit, because the user never opened the popover.
      expect(container.querySelector('.bg-primary')).not.toBeNull()
    } finally {
      vi.useRealTimers()
    }
  })

  // F-04, and this is the behaviour the wider window exists for — not the request
  // shape, which a mutant could satisfy while showing nothing. Real activity, then a
  // burst of the bell's own reads long enough to fill the DISPLAY depth several times
  // over: the events must still be on screen, because they are still inside the
  // EXAMINED window. Collapse the two constants and this page comes back empty.
  it('still shows real activity buried under a burst of its own reads', async () => {
    const user = userEvent.setup()
    appendReads(40)
    render(
      <Wrapper>
        <NotificationBell />
      </Wrapper>,
    )
    await user.click(screen.getByRole('button'))

    // CONTROL: the burst really did bury them — the newest real event is far below
    // the head now, deeper than the display could ever reach on its own.
    await waitFor(() => expect(tailRequest()).toBeDefined())
    const headAtRequest = tailRequest()!.from! + tailRequest()!.limit! - 1
    expect(headAtRequest - NEWEST.seq).toBeGreaterThan(20)

    await screen.findByText(labelOf(NEWEST))
    expect(renderedActions()).toContain(labelOf(NEWEST))
    // And still no reads on screen: the burst is examined, never displayed.
    expect(screen.queryAllByText('audit read')).toHaveLength(0)
  })

  // The floor: sequence numbers start at 1, so a ledger shorter than the window has
  // nothing below it to ask for. `from: 0` would name a sequence the chain never has.
  it('clamps the window at sequence 1 on a ledger shorter than it', async () => {
    listMock.mockResolvedValueOnce({
      items: [],
      cursor: '',
      has_more: false,
      head_seq: 5,
    })
    render(
      <Wrapper>
        <NotificationBell />
      </Wrapper>,
    )
    await waitFor(() => expect(tailRequest()).toBeDefined())
    const tail = tailRequest()!
    expect(tail.from).toBe(1)
    // ...and it is the FLOOR that clamped it, not a narrow window: the request still
    // asks for the full examination depth.
    expect(tail.limit).toBeGreaterThan(5)
  })

  // The non-firing direction: a dot that is always on proves nothing about a dot
  // that lights for new events.
  it('does not light the dot when the newest event was already seen', async () => {
    localStorage.setItem(LAST_SEEN_KEY, NEWEST.occurred_at)
    const { container } = render(
      <Wrapper>
        <NotificationBell />
      </Wrapper>,
    )
    await screen.findByRole('button')
    await waitFor(() => expect(tailRequest()).toBeDefined())
    await waitFor(() =>
      expect(container.querySelector('.bg-primary')).toBeNull(),
    )
  })

  // head_seq 0 is the empty ledger, and it has to settle on the probe alone: a
  // second request would be `from: 0`, a sequence the chain never has, and a bell
  // that waited for its answer would spin instead of saying "nothing yet".
  it('asks once and shows the empty state when the ledger is empty', async () => {
    listMock.mockResolvedValueOnce({
      items: [],
      cursor: '',
      has_more: false,
      head_seq: 0,
    })
    const user = userEvent.setup()
    render(
      <Wrapper>
        <NotificationBell />
      </Wrapper>,
    )
    await user.click(screen.getByRole('button'))
    // Wait for the settled empty state, not merely for the call: asserting "no
    // tail request" while the probe is still in flight would pass for the wrong
    // reason, and would pass just as well if the component never asked at all.
    await screen.findByText(/no recent activity/i)
    expect(screen.queryAllByText(/^event \d+$/)).toHaveLength(0)
    expect(listMock).toHaveBeenCalledTimes(1)
    expect(tailRequest()).toBeUndefined()
  })

  it('links "view all" to the audit ledger', async () => {
    const user = userEvent.setup()
    render(
      <Wrapper>
        <NotificationBell />
      </Wrapper>,
    )
    await user.click(screen.getByRole('button'))
    const link = await screen.findByRole('link', { name: /view all/i })
    expect(link).toHaveAttribute('href', '/audit')
  })
})
