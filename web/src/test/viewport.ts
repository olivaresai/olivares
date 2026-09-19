// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A VIEWPORT WIDTH FOR jsdom, answered through `matchMedia` the way a browser answers it.
//
// ⛔ IT TAKES A WIDTH, NOT A BOOLEAN, and that is the whole point. The stubs it replaces
//    took `phone: boolean` and hard-coded the string `max-width: 639px`, so a test could
//    only ever ask the ONE question the stub already knew the answer to — and a second
//    breakpoint somewhere else in the tree was invisible to it. A console that collapses
//    its header at 639 and its tables at 390 has a band between them where the two
//    disagree, and no boolean stub can express a width inside that band.
//
// It parses the `(max-width: Npx)` / `(min-width: Npx)` forms the tree actually uses and
// answers each against ONE width, so every consumer of `matchMedia` in a render answers
// the same viewport — which is what a browser guarantees and what makes a disagreement
// between two of them a test failure rather than a stub setting.
import { vi } from 'vitest'

/** Answer every media query in the tree against this one viewport width. */
export function stubViewportWidth(width: number): void {
  Object.defineProperty(window, 'matchMedia', {
    writable: true,
    configurable: true,
    value: (query: string): MediaQueryList => {
      const max = /\(\s*max-width:\s*(\d+)px\s*\)/.exec(query)
      const min = /\(\s*min-width:\s*(\d+)px\s*\)/.exec(query)
      // A query this stub does not understand answers `false` — the desktop layout,
      // which is the same direction `useIsPhone` documents for a missing `matchMedia`:
      // a reader gets the surface with every control in the row, never a collapsed one
      // they cannot open.
      const matches =
        (max ? width <= Number(max[1]) : false) ||
        (min ? width >= Number(min[1]) : false)
      return {
        matches,
        media: query,
        onchange: null,
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
        addListener: vi.fn(),
        removeListener: vi.fn(),
        dispatchEvent: vi.fn(),
      } as unknown as MediaQueryList
    },
  })
}
