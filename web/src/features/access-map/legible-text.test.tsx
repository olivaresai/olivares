// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE TEXT ON THE ACCESS MAP IS AT LEAST 12 PX AT EVERY ZOOM THE PAGE CHOOSES.
//
// Rendered size = font size x zoom. The page chooses the zoom through `fitView`, whose
// floor is LEGIBLE_FIT_MIN_ZOOM, so the smallest text on the canvas times that floor is
// the smallest text the page can ever draw by itself. jsdom has no layout, so this reads
// the one fact it can: the declared pixel sizes in the four files that paint the map
// (the nodes, the edge labels, the legend and the detail panel), and the floor.
import { describe, expect, it } from 'vitest'
import { LEGIBLE_FIT_MIN_ZOOM } from '@/features/shared/graph/graph-canvas'
import detailSource from './detail.tsx?raw'
import edgesSource from './edges.tsx?raw'
import legendSource from './legend.tsx?raw'
import nodesSource from './nodes.tsx?raw'

const MIN_RENDERED_PX = 12

/** Every arbitrary pixel font size (`text-[10px]`) a source file declares. */
function declaredPx(source: string): number[] {
  return [...source.matchAll(/text-\[(\d+(?:\.\d+)?)px\]/g)].map((m) =>
    Number(m[1]),
  )
}

describe('access map text size', () => {
  it.each([
    ['nodes.tsx', nodesSource],
    ['edges.tsx', edgesSource],
    ['legend.tsx', legendSource],
    ['detail.tsx', detailSource],
  ])('%s declares no pixel font under 12', (_name, source) => {
    for (const px of declaredPx(source)) {
      expect(px * LEGIBLE_FIT_MIN_ZOOM).toBeGreaterThanOrEqual(MIN_RENDERED_PX)
    }
  })

  it('the fit floor never draws a 12 px caption smaller than 12 px', () => {
    expect(12 * LEGIBLE_FIT_MIN_ZOOM).toBeGreaterThanOrEqual(MIN_RENDERED_PX)
  })
})
