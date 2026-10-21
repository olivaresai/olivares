// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// WHAT THE TOOL IS DOING, beside its conversation (#1861): its own task list, the agents it
// spawned, the work it left running in the background, the files its edits changed and what
// its context holds. The engine reads each item from the transcript the tool keeps
// (modules/sessions/session_activity.go), so a reload or another browser shows the same; the
// conversation's live stream asks for a fresh read whenever the tool writes
// (session-conversation.tsx). One calm panel: sections fold, nothing shows the wire.
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import {
  AlarmClock,
  Bot,
  Check,
  ChevronRight,
  Circle,
  CircleDot,
  Eye,
  FilePen,
  FilePlus,
  Terminal,
  Workflow,
} from 'lucide-react'
import { useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import type { TFunction } from 'i18next'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { StatusGlyph, type Status } from '@/components/ui/status-glyph'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import type {
  ActivityAgentDTO,
  ActivityState,
  ActivityTaskDTO,
  RunActivityDTO,
  RunDTO,
} from '@/features/agentops/types'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { formatDuration, formatInt, formatPercent } from '@/lib/format'
import { cn } from '@/lib/utils'
import './i18n'

const GLYPH: Record<ActivityState, Status> = {
  running: 'working',
  completed: 'done',
  failed: 'failed',
  stopped: 'paused',
  scheduled: 'idle',
}

const TASK_ICON: Record<string, typeof Terminal> = {
  shell: Terminal,
  monitor: Eye,
  workflow: Workflow,
  wakeup: AlarmClock,
}

function StateGlyph({ state }: { state: ActivityState }) {
  const { t } = useTranslation('sessions')
  return (
    <StatusGlyph
      status={GLYPH[state] ?? 'idle'}
      label={t(`activity.state.${state}`, { defaultValue: state })}
      showLabel={false}
    />
  )
}

function Section({
  title,
  count,
  children,
  testId,
}: {
  title: string
  count?: string
  children: ReactNode
  testId: string
}) {
  return (
    <details open data-testid={testId} className="group">
      <summary className="flex cursor-pointer list-none items-center gap-1.5 py-1 text-caption font-medium text-foreground">
        <ChevronRight
          aria-hidden
          className="size-3.5 text-muted-foreground transition-transform group-open:rotate-90 motion-reduce:transition-none"
        />
        <span>{title}</span>
        {count ? (
          <span className="tabular-nums text-muted-foreground">{count}</span>
        ) : null}
      </summary>
      <div className="mt-1 flex flex-col gap-0.5 pl-5">{children}</div>
    </details>
  )
}

/** A file inside the session's folder is named by its place in it. */
function displayPath(path: string, folder?: string | null): string {
  if (folder) {
    const root = folder.endsWith('/') ? folder : `${folder}/`
    if (path.startsWith(root)) return path.slice(root.length)
  }
  return path
}

function elapsed(item: {
  duration_ms?: number
  started_at?: string
  ended_at?: string
}): string | null {
  if (item.duration_ms) return formatDuration(item.duration_ms)
  if (item.started_at && item.ended_at) {
    const ms = Date.parse(item.ended_at) - Date.parse(item.started_at)
    if (Number.isFinite(ms) && ms >= 0) return formatDuration(ms)
  }
  return null
}

function countLabel(t: TFunction, total: number, running: number): string {
  return running
    ? t('activity.countRunning', { total, running })
    : formatInt(total)
}

export function SessionActivity({
  run,
  onOpenChanges,
}: {
  run: RunDTO
  /** Opens the session's Changes, where a file's text and its diff are read. */
  onOpenChanges?: () => void
}) {
  const { t } = useTranslation(['sessions', 'common'])
  const { activeTenant } = useAuth()
  const query = useQuery({
    // The run's state is in the key: a session that stops ends what ran under it.
    queryKey: [
      ...agentOpsKeys.runActivity(activeTenant, run.run_ref),
      run.state,
    ],
    queryFn: ({ signal }) => agentOpsApi.activity(run.run_ref, { signal }),
    placeholderData: keepPreviousData,
  })

  if (query.isPending)
    return (
      <div
        aria-busy="true"
        data-testid="activity-loading"
        className="flex flex-col gap-2"
      >
        <Skeleton className="h-5 w-2/3" />
        <Skeleton className="h-5 w-full" />
        <Skeleton className="h-5 w-1/2" />
      </div>
    )
  if (query.isError)
    return (
      <div
        role="alert"
        data-testid="activity-failed"
        className="flex items-start gap-2 text-caption text-warning"
      >
        {/* The engine says why it does not show a session's activity (another tool, a
            session that held secrets); anything else is a failed read. */}
        <span className="min-w-0 flex-1">
          {query.error instanceof ApiError && query.error.status < 500
            ? query.error.message
            : t('activity.failed')}
        </span>
        <Button
          variant="secondary"
          size="sm"
          onClick={() => void query.refetch()}
        >
          {t('common:actions.retry')}
        </Button>
      </div>
    )
  return (
    // A state change asks again; until the answer comes, what is shown is the last one.
    <div
      aria-busy={query.isPlaceholderData || undefined}
      className={cn(
        'transition-opacity motion-reduce:transition-none',
        query.isPlaceholderData && 'opacity-60',
      )}
    >
      <ActivityBody
        run={run}
        activity={query.data}
        onOpenChanges={onOpenChanges}
      />
    </div>
  )
}

function ActivityBody({
  run,
  activity: a,
  onOpenChanges,
}: {
  run: RunDTO
  activity: RunActivityDTO
  onOpenChanges?: () => void
}) {
  const { t } = useTranslation('sessions')
  const quiet =
    !a.todos.length && !a.agents.length && !a.tasks.length && !a.files.length
  const done = a.todos.filter((todo) => todo.status === 'completed').length
  const running = (items: { state: ActivityState }[]) =>
    items.filter((i) => i.state === 'running').length

  let contextUsage = t('activity.contextUnknown', {
    tokens: formatInt(a.context_tokens),
  })
  if (a.context_window != null && a.context_window > 0) {
    contextUsage =
      a.context_percent != null
        ? t('activity.contextUsage', {
            tokens: formatInt(a.context_tokens),
            percent: formatPercent(a.context_percent, { digits: 1 }),
            window: formatInt(a.context_window),
          })
        : `${formatInt(a.context_tokens)} / ${formatInt(a.context_window)}`
  }

  return (
    <div data-testid="session-activity" className="flex flex-col gap-3">
      {a.transcript === 'missing' ? (
        <p
          role="status"
          data-testid="activity-missing"
          className="text-caption text-warning"
        >
          {t('activity.missing')}
        </p>
      ) : quiet ? (
        <p
          data-testid="activity-empty"
          className="text-caption text-muted-foreground"
        >
          {t('activity.empty')}
        </p>
      ) : null}

      {a.todos.length ? (
        <Section
          testId="activity-todos"
          title={t('activity.todos')}
          count={t('activity.todosCount', { done, total: a.todos.length })}
        >
          <ul className="flex flex-col gap-0.5">
            {a.todos.map((todo, i) => (
              <li
                key={`${i}:${todo.content}`}
                className={cn(
                  'flex items-start gap-1.5 text-caption',
                  todo.status === 'completed'
                    ? 'text-muted-foreground'
                    : 'text-foreground',
                )}
              >
                {todo.status === 'completed' ? (
                  <Check aria-hidden className="mt-0.5 size-3.5 shrink-0" />
                ) : todo.status === 'in_progress' ? (
                  <CircleDot
                    aria-hidden
                    className="mt-0.5 size-3.5 shrink-0 text-primary"
                  />
                ) : (
                  <Circle aria-hidden className="mt-0.5 size-3.5 shrink-0" />
                )}
                <span className="sr-only">
                  {t(`activity.todo.${todo.status}`, {
                    defaultValue: todo.status,
                  })}
                </span>
                <span className="min-w-0">
                  {todo.status === 'in_progress' && todo.active_form
                    ? todo.active_form
                    : todo.content}
                </span>
              </li>
            ))}
          </ul>
        </Section>
      ) : null}

      {a.agents.length ? (
        <Section
          testId="activity-agents"
          title={t('activity.agents')}
          count={countLabel(t, a.agents.length, running(a.agents))}
        >
          {a.agents.map((agent) => (
            <AgentRow key={agent.tool_use_id} run={run} agent={agent} />
          ))}
        </Section>
      ) : null}

      {a.tasks.length ? (
        <Section
          testId="activity-tasks"
          title={t('activity.tasks')}
          count={countLabel(t, a.tasks.length, running(a.tasks))}
        >
          {a.tasks.map((task) => (
            <TaskRow key={task.tool_use_id} task={task} />
          ))}
        </Section>
      ) : null}

      {a.files.length ? (
        <Section
          testId="activity-files"
          title={t('activity.files')}
          count={formatInt(a.files.length)}
        >
          {a.files.map((file) => (
            <button
              key={file.path}
              type="button"
              data-testid="activity-file"
              onClick={onOpenChanges}
              disabled={!onOpenChanges}
              title={file.path}
              className="flex min-w-0 items-center gap-1.5 rounded-sm py-0.5 text-left text-caption hover:bg-muted focus-visible:outline-2 focus-visible:outline-ring disabled:hover:bg-transparent"
            >
              {file.created ? (
                <FilePlus
                  aria-hidden
                  className="size-3.5 shrink-0 text-muted-foreground"
                />
              ) : (
                <FilePen
                  aria-hidden
                  className="size-3.5 shrink-0 text-muted-foreground"
                />
              )}
              <span className="min-w-0 flex-1 truncate font-mono">
                {displayPath(file.path, run.workspace_path)}
              </span>
              {file.created ? (
                <span className="text-muted-foreground">
                  {t('activity.fileNew')}
                </span>
              ) : null}
              <span className="sr-only">
                {t('activity.lines', {
                  added: file.added,
                  removed: file.removed,
                })}
              </span>
              <span aria-hidden className="tabular-nums text-success">
                +{file.added}
              </span>
              <span aria-hidden className="tabular-nums text-danger">
                −{file.removed}
              </span>
            </button>
          ))}
        </Section>
      ) : null}

      <Section testId="activity-context" title={t('activity.context')}>
        <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-0.5 text-caption">
          <dt className="text-muted-foreground">{t('activity.model')}</dt>
          <dd className="min-w-0 truncate font-mono text-foreground">
            {a.model || t('context.notDeclared')}
            {a.effort ? ` · ${a.effort}` : ''}
          </dd>
          <dt className="text-muted-foreground">{t('activity.tokens')}</dt>
          <dd className="tabular-nums text-foreground">{contextUsage}</dd>
          <dt className="text-muted-foreground">{t('activity.compactions')}</dt>
          <dd className="text-foreground">
            {a.compactions.length
              ? t('activity.compactedTimes', {
                  count: a.compactions.length,
                  n: formatInt(a.compactions.length),
                })
              : t('activity.neverCompacted')}
          </dd>
          {a.skills.length ? (
            <>
              <dt className="text-muted-foreground">{t('activity.skills')}</dt>
              <dd className="min-w-0 text-foreground">{a.skills.join(', ')}</dd>
            </>
          ) : null}
          {a.mcp_servers.length ? (
            <>
              <dt className="text-muted-foreground">{t('activity.mcp')}</dt>
              <dd className="min-w-0 text-foreground">
                {a.mcp_servers.join(', ')}
              </dd>
            </>
          ) : null}
        </dl>
        {a.unreadable ? (
          <p className="mt-1 text-caption text-warning">
            {t('activity.unreadable', {
              count: a.unreadable,
              n: formatInt(a.unreadable),
            })}
          </p>
        ) : null}
      </Section>
    </div>
  )
}

function TaskRow({ task }: { task: ActivityTaskDTO }) {
  const { t } = useTranslation('sessions')
  const workflow = task.workflow
  const Icon = TASK_ICON[task.kind] ?? Terminal
  const time = elapsed(task)
  return (
    <div className="flex flex-col gap-0.5 py-0.5" data-testid="activity-task">
      <div className="flex min-w-0 items-center gap-1.5 text-caption">
        <StateGlyph state={task.state} />
        <Icon
          role="img"
          aria-label={t(`activity.kind.${task.kind}`, {
            defaultValue: task.kind,
          })}
          className="size-3.5 shrink-0 text-muted-foreground"
        />
        <span className="min-w-0 flex-1 truncate text-foreground">
          {task.description || task.command || task.kind}
        </span>
        {task.state === 'scheduled' && task.due_at ? (
          <span className="text-muted-foreground">
            {t('activity.due', {
              time: new Date(task.due_at).toLocaleTimeString(),
            })}
          </span>
        ) : time ? (
          <span className="tabular-nums text-muted-foreground">{time}</span>
        ) : null}
      </div>
      {task.command && task.description ? (
        <code className="truncate pl-9 font-mono text-caption text-muted-foreground">
          {task.command}
        </code>
      ) : null}
      {task.summary ? (
        <p className="pl-9 text-caption text-muted-foreground">
          {task.summary}
        </p>
      ) : null}
      {workflow ? (
        <div className="pl-9 text-caption">
          {workflow.agents_total > 0 ? (
            <p aria-live="polite" className="text-muted-foreground">
              {t('activity.workflowProgress', {
                done: workflow.agents_done,
                total: workflow.agents_total,
              })}
            </p>
          ) : null}
          {workflow.phases.length ? (
            <ol
              aria-label={t('activity.workflowPhases')}
              className="mt-1 flex flex-col gap-1"
            >
              {workflow.phases.map((phase) => (
                <li key={phase.index}>
                  <div className="flex items-center gap-1.5">
                    {phase.state ? (
                      <StateGlyph state={phase.state} />
                    ) : (
                      <Circle
                        aria-hidden
                        className="size-3.5 shrink-0 text-muted-foreground"
                      />
                    )}
                    <span className="min-w-0 break-words text-foreground">
                      {phase.title}
                    </span>
                  </div>
                  {phase.agents_total > 0 ? (
                    <p className="pl-5 text-muted-foreground">
                      {t('activity.workflowProgress', {
                        done: phase.agents_done,
                        total: phase.agents_total,
                      })}
                    </p>
                  ) : null}
                  {phase.detail ? (
                    <p className="pl-5 text-muted-foreground">{phase.detail}</p>
                  ) : null}
                </li>
              ))}
            </ol>
          ) : null}
        </div>
      ) : null}
    </div>
  )
}

function AgentRow({ run, agent }: { run: RunDTO; agent: ActivityAgentDTO }) {
  const { t } = useTranslation('sessions')
  const [open, setOpen] = useState(false)
  const time = elapsed(agent)
  // Codex reports the child state in its root rollout; child transcript reads
  // are currently offered only for Claude's per-session subagents directory.
  const agentId = run.provider_driver === 'claude' ? agent.agent_id : undefined
  return (
    <div className="flex flex-col py-0.5" data-testid="activity-agent">
      <button
        type="button"
        disabled={!agentId}
        aria-expanded={agentId ? open : undefined}
        onClick={() => setOpen((o) => !o)}
        className="flex min-w-0 items-center gap-1.5 rounded-sm text-left text-caption hover:bg-muted focus-visible:outline-2 focus-visible:outline-ring disabled:hover:bg-transparent"
      >
        <StateGlyph state={agent.state} />
        <Bot aria-hidden className="size-3.5 shrink-0 text-muted-foreground" />
        <span className="min-w-0 flex-1 truncate text-foreground">
          {agent.description || agent.type || t('activity.agent')}
        </span>
        {agent.type ? (
          <span className="truncate text-muted-foreground">{agent.type}</span>
        ) : null}
        {time ? (
          <span className="tabular-nums text-muted-foreground">{time}</span>
        ) : null}
      </button>
      {agent.summary && !open ? (
        <p className="truncate pl-9 text-caption text-muted-foreground">
          {agent.summary}
        </p>
      ) : null}
      {open && agentId ? (
        <AgentTranscript run={run} agentId={agentId} agent={agent} />
      ) : null}
    </div>
  )
}

/** What one subagent was asked, the tools it called and what it answered. */
function AgentTranscript({
  run,
  agentId,
  agent,
}: {
  run: RunDTO
  agentId: string
  agent: ActivityAgentDTO
}) {
  const { t } = useTranslation('sessions')
  const { activeTenant } = useAuth()
  const query = useQuery({
    // Not under the activity's key: the panel's refresh does not reread an open
    // transcript; the agent's state changing does.
    queryKey: [
      ...agentOpsKeys.run(activeTenant, run.run_ref),
      'activity-agent',
      agentId,
      agent.state,
    ],
    queryFn: ({ signal }) =>
      agentOpsApi.activityAgent(run.run_ref, agentId, { signal }),
  })
  const facts = [
    agent.model,
    agent.tool_uses
      ? t('activity.toolUses', {
          count: agent.tool_uses,
          n: formatInt(agent.tool_uses),
        })
      : null,
    agent.tokens
      ? t('activity.agentTokens', { value: formatInt(agent.tokens) })
      : null,
  ].filter(Boolean)
  return (
    <div
      data-testid="activity-agent-transcript"
      className="mt-1 ml-9 flex max-h-80 flex-col gap-1.5 overflow-auto rounded-md border border-border bg-muted/40 p-2"
    >
      {facts.length ? (
        <p className="text-caption text-muted-foreground">
          {facts.join(' · ')}
        </p>
      ) : null}
      {query.isPending ? <Skeleton className="h-4 w-2/3" /> : null}
      {query.isError ? (
        <p role="alert" className="text-caption text-warning">
          {query.error instanceof ApiError && query.error.status < 500
            ? query.error.message
            : t('activity.agentFailed')}
        </p>
      ) : null}
      {query.data?.turns.map((turn, i) => (
        <div key={i} className="flex flex-col text-caption">
          <span className="text-overline text-muted-foreground uppercase">
            {t(`activity.role.${turn.role}`, { defaultValue: turn.role })}
          </span>
          <span
            className={cn(
              'whitespace-pre-wrap break-words text-foreground',
              (turn.role === 'tool' || turn.role === 'result') &&
                'font-mono text-muted-foreground',
            )}
          >
            {turn.text}
          </span>
        </div>
      ))}
    </div>
  )
}
