// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// CostSparkline — a tiny axis-less area trend for the daily cost column.
// Wraps the shared Sparkline primitive from @/components/charts, converting the
// raw number[] from the backend into the Record array that Sparkline expects.
// Decorative — always aria-hidden; the cost value itself conveys the data.
import { useMemo } from 'react'
import { Sparkline } from '@/components/charts'

export interface CostSparklineProps {
  /** Per-day cost in micro-USD, zero-filled by the backend for empty days. */
  data: number[]
  /** Stroke color override; falls back to the chart theme accent. */
  color?: string
  /** Height in px (default 40). */
  height?: number
}

/**
 * CostSparkline converts a flat `number[]` trend series (micro-USD per day)
 * into the `{ v: number }[]` record format that the shared `Sparkline`
 * component expects, then delegates all rendering to it.
 */
export function CostSparkline({ data, color, height = 40 }: CostSparklineProps) {
  const points = useMemo(
    () => data.map((v) => ({ v })),
    [data],
  )

  // An all-zero trend is still meaningful (nothing spent) — render the flat line.
  return (
    <Sparkline
      data={points}
      dataKey="v"
      color={color}
      height={height}
      className="min-w-[80px]"
    />
  )
}
