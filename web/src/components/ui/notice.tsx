// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE NOTICE: one sentence inside the pane it concerns, on a soft fill with no border;
// never a permanent banner across a page. Neutral says a fact, warn a reading that may
// be stale, bad a failure; those two carry an inner edge of their color. The error,
// stale and outcome-not-known states draw theirs with it (state-block.tsx).
import type { HTMLAttributes, ReactNode } from 'react'
import { cn } from '@/lib/utils'

type Tone = 'neutral' | 'warn' | 'bad'

const FILL: Record<Tone, string> = {
  neutral: 'bg-muted text-text-2',
  warn: 'bg-warn-soft text-text',
  bad: 'bg-bad-soft text-text',
}

const EDGE: Record<Tone, string> = {
  neutral: '',
  warn: 'shadow-[inset_0_0_0_1px_color-mix(in_srgb,var(--warn)_32%,transparent)]',
  bad: 'shadow-[inset_0_0_0_1px_color-mix(in_srgb,var(--bad)_32%,transparent)]',
}

const ICON: Record<Tone, string> = {
  neutral: 'text-text-3',
  warn: 'text-warn',
  bad: 'text-bad',
}

export function Notice({
  tone = 'neutral',
  icon,
  className,
  children,
  ...props
}: HTMLAttributes<HTMLDivElement> & { tone?: Tone; icon: ReactNode }) {
  return (
    <div
      data-slot="notice"
      data-tone={tone}
      className={cn(
        'flex items-start gap-3 rounded-card px-3.5 py-3 text-start text-caption leading-5',
        FILL[tone],
        EDGE[tone],
        className,
      )}
      {...props}
    >
      <span
        aria-hidden="true"
        className={cn('mt-px flex shrink-0 [&_svg]:size-4.5', ICON[tone])}
      >
        {icon}
      </span>
      <div className="min-w-0">{children}</div>
    </div>
  )
}
