// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// KEYBOARD FOCUS PAINTS THE RING A COMPONENT DRAWS.
//
// A component hides the browser's outline with `outline-none` and draws its own with
// `focus-visible:outline-2`. In Tailwind 4, `outline-none` also sets `--tw-outline-style: none`
// on the element, and `outline-<n>` paints `outline-style: var(--tw-outline-style)`: on one
// element the ring resolved to `none`, so keyboard focus showed nothing (WCAG 2.2 SC 2.4.7).
// index.css sets the variable back to `solid` under `:focus-visible` OUTSIDE every layer: an
// unlayered declaration beats every layered one, whatever the specificity or the order. Inside
// `@layer base` it would lose to the utility, so this test holds the rule at the top level.
// The unit environment does not run Tailwind (vite.config.ts `css: false`); the browser check
// is e2e-visual/focus-ring.spec.ts.
/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

// vitest runs with cwd = web/; comments first, so prose cannot hold a brace.
const COMMENT = /\/\*[\s\S]*?\*\//g
const css = readFileSync('src/index.css', 'utf8').replace(COMMENT, '')

/** The `prelude { body }` blocks at brace depth 0, in source order. */
function topLevelRules(source: string): Array<[string, string]> {
  const rules: Array<[string, string]> = []
  let depth = 0
  let start = 0
  let open = 0
  for (let i = 0; i < source.length; i++) {
    const c = source[i]
    if (c === '{') {
      if (depth === 0) open = i
      depth++
    } else if (c === '}') {
      depth--
      if (depth === 0) {
        const prelude = source.slice(start, open).trim()
        rules.push([prelude, source.slice(open + 1, i)])
        start = i + 1
      }
    } else if (c === ';' && depth === 0) {
      start = i + 1
    }
  }
  return rules
}

describe('the focus ring', () => {
  it('sets --tw-outline-style back to solid under :focus-visible, unlayered', () => {
    // Control: a rule inside a layer is not top level.
    const layered = topLevelRules('@layer base { :focus-visible { a: b; } }')
    expect(layered.map(([prelude]) => prelude)).toEqual(['@layer base'])

    const bodies = topLevelRules(css)
      .filter(([prelude]) => prelude === ':focus-visible')
      .map(([, body]) => body)
    expect(bodies).toHaveLength(1)
    expect(bodies[0]).toMatch(/--tw-outline-style:\s*solid\s*;/)
  })
})
