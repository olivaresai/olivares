// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import * as CheckboxPrimitive from '@radix-ui/react-checkbox'
import { Check } from 'lucide-react'
import type { ComponentProps } from 'react'
import { cn } from '@/lib/utils'

/**
 * Checkbox — Radix checkbox with a crisp lucide Check glyph: an 18 px box with a 1.5 px
 * control-boundary edge (3:1) when unchecked; checked and indeterminate are the orange with the
 * graphite check and an accent edge, as the switch is when on. Focus is the 2 px outline ring.
 * A checkbox that cannot act is faded to 45 %. Supports the indeterminate state Radix exposes
 * (`checked="indeterminate"`), which renders the same fill (callers can swap the glyph if they
 * need a dash).
 */
export function Checkbox({
  className,
  ...props
}: ComponentProps<typeof CheckboxPrimitive.Root>) {
  return (
    <CheckboxPrimitive.Root
      data-slot="checkbox"
      className={cn(
        'peer relative size-[18px] shrink-0 rounded-[5px] border-[1.5px] border-ctl-border bg-surface',
        // WCAG 2.2 SC 2.5.8 Target Size (Min): the visual box stays 18 px but a
        // transparent ::before extends the pointer target to 25x25 CSS px: 5 px past the
        // 15 px inside the edge (the pseudo-element belongs to the control, so clicks on
        // it toggle it).
        "before:absolute before:-inset-[5px] before:content-['']",
        'transition-[background-color,border-color] ease-out outline-none',
        'focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-focus',
        'data-[state=checked]:border-accent-strong data-[state=checked]:bg-accent data-[state=checked]:text-on-accent',
        'data-[state=indeterminate]:border-accent-strong data-[state=indeterminate]:bg-accent data-[state=indeterminate]:text-on-accent',
        'disabled:pointer-events-none disabled:opacity-45',
        'aria-disabled:cursor-not-allowed aria-disabled:opacity-45',
        className,
      )}
      {...props}
    >
      <CheckboxPrimitive.Indicator
        data-slot="checkbox-indicator"
        className="flex items-center justify-center text-current"
      >
        <Check className="size-3 stroke-[3]" />
      </CheckboxPrimitive.Indicator>
    </CheckboxPrimitive.Root>
  )
}
