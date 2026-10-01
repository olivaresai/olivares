// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE SESSION RAIL: Needs you, Working, Earlier, in that order, each row with a state
// word. A handoff offered to the operator waits for an answer, so it is in "Needs you",
// and its Review link opens it where Accept and Reject are.
import { fireEvent, render, screen, within } from '@testing-library/react'
import type { ComponentProps } from 'react'
import { describe, expect, it, vi } from 'vitest'

vi.mock('@tanstack/react-router', () => ({
  Link: ({
    children,
    to,
    search,
    ...props
  }: ComponentProps<'a'> & {
    to?: string
    search?: Record<string, string>
  }) => (
    <a
      href={search ? `${to}?${new URLSearchParams(search).toString()}` : to}
      {...props}
    >
      {children}
    </a>
  ),
}))

import type { LiveDTO } from '@/features/sessions/types'
import type { HandoffInboxItem } from '@/features/communications/types'
import { railGroups } from './session-rail-model'
import { SessionRailView } from './session-rail'

const NOW = Date.parse('2026-09-27T10:00:00Z')

function live(over: Partial<LiveDTO>): LiveDTO {
  return {
    session_ref: 'sess-1',
    live_ref: 'lr-1',
    attribution: 'legacy',
    cc_state: 'active',
    input_tokens: 0,
    output_tokens: 0,
    cost_micro_usd: 0,
    event_count: 1,
    tool_call_count: 0,
    first_event_at: '2026-09-27T09:50:00Z',
    last_event_at: '2026-09-27T09:58:00Z',
    duration_seconds: 480,
    ...over,
  } as LiveDTO
}

const HANDOFF = {
  carrier: {
    channel_id: '00000000-0000-4000-8000-000000000001',
    message_id: '00000000-0000-4000-8000-000000000002',
    delivery_id: '00000000-0000-4000-8000-0000000000d1',
    delivery_version: 1,
  },
  deadline_elapsed: false,
  handoff: {
    id: '00000000-0000-4000-8000-0000000000a1',
    state: 'offered',
    created_at: '2026-09-27T09:51:00Z',
    from: { kind: 'agent', ref: 'codex-1' },
    to: { kind: 'user', ref: 'user:sam' },
  },
  work_item: { id: 'wi_calibrate_dome_3' },
} as unknown as HandoffInboxItem

describe('railGroups', () => {
  it('uses the shared naming ladder and the observed action when no title exists', () => {
    const rows = railGroups(
      {
        live: [
          live({
            session_ref: 'action',
            current_action: 'create_issue',
            current_resource: 'github/create_issue',
          }),
          live({
            session_ref: 'summary',
            summary: 'Review ready',
            goal: 'Review changes',
          }),
          live({ session_ref: 'empty', model_ref: 'model-a' }),
        ],
        handoffs: [],
      },
      NOW,
    )[1].rows
    expect(rows.map((row) => row.title)).toEqual([
      'create_issue · github/create_issue',
      'Review ready',
      null,
    ])
    expect(rows[2].reference).toBe('empty')
  })

  it('puts a handoff offered to the operator in Needs you, above the sessions that ask', () => {
    const groups = railGroups(
      {
        live: [
          live({ session_ref: 'a', goal: 'Move stream ingest to async' }),
          live({
            session_ref: 'b',
            cc_state: 'silent_evasion',
            goal: 'Publish camera calibration',
          }),
          live({
            session_ref: 'c',
            cc_state: 'ended',
            goal: 'Fix the scene switcher',
          }),
        ],
        handoffs: [HANDOFF],
      },
      NOW,
    )
    expect(groups.map((g) => g.id)).toEqual(['needsYou', 'working', 'earlier'])
    const [needs, working, earlier] = groups
    expect(needs.rows.map((r) => r.kind)).toEqual(['handoff', 'session'])
    expect(needs.rows[0]).toMatchObject({
      state: 'need',
      from: 'codex-1',
      href: '/communications/handoffs?handoff=00000000-0000-4000-8000-0000000000d1',
    })
    expect(working.rows.map((r) => r.title)).toEqual([
      'Move stream ingest to async',
    ])
    expect(earlier.rows.map((r) => r.title)).toEqual(['Fix the scene switcher'])
  })
})

describe('SessionRailView', () => {
  it('shows the handoff in Needs you, with its sender and a Review link to the handoff', () => {
    const groups = railGroups({ live: [], handoffs: [HANDOFF] }, NOW)
    render(<SessionRailView groups={groups} status="ready" />)
    const needs = screen.getByRole('group', { name: 'Needs you' })
    const row = within(needs).getByRole('link', {
      name: /Handoff to you · from codex-1/,
    })
    expect(row).toHaveAttribute(
      'href',
      '/communications/handoffs?handoff=00000000-0000-4000-8000-0000000000d1',
    )
    // The state is a word, not a colour alone.
    expect(
      within(row).getByRole('img', { name: 'Needs you' }),
    ).toBeInTheDocument()
  })

  it('names each row by its state word and keeps the group order fixed', () => {
    const groups = railGroups(
      {
        live: [
          live({ session_ref: 'a', goal: 'Nightly sky-survey report' }),
          live({
            session_ref: 'c',
            cc_state: 'ended',
            goal: 'Review dome-control changes',
          }),
        ],
        handoffs: [],
      },
      NOW,
    )
    render(<SessionRailView groups={groups} status="ready" />)
    const names = screen
      .getAllByRole('group')
      .map((g) => g.getAttribute('aria-label'))
    expect(names).toEqual(['Needs you', 'Working', 'Earlier'])
    expect(
      screen.getByRole('link', { name: /Working.*Nightly sky-survey report/ }),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('link', { name: /Ended.*Review dome-control changes/ }),
    ).toBeInTheDocument()
  })
})

describe('SessionRailView from a keyboard', () => {
  const groups = () =>
    railGroups(
      {
        live: [
          live({ session_ref: 'a', goal: 'Move stream ingest to async' }),
          live({ session_ref: 'b', goal: 'Nightly sky-survey report' }),
          live({
            session_ref: 'c',
            cc_state: 'ended',
            goal: 'Review dome-control changes',
          }),
        ],
        handoffs: [HANDOFF],
      },
      NOW,
    )
  const rows = () =>
    Array.from(document.querySelectorAll<HTMLElement>('[data-rail-row]'))

  it('is ONE tab stop: the first row, and the rest wait for the arrows', () => {
    render(<SessionRailView groups={groups()} status="ready" />)
    expect(rows()).toHaveLength(4)
    expect(rows().filter((r) => r.tabIndex === 0)).toEqual([rows()[0]])
  })

  it("moves focus and the tab stop with the arrows, Home and End (the table's rail keys)", () => {
    render(<SessionRailView groups={groups()} status="ready" />)
    rows()[0].focus()
    fireEvent.keyDown(rows()[0], { key: 'ArrowDown' })
    expect(rows()[1]).toHaveFocus()
    expect(rows()[1].tabIndex).toBe(0)
    expect(rows()[0].tabIndex).toBe(-1)
    fireEvent.keyDown(rows()[1], { key: 'End' })
    expect(rows()[3]).toHaveFocus()
    fireEvent.keyDown(rows()[3], { key: 'ArrowDown' })
    expect(rows()[3]).toHaveFocus()
    fireEvent.keyDown(rows()[3], { key: 'Home' })
    expect(rows()[0]).toHaveFocus()
    fireEvent.keyDown(rows()[0], { key: 'ArrowUp' })
    expect(rows()[0]).toHaveFocus()
  })
})

describe('identifiers in the rail', () => {
  it('sets a work item id and an untitled session id in mono beside their localized label', () => {
    const groups = railGroups(
      {
        live: [live({ session_ref: 'sess-9f2a41c7', goal: undefined })],
        handoffs: [HANDOFF],
      },
      NOW,
    )
    render(<SessionRailView groups={groups} status="ready" />)
    const ids = Array.from(document.querySelectorAll('[data-rail-id]')).map(
      (el) => [el.textContent, el.className.includes('font-mono')],
    )
    expect(ids).toEqual([
      ['wi_calibrate_dome_3', true],
      ['9f2a41c7', true],
    ])
    expect(
      screen.getByRole('link', { name: /Work item wi_calibrate_dome_3/ }),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('link', { name: /Session sess-9f2a41c7/ }),
    ).toHaveAttribute('href', '/sessions?session=sess%3Asess-9f2a41c7')
    expect(screen.getByTitle('Session sess-9f2a41c7')).toHaveTextContent(
      'Session 9f2a41c7',
    )
  })
})
