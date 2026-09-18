// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// useUrlState — the canonical deep-link/URL-state hook. A view declares
// the search-param keys it OWNS; the hook seeds state from the URL and reflects
// every change back — by default with REPLACE semantics (filters never spam the
// history stack — Back leaves the view, it does not undo a filter click), and
// with PUSH where the call site declares its state a place rather than a filter
// (which session the work surface is showing, for example). See `UrlStateOptions`.
//
// Contract:
//   - Owned keys only: updates merge into the existing search, so params owned
//     by other components on the route (e.g. a shell's ?tab=) are preserved.
//     The hash is likewise not owned: navigate() is called with `hash: true` so
//     a filter or tab patch cannot wipe an unrelated fragment.
//   - Empty string and undefined both REMOVE a key — defaults live in code, not
//     in the URL, so a pristine view keeps a clean, shareable URL.
//   - Values are opaque strings; the view owns (de)serialization. Anything read
//     from the URL is untrusted input: views must validate before use, exactly
//     like model-ops validates ?tab= against its accessible set. For the common
//     case, useValidatedUrlState below folds that validation in AND reports
//     what it rejected, which the view is then expected to say out loud.
//   - The state FOLLOWS the location. It used to seed once from a lazy
//     initialiser and never look again, so browser Back/Forward — or a
//     navigate() from any other component — moved the URL while the view kept
//     the old state. They desynced silently, which is the one failure mode a
//     deep-link feature cannot afford.
//
// The feature route tree is generated from the registry outside the statically
// typed route tree, so navigate()'s search option needs the same `as never`
// cast every existing producer uses (see identity/nhi-roster.tsx).
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate, useRouterState } from '@tanstack/react-router'

/** The state shape: owned key → string value (absent = unset/default). */
export type UrlState = Record<string, string | undefined>

/** Whether a patch REPLACES the current history entry or PUSHES a new one. */
export type UrlHistoryMode = 'replace' | 'push'

/** What one patch may override for itself. */
export interface UrlPatchOptions {
  history?: UrlHistoryMode
}

/** Set (or clear) owned keys and reflect them into the URL. */
export type UrlStatePatch = (patch: UrlState, options?: UrlPatchOptions) => void

/** Optional extras forwarded on every reflect-into-URL navigation. */
export type UrlStateOptions = {
  /**
   * When set, passed through to `navigate()`. A tab (or filter) change is not a
   * page change: `/console` passes `false` so the router does not restore a
   * stale tab-strip `scrollLeft` after render (measured 2026-09-06). Omit to
   * keep the historical default (unset) for every other consumer.
   */
  resetScroll?: boolean
  /**
   * The default history mode for this call site. `replace` — the historical
   * behaviour and still the default — is right for a FILTER: Back should leave
   * the view, not undo a click on a facet.
   *
   * `push` exists for the one class this hook did not serve at first: state that
   * is a PLACE rather than a filter. Choosing another session on the work
   * surface moves the operator somewhere else, and an operator who presses Back
   * after it means "the session I was reading a moment ago", not "the screen I
   * was on before I opened this one". A surface can also override per call —
   * `patch(p, { history: 'replace' })` — because the same view usually holds
   * both kinds: which session is a place, which pane is a facet of it.
   */
  history?: UrlHistoryMode
}

/** Read the current values of `keys` from a canonical search string. On first
 * render the browser URL is the source; after that the router subscription is. */
function readSearch(keys: readonly string[], search?: string): UrlState {
  if (search === undefined && typeof window === 'undefined') return {}
  const params = new URLSearchParams(search ?? window.location.search)
  const out: UrlState = {}
  for (const k of keys) {
    const v = params.get(k)
    if (v !== null && v !== '') out[k] = v
  }
  return out
}

/** Same owned keys, same values — used to avoid a re-render per navigation. */
function sameState(a: UrlState, b: UrlState, keys: readonly string[]): boolean {
  return keys.every((k) => a[k] === b[k])
}

/**
 * Mirror a set of owned search params into component state.
 *
 * Returns the current state and a patch function: `patch({q: 'deny'})` sets a
 * key, `patch({q: undefined})` (or `''`) clears it, both reflected in the URL
 * via a replace navigation that merges with any non-owned params.
 */
export function useUrlState(
  keys: readonly string[],
  options?: UrlStateOptions,
): [UrlState, UrlStatePatch] {
  const navigate = useNavigate()
  // The owned-keys list is a stable contract per call site; freeze the first one
  // in state (not a ref) so later renders never read a ref during render.
  const [ownedKeys] = useState(keys)
  const resetScroll = options?.resetScroll
  const defaultHistory: UrlHistoryMode = options?.history ?? 'replace'
  const searchStr = useRouterState({
    select: (s: { location: { searchStr: string } }) => s.location.searchStr,
  })
  const fromUrl = useMemo(
    () => readSearch(ownedKeys, searchStr),
    [ownedKeys, searchStr],
  )
  // Optimistic overlay so a patch updates the view before the router reports
  // the new search string. When searchStr actually changes, drop the overlay
  // and trust the URL (Back/Forward, a foreign navigate, a saved view).
  const [optimistic, setOptimistic] = useState<UrlState | null>(null)
  const [seenSearch, setSeenSearch] = useState(searchStr)
  if (searchStr !== seenSearch) {
    setSeenSearch(searchStr)
    setOptimistic(null)
  }
  const state = optimistic ?? fromUrl
  const stateRef = useRef(state)
  useEffect(() => {
    stateRef.current = state
  })

  const patch = useCallback<UrlStatePatch>(
    (p, patchOptions) => {
      // navigate() is a side effect, so it stays in the event handler, never
      // inside a setState updater (StrictMode would fire it twice).
      const prev = stateRef.current
      const next: UrlState = { ...prev }
      const searchPatch: Record<string, unknown> = {}
      for (const k of ownedKeys) {
        if (!(k in p)) continue
        const v = p[k]
        if (v === undefined || v === '') delete next[k]
        else next[k] = v
        searchPatch[k] = next[k]
      }
      stateRef.current = next
      if (!sameState(prev, next, ownedKeys)) setOptimistic(next)
      // Reflect into the URL: merge over the existing search so non-owned
      // params survive; explicit undefined deletes (TanStack drops them).
      void navigate({
        search: (cur: Record<string, unknown>) => ({ ...cur, ...searchPatch }),
        replace: (patchOptions?.history ?? defaultHistory) === 'replace',
        // `true` keeps the current hash. Omitting hash would clear it, which
        // rewrites state the hook does not own.
        hash: true,
        ...(resetScroll !== undefined ? { resetScroll } : {}),
      } as never)
    },
    [navigate, ownedKeys, resetScroll, defaultHistory],
  )

  return [state, patch]
}

/**
 * The result of decoding one view's URL state.
 *
 *  - `value`  is the view's validated state, with every rejected key already
 *             fallen back to its in-code default.
 *  - `issues` names the keys that were rejected. It exists because falling back
 *             SILENTLY is what both original consumers did, and a deep-link
 *             that quietly ignores half of what it was handed shows the
 *             recipient different data than the author saw while looking
 *             identical. A view is expected to render this (UrlStateNotice).
 */
export interface UrlStateDecoded<T> {
  value: T
  issues: string[]
}

/**
 * useValidatedUrlState folds the untrusted-input handling into the hook: the
 * view supplies a pure decoder, and gets back its validated state, the patch
 * function, and the list of keys the decoder refused.
 *
 * The decoder must be pure and must NOT throw — a malformed URL is ordinary
 * input, not an exception.
 */
export function useValidatedUrlState<T>(
  keys: readonly string[],
  decode: (raw: UrlState) => UrlStateDecoded<T>,
  options?: UrlStateOptions,
): [T, UrlStatePatch, string[]] {
  const [raw, patch] = useUrlState(keys, options)
  const decoded = useMemo(() => decode(raw), [raw, decode])

  // A refused value is not in effect, so it must not stay in the address bar:
  // otherwise the operator copies a link that still carries it, the recipient
  // is told again that it could not be used, and the URL keeps disagreeing with
  // the screen for as long as anyone passes it on. Clearing it is a replace, so
  // it costs no history entry.
  //
  // The report is LATCHED across that cleanup: once the keys are gone the next
  // decode has nothing to complain about, and without the latch the notice
  // would flash and vanish before it could be read. It clears on the operator's
  // next deliberate change — or when they dismiss it.
  //
  // Latch the report during render (the array identity IS the event). The URL
  // cleanup is the only remaining effect: it navigates, it does not setState.
  const [reported, setReported] = useState<string[]>([])
  const [seenIssues, setSeenIssues] = useState<readonly string[] | null>(null)
  if (decoded.issues.length > 0 && seenIssues !== decoded.issues) {
    setSeenIssues(decoded.issues)
    setReported([...decoded.issues])
  }

  useEffect(() => {
    if (decoded.issues.length === 0) return
    const clear: UrlState = {}
    for (const k of decoded.issues) clear[k] = undefined
    // ALWAYS a replace, whatever this call site's default is: removing a value
    // that was never in effect is not a place the operator can go back to, and
    // pushing it would put a URL the screen never matched into their history.
    patch(clear, { history: 'replace' })
  }, [decoded, patch])

  const patchAndClearReport = useCallback<UrlStatePatch>(
    (p, patchOptions) => {
      setReported([])
      setSeenIssues(null)
      patch(p, patchOptions)
    },
    [patch],
  )

  // Always the latched array, never the freshly decoded one: the decoded array
  // is rebuilt on every render, and a consumer that re-armed on identity would
  // then never let itself be dismissed.
  return [decoded.value, patchAndClearReport, reported]
}
