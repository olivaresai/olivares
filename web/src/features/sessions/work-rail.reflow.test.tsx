// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A RAIL ROW IS ONE LINE AND CUTS ITS TITLE, NEVER ITS STATE OR ITS TIME. The row is the
// list's "find the session" instrument: tool icon, a one-line title (cut with an ellipsis;
// the whole name, the tail and the reference are on its hover), and on the right the
// state dot and the relative time, each whole. In German the old row spent 146 px
// on a state badge; a dot has no width to lose. jsdom has no layout, so this pins the
// arrangement; the browser capture at 1280 px in German and Japanese measures overflow.
import { render, screen, within } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { mergeSessions } from './provenance'
import type { LiveDTO } from './types'
import { WorkRail } from './work-rail'
import './i18n'

function live(over: Partial<LiveDTO>): LiveDTO {
  return {
    session_ref: 'sess-a',
    live_ref: 'lr-a',
    attribution: 'observed',
    cc_state: 'silent_evasion',
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

const classes = (el: Element) => el.className.split(/\s+/)

function renderRow() {
  const name =
    'Open a PR fixing the payments rounding bug in the invoicing service'
  const session = mergeSessions([live({ summary: name })], [])[0]
  render(
    <div style={{ width: 280 }}>
      <WorkRail
        sessions={[session]}
        selected={null}
        onOpen={vi.fn()}
        pinned={new Set()}
        onTogglePin={vi.fn()}
      />
    </div>,
  )
  const rowEl = screen.getByTestId('rail-row')
  return {
    name,
    rowEl,
    nameEl: within(rowEl).getByTestId('rail-row-name'),
    state: within(rowEl).getByTestId('rail-row-state'),
    time: rowEl.querySelector('time') as HTMLElement,
  }
}

describe('a rail row is two short lines (dev#3162)', () => {
  it('the title is cut to one line and its whole text stays on the hover', () => {
    const { name, nameEl, rowEl } = renderRow()
    expect(nameEl).toHaveTextContent(name)
    expect(nameEl.getAttribute('title')).toContain(name)
    expect(classes(nameEl)).toEqual(
      expect.arrayContaining(['truncate', 'min-w-0']),
    )
    expect(classes(rowEl)).toEqual(
      expect.arrayContaining(['h-14', 'min-[761px]:h-12']),
    )
    expect(classes(rowEl)).not.toContain('flex-wrap')
  })

  it('the state dot and the time are whole: they never shrink, cut or break', () => {
    const { state, time } = renderRow()
    expect(time).not.toBeNull()
    expect(classes(state)).toContain('shrink-0')
    expect(classes(time)).toEqual(
      expect.arrayContaining(['shrink-0', 'whitespace-nowrap', 'tabular-nums']),
    )
    expect(classes(time)).not.toContain('truncate')
  })

  // The order (dev#3162): the place, the state and its time on the first line, the
  // name under them.
  it('reads the state, then its time, then the name under them', () => {
    const { rowEl, nameEl, state, time } = renderRow()
    const order = Array.from(rowEl.querySelectorAll('*'))
    expect(order.indexOf(state)).toBeLessThan(order.indexOf(time))
    expect(order.indexOf(time)).toBeLessThan(order.indexOf(nameEl))
  })
})
