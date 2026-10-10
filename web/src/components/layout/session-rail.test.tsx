// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE SESSION RAIL's grouping: Needs you, Working, Earlier, in that order. A handoff
// offered to the operator waits for an answer, so it is in "Needs you".
import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'

import type { LiveDTO } from '@/features/sessions/types'
import type { HandoffInboxItem } from '@/features/communications/types'
import { railGroups, type RailState } from './session-rail-model'
import { RailGlyph } from './session-rail'

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

describe('RailGlyph', () => {
  // The Business cockpit draws the glyph too: the state word is its accessible name.
  it.each<[RailState, string]>([
    ['need', 'Needs you'],
    ['live', 'Working'],
    ['ended', 'Ended'],
    ['idle', 'Idle'],
  ])('names the %s state by its word', (state, word) => {
    render(<RailGlyph state={state} />)
    expect(screen.getByRole('img', { name: word })).toBeInTheDocument()
  })
})
