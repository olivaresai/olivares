// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A RECENT-SESSION ROW GROWS; IT DOES NOT CUT. The row was fixed at the list-row height and its
// sentence (what the session did, then the facts the engine reported) cut itself at the edge:
// at 1280 px in German, 817 px of sentence in an 810 px box. The row keeps the list-row height
// as its least height and the sentence wraps; the state badge and the time stay whole. It stays
// a link (a list of sessions, not a table). jsdom has no layout, so this pins the arrangement;
// the browser capture at 1280 px in German and Japanese measures that nothing overflows.
import type { ReactNode } from 'react'
import { describe, expect, it, vi } from 'vitest'
import { renderIntel, screen } from '@/test/intel'
import type { LiveDTO } from '@/features/sessions/types'
import { RecentWork } from './recent-work'
import './i18n'
import '@/features/sessions/i18n'

vi.mock('@tanstack/react-router', () => ({
  useRouterState: () => '',
  Link: ({
    children,
    to,
    search,
    ...rest
  }: {
    children: ReactNode
    to: string
    search?: Record<string, string>
  } & Record<string, unknown>) => {
    const query = new URLSearchParams(search ?? {}).toString()
    return (
      <a href={query ? `${to}?${query}` : to} {...rest}>
        {children}
      </a>
    )
  },
}))

const CELL =
  'td, th, [role="cell"], [role="gridcell"], [role="rowheader"], [role="columnheader"]'
const classes = (el: Element) => el.className.split(/\s+/)

function row(): LiveDTO {
  return {
    session_ref: 'sess-9f2a',
    live_ref: 'lr-9f2a',
    attribution: 'observed',
    cc_state: 'active',
    input_tokens: 0,
    output_tokens: 0,
    cost_micro_usd: 184200,
    event_count: 64,
    tool_call_count: 22,
    first_event_at: '2026-09-18T09:00:00Z',
    last_event_at: '2026-09-18T09:31:00Z',
    duration_seconds: 1860,
    summary: 'Open a PR fixing the payments rounding bug',
  }
}

describe('a recent-session row grows instead of cutting', () => {
  it('the sentence is whole and wraps; the row keeps the list-row height as its least height', () => {
    renderIntel(<RecentWork sessions={[row()]} state="ready" canStartSession />)
    const rowEl = screen.getByTestId('home-recent-row')
    expect(rowEl.querySelectorAll('.truncate')).toHaveLength(0)
    expect(classes(rowEl)).toContain('min-h-[var(--console-list-row-height)]')
    expect(classes(rowEl)).not.toContain('h-[var(--console-list-row-height)]')
    const sentence = rowEl.querySelector(
      '[data-slot="recent-row-sentence"]',
    ) as HTMLElement
    expect(sentence).not.toBeNull()
    expect(sentence).toHaveTextContent(
      /Open a PR fixing the payments rounding bug/,
    )
    expect(classes(sentence)).toContain('[overflow-wrap:anywhere]')
  })

  it('the state badge and the time stay whole', () => {
    renderIntel(<RecentWork sessions={[row()]} state="ready" canStartSession />)
    const rowEl = screen.getByTestId('home-recent-row')
    const badge = screen.getByText('Active').closest('span') as HTMLElement
    const time = rowEl.querySelector('time') as HTMLElement
    for (const el of [badge, time]) {
      expect(classes(el)).toEqual(
        expect.arrayContaining(['shrink-0', 'whitespace-nowrap']),
      )
      expect(classes(el)).not.toContain('truncate')
    }
  })

  it('the row stays a link in a list: no cell role on it, inside it or around it', () => {
    renderIntel(<RecentWork sessions={[row()]} state="ready" canStartSession />)
    const rowEl = screen.getByTestId('home-recent-row')
    expect(rowEl.tagName).toBe('A')
    expect(rowEl.closest(CELL)).toBeNull()
    expect(rowEl.querySelector(CELL)).toBeNull()
  })
})
