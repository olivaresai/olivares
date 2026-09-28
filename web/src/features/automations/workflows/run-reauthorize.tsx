// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQueryClient } from '@tanstack/react-query'
import { KeyRound } from 'lucide-react'
import { useId, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { workflowsApi, workflowsKeys } from './api'
import type { ReauthorizeRunResponse, WorkflowRun } from './types'
import './i18n'

/**
 * RunReauthorize is the owning continuation of a run paused for
 * reauthentication (modules/orchestration/workflow_reauthorize.go). It shows
 * the plan hash the operator re-approves and, for an administrator, posts
 * exactly that hash after an explicit confirmation. The engine binds the
 * caller's current credential to the run and resumes the paused steps.
 * The caller renders it only while isReauthenticationGated(run) holds.
 */
export function RunReauthorize({
  workflowId,
  run,
  canAdmin,
}: {
  workflowId: string
  run: WorkflowRun
  canAdmin: boolean
}) {
  const { t } = useTranslation('automations-workflows')
  const { activeTenant } = useAuth()
  const queryClient = useQueryClient()
  const headingId = useId()
  const [confirming, setConfirming] = useState(false)

  const reauthorize = usePrivilegedMutation<string, ReauthorizeRunResponse>({
    mutationFn: (planHash) =>
      workflowsApi.reauthorize(workflowId, run.id, planHash),
    successMessage: t('run.reauthorize.done'),
    // Not `invalidateKeys`: the hook refetches those BEFORE onDone, so seeding
    // the returned run afterwards would replace a fresher read with it. The
    // returned run is shown first; the runs prefix then refreshes the history
    // and this run's detail.
    onDone: (response) => {
      setConfirming(false)
      if (response.run) {
        queryClient.setQueryData(
          workflowsKeys.run(activeTenant, workflowId, response.run.id),
          response.run,
        )
      }
      void queryClient.invalidateQueries({
        queryKey: workflowsKeys.runs(activeTenant, workflowId),
      })
    },
  })

  return (
    <section
      aria-labelledby={headingId}
      className="space-y-3 rounded-lg border border-warning-line bg-warning-soft p-4"
    >
      <h4 id={headingId} className="text-body font-medium text-warning">
        {t('run.reauthorize.title')}
      </h4>
      <p className="text-body text-foreground">
        {t('run.reauthorize.explanation')}
      </p>
      <div>
        <p className="text-caption text-muted-foreground">
          {t('run.reauthorize.planHash')}
        </p>
        <code className="break-all font-mono text-body text-foreground">
          {run.plan_hash}
        </code>
      </div>
      {canAdmin ? (
        <>
          <Button
            variant="primary"
            size="sm"
            disabled={reauthorize.isPending}
            onClick={() => setConfirming(true)}
          >
            <KeyRound aria-hidden />
            {t('run.reauthorize.button')}
          </Button>
          <ConfirmDialog
            open={confirming}
            onOpenChange={setConfirming}
            title={t('run.reauthorize.confirmTitle')}
            description={t('run.reauthorize.confirmDescription')}
            confirmLabel={t('run.reauthorize.confirm')}
            pending={reauthorize.isPending}
            onConfirm={() =>
              // Close on failure so the alert below is not hidden behind the
              // dialog; a step-up resumes this same attempt with the same hash.
              reauthorize.mutate(run.plan_hash, {
                onError: () => setConfirming(false),
              })
            }
          >
            <p className="text-caption">{t('run.reauthorize.confirmPlan')}</p>
            <code className="break-all font-mono text-body text-foreground">
              {run.plan_hash}
            </code>
          </ConfirmDialog>
        </>
      ) : null}
      {reauthorize.error ? (
        <ReauthorizeError
          error={reauthorize.error}
          refresh={() =>
            void queryClient.invalidateQueries({
              queryKey: workflowsKeys.run(activeTenant, workflowId, run.id),
            })
          }
        />
      ) : null}
    </section>
  )
}

/**
 * ReauthorizeError maps a refused reauthorization by status. The engine's
 * refusals carry a message and no code, so the copy covers every refusal a
 * status can mean rather than guessing one from the prose.
 */
export function ReauthorizeError({
  error,
  refresh,
}: {
  error: unknown
  refresh?: () => void
}) {
  const { t } = useTranslation(['automations-workflows', 'common'])
  let message = t('run.reauthorize.failed')
  let refreshable = false
  if (error instanceof ApiError) {
    // Step-up is also a 403: branch on its code first, or an operator whose
    // session needs elevation reads that the credential cannot continue.
    if (error.isStepUpRequired) message = t('common:privileged.stepUp.title')
    else if (error.status === 403) message = t('run.reauthorize.forbidden')
    else if (error.status === 404) message = t('run.reauthorize.notFound')
    else if (error.status === 409) {
      message = t('run.reauthorize.conflict')
      refreshable = true
    } else if (error.status === 503) message = t('run.reauthorize.unavailable')
  }
  return (
    <div
      role="alert"
      className="flex flex-col items-start gap-2 rounded-md border border-danger-line bg-danger-soft p-3 text-body text-danger"
    >
      <span>{message}</span>
      {refreshable && refresh ? (
        <Button size="sm" variant="outline" onClick={refresh}>
          {t('run.reauthorize.refresh')}
        </Button>
      ) : null}
    </div>
  )
}
