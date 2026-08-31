// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Sandbox (module XVII) presentational pieces — PURE: data via props, no fetching,
// no auth, so they are trivially testable with fixtures and reused by the container.
// They encode the module's honesty rules literally:
//   - the run's `runner` and `isolated` flag are rendered AS RECORDED (isolation is
//     real, not faked); a `degraded` run reads as "executed, not scored", NEVER pass;
//   - `destroyed` is a calm neutral marker (the ephemeral state was discarded);
//   - a per-step output is BOUNDED SYNTHETIC mock text; a mock-miss is shown as its
//     deterministic marker, never a real resource.
import { useMemo } from 'react'
import {
  Archive,
  Box,
  FlaskConical,
  Lock,
  ShieldCheck,
  Trash2,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { StatusBadge } from '@/components/data/badges'
import { Badge, type BadgeVariant } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { CaveatNotice, SectionCard } from '@/features/_intel'
import { formatDateTime, formatScore, humanize } from '@/lib/format'
import { cn } from '@/lib/utils'
import type {
  Comparison,
  ComparisonVerdict,
  Output,
  Run,
  Scenario,
} from './types'

// --- runner / isolation / destroyed badges -----------------------------------

/** The execution backend, rendered literally. `inproc-mock` is isolated by
 *  construction; `container`/`microvm` are the pluggable OS-level backends. */
export function RunnerBadge({ runner }: { runner: string }) {
  const { t } = useTranslation('sandbox')
  const Icon = runner === 'inproc-mock' ? FlaskConical : Box
  return (
    <Badge variant="neutral" className="gap-1 font-mono">
      <Icon className="size-3" />
      {t(`runner.${runner}`, { defaultValue: runner })}
    </Badge>
  )
}

/** The isolation guarantee, AS RECORDED — never inferred or faked. */
export function IsolatedBadge({ isolated }: { isolated: boolean }) {
  const { t } = useTranslation('sandbox')
  if (!isolated) {
    return (
      <Tooltip>
        <TooltipTrigger asChild>
          <Badge variant="warning" className="gap-1">
            {t('run.notIsolated')}
          </Badge>
        </TooltipTrigger>
        <TooltipContent>{t('run.notIsolatedHint')}</TooltipContent>
      </Tooltip>
    )
  }
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Badge variant="success" className="gap-1">
          <ShieldCheck className="size-3" />
          {t('run.isolated')}
        </Badge>
      </TooltipTrigger>
      <TooltipContent>{t('run.isolatedHint')}</TooltipContent>
    </Tooltip>
  )
}

/** The ephemeral-state-discarded marker — calm neutral, not an alarm. */
export function DestroyedBadge({ destroyed }: { destroyed: boolean }) {
  const { t } = useTranslation('sandbox')
  if (!destroyed) return null
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Badge variant="neutral" className="gap-1">
          <Trash2 className="size-3" />
          {t('run.destroyed')}
        </Badge>
      </TooltipTrigger>
      <TooltipContent>{t('run.destroyedHint')}</TooltipContent>
    </Tooltip>
  )
}

/** The run's score, or an honest "not scored" when degraded (NEVER a fabricated
 *  pass). `score`/`passed` nullable ⇒ unscored. */
export function RunScore({ run }: { run: Run }) {
  const { t } = useTranslation('sandbox')
  const scored = run.score !== null && run.score !== undefined
  if (!scored) {
    return (
      <span className="text-xs text-muted-foreground italic">
        {t('run.notScored')}
      </span>
    )
  }
  return (
    <span className="inline-flex items-center gap-2">
      <span className="font-mono tabular-nums text-foreground">
        {formatScore(run.score)}
      </span>
      {run.passed === true ? (
        <Badge variant="success">{t('run.pass')}</Badge>
      ) : run.passed === false ? (
        <Badge variant="danger">{t('run.fail')}</Badge>
      ) : null}
    </span>
  )
}

// --- runs table --------------------------------------------------------------

export function RunsTable({
  runs,
  onRowClick,
}: {
  runs: Run[]
  onRowClick?: (run: Run) => void
}) {
  const { t, i18n } = useTranslation('sandbox')
  const columns = useMemo<TableColumn<Run>[]>(
    () => [
      {
        accessorKey: 'kind',
        header: t('runs.columns.kind'),
        cell: ({ row }) => (
          <Badge variant="outline">
            {t(`kind.${row.original.kind}`, {
              defaultValue: humanize(row.original.kind),
            })}
          </Badge>
        ),
      },
      {
        accessorKey: 'subject_ref',
        header: t('runs.columns.subject'),
        cell: ({ row }) => (
          <span className="font-mono text-xs text-foreground">
            {row.original.subject_ref}
          </span>
        ),
      },
      {
        accessorKey: 'runner',
        header: t('runs.columns.runner'),
        cell: ({ row }) => <RunnerBadge runner={row.original.runner} />,
      },
      {
        id: 'isolation',
        header: t('runs.columns.isolation'),
        cell: ({ row }) => (
          <div className="flex flex-wrap items-center gap-1.5">
            <IsolatedBadge isolated={row.original.isolated} />
            <DestroyedBadge destroyed={row.original.destroyed} />
          </div>
        ),
      },
      {
        accessorKey: 'status',
        header: t('runs.columns.status'),
        cell: ({ row }) => <StatusBadge status={row.original.status} />,
      },
      {
        id: 'steps',
        header: t('runs.columns.steps'),
        cell: ({ row }) => (
          <span className="font-mono text-xs tabular-nums text-muted-foreground">
            {row.original.steps_ok}/{row.original.steps_total}
            {row.original.steps_error > 0 ? (
              <span className="ml-1 text-danger">
                ({t('runs.errored', { count: row.original.steps_error })})
              </span>
            ) : null}
          </span>
        ),
      },
      {
        id: 'score',
        header: t('runs.columns.score'),
        cell: ({ row }) => <RunScore run={row.original} />,
      },
      {
        accessorKey: 'started_at',
        header: t('runs.columns.started'),
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
    <DataTable<Run>
      columns={columns}
      data={runs}
      getRowId={(r) => r.id}
      onRowClick={onRowClick}
      searchable
      className={cn(onRowClick && 'cursor-pointer')}
      empty={
        <EmptyState
          title={t('empty.runs.title')}
          description={t('empty.runs.description')}
        />
      }
    />
  )
}

// --- per-step outputs --------------------------------------------------------

/** One synthetic step output. Mock-miss is rendered as its deterministic marker
 *  with a distinct (warning) treatment — it never reached a real resource. */
export function OutputRow({ output }: { output: Output }) {
  const { t, i18n } = useTranslation('sandbox')
  return (
    <li className="flex flex-col gap-1 rounded-md border border-border bg-surface p-3">
      <div className="flex items-center justify-between gap-2">
        <span className="font-mono text-xs font-medium text-foreground">
          {output.step_key}
        </span>
        <div className="flex items-center gap-2">
          {output.mock_hit ? (
            <Badge variant="info">{t('outputs.mockHit')}</Badge>
          ) : (
            <Badge variant="warning">{t('outputs.mockMiss')}</Badge>
          )}
          <span className="text-[11px] text-muted-foreground">
            {formatDateTime(output.occurred_at, i18n.language)}
          </span>
        </div>
      </div>
      <pre
        className={cn(
          'overflow-x-auto rounded-sm bg-muted px-2 py-1.5 font-mono text-xs whitespace-pre-wrap',
          output.mock_hit ? 'text-foreground' : 'text-warning',
        )}
      >
        {output.output}
      </pre>
    </li>
  )
}

export function OutputsList({ outputs }: { outputs: Output[] }) {
  const { t } = useTranslation('sandbox')
  return (
    <div className="flex flex-col gap-3">
      <CaveatNotice>{t('outputs.syntheticNote')}</CaveatNotice>
      <ul className="flex flex-col gap-2">
        {outputs.map((o) => (
          // Keyed by row id, not step_key: two steps may legally share a key
          // (modules/sandbox/scenarios.go:259-263 only fills a BLANK one), and React
          // would then collapse/reorder two distinct outputs.
          <OutputRow key={o.id} output={o} />
        ))}
      </ul>
    </div>
  )
}

// --- comparison verdict badge (pre/post-deploy) ------------------------------

// The deploy-gate signal, with the contract's mandated colors. (The shared
// VerdictBadge only maps guardrail verdicts allow/flag/block — these comparison
// verdicts are this module's vocabulary, so we map + localize them here.)
const COMPARISON_VERDICT_VARIANT: Record<ComparisonVerdict, BadgeVariant> = {
  improved: 'success',
  regressed: 'danger',
  unchanged: 'neutral',
  inconclusive: 'warning',
}

export function ComparisonVerdictBadge({
  verdict,
}: {
  verdict: ComparisonVerdict
}) {
  const { t } = useTranslation('sandbox')
  return (
    <Badge variant={COMPARISON_VERDICT_VARIANT[verdict] ?? 'neutral'}>
      {t(`verdict.${verdict}`, { defaultValue: humanize(verdict) })}
    </Badge>
  )
}

// --- comparison card (pre/post-deploy) ---------------------------------------

const VERDICT_BORDER: Record<string, string> = {
  improved: 'border-success-line',
  regressed: 'border-danger-line',
  inconclusive: 'border-warning-line',
}

/** Pre/post-deploy verdict card: verdict badge + baseline vs candidate score +
 *  signed delta. The verdict color is the deploy-gate signal. */
export function ComparisonCard({ comparison }: { comparison: Comparison }) {
  const { t, i18n } = useTranslation('sandbox')
  const delta = comparison.delta
  const deltaTone =
    delta > 0
      ? 'text-success'
      : delta < 0
        ? 'text-danger'
        : 'text-muted-foreground'
  const signedDelta = `${delta > 0 ? '+' : ''}${formatScore(delta)}`
  return (
    <SectionCard className={cn(VERDICT_BORDER[comparison.verdict])}>
      <div className="flex flex-col gap-3">
        <div className="flex items-start justify-between gap-3">
          <div className="min-w-0">
            <div className="flex items-center gap-2">
              <ComparisonVerdictBadge verdict={comparison.verdict} />
              <span className="truncate font-mono text-xs text-muted-foreground">
                {comparison.subject_ref}
              </span>
            </div>
            <p className="mt-1 text-xs text-muted-foreground">
              {t('comparisons.decidedBy')}:{' '}
              <span className="font-mono text-foreground">
                {comparison.decided_by}
              </span>
              {' · '}
              {formatDateTime(comparison.occurred_at, i18n.language)}
            </p>
          </div>
          <div className="shrink-0 text-right">
            <div className="text-[11px] tracking-wide text-muted-foreground uppercase">
              {t('comparisons.delta')}
            </div>
            <div
              className={cn(
                'font-display text-lg font-semibold tabular-nums',
                deltaTone,
              )}
            >
              {signedDelta}
            </div>
          </div>
        </div>
        <div className="grid grid-cols-2 gap-3">
          <ScoreCell
            label={t('comparisons.baseline')}
            score={comparison.baseline_score}
          />
          <ScoreCell
            label={t('comparisons.candidate')}
            score={comparison.candidate_score}
          />
        </div>
        {comparison.suite_ref ? (
          <p className="text-xs text-muted-foreground">
            {t('comparisons.scoredAgainst')}:{' '}
            <span className="font-mono text-foreground">
              {comparison.suite_ref}
            </span>
          </p>
        ) : (
          <p className="text-xs text-muted-foreground italic">
            {t('comparisons.noSuite')}
          </p>
        )}
      </div>
    </SectionCard>
  )
}

function ScoreCell({ label, score }: { label: string; score: number }) {
  return (
    <div className="flex flex-col gap-0.5 rounded-md border border-border bg-muted/40 p-2">
      <span className="text-[11px] tracking-wide text-muted-foreground uppercase">
        {label}
      </span>
      <span className="font-mono text-base font-semibold tabular-nums text-foreground">
        {formatScore(score)}
      </span>
    </div>
  )
}

// --- scenarios table ---------------------------------------------------------

/**
 * How many steps a scenario declares.
 *
 * DERIVED, not read from a `steps_count` the engine never sends: `scenarioDTO`
 * projects the steps THEMSELVES (modules/sandbox/scenarios.go:44-51), so a count
 * field would be a second copy of the same fact for the engine to keep in sync.
 * `Array.isArray` rather than `steps?.length ?? 0` because this is a WIRE payload and
 * the type is only a compile-time claim about it — a non-array `steps` would throw
 * inside a table cell and take the whole panel down, where an honest 0 costs one row.
 */
export function stepCount(scenario: Scenario): number {
  return Array.isArray(scenario.steps) ? scenario.steps.length : 0
}

export function ScenariosTable({
  scenarios,
  onArchive,
}: {
  scenarios: Scenario[]
  /**
   * Archive handler, passed ONLY when the principal holds `sandbox:scenario:admin`.
   * Absent ⇒ the whole column is gone: the action is never offered and then 403'd,
   * and this component stays pure (the permission is decided by the container).
   */
  onArchive?: (scenario: Scenario) => void
}) {
  const { t } = useTranslation('sandbox')
  const columns = useMemo<TableColumn<Scenario>[]>(
    () => [
      {
        accessorKey: 'name',
        header: t('scenarios.columns.name'),
        cell: ({ row }) => (
          <div className="flex flex-col">
            <span className="text-sm font-medium text-foreground">
              {row.original.name}
            </span>
            <span className="text-xs text-muted-foreground">
              {row.original.description}
            </span>
          </div>
        ),
      },
      {
        accessorKey: 'subject_kind',
        header: t('scenarios.columns.subjectKind'),
        // `subject_kind` is optional on the wire and the console can now author a
        // scenario without one, so the absent case is real rather than theoretical. A
        // badge is a classification: an em-dash inside one would assert that "—" is
        // what this scenario exercises. No kind, no badge.
        cell: ({ row }) =>
          row.original.subject_kind ? (
            <Badge variant="neutral">
              {t(`subjectKind.${row.original.subject_kind}`, {
                defaultValue: humanize(row.original.subject_kind),
              })}
            </Badge>
          ) : (
            <span className="text-xs text-muted-foreground">
              {humanize(undefined)}
            </span>
          ),
      },
      {
        id: 'steps',
        header: t('scenarios.columns.steps'),
        cell: ({ row }) => (
          <span className="font-mono text-xs tabular-nums text-muted-foreground">
            {stepCount(row.original)}
          </span>
        ),
      },
      {
        accessorKey: 'status',
        header: t('scenarios.columns.status'),
        cell: ({ row }) => <StatusBadge status={row.original.status} />,
      },
      // The archive action, when the container passed one. An ALREADY-archived
      // scenario gets no button: the engine's archive is idempotent, so offering it
      // again would be a privileged, audited no-op dressed as a state change.
      ...(onArchive
        ? [
            {
              id: 'actions',
              header: t('scenarios.columns.actions'),
              enableSorting: false,
              enableGlobalFilter: false,
              cell: ({ row }: { row: { original: Scenario } }) =>
                row.original.status === 'archived' ? (
                  <span className="sr-only">
                    {t('scenarios.archive.alreadyArchived')}
                  </span>
                ) : (
                  <div className="flex justify-end">
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      onClick={() => onArchive(row.original)}
                    >
                      <Archive className="size-3.5" aria-hidden />
                      {t('scenarios.archive.action')}
                    </Button>
                  </div>
                ),
            },
          ]
        : []),
    ],
    [t, onArchive],
  )
  return (
    <DataTable<Scenario>
      columns={columns}
      data={scenarios}
      getRowId={(r) => r.id}
      searchable
      empty={
        <EmptyState
          title={t('empty.scenarios.title')}
          description={t('empty.scenarios.description')}
        />
      }
    />
  )
}

// --- the data-generation seam (POST-v1, not implemented) ---------------------

/** Synthetic-data generation is a documented POST-v1 extension point that the v1
 *  module does NOT implement — we surface it as a seam, never pretending it works. */
export function SyntheticDataSeam() {
  const { t } = useTranslation('sandbox')
  return (
    <div className="flex items-center gap-2">
      <Lock className="size-3.5 text-muted-foreground" />
      <span className="text-xs text-muted-foreground">
        {t('seam.syntheticData')}
      </span>
    </div>
  )
}
