// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { HTMLAttributes, ReactNode } from 'react'
import { cn } from '@/lib/utils'

const DOT = {
  neutral: 'bg-text-3',
  ok: 'bg-ok',
  warn: 'bg-warn',
  /** What waits for the person: the warm dot, and the word bold in the same ink. */
  need: 'bg-warn',
  bad: 'bg-bad',
  info: 'bg-info',
} as const

/** The word: calm in the second tone; a need is warm and bold, a failure in its ink. */
const WORD: Record<keyof typeof DOT, string> = {
  neutral: 'text-text-2',
  ok: 'text-text-2',
  warn: 'text-text-2',
  need: 'font-bold text-warn',
  bad: 'font-medium text-bad',
  info: 'text-text-2',
}

export type StatusTone = keyof typeof DOT

/** Decoration only: its chip or icon-only wrapper carries the state word. */
export function StatusDot({
  tone = 'neutral',
  className,
}: {
  tone?: StatusTone
  className?: string
}) {
  return (
    <span
      aria-hidden="true"
      data-slot="status-dot"
      data-shape="dot"
      className={cn(
        'size-2 shrink-0 rounded-full',
        'forced-colors:[background-color:CanvasText] forced-colors:[forced-color-adjust:none]',
        DOT[tone],
        className,
      )}
    />
  )
}

export interface StatusChipProps extends Omit<
  HTMLAttributes<HTMLSpanElement>,
  'children'
> {
  children: ReactNode
  tone?: StatusTone
}

/** State = dot + word. Boxed Tag/Badge components are for facts. */
export function StatusChip({
  children,
  tone = 'neutral',
  className,
  ...props
}: StatusChipProps) {
  if (typeof children === 'string' && !children.trim())
    throw new Error('StatusChip needs its state word.')
  return (
    <span
      data-slot="status-chip"
      className={cn(
        'inline-flex min-w-0 items-center gap-2 text-caption whitespace-nowrap',
        WORD[tone],
        className,
      )}
      {...props}
    >
      <StatusDot tone={tone} />
      {children}
    </span>
  )
}
