// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import {
  mergeSessions,
  operatorName,
  runWantsAPerson,
  sessionPlace,
  sessionShortId,
} from '@/features/sessions/provenance'
import {
  isArchived,
  isMarkedDone,
  isSnoozed,
} from '@/features/sessions/session-groups'
import type { RunDTO } from '@/features/agentops/types'
import type { RailSources } from './session-rail-model'

export interface AttentionSnapshot {
  key: string
  /** Run reference and start time: a resume retires the previous launch. */
  run: string
  address: string
  aliases?: string[]
  name: string
  state: 'working' | 'needs-you' | 'finished' | 'quiet' | 'ended'
}

/**
 * The engine shows `idle` both for a turn that ended and for a run silent for its
 * five-minute idle window (deriveRunState, modules/sessions/runtime_dto.go). Silence
 * needs more than that window since the last activity, so an idle run that was active
 * within less is a turn that ended. Two minutes of margin cover the rail's minute
 * clock and a browser clock that is a little off; beyond it, the run stays quiet
 * instead of claiming a finished turn.
 * ponytail: mirrors the engine's fixed window; an additive waiting_for_input field
 * replaces it if the window ever becomes configurable.
 */
export const TURN_ENDED_WITHIN_MS = 3 * 60_000

/** Admission is a transition, never a newly attached row or a streamed update. */
export function attentionEvent(
  previous: AttentionSnapshot | undefined,
  current: AttentionSnapshot | undefined,
  context: {
    hidden: boolean
    opened: string | null
    now: number
    lastNotified?: number
  },
): 'needsYou' | 'finished' | null {
  if (!previous || !current || previous.run !== current.run) return null
  if (!context.hidden && isOnScreen(current, context.opened)) return null
  if (
    context.lastNotified !== undefined &&
    context.now - context.lastNotified < 5_000
  )
    return null
  if (current.state === previous.state) return null
  if (current.state === 'needs-you') return 'needsYou'
  if (current.state === 'finished' && previous.state === 'working')
    return 'finished'
  return null
}

export function isOnScreen(
  session: AttentionSnapshot,
  opened: string | null,
): boolean {
  return (
    opened !== null &&
    (session.address === opened || session.aliases?.includes(opened) === true)
  )
}

/** Use the rail's full reads, before the sidebar's row cap. A session the person snoozed
 * or marked done (#2480), or an archived one (#2795), is not watched: it raises no
 * notification and is not in the tab title's count, as it is not in Needs you. */
export function attentionSnapshots(
  sources: RailSources,
  now: number,
): AttentionSnapshot[] {
  return mergeSessions([...sources.live], [...(sources.runs ?? [])])
    .filter(
      (session) =>
        !isSnoozed(session) && !isMarkedDone(session) && !isArchived(session),
    )
    .map((session) => {
      // This is the latest launch, even if an older run retained more control.
      const run = session.runs.reduce<
        (typeof session.runs)[number] | undefined
      >(
        (latest, item) =>
          !latest ||
          (item.started_at ?? item.created_at ?? '') >
            (latest.started_at ?? latest.created_at ?? '')
            ? item
            : latest,
        undefined,
      )
      const ended = run
        ? !['running', 'idle', 'waiting_approval', 'pending'].includes(
            run.state,
          )
        : session.live?.cc_state === 'ended'
      const needsYou = run
        ? runWantsAPerson(run)
        : session.live?.cc_state === 'silent_evasion' ||
          session.live?.unclaimed === true
      // What a notification calls a session: the operator's own name, else its tool and
      // folder with the row's tail (as a rail row does when another shares the name), else
      // the key (#2266). NEVER the list's summary or goal: that is text the session wrote,
      // often a prompt, and this title is painted by the OS outside the console.
      const place = sessionPlace(session)
      return {
        key: session.key,
        run: run
          ? `${run.run_ref}:${run.started_at ?? run.created_at ?? ''}`
          : session.key,
        address: session.key,
        aliases: session.runs.map((r) => `run:${r.run_ref}`),
        name:
          operatorName(session) ||
          (place ? `${place} ${sessionShortId(session)}` : session.key),
        state: ended
          ? 'ended'
          : needsYou
            ? 'needs-you'
            : run?.state === 'running' ||
                (!run && session.live?.cc_state === 'active')
              ? 'working'
              : run && turnEnded(run, now)
                ? 'finished'
                : 'quiet',
      }
    })
}

function turnEnded(run: RunDTO, now: number): boolean {
  if (run.state !== 'idle') return false
  const last = Date.parse(run.last_activity_at ?? '')
  return !Number.isNaN(last) && now - last < TURN_ENDED_WITHIN_MS
}
