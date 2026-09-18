// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE ONE SENTENCE THAT SAYS WHAT A SESSION WAS DOING.
//
// Pure, and in its own module so the component file exports only a component (see
// next-step-catalog.ts for why that matters here) and so the degradation ladder can be
// driven directly by a test, rung by rung, without rendering anything.
import type { LiveDTO } from '@/features/sessions/types'

/** How many rows the front door tells. Five is a screenful; the room holds the rest. */
export const RECENT_WORK_ROWS = 5

/** Which field the sentence came from — reported so a caller can say so. */
export type WorkLineSource = 'summary' | 'goal' | 'action' | 'ref'

/**
 * ⛔ IT WILL NOT INVENT THE SENTENCE. `goal` and `summary` are documented as "often
 *    ABSENT — no objective channel in" (features/sessions/types.ts), and the demo
 *    estate confirms it: measured against `serve --seed-demo` on 2026-09-17, NOT ONE
 *    seeded row carries either. So the sentence degrades in a stated order — summary,
 *    then goal, then the action and resource the connector reported, then the session's
 *    own reference — and every rung is a fact the engine sent.
 */
export function workLine(s: LiveDTO): { text: string; from: WorkLineSource } {
  if (s.summary?.trim()) return { text: s.summary.trim(), from: 'summary' }
  if (s.goal?.trim()) return { text: s.goal.trim(), from: 'goal' }
  if (s.current_action?.trim()) {
    const resource = s.current_resource?.trim()
    return {
      text: resource ? `${s.current_action} · ${resource}` : s.current_action,
      from: 'action',
    }
  }
  return { text: s.session_ref, from: 'ref' }
}
