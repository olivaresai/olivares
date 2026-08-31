// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { cn } from '@/lib/utils'

interface SparkChartProps {
  data: number[]
  width?: number
  height?: number
  className?: string
  color?: string
  fillOpacity?: number
}

export function SparkChart({
  data,
  width = 80,
  height = 24,
  className,
  color = 'currentColor',
  fillOpacity = 0.15,
}: SparkChartProps) {
  if (data.length < 2) return null

  const min = Math.min(...data)
  const max = Math.max(...data)
  const range = max - min || 1

  const points = data.map((v, i) => {
    const x = (i / (data.length - 1)) * width
    const y = height - ((v - min) / range) * (height - 2) - 1
    return `${x},${y}`
  })

  const linePath = `M${points.join(' L')}`
  const areaPath = `${linePath} L${width},${height} L0,${height} Z`

  return (
    <svg
      viewBox={`0 0 ${width} ${height}`}
      width={width}
      height={height}
      className={cn('shrink-0', className)}
      aria-hidden
    >
      <path d={areaPath} fill={color} opacity={fillOpacity} />
      <path
        d={linePath}
        fill="none"
        stroke={color}
        strokeWidth={1.5}
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  )
}
