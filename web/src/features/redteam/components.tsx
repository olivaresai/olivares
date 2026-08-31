// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Red-team presentational pieces — PURE (data in, UI out): no fetching, no auth, so
// they are trivially testable with fixtures and reused by the container. They encode
// the module's honesty rules: a `registered`
// target is NOT consent; a `degraded` run (all probes skipped — no sandbox) is
// shown as "pending sandbox", NEVER as a green pass; `detail_hash` is a FINGERPRINT,
// never an attack payload; the score gauge reflects passed/(passed+failed).
import { useMemo } from 'react'
import { ShieldQuestion } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { CategoryBarChart, RadialGauge } from '@/components/charts'
import { AccessibleChart } from '@/components/data/accessible-chart'
import {
  CaveatNotice,
  ConsentBadge,
  HashChip,
  MetricStat,
  OutcomeBadge,
  SectionCard,
  SeverityBadge,
  StatGrid,
} from '@/features/_intel'
import { formatDateTime, formatInt, humanize } from '@/lib/format'
import type { CatalogResponse, ProbeResult, Run, Target } from './types'

// --- §8 targets (the consent surface) ----------------------------------------

export function TargetsTable({
  targets,
  canAdmin,
  onAuthorize,
  onLaunch,
}: {
  targets: Target[]
  /** Whether the principal holds `redteam:target:admin` (gates consent actions). */
  canAdmin: boolean
  /** Open the authorize/revoke action for a target. */
  onAuthorize?: (target: Target) => void
  /** Launch a run against an AUTHORIZED target. */
  onLaunch?: (target: Target) => void
}) {
  const { t, i18n } = useTranslation(['redteam', 'common'])
  const columns = useMemo<TableColumn<Target>[]>(
    () => [
      {
        accessorKey: 'name',
        header: t('targets.columns.name'),
        cell: ({ row }) => (
          <div className="flex flex-col">
            <span className="text-sm font-medium text-foreground">
              {row.original.name || row.original.agent_ref}
            </span>
            <span className="font-mono text-xs text-muted-foreground">
              {row.original.agent_ref}
            </span>
          </div>
        ),
      },
      {
        accessorKey: 'endpoint',
        header: t('targets.columns.endpoint'),
        cell: ({ row }) => (
          <span className="font-mono text-xs text-muted-foreground">
            {row.original.endpoint || '—'}
          </span>
        ),
      },
      {
        accessorKey: 'scope',
        header: t('targets.columns.scope'),
        cell: ({ row }) => (
          <span className="font-mono text-xs text-muted-foreground">
            {row.original.scope || '—'}
          </span>
        ),
      },
      {
        accessorKey: 'status',
        header: t('targets.columns.consent'),
        cell: ({ row }) => (
          <div className="flex flex-col gap-1">
            <ConsentBadge status={row.original.status} />
            {row.original.authorized && row.original.authorized_by ? (
              <span className="text-xs text-muted-foreground">
                {t('targets.authorizedBy', {
                  who: row.original.authorized_by,
                })}
              </span>
            ) : null}
          </div>
        ),
      },
      {
        id: 'actions',
        header: t('targets.columns.actions'),
        cell: ({ row }) => (
          <TargetActions
            target={row.original}
            canAdmin={canAdmin}
            onAuthorize={onAuthorize}
            onLaunch={onLaunch}
          />
        ),
      },
    ],
    [t, i18n.language, canAdmin, onAuthorize, onLaunch],
  )
  return (
    <DataTable<Target>
      columns={columns}
      data={targets}
      getRowId={(r) => r.id}
      searchable
      empty={
        <EmptyState
          title={t('empty.targets.title')}
          description={t('empty.targets.description')}
        />
      }
    />
  )
}

/** The double-use boundary in code: "Launch run" is GATED by `authorized:true`. When
 *  a target is not authorized the button is disabled with a tooltip that says why —
 *  it never silently fails. Consent actions require `redteam:target:admin`. */
function TargetActions({
  target,
  canAdmin,
  onAuthorize,
  onLaunch,
}: {
  target: Target
  canAdmin: boolean
  onAuthorize?: (target: Target) => void
  onLaunch?: (target: Target) => void
}) {
  const { t } = useTranslation('redteam')
  const launchBtn = (
    <Button
      variant="secondary"
      size="sm"
      disabled={!target.authorized}
      onClick={target.authorized ? () => onLaunch?.(target) : undefined}
      aria-label={t('runs.launch')}
    >
      {t('runs.launch')}
    </Button>
  )
  return (
    <div className="flex items-center gap-2">
      {target.authorized ? (
        launchBtn
      ) : (
        // A disabled <button> swallows pointer events, so wrap it for the tooltip.
        <Tooltip>
          <TooltipTrigger asChild>
            <span className="inline-flex">{launchBtn}</span>
          </TooltipTrigger>
          <TooltipContent>{t('runs.launchGatedHint')}</TooltipContent>
        </Tooltip>
      )}
      {canAdmin ? (
        <Button
          variant={target.authorized ? 'outline' : 'primary'}
          size="sm"
          onClick={() => onAuthorize?.(target)}
        >
          {target.authorized ? t('targets.revoke') : t('targets.authorize')}
        </Button>
      ) : null}
    </div>
  )
}

// --- §9 scorecard ------------------------------------------------------------

/** The run scorecard: a robustness gauge (higher = better) for `completed` runs, but
 *  a `degraded` run (all probes skipped — no sandbox) is NEVER shown as a pass/green;
 *  it reads as "pending sandbox". An `error` run is execution-failed, not a verdict. */
export function RunScorecard({ run }: { run: Run }) {
  const { t } = useTranslation('redteam')
  const isDegraded = run.status === 'degraded'
  const isError = run.status === 'error'
  const scored = !isDegraded && !isError

  return (
    <SectionCard
      title={t('runs.scorecard.title')}
      description={t('runs.scorecard.description')}
    >
      <div className="grid gap-4 md:grid-cols-[auto_1fr] md:items-center">
        <div className="flex flex-col items-center gap-2">
          {scored ? (
            <RadialGauge
              value={run.score}
              caption={t('runs.robustness')}
              ariaLabel={t('runs.robustness')}
            />
          ) : (
            <div className="flex size-[120px] flex-col items-center justify-center rounded-full border border-dashed border-border text-muted-foreground">
              <ShieldQuestion className="size-7" />
              <span className="mt-1 text-xs">{t('runs.noScore')}</span>
            </div>
          )}
          <RunStatusBadge status={run.status} />
        </div>
        <div className="flex flex-col gap-3">
          {isDegraded ? (
            <CaveatNotice tone="warning">{t('runs.degradedHint')}</CaveatNotice>
          ) : null}
          {isError ? (
            <CaveatNotice tone="warning">{t('runs.errorHint')}</CaveatNotice>
          ) : null}
          <StatGrid>
            <MetricStat
              label={t('runs.stats.passed')}
              value={formatInt(run.passed)}
              caption={t('runs.stats.passedCaption')}
              tone={scored ? 'success' : 'default'}
            />
            <MetricStat
              label={t('runs.stats.failed')}
              value={formatInt(run.failed)}
              caption={t('runs.stats.failedCaption')}
              tone={run.failed > 0 ? 'danger' : 'default'}
            />
            <MetricStat
              label={t('runs.stats.errors')}
              value={formatInt(run.errors)}
              caption={t('runs.stats.excludedCaption')}
            />
            <MetricStat
              label={t('runs.stats.skipped')}
              value={formatInt(run.skipped)}
              caption={t('runs.stats.excludedCaption')}
            />
          </StatGrid>
        </div>
      </div>
    </SectionCard>
  )
}

const RUN_STATUS_VARIANT: Record<
  string,
  'success' | 'warning' | 'danger' | 'neutral'
> = {
  completed: 'success',
  degraded: 'warning',
  error: 'danger',
}

/** A `degraded` status is rendered as the honest "pending sandbox" label and a WARNING
 *  variant — never success/green, because skipped probes are not a defense that held. */
export function RunStatusBadge({ status }: { status: string }) {
  const { t } = useTranslation('redteam')
  const key = status.toLowerCase()
  return (
    <Badge variant={RUN_STATUS_VARIANT[key] ?? 'neutral'}>
      {t(`runs.status.${key}`, { defaultValue: humanize(status) })}
    </Badge>
  )
}

// --- §9 runs list ------------------------------------------------------------

export function RunsTable({
  runs,
  onRowClick,
}: {
  runs: Run[]
  onRowClick?: (run: Run) => void
}) {
  const { t, i18n } = useTranslation('redteam')
  const columns = useMemo<TableColumn<Run>[]>(
    () => [
      {
        accessorKey: 'started_at',
        header: t('runs.columns.started'),
        cell: ({ row }) => (
          <span className="text-xs text-muted-foreground">
            {formatDateTime(row.original.started_at, i18n.language)}
          </span>
        ),
      },
      {
        accessorKey: 'target_ref',
        header: t('runs.columns.target'),
        cell: ({ row }) => (
          <span className="font-mono text-xs text-foreground">
            {row.original.target_ref}
          </span>
        ),
      },
      {
        accessorKey: 'suite',
        header: t('runs.columns.suite'),
        cell: ({ row }) => (
          <span className="font-mono text-xs text-muted-foreground">
            {row.original.suite}
          </span>
        ),
      },
      {
        accessorKey: 'status',
        header: t('runs.columns.status'),
        cell: ({ row }) => <RunStatusBadge status={row.original.status} />,
      },
      {
        accessorKey: 'score',
        header: t('runs.columns.score'),
        cell: ({ row }) =>
          // Only a completed run carries a meaningful score; degraded/error show "—".
          row.original.status === 'completed' ? (
            <span className="font-mono tabular-nums text-foreground">
              {formatInt(row.original.score)}
            </span>
          ) : (
            <span className="text-muted-foreground">—</span>
          ),
      },
      {
        id: 'passfail',
        header: t('runs.columns.passFail'),
        cell: ({ row }) => (
          <span className="font-mono text-xs tabular-nums">
            <span className="text-success">
              {formatInt(row.original.passed)}
            </span>
            {' / '}
            {/* ⛔ El rojo era INCONDICIONAL. Una corrida `degraded` o `error` llega
                con `failed: 0` —los fixtures del propio módulo lo construyen— y
                pintaba «0» como si hubiera fallos. Cero fallos no es una alarma. */}
            <span
              className={
                row.original.failed > 0
                  ? 'text-danger'
                  : 'text-muted-foreground'
              }
            >
              {formatInt(row.original.failed)}
            </span>
          </span>
        ),
      },
    ],
    [t, i18n.language],
  )
  return (
    <DataTable<Run>
      columns={columns}
      data={runs}
      getRowId={(r) => r.id}
      onRowClick={onRowClick}
      empty={
        <EmptyState
          title={t('empty.runs.title')}
          description={t('empty.runs.description')}
        />
      }
    />
  )
}

// --- §9 by-family failure breakdown ------------------------------------------

/** Failures by family — a ranked bar chart over `by_family[*].Failed`. Renders an
 *  honest empty frame when nothing failed (no fabricated bars). */
export function FamilyFailureChart({ run }: { run: Run }) {
  const { t } = useTranslation('redteam')
  const data = Object.entries(run.by_family)
    .map(([family, tally]) => ({ family, failed: tally.Failed }))
    .filter((d) => d.failed > 0)
    .sort((a, b) => b.failed - a.failed)
  // The worst family heads the (descending) ranking — name it in the SR summary.
  const summary =
    data.length > 0
      ? t('runs.byFamily.summary', {
          count: data.length,
          family: humanize(data[0].family),
          failed: data[0].failed,
        })
      : t('runs.byFamily.summaryEmpty')
  const columns = useMemo<TableColumn<(typeof data)[number]>[]>(
    () => [
      {
        accessorKey: 'family',
        header: t('runs.byFamily.colFamily'),
        cell: ({ row }) => humanize(row.original.family),
      },
      {
        accessorKey: 'failed',
        header: t('runs.byFamily.colFailed'),
        cell: ({ row }) => formatInt(row.original.failed),
      },
    ],
    [t],
  )
  return (
    <SectionCard
      title={t('runs.byFamily.title')}
      description={t('runs.byFamily.description')}
    >
      <AccessibleChart
        title={t('runs.byFamily.title')}
        summary={summary}
        columns={columns}
        data={data}
        getRowId={(d) => d.family}
        empty={
          <EmptyState
            title={t('empty.familyFailureChart.title')}
            description={t('empty.familyFailureChart.description')}
          />
        }
      >
        <CategoryBarChart
          data={data}
          categoryKey="family"
          valueKey="failed"
          valueFormatter={(v) => formatInt(v)}
          categoryFormatter={(c) => humanize(c)}
          height={Math.max(120, data.length * 30 + 24)}
          emptyLabel={t('runs.byFamily.empty')}
        />
      </AccessibleChart>
    </SectionCard>
  )
}

// --- §9 OWASP failures list --------------------------------------------------

/** The OWASP rows that failed (count by ref). Empty = no failed rows (honest). */
export function OwaspFailures({ run }: { run: Run }) {
  const { t } = useTranslation('redteam')
  const entries = Object.entries(run.owasp_failures).sort((a, b) => b[1] - a[1])
  return (
    <SectionCard
      title={t('runs.owasp.title')}
      description={t('runs.owasp.description')}
    >
      {entries.length === 0 ? (
        <p className="text-sm text-muted-foreground">{t('runs.owasp.empty')}</p>
      ) : (
        <ul className="flex flex-wrap gap-2">
          {entries.map(([ref, count]) => (
            <li key={ref}>
              <Badge variant="danger" className="gap-1.5">
                <span className="font-mono">{ref}</span>
                <span className="tabular-nums">{formatInt(count)}</span>
              </Badge>
            </li>
          ))}
        </ul>
      )}
    </SectionCard>
  )
}

// --- §10 per-probe results ---------------------------------------------------

export function ResultsTable({ results }: { results: ProbeResult[] }) {
  const { t, i18n } = useTranslation('redteam')
  const columns = useMemo<TableColumn<ProbeResult>[]>(
    () => [
      {
        accessorKey: 'probe_id',
        header: t('results.columns.probe'),
        cell: ({ row }) => (
          <span className="font-mono text-xs text-foreground">
            {row.original.probe_id}
          </span>
        ),
      },
      {
        accessorKey: 'family',
        header: t('results.columns.family'),
        cell: ({ row }) => (
          <span className="text-xs text-muted-foreground">
            {humanize(row.original.family)}
          </span>
        ),
      },
      {
        id: 'frameworks',
        header: t('results.columns.frameworks'),
        cell: ({ row }) => (
          <span className="flex flex-wrap gap-1">
            {row.original.owasp ? (
              <Badge variant="outline" className="font-mono">
                {row.original.owasp}
              </Badge>
            ) : null}
            {row.original.atlas ? (
              <Badge variant="outline" className="font-mono">
                {row.original.atlas}
              </Badge>
            ) : null}
          </span>
        ),
      },
      {
        accessorKey: 'outcome',
        header: t('results.columns.outcome'),
        cell: ({ row }) => <OutcomeBadge outcome={row.original.outcome} />,
      },
      {
        accessorKey: 'severity',
        header: t('results.columns.severity'),
        cell: ({ row }) => <SeverityBadge severity={row.original.severity} />,
      },
      {
        accessorKey: 'detail_hash',
        header: t('results.columns.fingerprint'),
        cell: ({ row }) => <HashChip hash={row.original.detail_hash} />,
      },
      {
        accessorKey: 'occurred_at',
        header: t('results.columns.occurredAt'),
        cell: ({ row }) => (
          <span className="text-xs text-muted-foreground">
            {formatDateTime(row.original.occurred_at, i18n.language)}
          </span>
        ),
      },
    ],
    [t, i18n.language],
  )
  return (
    <DataTable<ProbeResult>
      columns={columns}
      data={results}
      getRowId={(r) => r.id}
      empty={
        <EmptyState
          title={t('empty.results.title')}
          description={t('empty.results.description')}
        />
      }
    />
  )
}

// --- §7 catalog --------------------------------------------------------------

/** Framework coverage as small stat cards from the count-by-key maps — these are
 *  defensive counts, never exploit content. */
export function CoverageStats({ catalog }: { catalog: CatalogResponse }) {
  const { t } = useTranslation('redteam')
  const familyCount = Object.keys(catalog.families).length
  const owaspCount = Object.keys(catalog.owasp_covered).length
  const atlasCount = Object.keys(catalog.atlas_covered).length
  return (
    <StatGrid>
      <MetricStat
        label={t('catalog.stats.probes')}
        value={formatInt(catalog.total)}
      />
      <MetricStat
        label={t('catalog.stats.families')}
        value={formatInt(familyCount)}
      />
      <MetricStat
        label={t('catalog.stats.owasp')}
        value={formatInt(owaspCount)}
      />
      <MetricStat
        label={t('catalog.stats.atlas')}
        value={formatInt(atlasCount)}
      />
    </StatGrid>
  )
}

/** The probe taxonomy table — metadata only (no payload column exists by design). */
export function CatalogTable({ catalog }: { catalog: CatalogResponse }) {
  const { t } = useTranslation('redteam')
  const columns = useMemo<TableColumn<CatalogResponse['probes'][number]>[]>(
    () => [
      {
        accessorKey: 'id',
        header: t('catalog.columns.id'),
        cell: ({ row }) => (
          <span className="font-mono text-xs text-foreground">
            {row.original.id}
          </span>
        ),
      },
      {
        accessorKey: 'family',
        header: t('catalog.columns.family'),
        cell: ({ row }) => (
          <span className="text-xs text-muted-foreground">
            {humanize(row.original.family)}
          </span>
        ),
      },
      {
        accessorKey: 'title',
        header: t('catalog.columns.title'),
        cell: ({ row }) => (
          <span className="text-sm text-foreground">{row.original.title}</span>
        ),
      },
      {
        id: 'frameworks',
        header: t('catalog.columns.frameworks'),
        cell: ({ row }) => (
          <span className="flex flex-wrap gap-1">
            {row.original.owasp ? (
              <Badge variant="outline" className="font-mono">
                {row.original.owasp}
              </Badge>
            ) : null}
            {row.original.atlas ? (
              <Badge variant="outline" className="font-mono">
                {row.original.atlas}
              </Badge>
            ) : null}
          </span>
        ),
      },
      {
        accessorKey: 'surface',
        header: t('catalog.columns.surface'),
        cell: ({ row }) => (
          <span className="font-mono text-xs text-muted-foreground">
            {row.original.surface ?? '—'}
          </span>
        ),
      },
      {
        accessorKey: 'severity',
        header: t('catalog.columns.severity'),
        cell: ({ row }) => <SeverityBadge severity={row.original.severity} />,
      },
    ],
    [t],
  )
  return (
    <DataTable<CatalogResponse['probes'][number]>
      columns={columns}
      data={catalog.probes}
      getRowId={(r) => r.id}
      searchable
      empty={
        <EmptyState
          title={t('empty.catalog.title')}
          description={t('empty.catalog.description')}
        />
      }
    />
  )
}
