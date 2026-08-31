// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import * as SwitchPrimitive from '@radix-ui/react-switch'
import type { ComponentProps } from 'react'
import { cn } from '@/lib/utils'

/**
 * Switch — Radix switch for immediate, binary settings (no confirm). Track is the
 * strong hairline color when off and copper when on; the thumb slides fast (120ms),
 * color-only otherwise. Focus is a ring on the track. Prefer this over a checkbox
 * for "applies instantly" toggles; use Checkbox for form selections.
 */
export function Switch({
  className,
  ...props
}: ComponentProps<typeof SwitchPrimitive.Root>) {
  return (
    <SwitchPrimitive.Root
      data-slot="switch"
      className={cn(
        'peer relative inline-flex h-5 w-9 shrink-0 items-center rounded-full border border-transparent',
        // WCAG 2.2 SC 2.5.8 Target Size (Min): the 20px-tall track keeps its look
        // but a transparent ::before raises the pointer target to ≥24px tall (the
        // 36px width already clears 24px); the pseudo belongs to the switch.
        "before:absolute before:-inset-y-1 before:inset-x-0 before:content-['']",
        'bg-border-strong transition-colors outline-none',
        'focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background',
        'data-[state=checked]:bg-accent-text',
        'disabled:pointer-events-none disabled:opacity-50',
        className,
      )}
      {...props}
    >
      <SwitchPrimitive.Thumb
        data-slot="switch-thumb"
        className={cn(
          'pointer-events-none block size-4 translate-x-0.5 rounded-full bg-surface shadow-sm',
          'transition-transform duration-150 ease-out data-[state=checked]:translate-x-[1.125rem]',
        )}
      />
    </SwitchPrimitive.Root>
  )
}
