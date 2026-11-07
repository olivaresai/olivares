// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE SIGNED-IN PERSON'S SNOOZE, DONE AND UNREAD MARKS. The engine keeps
// them, per person, like the pin (`modules/sessions/run_mark.go`), and sends them back on
// the runs it lists (`snoozed_until`, `done`, `unread`), which is all `session-groups.ts`
// sorts by. This is the one action that changes a mark and then re-reads the sessions,
// for the thread header's menu and the row menu alike.
//
// Snoozing and marking done are undone, not confirmed (`list-undo.ts`): the notice that
// says what was done carries Undo.
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useEffect, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from '@/components/ui/toaster'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import type { RunMarkDTO } from '@/features/agentops/types'
import { sessionNameLadder } from '@/features/home/work-line'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { sessionsKeys } from './api'
import { useOfferUndo } from './list-undo'
import { primaryRun, type UnifiedSession } from './provenance'

type Session = Pick<UnifiedSession, 'runs'>

export interface SessionMarks {
  /** Snooze the session until `ms` from now. */
  snooze: (session: Session, ms: number) => void
  /** End the snooze now. */
  unsnooze: (session: Session) => void
  /** Mark the session done, or not done. */
  setDone: (session: Session, done: boolean) => void
  /** Mark the session unread, or read. */
  setUnread: (session: Session, unread: boolean) => void
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
  const offerUndo = useOfferUndo()
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
  const run = (
    calls: (() => Promise<unknown>)[],
    onDone?: (answers: unknown[]) => void,
  ) => {
    if (!isPending && calls.length > 0)
      mutate(calls, { onSuccess: (answers) => onDone?.(answers) })
  }
  const onMarkable = (
    session: Session,
    call: (ref: string) => Promise<unknown>,
  ) => {
    const target = markableRun(session)
    return target ? [() => call(target.run_ref)] : []
  }
  const nameOf = (target: NonNullable<ReturnType<typeof markableRun>>) =>
    sessionNameLadder(target.name, null, t('untitled'), target.title).text
  return {
    snooze: (session, ms) => {
      const target = markableRun(session)
      const before = target?.snoozed_until
      run(
        onMarkable(session, (ref) =>
          agentOpsApi.snooze(ref, new Date(Date.now() + ms).toISOString()),
        ),
        ([set]) => {
          const until = (set as RunMarkDTO).snoozed_until
          if (!target || !until) return
          offerUndo({
            message: t('undo.snoozed', { name: nameOf(target) }),
            expect: [
              {
                ref: target.run_ref,
                still: (now) => now.snoozed_until === until,
              },
            ],
            // The snooze the session had before, when it had one.
            restore: () =>
              before
                ? agentOpsApi.snooze(target.run_ref, before)
                : agentOpsApi.unsnooze(target.run_ref),
          })
        },
      )
    },
    // Clearing a mark clears it on every run that carries it, as unpinning does.
    unsnooze: (session) =>
      run(
        session.runs
          .filter((r) => !!r.snoozed_until)
          .map((r) => () => agentOpsApi.unsnooze(r.run_ref)),
      ),
    setDone: (session, done) => {
      const target = markableRun(session)
      run(
        done
          ? onMarkable(session, (ref) => agentOpsApi.markDone(ref))
          : session.runs
              .filter((r) => r.done === true)
              .map((r) => () => agentOpsApi.unmarkDone(r.run_ref)),
        done && target
          ? () =>
              offerUndo({
                message: t('undo.done', { name: nameOf(target) }),
                expect: [
                  { ref: target.run_ref, still: (now) => now.done === true },
                ],
                restore: () => agentOpsApi.unmarkDone(target.run_ref),
              })
          : undefined,
      )
    },
    setUnread: (session, unread) =>
      run(
        unread
          ? onMarkable(session, (ref) => agentOpsApi.markUnread(ref))
          : session.runs
              .filter((r) => r.unread === true)
              .map((r) => () => agentOpsApi.markRead(r.run_ref)),
      ),
  }
}

/**
 * OPENING A SESSION MARKS IT READ, and so does what it does while it stays open on a tab
 * the person can see: a session they are looking at is not one they have to come back
 * to. The mark goes once per run and last-activity moment, so Mark unread on the open
 * session stays until it does something new or is opened again.
 *
 * ⛔ A FAILED MARK SAYS NOTHING. It is background bookkeeping, not what the person asked
 *    for; the list is read again either way, so the dot shows what the engine holds.
 */
export function useMarkSeen(session: Session, enabled: boolean) {
  const { activeTenant } = useAuth()
  const qc = useQueryClient()
  const { mutate } = useMutation({
    mutationFn: (ref: string) => agentOpsApi.markRead(ref),
    onSettled: () => {
      void qc.invalidateQueries({ queryKey: agentOpsKeys.all(activeTenant) })
      void qc.invalidateQueries({ queryKey: sessionsKeys.all(activeTenant) })
    },
  })
  const target = markableRun(session)
  const ref = target?.run_ref
  const activity = target?.last_activity_at
  const sent = useRef('')
  useEffect(() => {
    if (!enabled || !ref) return
    const key = `${ref}@${activity ?? ''}`
    const send = () => {
      if (document.visibilityState !== 'visible' || sent.current === key) return
      sent.current = key
      mutate(ref)
    }
    send()
    document.addEventListener('visibilitychange', send)
    return () => document.removeEventListener('visibilitychange', send)
  }, [enabled, ref, activity, mutate])
}
