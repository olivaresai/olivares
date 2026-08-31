// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE themed chart kit for the intelligence views (decision 1 = Recharts).
// Every wrapper resolves its colors from the live design tokens (useChartTheme), so
// charts track light/dark with the rest of the console and never carry a raw hex.
// These are PRESENTATION primitives — they render the modules' data, they do not
// compute it. The A2A communication graph (orchestration) uses React Flow, not this
// kit; this is for quantitative density (trends, breakdowns, gauges, scorecards).
import type { ComponentProps, ReactElement, ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import {
  Area,
  AreaChart,
  Bar,
  BarChart,
  CartesianGrid,
  Cell,
  Line,
  LineChart,
  Pie,
  PieChart,
  ReferenceLine,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts'
import { cn } from '@/lib/utils'
import { useChartTheme, type ChartTheme } from './chart-theme'

export { useChartTheme, type ChartTheme }

/** Recharts' render-prop `content` type (loose, lib-defined). We cast our themed
 *  tooltip to it at the call site — its readonly/wider payload is a structural
 *  superset of what we read (name/value/color/dataKey). */
type TooltipContent = ComponentProps<typeof Tooltip>['content']

type Formatter = (value: number) => string
const identity: Formatter = (v) => String(v)

const AXIS_FONT = 11

/** A series in a multi-line / multi-area trend chart. */
export interface SeriesSpec {
  key: string
  label: string
  color?: string
  kind?: 'area' | 'line'
}

// --- frame -------------------------------------------------------------------

/** Wraps a Recharts element in a responsive box with a calm empty state. A chart
 *  with no data must NOT render an axis cross with nothing on it — that reads as a
 *  bug; show "no data" instead. */
function ChartFrame({
  height,
  empty,
  emptyLabel,
  children,
  className,
}: {
  height: number
  empty?: boolean
  emptyLabel?: string
  children: ReactElement
  className?: string
}) {
  const { t } = useTranslation('common')
  if (empty) {
    return (
      <div
        style={{ height }}
        className={cn(
          'flex items-center justify-center rounded-md border border-dashed border-border',
          className,
        )}
      >
        <span className="text-xs text-muted-foreground">
          {emptyLabel ?? t('states.noResults')}
        </span>
      </div>
    )
  }
  return (
    <div style={{ width: '100%', height }} className={className}>
      <ResponsiveContainer width="100%" height="100%">
        {children}
      </ResponsiveContainer>
    </div>
  )
}

// --- tooltip -----------------------------------------------------------------

interface TooltipEntry {
  name?: string
  value?: number | string
  color?: string
  dataKey?: string | number
}

function makeTooltip(
  theme: ChartTheme,
  valueFormatter: Formatter,
  labelFormatter?: (label: string) => string,
) {
  return function ChartTooltip(props: {
    active?: boolean
    payload?: readonly TooltipEntry[]
    label?: string | number
  }): ReactNode {
    if (!props.active || !props.payload?.length) return null
    const label =
      props.label !== undefined
        ? labelFormatter
          ? labelFormatter(String(props.label))
          : String(props.label)
        : undefined
    return (
      <div
        className="rounded-md border border-border-strong bg-elevated px-2.5 py-1.5 shadow-lg"
        style={{ minWidth: 120 }}
      >
        {label ? (
          <p className="mb-1 text-xs font-medium text-foreground">{label}</p>
        ) : null}
        <ul className="flex flex-col gap-0.5">
          {props.payload.map((entry, i) => (
            <li
              key={entry.dataKey ?? i}
              className="flex items-center justify-between gap-3 text-xs"
            >
              <span className="flex items-center gap-1.5 text-muted-foreground">
                <span
                  aria-hidden
                  className="size-2 rounded-[2px]"
                  style={{ backgroundColor: entry.color ?? theme.mutedText }}
                />
                {entry.name}
              </span>
              <span className="font-mono tabular-nums text-foreground">
                {typeof entry.value === 'number'
                  ? valueFormatter(entry.value)
                  : entry.value}
              </span>
            </li>
          ))}
        </ul>
      </div>
    )
  }
}

// --- trend (area / line) -----------------------------------------------------

export interface TrendChartProps {
  data: Record<string, unknown>[]
  xKey: string
  series: SeriesSpec[]
  height?: number
  stacked?: boolean
  valueFormatter?: Formatter
  xTickFormatter?: (value: string) => string
  /** Optional horizontal reference line (e.g. a budget limit, a pass threshold). */
  reference?: { y: number; label?: string; color?: string }
  className?: string
  emptyLabel?: string
}

/** Time-series trend — areas by default (filled = volume), lines for overlays.
 *  Used for cost/token trends (FinOps), score trends (evals), latency (voice). */
export function TrendChart({
  data,
  xKey,
  series,
  height = 240,
  stacked = false,
  valueFormatter = identity,
  xTickFormatter,
  reference,
  className,
  emptyLabel,
}: TrendChartProps) {
  const theme = useChartTheme()
  const hasArea = series.some((s) => (s.kind ?? 'area') === 'area')
  const Chart = hasArea ? AreaChart : LineChart
  const colorFor = (s: SeriesSpec, i: number) =>
    s.color ?? theme.series[i % theme.series.length]

  return (
    <ChartFrame
      height={height}
      empty={data.length === 0}
      emptyLabel={emptyLabel}
      className={className}
    >
      <Chart data={data} margin={{ top: 8, right: 8, bottom: 0, left: 0 }}>
        <defs>
          {series.map((s, i) => (
            <linearGradient
              key={s.key}
              id={`grad-${s.key}`}
              x1="0"
              y1="0"
              x2="0"
              y2="1"
            >
              <stop offset="0%" stopColor={colorFor(s, i)} stopOpacity={0.28} />
              <stop
                offset="100%"
                stopColor={colorFor(s, i)}
                stopOpacity={0.02}
              />
            </linearGradient>
          ))}
        </defs>
        <CartesianGrid
          stroke={theme.grid}
          strokeDasharray="2 4"
          vertical={false}
        />
        <XAxis
          dataKey={xKey}
          tick={{ fill: theme.mutedText, fontSize: AXIS_FONT }}
          tickFormatter={xTickFormatter}
          tickLine={false}
          axisLine={{ stroke: theme.grid }}
          minTickGap={24}
        />
        <YAxis
          tick={{ fill: theme.mutedText, fontSize: AXIS_FONT }}
          tickFormatter={(v: number) => valueFormatter(v)}
          tickLine={false}
          axisLine={false}
          width={56}
        />
        <Tooltip
          content={
            makeTooltip(
              theme,
              valueFormatter,
              xTickFormatter,
            ) as unknown as TooltipContent
          }
          cursor={{ stroke: theme.border, strokeWidth: 1 }}
        />
        {reference ? (
          <ReferenceLine
            y={reference.y}
            stroke={reference.color ?? theme.warning}
            strokeDasharray="4 3"
            label={
              reference.label
                ? {
                    value: reference.label,
                    fill: reference.color ?? theme.warning,
                    fontSize: AXIS_FONT,
                    position: 'insideTopRight',
                  }
                : undefined
            }
          />
        ) : null}
        {series.map((s, i) =>
          (s.kind ?? 'area') === 'line' ? (
            <Line
              key={s.key}
              type="monotone"
              dataKey={s.key}
              name={s.label}
              stroke={colorFor(s, i)}
              strokeWidth={2}
              dot={false}
              activeDot={{ r: 3 }}
            />
          ) : (
            <Area
              key={s.key}
              type="monotone"
              dataKey={s.key}
              name={s.label}
              stackId={stacked ? 'stack' : undefined}
              stroke={colorFor(s, i)}
              strokeWidth={2}
              fill={`url(#grad-${s.key})`}
              activeDot={{ r: 3 }}
            />
          ),
        )}
      </Chart>
    </ChartFrame>
  )
}

// --- category bars -----------------------------------------------------------

export interface CategoryBarChartProps {
  data: Record<string, unknown>[]
  categoryKey: string
  valueKey: string
  height?: number
  valueFormatter?: Formatter
  /** Horizontal bars (category on Y) read better for ranked breakdowns. */
  layout?: 'horizontal' | 'vertical'
  /** Cycle the categorical palette so buckets are distinguishable; default true. */
  colorByCategory?: boolean
  color?: string
  categoryFormatter?: (value: string) => string
  className?: string
  emptyLabel?: string
}

/** Ranked breakdown bars — spend by model/provider/agent, failures by family. */
export function CategoryBarChart({
  data,
  categoryKey,
  valueKey,
  height = 240,
  valueFormatter = identity,
  layout = 'horizontal',
  colorByCategory = true,
  color,
  categoryFormatter,
  className,
  emptyLabel,
}: CategoryBarChartProps) {
  const theme = useChartTheme()
  const horizontal = layout === 'horizontal'
  return (
    <ChartFrame
      height={height}
      empty={data.length === 0}
      emptyLabel={emptyLabel}
      className={className}
    >
      <BarChart
        data={data}
        layout={horizontal ? 'vertical' : 'horizontal'}
        margin={{ top: 4, right: 16, bottom: 0, left: 8 }}
      >
        <CartesianGrid
          stroke={theme.grid}
          strokeDasharray="2 4"
          horizontal={!horizontal}
          vertical={horizontal}
        />
        {horizontal ? (
          <>
            <XAxis
              type="number"
              tick={{ fill: theme.mutedText, fontSize: AXIS_FONT }}
              tickFormatter={(v: number) => valueFormatter(v)}
              tickLine={false}
              axisLine={false}
            />
            <YAxis
              type="category"
              dataKey={categoryKey}
              tick={{ fill: theme.mutedText, fontSize: AXIS_FONT }}
              tickFormatter={categoryFormatter}
              tickLine={false}
              axisLine={false}
              width={120}
            />
          </>
        ) : (
          <>
            <XAxis
              type="category"
              dataKey={categoryKey}
              tick={{ fill: theme.mutedText, fontSize: AXIS_FONT }}
              tickFormatter={categoryFormatter}
              tickLine={false}
              axisLine={{ stroke: theme.grid }}
              minTickGap={8}
            />
            <YAxis
              type="number"
              tick={{ fill: theme.mutedText, fontSize: AXIS_FONT }}
              tickFormatter={(v: number) => valueFormatter(v)}
              tickLine={false}
              axisLine={false}
              width={56}
            />
          </>
        )}
        <Tooltip
          content={
            makeTooltip(
              theme,
              valueFormatter,
              categoryFormatter,
            ) as unknown as TooltipContent
          }
          cursor={{ fill: theme.grid, fillOpacity: 0.3 }}
        />
        <Bar
          dataKey={valueKey}
          radius={horizontal ? [0, 3, 3, 0] : [3, 3, 0, 0]}
        >
          {data.map((_, i) => (
            <Cell
              key={i}
              fill={
                colorByCategory
                  ? theme.series[i % theme.series.length]
                  : (color ?? theme.accent)
              }
            />
          ))}
        </Bar>
      </BarChart>
    </ChartFrame>
  )
}

// --- donut -------------------------------------------------------------------

export interface DonutDatum {
  key: string
  label: string
  value: number
  color?: string
}

export interface DonutChartProps {
  data: DonutDatum[]
  height?: number
  valueFormatter?: Formatter
  centerLabel?: string
  centerValue?: string
  className?: string
  emptyLabel?: string
}

/** Share-of-total donut — spend share, status mix. Pair with <ChartLegend>. */
export function DonutChart({
  data,
  height = 200,
  valueFormatter = identity,
  centerLabel,
  centerValue,
  className,
  emptyLabel,
}: DonutChartProps) {
  const theme = useChartTheme()
  const colorFor = (d: DonutDatum, i: number) =>
    d.color ?? theme.series[i % theme.series.length]
  const empty = data.length === 0 || data.every((d) => d.value === 0)
  return (
    <div className={cn('relative', className)}>
      <ChartFrame height={height} empty={empty} emptyLabel={emptyLabel}>
        <PieChart>
          <Pie
            data={data}
            dataKey="value"
            nameKey="label"
            innerRadius="62%"
            outerRadius="90%"
            paddingAngle={1.5}
            strokeWidth={0}
          >
            {data.map((d, i) => (
              <Cell key={d.key} fill={colorFor(d, i)} />
            ))}
          </Pie>
          <Tooltip
            content={
              makeTooltip(theme, valueFormatter) as unknown as TooltipContent
            }
          />
        </PieChart>
      </ChartFrame>
      {!empty && (centerValue || centerLabel) ? (
        <div className="pointer-events-none absolute inset-0 flex flex-col items-center justify-center">
          {centerValue ? (
            <span className="font-display text-lg font-semibold tabular-nums text-foreground">
              {centerValue}
            </span>
          ) : null}
          {centerLabel ? (
            <span className="text-xs text-muted-foreground">{centerLabel}</span>
          ) : null}
        </div>
      ) : null}
    </div>
  )
}

/** A small swatch/label/value legend, laid out by the view beside a donut/bars. */
export function ChartLegend({
  items,
  valueFormatter = identity,
  className,
}: {
  items: { key: string; label: string; value: number; color: string }[]
  valueFormatter?: Formatter
  className?: string
}) {
  return (
    <ul className={cn('flex flex-col gap-1.5', className)}>
      {items.map((it) => (
        <li
          key={it.key}
          className="flex items-center justify-between gap-3 text-xs"
        >
          <span className="flex min-w-0 items-center gap-1.5 text-muted-foreground">
            <span
              aria-hidden
              className="size-2 shrink-0 rounded-[2px]"
              style={{ backgroundColor: it.color }}
            />
            <span className="truncate">{it.label}</span>
          </span>
          <span className="shrink-0 font-mono tabular-nums text-foreground">
            {valueFormatter(it.value)}
          </span>
        </li>
      ))}
    </ul>
  )
}

// --- sparkline ---------------------------------------------------------------

/** A tiny axis-less area trend for a scorecard / metric stat. */
export function Sparkline({
  data,
  dataKey,
  color,
  height = 36,
  className,
}: {
  data: Record<string, unknown>[]
  dataKey: string
  color?: string
  height?: number
  className?: string
}) {
  const theme = useChartTheme()
  const stroke = color ?? theme.accent
  if (data.length === 0) return <div style={{ height }} className={className} />
  return (
    // Decorative micro-trend: it is always paired with a textual delta beside it, so
    // it is hidden from AT rather than adding a nameless graphic to the tree.
    <div aria-hidden="true" style={{ width: '100%', height }} className={className}>
      <ResponsiveContainer width="100%" height="100%">
        <AreaChart
          data={data}
          // Decorative + aria-hidden: disable Recharts' keyboard accessibility layer
          // so the SVG is not tab-focusable inside the aria-hidden wrapper (which would
          // be an aria-hidden-focus violation); the trend is conveyed by adjacent text.
          accessibilityLayer={false}
          margin={{ top: 2, right: 0, bottom: 0, left: 0 }}
        >
          <defs>
            <linearGradient id={`spark-${dataKey}`} x1="0" y1="0" x2="0" y2="1">
              <stop offset="0%" stopColor={stroke} stopOpacity={0.3} />
              <stop offset="100%" stopColor={stroke} stopOpacity={0} />
            </linearGradient>
          </defs>
          <Area
            type="monotone"
            dataKey={dataKey}
            stroke={stroke}
            strokeWidth={1.5}
            fill={`url(#spark-${dataKey})`}
            dot={false}
            isAnimationActive={false}
          />
        </AreaChart>
      </ResponsiveContainer>
    </div>
  )
}

// --- radial gauge (SVG) ------------------------------------------------------

/** A 0..100 arc gauge — robustness score, budget consumed, pass-rate. Hand-rolled
 *  SVG so it themes perfectly and reads at a glance. `tone` overrides the
 *  threshold-derived color (e.g. higher-is-better vs lower-is-better). */
export function RadialGauge({
  value,
  size = 120,
  label,
  caption,
  ariaLabel,
  tone,
  className,
}: {
  value: number
  size?: number
  label?: string
  caption?: string
  /** Accessible name for the gauge. Pass the metric (e.g. "Robustness") — the
   * label/caption painted inside the arc are NOT exposed in a role=img name.*/
  ariaLabel?: string
  tone?: 'accent' | 'success' | 'warning' | 'danger'
  className?: string
}) {
  const theme = useChartTheme()
  const clamped = Math.max(0, Math.min(100, value))
  // Accessible name: prefer an explicit ariaLabel (the metric), else fall back to the
  // visible label/caption. Use "%" (read locale-correctly by AT) not the hard-coded
  // English word "percent", and never emit a leading space.
  const namePrefix = (
    ariaLabel ?? [label, caption].filter(Boolean).join(' ')
  ).trim()
  const gaugeName = namePrefix
    ? `${namePrefix}: ${Math.round(clamped)}%`
    : `${Math.round(clamped)}%`
  const stroke = 8
  const radius = (size - stroke) / 2
  const circumference = 2 * Math.PI * radius
  // 270° sweep starting at the bottom-left for a familiar gauge feel.
  const sweep = 0.75
  const dash = (clamped / 100) * circumference * sweep
  const gap = circumference - dash
  const toneColor =
    tone === 'success'
      ? theme.success
      : tone === 'warning'
        ? theme.warning
        : tone === 'danger'
          ? theme.danger
          : tone === 'accent'
            ? theme.accent
            : clamped >= 80
              ? theme.success
              : clamped >= 50
                ? theme.warning
                : theme.danger
  return (
    <div className={cn('inline-flex flex-col items-center', className)}>
      <svg
        width={size}
        height={size}
        viewBox={`0 0 ${size} ${size}`}
        role="img"
        aria-label={gaugeName}
      >
        <g transform={`rotate(135 ${size / 2} ${size / 2})`}>
          <circle
            cx={size / 2}
            cy={size / 2}
            r={radius}
            fill="none"
            stroke={theme.grid}
            strokeWidth={stroke}
            strokeDasharray={`${circumference * sweep} ${circumference}`}
            strokeLinecap="round"
          />
          <circle
            cx={size / 2}
            cy={size / 2}
            r={radius}
            fill="none"
            stroke={toneColor}
            strokeWidth={stroke}
            strokeDasharray={`${dash} ${gap}`}
            strokeLinecap="round"
            style={{ transition: 'stroke-dasharray 400ms var(--ease-out)' }}
          />
        </g>
        <text
          x="50%"
          y="48%"
          textAnchor="middle"
          dominantBaseline="middle"
          className="fill-foreground font-display"
          style={{ fontSize: size * 0.26, fontWeight: 600 }}
        >
          {label ?? Math.round(clamped)}
        </text>
        {caption ? (
          <text
            x="50%"
            y="64%"
            textAnchor="middle"
            dominantBaseline="middle"
            className="fill-muted-foreground"
            style={{ fontSize: size * 0.1 }}
          >
            {caption}
          </text>
        ) : null}
      </svg>
    </div>
  )
}

// --- status / proportion bar (div) ------------------------------------------

export interface StatusSegment {
  key: string
  label: string
  value: number
  color: string
}

/** A single stacked proportion bar — compliance status mix, eval pass/fail. A bar,
 *  not a chart: exact, legible, and it degrades to a thin track when empty. */
export function StatusBar({
  segments,
  height = 10,
  showLegend = true,
  valueFormatter = identity,
  className,
}: {
  segments: StatusSegment[]
  height?: number
  showLegend?: boolean
  valueFormatter?: Formatter
  className?: string
}) {
  const total = segments.reduce((sum, s) => sum + s.value, 0)
  return (
    <div className={cn('flex flex-col gap-2', className)}>
      <div
        className="flex w-full overflow-hidden rounded-full bg-muted"
        style={{ height }}
        role="img"
        aria-label={segments
          .filter((s) => s.value > 0)
          .map((s) => `${s.label}: ${valueFormatter(s.value)}`)
          .join(', ')}
      >
        {total > 0 &&
          segments
            .filter((s) => s.value > 0)
            .map((s) => (
              <div
                key={s.key}
                style={{
                  width: `${(s.value / total) * 100}%`,
                  backgroundColor: s.color,
                }}
                title={`${s.label}: ${s.value}`}
              />
            ))}
      </div>
      {showLegend ? (
        <ul className="flex flex-wrap gap-x-4 gap-y-1">
          {segments.map((s) => (
            <li
              key={s.key}
              className="flex items-center gap-1.5 text-xs text-muted-foreground"
            >
              <span
                aria-hidden
                className="size-2 rounded-[2px]"
                style={{ backgroundColor: s.color }}
              />
              <span>{s.label}</span>
              <span className="font-mono tabular-nums text-foreground">
                {valueFormatter(s.value)}
              </span>
            </li>
          ))}
        </ul>
      ) : null}
    </div>
  )
}
