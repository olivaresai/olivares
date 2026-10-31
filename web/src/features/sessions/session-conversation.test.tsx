// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/errors'
import i18n from '@/lib/i18n'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import type { RunDTO } from '@/features/agentops/types'
import { SessionConversation } from './session-conversation'
import { sentPlace, sentTurnsPartition, useSentTurns } from './sent-turns'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import {
  readSessionDraft,
  sessionDraftKey,
  writeSessionDraft,
} from '@/lib/session-drafts'
import nativeHistory from './fixtures/codex-native-history.json'
import claudeStream from './fixtures/claude-2.1.309-subagents.jsonl?raw'
import './i18n'

const attachStatus = vi.hoisted(() => ({
  value: 'open' as 'open' | 'connecting' | 'error' | 'closed',
}))
const onFrameRef = vi.hoisted(() => ({
  current: null as
    | ((f: { seq: number; stream: string; line: string; at?: string }) => void)
    | null,
}))
const onHistoryRef = vi.hoisted(() => ({
  current: null as ((line: string) => void) | null,
}))
const messageRights = vi.hoisted(() => ({ writable: true }))

vi.mock('@/features/agentops/attach', () => ({
  useRunAttach: (opts: {
    onFrame: (f: { seq: number; stream: string; line: string }) => void
    onHistory?: (line: string) => void
  }) => {
    onFrameRef.current = opts.onFrame
    onHistoryRef.current = opts.onHistory ?? null
    return {
      status: attachStatus.value,
      ended: false,
      ioUnavailable: null,
      retry: () => {},
    }
  },
}))

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    can: (permission: string) =>
      permission !== 'sessions:run:write' || messageRights.writable,
    activeTenant: 'demo',
    principal: { kind: 'user', actor: 'alice', user_id: 'alice' },
  }),
}))

const run: RunDTO = {
  run_ref: 'run_1',
  name: 'session',
  authz_workspace_id: 'default-workspace',
  transport: 'stream-json',
  permission_mode: 'default',
  isolation: 'native',
  state: 'running',
  last_event_seq: 0,
  pep_provisioned: true,
  record_io: true,
  critical: false,
}

let lastClient: QueryClient | null = null
const qcIdle = () => lastClient?.isFetching() === 0

function wrap(overrides: Partial<RunDTO> = {}) {
  const qc = (lastClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  }))
  return render(
    <QueryClientProvider client={qc}>
      <SessionConversation
        run={{ ...run, ...overrides }}
        composerId="work-composer-input"
      />
    </QueryClientProvider>,
  )
}

function push(line: string, seq = 1, at?: string) {
  onFrameRef.current?.({ seq, stream: 'stdout', line, ...(at ? { at } : {}) })
}

beforeEach(() => {
  messageRights.writable = true
  attachStatus.value = 'open'
  onFrameRef.current = null
  useSentTurns.setState({ byRun: {}, seen: {}, said: {} })
})

describe('SessionConversation', () => {
  it('clears task placeholders across a sign-in owner change', async () => {
    const activity = vi
      .spyOn(agentOpsApi, 'activity')
      .mockResolvedValueOnce({
        transcript: 'read',
        todos: [{ content: 'Previous owner task', status: 'pending' }],
      } as Awaited<ReturnType<typeof agentOpsApi.activity>>)
      .mockImplementation(() => new Promise(() => {}))
    try {
      wrap({ provider_driver: 'claude' })
      await screen.findByText('Previous owner task')
      act(() => {
        lastClient!.clear()
        useSessionStore.setState((s) => ({
          credentialGeneration: s.credentialGeneration + 1,
        }))
      })
      expect(screen.queryByText('Previous owner task')).toBeNull()
    } finally {
      activity.mockRestore()
    }
  })

  it.each(['refused', 'pending', 'missing'])(
    'keeps inline todos when activity is %s',
    async (status) => {
      const activity = vi.spyOn(agentOpsApi, 'activity')
      if (status === 'refused')
        activity.mockRejectedValue(
          new ApiError(409, 'conflict', 'Transcript unavailable'),
        )
      else
        activity.mockResolvedValue({
          transcript: status,
          todos: [],
        } as unknown as Awaited<ReturnType<typeof agentOpsApi.activity>>)
      try {
        wrap({ provider_driver: 'claude' })
        act(() =>
          push(
            JSON.stringify({
              type: 'assistant',
              message: {
                content: [
                  {
                    type: 'tool_use',
                    id: 'todo-1',
                    name: 'TodoWrite',
                    input: {
                      todos: [
                        {
                          content: 'Keep inline task',
                          status: 'pending',
                          activeForm: '',
                        },
                      ],
                    },
                  },
                ],
              },
            }),
          ),
        )
        await waitFor(() => expect(qcIdle()).toBe(true))
        expect(screen.queryByRole('alert')).toBeNull()
        expect(screen.getByTestId('conversation-card-todos')).toHaveTextContent(
          'Keep inline task',
        )
      } finally {
        activity.mockRestore()
      }
    },
  )

  it.each(['claude', 'codex'])(
    'renders the %s current task list in the conversation and refreshes it',
    async (provider_driver) => {
      const activity = vi.spyOn(agentOpsApi, 'activity').mockResolvedValue({
        transcript: 'read',
        todos: [{ content: 'Read the code', status: 'pending' }],
      } as Awaited<ReturnType<typeof agentOpsApi.activity>>)
      try {
        wrap({ provider_driver })
        expect(
          await screen.findByTestId('conversation-card-todos'),
        ).toHaveTextContent('Read the code')
        activity.mockResolvedValue({
          transcript: 'read',
          todos: [{ content: 'Read the code', status: 'completed' }],
        } as Awaited<ReturnType<typeof agentOpsApi.activity>>)
        await act(async () => {
          await lastClient!.invalidateQueries({
            queryKey: agentOpsKeys.runActivity(
              useTenantStore.getState().activeTenant,
              run.run_ref,
            ),
          })
        })
        await waitFor(() =>
          expect(
            screen.getByTestId('conversation-card-todos'),
          ).toHaveTextContent('1 of 1'),
        )
        expect(screen.getAllByTestId('conversation-card-todos')).toHaveLength(1)
      } finally {
        activity.mockRestore()
      }
    },
  )

  it.each([
    'live',
    'pending turn',
    'later operator',
    'no write permission',
    'stopped',
    'pending',
    'approval',
  ])('checks whether a question is answerable with %s', async (reason) => {
    messageRights.writable = reason !== 'no write permission'
    wrap({
      state:
        reason === 'stopped'
          ? 'stopped'
          : reason === 'pending'
            ? 'pending'
            : 'running',
      ...(reason === 'approval' ? { pending_approval_ref: 'approval-1' } : {}),
    })
    act(() =>
      push(
        JSON.stringify({
          type: 'assistant',
          message: {
            content: [
              {
                type: 'tool_use',
                id: 'q1',
                name: 'AskUserQuestion',
                input: {
                  questions: [
                    {
                      question: 'Which store?',
                      header: 'Store',
                      multiSelect: false,
                      options: [{ label: 'SQLite', description: 'Local' }],
                    },
                  ],
                },
              },
            ],
          },
        }),
        1,
      ),
    )
    if (reason === 'pending turn')
      act(() =>
        useSentTurns
          .getState()
          .note(sentTurnsPartition(), run.run_ref, { text: 'Already sent' }),
      )
    if (reason === 'later operator')
      act(() =>
        push(
          JSON.stringify({
            type: 'user',
            message: { role: 'user', content: 'Moved on' },
          }),
          2,
        ),
      )
    expect(
      await screen.findByTestId('conversation-card-question'),
    ).toBeInTheDocument()
    if (reason === 'live') {
      expect(
        screen.getByRole('button', { name: 'Send answer' }),
      ).toBeInTheDocument()
      expect(screen.getByRole('radio', { name: /SQLite/ })).toBeEnabled()
    } else {
      expect(screen.queryByRole('button', { name: 'Send answer' })).toBeNull()
      expect(screen.getByRole('radio', { name: /SQLite/ })).toBeDisabled()
    }
  })

  it('keeps the empty conversation prompt for a retryable failure', () => {
    attachStatus.value = 'closed'
    wrap({ state: 'failed', reason: 'provider temporarily unavailable' })
    expect(screen.getByText('No messages yet.')).toBeInTheDocument()
  })
  it('does not ask for a sentence after a host isolation refusal', () => {
    attachStatus.value = 'closed'
    wrap({
      state: 'failed',
      reason:
        'the session was not started because its network boundary could not be set up: the host or container must allow unprivileged user and network namespaces and Landlock: operation not permitted',
    })
    expect(screen.queryByText('No messages yet.')).not.toBeInTheDocument()
    act(() => onHistoryRef.current?.(JSON.stringify(nativeHistory)))
    expect(screen.getByText('initial CLI prompt')).toBeInTheDocument()
  })
  it('shows native stored earlier turns on a fresh view and clears words on tenant or sign-in change', async () => {
    wrap({ state: 'stopped' })
    act(() => onHistoryRef.current?.(JSON.stringify(nativeHistory)))
    expect(await screen.findByText('initial CLI prompt')).toBeInTheDocument()
    expect(screen.getByText('earlier console prompt')).toBeInTheDocument()
    expect(screen.getAllByText('fixture answer')).toHaveLength(2)
    act(() => useTenantStore.setState({ activeTenant: 'another-tenant' }))
    expect(screen.queryByText('initial CLI prompt')).not.toBeInTheDocument()
    act(() => onHistoryRef.current?.(JSON.stringify(nativeHistory)))
    expect(screen.getByText('initial CLI prompt')).toBeInTheDocument()
    act(() =>
      useSessionStore.getState().setSession({
        csrfToken: 'history-session',
        sessionId: 'history-session',
        expiresAt: '2099-01-01T00:00:00Z',
      }),
    )
    act(() => onHistoryRef.current?.(JSON.stringify(nativeHistory)))
    expect(screen.getByText('initial CLI prompt')).toBeInTheDocument()
    act(() => useSessionStore.getState().clear())
    expect(screen.queryByText('initial CLI prompt')).not.toBeInTheDocument()
    expect(screen.queryByText('earlier console prompt')).not.toBeInTheDocument()
  })
  it('renders an assistant message and a collapsed tool row, never the raw wire', async () => {
    wrap()
    await screen.findByTestId('session-conversation')
    act(() => {
      push(
        JSON.stringify({
          type: 'assistant',
          message: {
            role: 'assistant',
            content: [{ type: 'text', text: 'OK' }],
          },
        }),
        1,
      )
      push(
        JSON.stringify({
          type: 'assistant',
          message: {
            role: 'assistant',
            content: [
              {
                type: 'tool_use',
                id: 'toolu_1',
                name: 'Read',
                input: { file_path: 'README.md' },
              },
            ],
          },
        }),
        2,
      )
    })
    expect(await screen.findByText('OK')).toBeInTheDocument()
    const items = screen.getAllByTestId('conversation-item')
    expect(
      items.some((el) => el.getAttribute('data-kind') === 'assistant'),
    ).toBe(true)
    expect(items.some((el) => el.getAttribute('data-kind') === 'tool')).toBe(
      true,
    )
    expect(screen.getByText('Read')).toBeInTheDocument()
    expect(screen.queryByText(/tool_use/)).toBeNull()
    // The call reads as what it acts on, never as its JSON.
    expect(screen.queryByText(/file_path/)).toBeNull()
    expect(screen.getAllByText('README.md').length).toBeGreaterThan(0)
  })

  // #3135: a file tool's path is shown whole through the shared PathText, in the row and in
  // its arguments, never cut into a string first (the full path was nowhere on the page).
  it('shows a file tool path whole through PathText, in the row and in its arguments', async () => {
    const long =
      '/home/ada/.claude/projects/-home-ada-work-shop/memory/MEMORY.md'
    wrap()
    await screen.findByTestId('session-conversation')
    act(() => {
      push(
        JSON.stringify({
          type: 'assistant',
          message: {
            role: 'assistant',
            content: [
              {
                type: 'tool_use',
                id: 'toolu_long',
                name: 'Read',
                input: { file_path: long },
              },
            ],
          },
        }),
        1,
      )
    })
    await screen.findByText('Read')
    const row = screen
      .getAllByTestId('conversation-item')
      .find((el) => el.getAttribute('data-kind') === 'tool')!
    const shown = Array.from(row.querySelectorAll('[data-slot="path-text"]'))
    // The row and the arguments, each the whole path as its hover.
    expect(shown).toHaveLength(2)
    for (const el of shown)
      expect(el.querySelector('code')).toHaveAttribute('title', long)
    expect(row.textContent).not.toContain('/…/')
    // The row is a <summary>: no button inside it.
    expect(row.querySelector('summary button')).toBeNull()
  })

  // #3135: while the call runs, the live line names its path through PathText too, so the
  // file name is not lost at the end of a long sentence.
  it('names a running file tool path whole through PathText in the live line', async () => {
    const long =
      '/home/ada/.claude/projects/-home-ada-work-shop/memory/MEMORY.md'
    wrap()
    await screen.findByTestId('session-conversation')
    act(() => {
      push(
        JSON.stringify({
          type: 'assistant',
          message: {
            role: 'assistant',
            content: [
              {
                type: 'tool_use',
                id: 'toolu_live',
                name: 'Read',
                input: { file_path: long },
              },
            ],
          },
        }),
        1,
      )
    })
    const live = await screen.findByTestId('conversation-live')
    const shown = live.querySelector('[data-slot="path-text"]')
    expect(shown).not.toBeNull()
    expect(shown!.querySelector('code')).toHaveAttribute('title', long)
    expect(live).toHaveTextContent(/^Reading /)
    expect(live.querySelector('button')).toBeNull()
  })

  // #3135 round 5: a running search names its folder through PathText too, so a long
  // folder keeps its end in the live line (Grep, Glob and LS share the search words).
  it.each([
    ['Grep', { pattern: 'TODO', path: '' }, /^Searching TODO · /],
    ['Glob', { pattern: '**/*.md', path: '' }, /^Searching \*\*\/\*\.md · /],
    ['LS', { path: '' }, /^Searching /],
  ])(
    'names a running %s folder whole through PathText in the live line',
    async (name, input, words) => {
      const long =
        '/home/ada/.claude/projects/-home-ada-work-shop/memory/deep/folder'
      wrap()
      await screen.findByTestId('session-conversation')
      act(() => {
        push(
          JSON.stringify({
            type: 'assistant',
            message: {
              role: 'assistant',
              content: [
                {
                  type: 'tool_use',
                  id: `toolu_${name}`,
                  name,
                  input: { ...input, path: long },
                },
              ],
            },
          }),
          1,
        )
      })
      const live = await screen.findByTestId('conversation-live')
      const shown = live.querySelector('[data-slot="path-text"]')
      expect(shown).not.toBeNull()
      expect(shown!.querySelector('code')).toHaveAttribute('title', long)
      expect(live).toHaveTextContent(words)
      expect(live.querySelector('button')).toBeNull()
    },
  )

  // #3135 round 6: a search pattern is the agent's text, never markup. It shows exactly as
  // typed beside the path; only the path fills the PathText slot.
  it.each(['TODO', '<strong>x</strong>', '<path/>', '{{lead}}'])(
    'shows the search pattern %s as plain text in the live line',
    async (pattern) => {
      const long =
        '/home/ada/.claude/projects/-home-ada-work-shop/memory/deep/folder'
      wrap()
      await screen.findByTestId('session-conversation')
      act(() => {
        push(
          JSON.stringify({
            type: 'assistant',
            message: {
              role: 'assistant',
              content: [
                {
                  type: 'tool_use',
                  id: 'toolu_hostile',
                  name: 'Grep',
                  input: { pattern, path: long },
                },
              ],
            },
          }),
          1,
        )
      })
      const live = await screen.findByTestId('conversation-live')
      expect(live.textContent?.startsWith(`Searching ${pattern} · `)).toBe(true)
      expect(live.querySelectorAll('strong')).toHaveLength(0)
      const shown = live.querySelectorAll('[data-slot="path-text"]')
      expect(shown).toHaveLength(1)
      expect(shown[0]!.querySelector('code')).toHaveAttribute('title', long)
    },
  )

  it('a Bash row shows its command in one line, the whole command and its description when opened', async () => {
    const user = userEvent.setup()
    wrap()
    await screen.findByTestId('session-conversation')
    act(() => {
      push(
        JSON.stringify({
          type: 'assistant',
          message: {
            role: 'assistant',
            content: [
              {
                type: 'tool_use',
                id: 'toolu_bash',
                name: 'Bash',
                input: {
                  command: 'echo cli-key-works > cli-key.txt',
                  description: 'Run the command the person asked for',
                },
              },
            ],
          },
        }),
        1,
      )
    })
    const row = (await screen.findByText('Bash')).closest('details')!
    expect(row.textContent).not.toContain('{"command"')
    await user.click(row.querySelector('summary')!)
    const full = row.querySelector('[data-slot="tool-command"]')
    expect(full?.textContent).toBe('echo cli-key-works > cli-key.txt')
    expect(
      screen.getByText('Run the command the person asked for'),
    ).toBeInTheDocument()
  })

  // acceptance (real Claude Code 2.1.287): a long Bash turn printed "Unrecognised frame"
  // for its progress and for the reply to the interrupt the console sent.
  it("shows a running tool's elapsed time on its row and no unrecognised frame", async () => {
    wrap()
    await screen.findByTestId('session-conversation')
    act(() => {
      push(
        JSON.stringify({
          type: 'assistant',
          message: {
            role: 'assistant',
            content: [
              {
                type: 'tool_use',
                id: 'toolu_bash_1',
                name: 'Bash',
                input: { command: 'python3 -c "import time; time.sleep(60)"' },
              },
            ],
          },
        }),
        1,
      )
      push(
        JSON.stringify({
          type: 'tool_progress',
          tool_use_id: 'toolu_bash_1',
          tool_name: 'Bash',
          parent_tool_use_id: null,
          elapsed_time_seconds: 31,
        }),
        2,
      )
      push(
        JSON.stringify({
          type: 'control_response',
          response: { subtype: 'success', request_id: 'olv-interrupt-1' },
        }),
        3,
      )
    })
    const rows = await screen.findAllByTestId('conversation-item')
    expect(rows).toHaveLength(1)
    expect(rows[0]).toHaveAttribute('data-kind', 'tool')
    expect(rows[0]).toHaveTextContent('31')
    expect(screen.queryByText('Unrecognised frame')).toBeNull()
  })

  // The working folder is the Context pane's line (`session-overview.test.tsx`): the
  // transcript draws no line of it, and tells the pane the folder the tool's own first
  // frame names, for a session that recorded none.
  it('draws no folder line and reports the folder the init frame names', async () => {
    const onFolder = vi.fn()
    const qc = new QueryClient({
      defaultOptions: {
        queries: { retry: false },
        mutations: { retry: false },
      },
    })
    render(
      <QueryClientProvider client={qc}>
        <SessionConversation run={run} onFolder={onFolder} />
      </QueryClientProvider>,
    )
    await screen.findByTestId('session-conversation')
    act(() => {
      push(
        JSON.stringify({
          type: 'system',
          subtype: 'init',
          cwd: '/engine/dir',
          tools: ['Read'],
          model: 'claude-opus-5',
        }),
      )
    })
    await waitFor(() => expect(onFolder).toHaveBeenCalledWith('/engine/dir'))
    expect(screen.queryByTestId('conversation-folder')).toBeNull()
    expect(screen.queryByText(/Working folder/)).toBeNull()
  })

  // The first line of the thread is worded by the catalog of the console's language, not
  // by the frame mapper: in German it reads "Sitzung gestartet · <model> · 4 Werkzeuge".
  it('words the session-start line in the console language', async () => {
    await i18n.changeLanguage('de')
    try {
      wrap()
      await screen.findByTestId('session-conversation')
      act(() => {
        push(
          JSON.stringify({
            type: 'system',
            subtype: 'init',
            cwd: '/engine/dir',
            tools: ['Read', 'Bash', 'Write', 'Task'],
            model: 'claude-opus-5',
          }),
        )
      })
      expect(
        await screen.findByText(
          'Sitzung gestartet · Claude Opus 5 · 4 Werkzeuge',
        ),
      ).toBeInTheDocument()
      expect(screen.queryByText(/Session started/)).toBeNull()
    } finally {
      // Unmount first: restoring the language under a mounted tree re-renders it outside act.
      cleanup()
      await i18n.changeLanguage('en')
    }
  })

  it('inspects a row by handing the raw frame to the caller', async () => {
    const onInspect = vi.fn()
    const qc = new QueryClient({
      defaultOptions: {
        queries: { retry: false },
        mutations: { retry: false },
      },
    })
    render(
      <QueryClientProvider client={qc}>
        <SessionConversation run={run} onInspect={onInspect} />
      </QueryClientProvider>,
    )
    await screen.findByTestId('session-conversation')
    act(() => {
      push(
        JSON.stringify({
          type: 'assistant',
          message: {
            role: 'assistant',
            content: [{ type: 'text', text: 'OK' }],
          },
        }),
      )
    })
    await userEvent.click(await screen.findByText('OK'))
    expect(onInspect).toHaveBeenCalled()
    expect(onInspect.mock.calls[0][0].raw[0]).toContain('"type":"assistant"')
  })

  // review on 0df0c254: a cancelled ACP turn showed only "Result".
  it('says in the footer how an ACP turn stopped when it did not just finish', async () => {
    wrap()
    await screen.findByTestId('session-conversation')
    act(() => {
      push(
        JSON.stringify({
          jsonrpc: '2.0',
          id: 3,
          result: { stopReason: 'cancelled' },
        }),
        1,
      )
    })
    const footer = (await screen.findAllByTestId('conversation-item')).find(
      (el) => el.getAttribute('data-kind') === 'result',
    )
    expect(footer).toHaveTextContent('Result')
    expect(footer).toHaveTextContent('Cancelled')
  })

  // acceptance: a failed Codex turn read as "Result · Turn completed".
  it('paints a failed turn as a failure, with its reason', async () => {
    wrap()
    await screen.findByTestId('session-conversation')
    act(() => {
      push(
        JSON.stringify({
          method: 'turn/completed',
          params: {
            turn: {
              id: 't',
              items: [],
              status: 'failed',
              error: { message: 'model not found' },
            },
          },
        }),
        1,
      )
    })
    const line = await screen.findByText('The turn failed: model not found')
    expect(line.closest('[data-testid="conversation-item"]')).toHaveClass(
      'text-danger',
    )
    expect(screen.queryByText('Result')).toBeNull()
  })

  // acceptance: a refused API key read as "api retry", ten times.
  it('says the provider refused the key, once, instead of a line per retry', async () => {
    wrap()
    await screen.findByTestId('session-conversation')
    act(() => {
      for (let seq = 1; seq <= 3; seq++)
        push(
          JSON.stringify({
            type: 'system',
            subtype: 'api_retry',
            error_status: 401,
            error: 'authentication_failed',
          }),
          seq,
        )
    })
    const line = await screen.findByText(
      'The provider refused the API key. Replace it under Providers.',
    )
    expect(line.closest('[data-testid="conversation-item"]')).toHaveClass(
      'text-danger',
    )
    expect(screen.queryByText(/api retry/i)).toBeNull()
  })

  it('counts the retries of any other provider failure, with its status', async () => {
    wrap()
    await screen.findByTestId('session-conversation')
    act(() => {
      for (let seq = 1; seq <= 2; seq++)
        push(
          JSON.stringify({
            type: 'system',
            subtype: 'api_retry',
            error_status: 529,
            error: 'server_error',
          }),
          seq,
        )
    })
    expect(
      await screen.findByText('Retrying (2): server_error (529)'),
    ).toBeInTheDocument()
  })

  // acceptance: 18 "Unrecognised frame" rows for one stderr dump; the failure read as quiet.
  it("shows a tool's output as one line and its failed reply as a failure", async () => {
    wrap()
    await screen.findByTestId('session-conversation')
    act(() => {
      push('Error handling request {', 1)
      push('  jsonrpc: "2.0",', 2)
      push('}', 3)
      push(
        JSON.stringify({
          jsonrpc: '2.0',
          id: 3,
          error: {
            code: -32603,
            message: 'Internal error: Cannot connect to API',
          },
        }),
        4,
      )
    })
    expect(
      await screen.findByText('Tool output (3): Error handling request {'),
    ).toBeInTheDocument()
    expect(screen.queryByText('Unrecognised frame')).toBeNull()
    const failure = screen.getByText('Internal error: Cannot connect to API')
    expect(failure.closest('[data-testid="conversation-item"]')).toHaveClass(
      'text-danger',
    )
  })

  // acceptance: one raw row with "\u001b[2m…\u001b[31mERROR\u001b[0m" in it.
  it("shows a tool's coloured line as words", async () => {
    wrap()
    await screen.findByTestId('session-conversation')
    act(() => {
      push(
        '\u001b[2m2026-10-02T19:30:41.144666Z\u001b[0m \u001b[31mERROR\u001b[0m ' +
          '\u001b[2mcodex_api::endpoint::responses_websocket\u001b[0m\u001b[2m:\u001b[0m ' +
          'failed to connect to websocket: HTTP error: 401 Unauthorized',
        1,
      )
    })
    const line = await screen.findByText(
      /^Tool output \(1\): 2026-10-02T19:30:41/,
    )
    expect(line.textContent).not.toContain('\u001b')
    expect(line.textContent).toContain(
      'ERROR codex_api::endpoint::responses_websocket: failed to connect',
    )
  })

  // acceptance: Codex never started the turn and the person's "Say hi." was nowhere.
  it('shows a first message the engine refused, with its reason', async () => {
    useSentTurns.getState().note(sentTurnsPartition(), 'run_1', {
      text: 'Say hi.',
      refused: 'the provider reports that this profile is not authenticated',
    })
    wrap()
    expect(await screen.findByText('Say hi.')).toBeInTheDocument()
    const reason = screen.getByText(
      'Not sent: the provider reports that this profile is not authenticated',
    )
    expect(reason).toHaveClass('text-danger')
    expect(screen.queryByText('Waiting for the session…')).toBeNull()
  })

  // #500: a launch waiting for an approval holds its first message until the session runs.
  it('shows a first message held for the approval, then the same message sent, once', async () => {
    useSentTurns.getState().note(sentTurnsPartition(), 'run_1', {
      text: 'Say hi.',
      waiting: true,
    })
    wrap()
    expect(await screen.findByText('Say hi.')).toBeInTheDocument()
    expect(screen.getByText('Sent when the session is ready.')).toHaveClass(
      'text-muted-foreground',
    )
    act(() => {
      useSentTurns
        .getState()
        .note(sentTurnsPartition(), 'run_1', { text: 'Say hi.' })
    })
    expect(screen.getAllByTestId('conversation-sent')).toHaveLength(1)
    expect(screen.queryByText('Sent when the session is ready.')).toBeNull()
  })

  // review on 3cf9f18a: a sign-out leaves no person's words behind.
  it('forgets the first message when the sign-in changes', async () => {
    useSentTurns
      .getState()
      .note(sentTurnsPartition(), 'run_1', { text: 'Say hi.' })
    wrap()
    expect(await screen.findByText('Say hi.')).toBeInTheDocument()
    act(() => {
      useSessionStore.setState((s) => ({
        credentialGeneration: s.credentialGeneration + 1,
      }))
    })
    expect(screen.queryByText('Say hi.')).toBeNull()
    expect(useSentTurns.getState().byRun).toEqual({})
  })

  it('shows a sent first message once: until the tool shows it, then as the tool does', async () => {
    useSentTurns
      .getState()
      .note(sentTurnsPartition(), 'run_1', { text: 'Say hi.' })
    wrap()
    await screen.findByTestId('conversation-sent')
    act(() => {
      push(
        JSON.stringify({
          method: 'item/completed',
          params: {
            item: {
              type: 'userMessage',
              id: 'u1',
              content: [{ type: 'text', text: 'Say hi.' }],
            },
          },
        }),
        1,
      )
    })
    expect(await screen.findByText('Say hi.')).toBeInTheDocument()
    expect(screen.getAllByText('Say hi.')).toHaveLength(1)
    expect(screen.queryByTestId('conversation-sent')).toBeNull()
  })

  // F1C: a failed start showed only "Result"; the stored reason had the sentence.
  it("shows a failed result's text as the failure, and not a successful one's", async () => {
    wrap()
    await screen.findByTestId('session-conversation')
    act(() => {
      push(
        JSON.stringify({
          type: 'result',
          subtype: 'error',
          is_error: true,
          result: 'F1 protocol fixture: deliberate provider start failure',
        }),
        1,
      )
      push(
        JSON.stringify({
          type: 'result',
          subtype: 'success',
          is_error: false,
          result: 'the reply, again',
        }),
        2,
      )
    })
    const failure = await screen.findByText(
      'F1 protocol fixture: deliberate provider start failure',
    )
    expect(failure).toHaveClass('text-danger')
    expect(screen.queryByText('the reply, again')).toBeNull()
  })

  // An approval Olivares refused because the session's sign-in had ended read only as an
  // interrupted turn; the engine's notice says why, whole, before the turn ends.
  it('shows the engine notice of a refused approval in its words, as a warning, before the turn ends', async () => {
    const message =
      'Olivares refused the tool request because this session’s access has ended.'
    wrap()
    await screen.findByTestId('session-conversation')
    act(() => {
      push(
        JSON.stringify({
          jsonrpc: '2.0',
          method: 'turn/started',
          params: { turn: { id: 't1' } },
        }),
        1,
      )
      push(
        JSON.stringify({
          type: 'olivares_notice',
          code: 'approval_refused',
          cause: 'access_ended',
          message,
        }),
        2,
      )
      push(
        JSON.stringify({
          jsonrpc: '2.0',
          method: 'turn/completed',
          params: { turn: { id: 't1', status: 'interrupted' } },
        }),
        3,
      )
    })
    const notice = await screen.findByText(message)
    expect(notice).not.toHaveClass('truncate')
    const row = notice.closest('[data-testid="conversation-item"]')!
    expect(row).toHaveClass('text-warning')
    // The turn's start is protocol bookkeeping (no row): the notice, then the turn's end.
    const rows = screen.getAllByTestId('conversation-item')
    expect(rows.map((r) => r.dataset.kind)).toEqual(['system', 'result'])
    expect(rows[0]).toBe(row)
  })

  // A Codex session read "Protocol frames: N" between every turn: handshakes, replies and
  // lifecycle notifications drew a row that told nothing.
  it('draws no row for protocol bookkeeping, and still shows a protocol error', async () => {
    wrap()
    await screen.findByTestId('session-conversation')
    act(() => {
      push(JSON.stringify({ id: 1, result: { thread: { id: 'th' } } }), 1)
      push(
        JSON.stringify({
          method: 'turn/started',
          params: { turn: { id: 't1' } },
        }),
        2,
      )
      push(
        JSON.stringify({
          method: 'item/completed',
          params: { item: { type: 'agentMessage', id: 'm1', text: 'Hello.' } },
        }),
        3,
      )
      push(
        JSON.stringify({
          method: 'thread/tokenUsage/updated',
          params: { tokenUsage: {} },
        }),
        4,
      )
      push(
        JSON.stringify({
          method: 'turn/completed',
          params: { turn: { id: 't1', status: 'completed' } },
        }),
        5,
      )
      push(
        JSON.stringify({
          id: 2,
          error: { code: -1, message: 'thread not found' },
        }),
        6,
      )
    })
    expect(await screen.findByText('Hello.')).toBeInTheDocument()
    expect(screen.queryByText(/Protocol frames/)).toBeNull()
    expect(screen.getByText('thread not found')).toBeInTheDocument()
    expect(
      screen.getAllByTestId('conversation-item').map((r) => r.dataset.kind),
    ).toEqual(['assistant', 'result', 'system'])
  })

  it('still says it waits while only the handshake has arrived', async () => {
    wrap()
    await screen.findByTestId('session-conversation')
    act(() => {
      push(JSON.stringify({ id: 1, result: { thread: { id: 'th' } } }), 1)
    })
    expect(
      await screen.findByText('Waiting for the session…'),
    ).toBeInTheDocument()
    expect(screen.queryByTestId('conversation-item')).toBeNull()
  })
})

// THE TRANSCRIPT IS ONE CENTERED COLUMN: the person's turn is a quiet block, the
// assistant's is plain text, tool calls are 13 px rows, and the end of a turn is a divider.
describe('SessionConversation — the thread reads as a thread', () => {
  const user = (text: string) =>
    JSON.stringify({ type: 'user', message: { role: 'user', content: text } })
  const assistant = (text: string) =>
    JSON.stringify({
      type: 'assistant',
      message: { role: 'assistant', content: [{ type: 'text', text }] },
    })
  const toolUse = (id: string, name: string, input: object) =>
    JSON.stringify({
      type: 'assistant',
      message: {
        role: 'assistant',
        content: [{ type: 'tool_use', id, name, input }],
      },
    })
  const toolResult = (id: string) =>
    JSON.stringify({
      type: 'user',
      message: {
        role: 'user',
        content: [{ type: 'tool_result', tool_use_id: id, content: 'ok' }],
      },
    })

  const item = (kind: string) =>
    screen
      .getAllByTestId('conversation-item')
      .filter((el) => el.getAttribute('data-kind') === kind)

  it('keeps the column to 760 px, centered', async () => {
    wrap()
    const log = await screen.findByRole('log')
    const column = log.firstElementChild as HTMLElement
    expect(column.className).toContain('max-w-[760px]')
    expect(column.className).toContain('mx-auto')
  })

  it("paints the person's turn as a muted block of the column's width, with no YOU label", async () => {
    wrap()
    await screen.findByTestId('session-conversation')
    act(() => push(user('What does calc.py do?'), 1))
    const [turn] = await waitFor(() => {
      const found = item('operator')
      expect(found).toHaveLength(1)
      return found
    })
    expect(turn).toHaveTextContent('What does calc.py do?')
    const cls = turn.className.split(/\s+/)
    expect(cls).toEqual(
      expect.arrayContaining([
        'bg-muted',
        'w-full',
        'rounded-[12px]',
        'px-4',
        'py-3',
      ]),
    )
    expect(screen.queryByText('You')).toBeNull()
    expect(screen.queryByText('YOU')).toBeNull()
  })

  it('paints the assistant as plain 15/24 text: no bubble, no ASSISTANT label', async () => {
    wrap()
    await screen.findByTestId('session-conversation')
    act(() => push(assistant('calc.py defines add(a, b).'), 1))
    const [reply] = await waitFor(() => {
      const found = item('assistant')
      expect(found).toHaveLength(1)
      return found
    })
    const cls = reply.className
    expect(cls).toContain('text-body-l')
    expect(cls).not.toMatch(/bg-|border|rounded-md/)
    expect(screen.queryByText('Assistant')).toBeNull()
    expect(screen.queryByText('ASSISTANT')).toBeNull()
  })

  it('paints a tool call as a 13 px row: verb, target in mono, and a check once it answered', async () => {
    wrap()
    await screen.findByTestId('session-conversation')
    act(() => {
      push(toolUse('t1', 'Read', { file_path: 'calc.py' }), 1)
      push(toolResult('t1'), 2)
    })
    const [row] = await waitFor(() => {
      const found = item('tool')
      expect(found).toHaveLength(1)
      return found
    })
    const summary = row.querySelector('summary') as HTMLElement
    expect(summary.className).toContain('text-caption')
    expect(summary).toHaveTextContent('Read')
    const target = summary.querySelector('.font-mono') as HTMLElement
    expect(target).toHaveTextContent('calc.py')
    expect(summary.querySelector('svg.lucide-check')).not.toBeNull()
  })

  it('folds the calls between two replies into one closed row that keeps them one click away', async () => {
    const userEv = userEvent.setup()
    wrap()
    await screen.findByTestId('session-conversation')
    act(() => {
      for (let i = 1; i <= 5; i++) {
        push(toolUse(`t${i}`, 'Read', { file_path: `f${i}.py` }), 2 * i)
        push(toolResult(`t${i}`), 2 * i + 1)
      }
    })
    const fold = await screen.findByTestId('conversation-work')
    expect(fold.querySelector('summary')).toHaveTextContent(/^Read 5 files$/)
    expect(fold.hasAttribute('open')).toBe(false)
    expect(within(fold).getAllByTestId('conversation-item')).toHaveLength(5)
    await userEv.click(fold.querySelector('summary') as HTMLElement)
    expect(fold.hasAttribute('open')).toBe(true)
  })

  it('folds a single call too: the reply is not cut by a lone tool row', async () => {
    wrap()
    await screen.findByTestId('session-conversation')
    act(() => {
      push(assistant('I will read it.'), 1)
      push(toolUse('t1', 'Read', { file_path: 'f1.py' }), 2)
      push(toolResult('t1'), 3)
      push(assistant('It defines add.'), 4)
    })
    const fold = await screen.findByTestId('conversation-work')
    expect(fold.querySelector('summary')).toHaveTextContent(/^Read 1 file$/)
    expect(screen.getAllByTestId('conversation-work')).toHaveLength(1)
  })

  it('ends a turn with a centered "Finished in" divider that opens the same content', async () => {
    const userEv = userEvent.setup()
    wrap()
    await screen.findByTestId('session-conversation')
    act(() =>
      push(
        JSON.stringify({
          type: 'result',
          subtype: 'success',
          is_error: false,
          result: 'done',
          duration_ms: 8000,
          usage: { input_tokens: 12, output_tokens: 18 },
        }),
        1,
      ),
    )
    const [divider] = await waitFor(() => {
      const found = item('result')
      expect(found).toHaveLength(1)
      return found
    })
    const summary = divider.querySelector('summary') as HTMLElement
    expect(summary).toHaveTextContent('Finished in 8s')
    expect(summary.className).toContain('text-overline')
    expect(summary.className).toContain('items-center')
    expect(divider.hasAttribute('open')).toBe(false)
    await userEv.click(summary)
    expect(divider.hasAttribute('open')).toBe(true)
    expect(screen.queryByRole('button', { name: 'Result' })).toBeNull()
  })

  it('says "Result" when no duration is known', async () => {
    wrap()
    await screen.findByTestId('session-conversation')
    act(() =>
      push(
        JSON.stringify({
          type: 'result',
          subtype: 'success',
          is_error: false,
          result: 'done',
        }),
        1,
      ),
    )
    const [divider] = await waitFor(() => {
      const found = item('result')
      expect(found).toHaveLength(1)
      return found
    })
    expect(divider.querySelector('summary')).toHaveTextContent(/^Result$/)
  })

  it.each([
    ['error', 'Reconnecting…'],
    ['connecting', 'Connecting…'],
  ] as const)(
    'says once, quietly, that the link is %s while the turns already read stay',
    async (status, words) => {
      wrap()
      await screen.findByTestId('session-conversation')
      act(() => push(assistant('first reply'), 1))
      await screen.findByText('first reply')
      expect(screen.queryByTestId('conversation-link')).toBeNull()
      cleanup()
      attachStatus.value = status
      wrap()
      await screen.findByTestId('session-conversation')
      // Connection state is visible before the first frame too.
      expect(await screen.findByTestId('conversation-link')).toHaveTextContent(
        words,
      )
      act(() => push(assistant('second reply'), 1))
      const line = await screen.findByTestId('conversation-link')
      expect(line).toHaveTextContent(words)
      expect(line).toHaveAttribute('role', 'status')
      expect(screen.getByText('second reply')).toBeInTheDocument()
    },
  )

  it("keeps the duration on a failed turn's divider", async () => {
    wrap()
    await screen.findByTestId('session-conversation')
    act(() =>
      push(
        JSON.stringify({
          type: 'result',
          subtype: 'error',
          is_error: true,
          result: 'provider refused',
          duration_ms: 8000,
        }),
        1,
      ),
    )
    const [divider] = await waitFor(() => {
      const found = item('result')
      expect(found).toHaveLength(1)
      return found
    })
    expect(divider.querySelector('summary')).toHaveTextContent('Result · 8s')
  })
})

it('shows an initial attach failure while a sent turn is pending', async () => {
  attachStatus.value = 'error'
  wrap()
  act(() =>
    useSentTurns
      .getState()
      .note(sentTurnsPartition(), run.run_ref, { text: 'pending question' }),
  )
  expect(await screen.findByTestId('conversation-link')).toHaveTextContent(
    'Reconnecting…',
  )
  expect(screen.queryByText('No messages yet.')).toBeNull()
})

// #1784: the released 0.1 painted two "thinking tokens" rows before the answer and an
// "allowed · five hour" line after it. They are the tool's bookkeeping, not turns.
describe('SessionConversation — the tool’s bookkeeping stays out of the thread (#1784)', () => {
  const thinking = (estimated: number) =>
    JSON.stringify({
      type: 'system',
      subtype: 'thinking_tokens',
      estimated_tokens: estimated,
      estimated_tokens_delta: estimated,
      user_message_uuid: '3b3cbd79-7c2a-47f5-8d52-7e7d0a1b5c11',
    })
  const answer = (text: string) =>
    JSON.stringify({
      type: 'assistant',
      message: { role: 'assistant', content: [{ type: 'text', text }] },
    })
  const rateLimit = (status: string) =>
    JSON.stringify({
      type: 'rate_limit_event',
      rate_limit_info: {
        status,
        resetsAt: 1760270400,
        rateLimitType: 'five_hour',
      },
    })

  it('paints neither thinking-token counters nor an allowed rate limit', async () => {
    wrap()
    await screen.findByTestId('session-conversation')
    act(() => {
      push(thinking(120), 1)
      push(thinking(412), 2)
      push(answer('The folder holds two files.'), 3)
      push(rateLimit('allowed'), 4)
    })
    await screen.findByText('The folder holds two files.')
    expect(
      screen
        .getAllByTestId('conversation-item')
        .map((row) => row.getAttribute('data-kind')),
    ).toEqual(['assistant'])
    expect(screen.queryByText(/thinking tokens/i)).toBeNull()
    expect(screen.queryByText(/five hour/i)).toBeNull()
  })

  it('tells the person, in words, that a limit stopped the turn and when it resets', async () => {
    wrap()
    await screen.findByTestId('session-conversation')
    act(() => push(rateLimit('rejected'), 1))
    expect(
      await screen.findByText(
        /^Claude Code reached its 5-hour limit; it resets .+$/,
      ),
    ).toBeInTheDocument()
    expect(screen.queryByText(/rejected|five hour/i)).toBeNull()
  })

  it('says how much of the window was used when the tool said so', async () => {
    wrap()
    await screen.findByTestId('session-conversation')
    act(() =>
      push(
        JSON.stringify({
          type: 'rate_limit_event',
          rate_limit_info: {
            status: 'rejected',
            resetsAt: 1760270400,
            rateLimitType: 'seven_day',
            utilization: 0.93,
          },
        }),
        1,
      ),
    )
    expect(
      await screen.findByText(
        /^Claude Code reached its weekly limit \(93% used\); it resets .+$/,
      ),
    ).toBeInTheDocument()
  })
})

// #1784: the released 0.1 painted the assistant's answer as the characters it was sent in
// ("- **Identify clients:** ..."). The reply is markdown: lists, code and bold are shown as
// such. It is read into elements, never into HTML, because what the model says can be
// shaped by what it read.
describe('SessionConversation — the assistant reply is rendered markdown (#1784)', () => {
  const answer = (text: string) =>
    JSON.stringify({
      type: 'assistant',
      message: { role: 'assistant', content: [{ type: 'text', text }] },
    })
  const reply = async (text: string, onInspect?: (item: unknown) => void) => {
    const qc = new QueryClient({
      defaultOptions: {
        queries: { retry: false },
        mutations: { retry: false },
      },
    })
    render(
      <QueryClientProvider client={qc}>
        <SessionConversation run={run} onInspect={onInspect} />
      </QueryClientProvider>,
    )
    await screen.findByTestId('session-conversation')
    act(() => push(answer(text), 1))
    return waitFor(() => {
      const row = screen
        .getAllByTestId('conversation-item')
        .find((el) => el.getAttribute('data-kind') === 'assistant')
      expect(row).toBeDefined()
      return row as HTMLElement
    })
  }

  it('paints lists, bold and code as formatted elements, with no markers left', async () => {
    const row = await reply(
      [
        'To find them:',
        '',
        '- **Identify clients:** list who calls the API',
        '- Check `config.yaml`',
        '',
        '1. Run the suite',
        '2. Read the report',
        '',
        '```sh',
        'make test',
        '```',
      ].join('\n'),
    )
    expect(within(row).getAllByRole('listitem')).toHaveLength(4)
    expect(within(row).getByText('Identify clients:').tagName).toBe('STRONG')
    expect(row.querySelector('li code')).toHaveTextContent('config.yaml')
    expect(row.querySelector('ol')).not.toBeNull()
    expect(row.querySelector('pre code')).toHaveTextContent('make test')
    expect(row.textContent).not.toContain('**')
    expect(row.textContent).not.toContain('```')
    expect(row.textContent).not.toContain('`config.yaml`')
  })

  it('shows markup in the reply as the characters it was sent in', async () => {
    const row = await reply(
      'Try <img src=x onerror="alert(1)"> and <script>alert(2)</script> today',
    )
    expect(row.querySelector('img, script')).toBeNull()
    expect(row).toHaveTextContent('<img src=x onerror="alert(1)">')
    expect(row).toHaveTextContent('<script>alert(2)</script>')
  })

  it('links only to web and mail addresses, and opens them without the opener', async () => {
    const row = await reply(
      'See [the docs](https://example.com/a?b=1) or [mail](mailto:a@example.com), not [this](javascript:alert(1)) or [that](data:text/html;base64,AAAA).',
    )
    const links = Array.from(row.querySelectorAll('a'))
    expect(links.map((a) => a.getAttribute('href'))).toEqual([
      'https://example.com/a?b=1',
      'mailto:a@example.com',
    ])
    for (const a of links) {
      expect(a.getAttribute('target')).toBe('_blank')
      expect(a.getAttribute('rel')).toBe('noopener noreferrer')
    }
    // an address that is not a web or mail one stays in the text as it was written
    expect(row).toHaveTextContent(
      'not [this](javascript:alert(1)) or [that](data:text/html;base64,AAAA).',
    )
  })

  it('says where a link goes, and never nests one link in another', async () => {
    const row = await reply(
      '[https://bank.example](https://evil.example) and [a [b](https://x.y)](https://z.w)',
    )
    const links = within(row).getAllByRole('link')
    expect(links).toHaveLength(2)
    expect(links[0]).toHaveAttribute('title', 'https://evil.example')
    expect(row.querySelector('a a')).toBeNull()
    expect(links[1]).toHaveTextContent('a b (https://x.y)')
  })

  it('paints headings, quotes, tables, rules and nested lists as elements', async () => {
    const row = await reply(
      [
        '## Plan',
        '',
        '> note',
        '',
        '| name | size |',
        '|---|---|',
        '| a | 1 |',
        '',
        '---',
        '',
        '3. three',
        '   - nested',
        '4. four',
      ].join('\n'),
    )
    expect(row.textContent).not.toContain('##')
    expect(within(row).getByText('Plan')).toBeInTheDocument()
    expect(row.querySelector('blockquote')).toHaveTextContent('note')
    expect(
      within(row)
        .getAllByRole('columnheader')
        .map((c) => c.textContent),
    ).toEqual(['name', 'size'])
    expect(within(row).getByRole('cell', { name: '1' })).toBeInTheDocument()
    expect(row.querySelector('hr')).not.toBeNull()
    const steps = row.querySelector('ol')!
    expect(steps.getAttribute('start')).toBe('3')
    expect(steps.querySelector('ul li')).toHaveTextContent('nested')
  })

  it('keeps the lines and indentation of a paragraph as they were written', async () => {
    const row = await reply('Traceback:\n  File "a.py", line 3')
    const paragraph = row.querySelector('p')!
    expect(paragraph.textContent).toBe('Traceback:\n  File "a.py", line 3')
    expect(paragraph.className).toContain('whitespace-pre-wrap')
  })

  it('repaints the reply as it streams in', async () => {
    wrap()
    await screen.findByTestId('session-conversation')
    const delta = (text: string) =>
      JSON.stringify({
        type: 'stream_event',
        event: {
          type: 'content_block_delta',
          index: 0,
          delta: { type: 'text_delta', text },
        },
      })
    act(() => push(delta('Hel'), 1))
    await screen.findByText('Hel')
    act(() => push(delta('lo **world**'), 2))
    const strong = await screen.findByText('world')
    expect(strong.tagName).toBe('STRONG')
    expect(screen.getAllByTestId('conversation-item')).toHaveLength(1)
  })

  it('does not open the inspector while its words are selected, nor on a double click', async () => {
    const onInspect = vi.fn()
    const row = await reply('Pick **this** word', onInspect)
    const words = row.querySelector('p')!
    window.getSelection()!.selectAllChildren(words)
    fireEvent.click(words)
    expect(onInspect).not.toHaveBeenCalled()
    window.getSelection()!.removeAllRanges()
    fireEvent.click(words, { detail: 2 })
    expect(onInspect).not.toHaveBeenCalled()
    // a selection somewhere else on the page is not this reply's
    const elsewhere = document.createElement('p')
    elsewhere.textContent = 'elsewhere'
    document.body.append(elsewhere)
    window.getSelection()!.selectAllChildren(elsewhere)
    fireEvent.click(words, { detail: 1 })
    expect(onInspect).toHaveBeenCalledTimes(1)
    elsewhere.remove()
    window.getSelection()!.removeAllRanges()
  })

  it('still opens the inspector from the reply, but not from a link inside it', async () => {
    const onInspect = vi.fn()
    const row = await reply(
      'Read [**the docs**](https://example.com/a) first.',
      onInspect,
    )
    // the click lands on the bold text inside the link, not on the anchor itself
    await userEvent.click(within(row).getByText('the docs'))
    expect(onInspect).not.toHaveBeenCalled()
    await userEvent.click(within(row).getByText(/first\./))
    expect(onInspect).toHaveBeenCalledTimes(1)
    expect(onInspect.mock.calls[0]![0]).toMatchObject({ kind: 'assistant' })
  })

  it('keeps the inspector one keystroke away without a pointer', async () => {
    const onInspect = vi.fn()
    const row = await reply('OK', onInspect)
    const inspect = within(row).getByRole('button', { name: /inspect/i })
    // the log region is the first stop; the reply's own button is the next
    for (let i = 0; i < 4 && document.activeElement !== inspect; i++)
      await userEvent.tab()
    expect(inspect).toHaveFocus()
    await userEvent.keyboard('{Enter}')
    expect(onInspect).toHaveBeenCalledTimes(1)
  })
})

// The side pane's Activity reads the tool's transcript again when the tool writes: once
// per burst of frames, not once per frame.
describe('SessionConversation and the Activity tab', () => {
  it('asks for a fresh activity read once per burst of frames', async () => {
    vi.useFakeTimers()
    try {
      const qc = new QueryClient()
      const invalidate = vi.spyOn(qc, 'invalidateQueries')
      render(
        <QueryClientProvider client={qc}>
          <SessionConversation run={run} />
        </QueryClientProvider>,
      )
      act(() => {
        push('{"type":"system","subtype":"init","session_id":"s"}', 1)
        push('{"type":"assistant","message":{"content":[]}}', 2)
        push('{"type":"user","message":{"content":[]}}', 3)
      })
      const activityKey = agentOpsKeys.runActivity(
        useTenantStore.getState().activeTenant,
        'run_1',
      )
      const reads = () =>
        invalidate.mock.calls.filter(
          ([filters]) =>
            JSON.stringify(filters?.queryKey) === JSON.stringify(activityKey),
        ).length
      expect(reads()).toBe(0)
      await act(async () => {
        await vi.advanceTimersByTimeAsync(1_600)
      })
      expect(reads()).toBe(1)
      act(() => push('{"type":"assistant","message":{"content":[]}}', 4))
      await act(async () => {
        await vi.advanceTimersByTimeAsync(1_600)
      })
      expect(reads()).toBe(2)
    } finally {
      vi.useRealTimers()
    }
  })
})

// #2129: the person's messages as a rail beside the thread. The index is the engine's
// (the transcript the tool keeps); a mark reaches the row of the thread that message is.
describe('SessionConversation — the message navigator', () => {
  beforeEach(() => useTenantStore.setState({ activeTenant: 'demo' }))
  afterEach(() => vi.restoreAllMocks())
  const message = (id: number, line: string, text = line) => ({
    id,
    role: 'user' as const,
    at: '2026-10-14T10:00:00.000Z',
    first_line: line,
    text,
  })

  it('reaches, highlights and announces the row of a message sent', async () => {
    const asked = vi
      .spyOn(agentOpsApi, 'messages')
      .mockImplementation(async (_ref, params) => ({
        driver: 'claude',
        transcript: 'read',
        total: 2,
        items:
          params?.id === 1
            ? [message(1, 'fix the failing test')]
            : [
                message(1, 'fix the failing test'),
                message(3, 'now the linter'),
              ],
      }))
    const userEvents = userEvent.setup()
    wrap()
    act(() => {
      push(
        JSON.stringify({
          type: 'user',
          message: { role: 'user', content: 'fix the failing test' },
        }),
        1,
      )
      push(
        JSON.stringify({
          type: 'assistant',
          message: {
            role: 'assistant',
            content: [{ type: 'text', text: 'Done.' }],
          },
        }),
        2,
      )
      push(
        JSON.stringify({
          type: 'user',
          message: { role: 'user', content: 'now the linter' },
        }),
        3,
      )
    })
    const marks = await screen.findAllByTestId('message-mark')
    expect(marks).toHaveLength(2)
    // The column keeps clear of the rail, so a message's Copy is never under it (#2856).
    expect(
      document.querySelector('[data-slot="conversation-column"]'),
    ).toHaveClass('pe-12')
    expect(marks[0]).toHaveAccessibleName(/Message 1, .*fix the failing test/)

    await userEvents.click(marks[0]!)
    const rows = screen.getAllByTestId('conversation-item')
    await waitFor(() => expect(rows[0]!.closest('li')).toHaveClass('ring-2'))
    // Every kind of row names its item, so a jump to an answer finds it too.
    for (const row of rows) expect(row).toHaveAttribute('data-item-id')
    expect(rows[1]).toHaveAttribute('data-kind', 'assistant')
    expect(rows[2]!.closest('li')).not.toHaveClass('ring-2')
    expect(screen.getByRole('status')).toHaveTextContent('Jumped to message 1.')
    expect(asked).toHaveBeenCalledWith(
      'run_1',
      { order: 'desc', limit: 200 },
      expect.anything(),
    )
  })

  it('draws nothing when the session has no messages the engine can read', async () => {
    const asked = vi
      .spyOn(agentOpsApi, 'messages')
      .mockRejectedValue(new ApiError(422, 'unprocessable', 'another tool'))
    wrap()
    await waitFor(() => expect(asked).toHaveBeenCalled())
    await waitFor(() => expect(qcIdle()).toBe(true))
    expect(screen.queryByTestId('message-rail')).not.toBeInTheDocument()
    expect(
      document.querySelector('[data-slot="conversation-column"]'),
    ).not.toHaveClass('pe-12')
    expect(screen.queryByTestId('message-bar')).not.toBeInTheDocument()
    expect(screen.queryByTestId('message-problem')).not.toBeInTheDocument()
  })

  // #2258: the engine answers 422 for a tool that keeps its record another way, and the
  // browser logs every failed request as a console error, so the first-hour journey on an
  // OpenCode session failed on a request the console never needed to make.
  it('does not ask a tool that keeps no transcript for its messages', async () => {
    const asked = vi.spyOn(agentOpsApi, 'messages').mockResolvedValue({
      driver: 'claude',
      transcript: 'read',
      total: 0,
      items: [],
    })
    wrap({ provider_driver: 'opencode' })
    await waitFor(() => expect(qcIdle()).toBe(true))
    expect(asked).not.toHaveBeenCalled()
    expect(screen.queryByTestId('message-bar')).not.toBeInTheDocument()
    expect(screen.queryByTestId('message-problem')).not.toBeInTheDocument()
  })

  // A legacy run names no driver; the engine answers for it (409), so it is still asked.
  it('asks a run that names no driver for its messages', async () => {
    const asked = vi.spyOn(agentOpsApi, 'messages').mockResolvedValue({
      driver: 'claude',
      transcript: 'read',
      total: 0,
      items: [],
    })
    wrap({ provider_driver: undefined })
    await waitFor(() => expect(asked).toHaveBeenCalledTimes(1))
  })

  it.each(['claude', 'codex'])(
    'asks a %s session for its messages',
    async (provider_driver) => {
      const asked = vi.spyOn(agentOpsApi, 'messages').mockResolvedValue({
        driver: provider_driver,
        transcript: 'read',
        total: 1,
        items: [message(1, 'fix the failing test')],
      })
      wrap({ provider_driver })
      await waitFor(() => expect(asked).toHaveBeenCalledTimes(1))
      // The rail marks messages the conversation shows.
      act(() =>
        push(
          JSON.stringify({
            type: 'user',
            message: { role: 'user', content: 'fix the failing test' },
          }),
          1,
        ),
      )
      expect(await screen.findAllByTestId('message-mark')).toHaveLength(1)
    },
  )

  it('reads the index again when the person sends another message, not on every burst of output', async () => {
    const asked = vi.spyOn(agentOpsApi, 'messages').mockResolvedValue({
      driver: 'claude',
      transcript: 'read',
      total: 1,
      items: [message(1, 'fix the failing test')],
    })
    wrap()
    await waitFor(() => expect(asked).toHaveBeenCalledTimes(1))
    const send = (role: 'user' | 'assistant', content: unknown, seq: number) =>
      act(() =>
        push(JSON.stringify({ type: role, message: { role, content } }), seq),
      )
    send('user', 'fix the failing test', 1)
    await waitFor(() => expect(asked).toHaveBeenCalledTimes(2))
    // Output that is not a message sent changes nothing in the index.
    send('assistant', [{ type: 'text', text: 'ok' }], 2)
    await new Promise((resolve) => setTimeout(resolve, 50))
    expect(asked).toHaveBeenCalledTimes(2)
    send('user', 'now the linter', 3)
    await waitFor(() => expect(asked).toHaveBeenCalledTimes(3))
  })
})

// the regression: a session that started on claude-opus-5-5 went on on claude-opus-4-8 silently.
describe('SessionConversation — the model in use', () => {
  const reply = (model: string, text: string) =>
    JSON.stringify({
      type: 'assistant',
      parent_tool_use_id: null,
      message: { model, content: [{ type: 'text', text }] },
    })
  const result = JSON.stringify({
    type: 'result',
    subtype: 'success',
    duration_ms: 1200,
  })

  it('names each turn after its model and tells a change in the conversation, with its time', () => {
    const onModel = vi.fn()
    const qc = new QueryClient()
    render(
      <QueryClientProvider client={qc}>
        {/* the session page always passes onInspect (work-surface.tsx) */}
        <SessionConversation run={run} onModel={onModel} onInspect={() => {}} />
      </QueryClientProvider>,
    )
    act(() => {
      push(reply('claude-opus-5-5', 'one'), 1)
      push(result, 2)
      push(reply('claude-opus-4-8', 'two'), 3, '2026-10-21T12:03:46Z')
    })
    expect(
      screen.getByText(/Finished in .+ · Claude Opus 5\.5/),
    ).toBeInTheDocument()
    const notice = screen
      .getAllByTestId('conversation-item')
      .find((el) => el.getAttribute('data-kind') === 'system')
    expect(notice).toHaveTextContent(
      /^Model changed at .+: Claude Opus 5\.5 → Claude Opus 4\.8$/,
    )
    expect(onModel).toHaveBeenLastCalledWith('claude-opus-4-8')
  })
})

// the regression: a message the person sends shows at once, and once, even when its words
// repeat an earlier message the tool already showed.
describe('SessionConversation — a sent message', () => {
  const echo = (text: string) =>
    JSON.stringify({ type: 'user', message: { role: 'user', content: text } })

  it('forgets a message once the tool shows it, so a later window cannot show it again', async () => {
    wrap()
    act(() => {
      useSentTurns
        .getState()
        .note(sentTurnsPartition(), 'run_1', { text: 'Look up the readme' })
    })
    await screen.findByTestId('conversation-sent')
    act(() => push(echo('Look up the readme'), 1))
    await waitFor(() => expect(useSentTurns.getState().byRun.run_1).toEqual([]))
  })

  it('says a message was never taken when the session ended first', async () => {
    useSentTurns
      .getState()
      .note(sentTurnsPartition(), 'run_1', { text: 'Look up the readme' })
    wrap({ state: 'stopped' })
    expect(await screen.findByTestId('conversation-sent')).toHaveTextContent(
      'The session ended before the tool took it.',
    )
  })

  it('places a message sent right after a remount after what the session showed before', async () => {
    const first = wrap()
    act(() => {
      push(echo('continue'), 1)
      push(echo('next'), 2)
    })
    await screen.findByText('next')
    first.unmount()
    wrap()
    // Sent before the replay of the session's frames reaches the new view.
    act(() => {
      useSentTurns.getState().note(sentTurnsPartition(), 'run_1', {
        text: 'continue',
        after: useSentTurns.getState().seen.run_1,
      })
    })
    act(() => {
      push(echo('continue'), 1)
      push(echo('next'), 2)
    })
    expect(screen.getByTestId('conversation-sent')).toHaveTextContent(
      'continue',
    )
    act(() => push(echo('continue'), 3))
    expect(screen.queryByTestId('conversation-sent')).toBeNull()
  })

  it('shows a repeated message at once and once when the tool shows it', async () => {
    wrap()
    act(() => push(echo('continue'), 1))
    await screen.findByText('continue')
    // Sent as the message bar sends: placed after the newest frame shown.
    act(() => {
      useSentTurns.getState().note(sentTurnsPartition(), 'run_1', {
        text: 'continue',
        after: useSentTurns.getState().seen.run_1,
      })
    })
    expect(screen.getByTestId('conversation-sent')).toHaveTextContent(
      'continue',
    )
    act(() => push(echo('continue'), 2))
    expect(screen.queryByTestId('conversation-sent')).toBeNull()
    expect(
      screen
        .getAllByTestId('conversation-item')
        .filter((el) => el.getAttribute('data-kind') === 'operator'),
    ).toHaveLength(2)
  })

  // Review round 2 of #2855: a Codex view that remounts restores the stored thread first,
  // then the ring replays the same items (seq 1-6 here: two turns of message, answer, end).
  const codexFrames = nativeHistory.turns.flatMap((turn) => [
    ...turn.items.map((item) =>
      JSON.stringify({
        method: 'item/completed',
        params: { item, turnId: turn.id },
      }),
    ),
    JSON.stringify({ method: 'turn/completed', params: { turn } }),
  ])
  // The person saw the first turn (frames 1-3), sent this as the message bar does, and
  // looked elsewhere before the tool took it.
  const sentAfterFirstTurn = (text: string) => {
    const first = wrap()
    act(() => codexFrames.slice(0, 3).forEach((line, i) => push(line, i + 1)))
    act(() =>
      useSentTurns.getState().note(sentTurnsPartition(), 'run_1', {
        text,
        ...sentPlace('run_1', text),
      }),
    )
    first.unmount()
  }

  it('settles a Codex message on a remount, when the stored thread and the ring both show it', () => {
    sentAfterFirstTurn('earlier console prompt')
    wrap()
    act(() => {
      onHistoryRef.current?.(JSON.stringify(nativeHistory))
      codexFrames.forEach((line, i) => push(line, i + 1))
    })
    expect(screen.queryByTestId('conversation-sent')).toBeNull()
  })

  it('settles a Codex message whose echo only the stored thread still shows', () => {
    sentAfterFirstTurn('earlier console prompt')
    wrap()
    // The ring no longer holds frames 4-4099: the echo was one of them.
    act(() => {
      onHistoryRef.current?.(JSON.stringify(nativeHistory))
      push(codexFrames[5]!, 4100)
    })
    expect(screen.queryByTestId('conversation-sent')).toBeNull()
  })

  it('settles a message on an ended session whose stored thread shows it', () => {
    sentAfterFirstTurn('earlier console prompt')
    // The run left this node: the stored thread comes, and no frame.
    wrap({ state: 'stopped' })
    act(() => onHistoryRef.current?.(JSON.stringify(nativeHistory)))
    expect(screen.queryByTestId('conversation-sent')).toBeNull()
  })

  it('never settles a message with a stored row from before it was sent', () => {
    sentAfterFirstTurn('initial CLI prompt')
    wrap({ state: 'stopped' })
    // The stored thread first, then the frames after the send (4-6): none is its echo.
    act(() => onHistoryRef.current?.(JSON.stringify(nativeHistory)))
    expect(screen.getByTestId('conversation-sent')).toHaveTextContent(
      'The session ended before the tool took it.',
    )
    act(() => codexFrames.slice(3).forEach((line, i) => push(line, i + 4)))
    expect(screen.getByTestId('conversation-sent')).toHaveTextContent(
      'initial CLI prompt',
    )
  })

  it('never settles a message with a stored row while the ring no longer holds its frames', () => {
    sentAfterFirstTurn('initial CLI prompt')
    wrap()
    act(() => {
      onHistoryRef.current?.(JSON.stringify(nativeHistory))
      push(codexFrames[5]!, 4100)
    })
    expect(screen.getByTestId('conversation-sent')).toHaveTextContent(
      'initial CLI prompt',
    )
  })

  it('keeps the second of two same messages until the tool shows it too', () => {
    wrap()
    act(() => push(echo('Look up the readme'), 1))
    const send = () =>
      useSentTurns.getState().note(sentTurnsPartition(), 'run_1', {
        text: 'continue',
        ...sentPlace('run_1', 'continue'),
      })
    act(() => {
      send()
      send()
    })
    act(() => push(echo('continue'), 2))
    expect(screen.getAllByTestId('conversation-sent')).toHaveLength(1)
    act(() => push(echo('continue'), 3))
    expect(screen.queryByTestId('conversation-sent')).toBeNull()
  })

  it('keeps the second of two same Codex messages the stored thread shows once', () => {
    sentAfterFirstTurn('earlier console prompt')
    sentAfterFirstTurn('earlier console prompt')
    wrap()
    act(() => {
      onHistoryRef.current?.(JSON.stringify(nativeHistory))
      push(codexFrames[5]!, 4100)
    })
    expect(screen.getAllByTestId('conversation-sent')).toHaveLength(1)
  })
})

// the regression: no message could be copied, neither the person's nor the tool's.
describe('SessionConversation — copying a message', () => {
  const asked = JSON.stringify({
    type: 'user',
    message: { role: 'user', content: 'Look up the readme' },
  })
  const answered = JSON.stringify({
    type: 'assistant',
    parent_tool_use_id: null,
    message: {
      content: [
        {
          type: 'text',
          text: 'Two things:\n\n- **One** thing\n\n```sh\nmake test\n```',
        },
      ],
    },
  })

  it('quotes only selected reply words into this session draft without replacing a comment', async () => {
    const key = sessionDraftKey(
      'demo',
      { kind: 'user', actor: 'alice', user_id: 'alice' },
      'run_1',
    )!
    writeSessionDraft(key, 'Please explain this part.')
    wrap()
    act(() => push(answered))
    const word = await screen.findByText('One')
    const range = document.createRange()
    range.selectNodeContents(word)
    window.getSelection()!.removeAllRanges()
    window.getSelection()!.addRange(range)
    fireEvent.click(screen.getByRole('button', { name: 'Quote reply' }))
    expect(readSessionDraft(key)).toBe('Please explain this part.\n\n> One\n\n')
    window.getSelection()!.removeAllRanges()
    writeSessionDraft(key, '')
  })

  it('does not quote a selection from outside the reply', async () => {
    const key = sessionDraftKey(
      'demo',
      { kind: 'user', actor: 'alice', user_id: 'alice' },
      'run_1',
    )!
    writeSessionDraft(key, '')
    wrap()
    act(() => {
      push(asked, 1)
      push(answered, 2)
    })
    const range = document.createRange()
    range.selectNodeContents(await screen.findByText('Look up the readme'))
    window.getSelection()!.removeAllRanges()
    window.getSelection()!.addRange(range)
    fireEvent.click(screen.getByRole('button', { name: 'Quote reply' }))
    expect(readSessionDraft(key)).toBe(
      '> Two things:\n> \n> - One thing\n> \n> make test\n\n',
    )
    window.getSelection()!.removeAllRanges()
    writeSessionDraft(key, '')
  })

  it('copies one Markdown block with its original markers independently', async () => {
    const user = userEvent.setup()
    const writeText = vi
      .spyOn(navigator.clipboard, 'writeText')
      .mockResolvedValue(undefined)
    wrap()
    act(() => push(answered))
    const buttons = await screen.findAllByRole('button', {
      name: 'Copy Markdown block',
    })
    await user.click(buttons[1]!)
    await waitFor(() =>
      expect(writeText).toHaveBeenCalledWith('- **One** thing'),
    )
    writeText.mockRestore()
  })

  it('keeps copying available to a reader without offering a writable quote', async () => {
    messageRights.writable = false
    wrap()
    act(() => push(answered))
    expect(
      await screen.findByRole('button', { name: 'Copy message' }),
    ).toBeEnabled()
    expect(
      screen.queryByRole('button', { name: 'Quote reply' }),
    ).not.toBeInTheDocument()
  })

  it('leaves every message selectable: none of its words sit in a button', async () => {
    wrap()
    act(() => {
      push(asked, 1)
      push(answered, 2)
    })
    for (const words of ['Look up the readme', 'One', 'make test'])
      expect((await screen.findByText(words)).closest('button')).toBeNull()
  })

  it('offers no Copy for a message without words, and paints no reply without words', async () => {
    wrap()
    act(() => {
      push(
        JSON.stringify({
          type: 'user',
          message: { role: 'user', content: [] },
        }),
        1,
      )
      push(
        JSON.stringify({
          type: 'assistant',
          parent_tool_use_id: null,
          message: { content: [{ type: 'text', text: '' }] },
        }),
        2,
      )
      push(asked, 3)
    })
    await screen.findByText('Look up the readme')
    const kinds = screen
      .getAllByTestId('conversation-item')
      .map((el) => el.getAttribute('data-kind'))
    expect(kinds).toEqual(['operator', 'operator'])
    expect(
      screen.getAllByRole('button', { name: 'Copy message' }),
    ).toHaveLength(1)
  })

  it('copies the exact text of a message, a reply and a code block, by pointer and by keyboard', async () => {
    const writeText = vi
      .spyOn(navigator.clipboard, 'writeText')
      .mockResolvedValue(undefined)
    const user = userEvent.setup()
    wrap()
    act(() => {
      push(asked, 1)
      push(answered, 2)
    })
    const [mine, theirs] = await screen.findAllByRole('button', {
      name: 'Copy message',
    })
    await user.click(mine!)
    expect(writeText).toHaveBeenLastCalledWith('Look up the readme')
    theirs!.focus()
    await user.keyboard('{Enter}')
    expect(writeText).toHaveBeenLastCalledWith(
      'Two things:\n\n- One thing\n\nmake test',
    )
    await user.click(screen.getByRole('button', { name: 'Copy code' }))
    expect(writeText).toHaveBeenLastCalledWith('make test')
    expect(await screen.findAllByText('Copied')).not.toHaveLength(0)
  })
})

describe('SessionConversation — copying a message, review', () => {
  const asked = JSON.stringify({
    type: 'user',
    message: { role: 'user', content: 'Look up the readme' },
  })

  it('says so where it can be seen when the copy fails', async () => {
    vi.spyOn(navigator.clipboard, 'writeText').mockRejectedValue(
      new Error('not allowed'),
    )
    const user = userEvent.setup()
    wrap()
    act(() => push(asked, 1))
    await user.click(
      await screen.findByRole('button', { name: 'Copy message' }),
    )
    // Shown, not only announced: jsdom has no CSS, so the class is the check.
    expect(
      await screen.findByText('Could not copy. Select the text and copy it.'),
    ).not.toHaveClass('sr-only')
  })

  it('opens the inspector from a message by click and by keyboard', async () => {
    const onInspect = vi.fn()
    const user = userEvent.setup()
    const qc = new QueryClient()
    render(
      <QueryClientProvider client={qc}>
        <SessionConversation run={run} onInspect={onInspect} />
      </QueryClientProvider>,
    )
    act(() => push(asked, 1))
    await user.click(await screen.findByText('Look up the readme'))
    expect(onInspect).toHaveBeenCalledTimes(1)
    const row = screen
      .getAllByTestId('conversation-item')
      .find((el) => el.getAttribute('data-kind') === 'operator')!
    // the row holds only its words: Inspect sits beside it, not in it (review of #2856)
    expect(row).toHaveTextContent(/^Look up the readme$/)
    within(row.closest('li')!)
      .getByRole('button', { name: /inspect/i })
      .focus()
    await user.keyboard('{Enter}')
    expect(onInspect).toHaveBeenCalledTimes(2)
  })

  it('copies a message the console sent before the tool shows it', async () => {
    const writeText = vi
      .spyOn(navigator.clipboard, 'writeText')
      .mockResolvedValue(undefined)
    useSentTurns
      .getState()
      .note(sentTurnsPartition(), 'run_1', { text: 'Say hi.', refused: 'no' })
    const user = userEvent.setup()
    wrap()
    await user.click(
      await screen.findByRole('button', { name: 'Copy message' }),
    )
    expect(writeText).toHaveBeenLastCalledWith('Say hi.')
  })
})

describe('SessionConversation — copying, second review', () => {
  it('shows a failed code copy under the code, not over it', async () => {
    vi.spyOn(navigator.clipboard, 'writeText').mockRejectedValue(
      new Error('not allowed'),
    )
    const user = userEvent.setup()
    wrap()
    act(() =>
      push(
        JSON.stringify({
          type: 'assistant',
          parent_tool_use_id: null,
          message: {
            content: [{ type: 'text', text: '```sh\nmake test\n```' }],
          },
        }),
        1,
      ),
    )
    await user.click(await screen.findByRole('button', { name: 'Copy code' }))
    const failure = await screen.findByText(
      'Could not copy. Select the text and copy it.',
    )
    expect(failure).not.toHaveClass('sr-only')
    expect(failure.closest('.absolute')).toBeNull()
  })
})

// THE CONVERSATION READS LIKE A CHAT, NOT AN EVENT LOG. Built from a real
// Claude Code 2.1.309 stream (fixtures/claude-2.1.309-subagents.jsonl, how it was recorded
// is in conversation-frames.test.ts): subagent, background command, task frames, heartbeats,
// thinking-only frames, an edit and a failing command. Each frame is pushed with the time
// the tool stamped on the frames around it, as the engine stamps when it reads a frame.
describe('SessionConversation — the work folds, one live line, nested subagents', () => {
  const REAL = claudeStream.trim().split('\n')
  const stamps = (() => {
    let last = '2026-10-23T11:32:07.000Z'
    return REAL.map((line) => {
      const at = (JSON.parse(line) as { timestamp?: string }).timestamp
      if (at) last = at
      return last
    })
  })()
  const pushReal = (upTo = REAL.length) =>
    act(() => {
      for (let i = 0; i < upTo; i++) push(REAL[i]!, i + 1, stamps[i])
    })

  it('folds the work between the prompt and the reply into one row that says what it did', async () => {
    wrap()
    await screen.findByTestId('session-conversation')
    pushReal()
    const work = await screen.findByTestId('conversation-work')
    expect(screen.getAllByTestId('conversation-work')).toHaveLength(1)
    expect(work.hasAttribute('open')).toBe(false)
    expect(work.querySelector('summary')).toHaveTextContent(
      /^Worked for 1m \d+s · ran 3 commands, edited 1 file · 1 failed$/,
    )
    const kinds = screen
      .getAllByTestId('conversation-item')
      .filter((el) => !work.contains(el))
      .map((el) => el.getAttribute('data-kind'))
    expect(kinds).toEqual(['system', 'operator', 'assistant', 'result'])
    for (const noise of [
      /task (started|progress|updated|notification)/,
      /background tasks/,
      /running · \d+ s/i,
      /allowed/,
      /<task-notification>/,
      /^Assistant$/,
      /Async agent launched/,
      /\{"file_path"/,
    ])
      expect(screen.queryByText(noise)).toBeNull()
  })

  it("nests a subagent's calls under its Agent row, folded, with its name and one-line result", async () => {
    wrap()
    await screen.findByTestId('session-conversation')
    pushReal()
    const agent = await screen.findByTestId('conversation-agent')
    const summary = agent.querySelector('summary') as HTMLElement
    expect(summary).toHaveTextContent('Count entries')
    expect(summary).toHaveTextContent(/There are 4 entries/)
    expect(agent.hasAttribute('open')).toBe(false)
    const calls = within(agent)
      .getAllByTestId('conversation-item')
      .map((el) => el.getAttribute('data-kind'))
    expect(calls).toEqual(['tool', 'tool', 'tool'])
  })

  it('names a failed call and never counts it as done', async () => {
    wrap()
    await screen.findByTestId('session-conversation')
    pushReal()
    const work = await screen.findByTestId('conversation-work')
    const failed = within(work)
      .getAllByTestId('conversation-item')
      .find((el) => el.textContent?.includes('&& false')) as HTMLElement
    expect(failed.querySelector('summary')).toHaveTextContent('Failed')
    expect(failed.querySelector('svg.lucide-check')).toBeNull()
  })

  it('shows an edit with its +N −M and a short excerpt of its diff', async () => {
    wrap()
    await screen.findByTestId('session-conversation')
    pushReal()
    const work = await screen.findByTestId('conversation-work')
    const edit = within(work)
      .getAllByTestId('conversation-item')
      .find((el) =>
        el.querySelector('summary')?.textContent?.startsWith('Edit'),
      )
    expect(edit?.querySelector('summary')).toHaveTextContent(
      /^Editnotes\.txt\+3−2$/,
    )
    const excerpt = within(work).getAllByTestId('conversation-diff')[1]
    expect(excerpt?.textContent).toBe('alpha-beta-gamma+delta+zeta+eta')
  })

  it('shows one live line while a call runs, with its elapsed time, and no heartbeat rows', async () => {
    wrap()
    await screen.findByTestId('session-conversation')
    // Up to the heartbeat of the 35 s command (frame 8), stamped now.
    act(() => {
      for (let i = 0; i <= 8; i++)
        push(REAL[i]!, i + 1, new Date().toISOString())
    })
    const live = await screen.findByTestId('conversation-live')
    expect(live).toHaveTextContent(/^Running 1 shell command · 30s$/)
    // Outside a pane with its composer (the session sheet is a dialog whose Escape closes
    // it) no Escape hint is offered.
    expect(live).not.toHaveTextContent('Esc')
    expect(screen.getAllByTestId('conversation-work')).toHaveLength(1)
    // The command answers: the live line becomes the work row.
    act(() => push(REAL[9]!, 10, new Date().toISOString()))
    act(() => push(REAL[10]!, 11, new Date().toISOString()))
    await waitFor(() =>
      expect(screen.queryByTestId('conversation-live')).toBeNull(),
    )
    expect(
      screen.getByTestId('conversation-work').querySelector('summary'),
    ).toHaveTextContent(/[Rr]an 1 command$/)
  })

  it('leaves Escape in the conversation to the side pane: it interrupts nothing there', async () => {
    const interrupt = vi
      .spyOn(agentOpsApi, 'interrupt')
      .mockResolvedValue(undefined as never)
    wrap()
    await screen.findByTestId('session-conversation')
    act(() => {
      for (let i = 0; i <= 8; i++)
        push(REAL[i]!, i + 1, new Date().toISOString())
    })
    const log = await screen.findByRole('log')
    log.focus()
    fireEvent.keyDown(log, { key: 'Escape' })
    expect(interrupt).not.toHaveBeenCalled()
    interrupt.mockRestore()
  })

  it('says no live line once the session has ended', async () => {
    wrap({ state: 'stopped' })
    await screen.findByTestId('session-conversation')
    act(() => {
      for (let i = 0; i <= 8; i++)
        push(REAL[i]!, i + 1, new Date().toISOString())
    })
    await screen.findByTestId('conversation-work')
    expect(screen.queryByTestId('conversation-live')).toBeNull()
  })
})

// A CODEX STREAM DOES NOT READ WORSE: the recorded follow stream of the CLI tests
// (protocol replies, two answers, two completed turns) and the real Codex 0.162.1 history
// keep their rows.
describe('SessionConversation — Codex keeps its rows', () => {
  // The CLI tests' recorded stream, read where it lives (one copy for both).
  const codexFollow = readFileSync(
    join(
      __dirname,
      '../../../../cmd/olivares/testdata/codex-follow-recorded.ndjson',
    ),
    'utf8',
  )

  it('reads the recorded app-server stream as two answers and two turn ends', async () => {
    wrap()
    await screen.findByTestId('session-conversation')
    act(() => {
      codexFollow
        .trim()
        .split('\n')
        .forEach((line, i) => push(line, i + 1))
    })
    await screen.findByText(/You asked: 'hello codex'/)
    expect(
      screen
        .getAllByTestId('conversation-item')
        .map((el) => el.getAttribute('data-kind')),
    ).toEqual(['assistant', 'result', 'assistant', 'result'])
  })

  it('folds a command between two answers, and keeps the stored real history', async () => {
    wrap()
    await screen.findByTestId('session-conversation')
    act(() => onHistoryRef.current?.(JSON.stringify(nativeHistory)))
    const item = (type: string, id: string, extra: object) =>
      JSON.stringify({
        method: 'item/completed',
        params: { turnId: 'turn-3', item: { type, id, ...extra } },
      })
    act(() => {
      push(
        item('commandExecution', 'c1', {
          command: 'go test ./...',
          exitCode: 0,
        }),
        1,
      )
      push(item('agentMessage', 'm1', { text: 'Tests pass.' }), 2)
    })
    expect(await screen.findByText('Tests pass.')).toBeInTheDocument()
    expect(screen.getByText('initial CLI prompt')).toBeInTheDocument()
    expect(
      screen.getByTestId('conversation-work').querySelector('summary'),
    ).toHaveTextContent(/^Ran 1 command$/)
  })
})

// the regression, the branches the real stream does not reach.
describe('SessionConversation — the work row in its other states', () => {
  const REAL = claudeStream.trim().split('\n')
  const use = (id: string, name: string, input: object) =>
    JSON.stringify({
      type: 'assistant',
      message: { content: [{ type: 'tool_use', id, name, input }] },
    })
  const answer = (id: string, isError = false) =>
    JSON.stringify({
      type: 'user',
      message: {
        content: [
          {
            type: 'tool_result',
            tool_use_id: id,
            content: 'ok',
            is_error: isError,
          },
        ],
      },
    })
  const summary = () =>
    screen.getByTestId('conversation-work').querySelector('summary')

  it('interrupts from the composer of its own pane, and only while a call runs', async () => {
    const interrupt = vi
      .spyOn(agentOpsApi, 'interrupt')
      .mockResolvedValue(undefined as never)
    const qc = new QueryClient()
    render(
      <QueryClientProvider client={qc}>
        <section data-focused="true">
          <SessionConversation run={run} />
          <textarea aria-label="composer" />
        </section>
        <section data-focused="false">
          <textarea aria-label="other pane" />
        </section>
      </QueryClientProvider>,
    )
    await screen.findByTestId('session-conversation')
    const composer = screen.getByRole('textbox', { name: 'composer' })
    // Nothing runs yet: Escape is the composer's own.
    fireEvent.keyDown(composer, { key: 'Escape' })
    act(() => push(use('t1', 'Bash', { command: 'make test' }), 1))
    expect(await screen.findByTestId('conversation-live')).toHaveTextContent(
      'Esc to interrupt',
    )
    // Another pane's field, and an Escape a control already took, interrupt nothing.
    fireEvent.keyDown(screen.getByRole('textbox', { name: 'other pane' }), {
      key: 'Escape',
    })
    composer.addEventListener('keydown', (e) => e.preventDefault(), {
      once: true,
    })
    fireEvent.keyDown(composer, { key: 'Escape' })
    expect(interrupt).not.toHaveBeenCalled()
    fireEvent.keyDown(composer, { key: 'Escape' })
    await waitFor(() => expect(interrupt).toHaveBeenCalledTimes(1))
    interrupt.mockRestore()
  })

  it('names files only when it knows them, and a tool it cannot class as a tool', async () => {
    wrap()
    await screen.findByTestId('session-conversation')
    act(() => {
      push(use('e1', 'Edit', {}), 1)
      push(answer('e1'), 2)
      push(use('m1', 'mcp__db__query', { sql: 'select 1' }), 3)
      push(answer('m1'), 4)
    })
    await waitFor(() =>
      expect(summary()).toHaveTextContent(/^Edited files, used 1 tool$/),
    )
  })

  it('counts a Codex command that exited non-zero as failed, never as run', async () => {
    wrap()
    await screen.findByTestId('session-conversation')
    act(() =>
      push(
        JSON.stringify({
          method: 'item/completed',
          params: {
            turnId: 't',
            item: {
              type: 'commandExecution',
              id: 'c1',
              command: 'go vet ./...',
              exitCode: 1,
            },
          },
        }),
        1,
      ),
    )
    await waitFor(() => expect(summary()).toHaveTextContent(/^1 failed$/))
    expect(
      screen.getByTestId('conversation-item').querySelector('summary'),
    ).toHaveTextContent('Failed')
  })

  it('shows no diff for an edit that failed', async () => {
    wrap()
    await screen.findByTestId('session-conversation')
    act(() => {
      push(
        use('e1', 'Edit', {
          file_path: 'a.ts',
          old_string: 'a',
          new_string: 'b',
        }),
        1,
      )
      push(answer('e1', true), 2)
    })
    await waitFor(() => expect(summary()).toHaveTextContent(/1 failed$/))
    expect(screen.queryByTestId('conversation-diff')).toBeNull()
    expect(screen.queryByText('+1')).toBeNull()
  })

  it('says a background command runs in the background until its task ends', async () => {
    wrap()
    await screen.findByTestId('session-conversation')
    // Up to the receipt of the background command (frames 11 to 14).
    act(() => {
      for (let i = 0; i <= 14; i++) push(REAL[i]!, i + 1)
    })
    const background = (await screen.findAllByTestId('conversation-item')).find(
      (el) =>
        el.getAttribute('data-kind') === 'tool' &&
        el.textContent?.includes('sleep(4)'),
    ) as HTMLElement
    expect(background.querySelector('summary')).toHaveTextContent(
      'In the background',
    )
    expect(background.textContent).not.toMatch(/running in background with ID/)
  })

  it('tells what a running edit or subagent does in the live line', async () => {
    wrap()
    await screen.findByTestId('session-conversation')
    act(() =>
      push(
        use('e1', 'Edit', {
          file_path: 'notes.txt',
          old_string: 'a',
          new_string: 'b',
        }),
        1,
      ),
    )
    expect(await screen.findByTestId('conversation-live')).toHaveTextContent(
      /^Editing notes\.txt/,
    )
    act(() => {
      push(answer('e1'), 2)
      push(use('a1', 'Agent', { description: 'Count entries', prompt: 'x' }), 3)
    })
    await waitFor(() =>
      expect(screen.getByTestId('conversation-live')).toHaveTextContent(
        /^Count entries is working/,
      ),
    )
  })
})

describe('SessionConversation — a stream that repeats a call id (the regression review)', () => {
  it('paints a call that names itself as its own parent once, and stops', async () => {
    wrap()
    await screen.findByTestId('session-conversation')
    const call = (parent?: string) =>
      JSON.stringify({
        type: 'assistant',
        ...(parent ? { parent_tool_use_id: parent } : {}),
        message: {
          content: [
            {
              type: 'tool_use',
              id: 'dup',
              name: 'Agent',
              input: { description: 'Loop' },
            },
          ],
        },
      })
    act(() => {
      push(call(), 1)
      push(call('dup'), 2)
    })
    await waitFor(() =>
      expect(
        screen.getAllByTestId('conversation-agent').length,
      ).toBeGreaterThan(0),
    )
    expect(
      screen.getAllByTestId('conversation-agent').length,
    ).toBeLessThanOrEqual(2)
  })
})

describe('SessionConversation — a work row always says something (the regression review)', () => {
  it('tells calls that only run in the background in words', async () => {
    wrap()
    await screen.findByTestId('session-conversation')
    const REAL = claudeStream.trim().split('\n')
    act(() => {
      for (let i = 0; i <= 14; i++) push(REAL[i]!, i + 1)
    })
    await waitFor(() =>
      expect(
        screen.getAllByTestId('conversation-work')[0]?.querySelector('summary'),
      ).toHaveTextContent(/1 in the background/),
    )
  })
})

describe('SessionConversation — it follows what grows at its end', () => {
  // The request card loads after the transcript; a following conversation must keep its
  // end in view, or Alt+A finds the request cut by the composer (measured at 1440 px).
  let resized: (() => void) | null = null
  beforeEach(() => {
    resized = null
    vi.stubGlobal(
      'ResizeObserver',
      class {
        cb: () => void
        constructor(cb: () => void) {
          this.cb = cb
          resized = cb
        }
        observe() {}
        unobserve() {}
        // A disconnected observer reports nothing more, as a browser's does.
        disconnect() {
          if (resized === this.cb) resized = null
        }
      },
    )
  })
  afterEach(() => vi.unstubAllGlobals())

  const grow = (log: HTMLElement, height: number) =>
    Object.defineProperty(log, 'scrollHeight', {
      value: height,
      configurable: true,
    })

  it('scrolls to the end when the content grows while it follows', () => {
    wrap()
    const log = screen.getByRole('log')
    grow(log, 1200)
    act(() => resized?.())
    expect(log.scrollTop).toBe(1200)
  })

  it('stays where the person scrolled when the content grows', () => {
    wrap()
    const log = screen.getByRole('log')
    grow(log, 1000)
    Object.defineProperty(log, 'clientHeight', {
      value: 300,
      configurable: true,
    })
    log.scrollTop = 100
    fireEvent.scroll(log)
    grow(log, 1400)
    act(() => resized?.())
    expect(log.scrollTop).toBe(100)
  })
})
