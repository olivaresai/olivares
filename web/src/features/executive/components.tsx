// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Executive dashboard — PURE presentational pieces. They take the rolled-up KPIs
// (derive.ts) as props and render them with the shared design-system kit: MetricStat
// headlines, themed charts, the honesty notices. No fetching, no auth — so they are
// trivially testable with fixtures and identical on screen and in the printed report.
// Every pillar tile is a drill-down link to its operational view (a leadership reader
// can always go deeper), and every coverage limit the rollup carries is shown.
import { useMemo, type ReactNode } from 'react'
import { Link } from '@tanstack/react-router'
import {
  Activity,
  ArrowRight,
  Boxes,
  ChevronRight,
  Coins,
  Minus,
  ScrollText,
  ShieldAlert,
  TrendingDown,
  TrendingUp,
  TriangleAlert,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'
import { Badge } from '@/components/ui/badge'
import {
  CategoryBarChart,
  DonutChart,
  ChartLegend,
  RadialGauge,
  Sparkline,
  StatusBar,
  TrendChart,
  useChartTheme,
} from '@/components/charts'
import {
  CaveatNotice,
  DisclaimerNote,
  IntelNotice,
  MetricStat,
  RiskTierBadge,
  SectionCard,
  SeverityBadge,
  StatGrid,
  TruncatedNotice,
} from '@/features/_intel'
import {
  formatInt,
  formatMicroUsd,
  formatPercent,
  formatTokens,
  formatDayKey,
} from '@/lib/format'
import type { SpendResponse } from '@/features/finops/types'
import {
  SEVERITY_ORDER,
  type ComplianceKpi,
  type CostKpi,
  type HealthKpi,
  type RiskKpi,
  type UsageKpi,
} from './derive'
// These tiles are reused by the HOME view (route `/`, the front door), whose chunk
// never imports the executive view — so `useTranslation('executive')` below had no
// bundle there and `DeltaCaption` printed `cost.deltaUp` verbatim on the first screen
// after login. home.test.tsx even imported this namespace by hand, which made the
// test pass over a defect production still had. The namespace travels with the
// components that translate.
import './i18n'

// --- small shared bits -------------------------------------------------------

/** A header "drill down" link to the operational view. Hidden in the printed
 *  report (a PDF has nowhere to navigate). */
export function DrillLink({
  to,
  children,
}: {
  to: string
  children: ReactNode
}) {
  return (
    <Link
      // The feature registry IS the route table, so these paths are always valid.
      to={to as never}
      className="inline-flex items-center gap-1 rounded-md px-1.5 py-0.5 text-xs font-medium text-accent-text outline-none transition-colors hover:bg-accent-soft focus-visible:ring-2 focus-visible:ring-ring print:hidden [&_svg]:size-3.5"
    >
      {children}
      <ArrowRight />
    </Link>
  )
}

/** A KPI tile that links to its operational view, with a calm hover lift. The link
 *  is inert in print (no chrome, no pointer) so the report reads as a flat report.
 *  Exported so the home overview composes the SAME tile chrome — one source of
 *  truth for the drill-down tile, never a second copy. */
export function LinkTile({
  to,
  children,
}: {
  to: string
  children: ReactNode
}) {
  return (
    <Link
      to={to as never}
      className="group block min-w-0 rounded-lg outline-none transition-[transform,box-shadow] duration-150 ease-out hover:-translate-y-px focus-visible:ring-2 focus-visible:ring-ring print:transform-none print:hover:translate-y-0 [&_*]:cursor-pointer"
    >
      {children}
    </Link>
  )
}

/** Period-over-period delta caption (rising spend is neutral/warning, never green).
 *  Exported so the home overview's spend tile renders the SAME delta semantics. */
export function DeltaCaption({ pct }: { pct: number | null }) {
  const { t } = useTranslation('executive')
  if (pct === null) return null
  const up = pct > 1
  const down = pct < -1
  const Icon = up ? TrendingUp : down ? TrendingDown : Minus
  const label = up
    ? t('cost.deltaUp', { pct: formatPercent(Math.abs(pct), { digits: 0 }) })
    : down
      ? t('cost.deltaDown', {
          pct: formatPercent(Math.abs(pct), { digits: 0 }),
        })
      : t('cost.deltaFlat')
  // Rising spend is not "good" — keep it neutral/warning, never green/red moralized.
  return (
    <span
      className={cn(
        'inline-flex items-center gap-1 text-xs',
        up ? 'text-warning' : 'text-muted-foreground',
      )}
    >
      <Icon className="size-3.5" />
      {label}
    </span>
  )
}

// --- headline pillars (cost / usage / risk / compliance) ---------------------

/**
 * WHICH SOURCE IS PARTIAL — the floor marker for ONE NAMED source, rendered beside the
 * figures it qualifies.
 *
 * `TruncatedNotice` is the shared strip for this state and stays the right thing where
 * one source feeds one card (the spend section below, the Inventory view). It takes no
 * source, and on a KPI that JOINS sources that silence is the defect: the executive
 * usage tile reads the inventory summary AND the live sessions page, and the front door
 * shows both on neighbouring tiles. One marker describes ONE source — the one it names —
 * and establishes nothing about any other. It composes shared `intel` strings with the
 * source's own name, and stays a caption line rather than a strip because a MetricStat
 * caption is a <span> and the strip is a <div>.
 *
 * THE LINE IS COMPACT ON PURPOSE, AND THE FULL SENTENCE IS NOT LOST. The first accepted
 * version put the whole mechanism sentence in this caption — right in principle (a
 * native `title` is not how keyboard, touch or assistive-technology users read a
 * caption), and wrong as a finish: on the executive usage tile two such sentences made
 * the card far taller than its neighbours, and on a 390px front door the two narrow
 * columns turned dense. So the caption keeps what a reader scans — the source, the
 * "partial" fact, and a few translated words naming the mechanism — and the complete
 * sentence moves to `PartialCoverageDisclosure`, a native disclosure that sits OUTSIDE
 * the tile link (a button or <details> inside a tile-wide <a> is invalid markup, and
 * this file does not do it). Nothing is title-only, nothing is hover-only, and the tile
 * link is unchanged: no extra control and no extra focus stop inside it.
 *
 * TWO MECHANISMS, TWO SENTENCES, and the caller names which one it has (`kind`):
 *  · `aggregate` — an aggregate hit its scan ceiling (`truncated: true`; Inventory).
 *    Brief `notices.truncatedBrief`; full sentence `notices.truncatedHint`, the one
 *    `TruncatedNotice` shows.
 *  · `page`      — a list endpoint returned ONE page and there were rows beyond it
 *    (`has_more: true`; the live Sessions page). Brief `notices.listTruncatedBrief`;
 *    full sentence `notices.listTruncatedHint`, the one `ListTruncationBadge` shows:
 *    one page, not the whole set, and the total cannot be inferred from the rows
 *    loaded. The scan-ceiling sentence would describe a mechanism that source does not
 *    have.
 * The visible label is the same for both — partial data, named — because that is the
 * fact a reader acts on; the mechanism is the brief, and it is on screen.
 *
 * It NEVER hides the count: a floor is a real answer. It qualifies a figure that
 * exists, so the caller mounts it only while that figure is the current, permitted,
 * successful one — a retired figure takes its marker with it.
 */
export function PartialSourceNote({
  source,
  kind = 'aggregate',
  testId,
}: {
  /** The source's own name, from the caller's vocabulary (e.g. `nav:items.inventory`). */
  source: string
  /** Which honesty seam the source carries — see above. Defaults to the aggregate's
   *  scan ceiling, the case this marker was introduced for. */
  kind?: PartialSourceKind
  testId: string
}) {
  const { t } = useTranslation('intel')
  const brief =
    kind === 'page'
      ? t('notices.listTruncatedBrief')
      : t('notices.truncatedBrief')
  return (
    <span
      className="block min-w-0 text-pretty break-words"
      data-testid={testId}
    >
      {source} —{' '}
      <span className="font-medium text-warning">{t('notices.truncated')}</span>
      {' · '}
      {brief}
    </span>
  )
}

export type PartialSourceKind = 'aggregate' | 'page'

/** ONE SOURCE THAT ANSWERED INCOMPLETELY, as the container states it: the name it
 *  goes by on this page, the honesty seam it carries, and the test id of its caption
 *  line. The same value feeds the compact caption inside the tile link AND the full
 *  explanation in the disclosure outside it, so the two can never name different
 *  sources or different mechanisms. */
export interface PartialSource {
  source: string
  kind: PartialSourceKind
  /** Marks the compact caption line. The full explanation row in the screen
   *  disclosure is `${testId}-explanation`; its print-only twin is
   *  `${testId}-print-explanation`. */
  testId: string
}

/** THE PARTIAL COVERAGE OF ONE TILE: every source behind it that answered incompletely,
 *  in caption order, plus the test id of the disclosure that explains them. The
 *  print-only copy of the same rows is `${testId}-print`. */
export interface PartialCoverage {
  sources: readonly PartialSource[]
  testId: string
}

/**
 * THE ROWS OF THE COMPLETE EXPLANATION — ONE RENDERER, TWO PLACES. The same warning
 * strip `TruncatedNotice` renders, one row per source, each row naming its source and
 * carrying the SAME full sentence the caption used to hold (`notices.truncatedHint`
 * for an aggregate at its scan ceiling, `notices.listTruncatedHint` for a one-page
 * list) — a definition list, so the source→sentence pairing is explicit rather than
 * positional. The sentences are the existing seven-language strings, unchanged.
 *
 * `PartialCoverageDisclosure` puts these rows behind a native <details> for the
 * screen; `PartialCoveragePrint` lays the same rows out for the printed report. Both
 * are fed the same `sources` array by `CoverageLinkTile`, so the two copies can never
 * name different sources, different mechanisms or a source the other has retired —
 * and the print copy only ever says what the screen disclosure would say when open.
 * The row test ids differ (`-explanation` on screen, `-print-explanation` in the print
 * copy) so the two copies are distinguishable and never duplicate an id.
 */
function PartialCoverageRows({
  sources,
  print = false,
  className,
}: {
  sources: readonly PartialSource[]
  /** Marks the rows as the print copy (`${testId}-print-explanation`). */
  print?: boolean
  className?: string
}) {
  const { t } = useTranslation('intel')
  return (
    <IntelNotice tone="warning" icon={<TriangleAlert />} className={className}>
      <dl className="flex flex-col gap-1.5">
        {sources.map((s) => (
          <div
            key={s.testId}
            className="text-pretty break-words"
            data-testid={`${s.testId}-${print ? 'print-' : ''}explanation`}
          >
            <dt className="inline font-medium text-warning">
              {s.source} — {t('notices.truncated')}
            </dt>{' '}
            <dd className="inline text-muted-foreground">
              {s.kind === 'page'
                ? t('notices.listTruncatedHint')
                : t('notices.truncatedHint')}
            </dd>
          </div>
        ))}
      </dl>
    </IntelNotice>
  )
}

/**
 * THE COMPLETE EXPLANATION, ONE DISCLOSURE PER TILE, OUTSIDE THE TILE LINK.
 *
 * A native <details>/<summary> (the control the license panel already uses for its
 * help text): the summary is a real tab stop that Enter and Space toggle, a tap toggles
 * it, and it needs no script or tooltip layer — a screen reader reads the summary as an
 * expandable control and the body as ordinary text once open. It is rendered as a
 * SIBLING of the tile link, never inside it: interactive content inside an <a> is
 * invalid, and the tile keeps its full-card hit target and single focus stop.
 *
 * The body is `PartialCoverageRows` — the one renderer of the complete explanation,
 * shared with the print copy below.
 *
 * THE PRINTED REPORT DOES NOT RELY ON THIS ELEMENT. A closed <details> hides its body
 * in print as on screen, and the only CSS that can reveal it from outside
 * (`::details-content`) is newer than the toolchain's own browser floor — so this
 * whole disclosure is simply hidden in print (`print:hidden`, like every other control
 * the report drops), and `PartialCoveragePrint` prints the same rows from outside it.
 * Nothing here depends on the details' open state or on any pseudo-element support.
 */
export function PartialCoverageDisclosure({
  sources,
  testId,
}: {
  sources: readonly PartialSource[]
  testId: string
}) {
  const { t } = useTranslation('executive')
  return (
    <details
      className="group min-w-0 text-xs print:hidden"
      data-testid={testId}
    >
      <summary className="inline-flex max-w-full cursor-pointer list-none items-center gap-1 rounded-sm px-1 py-0.5 text-muted-foreground outline-none transition-colors select-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring [&::-webkit-details-marker]:hidden">
        <ChevronRight
          className="size-3.5 shrink-0 transition-transform group-open:rotate-90"
          aria-hidden
        />
        <span className="min-w-0 text-pretty">{t('pillars.partialWhy')}</span>
      </summary>
      <PartialCoverageRows sources={sources} className="mt-1.5" />
    </details>
  )
}

/**
 * THE SAME EXPLANATION, LAID OUT FOR THE PRINTED REPORT ONLY. Export PDF prints what
 * is on screen (`window.print()`, see report.tsx), and a closed disclosure would print
 * nothing but its summary — so this block, a sibling OUTSIDE the <details>, carries the
 * identical rows through the same `PartialCoverageRows` renderer. On screen it is
 * `display: none` (not rendered, not focusable, not in the accessibility tree — the
 * same `hidden print:block` pattern as the report cover header); in print it is a
 * plain block with no control. It takes the very `sources` array the disclosure takes,
 * from the same container that lists a source only while its answer is current and
 * permitted, so a retired source leaves the print copy the moment it leaves the
 * caption and the disclosure: no stale row, no source the reader may not see.
 */
export function PartialCoveragePrint({
  sources,
  testId,
}: {
  sources: readonly PartialSource[]
  testId: string
}) {
  return (
    <div className="hidden min-w-0 text-xs print:block" data-testid={testId}>
      <PartialCoverageRows sources={sources} print />
    </div>
  )
}

/**
 * A `LinkTile` plus, when the tile's figures are a floor, its coverage disclosure
 * (screen) and the print-only copy of the same rows, both composed OUTSIDE the link:
 * the three are siblings in one grid cell, and the two explanations are fed the one
 * `partial.sources` array. With nothing partial it is exactly `LinkTile`, so a
 * complete tile keeps the previous rendering. Exported so the home overview composes
 * the SAME cell, never a second copy.
 */
export function CoverageLinkTile({
  to,
  partial,
  children,
}: {
  to: string
  partial?: PartialCoverage
  children: ReactNode
}) {
  if (!partial || partial.sources.length === 0) {
    return <LinkTile to={to}>{children}</LinkTile>
  }
  return (
    <div className="flex min-w-0 flex-col gap-1">
      <LinkTile to={to}>{children}</LinkTile>
      <PartialCoverageDisclosure
        sources={partial.sources}
        testId={partial.testId}
      />
      <PartialCoveragePrint
        sources={partial.sources}
        testId={`${partial.testId}-print`}
      />
    </div>
  )
}

export function KpiTiles({
  cost,
  usage,
  usageHints,
  usagePartial,
  usageScope,
  risk,
  compliance,
}: {
  cost?: CostKpi | null
  usage?: UsageKpi | null
  /** WHY A USAGE HALF IS UNKNOWN — one line per half the container could not
   *  establish (restricted to the role, or could not load), rendered under the
   *  caption beside the "—" it explains. The container derives it from the real
   *  query state; this tile only prints it. Absent when both halves are current. */
  usageHints?: ReactNode
  /** WHICH USAGE SOURCE IS ONLY A FLOOR — one entry per half the engine returned
   *  incomplete: the live sessions page that had rows beyond it (`has_more`), and/or
   *  the inventory summary that hit its scan ceiling (`truncated`). Each entry becomes
   *  a compact caption line under the counts it qualifies AND a row of the full
   *  explanation in the disclosure beneath the tile, outside its link. Each names its
   *  own source, so neither describes the other or establishes its completeness. The
   *  container lists a half only while its answer is current: nothing to qualify,
   *  nothing rendered. */
  usagePartial?: PartialCoverage
  /** WHAT THE USAGE TILE'S FIGURES COVER — a scope disclosure the container supplies
   *  (both reads behind this tile, the live sessions page and the inventory summary,
   *  are tenant-wide: neither endpoint takes a workspace selector). Rendered as a line
   *  under the caption, attached to the figures it qualifies; it describes coverage,
   *  never a grant and never completeness. */
  usageScope?: ReactNode
  risk?: RiskKpi | null
  compliance?: ComplianceKpi | null
}) {
  const { t } = useTranslation(['executive', 'nav'])
  const theme = useChartTheme()
  return (
    <StatGrid>
      {cost ? (
        <LinkTile to="/finops">
          <MetricStat
            icon={<Coins />}
            label={t('pillars.cost')}
            value={formatMicroUsd(cost.totalMicroUsd, { compact: true })}
            caption={t('pillars.costCaption')}
            trend={
              cost.trend.length > 1 ? (
                <div className="flex items-center justify-between gap-2">
                  <Sparkline
                    data={cost.trend}
                    dataKey="cost"
                    color={theme.accent}
                    className="max-w-[60%]"
                  />
                  <DeltaCaption pct={cost.deltaPct} />
                </div>
              ) : (
                <DeltaCaption pct={cost.deltaPct} />
              )
            }
          />
        </LinkTile>
      ) : null}

      {usage ? (
        <CoverageLinkTile to="/inventory" partial={usagePartial}>
          <MetricStat
            icon={<Boxes />}
            label={t('pillars.usage')}
            value={formatInt(usage.activeAgents)}
            caption={
              <>
                {/* `formatInt` prints the em-dash for a half the rollup did not
                    establish (`null`) — the same mark every intel surface uses for
                    "not known" — and a real 0 for a successful empty answer. */}
                {t('pillars.usageCaption', {
                  live: formatInt(usage.liveActive),
                  entities: formatInt(usage.totalEntities),
                })}
                {usageHints}
                {usagePartial?.sources.map((s) => (
                  <PartialSourceNote
                    key={s.testId}
                    source={s.source}
                    kind={s.kind}
                    testId={s.testId}
                  />
                ))}
                {usageScope ? (
                  <span className="block">{usageScope}</span>
                ) : null}
              </>
            }
            trend={
              usage.silentEvasion !== null && usage.silentEvasion > 0 ? (
                <span className="inline-flex items-center gap-1 text-xs text-warning">
                  <Activity className="size-3.5" />
                  {t('pillars.silentEvasion', { count: usage.silentEvasion })}
                </span>
              ) : undefined
            }
          />
        </CoverageLinkTile>
      ) : null}

      {risk ? (
        <LinkTile to="/security">
          <MetricStat
            icon={<ShieldAlert />}
            label={t('pillars.risk')}
            value={formatInt(risk.openFindings)}
            caption={t('pillars.riskCaption', {
              count: formatInt(risk.criticalHigh),
            })}
            tone={
              risk.criticalHigh > 0
                ? 'danger'
                : risk.openFindings > 0
                  ? 'warning'
                  : 'success'
            }
            trend={<SeverityRow bySeverity={risk.bySeverity} compact />}
          />
        </LinkTile>
      ) : null}

      {compliance ? (
        <LinkTile to="/compliance">
          <MetricStat
            icon={<ScrollText />}
            label={t('pillars.compliance')}
            value={
              compliance.coveredPct === null
                ? '—'
                : formatPercent(compliance.coveredPct, { digits: 0 })
            }
            caption={t('pillars.complianceCaption', {
              gap: formatInt(compliance.gap),
              unmapped: formatInt(compliance.unmapped),
            })}
            trend={
              <ComplianceMixBar
                compliance={compliance}
                height={6}
                showLegend={false}
              />
            }
          />
        </LinkTile>
      ) : null}
    </StatGrid>
  )
}

// --- cost section ------------------------------------------------------------

export function SpendSection({ cost }: { cost: CostKpi }) {
  const { t, i18n } = useTranslation('executive')
  // Ticks may abbreviate large sums; keyboard/pointer details keep micro-USD
  // precision and the locale's complete currency label.
  const spendCurrency = useMemo(
    () =>
      new Intl.NumberFormat(i18n.language, {
        style: 'currency',
        currency: 'USD',
        minimumFractionDigits: 2,
        maximumFractionDigits: 6,
      }),
    [i18n.language],
  )
  return (
    <SectionCard
      title={t('cost.trendTitle')}
      description={t('cost.trendDescription')}
      actions={<DrillLink to="/finops">{t('cost.title')}</DrillLink>}
    >
      {cost.truncated ? <TruncatedNotice className="mb-3" /> : null}
      <div className="mb-4 grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        <MiniStat
          label={t('pillars.cost')}
          value={formatMicroUsd(cost.totalMicroUsd, { compact: true })}
        >
          <DeltaCaption pct={cost.deltaPct} />
        </MiniStat>
        {cost.projectedMicroUsd !== null ? (
          <MiniStat
            label={t('cost.forecast')}
            value={formatMicroUsd(cost.projectedMicroUsd, { compact: true })}
            tone={cost.projectedOver ? 'warning' : undefined}
          >
            <span className="text-xs text-muted-foreground">
              {t('cost.atRunRate')}
            </span>
          </MiniStat>
        ) : null}
        <MiniStat
          label={t('cost.tokens')}
          value={formatTokens(cost.inputTokens + cost.outputTokens)}
        />
        {cost.activeModels !== null ? (
          <MiniStat
            label={t('cost.models')}
            value={formatInt(cost.activeModels)}
          />
        ) : (
          <MiniStat label={t('cost.samples')} value={formatInt(cost.samples)} />
        )}
      </div>
      <TrendChart
        data={cost.trend}
        xKey="key"
        series={[{ key: 'cost', label: t('cost.seriesCost') }]}
        valueFormatter={(v) =>
          formatMicroUsd(v, { compact: true, locale: i18n.language })
        }
        tooltipValueFormatter={(v) => spendCurrency.format(v / 1_000_000)}
        yAxis={{ width: 'auto', allowDecimals: false }}
        xTickFormatter={(k) => formatDayKey(k, i18n.language)}
        height={240}
      />
      {cost.projectedMicroUsd !== null ? (
        <DisclaimerNote className="mt-3" text={t('cost.runRateNote')} />
      ) : null}
    </SectionCard>
  )
}

/** Pure spend-by-dimension breakdown (the org/team/project summary). The dimension
 *  Select + query live in the container; this renders the chosen SpendResponse. */
export function SpendBreakdownChart({ spend }: { spend: SpendResponse }) {
  const { t } = useTranslation('executive')
  const theme = useChartTheme()
  const top = useMemo(
    () =>
      [...spend.buckets]
        .sort((a, b) => b.cost_micro_usd - a.cost_micro_usd)
        .slice(0, 8)
        .map((b, i) => ({
          ...b,
          color: theme.series[i % theme.series.length],
        })),
    [spend.buckets, theme.series],
  )
  if (top.length === 0) {
    return (
      <p className="py-6 text-center text-sm text-muted-foreground">
        {t('cost.noAttribution')}
      </p>
    )
  }
  return (
    <div className="grid gap-4 lg:grid-cols-[1.5fr_1fr]">
      <CategoryBarChart
        data={top}
        categoryKey="key"
        valueKey="cost_micro_usd"
        valueFormatter={(v) => formatMicroUsd(v, { compact: true })}
        height={Math.max(160, top.length * 30 + 24)}
      />
      <div className="flex flex-col gap-3">
        <DonutChart
          data={top.map((b) => ({
            key: b.key,
            label: b.key || '—',
            value: b.cost_micro_usd,
            color: b.color,
          }))}
          valueFormatter={(v) => formatMicroUsd(v, { compact: true })}
          centerValue={formatMicroUsd(spend.total_micro_usd, { compact: true })}
          centerLabel={t('cost.seriesCost')}
          height={180}
        />
        <ChartLegend
          items={top.map((b) => ({
            key: b.key,
            label: b.key || '—',
            value: b.cost_micro_usd,
            color: b.color,
          }))}
          valueFormatter={(v) => formatMicroUsd(v, { compact: true })}
        />
      </div>
    </div>
  )
}

// --- risk section ------------------------------------------------------------

/** A row of severity chips with counts (critical first). `compact` drops zeros. */
export function SeverityRow({
  bySeverity,
  compact = false,
}: {
  bySeverity: RiskKpi['bySeverity']
  compact?: boolean
}) {
  const entries = SEVERITY_ORDER.filter((s) => !compact || bySeverity[s] > 0)
  if (entries.length === 0) return null
  return (
    <div className="flex flex-wrap items-center gap-1.5">
      {entries.map((s) => (
        <span key={s} className="inline-flex items-center gap-1">
          <span className="font-mono text-xs tabular-nums text-foreground">
            {bySeverity[s]}
          </span>
          <SeverityBadge severity={s} />
        </span>
      ))}
    </div>
  )
}

export function RiskSection({ risk }: { risk: RiskKpi }) {
  const { t } = useTranslation('executive')
  return (
    <SectionCard
      title={t('risk.title')}
      description={t('risk.description')}
      actions={<DrillLink to="/security">{t('risk.title')}</DrillLink>}
    >
      <div className="grid gap-4 md:grid-cols-3">
        {/* findings */}
        <div className="flex flex-col gap-3 rounded-lg border border-border bg-surface p-4">
          <span className="text-xs font-medium uppercase tracking-wide text-muted-foreground">
            {t('risk.openFindings')}
          </span>
          <span className="font-display text-2xl font-semibold tabular-nums text-foreground">
            {formatInt(risk.openFindings)}
          </span>
          {risk.openFindings > 0 ? (
            <SeverityRow bySeverity={risk.bySeverity} />
          ) : (
            <span className="text-xs text-muted-foreground">
              {t('risk.noFindings')}
            </span>
          )}
        </div>

        {/* robustness */}
        <div className="flex flex-col items-center justify-center gap-2 rounded-lg border border-border bg-surface p-4 text-center">
          <span className="self-start text-xs font-medium uppercase tracking-wide text-muted-foreground">
            {t('risk.robustness')}
          </span>
          {risk.robustness.score !== null ? (
            <>
              <RadialGauge
                value={risk.robustness.score}
                size={108}
                caption="/100"
                ariaLabel={t('risk.robustness')}
              />
              <span className="text-xs text-muted-foreground">
                {t('risk.robustnessCaption')}
              </span>
            </>
          ) : (
            <div className="flex flex-1 flex-col items-center justify-center gap-2 py-3">
              <Badge variant="warning">{t('risk.robustnessPending')}</Badge>
              <span className="max-w-[14rem] text-xs leading-relaxed text-muted-foreground">
                {risk.robustness.status
                  ? t('risk.robustnessPendingHint')
                  : t('risk.noRuns')}
              </span>
            </div>
          )}
        </div>

        {/* access drift */}
        <div className="flex flex-col gap-3 rounded-lg border border-border bg-surface p-4">
          <span className="text-xs font-medium uppercase tracking-wide text-muted-foreground">
            {t('risk.drift')}
          </span>
          <DriftRow
            label={t('risk.driftFirm')}
            value={risk.drift.unexpectedFirm}
            tone="danger"
          />
          <DriftRow
            label={t('risk.driftPending')}
            value={risk.drift.unexpectedPending}
            tone="warning"
          />
          <DriftRow
            label={t('risk.driftUnused')}
            value={risk.drift.unused}
            tone="muted"
          />
          <DrillLink to="/access-map">{t('risk.drift')}</DrillLink>
        </div>
      </div>
      {risk.coverageLimited > 0 ? (
        <CaveatNotice className="mt-4">{t('risk.coverageNote')}</CaveatNotice>
      ) : null}
    </SectionCard>
  )
}

function DriftRow({
  label,
  value,
  tone,
}: {
  label: string
  value: number
  tone: 'danger' | 'warning' | 'muted'
}) {
  const color =
    value === 0
      ? 'text-muted-foreground'
      : tone === 'danger'
        ? 'text-danger'
        : tone === 'warning'
          ? 'text-warning'
          : 'text-foreground'
  return (
    <div className="flex items-baseline justify-between gap-2">
      <span className="text-sm text-muted-foreground">{label}</span>
      <span
        className={cn('font-mono text-lg font-semibold tabular-nums', color)}
      >
        {formatInt(value)}
      </span>
    </div>
  )
}

// --- compliance section ------------------------------------------------------

/** The satisfied/by_design/partial/gap/unmapped proportion bar, colored to match the
 *  ControlStatusBadge tokens (never collapses by_design into satisfied — docs/SECURITY-HARDENING.md).*/
export function ComplianceMixBar({
  compliance,
  height = 10,
  showLegend = true,
}: {
  compliance: Pick<
    ComplianceKpi,
    'satisfied' | 'byDesign' | 'partial' | 'gap' | 'unmapped'
  >
  height?: number
  showLegend?: boolean
}) {
  const { t } = useTranslation('executive')
  const theme = useChartTheme()
  const segments = [
    {
      key: 'satisfied',
      label: t('compliance.satisfied'),
      value: compliance.satisfied,
      color: theme.success,
    },
    {
      key: 'by_design',
      label: t('compliance.byDesign'),
      value: compliance.byDesign,
      color: theme.info,
    },
    {
      key: 'partial',
      label: t('compliance.partial'),
      value: compliance.partial,
      color: theme.warning,
    },
    {
      key: 'gap',
      label: t('compliance.gap'),
      value: compliance.gap,
      color: theme.danger,
    },
    {
      key: 'unmapped',
      label: t('compliance.unmapped'),
      value: compliance.unmapped,
      color: theme.slate,
    },
  ]
  return (
    <StatusBar
      segments={segments}
      height={height}
      showLegend={showLegend}
      valueFormatter={(v) => formatInt(v)}
    />
  )
}

export function ComplianceSection({
  compliance,
}: {
  compliance: ComplianceKpi
}) {
  const { t } = useTranslation('executive')
  const tiers = ['unacceptable', 'high', 'limited', 'minimal'] as const
  return (
    <SectionCard
      title={t('compliance.title')}
      description={t('compliance.description')}
      actions={<DrillLink to="/compliance">{t('compliance.title')}</DrillLink>}
    >
      <div className="grid gap-5 lg:grid-cols-[1fr_1.4fr]">
        <div className="flex flex-col gap-2">
          <span className="text-xs font-medium uppercase tracking-wide text-muted-foreground">
            {t('compliance.coverage')}
          </span>
          <span className="font-display text-3xl font-semibold tabular-nums text-foreground">
            {compliance.coveredPct === null
              ? '—'
              : formatPercent(compliance.coveredPct, { digits: 0 })}
          </span>
          <span className="text-xs text-muted-foreground">
            {t('compliance.ofControls', {
              total: formatInt(compliance.total),
              frameworks: formatInt(compliance.frameworks.length),
            })}
          </span>
          <ComplianceMixBar compliance={compliance} />
          {Object.values(compliance.riskTiers).some((n) => n > 0) ? (
            <div className="mt-2 flex flex-col gap-1.5">
              <span className="text-xs font-medium uppercase tracking-wide text-muted-foreground">
                {t('compliance.riskTiers')}
              </span>
              <div className="flex flex-wrap items-center gap-1.5">
                {tiers
                  .filter((tier) => compliance.riskTiers[tier] > 0)
                  .map((tier) => (
                    <span key={tier} className="inline-flex items-center gap-1">
                      <span className="font-mono text-xs tabular-nums text-foreground">
                        {compliance.riskTiers[tier]}
                      </span>
                      <RiskTierBadge tier={tier} />
                    </span>
                  ))}
              </div>
            </div>
          ) : null}
        </div>

        <div className="flex flex-col gap-3">
          <span className="text-xs font-medium uppercase tracking-wide text-muted-foreground">
            {t('compliance.frameworks')}
          </span>
          {compliance.frameworks.length === 0 ? (
            <p className="text-sm text-muted-foreground">
              {t('compliance.noFrameworks')}
            </p>
          ) : (
            compliance.frameworks.map((fw) => (
              <div key={fw.framework} className="flex flex-col gap-1.5">
                <div className="flex items-baseline justify-between gap-2">
                  <span className="text-sm font-medium text-foreground">
                    {fw.name}
                  </span>
                  <span className="font-mono text-xs text-muted-foreground">
                    {fw.version}
                  </span>
                </div>
                <ComplianceMixBar
                  compliance={{
                    satisfied: fw.summary.satisfied,
                    byDesign: fw.summary.by_design,
                    partial: fw.summary.partial,
                    gap: fw.summary.gap,
                    unmapped: fw.summary.unmapped,
                  }}
                  height={8}
                  showLegend={false}
                />
              </div>
            ))
          )}
        </div>
      </div>
      <DisclaimerNote className="mt-4" text={compliance.disclaimer} />
    </SectionCard>
  )
}

// --- reliability section -----------------------------------------------------

export function ReliabilitySection({ health }: { health: HealthKpi }) {
  const { t } = useTranslation('executive')
  const theme = useChartTheme()
  const hasData = health.total > 0
  const segments = [
    {
      key: 'healthy',
      label: t('reliability.healthy'),
      value: health.healthy,
      color: theme.success,
    },
    {
      key: 'degraded',
      label: t('reliability.degraded'),
      value: health.degraded,
      color: theme.warning,
    },
    {
      key: 'down',
      label: t('reliability.down'),
      value: health.down,
      color: theme.danger,
    },
    {
      key: 'unknown',
      label: t('reliability.unknown'),
      value: health.unknown,
      color: theme.slate,
    },
  ]
  return (
    <SectionCard
      title={t('reliability.title')}
      description={t('reliability.description')}
      actions={<DrillLink to="/health">{t('reliability.title')}</DrillLink>}
    >
      {hasData ? (
        <div className="grid gap-4 lg:grid-cols-[1.6fr_1fr]">
          <div className="flex flex-col justify-center">
            <StatusBar
              segments={segments}
              height={12}
              valueFormatter={(v) => formatInt(v)}
            />
          </div>
          <div className="grid grid-cols-2 gap-3">
            <MiniStat
              label={t('reliability.slaBreaches')}
              value={formatInt(health.slaBreaches)}
              tone={health.slaBreaches > 0 ? 'danger' : undefined}
            />
            <MiniStat
              label={t('reliability.incidents')}
              value={formatInt(health.openIncidents)}
              tone={health.openIncidents > 0 ? 'warning' : undefined}
            />
          </div>
        </div>
      ) : (
        <p className="py-6 text-center text-sm text-muted-foreground">
          {t('reliability.noData')}
        </p>
      )}
      <DisclaimerNote className="mt-3" text={t('reliability.nowNote')} />
    </SectionCard>
  )
}

// --- tiny stat (used by several sections) ------------------------------------

function MiniStat({
  label,
  value,
  tone,
  children,
}: {
  label: string
  value: ReactNode
  tone?: 'warning' | 'danger'
  children?: ReactNode
}) {
  return (
    <div className="flex flex-col gap-0.5 rounded-lg border border-border bg-surface p-3">
      <span className="text-xs font-medium uppercase tracking-wide text-muted-foreground">
        {label}
      </span>
      <span
        className={cn(
          'font-display text-lg font-semibold tabular-nums',
          tone === 'danger'
            ? 'text-danger'
            : tone === 'warning'
              ? 'text-warning'
              : 'text-foreground',
        )}
      >
        {value}
      </span>
      {children}
    </div>
  )
}
