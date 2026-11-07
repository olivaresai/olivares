// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { HTMLAttributes, ReactNode } from 'react'
import { cn } from '@/lib/utils'

/**
 * Marks a surface that carries a trust decision: an approval, a consent or step-up
 * dialog, a provenance chip, an edition or license badge. Inside `[data-trust-chrome]`
 * tokens.css re-declares every colour per theme, so a brand accent or a pack variable
 * set on the document cannot restyle it. Dialogs carry the attribute on their
 * content through `DialogContent trust`.
 */
export function TrustChrome({
  className,
  children,
  ...props
}: HTMLAttributes<HTMLDivElement> & { children?: ReactNode }) {
  return (
    <div
      data-trust-chrome=""
      className={cn('text-foreground', className)}
      {...props}
    >
      {children}
    </div>
  )
}

/**
 * The container of one extension panel. Layout and paint containment make it the
 * containing block of anything the panel positions (a `fixed` overlay stays inside it),
 * a stacking context of its own, and a clip, so a panel cannot be drawn over a trust
 * surface beside it.
 *
 * ponytail: it clips what the panel draws outside its box (Safari ignores
 * overflow-clip-margin, so a focus ring on the very edge is cut there); upgrade when a
 * panel needs an in-flow popover, by rendering that popover in a portal.
 */
export function PanelSlot({
  className,
  children,
}: {
  className?: string
  children: ReactNode
}) {
  return (
    <div
      data-panel-slot=""
      className={cn(
        'relative isolate empty:hidden [contain:layout_paint] [overflow-clip-margin:4px]',
        className,
      )}
    >
      {children}
    </div>
  )
}
