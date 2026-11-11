// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { runStateWord } from '@/features/agentops/run-state-word'
import type { RunState } from '@/features/agentops/types'
import {
  isLiveRun,
  isWorkingRun,
  primaryRun,
  runWantsAPerson,
  type UnifiedSession,
} from './provenance'
import type { WorkGroupId } from './session-groups'
import type { CcState } from './types'

export type RailState = 'need' | 'live' | 'ended' | 'idle'
export type StateTone = 'needs' | 'working' | 'idle' | 'failed'

/**
 * THE STATES A PERSON READS, each with one glyph (`SessionStatusMark`) and one word.
 * Working: a process is at work. Needs you: it waits for a person. Done: the turn or the
 * run finished. Failed: the run failed, or the provider refused its last turn. Paused: a
 * provider limit stopped it until the reset. Idle: an observed session at rest.
 * Every list that says a session's state (Sessions, Now, the palette) reads this one.
 */
export type SessionStatus =
  'working' | 'needs-you' | 'done' | 'failed' | 'paused' | 'idle'

interface SessionDisplayState {
  runState?: RunState
  observedState?: CcState
  group: WorkGroupId
  rail: RailState
  tone: StateTone
  status: SessionStatus
}

/** Engine facts only. Liveness and input permission remain separate from display. */
export function sessionDisplayState(
  session: Pick<UnifiedSession, 'runs' | 'live'>,
): SessionDisplayState {
  const run = primaryRun(session.runs)
  // The run's word is said once (`runStateWord`): the badge and /agentops read it too.
  const word = run ? runStateWord(run) : undefined
  const live = session.live
  const needs =
    live?.cc_state === 'silent_evasion' ||
    live?.unclaimed === true ||
    session.runs.some(runWantsAPerson)
  const working =
    session.runs.length > 0
      ? session.runs.some(isWorkingRun)
      : live?.cc_state === 'active'
  const group = needs ? 'attention' : working ? 'active' : 'settled'
  // The sidebar's count and the list's section are one read, so the same facts decide both.
  const rail = needs
    ? 'need'
    : working
      ? 'live'
      : (run ? isLiveRun(run) : live?.cc_state !== 'ended')
        ? 'idle'
        : 'ended'
  const tone: StateTone =
    run?.state === 'failed'
      ? 'failed'
      : group === 'attention'
        ? 'needs'
        : group === 'active'
          ? 'working'
          : word === 'failed'
            ? 'failed'
            : 'idle'
  // A run that answered its turn, stopped or ended is Done; only an observed session at
  // rest, or one the engine said nothing of, is Idle. A limit is Paused, in grey.
  const status: SessionStatus =
    tone === 'failed'
      ? 'failed'
      : tone === 'needs'
        ? 'needs-you'
        : tone === 'working'
          ? 'working'
          : word === 'paused'
            ? 'paused'
            : run || live?.cc_state === 'ended'
              ? 'done'
              : 'idle'
  return {
    runState: run?.state,
    observedState: run ? undefined : live?.cc_state,
    group,
    rail,
    tone,
    status,
  }
}
