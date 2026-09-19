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
