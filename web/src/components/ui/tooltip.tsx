// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import * as TooltipPrimitive from '@radix-ui/react-tooltip'
import type { ComponentProps } from 'react'
import { cn } from '@/lib/utils'

/**
 * Tooltip — the tiny on-hover/focus label over Radix Tooltip. Intentionally small:
 * `text-xs`, flat `bg-elevated` chip with a strong hairline and `shadow-md`. A
 * 400ms open delay keeps the UI calm; `skipDelayDuration={0}` so moving between
 * adjacent triggers still re-arms the delay. Radix handles pointer/keyboard focus
 * triggers and ARIA. Wrap the app (or a region) once in `TooltipProvider`.
 */

export function TooltipProvider({
  delayDuration = 400,
  skipDelayDuration = 0,
  ...props
}: ComponentProps<typeof TooltipPrimitive.Provider>) {
  return (
    <TooltipPrimitive.Provider
      delayDuration={delayDuration}
      skipDelayDuration={skipDelayDuration}
      {...props}
    />
  )
}

export const Tooltip = TooltipPrimitive.Root
export const TooltipTrigger = TooltipPrimitive.Trigger

export function TooltipContent({
  className,
  sideOffset = 6,
  ...props
}: ComponentProps<typeof TooltipPrimitive.Content>) {
  return (
    <TooltipPrimitive.Portal>
      <TooltipPrimitive.Content
        sideOffset={sideOffset}
        className={cn(
          'z-50 max-w-xs bg-elevated border border-border-strong rounded-md px-2 py-1',
          'text-xs text-foreground shadow-md',
          'transition-opacity duration-150 ease-out',
          'data-[state=delayed-open]:opacity-100 data-[state=instant-open]:opacity-100 data-[state=closed]:opacity-0',
          className,
        )}
        {...props}
      />
    </TooltipPrimitive.Portal>
  )
}
