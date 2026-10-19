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
  ])('puts it under Working when %s', (_why, s) => {
    expect(groupOf(s)).toBe('active')
  })

  it.each<[string, UnifiedSession]>([
    ['the connector caught a discrepancy', row({ cc_state: 'silent_evasion' })],
    ['the session holds no live claim', row({ unclaimed: true })],
    [
      'the provider says it needs a login',
      row(undefined, { state: 'running', provider_auth_state: 'required' }),
    ],
    [
      'a tool call waits on an approval (HU-R12)',
      row(undefined, { state: 'running', pending_approval_ref: 'apr_1' }),
    ],
    // The sidebar files this launch under Needs you; Sessions said Settled (binary 12).
    [
      'its launch waits for an approval',
      row(undefined, {
        state: 'waiting_approval',
        pending_approval_ref: 'apr_2',
      }),
    ],
  ])('puts it under Needs you when %s', (_why, s) => {
    expect(groupOf(s)).toBe('attention')
  })

  it.each<[string, UnifiedSession]>([
    ['it ended', row({ cc_state: 'ended' })],
    ['it is quiet within tolerance', row({ cc_state: 'idle' })],
    // A run the engine reports `idle` has answered its turn and waits for the person
    // (#1785): not working, whatever its process is doing. The observed half files the
    // same word the same way, one row above.
    ['its run answered and waits for input', row(undefined, { state: 'idle' })],
    [
      'its run waits for input while the observed row still says active',
      row(
        { session_ref: 'sess-1', cc_state: 'active' },
        { claude_session_id: 'sess-1', state: 'idle' },
      ),
    ],
    ['its run stopped', row(undefined, { state: 'stopped' })],
    ['its run was cleaned', row(undefined, { state: 'cleaned' })],
    // Root 19:15Z: a failed run is not waiting for anyone; the sidebar says Earlier.
    ['its run failed', row(undefined, { state: 'failed' })],
    [
      // Stop -> reload: the observed row still says active; the run decides (EU, 06).
      'its run stopped while the observed row still says active',
      row(
        { session_ref: 'sess-1', cc_state: 'active' },
        { claude_session_id: 'sess-1', state: 'stopped' },
      ),
    ],
  ])('puts it under Earlier when %s', (_why, s) => {
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

  it('keeps the pinned section first and the three state sections in their order', () => {
    // Pinned, then Needs you, Working, Earlier: what asks for the person comes first
    // among the rest.
    expect(groupSessions([done, working, asking]).map((g) => g.id)).toEqual([
      'pinned',
      'attention',
      'active',
      'settled',
    ])
  })

  it('returns an empty section rather than dropping it', () => {
    // A rail that changed shape every time a session settled would move the row under
    // the operator's cursor.
    const groups = groupSessions([working])
    expect(groups).toHaveLength(4)
    expect(groups.find((g) => g.id === 'settled')?.sessions).toEqual([])
    expect(groups.find((g) => g.id === 'pinned')?.sessions).toEqual([])
  })

  it('puts pinned sessions in their own section ABOVE every other, in pin order', () => {
    // A pin is "always first in my list" (#2130): a settled session pinned goes above
    // the one asking for a person, and two pins keep the order they were pinned in,
    // not the order the list gave them.
    const all = [working, asking, done, alsoDone]
    expect(
      railOrder(groupSessions(all, new Set([alsoDone.key, working.key]))).map(
        (s) => s.sessionRef,
      ),
    ).toEqual(['d', 'a', 'b', 'c'])
    expect(
      railOrder(groupSessions(all, new Set([working.key, alsoDone.key]))).map(
        (s) => s.sessionRef,
      ),
    ).toEqual(['a', 'd', 'b', 'c'])
  })

  it('lists a pinned session once: it leaves its state section', () => {
    const groups = groupSessions([working, done], new Set([done.key]))
    expect(
      groups.map((g) => [g.id, g.sessions.map((s) => s.sessionRef)]),
    ).toEqual([
      ['pinned', ['c']],
      ['attention', []],
      ['active', ['a']],
      ['settled', []],
    ])
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
