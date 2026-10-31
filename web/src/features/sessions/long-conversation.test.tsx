// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { RunDTO } from '@/features/agentops/types'
import { SessionConversation } from './session-conversation'
import { openOnWords } from './conversation-work'
import './i18n'

// Every conversation row reads its click handler from openOnWords while it renders, so the
// calls count the rows painted.
vi.mock('./conversation-work', async (actual) => {
  const real = await actual<typeof import('./conversation-work')>()
  return { ...real, openOnWords: vi.fn(real.openOnWords) }
})

type Frame = { seq: number; stream: string; line: string }

const onFrameRef = vi.hoisted(() => ({
  current: null as ((f: Frame) => void) | null,
  tail: undefined as number | undefined,
  onPage: undefined as ((earlier: boolean) => void) | undefined,
}))
const readAttachPage = vi.hoisted(() => vi.fn())

vi.mock('@/features/agentops/attach', () => ({
  useRunAttach: (opts: {
    onFrame: (f: Frame) => void
    tail?: number
    onPage?: (earlier: boolean) => void
  }) => {
    onFrameRef.current = opts.onFrame
    onFrameRef.tail = opts.tail
    onFrameRef.onPage = opts.onPage
    return {
      status: 'open',
      ended: false,
      ioUnavailable: null,
      retry: () => {},
    }
  },
  readAttachPage,
}))

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    can: () => true,
    activeTenant: 'demo',
    principal: { kind: 'user', actor: 'alice', user_id: 'alice' },
  }),
}))

const run = {
  run_ref: 'run_long',
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
} as RunDTO

function frame(i: number): string {
  return i % 2 === 0
    ? JSON.stringify({
        type: 'user',
        message: {
          role: 'user',
          content: [{ type: 'text', text: `question ${i}` }],
        },
      })
    : JSON.stringify({
        type: 'assistant',
        message: {
          id: `msg_${i}`,
          role: 'assistant',
          content: [{ type: 'text', text: `answer ${i}` }],
        },
      })
}

function push(i: number) {
  onFrameRef.current?.({ seq: i + 1, stream: 'stdout', line: frame(i) })
}

afterEach(() => cleanup())

describe('a long conversation', () => {
  it('paints only the new row when one more frame arrives', () => {
    render(
      <QueryClientProvider client={new QueryClient()}>
        <SessionConversation run={run} composerId="composer" />
      </QueryClientProvider>,
    )
    act(() => {
      for (let i = 0; i < 400; i++) push(i)
    })
    expect(screen.getByText('answer 399')).toBeInTheDocument()
    vi.mocked(openOnWords).mockClear()
    act(() => push(400))
    expect(screen.getByText('question 400')).toBeInTheDocument()
    expect(screen.getByText('answer 399')).toBeInTheDocument()
    // The new row, never the 400 already on the page.
    expect(vi.mocked(openOnWords).mock.calls.length).toBeLessThanOrEqual(2)
  })

  it('keeps the page to a bounded number of rows with 2000 frames', () => {
    const view = render(
      <QueryClientProvider client={new QueryClient()}>
        <SessionConversation run={run} composerId="composer" />
      </QueryClientProvider>,
    )
    act(() => {
      for (let i = 0; i < 2000; i++) push(i)
    })
    const rows = view.container.querySelectorAll(
      '[data-testid="session-conversation"] li',
    )
    expect(rows.length).toBeGreaterThan(0)
    expect(rows.length).toBeLessThanOrEqual(120)
  })

  it('opens on the last frames and reads earlier ones a page at a time', async () => {
    render(
      <QueryClientProvider client={new QueryClient()}>
        <SessionConversation run={run} composerId="composer" />
      </QueryClientProvider>,
    )
    // The attach opens on a tail; the engine says earlier frames are buffered.
    expect(onFrameRef.tail).toBeGreaterThan(0)
    act(() => {
      for (let i = 990; i < 1000; i++) push(i)
      onFrameRef.onPage?.(true)
    })
    expect(screen.queryByText('question 980')).toBeNull()
    readAttachPage.mockResolvedValueOnce({
      frames: Array.from({ length: 10 }, (_, k) => ({
        seq: 981 + k,
        stream: 'stdout',
        line: frame(980 + k),
      })),
      earlier: true,
    })
    fireEvent.click(screen.getByRole('button', { name: 'Show earlier output' }))
    expect(await screen.findByText('question 980')).toBeInTheDocument()
    expect(screen.getByText('answer 999')).toBeInTheDocument()
    expect(readAttachPage).toHaveBeenLastCalledWith(
      expect.objectContaining({ runRef: 'run_long', before: 991 }),
    )
    const from = readAttachPage.mock.lastCall![0].from as number
    expect(from).toBeGreaterThanOrEqual(1)
    expect(from).toBeLessThan(991)

    // The first frames: nothing is left before them, and the button goes.
    readAttachPage.mockResolvedValueOnce({ frames: [], earlier: false })
    fireEvent.click(screen.getByRole('button', { name: 'Show earlier output' }))
    await waitFor(() =>
      expect(
        screen.queryByRole('button', { name: 'Show earlier output' }),
      ).toBeNull(),
    )
    expect(readAttachPage).toHaveBeenLastCalledWith(
      expect.objectContaining({ before: 981 }),
    )
  })
})
