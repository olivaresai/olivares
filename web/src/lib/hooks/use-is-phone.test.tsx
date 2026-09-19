// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ONE PHONE BREAKPOINT, MEASURED AT THREE WIDTHS — and the middle one is the case.
//
// The console carried two: `(max-width: 639px)` in this hook, and `max-[390px]:hidden`
// written byte for byte in two tables. 390 and 1440 are the widths captures are taken
// at, so the 249 px band between the two numbers was never photographed and never
// tested: at 500 px the page header collapsed its verbs behind the disclosure while both
// tables still painted Driver, Environment, Launch and Created.
//
// So the assertion is not "the constants are equal" — that is the fix, not the defect.
// It is that for a given width the JavaScript answer and the CSS answer AGREE, checked
// at a width on each side of the breakpoint and at one inside the old band.
import { renderHook } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { stubViewportWidth } from '@/test/viewport'
import { HIDDEN_ON_PHONE, PHONE_MAX_PX, useIsPhone } from './use-is-phone'

afterEach(() => stubViewportWidth(1440))

/** The width the CSS class collapses at, read out of the class itself. */
function classBreakpoint(cls: string): number {
  const found = /max-\[(\d+)px\]:hidden/.exec(cls)
  if (!found) throw new Error(`not a phone-hiding class: ${cls}`)
  return Number(found[1])
}

describe('useIsPhone — one breakpoint, in JavaScript and in CSS', () => {
  it.each([
    [390, true],
    [500, true],
    [800, false],
  ])('at %ipx the shell collapses: %s', (width, collapsed) => {
    stubViewportWidth(width)
    const { result } = renderHook(() => useIsPhone())
    expect(result.current).toBe(collapsed)
  })

  it.each([390, 500, 800])(
    'at %ipx the tables hide exactly when the header collapses',
    (width) => {
      stubViewportWidth(width)
      const { result } = renderHook(() => useIsPhone())
      const hiddenByCss = width <= classBreakpoint(HIDDEN_ON_PHONE)
      // THE CASE, at 500: this used to be `true` for the header and `false` for the
      // columns, and nothing in the tree could say so.
      expect(hiddenByCss).toBe(result.current)
    },
  )

  it('the class names the same pixel the media query does', () => {
    // Tailwind only finds classes it can read as literal text, so the class cannot be
    // interpolated from the constant — a template would be tidy and would generate no
    // rule at all. This is what keeps the literal and the number from drifting.
    expect(classBreakpoint(HIDDEN_ON_PHONE)).toBe(PHONE_MAX_PX)
  })

  it('answers the desktop layout when the engine has no matchMedia', () => {
    const win = window as unknown as Record<string, unknown>
    const saved = win.matchMedia
    delete win.matchMedia
    try {
      const { result } = renderHook(() => useIsPhone())
      // Never a collapsed surface a reader cannot open.
      expect(result.current).toBe(false)
    } finally {
      win.matchMedia = saved
    }
  })
})
