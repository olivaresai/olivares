// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Evals (module XII) presentational pieces — PURE: data via props, no fetching/auth,
// so they are trivially testable with fixtures. They encode the honesty rules of the
// Contract: a `skipped` case is NEUTRAL, never styled as a pass (OutcomeBadge);
// a regressed run shows a danger `Regressed` badge (the Finding itself is handled by
// the security view, not duplicated here); a per-case `detail_hash` is a fingerprint
// (HashChip), never an expandable payload; no raw candidate output text anywhere.
import { useMemo } from 'react'
import { TrendingDown } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { StatusBadge } from '@/components/data/badges'
import { Badge } from '@/components/ui/badge'
import { EmptyState } from '@/components/ui/empty-state'
import {
  Sparkline,
  StatusBar,
  TrendChart,
  useChartTheme,
} from '@/components/charts'
import { HashChip, OutcomeBadge, SectionCard } from '@/features/_intel'
import { cn } from '@/lib/utils'
import {
  formatDateTime,
  formatFraction,
  formatInt,
  formatScore,
} from '@/lib/format'
import type { AbPairwise, CaseResult, EvalRun, Scorecard } from './types'

// --- scorecards --------------------------------------------------------------

/**
 * ⛔ EL VEREDICTO DE REGRESIÓN, COMO FUNCIÓN PURA, porque la decisión es la parte que importa y
 *    debe poder probarse sin pintar nada.
 *
 *    `regressed: false` significa CUATRO cosas y sólo una es «pasó». `resolveRegression`
 *    (`modules/evals/runs.go:257-276`) sale con el veredicto vacío en TRES caminos antes de
 *    comparar nada:
 *      · `agg.passed+agg.failed == 0` — no se puntuó nada («A degraded/all-skipped run is never
 *        flagged (it was not actually scored)»).
 *      · `suite.RegThreshold <= 0` — la suite ni siquiera comprueba regresiones.
 *      · `!found` — no había línea base con la que comparar.
 *    La columna pintaba «sin regresión» para los cuatro: una ejecución que NO MIDIÓ NADA se leía
 *    como una que midió y salió bien, en la señal que decide si una release sale.
 *
 * ⚠ LO QUE LA FILA SOSTIENE, y nada más:
 *      · `n_scored === 0` ⇒ no se puntuó. Cierto y suficiente.
 *      · `baseline_ref` VACÍO ⇒ no hubo comparación. Cierto en ESA dirección.
 *    Lo converso NO vale y por eso no se afirma: `baselineScore` devuelve la ref explícita aunque
 *    `found` sea falso (`runs.go:288-296`), así que presente ≠ comparada. Con ref presente se
 *    dice «sin regresión» a secas.
 */
export type VeredictoRegresion =
  'regressed' | 'notScored' | 'noBaseline' | 'noRegression'

export function veredictoRegresion(run: {
  regressed: boolean
  n_scored?: number
  baseline_ref?: string
}): VeredictoRegresion {
  if (run.regressed) return 'regressed'
  if (run.n_scored === 0) return 'notScored'
  if (!run.baseline_ref) return 'noBaseline'
  return 'noRegression'
}

/** One scorecard card: big mean score, pass-rate, run count, a trend sparkline and a
 *  loud `Regressed` badge when the subject has regressed vs its baseline. */
export function ScorecardCard({ scorecard }: { scorecard: Scorecard }) {
  const { t, i18n } = useTranslation(['evals', 'intel'])
  const theme = useChartTheme()
  const sparkData = scorecard.trend.map((p) => ({ at: p.at, score: p.score }))
  const sparkColor = scorecard.regressed ? theme.danger : theme.accent
  return (
    <SectionCard
      className={scorecard.regressed ? 'border-danger-line' : undefined}
    >
      <div className="flex flex-col gap-3">
        <div className="flex items-start justify-between gap-2">
          <div className="min-w-0">
            <span className="block truncate font-mono text-sm text-foreground">
              {scorecard.key}
            </span>
            <span className="text-xs text-muted-foreground">
              {/* No kind renders NOTHING. The previous form passed the missing
                  value as its own defaultValue, so when the engine omitted the
                  field the fallback was `undefined` and i18next returned the
                  key — the console printed `subjectKind.undefined`. */}
              {scorecard.subject_kind
                ? t(`subjectKind.${scorecard.subject_kind}`, {
                    defaultValue: scorecard.subject_kind,
                  })
                : null}
            </span>
          </div>
          {scorecard.regressed ? (
            <Badge variant="danger" className="shrink-0 gap-1">
              <TrendingDown className="size-3" />
              {t('scorecards.regressed')}
            </Badge>
          ) : null}
        </div>

        <div className="flex items-end justify-between gap-3">
          <div>
            <div className="font-display text-2xl font-semibold tracking-tight tabular-nums text-foreground">
              {formatScore(scorecard.mean_score)}
            </div>
            <div className="text-xs text-muted-foreground">
              {t('scorecards.meanScore')}
            </div>
          </div>
          <div className="text-right">
            {/* ⛔ LA TASA, NO LA MEDIA — y con su denominador, igual que la
                columna de Runs de más abajo. `pass_rate` es la media de las
                corridas (`scorecards.go:149`, `prSum/runs`): una corrida de 1
                caso pesa lo que una de 200, así que bajo el rótulo «Pass-rate»
                se enseñaba otro estadístico. `pooled_pass_rate` es aprobados
                sobre puntuados de TODAS las corridas. Ausente se pinta «—»
                (formatFraction ya lo hace): ausencia es «no se puntuó nada»,
                nunca «0 %». */}
            <div className="font-mono text-base tabular-nums text-foreground">
              {formatFraction(scorecard.pooled_pass_rate?.rate)}
              {scorecard.pooled_pass_rate !== undefined ? (
                <span className="ml-1 text-xs opacity-70">
                  {t('runs.overN', { n: scorecard.pooled_pass_rate.n })}
                </span>
              ) : null}
            </div>
            <div className="text-xs text-muted-foreground">
              {t('scorecards.passRate')}
            </div>
          </div>
        </div>

        <Sparkline
          data={sparkData}
          dataKey="score"
          color={sparkColor}
          height={36}
        />

        <div className="flex items-center justify-between text-xs text-muted-foreground">
          <span>
            {t('scorecards.runs')}:{' '}
            <span className="font-mono text-foreground">
              {formatInt(scorecard.runs, i18n.language)}
            </span>
          </span>
          <span>
            {t('scorecards.lastScore')}:{' '}
            <span className="font-mono text-foreground">
              {formatScore(scorecard.last_score)}
            </span>
          </span>
        </div>
      </div>
    </SectionCard>
  )
}

/** The scorecard grid — one card per subject, positioned by stable list index. */
export function ScorecardGrid({ scorecards }: { scorecards: Scorecard[] }) {
  return (
    <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
      {scorecards.map((sc) => (
        <ScorecardCard key={`${sc.subject_kind}:${sc.key}`} scorecard={sc} />
      ))}
    </div>
  )
}

// --- runs table --------------------------------------------------------------

export function RunsTable({
  runs,
  onRowClick,
}: {
  runs: EvalRun[]
  onRowClick?: (run: EvalRun) => void
}) {
  const { t, i18n } = useTranslation('evals')
  const columns = useMemo<TableColumn<EvalRun>[]>(
    () => [
      {
        accessorKey: 'subject_ref',
        header: t('runs.columns.subject'),
        cell: ({ row }) => (
          <div className="flex flex-col">
            <span className="font-mono text-xs text-foreground">
              {row.original.subject_ref}
            </span>
            <span className="text-xs text-muted-foreground">
              {row.original.suite_ref}
              {row.original.prompt_variant
                ? ` · ${row.original.prompt_variant}`
                : ''}
            </span>
          </div>
        ),
      },
      {
        accessorKey: 'status',
        header: t('runs.columns.status'),
        cell: ({ row }) => <StatusBadge status={row.original.status} />,
      },
      {
        accessorKey: 'score',
        header: t('runs.columns.score'),
        cell: ({ row }) => (
          <span className="font-mono tabular-nums text-foreground">
            {formatScore(row.original.score)}
          </span>
        ),
      },
      {
        accessorKey: 'pass_rate',
        header: t('runs.columns.passRate'),
        // ⛔ LA TASA VIAJA CON SU DENOMINADOR. El motor manda `n_scored` justamente para eso
        //    (`modules/evals/runs.go:400-402`: «n=2 and n=200 are different claims») y esta
        //    columna lo ignoraba. Un 100 % sobre dos casos se leía igual que uno sobre
        //    doscientos.
        cell: ({ row }) => (
          <span className="font-mono tabular-nums text-muted-foreground">
            {formatFraction(row.original.pass_rate)}
            {row.original.n_scored !== undefined ? (
              <span className="ml-1 text-xs opacity-70">
                {t('runs.overN', { n: row.original.n_scored })}
              </span>
            ) : null}
          </span>
        ),
      },
      {
        id: 'regressed',
        header: t('runs.columns.regression'),
        cell: ({ row }) => {
          const v = veredictoRegresion(row.original)
          if (v === 'regressed')
            return (
              <Badge variant="danger" className="gap-1">
                <TrendingDown className="size-3" />
                {t('runs.regressed', {
                  drift: formatFraction(row.original.drift),
                })}
              </Badge>
            )
          return (
            <span className="text-xs text-muted-foreground">
              {t(`runs.${v}`)}
            </span>
          )
        },
      },
      {
        accessorKey: 'started_at',
        header: t('runs.columns.startedAt'),
        cell: ({ row }) => (
          <span className="text-xs text-muted-foreground">
            {formatDateTime(row.original.started_at, i18n.language)}
          </span>
        ),
      },
    ],
    [t, i18n.language],
  )
  return (
    <DataTable<EvalRun>
      columns={columns}
      data={runs}
      getRowId={(r) => r.id}
      onRowClick={onRowClick}
      searchable
      empty={
        <EmptyState
          title={t('empty.runs.title')}
          description={t('empty.runs.description')}
        />
      }
    />
  )
}

// --- per-case results --------------------------------------------------------

/** Per-case results for a single run. The candidate output is NEVER here — only the
 *  outcome, score, a short clamped label, and the `detail_hash` fingerprint. */
export function CaseResultsTable({ results }: { results: CaseResult[] }) {
  const { t, i18n } = useTranslation('evals')
  const columns = useMemo<TableColumn<CaseResult>[]>(
    () => [
      {
        accessorKey: 'case_key',
        header: t('cases.columns.case'),
        cell: ({ row }) => (
          <span className="font-mono text-xs text-foreground">
            {row.original.case_key}
          </span>
        ),
      },
      {
        accessorKey: 'outcome',
        header: t('cases.columns.outcome'),
        cell: ({ row }) => <OutcomeBadge outcome={row.original.outcome} />,
      },
      {
        accessorKey: 'score',
        header: t('cases.columns.score'),
        cell: ({ row }) => (
          // A skipped case is not scored — show an em-dash, never a 0.00 that could
          // read as a graded result.
          <span className="font-mono tabular-nums text-muted-foreground">
            {row.original.outcome === 'skipped'
              ? '—'
              : formatScore(row.original.score)}
          </span>
        ),
      },
      {
        accessorKey: 'label',
        header: t('cases.columns.label'),
        cell: ({ row }) => (
          <span className="text-xs text-muted-foreground">
            {row.original.label || '—'}
          </span>
        ),
      },
      {
        accessorKey: 'detail_hash',
        header: t('cases.columns.fingerprint'),
        cell: ({ row }) => (
          <HashChip
            hash={row.original.detail_hash}
            label={t('cases.fingerprint')}
          />
        ),
      },
    ],
    [t, i18n.language],
  )
  return (
    <DataTable<CaseResult>
      columns={columns}
      data={results}
      getRowId={(r) => r.id}
      empty={
        <EmptyState
          title={t('empty.caseResults.title')}
          description={t('empty.caseResults.description')}
        />
      }
    />
  )
}

// --- A/B comparison ----------------------------------------------------------

/** Comparative bar of two prompt variants by score, with the winner highlighted and
 *  the delta called out, plus the judged head-to-head block when the comparison
 *  asked for one. */
export function AbComparison({
  variants,
  winner,
  delta,
  tie,
  pairwise,
}: {
  variants: { label: string; score: number; pass_rate: number }[]
  winner: string
  delta: number
  /** The engine's EXPLICIT tie (ab.go: `out.Tie`). Optional so the older callers
   *  that only pass a winner keep working; when it is absent we fall back to the
   *  empty-winner reading, which is what the response means. */
  tie?: boolean
  pairwise?: AbPairwise
}) {
  const { t } = useTranslation('evals')
  const theme = useChartTheme()
  const isTie = tie ?? (!winner || winner.toLowerCase() === 'tie')
  const maxScore = Math.max(0.0001, ...variants.map((v) => v.score))
  return (
    <SectionCard
      title={t('ab.title')}
      description={t('ab.description')}
      actions={
        isTie ? (
          <Badge variant="neutral">{t('ab.tie')}</Badge>
        ) : (
          <Badge variant="success">
            {t('ab.winner', { label: winner, delta: formatScore(delta) })}
          </Badge>
        )
      }
    >
      <div className="flex flex-col gap-4">
        {variants.map((v, i) => {
          const isWinner = !isTie && v.label === winner
          const barColor = isWinner ? theme.success : theme.slate
          return (
            <div key={v.label} className="flex flex-col gap-1.5">
              <div className="flex items-center justify-between gap-2 text-sm">
                <span
                  className={cn(
                    'font-mono',
                    isWinner
                      ? 'font-medium text-foreground'
                      : 'text-muted-foreground',
                  )}
                >
                  {v.label}
                  {isWinner ? (
                    <Badge variant="success" className="ml-2">
                      {t('ab.winnerTag')}
                    </Badge>
                  ) : null}
                </span>
                <span className="font-mono tabular-nums text-foreground">
                  {formatScore(v.score)}
                </span>
              </div>
              <div
                className="h-2.5 w-full overflow-hidden rounded-full bg-muted"
                role="img"
                aria-label={`${v.label}: ${formatScore(v.score)}`}
              >
                <div
                  className="h-full rounded-full"
                  style={{
                    width: `${(v.score / maxScore) * 100}%`,
                    backgroundColor: barColor,
                  }}
                  // stable order: variant `i` is positioned by its index in the list
                  data-variant-index={i}
                />
              </div>
              <span className="text-xs text-muted-foreground">
                {t('ab.passRate', { value: formatFraction(v.pass_rate) })}
              </span>
            </div>
          )
        })}
        {pairwise ? <PairwiseBlock pairwise={pairwise} /> : null}
      </div>
    </SectionCard>
  )
}

/** The order-swapped judged comparison. A SKIP is reported as a skip with its
 *  reason — never as a tie and never as a winner — and the position-consistency
 *  rate is shown with its denominator and interval, which is the whole point of
 *  measuring position bias instead of hiding it. */
function PairwiseBlock({ pairwise }: { pairwise: AbPairwise }) {
  const { t } = useTranslation('evals')
  const pc = pairwise.position_consistency
  return (
    <div className="flex flex-col gap-1.5 border-t border-border pt-3">
      <div className="flex items-center justify-between gap-2">
        <span className="text-sm font-medium text-foreground">
          {t('ab.pairwiseTitle')}
        </span>
        {pairwise.mode === 'skipped' ? (
          <Badge variant="neutral">{t('ab.pairwiseSkippedTag')}</Badge>
        ) : pairwise.winner ? (
          <Badge variant="success">
            {t('ab.pairwiseWinner', { label: pairwise.winner })}
          </Badge>
        ) : (
          <Badge variant="neutral">{t('ab.tie')}</Badge>
        )}
      </div>
      {pairwise.mode === 'skipped' ? (
        // The engine's skip_reason is English prose it generates (ab.go:190,211).
        // Printing it alone left a Spanish/German/Japanese console showing a bare
        // English sentence as its ONLY explanation. The localized line carries the
        // meaning; the engine's own words stay verbatim beside it, because a
        // paraphrase of why the engine skipped is not evidence of why it skipped.
        <>
          <p className="text-xs text-muted-foreground">
            {t('ab.pairwiseSkippedBody')}
          </p>
          {pairwise.skip_reason ? (
            <p className="font-mono text-xs text-muted-foreground" lang="en">
              {pairwise.skip_reason}
            </p>
          ) : null}
        </>
      ) : (
        <>
          <p className="text-xs tabular-nums text-muted-foreground">
            {t('ab.pairwiseCounts', {
              compared: pairwise.compared,
              aWins: pairwise.a_wins,
              bWins: pairwise.b_wins,
              ties: pairwise.ties,
              inconsistent: pairwise.inconsistent,
              errors: pairwise.errors,
            })}
          </p>
          {pc ? (
            <p className="text-xs tabular-nums text-muted-foreground">
              {t('ab.pairwiseConsistency', {
                rate: formatFraction(pc.rate),
                n: pc.n,
                lo: formatFraction(pc.ci.lo),
                hi: formatFraction(pc.ci.hi),
              })}
            </p>
          ) : null}
        </>
      )}
    </div>
  )
}

// --- drift -------------------------------------------------------------------

/** A score-over-time trend for a single scorecard, with the suite's pass threshold as
 *  a reference line — degradation reads at a glance. Plus a pass/fail/skipped mix bar
 *  for the most recent run when provided. */
export function DriftChart({
  scorecard,
  threshold,
}: {
  scorecard: Scorecard
  threshold?: number
}) {
  const { t, i18n } = useTranslation('evals')
  const theme = useChartTheme()
  const data = scorecard.trend.map((p) => ({
    at: p.at,
    score: p.score,
  }))
  return (
    <SectionCard
      title={t('drift.title', { subject: scorecard.key })}
      description={t('drift.description')}
    >
      <TrendChart
        data={data}
        xKey="at"
        series={[{ key: 'score', label: t('drift.score'), kind: 'line' }]}
        valueFormatter={(v) => formatScore(v)}
        xTickFormatter={(k) => formatDateTime(k, i18n.language)}
        reference={
          threshold !== undefined
            ? {
                y: threshold,
                label: t('drift.threshold'),
                color: theme.warning,
              }
            : undefined
        }
        height={260}
      />
    </SectionCard>
  )
}

/** A small pass/fail/error/skipped proportion bar for one run's case results. */
export function CaseOutcomeBar({ run }: { run: EvalRun }) {
  const { t } = useTranslation('evals')
  const theme = useChartTheme()
  return (
    <StatusBar
      segments={[
        {
          key: 'passed',
          label: t('outcomeBar.passed'),
          value: run.passed,
          color: theme.success,
        },
        {
          key: 'failed',
          label: t('outcomeBar.failed'),
          value: run.failed,
          color: theme.danger,
        },
        {
          key: 'errors',
          label: t('outcomeBar.errors'),
          value: run.errors,
          color: theme.warning,
        },
        // skipped is neutral/slate — never coloured as a pass.
        {
          key: 'skipped',
          label: t('outcomeBar.skipped'),
          value: run.skipped,
          color: theme.slate,
        },
      ]}
      valueFormatter={(v) => formatInt(v)}
    />
  )
}
