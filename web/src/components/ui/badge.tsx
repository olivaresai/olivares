// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { cva, type VariantProps } from 'class-variance-authority'
import type { HTMLAttributes } from 'react'
import { cn } from '@/lib/utils'

/**
 * Badge — the subtle-fill status/label chip. Each semantic variant is text =
 * semantic color, bg = the semantic soft fill, border = the semantic line, so ≤2
 * colors show per row and the table stays calm. Counts/ids inside should use mono
 * tabular-nums. Specialized product badges (status / access mode / R-RW confidence)
 * compose this in components/data so they can localize their labels.
 */
const badgeVariants = cva(
  cn(
    'inline-flex items-center gap-1 rounded-sm border px-1.5 py-0.5',
    'text-xs font-medium leading-none whitespace-nowrap',
  ),
  {
    variants: {
      variant: {
        neutral: 'border-border bg-muted text-muted-foreground',
        accent: 'border-accent-line bg-accent-soft text-accent-soft-foreground',
        success: 'border-success-line bg-success-soft text-success',
        warning: 'border-warning-line bg-warning-soft text-warning',
        danger: 'border-danger-line bg-danger-soft text-danger',
        info: 'border-info-line bg-info-soft text-info',
        /* outline-only chip for low-emphasis metadata */
        outline: 'border-border bg-transparent text-muted-foreground',
      },
    },
    defaultVariants: { variant: 'neutral' },
  },
)

export type BadgeVariant = NonNullable<
  VariantProps<typeof badgeVariants>['variant']
>

export interface BadgeProps
  extends HTMLAttributes<HTMLSpanElement>, VariantProps<typeof badgeVariants> {}

export function Badge({ className, variant, ...props }: BadgeProps) {
  return (
    <span className={cn(badgeVariants({ variant }), className)} {...props} />
  )
}

export { badgeVariants }
