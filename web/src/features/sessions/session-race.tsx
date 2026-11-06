// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import {
  QueryClient,
  QueryClientProvider,
  useMutation,
  useQueries,
} from '@tanstack/react-query'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { agentOpsApi } from '@/features/agentops/api'
import { useAuthBoundary } from '@/features/agentops/auth-boundary'
import { currentControlFence } from '@/features/agentops/work-fence'
import type { RunDTO } from '@/features/agentops/types'
import { useAuth } from '@/lib/auth/context'
import { formatMicroUsd } from '@/lib/format'
import { useStepUpOwner, type StepUpAttempt } from '@/stores/step-up'
import { SessionBranchChanges } from './session-branch-changes'
import { SessionChanges } from './session-changes'
import { SessionGit } from './session-git'
import './i18n'

export function SessionRace({ refs }: { refs: string[] }) {
  const boundary = useAuthBoundary()
  return <RaceCache key={`${boundary.epoch}:${refs.join(',')}`} refs={refs} />
}

// Existing diff components use tenant-scoped keys. Keep this comparison's cache
// with the principal and credential episode so their results cannot cross it.
function RaceCache({ refs }: { refs: string[] }) {
  const [client] = useState(
    () => new QueryClient({ defaultOptions: { queries: { retry: false } } }),
  )
  useEffect(
    () => () => {
      client.clear()
    },
    [client],
  )
  return (
    <QueryClientProvider client={client}>
      <RacePanel refs={refs} />
    </QueryClientProvider>
  )
}

function RacePanel({ refs }: { refs: string[] }) {
  const { t } = useTranslation('sessions')
  const { activeTenant, can } = useAuth()
  const captureOwner = useStepUpOwner()
  const [selected, setSelected] = useState<RunDTO | null>(null)
  const [kept, setKept] = useState<string | null>(null)
  const [merged, setMerged] = useState<string | null>(null)
  const queries = useQueries({
    queries: refs.map((ref) => ({
      queryKey: ['race', activeTenant, ref],
      queryFn: ({ signal }: { signal: AbortSignal }) =>
        agentOpsApi.getRun(ref, { tenant: activeTenant, signal }),
      refetchInterval: kept ? (false as const) : 5_000,
    })),
  })
  const runs = queries.flatMap((query) => (query.data ? [query.data] : []))
  const valid =
    runs.length === refs.length &&
    refs.length >= 2 &&
    new Set(refs).size === refs.length &&
    runs.every(
      (run) =>
        run.worktree_branch &&
        run.workspace_ref &&
        run.authz_workspace_id &&
        run.workspace_ref === runs[0].workspace_ref &&
        run.authz_workspace_id === runs[0].authz_workspace_id,
    )
  const mayKeep =
    can('sessions:run:write') &&
    can('sessions:stop:write') &&
    can('sessions:run:admin')
  const keep = useMutation({
    mutationFn: async ({
      winner,
      attempt,
    }: {
      winner: RunDTO
      attempt: StepUpAttempt
    }) => {
      const guard = () => {
        attempt.dispatchGuard()
        if (!valid || !mayKeep) throw new Error(t('race.invalid'))
      }
      const options = {
        tenant: activeTenant,
        signal: attempt.signal,
        dispatchGuard: guard,
        sessionEffects: attempt.sessionEffects,
      }
      // Stop every writer before merging. A refusal preserves every branch.
      for (const run of runs) {
        guard()
        const fence = await currentControlFence(run, options)
        guard()
        const stopped = await agentOpsApi.stop(run.run_ref, fence, options)
        if (
          !['stopped', 'failed', 'cleaned', 'declined', 'expired'].includes(
            stopped.state,
          )
        )
          throw new Error(t('race.notStopped'))
      }
      guard()
      await agentOpsApi.mergeWorktree(winner.run_ref, options)
      guard()
      setMerged(winner.run_ref)
      for (const run of runs) {
        if (run.run_ref === winner.run_ref) continue
        guard()
        await agentOpsApi.cleanup(run.run_ref, true, options)
      }
      attempt.dispatchGuard()
      return winner.run_ref
    },
    onSuccess: (ref) => {
      setKept(ref)
      setSelected(null)
    },
  })
  const failure =
    keep.error instanceof Error ? keep.error.message : t('race.failed')
  const errorText = merged ? `${t('race.cleanupFailed')} ${failure}` : failure

  return (
    <section
      data-testid="session-race"
      className="flex min-w-0 flex-col gap-4 p-4"
    >
      <h1 className="text-title font-semibold">{t('race.title')}</h1>
      <p className="text-body text-muted-foreground">{t('race.hint')}</p>
      {queries.some((query) => query.isError) ? (
        <p role="alert" className="text-danger">
          {t('race.loadError')}
        </p>
      ) : queries.some((query) => query.isPending) ? (
        <p role="status">{t('branchChanges.loading')}</p>
      ) : !valid ? (
        <p role="alert" className="text-danger">
          {t('race.invalid')}
        </p>
      ) : (
        <div className="grid min-w-0 grid-cols-1 gap-4 md:auto-cols-[minmax(20rem,1fr)] md:grid-flow-col md:grid-cols-none md:overflow-x-auto">
          {runs
            .filter((run) => !kept || run.run_ref === kept)
            .map((run) => (
              <article
                data-testid="race-choice"
                key={run.run_ref}
                className="flex min-w-0 flex-col gap-3 rounded-lg border border-border p-3"
              >
                <h2 className="break-words text-body font-semibold">
                  <a
                    className="underline underline-offset-2"
                    href={`/agentops?session=run:${encodeURIComponent(run.run_ref)}`}
                  >
                    {run.name || run.provider_driver || run.run_ref}
                  </a>
                </h2>
                <p
                  title={t('meter.costTitle')}
                  className="text-caption text-muted-foreground"
                >
                  {run.cost_micro_usd == null
                    ? t('meter.costUnknown')
                    : formatMicroUsd(run.cost_micro_usd)}
                </p>
                <SessionBranchChanges run={run} />
                <RaceEdits run={run} />
                {mayKeep && !kept ? (
                  <Button
                    size="sm"
                    disabled={
                      keep.isPending ||
                      (merged !== null && merged !== run.run_ref)
                    }
                    onClick={() => {
                      keep.reset()
                      setSelected(run)
                    }}
                  >
                    {t('race.keep')}
                  </Button>
                ) : null}
              </article>
            ))}
        </div>
      )}
      {kept ? <p role="status">{t('race.kept')}</p> : null}
      {keep.isError ? (
        <p role="alert" className="text-danger">
          {errorText}
        </p>
      ) : null}
      <ConfirmDialog
        open={selected !== null}
        onOpenChange={(open) => {
          if (!open) setSelected(null)
        }}
        title={t('race.keep')}
        description={t('race.confirm', {
          name: selected?.name || selected?.run_ref,
        })}
        confirmLabel={t('race.confirmKeep')}
        pending={keep.isPending}
        confirmDisabled={!valid || !mayKeep}
        tone="danger"
        onConfirm={() => {
          if (selected && valid && mayKeep)
            keep.mutate({ winner: selected, attempt: captureOwner().begin() })
        }}
      >
        {keep.isError ? (
          <p role="alert" className="text-danger">
            {errorText}
          </p>
        ) : null}
      </ConfirmDialog>
    </section>
  )
}

function RaceEdits({ run }: { run: RunDTO }) {
  const { t } = useTranslation('sessions')
  const [open, setOpen] = useState(false)
  return (
    <details onToggle={(event) => setOpen(event.currentTarget.open)}>
      <summary className="cursor-pointer text-caption">
        {t('race.uncommitted')}
      </summary>
      {open ? (
        <>
          <SessionChanges run={run} />
          <SessionGit run={run} />
        </>
      ) : null}
    </details>
  )
}
