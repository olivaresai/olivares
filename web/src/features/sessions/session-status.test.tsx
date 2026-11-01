// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// FIVE STATES, ONE GLYPH AND ONE WORD EACH: Working, Needs you, Done, Failed, Idle. The
// classifier answers which one a session is in; the mark draws it; every list that shows a
// session's state (the Sessions list, Now, the palette) asks the same two.
import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { railGroups } from '@/components/layout/session-rail-model'
import type { RunDTO } from '@/features/agentops/types'
import { mergeSessions } from './provenance'
import {
  sessionDisplayState,
  type SessionStatus,
} from './session-display-state'
import { SessionStatusMark } from './session-status'
import type { LiveDTO } from './types'

function run(over: Partial<RunDTO>): RunDTO {
  return {
    run_ref: 'run-status',
    transport: 'stream-json',
    permission_mode: 'default',
    isolation: 'native',
    state: 'running',
    last_event_seq: 0,
    pep_provisioned: true,
    record_io: false,
    critical: false,
    ...over,
  }
}

function observed(over: Partial<LiveDTO>): LiveDTO {
  return {
    session_ref: 'observed-status',
    cc_state: 'idle',
    input_tokens: 0,
    output_tokens: 0,
    cost_micro_usd: 0,
    event_count: 0,
    tool_call_count: 0,
    first_event_at: '',
    last_event_at: '',
    duration_seconds: 0,
    ...over,
  } as LiveDTO
}

const CASES: [string, RunDTO | LiveDTO, SessionStatus][] = [
  ['a running run', run({ state: 'running' }), 'working'],
  ['a run that has not started', run({ state: 'pending' }), 'working'],
  ['an observed active session', observed({ cc_state: 'active' }), 'working'],
  [
    'a run waiting for an approval',
    run({ state: 'waiting_approval' }),
    'needs-you',
  ],
  [
    'a tool call waiting on an approval',
    run({ state: 'running', pending_approval_ref: 'apr_1' }),
    'needs-you',
  ],
  [
    'a provider asking to be logged in',
    run({ state: 'running', provider_auth_state: 'required' }),
    'needs-you',
  ],
  [
    'a session that went silent',
    observed({ cc_state: 'silent_evasion' }),
    'needs-you',
  ],
  ['a run that answered its turn', run({ state: 'idle' }), 'done'],
  ['a stopped run', run({ state: 'stopped' }), 'done'],
  ['an observed session that ended', observed({ cc_state: 'ended' }), 'done'],
  ['a failed run', run({ state: 'failed' }), 'failed'],
  ['an observed session at rest', observed({ cc_state: 'idle' }), 'idle'],
]

const isRun = (source: RunDTO | LiveDTO): source is RunDTO =>
  'permission_mode' in source

function sessionOf(source: RunDTO | LiveDTO) {
  return isRun(source)
    ? mergeSessions([], [source])[0]
    : mergeSessions([source], [])[0]
}

describe('the classifier names each session by one of five statuses', () => {
  it.each(CASES)('%s is %s', (_what, source, status) => {
    expect(sessionDisplayState(sessionOf(source)).status).toBe(status)
  })

  it('feeds the rail rows (and so Now and the palette) the same status', () => {
    for (const [, source, status] of CASES) {
      const sources = isRun(source)
        ? { live: [], handoffs: [], runs: [source] }
        : { live: [source], handoffs: [] }
      const rows = railGroups(sources, 0).flatMap((g) => g.rows)
      expect(rows.map((r) => r.status)).toEqual([status])
    }
  })
})

const WORDS: [SessionStatus, string][] = [
  ['working', 'Working'],
  ['needs-you', 'Needs you'],
  ['done', 'Done'],
  ['failed', 'Failed'],
  ['idle', 'Idle'],
]

describe('SessionStatusMark', () => {
  it.each(WORDS)('names %s by its word', (status, word) => {
    render(<SessionStatusMark status={status} />)
    expect(screen.getByRole('img', { name: word })).toHaveAttribute(
      'data-status',
      status,
    )
  })

  it('draws five different shapes, so colour never carries a state alone', () => {
    const shapes = WORDS.map(([status, word]) => {
      const { unmount } = render(<SessionStatusMark status={status} />)
      const shape = screen
        .getByRole('img', { name: word })
        .getAttribute('data-shape')
      unmount()
      return shape
    })
    expect(shapes.every(Boolean)).toBe(true)
    expect(new Set(shapes).size).toBe(5)
  })

  it('paints a colour only on Needs you and Failed', () => {
    const colored = WORDS.filter(([status, word]) => {
      const { unmount } = render(<SessionStatusMark status={status} />)
      const painted = /(text|bg)-(warning|danger|success|accent)/.test(
        screen.getByRole('img', { name: word }).getAttribute('class') ?? '',
      )
      unmount()
      return painted
    }).map(([status]) => status)
    expect(colored).toEqual(['needs-you', 'failed'])
  })

  it('stays out of the accessibility tree beside a word that already names it', () => {
    render(<SessionStatusMark status="working" decorative />)
    expect(screen.queryByRole('img')).toBeNull()
  })
})
