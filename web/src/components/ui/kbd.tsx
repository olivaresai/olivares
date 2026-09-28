// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { ComponentProps } from 'react'
import { cn } from '@/lib/utils'

/**
 * Kbd — a keyboard-key chip for shortcut hints (command menu, tooltips, docs). A small
 * mono cap on the surface with the strong hairline; render the glyphs as children
 * (e.g. `⌘`, `K`, `Esc`). Use one Kbd per key and separate with a literal `+` for
 * chords so each key reads as its own physical cap.
 */
export function Kbd({ className, ...props }: ComponentProps<'kbd'>) {
  return (
    <kbd
      className={cn(
        'inline-flex h-5 min-w-5 items-center justify-center gap-0.5 rounded-[5px] border border-line-strong bg-surface px-[5px]',
        'text-[0.6875rem] font-mono font-medium leading-none text-text-2',
        className,
      )}
      {...props}
    />
  )
}
