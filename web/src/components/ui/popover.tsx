// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import * as PopoverPrimitive from '@radix-ui/react-popover'
import type { ComponentProps } from 'react'
import { cn } from '@/lib/utils'

/**
 * Popover — the anchored floating panel over Radix Popover, for richer transient
 * content than a tooltip (forms, filters, detail). Flat `bg-elevated` card with a
 * strong hairline, `rounded-lg`, `shadow-lg`; default `w-72`. Subtle fade keyed off
 * data-state. Radix supplies positioning, focus management, outside-click and ARIA.
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
          'z-50 w-72 bg-elevated border border-border-strong rounded-lg shadow-lg p-3',
          'text-sm text-foreground outline-none',
          'transition-opacity duration-150 ease-out',
          'data-[state=open]:opacity-100 data-[state=closed]:opacity-0',
          className,
        )}
        {...props}
      />
    </PopoverPrimitive.Portal>
  )
}
