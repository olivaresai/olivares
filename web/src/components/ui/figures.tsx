// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE FIGURES STRIP: the headline numbers a view leads with (total spend, pass rate, open
// findings…). One strip on a surface above the page, with the card shadow and no outline,
// whose cells are split by hairlines, not a card per number: a label in the second tone,
// the figure in the display face, one comparison line.
// The cells wrap as the width shrinks, and the hairlines follow them.
//
// The names are the ones the views already import (`StatGrid`, `MetricStat`, through
// `@/features/_intel`), so every view that had a row of number cards now draws this strip.
import type { ReactNode } from 'react'
import { cn } from '@/lib/utils'

/** The strip: each cell draws its end and bottom hairline, and the strip clips the last
 *  column's and the last row's against its own edge. */
export function StatGrid({
  children,
  className,
}: {
  children: ReactNode
  className?: string
}) {
  return (
    <div
      data-slot="figures"
      className={cn(
        'overflow-hidden rounded-card bg-surface shadow-[var(--shadow-card)]',
        className,
      )}
    >
      <div className="-me-px -mb-px grid grid-cols-[repeat(auto-fit,minmax(11rem,1fr))]">
        {children}
      </div>
    </div>
  )
}

export interface MetricStatProps {
  label: ReactNode
  value: ReactNode
  /** The one comparison line under the figure (units, period, against what). */
  caption?: ReactNode
  /** A small icon before the label; the label carries the meaning. */
  icon?: ReactNode
  /** A trend node at the bottom of the cell (e.g. a <Sparkline/>). */
  trend?: ReactNode
  /** Right-aligned slot next to the label (e.g. a badge). */
  aside?: ReactNode
  /** Tone tints the figure (used sparingly — e.g. a budget over its limit). */
  tone?: 'default' | 'success' | 'warning' | 'danger'
  /** `compact` for a narrow side column: a smaller figure and padding. */
  density?: 'default' | 'compact'
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
  density = 'default',
  className,
}: MetricStatProps) {
  const compact = density === 'compact'
  return (
    <div
      data-slot="figure"
      className={cn(
        'flex min-w-0 flex-col gap-1 border-e border-b border-line',
        compact ? 'px-3 py-2.5' : 'px-4.5 pt-4 pb-4',
        className,
      )}
    >
      <div className="flex items-start justify-between gap-2">
        <span className="flex min-w-0 items-center gap-1.5 text-caption text-text-2">
          {/* The label carries the meaning: the icon is decoration (WCAG 1.1.1). */}
          {icon ? (
            <span className="flex shrink-0 [&_svg]:size-3.5" aria-hidden>
              {icon}
            </span>
          ) : null}
          {label}
        </span>
        {aside}
      </div>
      <span
        className={cn(
          'font-display tabular-nums',
          compact ? 'text-heading font-semibold' : 'text-display',
          VALUE_TONE[tone],
        )}
      >
        {value}
      </span>
      {caption ? (
        <span className="min-w-0 text-caption text-balance text-text-3">
          {caption}
        </span>
      ) : null}
      {trend ? <div className="mt-auto pt-1">{trend}</div> : null}
    </div>
  )
}
