// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { RunDTO } from '@/features/agentops/types'
import { sameModel } from './conversation-frames'
import type { LiveDTO } from './types'

/**
 * The model a session names, in ONE order for every surface: the run's (what the launch
 * asked for), else the one its provider reported, else the observed row's. The observed
 * row's `model_ref` is the model of the LAST cost sample, so a session that took a side
 * call on another model would otherwise be named after that call.
 */
export function sessionModelRef(
  run: RunDTO | null | undefined,
  live: LiveDTO | null | undefined,
): string | undefined {
  return run?.model_ref || run?.usage_model_ref || live?.model_ref || undefined
}

/**
 * The model the launch asked for and the one its turns run on, when they differ
 * (dev#2786). Only a Claude Code launch on its own sign-in is asked for an exact id;
 * a provider names its models itself, so a provider-bound name is not held against it.
 */
export function modelMismatch(
  run: RunDTO | null | undefined,
  inUse: string | null | undefined,
): { requested: string; inUse: string } | null {
  const requested = run?.model_ref
  if (
    !requested ||
    !inUse ||
    run.provider_driver !== 'claude' ||
    run.provider_auth_source === 'managed_injection' ||
    sameModel(requested, inUse)
  )
    return null
  return { requested, inUse }
}

/** This session's usage: tokens always, the cost only when one was reported. */
export interface SessionUsage {
  input: number
  output: number
  costMicroUsd?: number
  costPartial: boolean
}

/**
 * The usage the header shows, summed across the session's runs. Money absent from
 * one run makes a known subtotal partial; absent from all runs stays unknown.
 * A session Olivares did not start has only its live row. None reported is `null`.
 */
export function sessionUsage(
  runs: readonly RunDTO[],
  live: LiveDTO | null | undefined,
): SessionUsage | null {
  if (runs.length > 0) {
    let input = 0
    let output = 0
    let costMicroUsd: number | undefined
    let reported = false
    let unpriced = false
    for (const run of runs) {
      input += run.input_tokens ?? 0
      output += run.output_tokens ?? 0
      reported ||= run.input_tokens != null || run.output_tokens != null
      if (run.cost_micro_usd != null) {
        costMicroUsd = (costMicroUsd ?? 0) + run.cost_micro_usd
        reported = true
      } else unpriced = true
    }
    return reported
      ? {
          input,
          output,
          costMicroUsd,
          costPartial: costMicroUsd != null && unpriced,
        }
      : null
  }
  if (
    live &&
    (live.input_tokens > 0 || live.output_tokens > 0 || live.cost_micro_usd > 0)
  )
    return {
      input: live.input_tokens,
      output: live.output_tokens,
      costMicroUsd: live.cost_micro_usd > 0 ? live.cost_micro_usd : undefined,
      costPartial: false,
    }
  return null
}
