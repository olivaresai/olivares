// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE SIGNED-IN PERSON'S SNOOZE AND DONE MARKS (#2480). The engine keeps them, per person,
// like the pin (`modules/sessions/run_mark.go`), and sends them back on the runs it lists
// (`snoozed_until`, `done`), which is all `session-groups.ts` sorts by. This is the one
// action that changes a mark and then re-reads the sessions.
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { toast } from '@/components/ui/toaster'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { sessionsKeys } from './api'
import { primaryRun, type UnifiedSession } from './provenance'

type Session = Pick<UnifiedSession, 'runs'>

export interface SessionMarks {
  /** Snooze the session until `ms` from now. */
  snooze: (session: Session, ms: number) => void
  /** End the snooze now. */
  unsnooze: (session: Session) => void
  /** Mark the session done, or not done. */
  setDone: (session: Session, done: boolean) => void
}

/** The run a new mark goes on: the primary one among those the engine still lists (a
 * released run takes no mark). A session with none is not offered the marks. */
export function markableRun(session: Session) {
  return primaryRun(session.runs.filter((run) => run.state !== 'cleaned'))
}

export function useSessionMarks(): SessionMarks {
  const { t } = useTranslation('sessions')
  const { activeTenant } = useAuth()
  const qc = useQueryClient()
  const { mutate, isPending } = useMutation({
    mutationFn: (calls: readonly (() => Promise<unknown>)[]) =>
      Promise.all(calls.map((call) => call())),
    onError: (err) =>
      toast.error(err instanceof ApiError ? err.message : t('rail.markFailed')),
    // Success or not, the engine's answer is the truth: re-read it.
    onSettled: () => {
      void qc.invalidateQueries({ queryKey: agentOpsKeys.all(activeTenant) })
      void qc.invalidateQueries({ queryKey: sessionsKeys.all(activeTenant) })
    },
  })
  const run = (calls: (() => Promise<unknown>)[]) => {
    if (!isPending && calls.length > 0) mutate(calls)
  }
  const onMarkable = (
    session: Session,
    call: (ref: string) => Promise<unknown>,
  ) => {
    const target = markableRun(session)
    return target ? [() => call(target.run_ref)] : []
  }
  return {
    snooze: (session, ms) =>
      run(
        onMarkable(session, (ref) =>
          agentOpsApi.snooze(ref, new Date(Date.now() + ms).toISOString()),
        ),
      ),
    // Clearing a mark clears it on every run that carries it, as unpinning does.
    unsnooze: (session) =>
      run(
        session.runs
          .filter((r) => !!r.snoozed_until)
          .map((r) => () => agentOpsApi.unsnooze(r.run_ref)),
      ),
    setDone: (session, done) =>
      run(
        done
          ? onMarkable(session, (ref) => agentOpsApi.markDone(ref))
          : session.runs
              .filter((r) => r.done === true)
              .map((r) => () => agentOpsApi.unmarkDone(r.run_ref)),
      ),
  }
}
