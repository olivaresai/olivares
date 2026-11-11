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
  /** In the rail's corner: 16 px high, in the counter step. */
  compact?: boolean
}

/** A quiet 18 px count pill, 11 px bold on a 10 % ink tint; on the soft amber token when it
 * asks for a person (opaque, so the same on any surface under it); absent at zero. */
export function CountBadge({
  count,
  attention = false,
  compact = false,
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
        'inline-flex shrink-0 items-center justify-center rounded-full font-bold tabular-nums',
        compact
          ? 'h-4 min-w-4 px-1 text-metadata-s'
          : 'h-[18px] min-w-5 px-1.5 text-metadata',
        attention
          ? 'bg-warn-soft text-warn'
          : 'bg-[color-mix(in_srgb,var(--text)_10%,transparent)] text-text-2',
        className,
      )}
      {...props}
    >
      {count}
    </span>
  )
}
