// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import * as PopoverPrimitive from '@radix-ui/react-popover'
import type { ComponentProps } from 'react'
import { cn } from '@/lib/utils'

/**
 * Popover — the anchored floating panel over Radix Popover, for richer transient
 * content than a tooltip (forms, filters, detail). As direction A2 draws an overlay: the
 * highest surface (`bg-raised`), the pop shadow with a hairline inside it and no outline,
 * `rounded-card`; default `w-72`. Subtle fade keyed off data-state. Radix supplies positioning, focus management, outside-click and ARIA.
 */

export const Popover = PopoverPrimitive.Root
export const PopoverTrigger = PopoverPrimitive.Trigger
export const PopoverAnchor = PopoverPrimitive.Anchor
export const PopoverClose = PopoverPrimitive.Close

export function PopoverContent({
  className,
  align = 'center',
  sideOffset = 6,
  ...props
}: ComponentProps<typeof PopoverPrimitive.Content>) {
  return (
    <PopoverPrimitive.Portal>
      <PopoverPrimitive.Content
        align={align}
        sideOffset={sideOffset}
        className={cn(
          'z-50 w-72 rounded-card border-0 bg-raised p-3 shadow-[var(--shadow-pop),inset_0_0_0_1px_var(--line)]',
          'text-body text-foreground outline-none',
          'transition-opacity duration-[var(--duration-panel)] ease-out',
          'data-[state=open]:opacity-100 data-[state=closed]:opacity-0',
          className,
        )}
        {...props}
      />
    </PopoverPrimitive.Portal>
  )
}
