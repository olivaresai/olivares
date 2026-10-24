// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE LAST-HOUR SWIMLANE ON ESTATE (#2547), over the demo estate's event log
// (swimlane.fixture.ts): one row per live session that says its numbers in words, opens
// its session in the sheet, carries the one orange mark only for the flagged approval,
// and turns a dragged range or a ten-minute mark into the Ledger's own window.
import axe from 'axe-core'
import type { AnchorHTMLAttributes, ReactNode, Ref } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { sessionsApi } from '@/features/sessions/api'
import { mergeSessions } from '@/features/sessions/provenance'
import { formatTimeOfDay } from '@/lib/format'
import {
  DEFAULT_AUTH,
  fireEvent,
  renderIntel,
  screen,
  userEvent,
  waitFor,
  within,
} from '@/test/intel'
import { Swimlane } from './swimlane'
import {
  approval,
  approvals,
  at,
  calls,
  events,
  iso,
  live,
  lives,
  NOW,
  run,
  runs,
} from './swimlane.fixture'

vi.mock('@/lib/auth/context', () => ({ useAuth: () => DEFAULT_AUTH }))
// A <Link> as a plain anchor whose href carries its search, so a test reads the address.
vi.mock('@tanstack/react-router', () => ({
  Link: ({
    children,
    to,
    search,
    ...rest
  }: {
    children: ReactNode
    to: string
    search?: Record<string, string>
  } & AnchorHTMLAttributes<HTMLAnchorElement> & {
      ref?: Ref<HTMLAnchorElement>
    }) => (
    <a
      href={`${to}${search ? `?${new URLSearchParams(search)}` : ''}`}
      {...rest}
    >
      {children}
    </a>
  ),
}))

const sessions = mergeSessions(lives, runs)
const byLiveRef = new Map(runs.map((r) => [r.live_ref!, events[r.run_ref]!]))
const clock = (hhmm: string) => formatTimeOfDay(at(hhmm), 'en')
const keyOf = (ref: string) =>
  sessions.find((s) => s.runs[0]?.run_ref === ref)!.key

beforeEach(() => {
  vi.spyOn(sessionsApi, 'timelineById').mockImplementation(
    async (liveRef: string) => ({
      items: byLiveRef.get(liveRef) ?? [],
      has_more: false,
    }),
  )
})
afterEach(() => vi.restoreAllMocks())

function show(onOpen = vi.fn()) {
  const view = renderIntel(
    <Swimlane
      sessions={sessions}
      approvals={approvals}
      now={NOW}
      onOpen={onOpen}
    />,
  )
  return { onOpen, ...view }
}

const lanes = () => screen.findAllByTestId('swimlane-lane')
const plotOf = (title: string) =>
  screen.getByRole('img', { name: new RegExp(`^${title}`) })
const said = (title: string) => plotOf(title).getAttribute('aria-label')!

describe('the last-hour swimlane', () => {
  it('reads each lane from the start of the hour, on a whole minute', async () => {
    show()
    expect(await lanes()).toHaveLength(7)
    for (const r of runs)
      expect(sessionsApi.timelineById).toHaveBeenCalledWith(
        r.live_ref,
        { since: '2026-10-17T15:42:00.000Z', limit: 1000, cursor: undefined },
        expect.objectContaining({ signal: expect.anything() }),
      )
  })

  it('says each lane in words, as its events say it', async () => {
    show()
    await waitFor(() =>
      expect(said('Refactor cart totals')).toContain('tool calls'),
    )
    expect(said('Refactor cart totals')).toContain(
      `Active ${clock('15:44')} to ${clock('15:53')}, ${clock('15:57')} to ${clock('16:06')}, ${clock('16:10')} to ${clock('16:16')}, ${clock('16:24')} to ${clock('16:42')}`,
    )
    expect(said('Refactor cart totals')).toContain('Claude Code · Lena Fischer')
    expect(said('Refactor cart totals')).toContain(
      `Waiting for approval since ${clock('16:31')}`,
    )
    expect(said('Reconcile invoice export')).toContain(
      `Needs you since ${clock('16:40')}`,
    )
    expect(said('Fix flaky checkout')).toContain(`Idle since ${clock('16:31')}`)
    expect(said('Nightly dependency audit')).toContain('Codex · Schedule')
    expect(said('Nightly dependency audit')).toContain('7 tool calls')
  })

  it('says what a waiting lane waits for, never that it is working', async () => {
    show()
    const all = await lanes()
    const side = (title: string) =>
      all.find((l) => l.textContent?.includes(title))!
    expect(
      within(side('Reconcile invoice export')).getByText('Needs you'),
    ).toBeInTheDocument()
    expect(
      within(side('Upgrade Terraform')).getByText('Waiting for approval'),
    ).toBeInTheDocument()
    expect(within(side('Upgrade Terraform')).queryByText('Working')).toBeNull()
  })

  it('opens a lane in the sheet, and keeps the link for a new tab', async () => {
    const { onOpen } = show()
    await lanes()
    const link = screen.getByRole('link', {
      name: /Refactor cart totals in checkout/,
    })
    expect(link.getAttribute('href')).toBe(
      `/sessions?${new URLSearchParams({ session: keyOf('lena') })}`,
    )
    await userEvent.click(link)
    expect(onOpen).toHaveBeenCalledWith(keyOf('lena'))
  })

  it('has one orange mark: the approval Needs you shows first', async () => {
    const { container } = show()
    await waitFor(() =>
      expect(
        container.querySelectorAll('[data-mark="run"]').length,
      ).toBeGreaterThan(0),
    )
    const orange = container.querySelectorAll(
      '[class*="accent"], [style*="accent"]',
    )
    expect(orange).toHaveLength(1)
    expect(orange[0]!.getAttribute('data-mark')).toBe('ask-flagged')
    expect(plotOf('Reconcile invoice export').contains(orange[0]!)).toBe(true)
  })

  it('draws no tick per tool call: a run is one bar, shaded by its calls', async () => {
    const { container } = show()
    await waitFor(() =>
      expect(container.querySelectorAll('[data-mark="run"]')).toHaveLength(20),
    )
  })

  it('turns a drag across the lanes into the Ledger window', async () => {
    show()
    await lanes()
    // The plot is 600 px wide from x=200: one minute is 10 px.
    const plot = screen.getByTestId('swimlane-plot')
    plot.getBoundingClientRect = () =>
      ({
        left: 200,
        right: 800,
        width: 600,
        top: 0,
        bottom: 28,
        height: 28,
      }) as DOMRect
    const area = screen.getByTestId('swimlane-area')
    // 15:42 + 23 min = 16:05; 15:42 + 38.5 min = 16:20:30.
    fireEvent.pointerDown(area, { button: 0, clientX: 430, pointerId: 1 })
    fireEvent.pointerMove(area, { clientX: 585, pointerId: 1 })
    fireEvent.pointerUp(area, { clientX: 585, pointerId: 1 })
    expect(screen.getByTestId('swimlane-range')).toBeInTheDocument()
    const ledger = screen.getByRole('link', { name: 'Open in the Ledger' })
    const url = new URL(ledger.getAttribute('href')!, 'http://x')
    expect(url.pathname).toBe('/audit')
    expect(url.searchParams.get('since')).toBe('2026-10-17T16:05:00.000Z')
    expect(url.searchParams.get('until')).toBe('2026-10-17T16:21:00.000Z')
  })

  it('selects ten minutes with one click on a mark, the way without dragging', async () => {
    show()
    await lanes()
    await userEvent.click(
      screen.getByRole('button', {
        name: `${clock('16:10')} to ${clock('16:20')}`,
      }),
    )
    const url = new URL(
      screen
        .getByRole('link', { name: 'Open in the Ledger' })
        .getAttribute('href')!,
      'http://x',
    )
    expect(url.searchParams.get('since')).toBe('2026-10-17T16:10:00.000Z')
    expect(url.searchParams.get('until')).toBe('2026-10-17T16:20:00.000Z')
    // Focus moves to the one next step, which carries the range; Estate keeps its one
    // live region, so the swimlane adds none.
    const ledger = screen.getByRole('link', { name: 'Open in the Ledger' })
    expect(document.activeElement).toBe(ledger)
    expect(ledger).toHaveAccessibleDescription(
      `${clock('16:10')} to ${clock('16:20')}`,
    )
    expect(screen.queryByRole('status')).toBeNull()
    await userEvent.click(screen.getByRole('button', { name: 'Clear' }))
    // The cleared button is gone; focus stays in the swimlane.
    expect(document.activeElement).toBe(screen.getByTestId('estate-swimlane'))
    expect(
      screen.queryByRole('link', { name: 'Open in the Ledger' }),
    ).toBeNull()
  })

  it('ends the range of the last mark at now', async () => {
    show()
    await lanes()
    await userEvent.click(
      screen.getByRole('button', {
        name: `${clock('16:40')} to ${clock('16:50')}`,
      }),
    )
    const url = new URL(
      screen
        .getByRole('link', { name: 'Open in the Ledger' })
        .getAttribute('href')!,
      'http://x',
    )
    expect(url.searchParams.get('until')).toBe('2026-10-17T16:42:00.000Z')
  })

  it('keeps every number in a table', async () => {
    show()
    await waitFor(() =>
      expect(said('Nightly dependency audit')).toContain('7 tool calls'),
    )
    await userEvent.click(screen.getByText('Show as a table'))
    const table = screen.getByRole('table')
    expect(within(table).getAllByRole('row')).toHaveLength(8)
    const audit = within(table).getByRole('row', {
      name: /Nightly dependency audit/,
    })
    expect(within(audit).getByText('7')).toBeInTheDocument()
  })

  it('says a lane it could not read and draws nothing for it, while the others draw', async () => {
    vi.mocked(sessionsApi.timelineById).mockImplementation(
      async (liveRef: string) => {
        if (liveRef === 'L-lena') throw new Error('503')
        return { items: byLiveRef.get(liveRef) ?? [], has_more: false }
      },
    )
    const { container } = show()
    await waitFor(() =>
      expect(said('Refactor cart totals')).toContain('Activity not read'),
    )
    await waitFor(() =>
      expect(said('Reconcile invoice export')).toContain('tool calls'),
    )
    expect(
      plotOf('Refactor cart totals').querySelectorAll('[data-mark="run"]'),
    ).toHaveLength(0)
    expect(container.querySelectorAll('[data-mark="run"]')).toHaveLength(16)
  })

  it('says a lane is being read until its events arrive', async () => {
    vi.mocked(sessionsApi.timelineById).mockReturnValue(new Promise(() => {}))
    show()
    await lanes()
    expect(said('Refactor cart totals')).toContain('Reading activity')
  })

  it('reads every page of the hour, by its cursor', async () => {
    const all = byLiveRef.get('L-lena')!
    vi.mocked(sessionsApi.timelineById).mockImplementation(
      async (liveRef: string, params?: { cursor?: string }) =>
        liveRef !== 'L-lena'
          ? { items: byLiveRef.get(liveRef) ?? [], has_more: false }
          : params?.cursor === 'page-2'
            ? { items: all.slice(20), has_more: false }
            : { items: all.slice(0, 20), has_more: true, cursor: 'page-2' },
    )
    show()
    await waitFor(() =>
      expect(said('Refactor cart totals')).toContain(
        `${clock('16:24')} to ${clock('16:42')}`,
      ),
    )
    expect(sessionsApi.timelineById).toHaveBeenCalledWith(
      'L-lena',
      expect.objectContaining({ cursor: 'page-2' }),
      expect.anything(),
    )
  })

  it('says a lane is not read when the timeline has more and gives no cursor', async () => {
    vi.mocked(sessionsApi.timelineById).mockImplementation(
      async (liveRef: string) => ({
        items: byLiveRef.get(liveRef) ?? [],
        has_more: liveRef === 'L-lena',
      }),
    )
    show()
    await waitFor(() =>
      expect(said('Refactor cart totals')).toContain('Activity not read'),
    )
  })

  it('reads a legacy session by its own reference, from the hour', async () => {
    const legacy = live('legacy', 'active', '16:40')
    const row = {
      ...legacy,
      live_ref: '',
      attribution: 'legacy',
      provider_profile_ref: undefined,
      run_ref: undefined,
    }
    vi.spyOn(sessionsApi, 'timeline').mockResolvedValue({
      items: calls('16:30', '16:40'),
      has_more: false,
    })
    renderIntel(
      <Swimlane sessions={mergeSessions([row], [])} approvals={[]} now={NOW} />,
    )
    await lanes()
    await waitFor(() =>
      expect(sessionsApi.timeline).toHaveBeenCalledWith(
        'ext-legacy',
        { since: '2026-10-17T15:42:00.000Z', limit: 1000, cursor: undefined },
        expect.anything(),
      ),
    )
    expect(sessionsApi.timelineById).not.toHaveBeenCalled()
  })

  it('says a run inside one minute by one time', async () => {
    vi.mocked(sessionsApi.timelineById).mockResolvedValue({
      items: [{ at: iso(at('16:20')), kind: 'tool', tool_ref: 'Read' }],
      has_more: false,
    })
    renderIntel(
      <Swimlane
        sessions={mergeSessions([], [run('one', 'idle', { name: 'One call' })])}
        approvals={[]}
        now={NOW}
      />,
    )
    await waitFor(() => expect(said('One call')).toContain('1 tool call.'))
    expect(said('One call')).toContain(`Active ${clock('16:20')}.`)
  })

  it('marks a failure with a red tick and a word', async () => {
    const failed = run('bad', 'failed', {
      name: 'Run the migration dry run',
      stopped_at: iso(at('16:38')),
    })
    vi.mocked(sessionsApi.timelineById).mockResolvedValue({
      items: calls('16:30', '16:38'),
      has_more: false,
    })
    const { container } = renderIntel(
      <Swimlane
        sessions={mergeSessions([], [failed])}
        approvals={[]}
        now={NOW}
      />,
    )
    await waitFor(() =>
      expect(said('Run the migration dry run')).toContain(
        `Failed at ${clock('16:38')}`,
      ),
    )
    expect(container.querySelector('[data-mark="failure"]')).not.toBeNull()
    expect(
      within(plotOf('Run the migration')).getByText('Failed'),
    ).toBeVisible()
  })

  it('says waits are not drawn when the approvals could not be read', async () => {
    renderIntel(
      <Swimlane
        sessions={sessions}
        approvals={[]}
        approvalsRead={false}
        now={NOW}
      />,
    )
    expect(
      await screen.findByText(
        'Approvals could not be read, so waits for approval are not drawn.',
      ),
    ).toBeInTheDocument()
  })

  it('says an approval waits without a time when the engine did not send one', async () => {
    const { created_at: _gone, ...untimed } = approval(
      'ap-marc',
      'osn_marc',
      '16:40',
    )
    const { container } = renderIntel(
      <Swimlane sessions={sessions} approvals={[untimed]} now={NOW} />,
    )
    await waitFor(() =>
      expect(said('Reconcile invoice export')).toContain('tool calls'),
    )
    expect(said('Reconcile invoice export')).toMatch(/Needs you\.$/)
    expect(container.querySelectorAll('[data-mark="wait"]')).toHaveLength(0)
  })

  it('draws the waiting mark solid, never a gradient: the glow scan reads a warn gradient as glow in dark', async () => {
    const { container } = show()
    await waitFor(() =>
      expect(
        container.querySelectorAll('[data-mark="wait"]').length,
      ).toBeGreaterThan(0),
    )
    const legend = within(screen.getByRole('list', { name: 'Legend' }))
    const swatch = legend
      .getByText('Waiting for approval')
      .querySelector('span[aria-hidden]')!
    for (const mark of [
      swatch,
      ...container.querySelectorAll('[data-mark="wait"]'),
    ])
      expect(mark.getAttribute('style') ?? '').not.toMatch(/gradient/)
  })

  it('has no axe violations', async () => {
    const { container } = show()
    await lanes()
    const results = await axe.run(container, {
      // jsdom computes no colours or layout; the browser captures check those.
      rules: { 'color-contrast': { enabled: false } },
    })
    expect(results.violations.map((v) => v.id)).toEqual([])
  })
})
