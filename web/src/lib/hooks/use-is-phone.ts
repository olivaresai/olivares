// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ONE PLACE THAT ASKS WHETHER THIS IS A PHONE, because two would answer differently.
//
// ⛔ IT WAS WRITTEN TWICE, BYTE FOR BYTE — in `page-header.tsx` and in the work
//    composer — and that is the defect this module exists to close, not a tidiness
//    preference. The duplicated thing is not only the hook: it is the BREAKPOINT. Two
//    `(max-width: 639px)` literals are two chances to move one of them, and a header
//    that collapses at a width where the composer still lays out three controls is a
//    layout nobody designed. The same reasoning put `useTenantLabel` and the scope
//    derivation each in one place.
import { useEffect, useState } from 'react'

/** Below this width the shell collapses its controls. Tailwind's `sm` breakpoint. */
export const PHONE_MAX_PX = 639
const PHONE_QUERY = `(max-width: ${PHONE_MAX_PX}px)`

/**
 * THE SAME BREAKPOINT, FOR THE ELEMENTS THAT COLLAPSE IN CSS INSTEAD OF IN JAVASCRIPT.
 *
 * ⛔ THE SECOND BREAKPOINT WAS WRITTEN TWICE, BYTE FOR BYTE, AFTER THIS MODULE EXISTED
 *    TO STOP EXACTLY THAT: two later rounds each added `const hideAt390 =
 *    'max-[390px]:hidden'` — one in the provider profiles table, one in the agents
 *    table — while this file's own comment said two literals are two chances to move
 *    one of them. The band between the two numbers is the defect, not the duplication:
 *    at 500 px the page header was in phone mode, with its verbs behind the disclosure,
 *    while both tables still painted Driver, Environment, Launch and Created. That is a
 *    layout nobody designed and no capture would have caught, because a capture is
 *    taken at 390 or at 1440 and never between.
 *
 * ⛔ IT IS A LITERAL AND NOT A TEMPLATE, and that is forced by the toolchain rather than
 *    chosen: Tailwind finds classes by scanning source text, so a class interpolated
 *    from the constant above would generate no rule at all and the column would simply
 *    never hide — it would look tidy and do nothing. The literal
 *    is tied to the number by a test that reddens the moment they disagree.
 */
export const HIDDEN_ON_PHONE = 'max-[639px]:hidden'

/**
 * Is this a phone-width viewport?
 *
 * ⛔ THE BREAKPOINT IS ASKED IN JAVASCRIPT AND NOT ONLY IN CSS, and the first reason is
 *    the accessibility tree rather than the layout. A `sm:hidden` button is
 *    `display:none` on a desktop, so it is not painted and not focusable — but it is
 *    still IN the document, and a control that exists on every one of 77 routes without
 *    ever being reachable is a control an audit has to explain away. Asking the query
 *    means the disclosure exists exactly where it is usable.
 *
 * ⛔ AND THE SECOND REASON IS HEIGHT, which is what the composer needs it for. Letting
 *    CSS decide with `flex-wrap` lets the BROWSER choose how many rows to use, and at
 *    390 px it chose three — 102 px in a box budgeted for 64. Asking the width means the
 *    component chooses WHAT to paint on the one row it has, instead of letting the row
 *    multiply.
 *
 * `matchMedia` missing (jsdom without the stub, a very old engine) answers `false`,
 * which is the desktop layout: the surface a reader gets is the one with every control
 * in the row, never a collapsed one they cannot open.
 */
export function useIsPhone(): boolean {
  const [phone, setPhone] = useState(
    () =>
      typeof window !== 'undefined' &&
      typeof window.matchMedia === 'function' &&
      window.matchMedia(PHONE_QUERY).matches,
  )
  useEffect(() => {
    if (
      typeof window === 'undefined' ||
      typeof window.matchMedia !== 'function'
    )
      return
    const query = window.matchMedia(PHONE_QUERY)
    const sync = () => setPhone(query.matches)
    sync()
    query.addEventListener('change', sync)
    return () => query.removeEventListener('change', sync)
  }, [])
  return phone
}
