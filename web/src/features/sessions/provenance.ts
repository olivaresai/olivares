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
import type { Attribution, LiveDTO } from './types'
import type { SessionTarget } from './session-target'

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
  /** Stable row identity. `live:<live_ref>` for a profile-scoped live row (B2 —
   * two homes may announce one id, so the row's own id is the key); `sess:<ref>`
   * for a LEGACY row or a legacy run's session; else `run:<ref>` — so two runs on
   * ONE session are one row. */
  key: string
  /** The observed session reference, when the plane knows one. */
  sessionRef?: string
  /** B2: the exact live row this session IS (absent for a run-only row whose managed
   * row has not been proven yet). */
  liveRef?: string
  /** B2: which channel the live row was folded from (absent without a row). */
  attribution?: Attribution
  /** B2: the provider profile the row, or the run, is attributed to. */
  profileRef?: string
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
  // B2: a PROFILED run is joined to its session by the row the plane PROVED — the
  // managed row whose id the run carries — and by nothing else. A legacy run never
  // joins a profile-scoped row: two homes may announce the very id it captured.
  if (run.provider_profile_ref || isScopedRow(live)) {
    return (
      !!run.live_ref &&
      run.live_ref === live.live_ref &&
      live.attribution === 'managed'
    )
  }
  if (!run.claude_session_id) return false
  if (run.claude_session_id !== live.session_ref) return false
  return !live.engine || live.engine === 'claude'
}

/** A row folded under a profile, a known source or the plane's bridge — anything
 * but the legacy channel. Its identity is its `live_ref`, never its external id. */
export function isScopedRow(live: LiveDTO): boolean {
  return !!live.attribution && live.attribution !== 'legacy'
}

/** The row key of a live row: the row's own id when scoped, the bare external id
 * (the legacy join key) otherwise. */
export function liveRowKey(live: LiveDTO): string {
  return isScopedRow(live)
    ? `live:${live.live_ref}`
    : `sess:${live.session_ref}`
}

/**
 * What the card should open on for a row: a scoped row by its `live_ref` (the only
 * unambiguous name it has), a legacy row by its bare id, and a run-only row by its
 * run — a profiled run then resolves its own managed row from the engine.
 */
export function sessionTarget(s: UnifiedSession): SessionTarget {
  if (s.liveRef && (s.live ? isScopedRow(s.live) : true)) {
    return { liveRef: s.liveRef }
  }
  const run = primaryRun(s.runs)
  if (s.sessionRef && !(run?.provider_profile_ref && !s.live)) {
    return { sessionRef: s.sessionRef }
  }
  return { runRef: run?.run_ref }
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
    const key = liveRowKey(l)
    rows.set(key, {
      key,
      sessionRef: l.session_ref,
      liveRef: l.live_ref || undefined,
      attribution: l.attribution,
      profileRef: l.provider_profile_ref || undefined,
      runs: [],
      live: l,
      provenance: 'discovered',
      control: 'observe',
      lastActivityMs: 0,
    })
  }

  for (const run of runs) {
    const sid = run.claude_session_id
    // B2: a profiled run's only join is the managed row the plane proved for it
    // (`run.live_ref`). A legacy run joins the LEGACY row of its bare id, as before.
    const profiled = !!run.provider_profile_ref
    const key = profiled
      ? run.live_ref
        ? `live:${run.live_ref}`
        : `run:${run.run_ref}`
      : sid
        ? `sess:${sid}`
        : `run:${run.run_ref}`
    const existing = rows.get(key)
    // An observed row that declares a DIFFERENT engine is not this run's session, so
    // the run keeps its own row rather than being folded onto a stranger.
    if (existing?.live && !runMatchesObserved(run, existing.live)) {
      const own = `run:${run.run_ref}`
      rows.set(own, {
        key: own,
        sessionRef: undefined,
        liveRef: profiled ? run.live_ref || undefined : undefined,
        profileRef: run.provider_profile_ref || undefined,
        runs: [run],
        provenance: 'launched',
        control: 'observe',
        lastActivityMs: 0,
      })
      continue
    }
    if (existing) {
      existing.runs.push(run)
      if (!existing.profileRef && run.provider_profile_ref)
        existing.profileRef = run.provider_profile_ref
      continue
    }
    rows.set(key, {
      key,
      // A profiled run's captured id is scoped to its profile: it is shown, but it
      // is not a key anything else may join on.
      sessionRef: sid || undefined,
      liveRef: profiled ? run.live_ref || undefined : undefined,
      profileRef: run.provider_profile_ref || undefined,
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
    s.liveRef ?? '',
    s.profileRef ?? '',
    ...s.runs.map((r) => r.run_ref),
    ...s.runs.map((r) => r.name ?? ''),
  ]
    .filter(Boolean)
    .join(' ')
}
