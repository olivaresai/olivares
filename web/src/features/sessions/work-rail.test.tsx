// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE RAIL FROM A KEYBOARD, which is the half a snapshot cannot show.
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import type { RunDTO } from '@/features/agentops/types'
import { mergeSessions, type UnifiedSession } from './provenance'
import { addressOf } from './session-address'
import type { LiveDTO } from './types'
import { WorkRail } from './work-rail'
import './i18n'
import '@/features/agentops/i18n'

function live(over: Partial<LiveDTO> = {}): LiveDTO {
  return {
    session_ref: 'sess-1',
    live_ref: 'lr-1',
    // OBSERVED, so the row key is its own opaque id (`live:lr-a`) — the address shape
    // a profile-scoped row has. A legacy row keys on the bare provider id instead, and
    // `session-address.test.ts` drives both.
    attribution: 'observed',
    cc_state: 'idle',
    input_tokens: 0,
    output_tokens: 0,
    cost_micro_usd: 0,
    event_count: 0,
    tool_call_count: 0,
    first_event_at: '2026-09-18T09:00:00Z',
    last_event_at: '2026-09-18T09:01:00Z',
    duration_seconds: 42,
    ...over,
  }
}

function row(over: Partial<LiveDTO>, runs: RunDTO[] = []): UnifiedSession {
  return mergeSessions([live(over)], runs)[0]
}

const working = row({
  session_ref: 'sess-a',
  live_ref: 'lr-a',
  cc_state: 'active',
  current_action: 'create_issue',
  current_resource: 'github/create_issue',
})
const asking = row({
  session_ref: 'sess-b',
  live_ref: 'lr-b',
  cc_state: 'silent_evasion',
  summary: 'Reviewed the migration',
})
const done = row({ session_ref: 'sess-c', live_ref: 'lr-c', cc_state: 'ended' })

function renderRail(over: Partial<Parameters<typeof WorkRail>[0]> = {}) {
  const onOpen = vi.fn()
  const onTogglePin = vi.fn()
  const utils = render(
    <WorkRail
      sessions={[working, asking, done]}
      selected={null}
      onOpen={onOpen}
      pinned={new Set()}
      onTogglePin={onTogglePin}
      {...over}
    />,
  )
  return { ...utils, onOpen, onTogglePin }
}

const rows = () => screen.getAllByRole('option')

describe('WorkRail — what a row says', () => {
  it('gives a long name the remaining width of a 256 px rail', () => {
    const name = 'dependency-audit-across-the-whole-estate'
    const long = mergeSessions(
      [],
      [
        {
          run_ref: 'run-long',
          name,
          transport: 'stream-json',
          permission_mode: 'default',
          isolation: 'native',
          state: 'running',
          last_event_seq: 0,
          pep_provisioned: true,
          record_io: false,
          critical: false,
          last_activity_at: '2026-09-18T09:01:00Z',
        },
      ],
    )[0]
    render(
      <div data-testid="rail-frame" style={{ width: 256 }}>
        <WorkRail
          sessions={[long]}
          selected={null}
          onOpen={vi.fn()}
          pinned={new Set()}
          onTogglePin={vi.fn()}
        />
      </div>,
    )
    const frame = screen.getByTestId('rail-frame')
    expect(frame).toHaveStyle({ width: '256px' })
    const rowEl = within(frame).getByTestId('rail-row')
    const nameEl = within(rowEl).getByTestId('rail-row-name')
    expect(nameEl).toHaveTextContent(name)
    // Name AND reference on the one hover: a truncated name stays readable and the
    // identifier the row no longer paints stays reachable from it.
    expect(nameEl.getAttribute('title')).toContain(name)
    expect(nameEl.getAttribute('title')).toContain('run-long')
    expect(nameEl.className.split(/\s+/)).toEqual(
      expect.arrayContaining(['flex-1', 'min-w-0', 'truncate']),
    )
    const growers = [...rowEl.children].filter(
      (el) => el instanceof HTMLElement && /\bflex-1\b/.test(el.className),
    )
    expect(growers).toEqual([nameEl])
    const badge = within(rowEl).getByText('Running')
    expect(badge.className.split(/\s+/)).toContain('min-w-0')
    expect(badge.className.split(/\s+/)).not.toContain('shrink-0')
    const age = rowEl.querySelector('time')
    expect(age).not.toBeNull()
    expect(age!.className.split(/\s+/)).toContain('min-w-0')
    expect(age!.className.split(/\s+/)).not.toContain('shrink-0')
  })

  it('names the session and its state on the line, and carries the rest on the row', () => {
    renderRail()
    const first = rows()[0]
    // ⛔ ONE LINE. The row used to paint the name, then the state with an
    //    elapsed time, then the observed clause — three lines, 92 px measured in a real
    //    browser, in a 256 px rail. What an operator picks a row BY stays painted:
    expect(within(first).getByText('Active')).toBeInTheDocument()
    // …and the NAME is what the session is doing, never its reference. `sess-a` was
    // the second rung of the old label, so a rail of discovered sessions read as six
    // machine ids. The reference is on the name's own `title`.
    expect(
      within(first).getByText('create_issue · github/create_issue'),
    ).toBeInTheDocument()
    expect(within(first).queryByText('sess-a')).toBeNull()
    expect(
      within(first).getByTestId('rail-row-name').getAttribute('title'),
    ).toContain('sess-a')
    // …and the two facts that came off the line are on the row itself, in the order the
    // row painted them. This is the assertion that makes "nothing was dropped" checkable:
    // delete the `title` and this case is red.
    expect(first).toHaveAttribute(
      'title',
      '42s · create_issue · github/create_issue',
    )
  })

  it('says so when a session has no telemetry, instead of printing its id twice', () => {
    const runOnly = mergeSessions(
      [],
      [
        {
          run_ref: 'run-9',
          name: 'nightly',
          transport: 'stream-json',
          permission_mode: 'default',
          isolation: 'native',
          state: 'running',
          last_event_seq: 0,
          pep_provisioned: true,
          record_io: false,
          critical: false,
        },
      ],
    )[0]
    renderRail({ sessions: [runOnly] })
    // The row names the run and shows its state, and paints NO second copy of its id —
    // which is what the old "no telemetry yet" line existed to prevent. With one line
    // there is nowhere to print it twice, so the check is that the row says the name
    // once and carries no observation on its `title` either: there is nothing to say.
    const row = rows()[0]
    expect(within(row).getByText('nightly')).toBeInTheDocument()
    expect(row).not.toHaveAttribute('title')
    expect(within(row).queryByText(/run-9/)).toBeNull()
  })

  it('a session nobody named says so, and keeps only the distinguishing tail', () => {
    // The rung the two cases above cannot reach: both of their rows ARE named — one by
    // the connector's reported action, one by the run the operator named — so neither
    // executes the rail's last rung, which is the one that used to paint `sess-…`.
    const bare = row({ session_ref: 'sess-coder-7a3f', live_ref: undefined })
    renderRail({ sessions: [bare] })
    const name = within(rows()[0]).getByTestId('rail-row-name')
    expect(name).toHaveTextContent('Untitled session')
    expect(name.textContent).not.toContain('sess-coder-7a3f')
    // Two untitled rows are still told apart, and the whole reference is one hover away.
    expect(name).toHaveTextContent('coder-7a3f')
    expect(name.getAttribute('title')).toContain('sess-coder-7a3f')
  })

  it('a PINNED row shows its mark, in the muted register and never in the accent', () => {
    // ⛔ THE FIXTURE PINS A ROW ON PURPOSE. Every rail fixture in this file passed
    //    `pinned={new Set()}`, so the mark rendered in no test at all — which is exactly
    //    why the accent on it survived the round that took the accent off the other
    //    icons: a census cannot see a glyph nothing renders.
    const address = addressOf(working)
    renderRail({ sessions: [working], pinned: new Set([address]) })
    const row = rows()[0]
    const mark = row.querySelector('svg.lucide-pin') as SVGElement
    expect(mark).not.toBeNull()
    // The accent means "this is the session on screen"; a pin is a state the operator
    // put the row in. One colour, one meaning.
    expect(mark.getAttribute('class')).toContain('text-muted-foreground')
    expect(mark.getAttribute('class')).not.toMatch(/text-accent/)
    // The WORD is what joins the row's accessible name — an aria-label on a bare <svg>
    // is not reliably announced.
    expect(mark.getAttribute('aria-hidden')).toBe('true')
    expect(within(row).getByText(/pinned/i)).toBeInTheDocument()
  })

  it('groups by what the session needs, and keeps an empty group', () => {
    renderRail({ sessions: [working] })
    // The three headings are always there. An empty one states its own sentence
    // rather than vanishing and moving the rows under the operator's cursor.
    expect(screen.getByText('Waiting for you')).toBeInTheDocument()
    expect(screen.getByText('Settled')).toBeInTheDocument()
    expect(screen.getByText(/Nothing is waiting for you/)).toBeInTheDocument()
    expect(screen.getByText(/No session has settled yet/)).toBeInTheDocument()
  })
})

describe('WorkRail — arrows move, Enter opens', () => {
  it('is ONE tab stop, and lands on the first row', async () => {
    const user = userEvent.setup()
    renderRail()
    await user.tab()
    // A rail of two hundred sessions must not be two hundred tab stops.
    expect(document.activeElement).toBe(rows()[0])
    await user.tab()
    expect(rows()).not.toContain(document.activeElement)
  })

  it('moves focus without opening anything', async () => {
    const user = userEvent.setup()
    const { onOpen } = renderRail()
    await user.tab()
    await user.keyboard('{ArrowDown}')
    expect(document.activeElement).toBe(rows()[1])
    // THE POINT: walking the rail must not fire a navigation — and on this screen a
    // navigation is a request — per keystroke.
    expect(onOpen).not.toHaveBeenCalled()
  })

  it('opens the focused row on Enter', async () => {
    const user = userEvent.setup()
    const { onOpen } = renderRail()
    await user.tab()
    await user.keyboard('{ArrowDown}{Enter}')
    expect(onOpen).toHaveBeenCalledTimes(1)
    expect(onOpen.mock.calls[0][0].sessionRef).toBe('sess-b')
  })

  it('does not wrap at either end', async () => {
    const user = userEvent.setup()
    renderRail()
    await user.tab()
    await user.keyboard('{ArrowUp}')
    expect(document.activeElement).toBe(rows()[0])
    await user.keyboard('{End}{ArrowDown}')
    expect(document.activeElement).toBe(rows()[rows().length - 1])
  })

  it('starts on the OPEN session, so a deep link can be walked away from', async () => {
    const user = userEvent.setup()
    renderRail({ selected: 'live:lr-c' })
    await user.tab()
    expect(document.activeElement).toBe(
      screen
        .getByTestId('work-rail')
        .querySelector('[data-address="live:lr-c"]'),
    )
  })

  it('marks the open session as selected, which focus alone does not', async () => {
    const user = userEvent.setup()
    renderRail({ selected: 'live:lr-a' })
    await user.tab()
    await user.keyboard('{ArrowDown}')
    expect(rows()[0]).toHaveAttribute('aria-selected', 'true')
    expect(rows()[1]).toHaveAttribute('aria-selected', 'false')
  })
})

describe('WorkRail — no gesture without a keyboard path', () => {
  it('pins the focused row with `p`', async () => {
    const user = userEvent.setup()
    const { onTogglePin } = renderRail()
    await user.tab()
    await user.keyboard('p')
    expect(onTogglePin).toHaveBeenCalledWith('live:lr-a')
  })

  it('pins the row the keyboard is ON, not the one that is open', async () => {
    const user = userEvent.setup()
    const { onTogglePin } = renderRail({ selected: 'live:lr-a' })
    await user.tab()
    await user.keyboard('{ArrowDown}p')
    expect(onTogglePin).toHaveBeenCalledWith('live:lr-b')
  })

  it('does nothing on `p` when there is nowhere to store a preference', async () => {
    // A token principal, or no tenant. A key that silently does nothing is worse than
    // one that is not bound, so the rail does not bind it either.
    const user = userEvent.setup()
    const { onOpen } = renderRail({ onTogglePin: null })
    await user.tab()
    await user.keyboard('p')
    expect(onOpen).not.toHaveBeenCalled()
  })

  it('puts NO focusable control inside a row, and stays one tab stop for it', async () => {
    // `option` is children-presentational in ARIA: a button in a row is invisible to
    // assistive technology and a `nested-interactive` finding. The equal MENU path is
    // in the narrative pane, on the session the operator is reading, and
    // `session-narrative.test.tsx` drives it.
    const user = userEvent.setup()
    renderRail({ selected: 'live:lr-c' })
    for (const option of rows())
      expect(option.querySelector('button')).toBeNull()
    await user.tab()
    expect(document.activeElement).toBe(
      screen
        .getByTestId('work-rail')
        .querySelector('[data-address="live:lr-c"]'),
    )
  })
})

describe('WorkRail — nothing at all', () => {
  it('says what the surface will show, and offers the one action', () => {
    renderRail({
      sessions: [],
      emptyAction: <button type="button">Start a session</button>,
    })
    expect(screen.getByText('No sessions yet')).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: 'Start a session' }),
    ).toBeInTheDocument()
  })

  it('does not call an empty estate empty while it is still loading', () => {
    renderRail({ sessions: [], loading: true })
    expect(screen.queryByText('No sessions yet')).toBeNull()
  })

  // A listbox owns options. While the first read is in flight it has none yet, and a
  // listbox with no option and no busy state is a broken ARIA tree (WCAG 1.3.1;
  // axe `aria-required-children`, critical). Busy is the true state of that moment.
  it('says it is busy while the first read is in flight, and stops saying so after', () => {
    const { unmount } = renderRail({ sessions: [], loading: true })
    expect(screen.getByRole('listbox')).toHaveAttribute('aria-busy', 'true')
    unmount()

    renderRail({ sessions: [working], loading: false })
    expect(screen.getByRole('listbox')).not.toHaveAttribute('aria-busy')
  })
})
