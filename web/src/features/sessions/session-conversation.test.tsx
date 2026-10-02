// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { RunDTO } from '@/features/agentops/types'
import { SessionConversation } from './session-conversation'
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
})
