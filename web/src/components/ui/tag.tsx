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
    'inline-flex h-[22px] max-w-full items-center gap-[5px] rounded-[6px] border px-2',
    'text-overline whitespace-nowrap [&_svg]:size-3 [&_svg]:shrink-0',
  ),
  {
    variants: {
      tone: {
        neutral: 'border-line bg-surface text-text-2',
        ok: 'border-transparent bg-ok-soft text-ok',
        warn: 'border-transparent bg-warn-soft text-warn',
        bad: 'border-transparent bg-bad-soft text-bad',
        info: 'border-transparent bg-info-soft text-info',
        accent: 'border-transparent bg-accent-soft text-accent-text',
      },
      mono: {
        true: 'font-mono text-mono-s',
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
