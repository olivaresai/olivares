// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { RunDTO } from '@/features/agentops/types'
import type { LiveDTO } from './types'

/** This session's usage: tokens always, the cost only when one was reported. */
export interface SessionUsage {
  input: number
  output: number
  costMicroUsd?: number
}

/**
 * The usage the header shows. A launched session's is its run's counters, summed
 * over its launches; a session Olivares did not start has only its live row. None
 * reported is `null`: unknown, which the header leaves out.
 */
export function sessionUsage(
  run: RunDTO | null | undefined,
  live: LiveDTO | null | undefined,
): SessionUsage | null {
  if (
    run &&
    (run.input_tokens != null ||
      run.output_tokens != null ||
      run.cost_micro_usd != null)
  )
    return {
      input: run.input_tokens ?? 0,
      output: run.output_tokens ?? 0,
      costMicroUsd: run.cost_micro_usd,
    }
  if (
    !run &&
    live &&
    (live.input_tokens > 0 || live.output_tokens > 0 || live.cost_micro_usd > 0)
  )
    return {
      input: live.input_tokens,
      output: live.output_tokens,
      costMicroUsd: live.cost_micro_usd > 0 ? live.cost_micro_usd : undefined,
    }
  return null
}
