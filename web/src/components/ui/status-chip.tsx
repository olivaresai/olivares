// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { HTMLAttributes, ReactNode } from 'react'
import { cn } from '@/lib/utils'

const DOT = {
  neutral: 'bg-text-3',
  ok: 'bg-ok',
  warn: 'bg-warn',
  bad: 'bg-bad',
  info: 'bg-info',
} as const

export type StatusTone = keyof typeof DOT

/** Decoration only: its chip or icon-only wrapper carries the state word. */
export function StatusDot({
  tone = 'neutral',
  working = false,
  className,
}: {
  tone?: StatusTone
  working?: boolean
  className?: string
}) {
  return (
    <span
      aria-hidden="true"
      data-slot="status-dot"
      data-shape="dot"
      className={cn(
        'size-1.5 shrink-0 rounded-full',
        'forced-colors:[background-color:CanvasText] forced-colors:[forced-color-adjust:none]',
        DOT[tone],
        working &&
          'animate-pulse [animation-duration:2s] motion-reduce:animate-none [[data-motion=reduce]_&]:animate-none',
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
  working?: boolean
}

/** State = dot + word. Boxed Tag/Badge components are for facts. */
export function StatusChip({
  children,
  tone = 'neutral',
  working = false,
  className,
  ...props
}: StatusChipProps) {
  if (typeof children === 'string' && !children.trim())
    throw new Error('StatusChip needs its state word.')
  return (
    <span
      data-slot="status-chip"
      className={cn(
        'inline-flex min-w-0 items-center gap-1.5 text-caption font-medium whitespace-nowrap text-text',
        className,
      )}
      {...props}
    >
      <StatusDot tone={tone} working={working} />
      {children}
    </span>
  )
}
