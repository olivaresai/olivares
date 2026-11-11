// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { ComponentProps } from 'react'
import { cn } from '@/lib/utils'

/**
 * Kbd — a keyboard-key chip for shortcut hints (command menu, tooltips, docs): a soft key, not a
 * bordered box. An 8 % tint of the text ink with a darker lower edge, in the key-hint size (the
 * text face: a key is not code) at 600; inside a button it takes the button's own ink. Render
 * the glyphs as children (e.g. `⌘`, `K`, `Esc`). Use one Kbd per key and separate with a
 * literal `+` for chords so each key reads as its own physical cap.
 */
export function Kbd({ className, ...props }: ComponentProps<'kbd'>) {
  return (
    <kbd
      className={cn(
        'inline-flex h-[18px] min-w-5 items-center justify-center gap-0.5 rounded-[5px] px-1.5',
        'bg-[color-mix(in_srgb,var(--text)_8%,transparent)] shadow-[inset_0_-1.5px_0_var(--line-strong)]',
        'text-metadata-s font-semibold leading-none text-text-2',
        className,
      )}
      {...props}
    />
  )
}
