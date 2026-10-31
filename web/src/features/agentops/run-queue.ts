// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE MESSAGES A PERSON LINES UP WHILE THE TOOL WORKS.
//
// The queue is the engine's: saved per session on the server, so it survives a reload and
// shows on every device. This file reads it, writes it, and answers the one question the
// composer asks on Enter: queue behind the running turn, or send now. It adds no state of
// its own and no steer of its own; the engine says whether the session's tool takes a
// message into a running turn (`can_steer`).
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { toast } from '@/components/ui/toaster'
import { useAuth } from '@/lib/auth/context'
import { agentOpsApi, agentOpsKeys } from './api'
import { launchFailureMessage } from './launch-readiness'
import type { QueuedMessageDTO, RunDTO, RunQueueDTO } from './types'

/** How often a visible queue is read again: it empties by itself when the turn ends. */
const QUEUE_REFRESH_MS = 2000

/** How Enter treats a message: lined up behind the running turn, or sent now. */
export type SendMode = 'queue' | 'send'

/**
 * Queue while a turn runs or while others already wait (a message sent past them would
 * overtake them); send otherwise. `steer` is the person's other choice (Ctrl+Enter):
 * send into the running turn, when the tool takes one. An engine without a queue gives
 * no answer here, and the message is sent as it always was.
 */
export function sendMode(
  queue: RunQueueDTO | undefined,
  steer: boolean,
): SendMode {
  if (!queue) return 'send'
  if (queue.items.length === 0 && queue.hold !== 'turn') return 'send'
  return steer && queue.can_steer ? 'send' : 'queue'
}

/** The session's queue; read while the session can take a message, and again every
 * couple of seconds while something waits in it or a turn runs (the field says what
 * Enter does from this answer). */
export function useRunQueue(
  run: Pick<RunDTO, 'run_ref' | 'state'>,
  enabled: boolean,
) {
  const { activeTenant } = useAuth()
  return useQuery({
    queryKey: agentOpsKeys.runQueue(activeTenant, run.run_ref),
    queryFn: ({ signal }) => agentOpsApi.queue(run.run_ref, { signal }),
    enabled: enabled && !!activeTenant,
    refetchInterval: (query) =>
      query.state.data?.items.length || run.state === 'running'
        ? QUEUE_REFRESH_MS
        : false,
    retry: false,
  })
}

/** The writes on one session's queue; each leaves the cache as the engine answers. */
export function useQueueActions(run: Pick<RunDTO, 'run_ref'>) {
  const { t } = useTranslation()
  const { activeTenant } = useAuth()
  const qc = useQueryClient()
  const key = agentOpsKeys.runQueue(activeTenant, run.run_ref)
  const refresh = () => qc.invalidateQueries({ queryKey: key })
  const keep = (state: RunQueueDTO) => qc.setQueryData(key, state)
  const onError = (error: unknown) => {
    toast.error(launchFailureMessage(error, t('nav:launcher.queue.failed')))
    void refresh()
  }
  return {
    edit: useMutation({
      mutationFn: (p: { id: string; text: string }) =>
        agentOpsApi.queueEdit(run.run_ref, p.id, p.text),
      onSuccess: refresh,
      onError,
    }),
    order: useMutation({
      mutationFn: (ids: string[]) => agentOpsApi.queueOrder(run.run_ref, ids),
      onSuccess: keep,
      onError,
    }),
    remove: useMutation({
      mutationFn: (id: string) => agentOpsApi.queueRemove(run.run_ref, id),
      onSuccess: keep,
      onError,
    }),
    send: useMutation({
      mutationFn: (id: string) => agentOpsApi.queueSteer(run.run_ref, id),
      onSuccess: refresh,
      onError,
    }),
  }
}

/** Queue a message after the others. `onQueued` runs once the engine has it, which is
 * when the composer lets go of the text. */
export function useQueueTurn(
  run: Pick<RunDTO, 'run_ref'>,
  onQueued: (text: string) => void,
) {
  const { t } = useTranslation()
  const { activeTenant } = useAuth()
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (text: string) => agentOpsApi.queueAdd(run.run_ref, text),
    onSuccess: (_item, text) => {
      onQueued(text)
      void qc.invalidateQueries({
        queryKey: agentOpsKeys.runQueue(activeTenant, run.run_ref),
      })
    },
    onError: (error: unknown) =>
      toast.error(launchFailureMessage(error, t('nav:launcher.queue.failed'))),
  })
}

/**
 * Say what a release threw away. Releasing a session drops its queue; the messages are
 * shown here, and one action copies them, so nothing the person typed goes unseen.
 */
export function useDiscardedQueueNotice() {
  const { t } = useTranslation()
  return (items: QueuedMessageDTO[] | undefined) => {
    if (!items?.length) return
    const text = items.map((item) => item.text).join('\n\n')
    toast.warning(t('nav:launcher.queue.discarded'), {
      description: text,
      duration: Infinity,
      action: {
        label: t('nav:launcher.queue.copy'),
        onClick: () => void navigator.clipboard.writeText(text),
      },
    })
  }
}
