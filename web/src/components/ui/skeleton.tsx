// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { ComponentProps } from 'react'
import { cn } from '@/lib/utils'

/**
 * Skeleton — a loading placeholder. A static block on the selection fill (C2-DESIGN §3:
 * nothing pulses); size it with width/height utilities to mirror the real content (text
 * lines, an avatar circle, a row). aria-busy keeps it announced as loading and lets the
 * release readiness guard (surface.ts) treat it as the in-progress marker it is.
 */
export function Skeleton({ className, ...props }: ComponentProps<'div'>) {
  return (
    <div
      aria-busy="true"
      className={cn('rounded-[6px] bg-active', className)}
      {...props}
    />
  )
}
