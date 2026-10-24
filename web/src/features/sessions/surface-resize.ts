// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import {
  MAX_PANE_PX,
  upTo,
  type ResizablePane,
} from '@/components/ui/pane-vars'

/**
 * The panes of the sessions work surface a person can resize, so the thread always keeps
 * room: the list and the context pane each stop at 35% of the surface, the side-by-side
 * threads split it between 30% and 70%.
 */
export const LIST_PANE: ResizablePane = {
  id: 'sessions-list',
  cssVar: '--list-w',
  bounds: (w) => upTo(240, 560, 0.35, w),
}
export const CONTEXT_PANE: ResizablePane = {
  id: 'sessions-context',
  cssVar: '--ctx-w',
  bounds: (w) => upTo(280, 640, 0.35, w),
}
export const BESIDE_PANE: ResizablePane = {
  id: 'sessions-beside',
  cssVar: '--beside-w',
  bounds: (w) =>
    w > 0
      ? {
          min: Math.min(Math.floor(w * 0.3), MAX_PANE_PX),
          max: Math.min(Math.floor(w * 0.7), MAX_PANE_PX),
        }
      : { min: 240, max: MAX_PANE_PX },
}
