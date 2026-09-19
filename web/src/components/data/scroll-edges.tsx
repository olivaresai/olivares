// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useRef, type ReactNode } from 'react'
import { cn } from '@/lib/utils'
import { useScrollEdges, type ScrollEdges } from '@/lib/hooks/use-scroll-edges'

/**
 * The visual half of that fix. The mechanism, the measurement and the reasons
 * live
 * with the hook, in `@/lib/hooks/use-scroll-edges`.
 */
/** The two fades. Rendered inside a `relative` box that contains the scroller. */
export function ScrollEdgeHints({ edges }: { edges: ScrollEdges }) {
  return (
    <>
      {edges.left && (
        <div
          data-slot="data-table-scroll-hint"
          data-edge="left"
          aria-hidden
          className="pointer-events-none absolute inset-y-0 left-0 z-20 w-6 bg-gradient-to-r from-surface to-transparent"
        />
      )}
      {edges.right && (
        <div
          data-slot="data-table-scroll-hint"
          data-edge="right"
          aria-hidden
          className="pointer-events-none absolute inset-y-0 right-0 z-20 w-6 bg-gradient-to-l from-surface to-transparent"
        />
      )}
    </>
  )
}

/**
 * The whole thing for a table that owns no scroller of its own: `StaticTable` inside a
 * `<div className="overflow-x-auto">` is the shape this replaces, and there are 30 of
 * those in `web/src/features`. This lane converts the ones on the screens it was asked
 * for; the rest keep working exactly as they did, with no hint.
 */
export function XScroll({
  children,
  className,
  contentKey,
}: {
  children: ReactNode
  className?: string
  /** Anything whose change resizes the content — a row count, a tab id. */
  contentKey?: unknown
}) {
  const ref = useRef<HTMLDivElement>(null)
  const edges = useScrollEdges(ref, [contentKey])
  return (
    <div className="relative">
      <ScrollEdgeHints edges={edges} />
      <div ref={ref} className={cn('overflow-x-auto', className)}>
        {children}
      </div>
    </div>
  )
}
