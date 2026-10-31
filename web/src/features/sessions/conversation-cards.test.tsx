// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import {
  useMutation,
  QueryClient,
  QueryClientProvider,
} from '@tanstack/react-query'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { useSentTurns } from './sent-turns'
import type { RunDTO } from '@/features/agentops/types'
import {
  ConversationCard,
  ConversationTaskList as TaskList,
} from './conversation-cards'

import { useConversationTasks } from './use-conversation-tasks'

function ConversationTaskList({
  run,
  tenant,
}: {
  run: RunDTO
  tenant: string | null
}) {
  const query = useConversationTasks(run, tenant)
  return <TaskList run={run} query={query} />
}

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 'resolved-tenant' }),
}))

const activity = vi.hoisted(() => vi.fn())
const input = vi.hoisted(() => vi.fn().mockResolvedValue({}))
vi.mock('@/features/agentops/api', () => ({
  agentOpsApi: { inputText: input, input, activity },
  agentOpsKeys: {
    runActivity: (tenant: string | null, ref: string) => [
      'activity',
      tenant,
      ref,
    ],
  },
}))
vi.mock('@/features/agentops/work-fence', () => ({
  currentControlFence: vi.fn().mockResolvedValue(undefined),
}))

const run = { run_ref: 'run-1', provider_driver: 'claude' } as RunDTO
const QUESTION = {
  kind: 'question' as const,
  questions: [
    {
      question: 'Which database?',
      header: 'Database',
      multiSelect: false,
      options: [
        { label: 'PostgreSQL', description: 'The shared store' },
        { label: 'SQLite', description: '' },
      ],
    },
  ],
}

function show(
  card: Parameters<typeof ConversationCard>[0]['card'],
  answerable = true,
) {
  const client = new QueryClient({
    defaultOptions: { mutations: { retry: false } },
  })
  return render(
    <QueryClientProvider client={client}>
      <ul>
        <ConversationCard card={card} run={run} answerable={answerable} />
      </ul>
    </QueryClientProvider>,
  )
}

beforeEach(() => input.mockClear())

describe('conversation cards', () => {
  it('sends the chosen option once and then shows the answer', async () => {
    const user = userEvent.setup()
    show(QUESTION)
    const send = screen.getByRole('button', { name: 'Send answer' })
    expect(send).toBeDisabled()
    await user.click(screen.getByRole('radio', { name: /SQLite/ }))
    await user.click(send)
    await waitFor(() => expect(input).toHaveBeenCalledTimes(1))
    // A Claude run takes its turns as a stream-json line, as the reply field sends them.
    const line = JSON.parse(input.mock.calls[0]?.[1] as string)
    expect(line.message.content).toBe('SQLite')
    expect(await screen.findByRole('status')).toHaveTextContent('SQLite')
    expect(screen.queryByRole('button', { name: 'Send answer' })).toBeNull()
  })

  it('does not add slash commands to sent turns', async () => {
    useSentTurns.setState({ byRun: {}, seen: {}, said: {} })
    const user = userEvent.setup()
    show({
      ...QUESTION,
      questions: [
        {
          ...QUESTION.questions[0]!,
          options: [{ label: '/compact', description: '' }],
        },
      ],
    })
    await user.click(screen.getByRole('radio', { name: /compact/ }))
    await user.click(screen.getByRole('button', { name: 'Send answer' }))
    await screen.findByRole('status')
    expect(useSentTurns.getState().byRun).toEqual({})
  })

  it('keeps one choice for a question that takes one', async () => {
    const user = userEvent.setup()
    show(QUESTION)
    await user.click(screen.getByRole('radio', { name: /PostgreSQL/ }))
    await user.click(screen.getByRole('radio', { name: /SQLite/ }))
    expect(screen.getByRole('radio', { name: /PostgreSQL/ })).not.toBeChecked()
    expect(screen.getByRole('radio', { name: /SQLite/ })).toBeChecked()
  })

  it('offers no answer once the session moved on', () => {
    show(QUESTION, false)
    expect(screen.queryByRole('button', { name: 'Send answer' })).toBeNull()
    expect(screen.getByRole('radio', { name: /PostgreSQL/ })).toBeDisabled()
  })

  it('sends one line per question, each with every choice picked', async () => {
    const user = userEvent.setup()
    const ask = (header: string, multiSelect: boolean) => ({
      question: `${header}?`,
      header,
      multiSelect,
      options: [{ label: `${header}1` }, { label: `${header}2` }].map((o) => ({
        ...o,
        description: '',
      })),
    })
    show({ kind: 'question', questions: [ask('A', true), ask('B', false)] })
    await user.click(screen.getByRole('checkbox', { name: /A1/ }))
    await user.click(screen.getByRole('checkbox', { name: /A2/ }))
    const send = screen.getByRole('button', { name: 'Send answer' })
    expect(send).toBeDisabled()
    await user.click(screen.getByRole('radio', { name: /B2/ }))
    await user.click(send)
    await waitFor(() => expect(input).toHaveBeenCalledTimes(1))
    const line = JSON.parse(input.mock.calls[0]?.[1] as string)
    expect(line.message.content).toBe('A: A1, A2\nB: B2')
  })

  it('selects an option with its number shortcut while the question has focus', async () => {
    const user = userEvent.setup()
    show(QUESTION)
    const first = screen.getByRole('radio', { name: /PostgreSQL/ })
    first.focus()
    await user.keyboard('2')
    expect(screen.getByRole('radio', { name: /SQLite/ })).toBeChecked()
    await user.click(screen.getByRole('button', { name: 'Send answer' }))
    await waitFor(() => expect(input).toHaveBeenCalledTimes(1))
    expect(JSON.parse(input.mock.calls[0]?.[1] as string).message.content).toBe(
      'SQLite',
    )
  })

  it('blocks answers while a composer turn is in flight', async () => {
    let finish!: () => void
    function ComposerSend() {
      const send = useMutation({
        mutationKey: ['session-turn', 'resolved-tenant', run.run_ref],
        mutationFn: () =>
          new Promise<void>((resolve) => {
            finish = resolve
          }),
      })
      return <button onClick={() => send.mutate()}>Composer send</button>
    }
    const user = userEvent.setup()
    const client = new QueryClient()
    render(
      <QueryClientProvider client={client}>
        <ComposerSend />
        <ul>
          <ConversationCard card={QUESTION} run={run} answerable />
        </ul>
      </QueryClientProvider>,
    )
    await user.click(screen.getByRole('radio', { name: /SQLite/ }))
    await user.click(screen.getByRole('button', { name: 'Composer send' }))
    expect(screen.getByRole('button', { name: 'Send answer' })).toBeDisabled()
    expect(screen.getByRole('radio', { name: /SQLite/ })).toBeDisabled()
    finish()
    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Send answer' })).toBeEnabled(),
    )
  })

  it.each(['ctrlKey', 'altKey', 'metaKey'])(
    'ignores modified number shortcuts (%s)',
    (modifier) => {
      show(QUESTION)
      fireEvent.keyDown(screen.getByRole('radio', { name: /PostgreSQL/ }), {
        key: '2',
        [modifier]: true,
      })
      expect(screen.getByRole('radio', { name: /SQLite/ })).not.toBeChecked()
    },
  )

  it('disables choices and prevents another answer while sending', async () => {
    let finish!: () => void
    input.mockImplementationOnce(
      () =>
        new Promise<void>((resolve) => {
          finish = resolve
        }),
    )
    const user = userEvent.setup()
    show(QUESTION)
    await user.click(screen.getByRole('radio', { name: /SQLite/ }))
    const send = screen.getByRole('button', { name: 'Send answer' })
    await user.click(send)
    expect(send).toBeDisabled()
    expect(screen.getByRole('radio', { name: /SQLite/ })).toBeDisabled()
    await user.click(send)
    expect(input).toHaveBeenCalledTimes(1)
    finish()
    await screen.findByRole('status')
  })

  it('counts the finished todos in the title and marks each state', () => {
    show({
      kind: 'todos',
      todos: [
        {
          content: 'Write test',
          status: 'completed',
          activeForm: 'Writing test',
        },
        { content: 'Fix', status: 'in_progress', activeForm: 'Fixing' },
        { content: 'Ship', status: 'pending', activeForm: 'Shipping' },
      ],
    })
    expect(
      screen.getByRole('region', { name: 'Todo list (1 of 3 done)' }),
    ).toBeVisible()
    expect(screen.getByText('Fixing').closest('li')).toHaveAttribute(
      'data-status',
      'in_progress',
    )
    expect(screen.getByText('Ship').closest('li')).toHaveAttribute(
      'data-status',
      'pending',
    )
  })

  it('renders a plan as formatted text', () => {
    show({ kind: 'plan', plan: '## Steps\n\n- Read\n- Change' })
    expect(screen.getByRole('region', { name: 'Proposed plan' })).toBeVisible()
    expect(screen.getByText('Change')).toBeVisible()
  })
})

describe('authoritative conversation task list', () => {
  it.each(['claude', 'codex'])(
    'reads %s tasks from the existing activity fold',
    async (driver) => {
      activity.mockResolvedValue({
        transcript: 'read',
        todos: [
          { content: 'Read the code', status: 'completed' },
          {
            content: 'Write the failing test',
            status: 'in_progress',
            active_form: 'Writing the failing test',
          },
        ],
      })
      const client = new QueryClient({
        defaultOptions: { queries: { retry: false } },
      })
      render(
        <QueryClientProvider client={client}>
          <ConversationTaskList
            run={{ ...run, provider_driver: driver }}
            tenant="tenant-a"
          />
        </QueryClientProvider>,
      )
      expect(
        await screen.findByTestId('conversation-card-todos'),
      ).toHaveTextContent('1 of 2')
      expect(screen.getByText('Writing the failing test')).toBeInTheDocument()
      expect(activity).toHaveBeenCalledWith(
        'run-1',
        expect.objectContaining({ signal: expect.any(AbortSignal) }),
      )
      client.setQueryData(['activity', 'tenant-a', 'run-1', undefined], {
        transcript: 'read',
        todos: [],
      })
      await waitFor(() =>
        expect(screen.queryByTestId('conversation-card-todos')).toBeNull(),
      )
    },
  )

  it('keeps the task list visible while the run state refreshes', async () => {
    activity
      .mockResolvedValueOnce({
        transcript: 'read',
        todos: [{ content: 'Keep visible', status: 'pending' }],
      })
      .mockImplementationOnce(() => new Promise(() => {}))
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    const view = (state: RunDTO['state']) => (
      <QueryClientProvider client={client}>
        <ConversationTaskList run={{ ...run, state }} tenant="a" />
      </QueryClientProvider>
    )
    const { rerender } = render(view('running'))
    await screen.findByText('Keep visible')
    rerender(view('stopped'))
    expect(screen.getByText('Keep visible')).toBeInTheDocument()
  })

  it.each(['run', 'tenant'])(
    'does not carry placeholder tasks into another %s',
    async (change) => {
      activity
        .mockResolvedValueOnce({
          transcript: 'read',
          todos: [{ content: 'Private old task', status: 'pending' }],
        })
        .mockImplementationOnce(() => new Promise(() => {}))
      const client = new QueryClient({
        defaultOptions: { queries: { retry: false } },
      })
      const view = (ref: string, tenant: string) => (
        <QueryClientProvider client={client}>
          <ConversationTaskList
            run={{ ...run, run_ref: ref }}
            tenant={tenant}
          />
        </QueryClientProvider>
      )
      const { rerender } = render(view('run-1', 'a'))
      await screen.findByText('Private old task')
      rerender(
        view(
          change === 'run' ? 'run-2' : 'run-1',
          change === 'tenant' ? 'b' : 'a',
        ),
      )
      expect(screen.queryByText('Private old task')).toBeNull()
    },
  )

  it('reports a failed read rather than showing an empty list', async () => {
    activity.mockRejectedValue(new Error('offline'))
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    render(
      <QueryClientProvider client={client}>
        <ConversationTaskList run={run} tenant={null} />
      </QueryClientProvider>,
    )
    expect(await screen.findByRole('alert')).toHaveTextContent(/activity/i)
  })
})
