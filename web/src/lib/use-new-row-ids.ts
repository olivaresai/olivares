// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useEffect, useState } from 'react'

const NONE: ReadonlySet<string> = new Set()

/**
 * The ids that just arrived at the head of a newest-first feed, for a short while.
 * Rows marked "new" get the C2-DESIGN §3 slide-in (`.feed-enter`, 6 px) exactly
 * once, then rest. An arrival is a row above the first row the feed already showed:
 * rows below it (Load more, a re-order) are not arrivals, and a list that shares no
 * row with the last one replaced it. `scope` names the view (filters, folds, ledger):
 * when it changes the feed starts over, so rows a filter reveals never slide. The
 * marks clear after a second (the slide runs 120 ms), so a virtualized row that
 * re-mounts does not replay, and `?still` (captures) marks nothing.
 */
export function useNewRowIds(
  ids: readonly string[],
  scope = '',
): ReadonlySet<string> {
  const [fresh, setFresh] = useState<ReadonlySet<string>>(NONE)
  const [known, setKnown] = useState<ReadonlySet<string> | null>(null)
  const [prevKey, setPrevKey] = useState<string | null>(null)
  const [prevScope, setPrevScope] = useState(scope)

  // Folded in during render (React's "storing information from previous renders"
  // pattern): when the list changed since the last render, the known set is
  // updated before this render commits, so the marks are the ones this render
  // paints. JSON serializes id boundaries: ids joined with a separator could
  // collide (["a\0b"] vs ["a", "b"]), and an empty feed is its own key, distinct
  // from any list.
  const key = ids.length === 0 ? '' : JSON.stringify(ids)
  if (scope !== prevScope) {
    // A new view seeds from what it shows now; a view still loading (empty)
    // seeds from its first page.
    setPrevScope(scope)
    setPrevKey(key)
    setKnown(ids.length === 0 ? null : new Set(ids))
    if (fresh !== NONE) setFresh(NONE)
  } else if (key !== prevKey) {
    setPrevKey(key)
    if (ids.length === 0) {
      // A refresh in flight empties the list for a render; the known set
      // survives, so the rows that come back do not replay.
      if (fresh !== NONE) setFresh(NONE)
    } else if (known === null) {
      // The first non-empty list seeds the known set: the first page of a load
      // is not a batch of new arrivals.
      setKnown(new Set(ids))
    } else {
      const head = ids.findIndex((id) => known.has(id))
      setKnown(new Set(ids))
      if (head > 0) setFresh(new Set(ids.slice(0, head)))
    }
  }

  // The countdown is keyed on the marks themselves, so unrelated re-renders
  // (a poll with no new rows, a state change elsewhere) never cancel it.
  useEffect(() => {
    if (fresh === NONE) return
    const timer = window.setTimeout(() => setFresh(NONE), 1000)
    return () => window.clearTimeout(timer)
  }, [fresh])

  return new URLSearchParams(window.location.search).has('still') ? NONE : fresh
}
