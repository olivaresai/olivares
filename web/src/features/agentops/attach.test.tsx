// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { act, renderHook, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import { useRunAttach } from './attach'
import {
  isAttachIOUnavailable,
  type AttachFrame,
  type AttachLag,
  type AttachNotice,
} from './types'

/** One-shot ReadableStream of UTF-8 bytes from string chunks. */
function streamFrom(...chunks: string[]): ReadableStream<Uint8Array> {
  const enc = new TextEncoder()
  return new ReadableStream({
    start(controller) {
      for (const c of chunks) controller.enqueue(enc.encode(c))
      controller.close()
    },
  })
}

const sse = (event: string, data: unknown) =>
  `event: ${event}\ndata: ${JSON.stringify(data)}\n\n`

beforeEach(() => {
  useSessionStore.setState({
    token: 'olvs_test',
    sessionId: 's1',
    expiresAt: '2099-01-01T00:00:00Z',
  })
  useTenantStore.setState({ activeTenant: 't1' })
})

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
  useSessionStore.setState({ token: null, sessionId: null, expiresAt: null })
})

describe('useRunAttach', () => {
  it('delivers output frames in order, dedupes seq replays, resyncs past a lag, and ends', async () => {
    const body = streamFrom(
      sse('output', { seq: 0, stream: 'stdout', line: 'a' }),
      sse('output', { seq: 1, stream: 'stdout', line: 'b' }),
      sse('lag', { type: 'lag', dropped: 3, next_seq: 5 }),
      sse('output', { seq: 5, stream: 'stdout', line: 'c' }),
      // A duplicate of seq 5 (a replay boundary) MUST be dropped, not re-delivered.
      sse('output', { seq: 5, stream: 'stdout', line: 'c-dup' }),
      sse('end', { type: 'end' }),
    )
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, body } as Response)
    vi.stubGlobal('fetch', fetchMock)

    const frames: AttachFrame[] = []
    const lags: AttachLag[] = []
    const { result } = renderHook(() =>
      useRunAttach({
        runRef: 'run-x',
        onFrame: (f) => frames.push(f),
        onLag: (l) => lags.push(l),
      }),
    )

    await waitFor(() => expect(result.current.ended).toBe(true))

    // The first connect requests from the start of the ring.
    expect(fetchMock).toHaveBeenCalledWith(
      expect.stringContaining('/v1/m/sessions/runs/run-x/attach?from=0'),
      expect.objectContaining({ method: 'GET' }),
    )
    // Exactly the three distinct frames, in order — the seq-5 duplicate was deduped.
    expect(frames.map((f) => f.line)).toEqual(['a', 'b', 'c'])
    // The lag sentinel surfaced honestly.
    expect(lags).toEqual([{ type: 'lag', dropped: 3, next_seq: 5 }])
  })

  it('stays closed without a token (deny-closed: no anonymous attach)', async () => {
    useSessionStore.setState({ token: null, sessionId: null, expiresAt: null })
    const fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
    const { result } = renderHook(() =>
      useRunAttach({ runRef: 'run-x', onFrame: () => {} }),
    )
    expect(result.current.status).toBe('closed')
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('stops on typed I/O-absence without ending or retrying', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const body = streamFrom(
      sse('notice', {
        type: 'notice',
        state: 'running',
        detail: 'session is not live on this node; no bridged I/O stream',
        io_unavailable: 'not_live_on_node',
      }),
    )
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, body } as Response)
    vi.stubGlobal('fetch', fetchMock)

    const notices: AttachNotice[] = []
    const ends: number[] = []
    const { result } = renderHook(() =>
      useRunAttach({
        runRef: 'run-x',
        onFrame: () => {},
        onNotice: (n) => notices.push(n),
        onEnd: () => ends.push(1),
      }),
    )

    await waitFor(() =>
      expect(result.current.ioUnavailable).toBe('not_live_on_node'),
    )
    expect(result.current.ended).toBe(false)
    expect(ends).toEqual([])
    expect(notices).toHaveLength(1)
    const calls = fetchMock.mock.calls.length
    expect(calls).toBe(1)
    await vi.advanceTimersByTimeAsync(30_000)
    expect(fetchMock).toHaveBeenCalledTimes(calls)
  })

  it('stops on remote-control classification without ending', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const body = streamFrom(
      sse('notice', {
        type: 'notice',
        state: 'running',
        detail:
          'remote-control: I/O is relayed to Anthropic cloud, not bridged',
        io_unavailable: 'remote_control',
      }),
    )
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, body } as Response)
    vi.stubGlobal('fetch', fetchMock)
    const ends: number[] = []
    const { result } = renderHook(() =>
      useRunAttach({
        runRef: 'run-x',
        onFrame: () => {},
        onEnd: () => ends.push(1),
      }),
    )
    await waitFor(() =>
      expect(result.current.ioUnavailable).toBe('remote_control'),
    )
    expect(result.current.ended).toBe(false)
    expect(ends).toEqual([])
    await vi.advanceTimersByTimeAsync(30_000)
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it('does not treat a legacy notice without classification as terminal', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    let n = 0
    const fetchMock = vi.fn().mockImplementation(() => {
      n += 1
      if (n === 1) {
        return Promise.resolve({
          ok: true,
          body: streamFrom(
            sse('notice', {
              type: 'notice',
              state: 'running',
              detail: 'session is not live on this node; no bridged I/O stream',
            }),
          ),
        } as Response)
      }
      return new Promise(() => {})
    })
    vi.stubGlobal('fetch', fetchMock)
    const ends: number[] = []
    const { result } = renderHook(() =>
      useRunAttach({
        runRef: 'run-x',
        onFrame: () => {},
        onEnd: () => ends.push(1),
      }),
    )
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))
    await vi.advanceTimersByTimeAsync(1_000)
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2))
    expect(result.current.ended).toBe(false)
    expect(result.current.ioUnavailable).toBeNull()
    expect(ends).toEqual([])
  })

  it('reconnects with the cursor after a transport close without a sentinel', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    let n = 0
    const fetchMock = vi.fn().mockImplementation(() => {
      n += 1
      if (n === 1) {
        return Promise.resolve({
          ok: true,
          body: streamFrom(
            sse('output', { seq: 1, stream: 'stdout', line: 'a' }),
          ),
        } as Response)
      }
      return new Promise(() => {})
    })
    vi.stubGlobal('fetch', fetchMock)
    const frames: AttachFrame[] = []
    renderHook(() =>
      useRunAttach({
        runRef: 'run-x',
        onFrame: (f) => frames.push(f),
      }),
    )
    await waitFor(() => expect(frames.map((f) => f.line)).toEqual(['a']))
    await vi.advanceTimersByTimeAsync(1_000)
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2))
    expect(String(fetchMock.mock.calls[0][0])).toContain('from=0')
    expect(String(fetchMock.mock.calls[1][0])).toContain('from=2')
  })

  it('keeps the replay cursor across a same-identity sessionKey restart', async () => {
    const urls: string[] = []
    const fetchMock = vi.fn().mockImplementation((url: string) => {
      urls.push(String(url))
      const from = Number(
        new URL(String(url), 'http://fixture').searchParams.get('from'),
      )
      const frames = [1, 2].filter((seq) => seq >= from)
      const body = new ReadableStream<Uint8Array>({
        start(controller) {
          const encoder = new TextEncoder()
          for (const seq of frames) {
            controller.enqueue(
              encoder.encode(
                sse('output', { seq, stream: 'stdout', line: `line-${seq}` }),
              ),
            )
          }
        },
      })
      return Promise.resolve({ ok: true, body } as Response)
    })
    vi.stubGlobal('fetch', fetchMock)
    const seen: string[] = []
    const { rerender } = renderHook(
      ({ sessionKey }: { sessionKey: string }) =>
        useRunAttach({
          runRef: 'run-x',
          sessionKey,
          onFrame: (f) => seen.push(f.line),
        }),
      { initialProps: { sessionKey: 'running' } },
    )
    await waitFor(() => expect(seen).toEqual(['line-1', 'line-2']))
    rerender({ sessionKey: 'idle' })
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2))
    expect(urls[0]).toContain('from=0')
    expect(urls[1]).toContain('from=3')
    expect(seen).toEqual(['line-1', 'line-2'])
  })

  it('keeps the replay cursor across explicit retry after a typed notice', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const urls: string[] = []
    let n = 0
    const fetchMock = vi.fn().mockImplementation((url: string) => {
      urls.push(String(url))
      n += 1
      if (n === 1) {
        return Promise.resolve({
          ok: true,
          body: streamFrom(
            sse('output', { seq: 1, stream: 'stdout', line: 'a' }),
          ),
        } as Response)
      }
      if (n === 2) {
        return Promise.resolve({
          ok: true,
          body: streamFrom(
            sse('notice', {
              type: 'notice',
              io_unavailable: 'not_live_on_node',
            }),
          ),
        } as Response)
      }
      return new Promise(() => {})
    })
    vi.stubGlobal('fetch', fetchMock)
    const { result } = renderHook(() =>
      useRunAttach({ runRef: 'run-x', onFrame: () => {} }),
    )
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))
    await vi.advanceTimersByTimeAsync(1_000)
    await waitFor(() =>
      expect(result.current.ioUnavailable).toBe('not_live_on_node'),
    )
    act(() => {
      result.current.retry()
    })
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(3))
    expect(urls[0]).toContain('from=0')
    expect(urls[1]).toContain('from=2')
    expect(urls[2]).toContain('from=2')
  })

  it('does not treat an unknown io_unavailable classification as terminal', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const fetchMock = vi
      .fn()
      .mockImplementationOnce(() =>
        Promise.resolve({
          ok: true,
          body: streamFrom(
            sse('notice', {
              type: 'notice',
              io_unavailable: 'future-classification',
            }),
          ),
        } as Response),
      )
      .mockImplementation(() => new Promise(() => {}))
    vi.stubGlobal('fetch', fetchMock)
    const { result } = renderHook(() =>
      useRunAttach({ runRef: 'run-x', onFrame: () => {} }),
    )
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))
    await vi.advanceTimersByTimeAsync(1_000)
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2))
    expect(result.current.ended).toBe(false)
    expect(result.current.ioUnavailable).toBeNull()
  })

  it('does not inherit cursor, transcript, or late callbacks across identity', async () => {
    const enc = new TextEncoder()
    const controllers: ReadableStreamDefaultController<Uint8Array>[] = []
    const fetchMock = vi.fn().mockImplementation(() => {
      const body = new ReadableStream<Uint8Array>({
        start(controller) {
          controllers.push(controller)
        },
      })
      return Promise.resolve({ ok: true, body } as Response)
    })
    vi.stubGlobal('fetch', fetchMock)

    const framesA: string[] = []
    const framesB: string[] = []
    const { rerender } = renderHook(
      ({
        runRef,
        onFrame,
      }: {
        runRef: string
        onFrame: (f: AttachFrame) => void
      }) => useRunAttach({ runRef, onFrame }),
      {
        initialProps: {
          runRef: 'run-a',
          onFrame: (f: AttachFrame) => framesA.push(f.line),
        },
      },
    )

    await waitFor(() => expect(controllers.length).toBe(1))
    controllers[0].enqueue(
      enc.encode(sse('output', { seq: 4, stream: 'stdout', line: 'from-a' })),
    )
    await waitFor(() => expect(framesA).toEqual(['from-a']))

    rerender({
      runRef: 'run-b',
      onFrame: (f: AttachFrame) => framesB.push(f.line),
    })
    await waitFor(() => expect(fetchMock.mock.calls.length).toBeGreaterThan(1))
    const lastUrl = String(fetchMock.mock.calls.at(-1)?.[0])
    expect(lastUrl).toContain('/runs/run-b/attach')
    expect(lastUrl).toContain('from=0')

    try {
      controllers[0].enqueue(
        enc.encode(sse('output', { seq: 5, stream: 'stdout', line: 'late-a' })),
      )
    } catch {
      /* aborted streams may reject a late enqueue */
    }
    await waitFor(() => expect(controllers.length).toBeGreaterThan(1))
    controllers[controllers.length - 1].enqueue(
      enc.encode(sse('output', { seq: 1, stream: 'stdout', line: 'from-b' })),
    )
    await waitFor(() => expect(framesB).toEqual(['from-b']))
    expect(framesA).toEqual(['from-a'])
    expect(framesB).not.toContain('from-a')
    expect(framesB).not.toContain('late-a')
  })

  it.each(['tenant', 'credential'] as const)(
    'drops late output, lag, notice and end after %s identity cleanup',
    async (identity) => {
      const controllers: ReadableStreamDefaultController<Uint8Array>[] = []
      const fetchMock = vi.fn().mockImplementation(() => {
        const body = new ReadableStream<Uint8Array>({
          start(controller) {
            controllers.push(controller)
          },
        })
        return Promise.resolve({ ok: true, body } as Response)
      })
      vi.stubGlobal('fetch', fetchMock)
      const deliveries: unknown[] = []
      const { result } = renderHook(() =>
        useRunAttach({
          runRef: 'run-x',
          onFrame: (f) => deliveries.push(f),
          onLag: (l) => deliveries.push(l),
          onNotice: (n) => deliveries.push(n),
          onEnd: () => deliveries.push('end'),
        }),
      )
      await waitFor(() => expect(controllers).toHaveLength(1))
      act(() => {
        if (identity === 'tenant') {
          useTenantStore.setState({ activeTenant: 't2' })
        } else {
          useSessionStore.setState({ token: 'olvs_other' })
        }
      })
      await waitFor(() => expect(controllers).toHaveLength(2))
      const encoder = new TextEncoder()
      try {
        controllers[0].enqueue(
          encoder.encode(
            sse('output', { seq: 1, stream: 'stdout', line: 'late' }) +
              sse('lag', { type: 'lag', dropped: 5, next_seq: 9 }) +
              sse('notice', {
                type: 'notice',
                io_unavailable: 'not_live_on_node',
              }) +
              sse('end', { type: 'end' }),
          ),
        )
      } catch {
        /* aborted */
      }
      expect(deliveries).toEqual([])
      expect(result.current.ended).toBe(false)
      expect(result.current.ioUnavailable).toBeNull()
    },
  )
})

describe('isAttachIOUnavailable', () => {
  it('accepts only the two stable classifications', () => {
    expect(isAttachIOUnavailable('not_live_on_node')).toBe(true)
    expect(isAttachIOUnavailable('remote_control')).toBe(true)
    expect(
      isAttachIOUnavailable(
        'session is not live on this node; no bridged I/O stream',
      ),
    ).toBe(false)
    expect(isAttachIOUnavailable(undefined)).toBe(false)
    expect(isAttachIOUnavailable('other')).toBe(false)
  })
})
