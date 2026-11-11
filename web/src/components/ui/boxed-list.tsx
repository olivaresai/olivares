// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { ComponentProps } from 'react'
import { cn } from '@/lib/utils'

/** One group on a surface above the page, with the card shadow and no outline: the only
 *  hairlines are the ones between rows. Rows keep their native controls and semantics. */
export function BoxedList({ className, ...props }: ComponentProps<'div'>) {
  return (
    <div
      data-slot="boxed-list"
      className={cn(
        'overflow-hidden rounded-card bg-surface shadow-[var(--shadow-card)] [&>[data-slot=boxed-list-row]:first-child]:border-t-0',
        className,
      )}
      {...props}
    />
  )
}

export function BoxedListRow({ className, ...props }: ComponentProps<'div'>) {
  return (
    <div
      data-slot="boxed-list-row"
      className={cn(
        'flex min-h-[var(--density-row-list)] items-center gap-3.5 border-t border-line px-4 py-[9px]',
        className,
      )}
      {...props}
    />
  )
}
