// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { CSSProperties, RefObject } from 'react'
import { ResizeHandle } from '@/components/ui/resize-handle'
import { usePaneWidth, usePaneWidthsStore } from '@/stores/pane-widths'
import type { ResizablePane } from './pane-vars'

/** The handle on one pane's edge: it reads and writes that pane's stored width. */
export function PaneResizeHandle({
  pane,
  container,
  within = container,
  target,
  grow,
  label,
  className,
  style,
}: {
  pane: ResizablePane
  /** The element that carries the custom property (a drag sets it live). */
  container: RefObject<HTMLElement | null>
  /** The element whose width sets the limits; the container unless the pane sits in a wider one. */
  within?: RefObject<HTMLElement | null>
  target: RefObject<HTMLElement | null>
  grow: 1 | -1
  label: string
  className?: string
  style?: CSSProperties
}) {
  const stored = usePaneWidth(pane.id)
  return (
    <ResizeHandle
      label={label}
      target={target}
      grow={grow}
      bounds={() => pane.bounds(within.current?.clientWidth ?? 0)}
      onPreview={(px) => {
        const grid = container.current
        const shown = px ?? stored
        if (!grid) return
        if (shown === undefined) grid.style.removeProperty(pane.cssVar)
        else grid.style.setProperty(pane.cssVar, `${shown}px`)
      }}
      onCommit={(px) => usePaneWidthsStore.getState().commit(pane.id, px)}
      onReset={() => usePaneWidthsStore.getState().reset(pane.id)}
      className={className}
      style={style}
    />
  )
}
