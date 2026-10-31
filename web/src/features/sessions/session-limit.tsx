// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A turn the tool ended on a usage or rate limit: one sentence in the session header, and
// the Move to action where the person can use it. The fact is the run's `limit`, taken from
// the tool's own result by the engine; this only says it.
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import { isWorkBound } from '@/features/agentops/work-fence'
import { useAuth } from '@/lib/auth/context'
import { ApiError } from '@/lib/api/errors'
import { toast } from '@/components/ui/toaster'
import { sessionsKeys } from './api'
import { LimitResumePreference } from './limit-resume-preference'
import { ArrowRightLeft } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import type { RunDTO } from '@/features/agentops/types'
import type { Capability } from './provenance'
import { canMoveSession } from './provenance'
import { limitLifted, limitSentence } from './session-limit-text'
import { MoveSessionDialog } from './session-move'
import './i18n'

export function LimitNotice({
  run,
  caps,
}: {
  run: RunDTO
  caps: Capability[]
}) {
  const { t } = useTranslation('sessions')
  const [moveOpen, setMoveOpen] = useState(false)
  const { activeTenant, can } = useAuth()
  const qc = useQueryClient()
  const eligible =
    run.limit?.kind === 'usage' &&
    !!run.limit.resets_at &&
    can('sessions:run:write') &&
    caps.some(
      (cap) => (cap.id === 'stop' || cap.id === 'resume') && cap.available,
    ) &&
    !isWorkBound(run) &&
    !run.archived_at
  const canSnooze =
    run.limit?.kind === 'usage' &&
    !!run.limit.resets_at &&
    !run.archived_at &&
    run.state !== 'cleaned'
  const action = useMutation({
    mutationFn: async (choice: 'schedule' | 'cancel' | 'snooze') => {
      if (choice === 'snooze')
        return agentOpsApi.snooze(run.run_ref, run.limit!.resets_at!)
      return choice === 'cancel'
        ? agentOpsApi.cancelResumeAtReset(run.run_ref)
        : agentOpsApi.resumeAtReset(run.run_ref)
    },
    onSuccess: (_result, choice) => {
      void qc.invalidateQueries({ queryKey: agentOpsKeys.all(activeTenant) })
      void qc.invalidateQueries({ queryKey: sessionsKeys.all(activeTenant) })
      if (choice === 'cancel') toast.success(t('limit.cancelled'))
    },
    onError: (err) =>
      toast.error(err instanceof ApiError ? err.message : t('move.failed')),
  })
  if (
    !run.limit ||
    (limitLifted(run.limit) && !run.resume_at && !run.resume_cancelled)
  )
    return null
  // A live session is stopped first and a stopped one moves as it is: either way the
  // person needs the control that gets it there.
  const canMove =
    canMoveSession(run) &&
    caps.some((c) => (c.id === 'stop' || c.id === 'resume') && c.available)
  return (
    <span
      data-testid="thread-limit"
      className="flex min-w-0 flex-wrap items-center gap-1"
    >
      <span className="truncate text-caption font-medium text-warning">
        {limitSentence(run.limit, t)}
      </span>
      {run.resume_cancelled && (
        <span role="status">{t('limit.cancelled')}</span>
      )}
      {eligible && (
        <>
          {run.resume_at ? (
            <>
              <span role="status">
                {t('limit.resumes', {
                  time: new Date(run.resume_at).toLocaleString(),
                })}
              </span>
              <Button
                variant="ghost"
                size="sm"
                disabled={action.isPending}
                onClick={() => action.mutate('cancel')}
              >
                {t('limit.cancel')}
              </Button>
            </>
          ) : (
            <Button
              variant="ghost"
              size="sm"
              disabled={action.isPending || limitLifted(run.limit)}
              onClick={() => action.mutate('schedule')}
            >
              {t('limit.resumeAtReset')}
            </Button>
          )}
          <LimitResumePreference compact />
        </>
      )}
      {canSnooze && (
        <Button
          variant="ghost"
          size="sm"
          disabled={action.isPending || limitLifted(run.limit)}
          onClick={() => action.mutate('snooze')}
        >
          {t('limit.snoozeUntilReset')}
        </Button>
      )}
      {canMove && run.limit.kind === 'usage' ? (
        <>
          <Button
            variant="ghost"
            size="sm"
            data-testid="thread-limit-move"
            onClick={() => setMoveOpen(true)}
          >
            <ArrowRightLeft />
            {t('move.open')}
          </Button>
          {moveOpen && (
            <MoveSessionDialog
              key={run.run_ref}
              run={run}
              onClose={() => setMoveOpen(false)}
            />
          )}
        </>
      ) : null}
    </span>
  )
}
