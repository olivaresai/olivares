// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// regression net for the SC 1.4.1 / 1.4.11 selection fix.
//
// WHY A SOURCE-LEVEL GUARD. `task at:gate` measures design TOKENS on a probe div; it
// never looks at which utility a component uses. So reverting these five sites to
// `ring-accent-line` (1.34:1 dark / 1.60:1 light — colour only, below the 3:1 floor)
// left the gate printing "0 blocking": --accent-strong still existed and passed, and
// --accent-line is a legitimately waived hairline. The token measurement cannot see
// the regression; only the usage can be pinned. The session-viewer rows also have a
// rendered test (session-viewer.test.tsx), but topo-list and the React-Flow node are
// expensive to mount, and this invariant is what actually has to hold.
/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

/** The five places where a selected/active state is conveyed, and how. */
const SELECTION_SITES = [
  {
    file: 'src/features/session-viewer/unified-timeline.tsx',
    aria: 'aria-pressed={selected}',
    occurrences: 2, // ActivityRow + EvidenceRow
  },
  {
    file: 'src/features/session-viewer/tools-panel.tsx',
    aria: 'aria-pressed={isActive}',
    occurrences: 1,
  },
  {
    file: 'src/features/automations/workflows/topo-list.tsx',
    aria: "aria-current={isSelected ? 'true' : undefined}",
    occurrences: 1,
  },
  {
    file: 'src/features/automations/workflows/editor.tsx',
    aria: "aria-current={selected ? 'true' : undefined}",
    occurrences: 1,
  },
]

describe('the selected state never rests on colour alone', () => {
  for (const site of SELECTION_SITES) {
    const src = readFileSync(site.file, 'utf8')

    it(`${site.file} states selection in the accessibility tree`, () => {
      expect(src.split(site.aria).length - 1).toBe(site.occurrences)
    })

    it(`${site.file} carries a non-colour signal per selection site`, () => {
      // The rail: filled when selected, transparent otherwise, same width in both —
      // presence/absence is the signal, so it survives greyscale and colour blindness.
      expect(src.split("'bg-accent-strong' : 'bg-transparent'").length - 1).toBe(
        site.occurrences,
      )
      expect(src).toContain('w-1 shrink-0 rounded-full')
    })

    it(`${site.file} does not identify state with the waived hairline`, () => {
      // --accent-line is waived by the AT gate as a RESTING hairline. It must never
      // come back as a ring or as the sole state colour here, or the waiver's written
      // reason (web/e2e-visual/at-run.ts, ADVISORY) becomes false.
      expect(src).not.toContain('ring-accent-line')
      expect(src).not.toContain('border-accent-line')
    })
  }

  it('the editor keeps the selection signals independent of validity', () => {
    // A node that is selected AND invalid loses its accent border to border-danger.
    // If the rail and the ARIA were also gated on !invalid, that node would carry NO
    // selection signal at all — not colour, not shape, not the a11y tree — precisely
    // when the user has clicked it to fix it.
    const src = readFileSync(
      'src/features/automations/workflows/editor.tsx',
      'utf8',
    )
    expect(src).not.toContain('selected && !invalid')
  })
})
