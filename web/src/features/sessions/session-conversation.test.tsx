// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  act,
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { RunDTO } from '@/features/agentops/types'
import { SessionConversation } from './session-conversation'
import { sentTurnsPartition, useSentTurns } from './sent-turns'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import nativeHistory from './fixtures/codex-native-history.json'
import './i18n'

const attachStatus = vi.hoisted(() => ({
  value: 'open' as 'open' | 'connecting' | 'error' | 'closed',
}))
const onFrameRef = vi.hoisted(() => ({
  current: null as
    ((f: { seq: number; stream: string; line: string }) => void) | null,
}))
const onHistoryRef = vi.hoisted(() => ({
  current: null as ((line: string) => void) | null,
}))

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
  useAuth: () => ({ can: () => true, activeTenant: 'demo' }),
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

function wrap(overrides: Partial<RunDTO> = {}) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  return render(
    <QueryClientProvider client={qc}>
      <SessionConversation run={{ ...run, ...overrides }} />
    </QueryClientProvider>,
  )
}

function push(line: string, seq = 1) {
  onFrameRef.current?.({ seq, stream: 'stdout', line })
}

beforeEach(() => {
  attachStatus.value = 'open'
  onFrameRef.current = null
  useSentTurns.setState({ byRun: {} })
})

describe('SessionConversation', () => {
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
    expect(screen.getAllByText(/file_path/).length).toBeGreaterThan(0)
  })

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

  // HU 025 (real Claude Code 2.1.287): a long Bash turn printed "Unrecognised frame"
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

  // SR2C on 0df0c254: a cancelled ACP turn showed only "Result".
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

  // HU2-12: a failed Codex turn read as "Result · Turn completed".
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
    expect(line.closest('button')).toHaveClass('text-danger')
    expect(screen.queryByText('Result')).toBeNull()
  })

  // HU2-13: a refused API key read as "api retry", ten times.
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
      'The provider refused the API key. Replace it under API keys.',
    )
    expect(line.closest('button')).toHaveClass('text-danger')
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

  // HU2-23: 18 "Unrecognised frame" rows for one stderr dump; the failure read as quiet.
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
    expect(failure.closest('button')).toHaveClass('text-danger')
  })

  // HU2-27: one raw row with "\u001b[2m…\u001b[31mERROR\u001b[0m" in it.
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

  // HU2-27: Codex never started the turn and the person's "Say hi." was nowhere.
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
    expect(
      screen.getByText(
        'Sent when the launch is approved and the session starts.',
      ),
    ).toHaveClass('text-muted-foreground')
    act(() => {
      useSentTurns
        .getState()
        .note(sentTurnsPartition(), 'run_1', { text: 'Say hi.' })
    })
    expect(screen.getAllByTestId('conversation-sent')).toHaveLength(1)
    expect(
      screen.queryByText(
        'Sent when the launch is approved and the session starts.',
      ),
    ).toBeNull()
  })

  // SR4C on 3cf9f18a: a sign-out leaves no person's words behind.
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
    const row = notice.closest('button')!
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

  it('folds the older tool calls of a long run under "+N tool calls", and keeps them one click away', async () => {
    const userEv = userEvent.setup()
    wrap()
    await screen.findByTestId('session-conversation')
    act(() => {
      for (let i = 1; i <= 5; i++)
        push(toolUse(`t${i}`, 'Read', { file_path: `f${i}.py` }), i)
    })
    const fold = await screen.findByTestId('conversation-tools-fold')
    expect(fold).toHaveTextContent('+2 tool calls')
    expect(fold.hasAttribute('open')).toBe(false)
    // The latest three are on the page; the first two are inside the fold.
    expect(within(fold).getAllByTestId('conversation-item')).toHaveLength(2)
    expect(item('tool')).toHaveLength(5)
    await userEv.click(fold.querySelector('summary') as HTMLElement)
    expect(fold.hasAttribute('open')).toBe(true)
  })

  it('does not fold three tool calls or fewer', async () => {
    wrap()
    await screen.findByTestId('session-conversation')
    act(() => {
      for (let i = 1; i <= 3; i++)
        push(toolUse(`t${i}`, 'Read', { file_path: `f${i}.py` }), i)
    })
    await waitFor(() => expect(item('tool')).toHaveLength(3))
    expect(screen.queryByTestId('conversation-tools-fold')).toBeNull()
  })

  it('ends a turn with a centered "Worked for" divider that opens the same content', async () => {
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
    expect(summary).toHaveTextContent('Worked for 8.0s')
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
    expect(divider.querySelector('summary')).toHaveTextContent('Result · 8.0s')
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
  expect(screen.queryByText('No turns yet. Send a sentence below.')).toBeNull()
})
