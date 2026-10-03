// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { RunStateBadge } from '@/features/agentops/run-state-badge'
import type { RunDTO } from '@/features/agentops/types'
import { CcStateBadge } from './cc-state-badge'
import type { LiveDTO } from './types'

/**
 * ONE STATE PER SESSION. A launched session's state is its run's; the observed state is
 * for sessions Olivares did not start. Showing both read "Active · Stopped · Live" after a
 * Stop (HU-11 in the narrative, EU on Business 06 in the detail sheet), because the
 * observed row keeps its last signal after the process ends.
 */
export function SessionStateBadge({
  run,
  live,
  className,
}: {
  run?: RunDTO
  live?: LiveDTO
  className?: string
}) {
  if (run) return <RunStateBadge state={run.state} className={className} />
  if (live) return <CcStateBadge state={live.cc_state} className={className} />
  return null
}

/** The live dot only while something can stream: no run, or a run that works. */
export function showsLiveDot(run?: RunDTO, live?: LiveDTO): boolean {
  return !!live && (!run || run.state === 'running' || run.state === 'idle')
}
