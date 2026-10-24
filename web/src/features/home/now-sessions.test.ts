// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import type { RunDTO } from '@/features/agentops/types'
import { mergeSessions } from '@/features/sessions/provenance'
import type { LiveDTO } from '@/features/sessions/types'
import {
  actsForPeople,
  dayCost,
  dayIsFloor,
  isLiveSession,
} from './now-sessions'

const NOW = new Date(2026, 9, 16, 15, 0).getTime()
const at = (h: number, day = 16) => new Date(2026, 9, day, h, 0).toISOString()

function live(over: Partial<LiveDTO> = {}): LiveDTO {
  return {
    session_ref: 'sess-1',
    attribution: 'legacy',
    cc_state: 'active',
    input_tokens: 10,
    output_tokens: 10,
    cost_micro_usd: 0,
    event_count: 1,
    tool_call_count: 0,
    first_event_at: at(9),
    last_event_at: at(9),
    duration_seconds: 1,
    critical: false,
    ...over,
  } as LiveDTO
}

function run(over: Partial<RunDTO> = {}): RunDTO {
  return {
    run_ref: 'run-1',
    tenant_id: 'demo',
    state: 'running',
    created_at: at(9),
    ...over,
  } as RunDTO
}

describe('isLiveSession: the tile rule, one session at a time', () => {
  it('an observed row is live while the tile counts it', () => {
    for (const cc_state of ['active', 'idle', 'silent_evasion'])
      expect(isLiveSession(mergeSessions([live({ cc_state })], [])[0]!)).toBe(
        true,
      )
    expect(
      isLiveSession(mergeSessions([live({ cc_state: 'ended' })], [])[0]!),
    ).toBe(false)
  })

  it('a run decides for its session: live, held for approval, or ended', () => {
    const of = (state: string) =>
      isLiveSession(mergeSessions([], [run({ state })])[0]!)
    expect(of('running')).toBe(true)
    expect(of('idle')).toBe(true)
    expect(of('waiting_approval')).toBe(true)
    expect(of('stopped')).toBe(false)
    expect(of('failed')).toBe(false)
  })
})

describe("dayCost: what today's sessions cost, never a made-up zero", () => {
  it('sums the costs of the sessions that worked since midnight', () => {
    const sessions = mergeSessions(
      [
        live({
          session_ref: 'a',
          cost_micro_usd: 420_000,
          last_event_at: at(14),
        }),
        live({
          session_ref: 'b',
          cost_micro_usd: 80_000,
          last_event_at: at(10),
        }),
        live({
          session_ref: 'old',
          cost_micro_usd: 9_000_000,
          first_event_at: at(22, 15),
          last_event_at: at(23, 15),
        }),
      ],
      [],
    )
    expect(dayCost(sessions, NOW)).toEqual({
      sessions: 2,
      costMicroUsd: 500_000,
      unpriced: 0,
    })
  })

  it('a session that reported no cost is unpriced, not $0', () => {
    const sessions = mergeSessions(
      [live({ session_ref: 'a', cost_micro_usd: 0, last_event_at: at(14) })],
      [],
    )
    expect(dayCost(sessions, NOW)).toEqual({
      sessions: 1,
      costMicroUsd: null,
      unpriced: 1,
    })
  })

  it("a run's own cost counts, and a run without one is unpriced", () => {
    const sessions = mergeSessions(
      [],
      [
        run({
          run_ref: 'r1',
          cost_micro_usd: 1_000_000,
          last_activity_at: at(13),
        }),
        run({ run_ref: 'r2', last_activity_at: at(12) }),
      ],
    )
    const today = dayCost(sessions, NOW)
    expect(today.sessions).toBe(2)
    expect(today.costMicroUsd).toBe(1_000_000)
    expect(today.unpriced).toBe(1)
  })

  it('a session priced on one run of two counts its known part and is unpriced too', () => {
    // Two launches against one provider session are one session (mergeSessions).
    const sessions = mergeSessions(
      [],
      [
        run({
          run_ref: 'r1',
          claude_session_id: 'sess-x',
          cost_micro_usd: 300_000,
          last_activity_at: at(13),
        }),
        run({
          run_ref: 'r2',
          claude_session_id: 'sess-x',
          last_activity_at: at(14),
        }),
      ],
    )
    expect(sessions).toHaveLength(1)
    expect(dayCost(sessions, NOW)).toEqual({
      sessions: 1,
      costMicroUsd: 300_000,
      unpriced: 1,
    })
  })

  it('midnight itself is today; a millisecond before is not', () => {
    const midnight = new Date(2026, 9, 16).getTime()
    const s = mergeSessions([live({ cost_micro_usd: 1_000 })], [])[0]!
    expect(dayCost([{ ...s, lastActivityMs: midnight }], NOW).sessions).toBe(1)
    expect(
      dayCost([{ ...s, lastActivityMs: midnight - 1 }], NOW).sessions,
    ).toBe(0)
  })

  it('nothing worked today: no sessions and no figure', () => {
    expect(dayCost([], NOW)).toEqual({
      sessions: 0,
      costMicroUsd: null,
      unpriced: 0,
    })
  })
})

describe('dayIsFloor: a cut page hides today only if all it holds is today', () => {
  const t = (h: number, day = 16) => new Date(2026, 9, day, h).getTime()
  it('a complete page is never a floor', () => {
    expect(dayIsFloor(false, [t(9)], NOW)).toBe(false)
    expect(dayIsFloor(undefined, [t(9)], NOW)).toBe(false)
  })
  it('a cut page of today only is a floor; one that reaches yesterday is not', () => {
    expect(dayIsFloor(true, [t(14), t(9)], NOW)).toBe(true)
    expect(dayIsFloor(true, [t(14), t(23, 15)], NOW)).toBe(false)
  })
  it('a row of unknown time counts as today', () => {
    expect(dayIsFloor(true, [t(14), Number.NaN], NOW)).toBe(true)
  })
})

describe('dayCost of yesterday: the sessions whose last work was yesterday', () => {
  it('counts the sessions that last worked between the two midnights, and no other', () => {
    const sessions = mergeSessions(
      [
        live({
          session_ref: 'today',
          cost_micro_usd: 420_000,
          last_event_at: at(14),
        }),
        live({
          session_ref: 'yesterday',
          cost_micro_usd: 310_000,
          first_event_at: at(9, 15),
          last_event_at: at(23, 15),
        }),
        live({
          session_ref: 'before',
          cost_micro_usd: 9_000_000,
          first_event_at: at(9, 14),
          last_event_at: at(10, 14),
        }),
      ],
      [],
    )
    expect(dayCost(sessions, NOW, 1)).toEqual({
      sessions: 1,
      costMicroUsd: 310_000,
      unpriced: 0,
    })
  })

  it('a cut page that reaches the day before yesterday is not a floor for yesterday', () => {
    const t = (h: number, day: number) => new Date(2026, 9, day, h).getTime()
    expect(dayIsFloor(true, [t(14, 16), t(9, 15)], NOW, 1)).toBe(true)
    expect(dayIsFloor(true, [t(14, 16), t(9, 14)], NOW, 1)).toBe(false)
  })
})

describe('actsForPeople: how many people the live sessions act for', () => {
  const session = (acts_for: RunDTO['acts_for'], run_ref: string) =>
    mergeSessions([], [run({ run_ref, acts_for })])[0]!

  it('counts each person once and no automation', () => {
    expect(
      actsForPeople([
        session({ kind: 'user', user_id: 'u1' }, 'r1'),
        session({ kind: 'user', user_id: 'u1' }, 'r2'),
        session({ kind: 'user', user_id: 'u2' }, 'r3'),
        session({ kind: 'automation' }, 'r4'),
      ]),
    ).toBe(2)
  })

  it('is unknown, never a count, while one session does not say whom it acts for', () => {
    expect(
      actsForPeople([
        session({ kind: 'user', user_id: 'u1' }, 'r1'),
        session({ kind: 'unknown', reason: 'predates the record' }, 'r2'),
      ]),
    ).toBeNull()
    expect(
      actsForPeople([
        session({ kind: 'user', user_id: 'u1' }, 'r1'),
        session(undefined, 'r2'),
      ]),
    ).toBeNull()
    // An observed session with no launch record says nothing about whom it acts for.
    expect(actsForPeople(mergeSessions([live()], []))).toBeNull()
  })
})
