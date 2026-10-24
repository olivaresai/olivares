// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE LAST HOUR OF EVERY LIVE SESSION, AS ROWS (#2547). Pure: the rows are a function of
// what the engine already sent — each session's timeline events from an hour ago on, its
// runs, and the pending approvals — and of the clock. Nothing here is guessed: a stretch
// of events is a run, a pending approval is a wait from the moment it was asked, an idle
// run waits for its person from its last event, and a failed run fails where it stopped.
import type { ApprovalDTO } from '@/features/governance/types'
import {
  isLiveRun,
  isWorkingRun,
  runAwaitedApproval,
  runWantsAPerson,
  type UnifiedSession,
} from '@/features/sessions/provenance'
import type { TimelineDTO } from '@/features/sessions/types'
import { isLiveSession } from './now-sessions'

/** Past this many live sessions the swimlanes are too tall: a build with a fleet panel draws
 * that instead (#2565). */
export const FLEET_SESSIONS = 30
/** The swimlane shows the last hour. */
export const WINDOW_MS = 60 * 60_000
/** Two events further apart than this are two runs: the tool went quiet in between. */
export const RUN_GAP_MS = 3 * 60_000
/** The axis ticks every ten minutes. */
export const TICK_MS = 10 * 60_000
/** No mark closer to now than this: the now label stands there. */
const NOW_ROOM_MS = 2 * 60_000
const MINUTE = 60_000

/** One stretch of activity. `density` counts the tool calls in each minute of it. */
export interface LaneRun {
  start: number
  end: number
  /** The run is still going: it reaches now. */
  live: boolean
  density: number[]
}

/** A pending approval of this session, drawn from the moment it was asked (or the window
 * start) to now. `asked` is the real moment, said in words; it is absent from an engine
 * that does not send it, and then nothing is drawn. A `strip` sits under a run that goes
 * on working; otherwise the wait has the row to itself. */
export interface LaneWait {
  id: string
  start: number
  end: number
  asked?: number
  flagged: boolean
  strip: boolean
}

export interface LaneAsk {
  id: string
  at: number
  flagged: boolean
}

export type LaneState = 'working' | 'waiting' | 'idle' | 'failed'

export interface Lane {
  session: UnifiedSession
  state: LaneState
  runs: LaneRun[]
  waits: LaneWait[]
  asks: LaneAsk[]
  /** An idle session waits for its person's next prompt: drawn from `idleFrom` (cut to
   * the window), said from `idleSince` (its last event, when one is known). */
  idleFrom?: number
  idleSince?: number
  failedAt?: number
  toolCalls: number
  /** False while the session's events are not read (loading, refused or failed): the
   * lane draws nothing it does not know. */
  read: boolean
}

export interface LaneInput {
  sessions: readonly UnifiedSession[]
  /** Each session's timeline from the window start, by `UnifiedSession.key`; absent
   * while it is not read. */
  events: ReadonlyMap<string, readonly TimelineDTO[] | undefined>
  /** The pending approvals in the queue's own order: the first is the flagged one, the
   * one Needs you shows first. */
  approvals: readonly ApprovalDTO[]
  now: number
}

const isCall = (e: TimelineDTO) => e.kind === 'tool' || e.kind === 'mcp'

function time(iso: string | undefined): number | undefined {
  const t = iso ? Date.parse(iso) : NaN
  return Number.isNaN(t) ? undefined : t
}

/** When the session failed this hour: its newest run (runs are newest first) failed at
 * or after `from`. An older failed run of a session that went on is not its state. */
function failedAt(s: UnifiedSession, from: number): number | undefined {
  const run = s.runs[0]
  if (run?.state !== 'failed') return undefined
  const at = time(run.stopped_at ?? run.last_activity_at)
  return at !== undefined && at >= from ? at : undefined
}

/** The approvals this session asked for: the ones its runs wait on, or the ones that
 * name its canonical session. */
function approvalsOf(s: UnifiedSession, all: readonly ApprovalDTO[]) {
  const ids = new Set(s.runs.map(runAwaitedApproval).filter(Boolean))
  const sids = new Set(
    [s.live?.canonical_sid, ...s.runs.map((r) => r.canonical_sid)].filter(
      Boolean,
    ),
  )
  return all.filter(
    (a) => ids.has(a.id) || (!!a.session_ref && sids.has(a.session_ref)),
  )
}

/** Stretches of activity: events no further apart than RUN_GAP_MS, cut to the window. */
function runsOf(events: readonly TimelineDTO[], from: number, now: number) {
  const times = events
    .map((e) => ({ at: time(e.at), call: isCall(e) }))
    .filter((e): e is { at: number; call: boolean } => e.at !== undefined)
    .sort((a, b) => a.at - b.at)
  const runs: { start: number; end: number; calls: number[] }[] = []
  for (const e of times) {
    if (e.at > now) continue
    const last = runs[runs.length - 1]
    if (last && e.at - last.end <= RUN_GAP_MS) last.end = e.at
    else runs.push({ start: e.at, end: e.at, calls: [] })
    if (e.call) runs[runs.length - 1]!.calls.push(e.at)
  }
  return runs
    .filter((r) => r.end >= from)
    .map((r) => ({ ...r, start: Math.max(r.start, from) }))
}

function densityOf(start: number, end: number, calls: number[]): number[] {
  const out = new Array<number>(
    Math.max(1, Math.ceil((end - start) / MINUTE)),
  ).fill(0)
  for (const at of calls) {
    if (at < start) continue
    out[Math.min(out.length - 1, Math.floor((at - start) / MINUTE))]! += 1
  }
  return out
}

/** The sessions the swimlane draws: every live one, and one that failed this hour. */
export function onSwimlane(s: UnifiedSession, from: number): boolean {
  return isLiveSession(s) || failedAt(s, from) !== undefined
}

export function buildLanes({
  sessions,
  events,
  approvals,
  now,
}: LaneInput): Lane[] {
  const from = now - WINDOW_MS
  const flaggedId = approvals[0]?.id
  return sessions
    .filter((s) => onSwimlane(s, from))
    .map((session) => {
      const own = events.get(session.key)
      const failed = failedAt(session, from)
      const working = session.runs.some(isWorkingRun)
      const idle =
        !working &&
        (session.runs.some(isLiveRun) || session.live?.cc_state === 'idle')
      const mine = approvalsOf(session, approvals)
      const asks: LaneAsk[] = mine.flatMap((a) => {
        const at = time(a.created_at)
        return at !== undefined && at >= from
          ? [{ id: a.id, at, flagged: a.id === flaggedId }]
          : []
      })
      const raw = own ? runsOf(own, from, now) : []
      const lastEvent = raw.length ? raw[raw.length - 1]!.end : undefined
      // A run that works now reaches now while its events keep coming, unless an
      // approval asked after its last event stopped it: then the wait takes over. A
      // working run that has been quiet longer than a gap ends where its events end.
      const live =
        working &&
        lastEvent !== undefined &&
        now - lastEvent <= RUN_GAP_MS &&
        !mine.some((a) => (time(a.created_at) ?? -Infinity) >= lastEvent)
      const runs: LaneRun[] = raw.map((r, i) => {
        const end = live && i === raw.length - 1 ? now : r.end
        return {
          start: r.start,
          end,
          live: live && i === raw.length - 1,
          density: densityOf(r.start, end, r.calls),
        }
      })
      const waits: LaneWait[] = mine.map((a) => {
        const asked = time(a.created_at)
        const start = asked === undefined ? now : Math.max(asked, from)
        return {
          id: a.id,
          start,
          end: now,
          asked,
          flagged: a.id === flaggedId,
          strip: runs.some((r) => r.end > start),
        }
      })
      const state: LaneState =
        failed !== undefined
          ? 'failed'
          : mine.length > 0 || session.runs.some(runWantsAPerson)
            ? 'waiting'
            : idle
              ? 'idle'
              : 'working'
      const idleSince =
        state === 'idle'
          ? (lastEvent ?? time(session.live?.last_event_at))
          : undefined
      return {
        session,
        state,
        runs,
        waits,
        asks,
        idleFrom:
          state === 'idle' ? Math.max(idleSince ?? from, from) : undefined,
        idleSince,
        failedAt: failed,
        toolCalls: runs.reduce(
          (n, r) => n + r.density.reduce((a, b) => a + b, 0),
          0,
        ),
        read: own !== undefined,
      }
    })
}

/** Density in four tones: none, a few, steady, busy (tool calls in one minute). */
export function densityStep(calls: number): 0 | 1 | 2 | 3 {
  return calls === 0 ? 0 : calls <= 2 ? 1 : calls <= 5 ? 2 : 3
}

/** The ten-minute marks inside the hour that ends now, on the viewer's clock (:00, :10,
 * whatever the time zone's offset). A mark closer to now than NOW_ROOM_MS is left out:
 * the now line and its label stand there. */
export function windowTicks(now: number): number[] {
  const offset = new Date(now).getTimezoneOffset() * MINUTE
  const out: number[] = []
  for (
    let t = Math.ceil((now - WINDOW_MS - offset) / TICK_MS) * TICK_MS + offset;
    t <= now - NOW_ROOM_MS;
    t += TICK_MS
  )
    if (t > now - WINDOW_MS) out.push(t)
  return out
}

/** A selected range as the Ledger's own window (`since`/`until`), whole minutes that
 * cover it, earliest first. */
export function ledgerRange(a: number, b: number) {
  const since = Math.floor(Math.min(a, b) / MINUTE) * MINUTE
  const until = Math.ceil(Math.max(a, b) / MINUTE) * MINUTE
  return {
    since: new Date(since).toISOString(),
    until: new Date(until).toISOString(),
  }
}
