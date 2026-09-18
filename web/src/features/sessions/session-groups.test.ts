// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// WHAT THE RAIL IS SORTED BY. Pure, so each rule is driven rung by rung.
import { describe, expect, it } from 'vitest'
import type { RunDTO } from '@/features/agentops/types'
import { mergeSessions, type UnifiedSession } from './provenance'
import type { LiveDTO } from './types'
import { groupOf, groupSessions, railOrder } from './session-groups'

function live(over: Partial<LiveDTO> = {}): LiveDTO {
  return {
    session_ref: 'sess-1',
    live_ref: 'lr-1',
    attribution: 'legacy',
    cc_state: 'idle',
    input_tokens: 0,
    output_tokens: 0,
    cost_micro_usd: 0,
    event_count: 0,
    tool_call_count: 0,
    first_event_at: '2026-09-18T09:00:00Z',
    last_event_at: '2026-09-18T09:01:00Z',
    duration_seconds: 60,
    ...over,
  }
}

function run(over: Partial<RunDTO> = {}): RunDTO {
  return {
    run_ref: 'run-1',
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

/** One row, built through the REAL join so the grouping is driven by what it produces. */
function row(l?: Partial<LiveDTO>, r?: Partial<RunDTO>): UnifiedSession {
  const [first] = mergeSessions(l ? [live(l)] : [], r ? [run(r)] : [])
  return first
}

describe('groupOf — membership is a field the engine sent', () => {
  it.each<[string, UnifiedSession]>([
    ['the engine says it is working now', row({ cc_state: 'active' })],
    ['a run holds a live process', row(undefined, { state: 'running' })],
    ['a run is pending', row(undefined, { state: 'pending' })],
    // `idle` on a RUN means the process is alive and quiet; on the observed half it
    // means quiet within tolerance. The two vocabularies share a word and mean
    // different things, and the rail follows the run: there is a process.
    ['a run is idle but alive', row(undefined, { state: 'idle' })],
  ])('puts it under Active when %s', (_why, s) => {
    expect(groupOf(s)).toBe('active')
  })

  it.each<[string, UnifiedSession]>([
    ['the connector caught a discrepancy', row({ cc_state: 'silent_evasion' })],
    ['the session holds no live claim', row({ unclaimed: true })],
    ['a run failed', row(undefined, { state: 'failed' })],
    [
      'the provider says it needs a login',
      row(undefined, { state: 'running', provider_auth_state: 'required' }),
    ],
  ])('puts it under Waiting for you when %s', (_why, s) => {
    expect(groupOf(s)).toBe('attention')
  })

  it.each<[string, UnifiedSession]>([
    ['it ended', row({ cc_state: 'ended' })],
    ['it is quiet within tolerance', row({ cc_state: 'idle' })],
    ['its run stopped', row(undefined, { state: 'stopped' })],
    ['its run was cleaned', row(undefined, { state: 'cleaned' })],
  ])('puts it under Settled when %s', (_why, s) => {
    expect(groupOf(s)).toBe('settled')
  })

  it('asks louder than it works: a working session that needs a person is attention', () => {
    // Both conditions at once is the case the ORDER of the checks decides, and the
    // operator needs to see the asking — a session that is busy AND failed is not a
    // busy session.
    expect(
      groupOf(
        row({ cc_state: 'active', unclaimed: true }, { state: 'running' }),
      ),
    ).toBe('attention')
  })

  it('does not invent attention from silence', () => {
    // No clock arithmetic anywhere: a session quiet for a week is Settled, because
    // nothing the engine sent says a person is needed.
    expect(
      groupOf(row({ cc_state: 'idle', last_event_at: '2020-01-01T00:00:00Z' })),
    ).toBe('settled')
  })
})

describe('groupSessions — the rail in reading order', () => {
  const working = row({
    session_ref: 'a',
    live_ref: 'lr-a',
    cc_state: 'active',
  })
  const asking = row({
    session_ref: 'b',
    live_ref: 'lr-b',
    cc_state: 'silent_evasion',
  })
  const done = row({ session_ref: 'c', live_ref: 'lr-c', cc_state: 'ended' })
  const alsoDone = row({
    session_ref: 'd',
    live_ref: 'lr-d',
    cc_state: 'ended',
  })

  it('keeps the three sections and their order', () => {
    expect(groupSessions([done, working, asking]).map((g) => g.id)).toEqual([
      'active',
      'attention',
      'settled',
    ])
  })

  it('returns an empty section rather than dropping it', () => {
    // A rail that changed shape every time a session settled would move the row under
    // the operator's cursor.
    const groups = groupSessions([working])
    expect(groups).toHaveLength(3)
    expect(groups.find((g) => g.id === 'settled')?.sessions).toEqual([])
  })

  it('lifts a pin WITHIN its section, never above one', () => {
    // The whole rule: a pin is a bookmark, not a priority. Pinning a settled session
    // must not put it above a session that is asking for a person.
    const order = railOrder(
      groupSessions([working, asking, done, alsoDone], new Set([alsoDone.key])),
    )
    expect(order.map((s) => s.sessionRef)).toEqual(['a', 'b', 'd', 'c'])
  })

  it('preserves the incoming order inside a section (the join sorted it)', () => {
    const order = railOrder(groupSessions([done, alsoDone]))
    expect(order.map((s) => s.sessionRef)).toEqual(['c', 'd'])
  })

  it('ignores a pin naming a session that is not here', () => {
    const order = railOrder(groupSessions([working], new Set(['live:gone'])))
    expect(order).toEqual([working])
  })
})
