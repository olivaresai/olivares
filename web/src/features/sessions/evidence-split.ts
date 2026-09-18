// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE THREE READINGS OF ONE TIMELINE PAGE — pure, and in its own module because
// a rule driven directly by a test is worth more than the same rule inside a component,
// and because this repo's fast-refresh lint is right that a component file should
// export components.
import type { TimelineDTO } from './types'

/** How many entries one read brings back. A ceiling, and the counts say when it is hit. */
export const EVIDENCE_PAGE = 100

interface Split {
  checks: TimelineDTO[]
  activity: TimelineDTO[]
  resources: { ref: string; count: number }[]
  /** The read filled its page: every count below is a floor, not a total. */
  bounded: boolean
}

/** Split one page of the timeline into the three readings. Pure and exported for its test. */
export function splitEvidence(
  entries: readonly TimelineDTO[],
  hasMore: boolean,
): Split {
  const checks: TimelineDTO[] = []
  const activity: TimelineDTO[] = []
  const byResource = new Map<string, number>()
  for (const e of entries) {
    if (e.kind === 'finding') checks.push(e)
    else if (e.kind === 'tool' || e.kind === 'mcp') activity.push(e)
    const ref = e.resource_ref?.trim()
    if (ref) byResource.set(ref, (byResource.get(ref) ?? 0) + 1)
  }
  return {
    checks,
    activity,
    resources: [...byResource.entries()]
      .map(([ref, count]) => ({ ref, count }))
      .sort((a, b) => b.count - a.count || a.ref.localeCompare(b.ref)),
    bounded: hasMore,
  }
}
