// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// one session is one session, whether you discovered it or launched it.
//
// The console used to model a session TWICE: an observed session under Visibility
// (`/sessions`, sessions:live:read) and an operated run under Management
// (`/agentops`, sessions:run:read), in different nav sections, with a detail card on
// only one of the two. That asked the operator to know, before looking, whether the
// session had been discovered or launched — which is the very thing they came to the
// screen to find out.
//
// This module is the JOIN and nothing else: pure functions over the two DTOs, so the
// rules below are testable without a DOM and cannot drift into a component.
//
// The join key is the provider's session id: a run's `claude_session_id` IS the
// `session_ref` of the live/timeline tables (modules/sessions/export.go:253 resolves a
// recording credential through that same identity, and runtime_bridge.go:80 captures
// it off the stream-json init frame).

import type { RunDTO, RunState, Transport } from '@/features/agentops/types'
import type { LiveDTO } from './types'

/** Where the session came from — a fact ABOUT THE SESSION, not a menu section.
 *  - `launched`   Olivares started this process; at least one run links to it.
 *  - `discovered` Olivares observes it and did not start it: no run links to it. */
export type Provenance = 'launched' | 'discovered'

/**
 * What OLIVARES can do with this session right now — the plane's reach, with no
 * reference to who is asking (that is `capabilities` below, and conflating the two is
 * how a console ends up saying "you can't" when it means "nobody can"):
 *  - `full`      a live process the plane owns AND whose I/O it bridges: it can be
 *                watched, driven and stopped.
 *  - `lifecycle` a process the plane owns but cannot see into (remote-control relays
 *                its I/O to Anthropic's cloud), or a stopped run it can resume: start
 *                and stop, nothing in between.
 *  - `observe`   no process the plane holds: it can be read and audited, not steered.
 */
export type ControlLevel = 'full' | 'lifecycle' | 'observe'

/** Why a capability is unavailable. The plane's limits (`no-run`, `no-observation`,
 * `transport`, `state`) and the caller's (`permission`) are deliberately different
 * answers: one says Olivares cannot, the other says YOU cannot. */
export type Unavailable =
  'no-run' | 'no-observation' | 'transport' | 'state' | 'permission'

export interface Capability {
  id: CapabilityId
  available: boolean
  /** Set only when `available` is false. */
  reason?: Unavailable
}

export type CapabilityId =
  'watch' | 'attach' | 'drive' | 'stop' | 'resume' | 'cleanup' | 'delete'

/** One session, however it reached us. */
export interface UnifiedSession {
  /** Stable row identity. `sess:<ref>` when a session id is known (observed, or
   * announced by a run), else `run:<ref>` — so two runs on ONE session are one row. */
  key: string
  /** The observed session reference, when the plane knows one. */
  sessionRef?: string
  /** Every run that drives this session, newest first. Length > 1 is a real state
   * (a resume, or a second launch against the same Claude session), so this is a
   * list rather than a winner picked by the console. */
  runs: RunDTO[]
  /** The observed snapshot, when it is loaded. ABSENT does NOT mean "no telemetry":
   * it can mean the session is not on the loaded page, or that the caller lacks
   * sessions:live:read. The card resolves it; the table says nothing about it. */
  live?: LiveDTO
  provenance: Provenance
  control: ControlLevel
  /** Sort key: the most recent activity from either side, as epoch ms (0 = unknown). */
  lastActivityMs: number
}

/** The run states in which the plane holds a live process. `idle` is DERIVED from
 * activity recency by the engine — the process is not killed — so it is live. */
const LIVE_RUN_STATES: RunState[] = ['pending', 'running', 'idle']
/** The states from which a run can be brought back. */
const RESUMABLE_RUN_STATES: RunState[] = ['stopped', 'failed']

export function isLiveRun(run: RunDTO): boolean {
  return LIVE_RUN_STATES.includes(run.state)
}

/**
 * A stopped run can be resumed only if the plane can re-attach to the conversation:
 * `stream-json` resumes with `claude --resume <claude_session_id>`, so without a
 * captured id there is nothing to resume into. Mirrors run-detail.tsx's own rule —
 * the backend is the source of truth and refuses regardless.
 */
export function isResumableRun(run: RunDTO): boolean {
  return (
    RESUMABLE_RUN_STATES.includes(run.state) &&
    (run.transport !== 'stream-json' || !!run.claude_session_id)
  )
}

/** I/O is bridged only for stream-json. `remote-control` is LIFECYCLE-ONLY: Olivares
 * manages the process but its I/O goes to Anthropic's cloud, so it cannot show it
 * (features/agentops/types.ts:24). */
export function isBridged(transport: Transport): boolean {
  return transport === 'stream-json'
}

/** The plane's reach over ONE run. */
function controlOfRun(run: RunDTO): ControlLevel {
  if (isLiveRun(run)) return isBridged(run.transport) ? 'full' : 'lifecycle'
  return isResumableRun(run) ? 'lifecycle' : 'observe'
}

const CONTROL_RANK: Record<ControlLevel, number> = {
  observe: 0,
  lifecycle: 1,
  full: 2,
}

/** The plane's reach over a session: the STRONGEST of its runs, because a session
 * driven by one bridged run and one relayed run really is fully controllable. */
export function controlLevel(runs: RunDTO[]): ControlLevel {
  let best: ControlLevel = 'observe'
  for (const run of runs) {
    const c = controlOfRun(run)
    if (CONTROL_RANK[c] > CONTROL_RANK[best]) best = c
  }
  return best
}

/**
 * The run the card acts on by default: the one that CARRIES the session's control
 * level, so the level, the label and the offered actions are all about the same run.
 *
 * Picking the first live run instead was a real inconsistency (found by the
 * contrast): a session driven by a live relayed run AND a live bridged one reported
 * "Full control" — the bridged run's reach — while the capability list described the
 * relayed one and denied the I/O the level had just promised.
 *
 * The card still lists every run and lets the operator act on any of them; this is
 * only which one it opens on.
 */
export function primaryRun(runs: RunDTO[]): RunDTO | undefined {
  let best: RunDTO | undefined
  let bestRank = -1
  for (const run of runs) {
    const rank = CONTROL_RANK[controlOfRun(run)]
    if (rank > bestRank) {
      best = run
      bestRank = rank
    }
  }
  return best
}

/** The permissions a capability needs, mirrored from the routes that enforce them
 * (modules/sessions/runtime_api.go:39-49). */
export interface Grants {
  liveRead: boolean
  runRead: boolean
  runWrite: boolean
  runAdmin: boolean
}

/**
 * What the PRINCIPAL can do with this session, and when they cannot, whether that is
 * the plane's limit or their own. The order of the checks is the honest order: a
 * capability the plane does not have at all is never reported as a permission
 * problem, because telling an operator to ask for a permission that would change
 * nothing is worse than telling them no.
 */
export function capabilities(
  s: Pick<UnifiedSession, 'runs' | 'sessionRef'>,
  grants: Grants,
): Capability[] {
  const run = primaryRun(s.runs)
  const out: Capability[] = []
  const cap = (id: CapabilityId, ok: boolean, reason?: Unavailable) =>
    out.push(ok ? { id, available: true } : { id, available: false, reason })

  // watch — the observed half. Available when a session reference exists to read.
  cap(
    'watch',
    !!s.sessionRef && grants.liveRead,
    !s.sessionRef ? 'no-observation' : 'permission',
  )

  if (!run) {
    for (const id of [
      'attach',
      'drive',
      'stop',
      'resume',
      'cleanup',
      'delete',
    ] as const) {
      cap(id, false, 'no-run')
    }
    return out
  }

  const live = isLiveRun(run)
  const bridged = isBridged(run.transport)

  cap(
    'attach',
    live && bridged && grants.runRead,
    !live ? 'state' : !bridged ? 'transport' : 'permission',
  )
  cap(
    'drive',
    live && bridged && grants.runWrite,
    !live ? 'state' : !bridged ? 'transport' : 'permission',
  )
  cap('stop', live && grants.runWrite, !live ? 'state' : 'permission')
  cap(
    'resume',
    isResumableRun(run) && grants.runWrite,
    !RESUMABLE_RUN_STATES.includes(run.state)
      ? 'state'
      : !isResumableRun(run)
        ? 'transport' // stopped stream-json run with no captured id: nothing to resume into
        : 'permission',
  )
  cap(
    'cleanup',
    RESUMABLE_RUN_STATES.includes(run.state) && grants.runAdmin,
    !RESUMABLE_RUN_STATES.includes(run.state) ? 'state' : 'permission',
  )
  cap(
    'delete',
    run.state === 'cleaned' && grants.runAdmin,
    run.state !== 'cleaned' ? 'state' : 'permission',
  )
  return out
}

function epoch(ts?: string): number {
  if (!ts) return 0
  const n = Date.parse(ts)
  return Number.isNaN(n) ? 0 : n
}

/** The most recent moment either side of a session was seen. */
function activityOf(live: LiveDTO | undefined, runs: RunDTO[]): number {
  let ms = epoch(live?.last_event_at)
  for (const r of runs) {
    ms = Math.max(
      ms,
      epoch(r.last_activity_at),
      epoch(r.started_at),
      epoch(r.created_at),
    )
  }
  return ms
}

/**
 * A run's `claude_session_id` is, by construction, a CLAUDE session id: the runtime
 * launches `claude` and reads the id off ITS init frame. An observed row is only the
 * same session if it is a Claude session too.
 *
 * This is not a hypothetical: provider ids are opaque strings and two engines can
 * issue the same one, which is exactly why the identity plane keys its aliases on
 * (provider, external_id) rather than on the id alone (modules/sessions/identity.go:18).
 * The Codex connector declares `engine: "codex"` on every edge it emits
 * (connectors/codex/session/observations.go:40); the Claude connector declares nothing,
 * so an unlabelled row is treated as a possible Claude session — a run is folded into
 * a row whose engine CONTRADICTS it, never into one that merely fails to confirm it.
 *
 * Found by the contrast (Codex sol max, 2026-08-10): the join used the bare id
 * and would have folded a Claude run onto a Codex session that happened to share it.
 */
export function runMatchesObserved(run: RunDTO, live: LiveDTO): boolean {
  if (!run.claude_session_id) return false
  if (run.claude_session_id !== live.session_ref) return false
  return !live.engine || live.engine === 'claude'
}

/**
 * The JOIN: one row per session, from the observed page and the run page.
 *
 * A run whose `claude_session_id` is not among the observed rows still gets a row of
 * its own keyed by that id — it is a session the plane launched whose telemetry has
 * not arrived (or is not on this page). Merging it into nothing would hide a running
 * session; inventing an observed half for it would be worse.
 */
export function mergeSessions(
  live: LiveDTO[],
  runs: RunDTO[],
): UnifiedSession[] {
  const rows = new Map<string, UnifiedSession>()

  for (const l of live) {
    const key = `sess:${l.session_ref}`
    rows.set(key, {
      key,
      sessionRef: l.session_ref,
      runs: [],
      live: l,
      provenance: 'discovered',
      control: 'observe',
      lastActivityMs: 0,
    })
  }

  for (const run of runs) {
    const sid = run.claude_session_id
    const key = sid ? `sess:${sid}` : `run:${run.run_ref}`
    const existing = rows.get(key)
    // An observed row that declares a DIFFERENT engine is not this run's session, so
    // the run keeps its own row rather than being folded onto a stranger.
    if (existing?.live && !runMatchesObserved(run, existing.live)) {
      const own = `run:${run.run_ref}`
      rows.set(own, {
        key: own,
        sessionRef: undefined,
        runs: [run],
        provenance: 'launched',
        control: 'observe',
        lastActivityMs: 0,
      })
      continue
    }
    if (existing) {
      existing.runs.push(run)
      continue
    }
    rows.set(key, {
      key,
      sessionRef: sid || undefined,
      runs: [run],
      provenance: 'launched',
      control: 'observe',
      lastActivityMs: 0,
    })
  }

  const out: UnifiedSession[] = []
  for (const row of rows.values()) {
    row.provenance = row.runs.length > 0 ? 'launched' : 'discovered'
    row.control = controlLevel(row.runs)
    row.lastActivityMs = activityOf(row.live, row.runs)
    out.push(row)
  }
  out.sort((a, b) => b.lastActivityMs - a.lastActivityMs)
  return out
}

/** A row's display label: the run's operator-given name when there is one (that is
 * what the operator typed), else the session reference, else the run reference. */
export function sessionLabel(s: UnifiedSession): string {
  // The PRIMARY run's name first, not merely the first named one. Seen on screen
  // (2026-08-10, demo estate): a session driven by a relayed run AND a bridged one was
  // titled by the relayed one while the row reported "Full control" — a reach that
  // came from the other run. The row now names the run whose reach it is reporting.
  const named = primaryRun(s.runs)?.name || s.runs.find((r) => r.name)?.name
  return named || s.sessionRef || s.runs[0]?.run_ref || s.key
}

/** Everything a row can be searched by: its label, its session reference and every
 * run reference. The label alone would make a session UNFINDABLE by the very id the
 * rest of the plane (the ledger, the API, a deep link) calls it — the observed screen
 * used to show that ref as the row title, so searching it has to keep working. */
export function sessionSearchKey(s: UnifiedSession): string {
  return [
    sessionLabel(s),
    s.sessionRef ?? '',
    ...s.runs.map((r) => r.run_ref),
    ...s.runs.map((r) => r.name ?? ''),
  ]
    .filter(Boolean)
    .join(' ')
}
