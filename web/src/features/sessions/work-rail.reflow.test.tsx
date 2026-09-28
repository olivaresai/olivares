// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A RAIL ROW REFLOWS; IT DOES NOT CUT. In a 256 px rail, German put a state badge of 146 px and
// a relative time of 85 px on the one line the row had, and the session's name got 0 px: the
// badge, the name and the time were all cut, with the rest on a hover title that no keyboard
// or touch user can open. The row now puts the state and the time on its first line, each
// whole, and the name on its own line, whole and wrapping. The reading order stays state,
// name, time. jsdom has no layout, so this pins the arrangement; the browser capture at
// 1280 px in German and Japanese measures that nothing overflows.
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
    <div style={{ width: 256 }}>
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
    badge: rowEl.querySelector('[data-slot="rail-row-state"]') as HTMLElement,
    time: rowEl.querySelector('time') as HTMLElement,
  }
}

describe('a rail row reflows instead of cutting', () => {
  it('the name is whole: no truncation, it wraps, on a line of its own', () => {
    const { name, nameEl, rowEl } = renderRow()
    expect(nameEl).toHaveTextContent(name)
    expect(classes(nameEl)).not.toContain('truncate')
    expect(classes(nameEl)).toContain('basis-full')
    expect(classes(nameEl)).toContain('[overflow-wrap:anywhere]')
    expect(classes(rowEl)).toContain('flex-wrap')
  })

  it('the state badge and the time are whole: they never shrink, cut or break', () => {
    const { badge, time } = renderRow()
    expect(badge).not.toBeNull()
    expect(time).not.toBeNull()
    for (const el of [badge, time]) {
      expect(classes(el)).toEqual(
        expect.arrayContaining(['shrink-0', 'whitespace-nowrap']),
      )
      expect(classes(el)).not.toContain('truncate')
      expect(classes(el)).not.toContain('min-w-0')
    }
  })

  it('reads state, name, time: the order of the row for assistive technology is unchanged', () => {
    const { rowEl, badge, nameEl, time } = renderRow()
    const kids = Array.from(rowEl.children)
    expect(kids.indexOf(badge)).toBeLessThan(kids.indexOf(nameEl))
    expect(kids.indexOf(nameEl)).toBeLessThan(kids.indexOf(time))
    // Painted: the state and the time on the first line, the name below them.
    expect(classes(time)).toContain('order-1')
    expect(classes(nameEl)).toContain('order-2')
  })
})
