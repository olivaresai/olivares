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
export type WorkLineSource = 'summary' | 'goal' | 'action' | 'untitled'

/** The distinguishing tail of a session id, never the `sess-` type prefix. */
export function shortSessionId(id: string): string {
  return id.replace(/^sess-/, '')
}

/**
 * ⛔ IT WILL NOT INVENT THE SENTENCE. `goal` and `summary` are documented as "often
 *    ABSENT — no objective channel in" (features/sessions/types.ts), and the demo
 *    estate confirms it: measured against `serve --seed-demo` on 2026-09-17, NOT ONE
 *    seeded row carries either. So the sentence degrades in a stated order — summary,
 *    then goal, then the action and resource the connector reported, then untitled
 *    with the id on `title=` — and every rung is a fact the engine sent. The id is
 *    never the row name.
 */
export function workLine(s: LiveDTO): {
  text: string
  from: WorkLineSource
  id?: string
} {
  if (s.summary?.trim()) return { text: s.summary.trim(), from: 'summary' }
  if (s.goal?.trim()) return { text: s.goal.trim(), from: 'goal' }
  if (s.current_action?.trim()) {
    const resource = s.current_resource?.trim()
    return {
      text: resource ? `${s.current_action} · ${resource}` : s.current_action,
      from: 'action',
    }
  }
  return { text: '', from: 'untitled', id: s.session_ref }
}

/** Which rung named a session: the ladder above, with the operator's own name on top. */
export type SessionNameSource = 'run' | WorkLineSource

/**
 * WHAT A SESSION IS CALLED — THE ONE LADDER, and the reason this function exists is that
 * there used to be three.
 *
 * ⛔ THREE HELPERS ANSWERED THIS QUESTION WITH THREE DIFFERENT ANSWERS, measured on one
 *    input: a session whose run the operator named `deploy-api`, with no summary. The
 *    sessions table said `deploy-api`; the shared directory lookup answered `null`, so
 *    `/audit`, `/recordings` and `/inventory` printed the reference; and a fourth caller
 *    printed the reference outright, because its own last rung WAS the reference. One
 *    session, four surfaces, four names — and every one of them green.
 *
 * ⛔ THE RUNGS, IN ORDER, AND EVERY ONE A FACT SOMEBODY SENT:
 *      run — the name the operator typed at launch. It outranks everything below it
 *            because it is the only rung a person chose;
 *      summary, goal, action — `workLine`, called and not copied, so the front door's
 *            sentence and a session's name can never drift apart;
 *      untitled — the caller's own word. NEVER the reference: a rail of six rows reading
 *            `sess-batch-1 … sess-batch-4` tells an operator nothing to choose between.
 *
 * ⛔ AND IT REPORTS WHICH RUNG ANSWERED, so a caller states a POLICY instead of walking
 *    the ladder again. The table stops above `action` because it paints the action in a
 *    column of its own three centimetres to the right; the directory lookup refuses
 *    `untitled` because the word belongs to the screen that paints it. Both are one
 *    line reading `from`, which is what keeps this the only ladder.
 *
 * `runName` is `null` where no run is in hand — the live page carries none — and that
 * is a fact about the READ, not a shorter ladder.
 */
export function sessionNameLadder(
  runName: string | null | undefined,
  live: LiveDTO | null | undefined,
  untitled: string,
): { text: string; from: SessionNameSource } {
  const named = runName?.trim()
  if (named) return { text: named, from: 'run' }
  const line = live ? workLine(live) : null
  if (line && line.from !== 'untitled')
    return { text: line.text, from: line.from }
  return { text: untitled, from: 'untitled' }
}
