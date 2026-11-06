// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The live sessions, counted by the tool each runs and by the person it acts for. Pure, over
// the merged sessions Now already reads: the same rule says which are live (`isLiveSession`)
// and the same facts say the tool and the person, so Now and this page never disagree.
import { isLiveSession } from '@/features/home/now-sessions'
import { primaryRun, sessionTool } from '@/features/sessions/provenance'
import type { UnifiedSession } from '@/features/sessions/provenance'

/** One row of a breakdown. `named` has a `name`; the other two are words the view supplies:
 *  automation is a session no person launched, unknown one that does not say. */
export interface Bucket {
  kind: 'named' | 'automation' | 'unknown'
  name: string
  count: number
}

type Placed = Omit<Bucket, 'count'> & { id: string }

const UNKNOWN: Placed = { id: 'unknown', kind: 'unknown', name: '' }

function tally(
  sessions: readonly UnifiedSession[],
  placeOf: (s: UnifiedSession) => Placed,
): Bucket[] {
  const buckets = new Map<string, Bucket>()
  for (const s of sessions) {
    const { id, ...bucket } = placeOf(s)
    const seen = buckets.get(id)
    if (seen) seen.count++
    else buckets.set(id, { ...bucket, count: 1 })
  }
  // Ties go by name, then by kind: two rows with no name keep one order between polls.
  return [...buckets.values()].sort(
    (a, b) =>
      b.count - a.count ||
      a.name.localeCompare(b.name, 'en') ||
      a.kind.localeCompare(b.kind, 'en'),
  )
}

export function liveBreakdown(sessions: readonly UnifiedSession[]): {
  total: number
  byTool: Bucket[]
  byPerson: Bucket[]
} {
  const live = sessions.filter(isLiveSession)
  return {
    total: live.length,
    byTool: tally(live, (s) => {
      const tool = sessionTool(s)
      return tool ? { id: `tool:${tool}`, kind: 'named', name: tool } : UNKNOWN
    }),
    byPerson: tally(live, (s) => {
      const actsFor = primaryRun(s.runs)?.acts_for
      if (actsFor?.kind === 'automation')
        return { id: 'automation', kind: 'automation', name: '' }
      const name = actsFor?.name || actsFor?.user_id
      if (actsFor?.kind !== 'user' || !name) return UNKNOWN
      return { id: `user:${actsFor.user_id ?? name}`, kind: 'named', name }
    }),
  }
}
