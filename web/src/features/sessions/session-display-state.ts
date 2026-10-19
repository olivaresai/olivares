// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { RunState } from '@/features/agentops/types'
import {
  isWorkingRun,
  primaryRun,
  runWantsAPerson,
  type UnifiedSession,
} from './provenance'
import type { WorkGroupId } from './session-groups'
import type { CcState } from './types'

export type RailState = 'need' | 'live' | 'ended' | 'idle'
export type StateTone = 'needs' | 'working' | 'idle' | 'failed'

interface SessionDisplayState {
  runState?: RunState
  observedState?: CcState
  group: WorkGroupId
  rail: RailState
  tone: StateTone
}

/** Engine facts only. Liveness and input permission remain separate from display. */
export function sessionDisplayState(
  session: Pick<UnifiedSession, 'runs' | 'live'>,
): SessionDisplayState {
  const run = primaryRun(session.runs)
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
  const rail = run
    ? runWantsAPerson(run) || run.state === 'pending'
      ? 'need'
      : run.state === 'running'
        ? 'live'
        : run.state === 'idle'
          ? 'idle'
          : 'ended'
    : needs
      ? 'need'
      : live?.cc_state === 'active'
        ? 'live'
        : live?.cc_state === 'ended'
          ? 'ended'
          : 'idle'
  return {
    runState: run?.state,
    observedState: run ? undefined : live?.cc_state,
    group,
    rail,
    tone:
      run?.state === 'failed'
        ? 'failed'
        : group === 'attention'
          ? 'needs'
          : group === 'active'
            ? 'working'
            : 'idle',
  }
}
