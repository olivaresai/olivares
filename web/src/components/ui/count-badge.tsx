// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { HTMLAttributes } from 'react'
import { cn } from '@/lib/utils'

export interface CountBadgeProps extends Omit<
  HTMLAttributes<HTMLSpanElement>,
  'children'
> {
  count: number | string
  attention?: boolean
}

/** A quiet 18 px count pill in the counter size (10 px); amber when it asks for a person,
 * absent at zero. */
export function CountBadge({
  count,
  attention = false,
  className,
  ...props
}: CountBadgeProps) {
  if (
    count === '' ||
    count === '0' ||
    (typeof count === 'number' && (!Number.isFinite(count) || count <= 0))
  )
    return null
  return (
    <span
      data-slot="count-badge"
      className={cn(
        'inline-flex h-[18px] min-w-[18px] shrink-0 items-center justify-center rounded-full px-1.5 text-metadata-s font-medium tabular-nums',
        attention ? 'bg-warn-soft text-warn' : 'bg-hover text-text-2',
        className,
      )}
      {...props}
    >
      {count}
    </span>
  )
}
