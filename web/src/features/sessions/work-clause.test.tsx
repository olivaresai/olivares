// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// When a session has no summary and no goal, the naming ladder names it by what the engine
// reported it doing: the action and the resource, both machine identifiers (`web.search`,
// `appdb.public.customers`). They are shown exactly as sent, in every language, so they render
// in the identifier element (code, machine face), and the words around them stay translated.
// Plain dotted copy is what an untranslated key looks like, and a check for untranslated
// keys cannot tell the two apart.
/// <reference types="node" />
import { readFileSync } from 'node:fs'
import type { ReactNode } from 'react'
import { render, screen, within } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { renderIntel } from '@/test/intel'
import { RecentWork } from '@/features/home/recent-work'
import { mergeSessions } from './provenance'
import type { LiveDTO } from './types'
import { WorkClause } from './work-clause'
import { WorkRail } from './work-rail'

import './i18n'
import '@/features/home/i18n'

/** RecentWork takes the merged sessions (runs and live rows); these fixtures are live rows only. */
const asSessions = (rows: Parameters<typeof mergeSessions>[0]) =>
  mergeSessions(rows, [])

// The recent row is a router link; this double builds the href the router would.
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

const SHAPE = /^[a-z][a-zA-Z0-9_]*(\.[a-zA-Z][a-zA-Z0-9_]*){1,5}$/

/** Dotted text outside code or pre: the shape an untranslated key has. Links are NOT skipped. */
function plainDotted(root: HTMLElement): string[] {
  const out: string[] = []
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT)
  for (let n = walker.nextNode(); n; n = walker.nextNode()) {
    const text = (n.textContent ?? '').trim()
    if (SHAPE.test(text) && !n.parentElement?.closest('code, pre'))
      out.push(text)
  }
  return out
}

function live(over: Partial<LiveDTO> = {}): LiveDTO {
  return {
    session_ref: 'sess-7c1d',
    live_ref: 'lr-7c1d',
    attribution: 'observed',
    cc_state: 'silent_evasion',
    input_tokens: 0,
    output_tokens: 0,
    cost_micro_usd: 0,
    event_count: 6,
    tool_call_count: 2,
    first_event_at: '2026-09-18T09:00:00Z',
    last_event_at: '2026-09-18T09:03:00Z',
    duration_seconds: 180,
    current_action: 'web.search',
    ...over,
  }
}

describe('the action a session reports renders as an identifier', () => {
  it('the action and the resource are code; the separator is plain', () => {
    const s = live({ current_resource: 'appdb.public.customers' })
    const { container } = render(
      <WorkClause text="web.search · appdb.public.customers" live={s} />,
    )
    const codes = Array.from(container.querySelectorAll('code'))
    expect(codes.map((c) => c.textContent)).toEqual([
      'web.search',
      'appdb.public.customers',
    ])
    for (const c of codes) expect(c).toHaveClass('font-mono')
    expect(container).toHaveTextContent('web.search · appdb.public.customers')
    expect(plainDotted(container)).toEqual([])
  })

  it('any other rung (a summary, a goal, a run name) stays plain copy', () => {
    const s = live({ summary: 'Reviewed the migration' })
    const { container } = render(
      <WorkClause text="Reviewed the migration" live={s} />,
    )
    expect(container.querySelector('code')).toBeNull()
    expect(container).toHaveTextContent('Reviewed the migration')
  })

  it('the rail row names such a session with the identifier in code', () => {
    const session = mergeSessions([live()], [])[0]
    render(
      <WorkRail
        sessions={[session]}
        selected={null}
        onOpen={vi.fn()}
        pinned={new Set()}
        onTogglePin={vi.fn()}
      />,
    )
    const rowEl = screen.getByTestId('rail-row')
    const nameEl = within(rowEl).getByTestId('rail-row-name')
    expect(nameEl.querySelector('code')).toHaveTextContent('web.search')
    expect(plainDotted(rowEl)).toEqual([])
  })

  it('the front door row names it the same way', () => {
    renderIntel(<RecentWork sessions={asSessions([live()])} state="ready" />)
    const rowEl = screen.getByTestId('home-recent-row')
    expect(rowEl.querySelector('code')).toHaveTextContent('web.search')
    expect(plainDotted(rowEl)).toEqual([])
  })

  it('the control: the same text in a plain span still reads as a key', () => {
    const { container } = render(<span>web.search</span>)
    expect(plainDotted(container)).toEqual(['web.search'])
  })

  it('every surface that paints the ladder paints it through WorkClause', () => {
    for (const [file, pattern] of [
      [
        'src/features/sessions/work-rail.tsx',
        /<WorkClause\b[^>]*text=\{naming\.name\}/,
      ],
      [
        'src/features/sessions/session-card.tsx',
        /<WorkClause\b[^>]*text=\{naming\.name\}/,
      ],
      [
        'src/features/sessions/session-overview.tsx',
        /<WorkClause\b[^>]*text=\{line\.text\}/,
      ],
      ['src/features/home/recent-work.tsx', /<WorkClause\b[^>]*text=\{name\}/],
    ] as const) {
      expect(readFileSync(file, 'utf8'), file).toMatch(pattern)
    }
  })
})
