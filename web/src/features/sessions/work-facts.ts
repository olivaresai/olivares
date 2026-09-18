// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// WHAT A SESSION COST, IN THE FIELDS THE ENGINE SENT.
//
// This line was first built inside the front door's `WorkRow`. The work surface needs the
// same line, and the component is reused rather than telling the same session two ways —
// so the RULE lives in the plane that owns sessions, where both callers read it, and the
// front door is one of the callers.
//
// ⛔ A FIELD THE ENGINE DID NOT SEND IS LEFT OUT, NEVER PRINTED AS A ZERO. "0 tool
//    calls" is a claim that the session made none; an absent count is the claim that
//    nobody counted. The two look alike on a screen and mean opposite things, and this
//    is the one place the difference is decided.
import type { LiveDTO } from './types'

/**
 * The subset of i18next's `t` this needs.
 *
 * The keys below carry their namespace EXPLICITLY (`sessions:…`) and that is not
 * decoration: this module takes `t` as an argument, so `check-i18n-usage.mjs` — which
 * derives a file's namespace from its `useTranslation` call — has nothing to read here
 * and falls back to `common`, where these keys do not exist. Naming the namespace makes
 * the gate able to check them, and makes the call correct from any caller's `t`.
 */
export type FactTranslate = (
  key: string,
  options?: Record<string, unknown>,
) => string

export interface FactFormatters {
  /** Milliseconds → "15.0s". The console's own `formatDuration`. */
  duration: (ms: number) => string
  /** Micro-USD → "$0.042". The console's own `formatMicroUsd`. */
  cost: (microUsd: number) => string
}

/**
 * The facts line, in reading order: how long it worked, how many tool calls, how many
 * events, what it cost, which model. Every entry is a figure the engine sent.
 */
export function workFacts(
  s: LiveDTO,
  t: FactTranslate,
  fmt: FactFormatters,
): string[] {
  return [
    s.duration_seconds > 0
      ? t('sessions:work.worked', {
          duration: fmt.duration(s.duration_seconds * 1000),
        })
      : null,
    s.tool_call_count > 0
      ? t('sessions:work.toolCalls', { count: s.tool_call_count })
      : null,
    s.event_count > 0
      ? t('sessions:work.events', { count: s.event_count })
      : null,
    s.cost_micro_usd > 0 ? fmt.cost(s.cost_micro_usd) : null,
    s.model_ref ?? null,
  ].filter((f): f is string => f !== null)
}
