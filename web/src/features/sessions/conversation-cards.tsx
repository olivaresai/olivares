// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A QUESTION, A PLAN AND A TODO LIST, AS CARDS IN THE CONVERSATION. The tool's
// own call (AskUserQuestion, ExitPlanMode, TodoWrite) is what the card reads, so a card
// shows what the agent asked, proposed or listed and nothing else. A question is answered
// where it is read: the answer goes to the session as the person's next message, the same
// request the reply field sends (same permission, same audit), so there is no second way
// to talk to a session.
import { useIsMutating, useMutation } from '@tanstack/react-query'
import {
  Check,
  Circle,
  CircleDot,
  ListTodo,
  MessageCircleQuestion,
  ScrollText,
} from 'lucide-react'
import { useId, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { toast } from '@/components/ui/toaster'
import { launchFailureMessage } from '@/features/agentops/launch-readiness'
import { sendTurn } from '@/features/agentops/session-turn'
import type { RunDTO } from '@/features/agentops/types'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { cn } from '@/lib/utils'
import type { ToolCard, ToolQuestion } from './conversation-frames'
import { MarkdownText } from './markdown-text'
import { sentPlace, sentTurnsPartition, useSentTurns } from './sent-turns'
import type { useConversationTasks } from './use-conversation-tasks'
import './i18n'

/** The words one answer sends: the choice itself for one question, each question's header
 * (or the question) with its choice when the call asked several. */
function answerText(
  questions: readonly ToolQuestion[],
  picked: readonly (readonly string[])[],
): string {
  if (questions.length === 1) return (picked[0] ?? []).join(', ')
  return questions
    .map((q, i) => `${q.header || q.question}: ${(picked[i] ?? []).join(', ')}`)
    .join('\n')
}

function CardFrame({
  icon,
  title,
  testid,
  children,
}: {
  icon: React.ReactNode
  title: string
  testid: string
  children: React.ReactNode
}) {
  return (
    <li>
      <section
        data-testid={testid}
        aria-label={title}
        className="my-1 flex flex-col gap-2 rounded-card border border-border bg-surface-1 p-3"
      >
        <h3 className="flex items-center gap-2 text-caption font-medium text-text-2">
          {icon}
          {title}
        </h3>
        {children}
      </section>
    </li>
  )
}

function QuestionBody({
  questions,
  run,
  answerable,
}: {
  questions: readonly ToolQuestion[]
  run: RunDTO
  answerable: boolean
}) {
  const { t } = useTranslation('sessions')
  const groupId = useId()
  const [picked, setPicked] = useState<readonly (readonly string[])[]>(
    questions.map(() => []),
  )
  const [sent, setSent] = useState<string | null>(null)
  const { activeTenant: tenant } = useAuth()
  const turnQueue = ['session-turn', tenant, run.run_ref]
  const sendsInQueue = useIsMutating({ mutationKey: turnQueue })
  const send = useMutation({
    mutationKey: turnQueue,
    mutationFn: async (text: string) => {
      const partition = sentTurnsPartition()
      const place = sentPlace(run.run_ref, text)
      await sendTurn(run, text)
      return { partition, place }
    },
    retry: false,
    onSuccess: ({ partition, place }, text) => {
      setSent(text)
      if (text.trim() && !text.trimStart().startsWith('/'))
        useSentTurns.getState().note(partition, run.run_ref, { text, ...place })
    },
    onError: (error: unknown) =>
      toast.error(
        launchFailureMessage(error, t('conversation.card.sendFailed')),
      ),
  })
  const choose = (q: number, label: string) =>
    setPicked((prev) =>
      prev.map((own, i) => {
        if (i !== q) return own
        if (!questions[q]!.multiSelect) return [label]
        return own.includes(label)
          ? own.filter((l) => l !== label)
          : [...own, label]
      }),
    )
  const ready = picked.every((own) => own.length > 0)
  const open = answerable && sent === null && sendsInQueue === 0
  return (
    <>
      {questions.map((q, qi) => (
        <fieldset
          key={qi}
          className="flex min-w-0 flex-col gap-1.5"
          disabled={!open}
          onKeyDown={(event) => {
            const index = Number(event.key) - 1
            if (
              !open ||
              event.altKey ||
              event.ctrlKey ||
              event.metaKey ||
              !/^[1-9]$/.test(event.key) ||
              !q.options[index]
            )
              return
            event.preventDefault()
            choose(qi, q.options[index]!.label)
            event.currentTarget
              .querySelectorAll<HTMLInputElement>('input')
              [index]?.focus()
          }}
        >
          <legend className="mb-1 text-body text-text-1">
            {q.header ? (
              <span className="me-2 rounded-ctl bg-surface-2 px-1.5 py-0.5 text-overline text-text-3">
                {q.header}
              </span>
            ) : null}
            {q.question}
          </legend>
          {q.options.map((o, oi) => {
            const on = (picked[qi] ?? []).includes(o.label)
            return (
              <label
                key={oi}
                className={cn(
                  'flex w-full items-start gap-2 rounded-ctl border px-2 py-1.5 text-left',
                  'focus-within:ring-2 focus-within:ring-focus',
                  on ? 'border-accent bg-surface-2' : 'border-border',
                )}
              >
                <input
                  type={q.multiSelect ? 'checkbox' : 'radio'}
                  name={`${groupId}-${qi}`}
                  checked={on}
                  disabled={!open}
                  onChange={() => choose(qi, o.label)}
                  className="mt-1 size-4 shrink-0 accent-accent"
                />
                <span className="min-w-0 flex-1">
                  <span className="block text-body text-text-1">{o.label}</span>
                  {o.description ? (
                    <span className="block text-caption text-text-3">
                      {o.description}
                    </span>
                  ) : null}
                </span>
                {oi < 9 ? (
                  <kbd aria-hidden className="text-caption text-text-3">
                    {oi + 1}
                  </kbd>
                ) : null}
              </label>
            )
          })}
        </fieldset>
      ))}
      {sent !== null ? (
        <p
          role="status"
          className="flex items-center gap-1 text-caption text-text-3"
        >
          <Check aria-hidden className="size-3.5" />
          {t('conversation.card.answered', { answer: sent })}
        </p>
      ) : answerable ? (
        <div>
          <Button
            size="sm"
            disabled={!ready || !open}
            onClick={() => send.mutate(answerText(questions, picked))}
          >
            {t('conversation.card.send')}
          </Button>
        </div>
      ) : null}
    </>
  )
}

/** The card a question, plan or todo call is. `answerable`: the session is live, the
 * person may write to it, and nothing was said after the question. */
export function ConversationCard({
  card,
  run,
  answerable,
}: {
  card: ToolCard
  run: RunDTO
  answerable: boolean
}) {
  const { t } = useTranslation('sessions')
  if (card.kind === 'question')
    return (
      <CardFrame
        icon={<MessageCircleQuestion aria-hidden className="size-4" />}
        title={t('conversation.card.question')}
        testid="conversation-card-question"
      >
        <QuestionBody
          questions={card.questions}
          run={run}
          answerable={answerable}
        />
      </CardFrame>
    )
  if (card.kind === 'plan')
    return (
      <CardFrame
        icon={<ScrollText aria-hidden className="size-4" />}
        title={t('conversation.card.plan')}
        testid="conversation-card-plan"
      >
        <MarkdownText text={card.plan} />
      </CardFrame>
    )
  const done = card.todos.filter((x) => x.status === 'completed').length
  return (
    <CardFrame
      icon={<ListTodo aria-hidden className="size-4" />}
      title={t('conversation.card.todos', { done, total: card.todos.length })}
      testid="conversation-card-todos"
    >
      <ul className="flex flex-col gap-1">
        {card.todos.map((todo, i) => (
          <li
            key={i}
            data-status={todo.status}
            className={cn(
              'flex items-start gap-2 text-body',
              todo.status === 'completed' && 'text-text-3 line-through',
            )}
          >
            {todo.status === 'completed' ? (
              <Check aria-hidden className="mt-0.5 size-4 shrink-0" />
            ) : todo.status === 'in_progress' ? (
              <CircleDot aria-hidden className="mt-0.5 size-4 shrink-0" />
            ) : (
              <Circle aria-hidden className="mt-0.5 size-4 shrink-0" />
            )}
            {todo.status === 'in_progress' && todo.activeForm
              ? todo.activeForm
              : todo.content}
          </li>
        ))}
      </ul>
    </CardFrame>
  )
}

export function ConversationTaskList({
  run,
  query,
}: {
  run: RunDTO
  query: ReturnType<typeof useConversationTasks>
}) {
  const { t } = useTranslation(['sessions', 'common'])
  if (query.error instanceof ApiError && query.error.status < 500) return null

  if (query.isError)
    return (
      <p role="alert" className="text-caption text-warning">
        {t('activity.failed')}
        <Button
          size="sm"
          variant="secondary"
          onClick={() => void query.refetch()}
        >
          {t('common:actions.retry')}
        </Button>
      </p>
    )
  if (query.data?.transcript !== 'read' || !query.data.todos.length) return null
  return (
    <ul>
      <ConversationCard
        run={run}
        answerable={false}
        card={{
          kind: 'todos',
          todos: query.data.todos.map((todo) => ({
            content: todo.content,
            status: todo.status,
            activeForm: todo.active_form ?? '',
          })),
        }}
      />
    </ul>
  )
}
