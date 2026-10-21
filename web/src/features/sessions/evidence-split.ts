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

/** Closer together than this, a group's entries are one moment, not a range. */
const RANGE_MS = 60_000

/** Identical entries of one session, folded: what they said, how often, and when. */
export interface EntryGroup {
  /** What identifies the group: the kind, the words and the resource. Unique among groups. */
  key: string
  kind: TimelineDTO['kind']
  /** The entry's own words; empty when it carried none (the view names its kind). */
  label: string
  /** The resource the group is about, when it names one. */
  resource: string
  count: number
  newest: string
  oldest: string
  /** The span is long enough to say as a range; closer than that is one moment. */
  ranged: boolean
  /** Newest first. */
  entries: TimelineDTO[]
}

/** An unparseable `at` sorts last and never counts toward a range. */
const timeOf = (e: TimelineDTO): number => {
  const t = Date.parse(e.at)
  return Number.isNaN(t) ? Number.NEGATIVE_INFINITY : t
}

/**
 * Fold identical entries into one row each (#2267: "cooperative trace gap" three times
 * with one time is one fact told three times; a call that wrote two entries is one turn
 * told twice). Identical means the same kind and words about the same resource; anything
 * that differs stays its own row. `mode` and `source` are not on any row, so they do not
 * separate rows either: two rows that read the same would be the defect again.
 * Newest group first.
 */
export function groupEntries(entries: readonly TimelineDTO[]): EntryGroup[] {
  const newestFirst = [...entries].sort((a, b) => timeOf(b) - timeOf(a))
  const groups = new Map<string, EntryGroup>()
  for (const e of newestFirst) {
    const label = (e.title || e.tool_ref || '').trim()
    const resource = e.resource_ref?.trim() ?? ''
    const key = JSON.stringify([e.kind, label, resource])
    const group = groups.get(key)
    if (group) group.entries.push(e)
    else
      groups.set(key, {
        key,
        kind: e.kind,
        label,
        resource,
        count: 0,
        newest: e.at,
        oldest: e.at,
        ranged: false,
        entries: [e],
      })
  }
  return [...groups.values()].map((g) => {
    const dated = g.entries.filter((e) => Number.isFinite(timeOf(e)))
    const oldest = dated.length > 0 ? dated[dated.length - 1] : g.entries[0]
    return {
      ...g,
      count: g.entries.length,
      oldest: oldest.at,
      ranged:
        dated.length > 1 && timeOf(g.entries[0]) - timeOf(oldest) >= RANGE_MS,
    }
  })
}

/** A name ends in `ref` as a whole word or path segment, not as the tail of another name. */
const endsWithName = (words: string, ref: string): boolean =>
  words === ref || words.endsWith(` ${ref}`) || words.endsWith(`/${ref}`)

/**
 * What a row says, once. The resource goes under the words only when it adds something:
 * "Task worker-indexer" over "worker-indexer" told it twice, and an MCP call named
 * "create_issue" over "github/create_issue" is the same call, so the qualified name
 * becomes the words. Only an MCP call is qualified that way: for a plain tool the
 * resource is a path, and "Read" over "src/Read" are two different things.
 */
export function rowWords(
  kind: string,
  label: string,
  resource: string | undefined,
): { title: string; subtitle: string } {
  const ref = resource?.trim() ?? ''
  const title = kind === 'mcp' && ref.endsWith(`/${label}`) ? ref : label
  return {
    title,
    subtitle: ref && !endsWithName(title, ref) ? ref : '',
  }
}
