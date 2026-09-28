// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import * as SwitchPrimitive from '@radix-ui/react-switch'
import type { ComponentProps } from 'react'
import { cn } from '@/lib/utils'

/**
 * Switch — Radix switch for immediate, binary settings (no confirm). Track is the
 * control boundary color when off (3:1 against the surface, and under the thumb) and the
 * orange fill with its border and the on-accent thumb when on; the thumb slides fast
 * (120ms); a switch that cannot act is a dashed empty track,
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
        'bg-ctl-border transition-colors duration-100 ease-out outline-none',
        'focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-focus',
        'data-[state=checked]:border-accent-border data-[state=checked]:bg-accent',
        'disabled:pointer-events-none disabled:border-dashed disabled:border-ctl-border disabled:bg-transparent',
        'aria-disabled:cursor-not-allowed aria-disabled:border-dashed aria-disabled:border-ctl-border aria-disabled:bg-transparent',
        className,
      )}
      {...props}
    >
      <SwitchPrimitive.Thumb
        data-slot="switch-thumb"
        className={cn(
          'pointer-events-none block size-3.5 translate-x-[3px] rounded-full bg-surface',
          'transition-transform duration-100 ease-out data-[state=checked]:translate-x-[17px] data-[state=checked]:bg-on-accent',
          '[[data-disabled]>&]:bg-ctl-border [[aria-disabled=true]>&]:bg-ctl-border',
        )}
      />
    </SwitchPrimitive.Root>
  )
}
