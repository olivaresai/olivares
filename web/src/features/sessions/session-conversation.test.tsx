// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { RunDTO } from '@/features/agentops/types'
import { SessionConversation } from './session-conversation'
import { sentTurnsPartition, useSentTurns } from './sent-turns'
import { useSessionStore } from '@/stores/session'
import './i18n'

const onFrameRef = vi.hoisted(() => ({
  current: null as
    ((f: { seq: number; stream: string; line: string }) => void) | null,
}))

vi.mock('@/features/agentops/attach', () => ({
  useRunAttach: (opts: {
    onFrame: (f: { seq: number; stream: string; line: string }) => void
  }) => {
    onFrameRef.current = opts.onFrame
    return {
      status: 'open',
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
  transport: 'stream-json',
  permission_mode: 'default',
  isolation: 'native',
  state: 'running',
  last_event_seq: 0,
  pep_provisioned: true,
  record_io: true,
  critical: false,
}

function wrap(workspaceRef?: string) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  return render(
    <QueryClientProvider client={qc}>
      <SessionConversation run={run} workspaceRef={workspaceRef} />
    </QueryClientProvider>,
  )
}

function push(line: string, seq = 1) {
  onFrameRef.current?.({ seq, stream: 'stdout', line })
}

beforeEach(() => {
  onFrameRef.current = null
  useSentTurns.setState({ byRun: {} })
})

describe('SessionConversation', () => {
  it('renders an assistant message and a collapsed tool row, never the raw wire', async () => {
    wrap('ws-1')
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
    wrap('ws-1')
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
    wrap('ws-1')
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

  it('warns when there is no workspace and the init frame names a cwd', async () => {
    wrap(undefined)
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
    expect(
      await screen.findByTestId('conversation-cwd-warning'),
    ).toHaveTextContent('/engine/dir')
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
        <SessionConversation
          run={run}
          onInspect={onInspect}
          workspaceRef="ws"
        />
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
    wrap('ws-1')
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
    wrap('ws-1')
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
    wrap('ws-1')
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
    wrap('ws-1')
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
    wrap('ws-1')
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
    wrap('ws-1')
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
    wrap('ws-1')
    expect(await screen.findByText('Say hi.')).toBeInTheDocument()
    const reason = screen.getByText(
      'Not sent: the provider reports that this profile is not authenticated',
    )
    expect(reason).toHaveClass('text-danger')
    expect(screen.queryByText('Waiting for the session…')).toBeNull()
  })

  // SR4C on 3cf9f18a: a sign-out leaves no person's words behind.
  it('forgets the first message when the sign-in changes', async () => {
    useSentTurns
      .getState()
      .note(sentTurnsPartition(), 'run_1', { text: 'Say hi.' })
    wrap('ws-1')
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
    wrap('ws-1')
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
    wrap('ws-1')
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
})
