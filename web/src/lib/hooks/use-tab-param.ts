// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// useTabParam — "this value lives in ?tab=". One hook for every view whose tab strip must
// survive a reload, a pasted link and Back/Forward, built on useUrlState so it shares its
// rules: the URL is the state, a click REPLACES the history entry (Back leaves the view, it
// does not walk the tabs), params the view does not own survive, and the default tab keeps
// the URL clean.
//
// An unknown or not-offered value shows `fallback` (the first tab unless the caller says
// otherwise) and is LEFT in the address. The offered set often depends on who the principal
// is (`can()`), and it can be smaller for one render while the session settles: rewriting
// the address then would destroy a valid link for good. Derived from the address on every
// render, the tab opens by itself as soon as it is offered, and the next click replaces it.
import { useCallback } from 'react'
import { useUrlState } from './use-url-state'

/** Where a view keeps its tab, and which tab it opens on. */
export interface TabParamOptions<T extends string> {
  /** The search param that owns the tab. Default `tab`; a second strip on a page needs its own. */
  key?: string
  /** The tab shown when the URL names none (or one that is not offered). Default: the first. */
  fallback?: T
}

/**
 * `values` is the set of tabs on offer now (it may differ per principal). Returns the active
 * tab and a setter. The setter writes the URL; the returned tab follows the URL.
 */
export function useTabParam<T extends string>(
  values: readonly T[],
  options?: TabParamOptions<T>,
): [T, (next: string) => void] {
  const key = options?.key ?? 'tab'
  const fallback = options?.fallback ?? values[0]
  const [url, patch] = useUrlState([key], { resetScroll: false })
  const want = url[key]
  const tab =
    want !== undefined && (values as readonly string[]).includes(want)
      ? (want as T)
      : fallback
  const setTab = useCallback(
    (next: string) => patch({ [key]: next === fallback ? undefined : next }),
    [patch, key, fallback],
  )
  return [tab, setTab]
}
