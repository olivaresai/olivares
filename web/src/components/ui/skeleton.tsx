// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { ComponentProps } from 'react'
import { cn } from '@/lib/utils'

/**
 * Skeleton — a loading placeholder. A block on the selection fill with a subtle pulse; size
 * it with width/height utilities to mirror the real content (text lines, an avatar circle,
 * a row). The pulse stops under the reduced-motion preference and the Reduce motion setting.
 */
export function Skeleton({ className, ...props }: ComponentProps<'div'>) {
  return (
    <div
      className={cn(
        'animate-pulse rounded-[6px] bg-active motion-reduce:animate-none [[data-motion=reduce]_&]:animate-none',
        className,
      )}
      {...props}
    />
  )
}
