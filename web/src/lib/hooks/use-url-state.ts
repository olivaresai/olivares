// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// useUrlState — the canonical deep-link/URL-state hook. A view declares
// the search-param keys it OWNS; the hook seeds state from the URL and reflects
// every change back with REPLACE semantics (filters never spam the history
// stack — Back leaves the view, it does not undo a filter click).
//
// Contract:
//   - Owned keys only: updates merge into the existing search, so params owned
//     by other components on the route (e.g. a shell's ?tab=) are preserved.
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
): [UrlState, (patch: UrlState) => void] {
  const navigate = useNavigate()
  // The owned-keys list is a stable contract per call site; keep the first one.
  const keysRef = useRef(keys)
  const [state, setState] = useState<UrlState>(() =>
    readSearch(keysRef.current),
  )

  // Follow the location. Subscribing to the router (rather than to popstate)
  // covers programmatic navigation too, which is how a saved view is applied
  // and how one feature deep-links into another.
  const searchStr = useRouterState({
    select: (s: { location: { searchStr: string } }) => s.location.searchStr,
  })
  useEffect(() => {
    // `searchStr` is the notification's payload. Reading window.location here
    // can observe the previous URL for one tick and revert the optimistic state
    // after a filter already fired its new request.
    const next = readSearch(keysRef.current, searchStr)
    // Guard the identity: a replace we just performed re-runs this effect, and
    // handing back a fresh object every time would re-render every consumer on
    // every navigation in the app.
    setState((prev) => (sameState(prev, next, keysRef.current) ? prev : next))
  }, [searchStr])

  const stateRef = useRef(state)
  stateRef.current = state

  const patch = useCallback(
    (p: UrlState) => {
      // Compute against the LATEST state through a ref rather than inside a
      // setState updater: navigate() is a side effect, and a side effect in a
      // React reducer runs twice under StrictMode.
      const prev = stateRef.current
      const next: UrlState = { ...prev }
      const searchPatch: Record<string, unknown> = {}
      for (const k of keysRef.current) {
        if (!(k in p)) continue
        const v = p[k]
        if (v === undefined || v === '') delete next[k]
        else next[k] = v
        searchPatch[k] = next[k]
      }
      stateRef.current = next
      setState(next)
      // Reflect into the URL: merge over the existing search so non-owned
      // params survive; explicit undefined deletes (TanStack drops them).
      void navigate({
        search: (cur: Record<string, unknown>) => ({ ...cur, ...searchPatch }),
        replace: true,
      } as never)
    },
    [navigate],
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
): [T, (patch: UrlState) => void, string[]] {
  const [raw, patch] = useUrlState(keys)
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
  const [reported, setReported] = useState<string[]>([])
  useEffect(() => {
    if (decoded.issues.length === 0) return
    // A NEW array instance per event, CLONED here rather than passed through.
    // The identity is what lets a consumer tell "the same latched complaint"
    // from "it happened again" — keying on the joined names cannot, because two
    // bad links naming the same key are two events and the second has to speak.
    // Storing the decoder's own array would make that contract depend on the
    // decoder allocating a fresh one, which nothing requires it to do.
    setReported([...decoded.issues])
    const clear: UrlState = {}
    for (const k of decoded.issues) clear[k] = undefined
    patch(clear)
    // `decoded` is memoised on [raw, decode], so this re-enters exactly when the
    // URL state actually changed — never once per render.
  }, [decoded, patch])

  const patchAndClearReport = useCallback(
    (p: UrlState) => {
      setReported([])
      patch(p)
    },
    [patch],
  )

  // Always the latched array, never the freshly decoded one: the decoded array
  // is rebuilt on every render, and a consumer that re-armed on identity would
  // then never let itself be dismissed.
  return [decoded.value, patchAndClearReport, reported]
}
