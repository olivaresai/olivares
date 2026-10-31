// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// WHAT THE RAIL IS SORTED BY. Pure, so each rule is driven rung by rung.
import { describe, expect, it } from 'vitest'
import type { RunDTO } from '@/features/agentops/types'
import { mergeSessions, type UnifiedSession } from './provenance'
import type { LiveDTO } from './types'
import {
  groupOf,
  groupSessions,
  isArchived,
  railOrder,
  workingCount,
} from './session-groups'

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
      'today',
      'settled',
      'snoozed',
      'archived',
    ])
  })

  it('returns an empty section rather than dropping it', () => {
    // A rail that changed shape every time a session settled would move the row under
    // the operator's cursor.
    const groups = groupSessions([working])
    expect(groups).toHaveLength(7)
    expect(groups.find((g) => g.id === 'today')?.sessions).toEqual([])
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
      ['today', []],
      ['settled', []],
      ['snoozed', []],
      ['archived', []],
    ])
  })

  it('preserves the incoming order inside a section (the join sorted it)', () => {
    const order = railOrder(groupSessions([done, alsoDone]))
    expect(order.map((s) => s.sessionRef)).toEqual(['c', 'd'])
  })

  // The person's own marks (#2480), sent by the engine on the session's runs.
  const snoozed = row(undefined, {
    run_ref: 'run-s',
    state: 'running',
    snoozed_until: '2030-01-02T09:00:00Z',
  })
  const marked = row(undefined, {
    run_ref: 'run-m',
    state: 'running',
    pending_approval_ref: 'apr_9',
    done: true,
  })

  it('takes a snoozed session out of every other section, into Snoozed at the end', () => {
    // Snooze is "not in my list until then", whatever the session is doing or whether
    // it is pinned; the rail keeps the section so the person can still reach it.
    const groups = groupSessions([working, snoozed], new Set([snoozed.key]))
    expect(groups.map((g) => [g.id, g.sessions.map((s) => s.key)])).toEqual([
      ['pinned', []],
      ['attention', []],
      ['active', [working.key]],
      ['today', []],
      ['settled', []],
      ['snoozed', [snoozed.key]],
      ['archived', []],
    ])
  })

  it('files a session the person marked done under Settled, whatever it is doing', () => {
    expect(groupOf(marked)).toBe('attention')
    const groups = groupSessions([marked, working])
    expect(groups.find((g) => g.id === 'settled')?.sessions).toEqual([marked])
    expect(groups.find((g) => g.id === 'attention')?.sessions).toEqual([])
  })

  it('keeps a pinned session first even when it is marked done', () => {
    const groups = groupSessions([marked], new Set([marked.key]))
    expect(groups.find((g) => g.id === 'pinned')?.sessions).toEqual([marked])
  })

  // The session's own archived mark (#2795), the same for everyone who reads it.
  const archived = row(undefined, {
    run_ref: 'run-z',
    state: 'running',
    pending_approval_ref: 'apr_z',
    snoozed_until: '2030-01-02T09:00:00Z',
    archived_at: '2026-10-21T12:00:00Z',
  })

  it('takes an archived session out of every section, pinned or snoozed, into Archived last', () => {
    expect(isArchived(archived)).toBe(true)
    const groups = groupSessions([working, archived], new Set([archived.key]))
    expect(groups.map((g) => [g.id, g.sessions.map((s) => s.key)])).toEqual([
      ['pinned', []],
      ['attention', []],
      ['active', [working.key]],
      ['today', []],
      ['settled', []],
      ['snoozed', []],
      ['archived', [archived.key]],
    ])
  })

  it('walks the archived rows only while the rail shows them', () => {
    const groups = groupSessions([archived, working])
    expect(railOrder(groups)).toEqual([working])
    expect(railOrder(groups, true)).toEqual([working])
    expect(railOrder(groups, false, true)).toEqual([working, archived])
  })

  it('walks the snoozed rows only while the rail shows them', () => {
    const groups = groupSessions([snoozed, working])
    expect(railOrder(groups)).toEqual([working])
    expect(railOrder(groups, true)).toEqual([working, snoozed])
  })

  it('counts as working only what the Working section shows', () => {
    // "N working" in the header and on the dashboard card is the Working section's
    // size: a working session the person snoozed, marked done or archived is not in it.
    const markedWorking = row(undefined, {
      run_ref: 'run-w',
      state: 'running',
      done: true,
    })
    const archivedWorking = row(undefined, {
      run_ref: 'run-a',
      state: 'running',
      archived_at: '2026-10-21T12:00:00Z',
    })
    const all = [working, snoozed, markedWorking, archivedWorking]
    expect(groupOf(snoozed)).toBe('active')
    expect(groupOf(markedWorking)).toBe('active')
    expect(groupOf(archivedWorking)).toBe('active')
    expect(workingCount(all)).toBe(
      groupSessions(all).find((g) => g.id === 'active')?.sessions.length,
    )
    expect(workingCount(all)).toBe(1)
  })

  it('ignores a pin naming a session that is not here', () => {
    const order = railOrder(groupSessions([working], new Set(['live:gone'])))
    expect(order).toEqual([working])
  })
})

// The list is grouped by what a session needs from the person, and keeps what ended
// today apart from what ended before.
describe('the day a session finished', () => {
  const now = new Date(2026, 9, 23, 15, 0).getTime()
  const today = row({
    session_ref: 's-today',
    live_ref: 'lr-today',
    cc_state: 'ended',
    last_event_at: new Date(2026, 9, 23, 9, 0).toISOString(),
  })
  const yesterday = row({
    session_ref: 's-yesterday',
    live_ref: 'lr-yesterday',
    cc_state: 'ended',
    last_event_at: new Date(2026, 9, 22, 20, 0).toISOString(),
  })

  it('files a session that finished today under Done today, an older one under Earlier', () => {
    const groups = groupSessions([yesterday, today], new Set(), now)
    expect(groups.map((g) => g.id)).toEqual([
      'pinned',
      'attention',
      'active',
      'today',
      'settled',
      'snoozed',
      'archived',
    ])
    expect(groups.find((g) => g.id === 'today')?.sessions).toEqual([today])
    expect(groups.find((g) => g.id === 'settled')?.sessions).toEqual([
      yesterday,
    ])
  })

  it('starts the day at local midnight: one millisecond before is Earlier', () => {
    const at = (ms: number, ref: string) =>
      row({
        session_ref: ref,
        live_ref: `lr-${ref}`,
        cc_state: 'ended',
        last_event_at: new Date(ms).toISOString(),
      })
    const midnight = new Date(2026, 9, 23).getTime()
    const first = at(midnight, 's-first')
    const last = at(midnight - 1, 's-last')
    const groups = groupSessions([first, last], new Set(), now)
    expect(groups.find((g) => g.id === 'today')?.sessions).toEqual([first])
    expect(groups.find((g) => g.id === 'settled')?.sessions).toEqual([last])
  })
})

// Working sessions fold into one collapsed group, so a running session never moves a row
// under the pointer; one leaves the fold only when it finishes, fails or asks.
describe('the Working fold and the rise rule', () => {
  const now = new Date(2026, 9, 23, 15, 0).getTime()
  const running = (ref: string, startedAt: string, lastAt: string) =>
    row(undefined, {
      run_ref: ref,
      state: 'running',
      started_at: startedAt,
      last_activity_at: lastAt,
    })
  const ids = (groups: ReturnType<typeof groupSessions>, id: string) =>
    (groups.find((g) => g.id === id)?.sessions ?? []).map(
      (s) => s.runs[0]?.run_ref,
    )

  it('leaves the Working rows out of the walk while the fold is shut', () => {
    const w = running('w', '2026-10-23T09:00:00Z', '2026-10-23T09:05:00Z')
    const asks = row(undefined, { run_ref: 'a', state: 'waiting_approval' })
    const groups = groupSessions([w, asks], new Set(), now)
    expect(railOrder(groups, false, false, false)).toEqual([asks])
    expect(railOrder(groups, false, false, true)).toEqual([asks, w])
    // The surface still opens on a working session when nothing asks: the default is
    // the first row of the whole list, folded or not.
    expect(railOrder(groups)).toEqual([asks, w])
  })

  it('keeps the Working rows in the order they started, whoever spoke last', () => {
    // `a` started first and speaks last: by activity it would sit above `b`, and the row
    // under the pointer would change with every event.
    const a = running('a', '2026-10-23T09:00:00Z', '2026-10-23T09:50:00Z')
    const b = running('b', '2026-10-23T09:10:00Z', '2026-10-23T09:20:00Z')
    expect(ids(groupSessions([a, b], new Set(), now), 'active')).toEqual([
      'b',
      'a',
    ])
    const aSpokeAgain = running(
      'a',
      '2026-10-23T09:00:00Z',
      '2026-10-23T09:55:00Z',
    )
    expect(
      ids(groupSessions([aSpokeAgain, b], new Set(), now), 'active'),
    ).toEqual(['b', 'a'])
  })

  it('lets a session out of the fold when it finishes: first in Done today', () => {
    const older = row(undefined, {
      run_ref: 'older',
      state: 'idle',
      last_activity_at: '2026-10-23T08:00:00Z',
    })
    const was = running('was', '2026-10-23T09:00:00Z', '2026-10-23T09:30:00Z')
    const finished = row(undefined, {
      run_ref: 'was',
      state: 'idle',
      started_at: '2026-10-23T09:00:00Z',
      last_activity_at: '2026-10-23T09:40:00Z',
    })
    expect(ids(groupSessions([was, older], new Set(), now), 'active')).toEqual([
      'was',
    ])
    const after = groupSessions([finished, older], new Set(), now)
    expect(ids(after, 'active')).toEqual([])
    expect(ids(after, 'today')).toEqual(['was', 'older'])
  })

  it('lets a session out of the fold when it needs an answer: first of all', () => {
    const w = running('w', '2026-10-23T09:00:00Z', '2026-10-23T09:30:00Z')
    const asks = row(undefined, {
      run_ref: 'w2',
      state: 'running',
      pending_approval_ref: 'apr_1',
    })
    const groups = groupSessions([w, asks], new Set(), now)
    expect(ids(groups, 'attention')).toEqual(['w2'])
    expect(groups[1].id).toBe('attention')
  })

  it('lets a session out of the fold when the person pins it', () => {
    const w = running('w', '2026-10-23T09:00:00Z', '2026-10-23T09:30:00Z')
    const groups = groupSessions([w], new Set([w.key]), now)
    expect(ids(groups, 'pinned')).toEqual(['w'])
    expect(ids(groups, 'active')).toEqual([])
  })
})
