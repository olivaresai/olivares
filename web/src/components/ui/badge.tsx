// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { cva, type VariantProps } from 'class-variance-authority'
import type { HTMLAttributes } from 'react'
import { cn } from '@/lib/utils'

/**
 * Badge — the compatible boxed fact label. States use StatusChip. Each semantic variant is text =
 * semantic color on the semantic soft fill, with no outline, so ≤2 colors show per row
 * and the table stays calm. Counts/ids inside should use mono
 * tabular-nums. Access-mode and R/RW confidence badges compose this in components/data
 * so they can localize their labels; status badges compose StatusChip instead.
 */
const badgeVariants = cva(
  cn(
    'inline-flex h-[22px] items-center gap-1 rounded-[6px] px-2',
    'text-overline whitespace-nowrap',
  ),
  {
    variants: {
      // The tag's look: the text tint at 12/500 with no outline; a status fact is bold.
      variant: {
        neutral: 'bg-muted font-medium text-text-2',
        accent: 'bg-accent-soft font-bold text-accent-soft-foreground',
        success: 'bg-success-soft font-bold text-success',
        warning: 'bg-warning-soft font-bold text-warning',
        danger: 'bg-danger-soft font-bold text-danger',
        info: 'bg-info-soft font-bold text-info',
        /* outline-only chip for low-emphasis metadata */
        outline: 'border border-line-strong font-medium text-text-2',
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
