// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE TOOL'S BACKGROUND TASKS, ABOVE THE COMPOSER: each one that runs, with its name,
// how long it has run and its own Stop, where the person writes. The list is the engine's
// activity read, shared with the Activity tab; a task that ends leaves the strip. Stop is the
// tool's own (Claude Code's stop_task): the session and its turn keep running.
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Square } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { toast } from '@/components/ui/toaster'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import type { ActivityTaskDTO, RunDTO } from '@/features/agentops/types'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { formatDuration } from '@/lib/format'
import { useConversationTasks } from './use-conversation-tasks'
import { useNow } from './use-now'
import './i18n'

const taskName = (task: ActivityTaskDTO) =>
  task.description || task.command || task.kind

export function BackgroundTaskStrip({ run }: { run: RunDTO }) {
  const { t } = useTranslation('sessions')
  const { activeTenant, can } = useAuth()
  const queryClient = useQueryClient()
  const { data } = useConversationTasks(run, activeTenant)
  const live =
    run.state === 'running' || run.state === 'idle'
      ? (data?.tasks ?? []).filter(
          (task) => task.state === 'running' && task.task_id,
        )
      : []
  const now = useNow(live.length > 0)
  const stop = useMutation({
    mutationFn: (taskId: string) => agentOpsApi.stopTask(run.run_ref, taskId),
    onSuccess: () =>
      queryClient.invalidateQueries({
        queryKey: agentOpsKeys.runActivity(activeTenant, run.run_ref),
      }),
    onError: (err) =>
      toast.error(
        err instanceof ApiError ? err.message : t('backgroundTasks.stopFailed'),
      ),
  })
  if (live.length === 0) return null
  // Only Claude Code takes a stop for one task; the engine refuses it for any other tool.
  const canStop = can('sessions:run:write') && run.provider_driver === 'claude'
  return (
    <ul
      aria-label={t('backgroundTasks.label')}
      data-testid="background-task-strip"
      className="m-0 flex list-none flex-wrap gap-1.5 px-3 pt-2"
    >
      {live.map((task) => {
        const name = taskName(task)
        const started = task.started_at ? Date.parse(task.started_at) : NaN
        return (
          <li
            key={task.task_id}
            className="flex min-w-0 items-center gap-1.5 rounded-full border border-line bg-muted py-0.5 ps-2.5 pe-0.5 text-caption text-text-2"
          >
            <span
              aria-hidden
              className="size-1.5 shrink-0 rounded-full bg-ok"
            />
            <span
              className="min-w-0 max-w-64 truncate text-text"
              title={task.command ?? name}
            >
              {name}
            </span>
            {Number.isFinite(started) ? (
              <span className="tabular-nums">
                {formatDuration(Math.max(0, now - started))}
              </span>
            ) : null}
            {canStop ? (
              <Button
                type="button"
                size="icon-sm"
                variant="ghost"
                className="rounded-full"
                aria-label={t('backgroundTasks.stop', { name })}
                title={t('backgroundTasks.stop', { name })}
                disabled={stop.isPending && stop.variables === task.task_id}
                onClick={() => stop.mutate(task.task_id!)}
              >
                <Square className="size-3 fill-current" aria-hidden />
              </Button>
            ) : (
              <span className="pe-2" />
            )}
          </li>
        )
      })}
    </ul>
  )
}
