// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// MetricStat / StatGrid — the headline-number primitives the intelligence views lead
// with (total spend, pass-rate, robustness score, open findings…). A label, a large
// figure, an optional caption, an optional trend (sparkline or delta). Built on the
// design-system Card so density stays calm and consistent across all five views.
import type { ReactNode } from 'react'
import { cn } from '@/lib/utils'
import { Card } from '@/components/ui/card'

/** Responsive grid for a row of MetricStats (auto-fits, min column width). */
export function StatGrid({
  children,
  className,
}: {
  children: ReactNode
  className?: string
}) {
  return (
    <div
      className={cn(
        'grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4',
        className,
      )}
    >
      {children}
    </div>
  )
}

export interface MetricStatProps {
  label: ReactNode
  value: ReactNode
  /** Sub-line under the value (units, period, qualifier). */
  caption?: ReactNode
  /** A small leading icon chip. */
  icon?: ReactNode
  /** A trend/aux node rendered at the bottom of the card (e.g. a <Sparkline/>). */
  trend?: ReactNode
  /** Right-aligned slot next to the label (e.g. a badge). */
  aside?: ReactNode
  /** Tone tints the value (used sparingly — e.g. a budget over its limit). */
  tone?: 'default' | 'success' | 'warning' | 'danger'
  className?: string
}

const VALUE_TONE: Record<NonNullable<MetricStatProps['tone']>, string> = {
  default: 'text-foreground',
  success: 'text-success',
  warning: 'text-warning',
  danger: 'text-danger',
}

export function MetricStat({
  label,
  value,
  caption,
  icon,
  trend,
  aside,
  tone = 'default',
  className,
}: MetricStatProps) {
  return (
    <Card className={cn('flex flex-col gap-2 p-4', className)}>
      <div className="flex items-start justify-between gap-2">
        <div className="flex items-center gap-2">
          {/* The label text carries the meaning, so the leading icon is decorative —
              hide it from the a11y tree (WCAG 1.1.1, no redundant announcement). */}
          {icon ? (
            <span
              className="flex size-7 items-center justify-center rounded-md bg-muted text-muted-foreground [&_svg]:size-4"
              aria-hidden
            >
              {icon}
            </span>
          ) : null}
          <span className="text-xs font-medium tracking-wide text-muted-foreground uppercase">
            {label}
          </span>
        </div>
        {aside}
      </div>
      <div className="flex flex-col gap-0.5">
        <span
          className={cn(
            'font-display text-2xl font-semibold tracking-tight tabular-nums',
            VALUE_TONE[tone],
          )}
        >
          {value}
        </span>
        {caption ? (
          <span className="text-xs text-muted-foreground">{caption}</span>
        ) : null}
      </div>
      {trend ? <div className="mt-auto pt-1">{trend}</div> : null}
    </Card>
  )
}
