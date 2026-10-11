// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE LIST'S FILTER FIELD, as a pure function. It matches what a row SHOWS and nothing
// else: the name the row is called by, its distinguishing tail, the tool's product name and
// the folder's name. A reference or a path the row does not paint is not searched, so the
// field never keeps a row for a reason the person cannot see.
import { toolName } from '@/features/agentops/tool-names'
import { runFolder } from './folder'
import {
  primaryRun,
  sessionNaming,
  sharedNames,
  type UnifiedSession,
} from './provenance'

/** Below this many sessions the list is short enough to read, and has no filter field. */
export const FILTER_FROM = 6

function haystack(
  session: UnifiedSession,
  untitled: string,
  shared: ReadonlySet<string>,
): string {
  const run = primaryRun(session.runs)
  const naming = sessionNaming(session, untitled, shared)
  const driver = run?.provider_driver || session.live?.provider
  return [
    naming.name,
    naming.shortId,
    driver ? toolName(driver) : '',
    runFolder(run),
  ]
    .filter(Boolean)
    .join(' ')
    .toLowerCase()
}

/** The sessions whose row shows every word of `query` (case-insensitive); all of them for an empty one. */
export function filterSessions(
  sessions: readonly UnifiedSession[],
  query: string,
  untitled: string,
): UnifiedSession[] {
  const words = query.toLowerCase().split(/\s+/).filter(Boolean)
  if (words.length === 0) return [...sessions]
  const shared = sharedNames(sessions, untitled)
  return sessions.filter((s) => {
    const text = haystack(s, untitled, shared)
    return words.every((w) => text.includes(w))
  })
}
