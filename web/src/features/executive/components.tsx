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
  Coins,
  Minus,
  ScrollText,
  ShieldAlert,
  TrendingDown,
  TrendingUp,
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
      className="group block rounded-lg outline-none transition-[transform,box-shadow] duration-150 ease-out hover:-translate-y-px focus-visible:ring-2 focus-visible:ring-ring print:transform-none print:hover:translate-y-0 [&_*]:cursor-pointer"
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

export function KpiTiles({
  cost,
  usage,
  risk,
  compliance,
}: {
  cost?: CostKpi | null
  usage?: UsageKpi | null
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
        <LinkTile to="/inventory">
          <MetricStat
            icon={<Boxes />}
            label={t('pillars.usage')}
            value={formatInt(usage.activeAgents)}
            caption={t('pillars.usageCaption', {
              live: formatInt(usage.liveActive),
              entities: formatInt(usage.totalEntities),
            })}
            trend={
              usage.silentEvasion > 0 ? (
                <span className="inline-flex items-center gap-1 text-xs text-warning">
                  <Activity className="size-3.5" />
                  {t('pillars.silentEvasion', { count: usage.silentEvasion })}
                </span>
              ) : undefined
            }
          />
        </LinkTile>
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
        valueFormatter={(v) => formatMicroUsd(v, { compact: true })}
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
