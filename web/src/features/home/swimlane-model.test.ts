// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE LAST HOUR, ROW BY ROW, FROM THE EVENT LOG (#2547): every row of the demo estate
// (swimlane.fixture.ts) must say what its events say.
import { describe, expect, it } from 'vitest'
import { mergeSessions } from '@/features/sessions/provenance'
import {
  buildLanes,
  densityStep,
  ledgerRange,
  windowTicks,
  WINDOW_MS,
} from './swimlane-model'
import {
  approval,
  approvals,
  at,
  calls,
  events,
  iso,
  lives,
  NOW,
  run,
  runs,
} from './swimlane.fixture'

const sessions = mergeSessions(lives, runs)
const eventsOf = (key: string) =>
  events[sessions.find((s) => s.key === key)!.runs[0]!.run_ref]

function lanes() {
  return buildLanes({
    sessions,
    events: new Map(sessions.map((s) => [s.key, eventsOf(s.key)])),
    approvals,
    now: NOW,
  })
}
const laneOf = (ref: string) =>
  lanes().find((l) => l.session.runs[0]?.run_ref === ref)!
const hhmm = (ms: number) => new Date(ms).toISOString().slice(11, 16)
const spans = (ref: string) =>
  laneOf(ref).runs.map(
    (r) => `${hhmm(r.start)}-${hhmm(r.end)}${r.live ? ' live' : ''}`,
  )

describe('buildLanes: the demo estate', () => {
  it('gives every live session one lane', () => {
    expect(lanes()).toHaveLength(7)
  })

  it('draws a run per stretch of activity, and the live one reaches now', () => {
    expect(spans('lena')).toEqual([
      '15:44-15:53',
      '15:57-16:06',
      '16:10-16:16',
      '16:24-16:42 live',
    ])
    expect(spans('marc')).toEqual(['15:48-16:01', '16:06-16:13', '16:33-16:40'])
    expect(spans('audit')).toEqual(['16:39-16:42 live'])
  })

  it('cuts a run at the window start', () => {
    expect(spans('sam')[0]).toBe('15:42-15:49')
    expect(laneOf('sam').runs[0]!.start).toBe(NOW - WINDOW_MS)
  })

  it('counts the tool calls the timeline holds, never the cost events', () => {
    const tomas = laneOf('tomas')
    const inWindow = events.tomas!.filter(
      (e) => e.kind === 'tool' && Date.parse(e.at) >= NOW - WINDOW_MS,
    ).length
    expect(tomas.toolCalls).toBe(inWindow)
    expect(
      tomas.runs.reduce((n, r) => n + r.density.reduce((a, b) => a + b, 0), 0),
    ).toBe(tomas.toolCalls)
  })

  it('waits from the moment each approval was asked, and flags only the first in the queue', () => {
    const marc = laneOf('marc')
    expect(marc.waits).toEqual([
      expect.objectContaining({ start: at('16:40'), end: NOW, flagged: true }),
    ])
    expect(marc.asks).toEqual([
      { at: at('16:40'), flagged: true, id: 'ap-marc' },
    ])
    expect(laneOf('sam').waits).toEqual([
      expect.objectContaining({ start: at('16:36'), end: NOW, flagged: false }),
    ])
    const flagged = lanes().flatMap((l) => [
      ...l.waits.filter((w) => w.flagged),
      ...l.asks.filter((a) => a.flagged),
    ])
    expect(flagged).toHaveLength(2)
  })

  it('shows a wait under a run that goes on working as a strip', () => {
    expect(laneOf('lena').waits).toEqual([
      expect.objectContaining({
        start: at('16:31'),
        strip: true,
        flagged: false,
      }),
    ])
    expect(laneOf('marc').waits[0]!.strip).toBe(false)
    // Asked at the moment its last run ended: nothing works on, so it is no strip.
    expect(laneOf('sam').waits[0]!.strip).toBe(false)
  })

  it('stops a run at an approval asked after its last event', () => {
    expect(laneOf('sam').runs.some((r) => r.live)).toBe(false)
    expect(laneOf('marc').runs.some((r) => r.live)).toBe(false)
  })

  it('says an idle session waits for its person from its last event', () => {
    const tomas = laneOf('tomas')
    expect(tomas.state).toBe('idle')
    expect(tomas.idleFrom).toBe(at('16:31'))
    expect(tomas.idleSince).toBe(at('16:31'))
    expect(tomas.runs.some((r) => r.live)).toBe(false)
  })
})

describe('buildLanes: edges', () => {
  const one = (r: ReturnType<typeof run>, evts = calls('16:10', '16:20')) => {
    const sessions = mergeSessions([], [r])
    return buildLanes({
      sessions,
      events: new Map([[sessions[0]!.key, evts]]),
      approvals: [],
      now: NOW,
    })[0]!
  }

  it('ends a working run where its events end once it has been quiet longer than a gap', () => {
    const lane = one(run('quiet', 'running'), calls('16:20', '16:34'))
    expect(lane.runs).toEqual([
      expect.objectContaining({
        start: at('16:20'),
        end: at('16:34'),
        live: false,
      }),
    ])
  })

  it('does not call a working session failed for an older run that failed', () => {
    const newer = run('again', 'running', { live_ref: 'L-x' })
    const older = run('first', 'failed', {
      live_ref: 'L-x',
      stopped_at: iso(at('16:05')),
    })
    const [session] = mergeSessions([], [newer])
    const [lane] = buildLanes({
      sessions: [{ ...session!, runs: [newer, older] }],
      events: new Map([[session!.key, calls('16:30', '16:42')]]),
      approvals: [],
      now: NOW,
    })
    expect(lane!.state).toBe('working')
    expect(lane!.failedAt).toBeUndefined()
  })

  it('says when an approval was asked, even before the hour, and draws it from the hour', () => {
    const r = run('long', 'running', { pending_approval_ref: 'ap-long' })
    const sessions = mergeSessions([], [r])
    const [lane] = buildLanes({
      sessions,
      events: new Map([[sessions[0]!.key, calls('15:00', '15:10')]]),
      approvals: [approval('ap-long', 'osn_long', '15:20')],
      now: NOW,
    })
    expect(lane!.waits).toEqual([
      expect.objectContaining({ asked: at('15:20'), start: NOW - WINDOW_MS }),
    ])
    expect(lane!.asks).toEqual([])
  })

  it('draws no wait and no ask for an approval whose ask time the engine did not send', () => {
    const r = run('old', 'running', { pending_approval_ref: 'ap-old' })
    const sessions = mergeSessions([], [r])
    const { created_at: _gone, ...untimed } = approval(
      'ap-old',
      'osn_old',
      '16:00',
    )
    const [lane] = buildLanes({
      sessions,
      events: new Map([[sessions[0]!.key, calls('16:30', '16:42')]]),
      approvals: [untimed],
      now: NOW,
    })
    expect(lane!.waits).toEqual([
      expect.objectContaining({ asked: undefined, start: NOW }),
    ])
    expect(lane!.asks).toEqual([])
    expect(lane!.state).toBe('waiting')
    // Its run still works: nothing says when it stopped.
    expect(lane!.runs.at(-1)!.live).toBe(true)
  })

  it('marks a failure where the run failed, and keeps a session that failed this hour', () => {
    const failed = run('bad', 'failed', {
      stopped_at: iso(at('16:20')),
      reason: 'exit status 1',
    })
    const sessions = mergeSessions([], [failed])
    const [lane] = buildLanes({
      sessions,
      events: new Map([[sessions[0]!.key, calls('16:10', '16:20')]]),
      approvals: [],
      now: NOW,
    })
    expect(lane!.state).toBe('failed')
    expect(lane!.failedAt).toBe(at('16:20'))
  })

  it('leaves out a session that ended this hour without failing, or failed before it', () => {
    const old = run('old', 'failed', { stopped_at: iso(at('15:20')) })
    const done = run('done', 'stopped', { stopped_at: iso(at('16:20')) })
    expect(
      buildLanes({
        sessions: mergeSessions([], [old, done]),
        events: new Map(),
        approvals: [],
        now: NOW,
      }),
    ).toEqual([])
  })

  it('draws no run for a lane whose events were not read, and says so', () => {
    const [lane] = buildLanes({
      sessions: mergeSessions([], [run('x', 'running')]),
      events: new Map(),
      approvals: [],
      now: NOW,
    })
    expect(lane!.read).toBe(false)
    expect(lane!.runs).toEqual([])
  })
})

describe('the axis and the range', () => {
  it('ticks every ten minutes inside the hour', () => {
    expect(windowTicks(NOW).map(hhmm)).toEqual([
      '15:50',
      '16:00',
      '16:10',
      '16:20',
      '16:30',
      '16:40',
    ])
  })

  it('steps tool-call density into four tones', () => {
    expect([0, 1, 2, 3, 5, 6, 12].map(densityStep)).toEqual([
      0, 1, 1, 2, 2, 3, 3,
    ])
  })

  it('turns a selected range into the Ledger window, earliest first, to the minute', () => {
    expect(ledgerRange(at('16:20') + 31_000, at('16:05') + 2_000)).toEqual({
      since: '2026-10-17T16:05:00.000Z',
      until: '2026-10-17T16:21:00.000Z',
    })
  })
})
