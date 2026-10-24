// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { CSSProperties } from 'react'
import type { ResizeBounds } from '@/components/ui/resize-handle'
import { usePaneWidthsStore } from '@/stores/pane-widths'

/**
 * A pane a person can resize. `cssVar` is the custom property the pane's grid reads (the
 * stylesheet default applies until a width is stored); `bounds` takes the width of the
 * space that holds the pane and returns what the pane may take, so the work always keeps
 * room.
 */
export interface ResizablePane {
  id: string
  cssVar: string
  bounds: (container: number) => ResizeBounds
}

/** The widest the engine stores for one pane. */
export const MAX_PANE_PX = 4000

/**
 * Between `min` and `max` px, and never more than `share` of the space that holds the
 * pane: where that space is too narrow for `min`, the share wins, as it does in the CSS.
 */
export const upTo = (
  min: number,
  max: number,
  share: number,
  container: number,
): ResizeBounds => {
  const hi = Math.min(
    max,
    container > 0 ? Math.floor(container * share) : max,
    MAX_PANE_PX,
  )
  return { min: Math.min(min, hi), max: hi }
}

/** The stored widths as the custom properties of the element that holds `panes`. */
export function usePaneVars(...panes: ResizablePane[]): CSSProperties {
  const widths = usePaneWidthsStore((s) => s.widths)
  const vars: Record<string, string> = {}
  for (const pane of panes) {
    const px = widths[pane.id]
    // The engine keeps any 1..4000 px (an API write, another client's limit); the pane
    // never shows narrower than its own minimum, so its content cannot clip.
    if (px !== undefined)
      vars[pane.cssVar] = `${Math.max(px, pane.bounds(0).min)}px`
  }
  return vars as CSSProperties
}
