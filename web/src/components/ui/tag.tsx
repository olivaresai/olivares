// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { cva, type VariantProps } from 'class-variance-authority'
import type { HTMLAttributes } from 'react'
import { cn } from '@/lib/utils'

/**
 * Tag — a boxed non-state fact: a tool, branch, folder, scope or identifier.
 * States use StatusChip; counts use CountBadge. Existing tone/mono props stay compatible.
 */
const tagVariants = cva(
  cn(
    'inline-flex h-[22px] max-w-full items-center gap-[5px] rounded-[6px] px-2',
    'text-overline whitespace-nowrap [&_svg]:size-[13px] [&_svg]:shrink-0',
  ),
  {
    variants: {
      // A fact on the text tint at 12/500 with no outline; a status fact is bold.
      tone: {
        neutral: 'bg-muted font-medium text-text-2',
        ok: 'bg-ok-soft font-bold text-ok',
        warn: 'bg-warn-soft font-bold text-warn',
        bad: 'bg-bad-soft font-bold text-bad',
        info: 'bg-info-soft font-bold text-info',
        accent: 'bg-accent-soft font-bold text-accent-text',
      },
      mono: {
        true: 'font-mono text-mono-s font-normal',
        false: '',
      },
    },
    defaultVariants: { tone: 'neutral', mono: false },
  },
)

export type TagTone = NonNullable<VariantProps<typeof tagVariants>['tone']>

export interface TagProps
  extends HTMLAttributes<HTMLSpanElement>, VariantProps<typeof tagVariants> {}

export function Tag({ className, tone, mono, ...props }: TagProps) {
  return (
    <span
      data-slot="tag"
      data-tone={tone ?? 'neutral'}
      className={cn(tagVariants({ tone, mono }), className)}
      {...props}
    />
  )
}
