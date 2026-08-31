// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// TeamCostsView — team cost attribution page.
// Displays team-level spend for a selectable period (7d/30d/90d) with a
// daily-trend sparkline and expandable inline rows that show project and model
// breakdowns. The backend sorts teams by cost descending; this view preserves
// that order. Costs are integer micro-USD throughout — never floats.
//
// The period is deep-linkable: it is the whole slice a cost link is
// about, so "look at 90d" has to survive being pasted into a ticket. The
// expanded-rows set deliberately is NOT — it holds literal team names and the
// DTO carries no opaque id, so serialising it would leak the org chart into the
// address bar, browser history and any link store on the way.
import { useQuery } from '@tanstack/react-query'
import { ChevronDown, ChevronRight, DollarSign, Download } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import { StepUpRequiredState } from '@/components/layout/step-up-state'
import { ErrorState, ForbiddenState } from '@/components/ui/error-state'
import { PageHeader } from '@/components/ui/page-header'
import { Skeleton } from '@/components/ui/skeleton'
import { SavedViewsMenu } from '@/features/saved-views'
import { UrlStateNotice } from '@/features/shared'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { formatInt, formatMicroUsd, formatTokens } from '@/lib/format'
import {
  useValidatedUrlState,
  type UrlState,
  type UrlStateDecoded,
} from '@/lib/hooks/use-url-state'
import { cn } from '@/lib/utils'
import { teamCostsApi, teamCostsKeys } from './api'
import { CostSparkline } from './cost-sparkline'
import type {
  ModelSummaryDTO,
  ProjectSummaryDTO,
  SummaryPeriod,
  TeamSummaryDTO,
} from './types'
import './i18n'

const PERIODS: SummaryPeriod[] = ['7d', '30d', '90d']
/** The default lives here, not in the URL: a pristine view stays shareable. */
const DEFAULT_PERIOD: SummaryPeriod = '30d'
const URL_KEYS = ['period'] as const

function isPeriod(value: unknown): value is SummaryPeriod {
  return PERIODS.some((p) => p === value)
}

/** Keep only a period this view actually offers — the value may come from a
 *  pasted link or a stored saved view, both untrusted. Returns undefined for
 *  the default too, so the one rule "the default is never serialised" holds in
 *  the URL and in a saved view alike. */
function sanitizePeriod(value: unknown): SummaryPeriod | undefined {
  return isPeriod(value) && value !== DEFAULT_PERIOD ? value : undefined
}

/** Pure, never throws: a malformed URL is ordinary input, not an exception.
 *  Module-level so the identity is stable across renders. */
function decodeUrlState(raw: UrlState): UrlStateDecoded<SummaryPeriod> {
  if (raw.period === undefined) return { value: DEFAULT_PERIOD, issues: [] }
  if (isPeriod(raw.period)) return { value: raw.period, issues: [] }
  return { value: DEFAULT_PERIOD, issues: ['period'] }
}

export function TeamCostsView() {
  const { t, i18n } = useTranslation('team-costs')
  const lang = i18n.language
  const { activeTenant } = useAuth()

  const [period, patchUrlState, urlIssues] = useValidatedUrlState(
    URL_KEYS,
    decodeUrlState,
  )
  // A saved view's rejected value never reaches the URL, so the decoder never
  // sees it — without this the stored-value case would fall back in the silence
  // the URL case no longer does.
  const [savedViewIssues, setSavedViewIssues] = useState<string[]>([])
  /** Set of expanded team names. Local by design — see the header note. */
  const [expanded, setExpanded] = useState<Set<string>>(new Set())

  function selectPeriod(next: SummaryPeriod) {
    setSavedViewIssues([])
    patchUrlState({ period: sanitizePeriod(next) })
  }

  function applySavedView(params: Record<string, string>) {
    const clean = sanitizePeriod(params.period)
    // `period` present but unusable is a rejection; absent means the default.
    setSavedViewIssues(
      params.period !== undefined && !isPeriod(params.period) ? ['period'] : [],
    )
    patchUrlState({ period: clean })
  }

  const savedViewParams = useMemo(
    () => ({ period: sanitizePeriod(period) }),
    [period],
  )

  const query = useQuery({
    queryKey: teamCostsKeys.summary(activeTenant, period),
    queryFn: () => teamCostsApi.summary(period),
  })

  function toggleExpanded(team: string) {
    setExpanded((prev) => {
      const next = new Set(prev)
      if (next.has(team)) {
        next.delete(team)
      } else {
        next.add(team)
      }
      return next
    })
  }

  function handleExportCsv() {
    const teams = query.data?.teams ?? []
    if (teams.length === 0) return

    const rows: string[][] = [
      ['Team', 'Sessions', 'Input Tokens', 'Output Tokens', 'Cost (USD)'],
      ...teams.map((r) => [
        r.team,
        String(r.sessions),
        String(r.input_tokens),
        String(r.output_tokens),
        String(r.cost_micro_usd / 1_000_000),
      ]),
    ]
    const csv = rows
      .map((r) => r.map((c) => `"${c.replace(/"/g, '""')}"`).join(','))
      .join('\n')
    const blob = new Blob([csv], { type: 'text/csv;charset=utf-8;' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `team-costs-${period}.csv`
    a.click()
    URL.revokeObjectURL(url)
  }

  const teams = query.data?.teams ?? []

  return (
    <div className="flex h-full flex-col gap-4">
      <PageHeader
        icon={DollarSign}
        title={t('title')}
        description={t('subtitle')}
        actions={
          <>
            <SavedViewsMenu
              featureId="team-costs"
              params={savedViewParams}
              onApply={applySavedView}
            />
            <Button
              variant="secondary"
              size="sm"
              onClick={handleExportCsv}
              disabled={teams.length === 0}
              aria-label={t('export')}
            >
              <Download />
              {t('export')}
            </Button>
          </>
        }
      />

      {/* Said out loud, directly above the control it is about: a rejected
          value that falls back in silence shows a link's recipient a different
          slice than its author saw, while looking identical. */}
      {savedViewIssues.length > 0 ? (
        <UrlStateNotice issues={savedViewIssues} origin="saved-view" />
      ) : (
        <UrlStateNotice issues={urlIssues} />
      )}

      {/* Period selector */}
      <div
        className="flex items-center gap-1"
        role="group"
        aria-label={t('period.label')}
      >
        {PERIODS.map((p) => (
          <button
            key={p}
            type="button"
            onClick={() => selectPeriod(p)}
            className={cn(
              'h-7 rounded px-2.5 text-xs transition-colors',
              p === period
                ? 'bg-accent text-accent-foreground'
                : 'bg-transparent text-muted-foreground hover:bg-muted hover:text-foreground',
            )}
            aria-pressed={p === period}
          >
            {t(`period.${p}`)}
          </button>
        ))}
      </div>

      {/* Content */}
      {query.isLoading ? (
        <TableSkeleton />
      ) : query.error instanceof ApiError && query.error.isStepUpRequired ? (
        // ⛔ ASEGURAMIENTO ANTES QUE ROL: `isForbidden` es SÓLO el status 403
        // (lib/api/errors.ts:59) y un `step_up_required` lo satisface también, así que
        // leerlo primero acusaba al operador de un permiso que SÍ tiene, y sin salida.
        <StepUpRequiredState
          action="generic"
          onElevated={() => void query.refetch()}
        />
      ) : query.error instanceof ApiError && query.error.isForbidden ? (
        <ForbiddenState />
      ) : query.error ? (
        <ErrorState retry={() => void query.refetch()} />
      ) : teams.length === 0 ? (
        <EmptyState
          icon={<DollarSign />}
          title={t('empty.title')}
          description={t('empty.description')}
        />
      ) : (
        <TeamTable
          teams={teams}
          expanded={expanded}
          onToggle={toggleExpanded}
          lang={lang}
          t={t}
        />
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// TeamTable — custom expandable table (DataTable does not support inline row
// expansion, so we render native HTML table elements styled to match it).
// ---------------------------------------------------------------------------

interface TeamTableProps {
  teams: TeamSummaryDTO[]
  expanded: Set<string>
  onToggle: (team: string) => void
  lang: string
  t: ReturnType<typeof useTranslation<'team-costs'>>['t']
}

function TeamTable({ teams, expanded, onToggle, lang, t }: TeamTableProps) {
  return (
    <div className="overflow-auto rounded-md border border-border">
      <table
        className="w-full min-w-[640px] border-collapse text-sm"
        role="grid"
        aria-label={t('title')}
        aria-rowcount={teams.length + 1}
      >
        <thead className="sticky top-0 z-10 bg-surface">
          <tr className="border-b border-border" role="row">
            <th
              scope="col"
              className="w-8 px-3 py-2"
              role="columnheader"
              aria-colindex={1}
            />
            <th
              scope="col"
              className="px-3 py-2 text-left font-medium text-foreground"
              role="columnheader"
              aria-colindex={2}
            >
              {t('cols.team')}
            </th>
            <th
              scope="col"
              className="px-3 py-2 text-right font-medium text-foreground"
              role="columnheader"
              aria-colindex={3}
            >
              {t('cols.sessions')}
            </th>
            <th
              scope="col"
              className="px-3 py-2 text-right font-medium text-foreground"
              role="columnheader"
              aria-colindex={4}
            >
              {t('cols.tokensIn')}
            </th>
            <th
              scope="col"
              className="px-3 py-2 text-right font-medium text-foreground"
              role="columnheader"
              aria-colindex={5}
            >
              {t('cols.tokensOut')}
            </th>
            <th
              scope="col"
              className="px-3 py-2 text-right font-medium text-foreground"
              role="columnheader"
              aria-colindex={6}
            >
              {t('cols.cost')}
            </th>
            <th
              scope="col"
              className="w-24 px-3 py-2 text-left font-medium text-foreground"
              role="columnheader"
              aria-colindex={7}
            >
              {t('cols.trend')}
            </th>
          </tr>
        </thead>
        <tbody>
          {teams.map((team, idx) => (
            <TeamRow
              key={team.team}
              team={team}
              rowIndex={idx + 2}
              isExpanded={expanded.has(team.team)}
              onToggle={() => onToggle(team.team)}
              lang={lang}
              t={t}
            />
          ))}
        </tbody>
      </table>
    </div>
  )
}

// ---------------------------------------------------------------------------
// TeamRow — one collapsible team row + optional inline expansion panel.
// ---------------------------------------------------------------------------

interface TeamRowProps {
  team: TeamSummaryDTO
  rowIndex: number
  isExpanded: boolean
  onToggle: () => void
  lang: string
  t: ReturnType<typeof useTranslation<'team-costs'>>['t']
}

function TeamRow({
  team,
  rowIndex,
  isExpanded,
  onToggle,
  lang,
  t,
}: TeamRowProps) {
  const ChevronIcon = isExpanded ? ChevronDown : ChevronRight

  return (
    <>
      <tr
        className={cn(
          'cursor-pointer border-b border-border last:border-0 transition-colors hover:bg-muted',
          isExpanded && 'bg-muted/50',
        )}
        role="row"
        aria-rowindex={rowIndex}
        aria-expanded={isExpanded}
        onClick={onToggle}
        onKeyDown={(e) => {
          if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault()
            onToggle()
          }
        }}
        tabIndex={0}
      >
        {/* Expand/collapse chevron */}
        <td
          className="px-3 py-2 text-muted-foreground"
          role="gridcell"
          aria-colindex={1}
        >
          <ChevronIcon className="size-3.5" aria-hidden="true" />
        </td>
        {/* Team name */}
        <td
          className="px-3 py-2 font-medium text-foreground"
          role="gridcell"
          aria-colindex={2}
        >
          {team.team}
        </td>
        {/* Sessions */}
        <td
          className="px-3 py-2 text-right font-mono text-xs tabular-nums text-muted-foreground"
          role="gridcell"
          aria-colindex={3}
        >
          {formatInt(team.sessions, lang)}
        </td>
        {/* Input tokens */}
        <td
          className="px-3 py-2 text-right font-mono text-xs tabular-nums text-muted-foreground"
          role="gridcell"
          aria-colindex={4}
        >
          {formatTokens(team.input_tokens, lang)}
        </td>
        {/* Output tokens */}
        <td
          className="px-3 py-2 text-right font-mono text-xs tabular-nums text-muted-foreground"
          role="gridcell"
          aria-colindex={5}
        >
          {formatTokens(team.output_tokens, lang)}
        </td>
        {/* Cost */}
        <td
          className="px-3 py-2 text-right font-mono text-xs tabular-nums text-foreground"
          role="gridcell"
          aria-colindex={6}
        >
          {formatMicroUsd(team.cost_micro_usd, { compact: true, locale: lang })}
        </td>
        {/* Sparkline */}
        <td className="px-3 py-2" role="gridcell" aria-colindex={7}>
          {team.trend.length > 0 && (
            <CostSparkline data={team.trend} height={32} />
          )}
        </td>
      </tr>

      {/* Inline expansion — project and model breakdowns */}
      {isExpanded && (
        <tr role="row" aria-rowindex={rowIndex}>
          <td colSpan={7} className="bg-muted/30 px-6 py-4">
            <div className="flex flex-col gap-4 sm:flex-row sm:gap-8">
              <ProjectBreakdown projects={team.projects} lang={lang} t={t} />
              <ModelBreakdown models={team.models} lang={lang} t={t} />
            </div>
          </td>
        </tr>
      )}
    </>
  )
}

// ---------------------------------------------------------------------------
// ProjectBreakdown — compact table of per-project spend within a team.
// ---------------------------------------------------------------------------

interface ProjectBreakdownProps {
  projects: ProjectSummaryDTO[]
  lang: string
  t: ReturnType<typeof useTranslation<'team-costs'>>['t']
}

function ProjectBreakdown({ projects, lang, t }: ProjectBreakdownProps) {
  return (
    <div className="min-w-[220px] flex-1">
      <p className="mb-2 text-xs font-semibold uppercase tracking-wider text-muted-foreground">
        {t('breakdown.projects')}
      </p>
      {projects.length === 0 ? (
        <p className="text-xs text-muted-foreground">
          {t('breakdown.noProjects')}
        </p>
      ) : (
        <table className="w-full text-xs" aria-label={t('breakdown.projects')}>
          <thead>
            <tr className="border-b border-border">
              <th
                scope="col"
                className="pb-1 text-left font-medium text-muted-foreground"
              >
                {t('breakdown.project')}
              </th>
              <th
                scope="col"
                className="pb-1 text-right font-medium text-muted-foreground"
              >
                {t('breakdown.sessions')}
              </th>
              <th
                scope="col"
                className="pb-1 text-right font-medium text-muted-foreground"
              >
                {t('breakdown.cost')}
              </th>
            </tr>
          </thead>
          <tbody>
            {projects.map((p) => (
              <tr
                key={p.project}
                className="border-b border-border/50 last:border-0"
              >
                <td className="py-1 font-medium text-foreground">
                  {p.project}
                </td>
                <td className="py-1 text-right tabular-nums text-muted-foreground">
                  {formatInt(p.sessions, lang)}
                </td>
                <td className="py-1 text-right tabular-nums text-foreground">
                  {formatMicroUsd(p.cost_micro_usd, {
                    compact: true,
                    locale: lang,
                  })}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// ModelBreakdown — compact table of per-model spend within a team.
// ---------------------------------------------------------------------------

interface ModelBreakdownProps {
  models: ModelSummaryDTO[]
  lang: string
  t: ReturnType<typeof useTranslation<'team-costs'>>['t']
}

function ModelBreakdown({ models, lang, t }: ModelBreakdownProps) {
  return (
    <div className="min-w-[220px] flex-1">
      <p className="mb-2 text-xs font-semibold uppercase tracking-wider text-muted-foreground">
        {t('breakdown.models')}
      </p>
      {models.length === 0 ? (
        <p className="text-xs text-muted-foreground">
          {t('breakdown.noModels')}
        </p>
      ) : (
        <table className="w-full text-xs" aria-label={t('breakdown.models')}>
          <thead>
            <tr className="border-b border-border">
              <th
                scope="col"
                className="pb-1 text-left font-medium text-muted-foreground"
              >
                {t('breakdown.model')}
              </th>
              <th
                scope="col"
                className="pb-1 text-right font-medium text-muted-foreground"
              >
                {t('breakdown.cost')}
              </th>
            </tr>
          </thead>
          <tbody>
            {models.map((m) => (
              <tr
                key={m.model}
                className="border-b border-border/50 last:border-0"
              >
                <td className="py-1 font-mono text-foreground">{m.model}</td>
                <td className="py-1 text-right tabular-nums text-foreground">
                  {formatMicroUsd(m.cost_micro_usd, {
                    compact: true,
                    locale: lang,
                  })}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// TableSkeleton — loading placeholder
// ---------------------------------------------------------------------------

function TableSkeleton() {
  return (
    <div className="flex flex-col gap-2">
      {Array.from({ length: 5 }).map((_, i) => (
        <Skeleton key={i} className="h-10 w-full rounded" />
      ))}
    </div>
  )
}
