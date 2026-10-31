// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { ListTruncationBadge } from '@/features/_intel'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import { RunStateBadge } from '@/features/agentops/run-state-badge'
import type { RunDTO } from '@/features/agentops/types'
import { isLiveRun, LIVE_RUN_STATES } from '@/features/sessions/provenance'
import { RelTime } from '@/features/shared/rel-time'
import { useAuth } from '@/lib/auth/context'
import './i18n'
import type { KillSwitchDTO } from './types'

// The engine's list ceiling, the same one the stops list asks for.
const RUNS_PAGE = 1000
// The runs list pages newest first, so a long-lived session falls off an unfiltered page
// first. Each live state is therefore its own STORE-filtered read (runtime_api.go
// handleListRuns): idle is derived from stored running, so it is asked for by name.
// While a stop is ending sessions the engine finishes every run within 15 s
// (modules/sessions/runtime_killswitch.go), so the list follows it closely; otherwise
// it keeps the page's calm cadence.
const FLIP_POLL_MS = 2_000
const CALM_POLL_MS = 12_000

/** When the active estate stop began, in ms, or null when every agent may run. No run
 *  can start while it is in force, so the runs that ended since are the ones it ended. */
export function estateSince(active: KillSwitchDTO[]): number | null {
  const estate = active.find((s) => s.scope_kind === 'estate')
  const ms = Date.parse(estate?.engaged_at ?? '')
  return Number.isNaN(ms) ? null : ms
}

/** One row per run: every live run, and every run that ended since the estate stop
 *  began, so a row reads Working, then Stopped. The reads race the stop, so a run seen
 *  both live and ended is shown ended; newest first, as the engine lists them. */
export function watchedRuns(runs: RunDTO[], since: number | null): RunDTO[] {
  const byRef = new Map<string, RunDTO>()
  for (const run of runs) {
    const seen = byRef.get(run.run_ref)
    if (!seen || (isLiveRun(seen) && !isLiveRun(run)))
      byRef.set(run.run_ref, run)
  }
  return [...byRef.values()]
    .filter((run) => {
      if (isLiveRun(run)) return true
      if (since === null) return false
      const stopped = Date.parse(run.stopped_at ?? '')
      return !Number.isNaN(stopped) && stopped >= since
    })
    .sort((a, b) => (b.created_at ?? '').localeCompare(a.created_at ?? ''))
}

/** Poll fast only while an estate stop is in force and some run is still live. */
export function pollMs(since: number | null, runs: RunDTO[]): number {
  return since !== null && runs.some(isLiveRun) ? FLIP_POLL_MS : CALM_POLL_MS
}

/** The live session list of the kill-switch screen. */
export function LiveRunsSection({ active }: { active: KillSwitchDTO[] }) {
  const { t } = useTranslation('killswitch')
  const { activeTenant } = useAuth()
  const since = estateSince(active)
  const states: string[] =
    since === null ? [...LIVE_RUN_STATES] : [...LIVE_RUN_STATES, 'stopped']

  const runs = useQuery({
    // Under the runs key, so the engage's invalidation refreshes it at once.
    queryKey: [...agentOpsKeys.runs(activeTenant), 'killswitch', states],
    queryFn: async ({ signal }) => {
      const reads = await Promise.all(
        states.map((state) =>
          agentOpsApi.listRuns(
            { state, limit: RUNS_PAGE },
            { tenant: activeTenant, signal },
          ),
        ),
      )
      return {
        items: reads.flatMap((r) => r.items),
        has_more: reads.some((r) => r.has_more),
      }
    },
    // When the estate stop lands the key gains the ended read: keep the rows on screen
    // until it answers, never another tenant's (the tenant is the key's second part).
    placeholderData: (previous, previousQuery) =>
      previousQuery?.queryKey[1] === activeTenant ? previous : undefined,
    refetchInterval: (query) => pollMs(since, query.state.data?.items ?? []),
  })
  const rows = watchedRuns(runs.data?.items ?? [], since)
  const live = rows.filter(isLiveRun).length
  // "Nothing is running" is said only when every read answered in full.
  const complete = runs.isSuccess && !runs.data.has_more

  return (
    <section
      className="flex flex-col gap-3"
      aria-labelledby="ks-runs-title"
      data-testid="killswitch-runs"
    >
      <div>
        <h2 id="ks-runs-title" className="text-heading">
          {t('runs.title')}
        </h2>
        <p className="text-caption text-muted-foreground">
          {since === null ? t('runs.caption') : t('runs.captionStopped')}
        </p>
      </div>

      <ListTruncationBadge
        query={runs}
        label={t('runs.truncated', { n: RUNS_PAGE })}
        hint={t('runs.truncatedHint')}
      />

      {/* A failed poll keeps the rows it already had: they are the last known
          state, and an empty page mid-incident would read as "all stopped". */}
      {runs.isError && (
        <p role="alert" className="text-body text-danger">
          {t('runs.error')}
        </p>
      )}

      {/* One announcement per change of the counts, not one per row or per tick of
          a relative time. */}
      <p role="status" className="sr-only">
        {runs.data ? t('runs.status', { live, ended: rows.length - live }) : ''}
      </p>

      {rows.length > 0 ? (
        <ul className="flex flex-col divide-y divide-border rounded-lg border border-border">
          {rows.map((run) => (
            <li
              key={run.run_ref}
              className="flex min-w-0 items-center justify-between gap-3 px-3 py-2"
            >
              <span
                className="min-w-0 truncate"
                title={run.name || run.run_ref}
              >
                {run.name || (
                  <span className="font-mono text-caption">{run.run_ref}</span>
                )}
              </span>
              <span className="flex shrink-0 items-center gap-2">
                <RelTime
                  ts={isLiveRun(run) ? run.started_at : run.stopped_at}
                  className="whitespace-nowrap text-caption text-muted-foreground"
                />
                <RunStateBadge run={run} />
              </span>
            </li>
          ))}
        </ul>
      ) : complete ? (
        <p className="text-body text-muted-foreground">{t('runs.empty')}</p>
      ) : null}
    </section>
  )
}
