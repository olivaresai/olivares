// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import * as SwitchPrimitive from '@radix-ui/react-switch'
import type { ComponentProps } from 'react'
import { cn } from '@/lib/utils'

/**
 * Switch — Radix switch for immediate, binary settings (no confirm), drawn as a full pill.
 * Off: the control-boundary track (3:1 against the row) with a white knob: the surface in light,
 * the inverse surface in dark and black (3.2:1 on the track; the black theme's text ink is grey).
 * Under the pointer the off track darkens, so the knob keeps 3:1. On: the orange with the graphite
 * knob and an accent edge, as everything that sits on the orange is graphite (white on it is
 * 2.7:1). The knob slides with the duration token (none under reduced motion); a switch
 * that cannot act is faded to 45 %. The 24 px track has 44 px of pointer target. Focus is a ring
 * outside the track. Prefer this over a checkbox for "applies instantly" toggles; use Checkbox
 * for form selections.
 */
export function Switch({
  className,
  ...props
}: ComponentProps<typeof SwitchPrimitive.Root>) {
  return (
    <SwitchPrimitive.Root
      data-slot="switch"
      className={cn(
        'peer relative inline-flex h-6 w-11 shrink-0 items-center rounded-full border border-transparent',
        // WCAG 2.2 SC 2.5.8 Target Size: the 24 px track keeps its look and a transparent
        // ::before raises the pointer target to 44 px high (its offsets start inside the 1 px
        // border: 11 px there is 10 px outside the track); the pseudo belongs to the switch.
        "before:absolute before:-inset-x-0.5 before:-inset-y-[11px] before:content-['']",
        'bg-ctl-border transition-[background-color,border-color] ease-out outline-none',
        // Under the pointer the off track darkens: toward the ink in light, the frame in dark
        // (8 % there: the track is still the boundary and keeps 3:1 on a raised row).
        'data-[state=unchecked]:hover:bg-[color-mix(in_srgb,var(--text)_12%,var(--ctl-border))]',
        'dark:data-[state=unchecked]:hover:bg-[color-mix(in_srgb,var(--frame)_8%,var(--ctl-border))]',
        // The accent edge is the border, the outermost pixel, as on the checkbox.
        'data-[state=checked]:border-accent-strong data-[state=checked]:bg-accent data-[state=checked]:hover:bg-accent-hover',
        'focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-focus',
        'disabled:pointer-events-none disabled:opacity-45',
        'aria-disabled:cursor-not-allowed aria-disabled:opacity-45',
        className,
      )}
      {...props}
    >
      <SwitchPrimitive.Thumb
        data-slot="switch-thumb"
        className={cn(
          'pointer-events-none block size-[18px] translate-x-[2px] rounded-full bg-surface shadow-[var(--shadow-card)] dark:bg-[var(--inverse)]',
          'transition-[translate,background-color] ease-out',
          'data-[state=checked]:translate-x-[22px] data-[state=checked]:bg-on-accent data-[state=checked]:shadow-none',
        )}
      />
    </SwitchPrimitive.Root>
  )
}
