// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { Profiler } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import { LiveConsole } from './live-console'
import './i18n'
import type { RunDTO } from './types'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    can: (permission: string) => permission === 'sessions:run:write',
    principal: { user_id: 'user-a' },
    activeTenant: useTenantStore.getState().activeTenant,
  }),
}))

vi.mock('@/lib/api/client', () => ({
  http: {
    get: vi.fn(),
    post: vi.fn(),
    patch: vi.fn(),
    put: vi.fn(),
    delete: vi.fn(),
  },
}))

vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
}))

const enc = new TextEncoder()
const sse = (event: string, data: unknown) =>
  `event: ${event}\ndata: ${JSON.stringify(data)}\n\n`
const output = (seq: number, line = `fixture-line-${seq}`) =>
  sse('output', { seq, stream: 'stdout', line })

function streamFrom(...chunks: string[]): ReadableStream<Uint8Array> {
  return new ReadableStream({
    start(controller) {
      for (const c of chunks) controller.enqueue(enc.encode(c))
      controller.close()
    },
  })
}

const bodies: ReadableStreamDefaultController<Uint8Array>[] = []
function openBody(...chunks: string[]) {
  return new ReadableStream<Uint8Array>({
    start(controller) {
      bodies.push(controller)
      for (const chunk of chunks) controller.enqueue(enc.encode(chunk))
    },
  })
}
function eof(...chunks: string[]) {
  const b = openBody(...chunks)
  bodies.at(-1)!.close()
  return b
}
function ok(stream: ReadableStream<Uint8Array>) {
  return Promise.resolve({ ok: true, body: stream } as Response)
}

const baseRun: RunDTO = {
  run_ref: 'run_a',
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

function wrap(run: RunDTO, onCommit?: () => void) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  const tree = (r: RunDTO) => (
    <QueryClientProvider client={qc}>
      <Profiler id="attach-commit" onRender={() => onCommit?.()}>
        <LiveConsole run={r} />
      </Profiler>
    </QueryClientProvider>
  )
  const view = render(tree(run))
  return { ...view, run: (r: RunDTO) => view.rerender(tree(r)) }
}

beforeEach(() => {
  useSessionStore.setState({
    token: 'olvs_test',
    sessionId: 's1',
    expiresAt: '2099-01-01T00:00:00Z',
  })
  useTenantStore.setState({ activeTenant: 't1' })
})

afterEach(() => {
  for (const c of bodies.splice(0)) {
    try {
      c.close()
    } catch {
      /* already closed */
    }
  }
  vi.useRealTimers()
  vi.unstubAllGlobals()
  useSessionStore.setState({ token: null, sessionId: null, expiresAt: null })
})

describe('LiveConsole attach continuity', () => {
  it('shows typed I/O-absence without marking the process ended and retries', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    let n = 0
    const fetchMock = vi.fn().mockImplementation(() => {
      n += 1
      if (n === 1) {
        return ok(
          streamFrom(
            sse('notice', {
              type: 'notice',
              state: 'running',
              detail: 'session is not live on this node; no bridged I/O stream',
              io_unavailable: 'not_live_on_node',
            }),
          ),
        )
      }
      return new Promise(() => {})
    })
    vi.stubGlobal('fetch', fetchMock)

    wrap(baseRun)
    expect(
      await screen.findByText('No bridged I/O on this node.'),
    ).toBeInTheDocument()
    expect(
      screen.queryByText('The session I/O stream ended.'),
    ).not.toBeInTheDocument()
    expect(screen.queryByText(/has not ended/i)).not.toBeInTheDocument()
    expect(fetchMock).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(30_000)
    expect(fetchMock).toHaveBeenCalledTimes(1)

    fireEvent.click(screen.getByRole('button', { name: 'Retry attach' }))
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2))
  })

  it('F1 state change on the same run preserves each displayed output exactly once', async () => {
    const urls: string[] = []
    const fetchMock = vi.fn((url: string) => {
      urls.push(url)
      const from = Number(
        new URL(url, 'http://fixture').searchParams.get('from'),
      )
      return ok(
        openBody(
          ...[1, 2].filter((seq) => seq >= from).map((seq) => output(seq)),
        ),
      )
    })
    vi.stubGlobal('fetch', fetchMock)
    const view = wrap(baseRun)
    await screen.findByText('fixture-line-2')
    view.run({ ...baseRun, state: 'idle' })
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2))
    expect(urls[0]).toContain('from=0')
    expect(urls[1]).toContain('from=3')
    expect(screen.getAllByText('fixture-line-1')).toHaveLength(1)
    expect(screen.getAllByText('fixture-line-2')).toHaveLength(1)
  })

  it('F1 retry after output and a subsequent unavailable attempt keeps the cursor', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const urls: string[] = []
    let request = 0
    const fetchMock = vi.fn((url: string) => {
      urls.push(url)
      request += 1
      if (request === 1) return ok(streamFrom(output(1)))
      if (request === 2)
        return ok(
          streamFrom(
            sse('notice', {
              type: 'notice',
              io_unavailable: 'not_live_on_node',
            }),
          ),
        )
      const from = Number(
        new URL(url, 'http://fixture').searchParams.get('from'),
      )
      return ok(
        openBody(
          ...[1, 2].filter((seq) => seq >= from).map((seq) => output(seq)),
        ),
      )
    })
    vi.stubGlobal('fetch', fetchMock)
    wrap(baseRun)
    await screen.findByText('fixture-line-1')
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1000)
    })
    const retry = await screen.findByRole('button', { name: 'Retry attach' })
    fireEvent.click(retry)
    await screen.findByText('fixture-line-2')
    expect(urls[0]).toContain('from=0')
    expect(urls[1]).toContain('from=2')
    expect(urls[2]).toContain('from=2')
    expect(screen.getAllByText('fixture-line-1')).toHaveLength(1)
  })

  it('F1 a state change after retention eviction reports dropped=7 next_seq=10', async () => {
    const urls: string[] = []
    let request = 0
    const fetchMock = vi.fn((url: string) => {
      urls.push(url)
      request += 1
      if (request === 1) return ok(openBody(output(1), output(2)))
      const from = Number(
        new URL(url, 'http://fixture').searchParams.get('from'),
      )
      const gap =
        from >= 1 && from < 10
          ? [sse('lag', { type: 'lag', dropped: 10 - from, next_seq: 10 })]
          : []
      return ok(openBody(...gap, output(10)))
    })
    vi.stubGlobal('fetch', fetchMock)
    const view = wrap(baseRun)
    await screen.findByText('fixture-line-2')
    view.run({ ...baseRun, state: 'idle' })
    await screen.findByText('fixture-line-10')
    expect(urls[1]).toContain('from=3')
    expect(screen.getByText(/7 frame\(s\) dropped/)).toBeInTheDocument()
    expect(screen.getAllByText('fixture-line-1')).toHaveLength(1)
    expect(screen.getAllByText('fixture-line-2')).toHaveLength(1)
  })

  it.each(['tenant', 'credential', 'run'] as const)(
    'F2 first committed %s snapshot excludes prior transcript, lag and status',
    async (identity) => {
      vi.stubGlobal(
        'fetch',
        vi.fn(() => ok(openBody())),
      )
      const commits: string[] = []
      let capture = false
      const view = wrap(baseRun, () => {
        if (capture) commits.push(document.body.textContent ?? '')
      })
      await waitFor(() => expect(bodies).toHaveLength(1))
      await act(async () => {
        bodies[0].enqueue(
          enc.encode(
            output(1, 'previous-authority-output') +
              sse('lag', { type: 'lag', dropped: 7, next_seq: 10 }) +
              sse('notice', {
                type: 'notice',
                io_unavailable: 'not_live_on_node',
              }),
          ),
        )
        bodies[0].close()
      })
      expect(
        await screen.findByText('previous-authority-output'),
      ).toBeInTheDocument()
      expect(screen.getByText(/7 frame\(s\) dropped/)).toBeInTheDocument()
      expect(
        await screen.findByRole('button', { name: 'Retry attach' }),
      ).toBeInTheDocument()
      capture = true
      await act(async () => {
        if (identity === 'tenant') {
          useTenantStore.setState({ activeTenant: 't2' })
        }
        if (identity === 'credential') {
          // Production advances credentialGeneration only through setSession
          // (login, refresh, 401 clear). Patching `token` on the store would
          // leave the opaque remount key unchanged and leak the prior
          // transcript — the defect this case exists to catch.
          useSessionStore.getState().setSession({
            token: 'olvs_other',
            sessionId: 's1',
            expiresAt: '2099-01-01T00:00:00Z',
          })
        }
        if (identity === 'run') {
          view.run({ ...baseRun, run_ref: 'run_b' })
        }
      })
      expect(
        screen.queryByText('previous-authority-output'),
      ).not.toBeInTheDocument()
      expect(screen.queryByText(/7 frame\(s\) dropped/)).not.toBeInTheDocument()
      expect(
        commits.some((text) => text.includes('previous-authority-output')),
      ).toBe(false)
      expect(commits.some((text) => /7 frame\(s\) dropped/.test(text))).toBe(
        false,
      )
      expect(
        commits.some((text) => text.includes('The session I/O stream ended.')),
      ).toBe(false)
      expect(
        commits.some((text) => text.includes('No bridged I/O on this node.')),
      ).toBe(false)
    },
  )

  it('F3 stopped session without retained I/O must not claim it has not ended', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(() =>
        ok(
          eof(
            sse('notice', {
              type: 'notice',
              state: 'stopped',
              io_unavailable: 'not_live_on_node',
            }),
          ),
        ),
      ),
    )
    wrap({ ...baseRun, state: 'stopped' })
    await screen.findByRole('button', { name: 'Retry attach' })
    expect(screen.queryByText(/has not ended/i)).not.toBeInTheDocument()
    expect(screen.getByText('No bridged I/O on this node.')).toBeInTheDocument()
    expect(
      screen.getByText(/This session is not live \(it is Stopped\)/),
    ).toBeInTheDocument()
    expect(
      screen.queryByText('The session I/O stream ended.'),
    ).not.toBeInTheDocument()
  })

  it('drops transcript and cursor when the run identity changes', async () => {
    const fetchMock = vi.fn().mockImplementation(() => ok(openBody()))
    vi.stubGlobal('fetch', fetchMock)

    const { rerender } = wrap(baseRun)
    await waitFor(() => expect(bodies).toHaveLength(1))
    await act(async () => {
      bodies[0].enqueue(
        enc.encode(
          sse('output', { seq: 1, stream: 'stdout', line: 'alpha-line' }),
        ),
      )
    })
    expect(await screen.findByText('alpha-line')).toBeInTheDocument()

    const qc = new QueryClient({
      defaultOptions: {
        queries: { retry: false },
        mutations: { retry: false },
      },
    })
    rerender(
      <QueryClientProvider client={qc}>
        <LiveConsole run={{ ...baseRun, run_ref: 'run_b' }} />
      </QueryClientProvider>,
    )
    await waitFor(() =>
      expect(screen.queryByText('alpha-line')).not.toBeInTheDocument(),
    )
    await waitFor(() => expect(bodies.length).toBeGreaterThan(1))
    try {
      bodies[0].enqueue(
        enc.encode(
          sse('output', { seq: 2, stream: 'stdout', line: 'late-alpha' }),
        ),
      )
    } catch {
      /* aborted */
    }
    await act(async () => {
      bodies[bodies.length - 1].enqueue(
        enc.encode(
          sse('output', { seq: 1, stream: 'stdout', line: 'beta-line' }),
        ),
      )
    })
    expect(await screen.findByText('beta-line')).toBeInTheDocument()
    expect(screen.queryByText('alpha-line')).not.toBeInTheDocument()
    expect(screen.queryByText('late-alpha')).not.toBeInTheDocument()
    const last = String(fetchMock.mock.calls.at(-1)?.[0])
    expect(last).toContain('/runs/run_b/attach')
    expect(last).toContain('from=0')
  })
})
