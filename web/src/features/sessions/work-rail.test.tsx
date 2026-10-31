// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE RAIL FROM A KEYBOARD, which is the half a snapshot cannot show.
import {
  fireEvent,
  render as renderRaw,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import axe from 'axe-core'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import type { RunDTO } from '@/features/agentops/types'
import { TooltipProvider } from '@/components/ui/tooltip'
import { DropdownMenuItem } from '@/components/ui/dropdown-menu'
import { usePreferencesStore } from '@/stores/preferences'
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
  provider: 'claude',
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

/** A launched session: it has a run, so the engine can keep a pin for it. */
function launchedRow(ref: string): UnifiedSession {
  return mergeSessions(
    [],
    [
      {
        run_ref: ref,
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
}

/**
 * The rail as it is once the Working fold is open. The rows this file asserts on are
 * mostly running sessions, so each render opens the fold first; the fold's own behavior,
 * shut by default, is the last describe, which renders with `renderRaw`.
 */
function render(ui: Parameters<typeof renderRaw>[0]) {
  const utils = renderRaw(ui)
  const toggle = screen.queryByRole('button', { name: /^Working\s*\d+$/ })
  if (toggle?.getAttribute('aria-expanded') === 'false') fireEvent.click(toggle)
  return utils
}

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

/** Tab into the rail's rows. The Working fold's button is a control of its own and, when
 * no row precedes it, the first stop: the walk goes on to the row the keyboard starts on. */
async function tabIntoRows(user: ReturnType<typeof userEvent.setup>) {
  await user.tab()
  if (document.activeElement?.id === 'rail-group-active') await user.tab()
}

describe('WorkRail — what a row says', () => {
  it('keeps a long name on one line, whole on the hover, with its reference', () => {
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
      <div data-testid="rail-frame" style={{ width: 300 }}>
        <WorkRail
          sessions={[long]}
          selected={null}
          onOpen={vi.fn()}
          pinned={new Set()}
          onTogglePin={vi.fn()}
        />
      </div>,
    )
    const rowEl = within(screen.getByTestId('rail-frame')).getByTestId(
      'rail-row',
    )
    const nameEl = within(rowEl).getByTestId('rail-row-name')
    expect(nameEl).toHaveTextContent(name)
    // Name AND reference on the one hover: a cut name stays readable and the
    // identifier the row does not paint stays reachable from it.
    expect(nameEl.getAttribute('title')).toContain(name)
    expect(nameEl.getAttribute('title')).toContain('run-long')
    // A row is one line at the list's standard row height: the title is cut with an
    // ellipsis.
    expect(nameEl.className.split(/\s+/)).toEqual(
      expect.arrayContaining(['truncate', 'min-w-0']),
    )
    // Two short lines: the place and the state above, the name below.
    expect(rowEl.className.split(/\s+/)).toEqual(
      expect.arrayContaining(['h-14', 'min-[761px]:h-12']),
    )
    // The state is a word, whole: a working session says how long it has worked.
    const state = within(rowEl).getByTestId('rail-row-state')
    expect(state).toHaveTextContent(/^Working/)
    expect(state.className.split(/\s+/)).toEqual(
      expect.arrayContaining(['shrink-0', 'tabular-nums']),
    )
  })

  it('keeps the tool and the folder on the hover, one line, never the path in the name', () => {
    const launched = mergeSessions(
      [],
      [
        {
          run_ref: 'run-f',
          name: 'fix-tests',
          transport: 'stream-json',
          permission_mode: 'default',
          isolation: 'native',
          state: 'running',
          last_event_seq: 0,
          pep_provisioned: true,
          record_io: false,
          critical: false,
          provider_driver: 'claude',
          workspace_path: '/srv/work/repo',
        },
      ],
    )[0]
    renderRail({ sessions: [launched] })
    const first = rows()[0]
    // ONE LINE: there is no second line to leave blank (#2266).
    expect(within(first).queryByTestId('rail-row-meta')).toBeNull()
    const name = within(first).getByTestId('rail-row-name')
    expect(name).toHaveTextContent('fix-tests')
    expect(name.textContent).not.toContain('/srv/work')
    // The tool, the folder and its whole path are one hover away.
    expect(first.getAttribute('title')).toContain('Claude Code · repo')
    expect(first.getAttribute('title')).toContain('/srv/work/repo')
  })

  it('names a launched session with no prompt by its tool and folder, never Untitled', () => {
    const launched = mergeSessions(
      [],
      [
        {
          run_ref: 'run-g',
          transport: 'stream-json',
          permission_mode: 'default',
          isolation: 'native',
          state: 'running',
          last_event_seq: 0,
          pep_provisioned: true,
          record_io: false,
          critical: false,
          provider_driver: 'claude',
          workspace_path: '/srv/work/billing-api',
        },
      ],
    )[0]
    renderRail({ sessions: [launched] })
    const name = within(rows()[0]).getByTestId('rail-row-name')
    expect(name).toHaveTextContent('Claude Code · billing-api')
    expect(name.textContent).not.toContain('Untitled session')
  })

  it('keeps the title its room: the Open beside button takes the time\u2019s place on hover, not a column', () => {
    render(
      <TooltipProvider delayDuration={0}>
        <WorkRail
          sessions={[working, asking, done]}
          selected={addressOf(done)}
          onOpen={vi.fn()}
          onOpenBeside={vi.fn()}
          pinned={new Set()}
          onTogglePin={vi.fn()}
        />
      </TooltipProvider>,
    )
    const first = rows()[0]
    // No permanent padding for the button from the shell breakpoint up (a title had 103 px
    // of 300 left); a phone paints the button always and keeps its column.
    const cls = first.className.split(/\s+/)
    expect(cls).not.toContain('pr-12')
    expect(cls).toContain('max-[760px]:pr-12')
    // The time yields to the button by fading on hover: it is never `visibility:hidden`
    // (that leaves the row's accessible name) and a focused row keeps it.
    const time = first.querySelector('[data-testid="rail-row-state"]')!
      .parentElement as HTMLElement
    expect(time.querySelector('time')).not.toBeNull()
    const timeCls = time.className.split(/\s+/)
    expect(timeCls).toContain('min-[761px]:group-hover/row:opacity-0')
    expect(time.className).not.toMatch(/invisible|focus-within/)
    // The button yields the same way: it appears on hover (always on a phone) and NEVER on
    // focus. A clicked or arrowed-to row matches `group-focus-within/row` with the pointer
    // elsewhere, and the button is absolutely placed over the time's last 40 px, with
    // the reserve gone: painting it there covered the time and state of the focused row.
    const button = first.parentElement?.querySelector('button')
    expect(button).not.toBeNull()
    const buttonCls = (button as HTMLElement).className.split(/\s+/)
    expect(buttonCls).toContain('group-hover/row:opacity-100')
    expect(buttonCls).toContain('max-[760px]:opacity-100')
    expect((button as HTMLElement).className).not.toMatch(/focus-within/)
    // A touch screen has no hover to reveal it (`group-hover` is gated by
    // `(hover: hover)`), and a tap used to reveal it by focus: a coarse pointer paints the
    // button always and keeps its column, exactly as a phone does.
    expect(cls).toContain('pointer-coarse:pr-12')
    expect(buttonCls).toContain('pointer-coarse:opacity-100')
  })

  it('puts the folder\u2019s path on the hover only for a folder an operator chose', () => {
    const base = {
      transport: 'stream-json',
      permission_mode: 'default',
      isolation: 'native',
      state: 'running',
      last_event_seq: 0,
      pep_provisioned: true,
      record_io: false,
      critical: false,
      provider_driver: 'claude',
    } as const
    const chosen = mergeSessions(
      [],
      [
        {
          ...base,
          run_ref: 'run-c',
          name: 'fix',
          workspace_path: '/srv/work/repo',
        },
      ],
    )[0]
    const made = mergeSessions(
      [],
      [
        {
          ...base,
          run_ref: 'run-m',
          name: 'fix',
          workspace_path: '/var/lib/olivares/runs/run-m',
        },
      ],
    )[0]
    renderRail({ sessions: [chosen, made] })
    const [a, b] = rows()
    const titles = [a.getAttribute('title'), b.getAttribute('title')]
    expect(titles.some((t) => t?.includes('/srv/work/repo'))).toBe(true)
    expect(titles.some((t) => t?.includes('/var/lib/olivares'))).toBe(false)
  })

  it.each(['compact', 'comfortable'] as const)(
    'a row is one standard %s row high, from the shell breakpoint up',
    (density) => {
      usePreferencesStore.setState({ density })
      try {
        renderRail()
        for (const r of rows()) {
          // Two short lines: a step taller than the shell's one-line row.
          expect(r.className.split(/\s+/)).toEqual(
            expect.arrayContaining([
              'h-14',
              density === 'compact' ? 'min-[761px]:h-11' : 'min-[761px]:h-12',
            ]),
          )
        }
      } finally {
        usePreferencesStore.setState({ density: 'comfortable' })
      }
    },
  )

  it('names the session and its state on the line, and carries the rest on the row', () => {
    renderRail()
    // Needs you first: the working session is the second row.
    const first = rows()[1]
    // ⛔ ONE LINE. The row used to paint the name, then the state with an
    //    elapsed time, then the observed clause — three lines, 92 px measured in a real
    //    browser, in a 256 px rail. What an operator picks a row BY stays painted:
    expect(within(first).getByTestId('rail-row-state')).toHaveTextContent(
      /^Working/,
    )
    // …and the NAME is what the session is doing, never its reference. `sess-a` was
    // the second rung of the old label, so a rail of discovered sessions read as six
    // machine ids. The reference is on the name's own `title`.
    // The title is the tool: the action and resource the engine reported are the
    // secondary fact, on the hover, never the name (#2266).
    const name = within(first).getByTestId('rail-row-name')
    expect(name).toHaveTextContent('Claude Code')
    expect(name.textContent).not.toContain('create_issue')
    expect(name.querySelectorAll('code')).toHaveLength(0)
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

  it('lists the pinned sessions in their own section FIRST, above Needs you', () => {
    // #2130: a pin is "always first in my list". The pinned row leaves its state
    // section (Earlier is not drawn once its only row is pinned) and the section above
    // all the others carries the count like every other.
    renderRail({ pinned: new Set([addressOf(done)]) })
    const pinnedGroup = screen.getByRole('group', { name: /^Pinned\s*1$/ })
    expect(pinnedGroup.contains(rows()[0])).toBe(true)
    const attention = screen.getByRole('group', { name: /^Needs you\s*1$/ })
    expect(
      pinnedGroup.compareDocumentPosition(attention) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy()
    expect(screen.queryByText('Earlier')).toBeNull()
    expect(
      within(pinnedGroup).getByText(/pinned/i, { selector: '.sr-only' }),
    ).toBeTruthy()
  })

  it('draws no Pinned section while nothing is pinned', () => {
    renderRail()
    expect(screen.queryByRole('group', { name: /^Pinned/ })).toBeNull()
  })

  it('groups by what the session needs, as "label count", and draws no empty group', () => {
    renderRail({ sessions: [working] })
    // The group by its name: its one row says Working too, with no time it did not see.
    expect(screen.queryByText('Needs you')).toBeNull()
    expect(screen.queryByText('Earlier')).toBeNull()
    // No sentence stands in for a group that has no rows.
    expect(screen.queryByText(/Nothing is waiting for you/)).toBeNull()
    expect(screen.queryByText(/No session has settled yet/)).toBeNull()
    expect(screen.getByRole('group', { name: /^Working\s*1$/ })).toBeTruthy()
  })

  it('lists Needs you first, then Working, then Earlier', () => {
    renderRail()
    const names = screen
      .getAllByRole('group')
      .map((g) => g.getAttribute('aria-labelledby'))
      .map((id) => document.getElementById(id as string)?.textContent)
    expect(names).toEqual(['Needs you 1', 'Working 1', 'Earlier 1'])
    expect(rows().map((r) => r.getAttribute('data-address'))).toEqual([
      'live:lr-b',
      'live:lr-a',
      'live:lr-c',
    ])
  })

  it('keeps snoozed sessions out of the list, folded behind one toggle under it', async () => {
    // #2480: a snooze takes the session out of the person's list until its moment; the
    // toggle keeps it one click away so a snooze is never a way to lose a session.
    const user = userEvent.setup()
    const snoozed = mergeSessions(
      [],
      [
        {
          ...launchedRow('run-z').runs[0],
          snoozed_until: '2030-01-02T09:00:00Z',
        },
      ],
    )[0]
    renderRail({ sessions: [working, snoozed] })
    expect(rows().map((r) => r.getAttribute('data-address'))).toEqual([
      'live:lr-a',
    ])
    const toggle = screen.getByRole('button', { name: /^Snoozed\s*1$/ })
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
    // Outside the listbox: a listbox owns options, never a button.
    expect(screen.getByRole('listbox').contains(toggle)).toBe(false)

    await user.click(toggle)
    expect(toggle).toHaveAttribute('aria-expanded', 'true')
    const group = screen.getByRole('group', { name: /^Snoozed\s*1$/ })
    expect(group.contains(rows()[rows().length - 1])).toBe(true)
    expect(rows().map((r) => r.getAttribute('data-address'))).toEqual([
      'live:lr-a',
      addressOf(snoozed),
    ])
  })

  it('draws no empty listbox when every session is snoozed, only the toggle', async () => {
    // A listbox with no option is a broken ARIA tree; folded, there is nothing in it.
    const user = userEvent.setup()
    const snoozed = mergeSessions(
      [],
      [
        {
          ...launchedRow('run-z').runs[0],
          snoozed_until: '2030-01-02T09:00:00Z',
        },
      ],
    )[0]
    renderRail({ sessions: [snoozed] })
    expect(screen.queryByRole('listbox')).toBeNull()
    await user.click(screen.getByRole('button', { name: /^Snoozed\s*1$/ }))
    expect(rows().map((r) => r.getAttribute('data-address'))).toEqual([
      addressOf(snoozed),
    ])
  })

  it('draws no Snoozed toggle while nothing is snoozed', () => {
    renderRail()
    expect(screen.queryByRole('button', { name: /^Snoozed/ })).toBeNull()
  })

  it('marks the open row with the neutral fill, not the accent', () => {
    renderRail({ selected: 'live:lr-a' })
    const open = rows().find((r) => r.getAttribute('aria-selected') === 'true')!
    const cls = open.className.split(/\s+/)
    expect(cls).toContain('bg-active')
    expect(open.className).not.toMatch(/accent/)
    expect(cls).not.toContain('border-l-2')
  })
})

describe('WorkRail — arrows move, Enter opens', () => {
  it('is ONE tab stop, and lands on the first row', async () => {
    const user = userEvent.setup()
    renderRail()
    await tabIntoRows(user)
    // A rail of two hundred sessions must not be two hundred tab stops.
    expect(document.activeElement).toBe(rows()[0])
    await tabIntoRows(user)
    expect(rows()).not.toContain(document.activeElement)
  })

  it('moves focus without opening anything', async () => {
    const user = userEvent.setup()
    const { onOpen } = renderRail()
    await tabIntoRows(user)
    await user.keyboard('{ArrowDown}')
    expect(document.activeElement).toBe(rows()[1])
    // THE POINT: walking the rail must not fire a navigation — and on this screen a
    // navigation is a request — per keystroke.
    expect(onOpen).not.toHaveBeenCalled()
  })

  it('opens the focused row on Enter', async () => {
    const user = userEvent.setup()
    const { onOpen } = renderRail()
    await tabIntoRows(user)
    await user.keyboard('{ArrowDown}{Enter}')
    expect(onOpen).toHaveBeenCalledTimes(1)
    expect(onOpen.mock.calls[0][0].sessionRef).toBe('sess-a')
  })

  it('does not wrap at either end', async () => {
    const user = userEvent.setup()
    renderRail()
    await tabIntoRows(user)
    await user.keyboard('{ArrowUp}')
    expect(document.activeElement).toBe(rows()[0])
    await user.keyboard('{End}{ArrowDown}')
    expect(document.activeElement).toBe(rows()[rows().length - 1])
  })

  it('starts on the OPEN session, so a deep link can be walked away from', async () => {
    const user = userEvent.setup()
    renderRail({ selected: 'live:lr-c' })
    await tabIntoRows(user)
    expect(document.activeElement).toBe(
      screen
        .getByTestId('work-rail')
        .querySelector('[data-address="live:lr-c"]'),
    )
  })

  it('marks the open session as selected, which focus alone does not', async () => {
    const user = userEvent.setup()
    renderRail({ selected: 'live:lr-a' })
    await tabIntoRows(user)
    await user.keyboard('{ArrowDown}')
    expect(rows()[1]).toHaveAttribute('aria-selected', 'true')
    expect(rows()[2]).toHaveAttribute('aria-selected', 'false')
  })
})

describe('WorkRail — no gesture without a keyboard path', () => {
  it('pins the focused row with `p`', async () => {
    const user = userEvent.setup()
    const { onTogglePin } = renderRail({
      sessions: [launchedRow('run-a'), launchedRow('run-b')],
    })
    await tabIntoRows(user)
    await user.keyboard('p')
    expect(onTogglePin).toHaveBeenCalledWith('run:run-a')
  })

  it('pins the row the keyboard is ON, not the one that is open', async () => {
    const user = userEvent.setup()
    const { onTogglePin } = renderRail({
      sessions: [launchedRow('run-a'), launchedRow('run-b')],
      selected: 'run:run-a',
    })
    await tabIntoRows(user)
    await user.keyboard('{ArrowDown}p')
    expect(onTogglePin).toHaveBeenCalledWith('run:run-b')
  })

  it('does nothing on `p` for a session with no run: the engine has nothing to pin it on', async () => {
    // An observed session. The pin is kept by the engine against a run, so the key is
    // not bound here rather than bound to a no-op.
    const user = userEvent.setup()
    const { onTogglePin } = renderRail()
    await tabIntoRows(user)
    await user.keyboard('p')
    expect(onTogglePin).not.toHaveBeenCalled()
  })

  it('does nothing on `p` when there is nowhere to store a preference', async () => {
    // A token principal, or no tenant. A key that silently does nothing is worse than
    // one that is not bound, so the rail does not bind it either.
    const user = userEvent.setup()
    const { onOpen } = renderRail({ onTogglePin: null })
    await tabIntoRows(user)
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
    await tabIntoRows(user)
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

describe('WorkRail — every row has one actions menu (#2795)', () => {
  // The menu's items are the caller's (`session-menu.tsx`); the rail owns the three ways
  // to open them on one row and where focus goes after.
  function renderMenuRail(over: Partial<Parameters<typeof WorkRail>[0]> = {}) {
    const rowMenu = vi.fn((s: UnifiedSession) => (
      <DropdownMenuItem>{`Rename ${addressOf(s)}`}</DropdownMenuItem>
    ))
    const onOpen = vi.fn()
    const onTogglePin = vi.fn()
    const utils = render(
      <TooltipProvider>
        <WorkRail
          sessions={[launchedRow('run-a'), launchedRow('run-b')]}
          selected={null}
          onOpen={onOpen}
          pinned={new Set()}
          onTogglePin={onTogglePin}
          rowMenu={rowMenu}
          {...over}
        />
      </TooltipProvider>,
    )
    return { ...utils, rowMenu, onOpen, onTogglePin }
  }

  it('opens on a right-click, instead of the browser’s own menu', () => {
    renderMenuRail()
    const second = rows()[1]
    // fireEvent answers false when a handler prevented the default.
    expect(fireEvent.contextMenu(second)).toBe(false)
    const menu = screen.getByRole('menu')
    expect(
      within(menu).getByRole('menuitem', { name: 'Rename run:run-b' }),
    ).toBeInTheDocument()
  })

  it('opens from the row’s ⋯ button, which is no control inside the option', async () => {
    const user = userEvent.setup()
    renderMenuRail()
    const more = screen.getAllByTestId('rail-row-menu')[0]
    // The pointer's shortcut, like Open beside: outside the option, out of the tab
    // order and the accessibility tree; the keyboard has Shift+F10.
    expect(rows()[0].contains(more)).toBe(false)
    expect(more).toHaveAttribute('tabindex', '-1')
    expect(more).toHaveAttribute('aria-hidden', 'true')
    await user.click(more)
    expect(
      within(screen.getByRole('menu')).getByRole('menuitem', {
        name: 'Rename run:run-a',
      }),
    ).toBeInTheDocument()
  })

  it.each([['{Shift>}{F10}{/Shift}'], ['{ContextMenu}']])(
    'opens for the focused row from the keyboard (%s), and gives focus back to it',
    async (keys) => {
      const user = userEvent.setup()
      renderMenuRail()
      await tabIntoRows(user)
      await user.keyboard('{ArrowDown}')
      await user.keyboard(keys)
      const menu = screen.getByRole('menu')
      expect(
        within(menu).getByRole('menuitem', { name: 'Rename run:run-b' }),
      ).toBeInTheDocument()
      await user.keyboard('{Escape}')
      expect(screen.queryByRole('menu')).toBeNull()
      expect(document.activeElement).toBe(rows()[1])
    },
  )

  it('opens for the row that HAS focus, after a click moved it there', async () => {
    // The keyboard's row is the one the DOM focused, whatever moved it: a right-click
    // on one row and then a click on another must not leave Shift+F10 on the first.
    const user = userEvent.setup()
    renderMenuRail()
    fireEvent.contextMenu(rows()[0])
    await user.keyboard('{Escape}')
    await user.click(rows()[1])
    await user.keyboard('{Shift>}{F10}{/Shift}')
    expect(
      within(screen.getByRole('menu')).getByRole('menuitem', {
        name: 'Rename run:run-b',
      }),
    ).toBeInTheDocument()
  })

  it('keeps the keys pressed in the open menu to the menu: none reaches the rail', async () => {
    // The menu is portalled out of the rail's DOM but not out of its React tree, so its
    // key events bubble to the rail's handler. Enter on an item must not also open the
    // session, nor `p` (typeahead) pin it, nor an arrow move the rail.
    const user = userEvent.setup()
    const { onOpen, onTogglePin } = renderMenuRail()
    await tabIntoRows(user)
    await user.keyboard('{Shift>}{F10}{/Shift}')
    const item = within(screen.getByRole('menu')).getByRole('menuitem', {
      name: 'Rename run:run-a',
    })
    item.focus()
    await user.keyboard('p')
    await user.keyboard('{ArrowDown}')
    await user.keyboard('{Enter}')
    expect(onOpen).not.toHaveBeenCalled()
    expect(onTogglePin).not.toHaveBeenCalled()
    expect(rows()[1]).toHaveAttribute('tabindex', '-1')
  })

  it('draws no ⋯ and keeps the browser menu when there is no menu to offer', () => {
    renderMenuRail({ rowMenu: undefined })
    expect(screen.queryByTestId('rail-row-menu')).toBeNull()
    expect(fireEvent.contextMenu(rows()[0])).toBe(true)
  })
})

// The list says what each session is doing in a word with its time, the place above
// the title, and the tool by its own mark, not one glyph for every row.
describe('WorkRail — a row says its state in words, its place and its tool', () => {
  const launched = (over: Partial<RunDTO>) =>
    mergeSessions(
      [],
      [
        {
          run_ref: 'run-x',
          name: 'Fix the checkout totals',
          transport: 'stream-json',
          permission_mode: 'default',
          isolation: 'native',
          state: 'running',
          last_event_seq: 0,
          pep_provisioned: true,
          record_io: false,
          critical: false,
          provider_driver: 'claude',
          workspace_path: '/home/dev/payments-api',
          last_activity_at: new Date().toISOString(),
          ...over,
        },
      ],
    )[0]
  const rail = (s: UnifiedSession) =>
    render(
      <WorkRail
        sessions={[s]}
        selected={null}
        onOpen={vi.fn()}
        pinned={new Set()}
        onTogglePin={vi.fn()}
      />,
    )
  const state = () => screen.getByTestId('rail-row-state')

  it('says Working with how long it has worked, the folder above the name and the tool as its mark', () => {
    rail(launched({ started_at: new Date(Date.now() - 12_000).toISOString() }))
    const rowEl = screen.getByTestId('rail-row')
    expect(state()).toHaveTextContent(/^Working 1[2-4]s$/)
    expect(within(rowEl).getByTestId('rail-row-place')).toHaveTextContent(
      'payments-api',
    )
    expect(within(rowEl).getByTestId('tool-mark')).toHaveTextContent('CC')
    expect(within(rowEl).getByTestId('tool-mark')).toHaveAccessibleName(
      'Claude Code',
    )
    expect(rowEl.querySelector('svg.lucide-square-terminal')).toBeNull()
  })

  it.each([
    [{ state: 'failed' as const }, 'Failed'],
    [{ state: 'idle' as const }, 'Done'],
    [{ state: 'stopped' as const, reason: 'stopped by a person' }, 'Stopped'],
    [{ state: 'running' as const, pending_approval_ref: 'apr_1' }, 'Needs you'],
  ])('says %o as %s', (over, word) => {
    rail(launched(over))
    expect(state()).toHaveTextContent(new RegExp(`^${word}`))
  })

  it('says a session that is not working ended how long ago in one short unit', () => {
    rail(
      launched({
        state: 'idle',
        last_activity_at: new Date(Date.now() - 5 * 60_000).toISOString(),
      }),
    )
    const time = screen.getByTestId('rail-row').querySelector('time')!
    expect(time).toHaveTextContent(/^5m$/)
    expect(time.getAttribute('title')).toBeTruthy()
  })

  it('says Pending, with no clock, for a run that has not started', () => {
    rail(
      launched({
        run_ref: 'run-p',
        state: 'pending',
        created_at: new Date(Date.now() - 12 * 60_000).toISOString(),
      }),
    )
    expect(state()).toHaveTextContent(/^Pending$/)
  })

  it('times no turn it did not see start: a session up for hours says Working', () => {
    rail(
      launched({
        run_ref: 'run-3h',
        started_at: new Date(Date.now() - 3 * 3_600_000).toISOString(),
      }),
    )
    expect(state()).toHaveTextContent(/^Working$/)
  })

  it('times the turn it saw start, from that moment, in whole seconds', async () => {
    const at = (s: RunDTO['state']) =>
      launched({
        run_ref: 'run-turn',
        state: s,
        started_at: new Date(Date.now() - 3 * 3_600_000).toISOString(),
      })
    const props = {
      selected: null,
      onOpen: vi.fn(),
      pinned: new Set<string>(),
      onTogglePin: vi.fn(),
    }
    const view = render(<WorkRail sessions={[at('idle')]} {...props} />)
    view.rerender(<WorkRail sessions={[at('running')]} {...props} />)
    // The row is in the shut Working fold when the turn starts: the rail noted the
    // moment, and the row opened later reads it.
    fireEvent.click(screen.getByRole('button', { name: /^Working\s*1$/ }))
    await waitFor(() => expect(state()).toHaveTextContent(/^Working [0-9]s$/))
    // The person leaves the list; back later, the session may be in another turn: the
    // list never times it from the turn it saw before.
    view.unmount()
    render(<WorkRail sessions={[at('running')]} {...props} />)
    await new Promise((r) => setTimeout(r, 20))
    expect(state()).toHaveTextContent(/^Working$/)
  })

  it('gives a working row its clock and every other row its age', () => {
    const working = rail(launched({}))
    expect(screen.getByTestId('rail-row').querySelector('time')).toBeNull()
    working.unmount()
    rail(launched({ state: 'failed' }))
    expect(screen.getByTestId('rail-row').querySelector('time')).not.toBeNull()
  })

  it('draws every mark in capitals, a one-word tool too', () => {
    rail(launched({ provider_driver: 'codex' }))
    expect(screen.getByTestId('tool-mark')).toHaveTextContent(/^CO$/)
  })

  it('says a seen session that ended Done, the word the list uses for a finished turn', () => {
    const [ended] = mergeSessions(
      [
        {
          session_ref: 'sess-e',
          live_ref: 'lr-e',
          attribution: 'observed',
          cc_state: 'ended',
          input_tokens: 0,
          output_tokens: 0,
          cost_micro_usd: 0,
          event_count: 3,
          tool_call_count: 0,
          first_event_at: new Date(Date.now() - 60_000).toISOString(),
          last_event_at: new Date(Date.now() - 30_000).toISOString(),
          duration_seconds: 30,
        } as LiveDTO,
      ],
      [],
    )
    rail(ended!)
    expect(state()).toHaveTextContent(/^Done$/)
  })
})

// Working sessions fold into one shut group, so a running session never moves a row under
// the pointer.
/** The rail exactly as it arrives: the Working fold shut. */
function renderShut(over: Partial<Parameters<typeof WorkRail>[0]> = {}) {
  return renderRaw(
    <WorkRail
      sessions={[working, asking, done]}
      selected={null}
      onOpen={vi.fn()}
      pinned={new Set()}
      onTogglePin={vi.fn()}
      {...over}
    />,
  )
}

describe('WorkRail — the Working fold', () => {
  const addresses = () => rows().map((r) => r.getAttribute('data-address'))
  const toggle = () => screen.getByRole('button', { name: /^Working\s*1$/ })

  it('folds the working sessions behind one toggle, shut, with its count', () => {
    renderShut()
    expect(toggle()).toHaveAttribute('aria-expanded', 'false')
    expect(addresses()).toEqual(['live:lr-b', 'live:lr-c'])
  })

  it('opens in place, between Needs you and Earlier, and shuts again', async () => {
    const user = userEvent.setup()
    renderShut()
    await user.click(toggle())
    expect(toggle()).toHaveAttribute('aria-expanded', 'true')
    expect(addresses()).toEqual(['live:lr-b', 'live:lr-a', 'live:lr-c'])
    await user.click(toggle())
    expect(addresses()).toEqual(['live:lr-b', 'live:lr-c'])
  })

  it('arrives open when the session on screen is working', () => {
    renderShut({ selected: 'live:lr-a' })
    expect(toggle()).toHaveAttribute('aria-expanded', 'true')
    expect(addresses()).toContain('live:lr-a')
  })

  it('does not walk the folded rows with the arrow keys', async () => {
    const user = userEvent.setup()
    renderShut()
    rows()[0].focus()
    await user.keyboard('{ArrowDown}')
    expect(rows()[1]).toHaveFocus()
    expect(rows()[1].getAttribute('data-address')).toBe('live:lr-c')
  })

  it('draws a rail whose every session is working as the toggle alone', async () => {
    const user = userEvent.setup()
    renderShut({ sessions: [working] })
    expect(screen.queryAllByRole('option')).toHaveLength(0)
    await user.click(toggle())
    expect(addresses()).toEqual(['live:lr-a'])
  })

  it('names the state in the row, with its mark, whatever its group', async () => {
    const user = userEvent.setup()
    renderShut()
    await user.click(toggle())
    const byAddress = (a: string) =>
      rows().find((r) => r.getAttribute('data-address') === a)!
    for (const [address, status, word] of [
      ['live:lr-b', 'needs-you', 'Needs you'],
      ['live:lr-a', 'working', 'Working'],
      ['live:lr-c', 'done', 'Done'],
    ] as const) {
      const state = within(byAddress(address)).getByTestId('rail-row-state')
      expect(state).toHaveAttribute('data-status', status)
      expect(state).toHaveTextContent(new RegExp(`^${word}`))
      expect(byAddress(address)).toHaveAccessibleName(new RegExp(word))
    }
  })

  it('has no aria-required-children violation, open, shut or all working', async () => {
    const user = userEvent.setup()
    const violations = async () =>
      (
        await axe.run(document.body, {
          runOnly: { type: 'rule', values: ['aria-required-children'] },
        })
      ).violations
    const { unmount } = renderShut()
    expect(await violations()).toEqual([])
    await user.click(toggle())
    expect(await violations()).toEqual([])
    unmount()
    renderShut({ sessions: [working] })
    expect(await violations()).toEqual([])
  })
})
