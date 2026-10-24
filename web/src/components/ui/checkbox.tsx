// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import * as CheckboxPrimitive from '@radix-ui/react-checkbox'
import { Check } from 'lucide-react'
import type { ComponentProps } from 'react'
import { cn } from '@/lib/utils'

/**
 * Checkbox — Radix checkbox with a crisp lucide Check glyph. Hairline border when
 * unchecked; checked and indeterminate fill the box with the neutral ink — state,
 * not an action, so it never counts as one of a screen's orange verb controls
 * (#2701, the #2696 Switch decision). Focus is a ring. Supports the indeterminate
 * state Radix exposes (`checked="indeterminate"`), which renders the same fill
 * (callers can swap the glyph if they need a dash).
 */
export function Checkbox({
  className,
  ...props
}: ComponentProps<typeof CheckboxPrimitive.Root>) {
  return (
    <CheckboxPrimitive.Root
      data-slot="checkbox"
      className={cn(
        'peer relative size-4 shrink-0 rounded-sm border border-ctl-border bg-surface',
        // WCAG 2.2 SC 2.5.8 Target Size (Min): the visual box stays 16px but a
        // transparent ::before extends the pointer target to 24×24 CSS px (the
        // pseudo-element belongs to the control, so clicks on it toggle it).
        "before:absolute before:-inset-1 before:content-['']",
        'transition-colors outline-none',
        'focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background',
        'data-[state=checked]:bg-foreground data-[state=checked]:text-surface',
        'data-[state=indeterminate]:bg-foreground data-[state=indeterminate]:text-surface',
        'disabled:pointer-events-none disabled:opacity-50',
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
