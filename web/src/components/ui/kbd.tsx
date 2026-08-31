// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { ComponentProps } from 'react'
import { cn } from '@/lib/utils'

/**
 * Kbd — a keyboard-key chip for shortcut hints (command menu, tooltips, docs). A small
 * mono pill on a `muted` fill with a hairline border; render the glyphs as children
 * (e.g. `⌘`, `K`, `Esc`). Use one Kbd per key and separate with a literal `+` for
 * chords so each key reads as its own physical cap.
 */
export function Kbd({ className, ...props }: ComponentProps<'kbd'>) {
  return (
    <kbd
      className={cn(
        'inline-flex h-5 items-center gap-0.5 rounded-sm border border-border bg-muted px-1.5',
        'text-[0.6875rem] font-mono leading-none text-muted-foreground',
        className,
      )}
      {...props}
    />
  )
}
