// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import * as CheckboxPrimitive from '@radix-ui/react-checkbox'
import { Check } from 'lucide-react'
import type { ComponentProps } from 'react'
import { cn } from '@/lib/utils'

/**
 * Checkbox — Radix checkbox with a copper-filled checked state and a crisp lucide
 * Check glyph. Hairline border when unchecked, accent fill when checked; focus is a
 * ring. Supports the indeterminate state Radix exposes (`checked="indeterminate"`),
 * which renders the same fill (callers can swap the glyph if they need a dash).
 */
export function Checkbox({
  className,
  ...props
}: ComponentProps<typeof CheckboxPrimitive.Root>) {
  return (
    <CheckboxPrimitive.Root
      data-slot="checkbox"
      className={cn(
        'peer relative size-4 shrink-0 rounded-sm border border-border-strong bg-surface',
        // WCAG 2.2 SC 2.5.8 Target Size (Min): the visual box stays 16px but a
        // transparent ::before extends the pointer target to 24×24 CSS px (the
        // pseudo-element belongs to the control, so clicks on it toggle it).
        "before:absolute before:-inset-1 before:content-['']",
        'transition-colors outline-none',
        'focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background',
        'data-[state=checked]:border-accent-text data-[state=checked]:bg-accent data-[state=checked]:text-accent-foreground',
        'data-[state=indeterminate]:border-accent-text data-[state=indeterminate]:bg-accent data-[state=indeterminate]:text-accent-foreground',
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
