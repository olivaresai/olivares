// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it, vi } from 'vitest'
import { createSSEParser, subscribeStream } from './sse'

describe('createSSEParser', () => {
  it('parses a single complete frame with an event name', () => {
    const seen: { event: string; data: string }[] = []
    const feed = createSSEParser((m) => seen.push(m))
    feed('event: session\ndata: {"x":1}\n\n')
    expect(seen).toEqual([{ event: 'session', data: '{"x":1}' }])
  })

  it('defaults the event to "message" and joins multi-line data', () => {
    const seen: { event: string; data: string }[] = []
    const feed = createSSEParser((m) => seen.push(m))
    feed('data: line1\ndata: line2\n\n')
    expect(seen).toEqual([{ event: 'message', data: 'line1\nline2' }])
  })

  it('ignores comment heartbeats (": ping") and empty frames', () => {
    const seen: unknown[] = []
    const feed = createSSEParser((m) => seen.push(m))
    feed(': ping\n\n')
    feed(': connected\n\n')
    expect(seen).toHaveLength(0)
  })

  it('buffers a frame split across chunks', () => {
    const seen: { event: string; data: string }[] = []
    const feed = createSSEParser((m) => seen.push(m))
    feed('event: heal')
    feed('th\ndata: ')
    feed('ok\n\n')
    expect(seen).toEqual([{ event: 'health', data: 'ok' }])
  })

  it('handles CRLF line endings', () => {
    const seen: { event: string; data: string }[] = []
    const feed = createSSEParser((m) => seen.push(m))
    feed('event: a\r\ndata: b\r\n\r\n')
    expect(seen).toEqual([{ event: 'a', data: 'b' }])
  })

  it('strips exactly one leading space after the colon', () => {
    const seen: { event: string; data: string }[] = []
    const feed = createSSEParser((m) => seen.push(m))
    feed('data:  two-spaces\n\n') // one stripped, one kept
    expect(seen[0]!.data).toBe(' two-spaces')
  })
})

describe('subscribeStream', () => {
  function streamOf(chunks: string[]): ReadableStream<Uint8Array> {
    const enc = new TextEncoder()
    return new ReadableStream({
      start(controller) {
        for (const c of chunks) controller.enqueue(enc.encode(c))
        controller.close()
      },
    })
  }

  it('attaches bearer + tenant headers and pumps frames to onMessage', async () => {
    const fetchMock = vi.fn(
      async (_url: string, _init?: RequestInit) =>
        new Response(streamOf(['event: session\ndata: {"n":1}\n\n']), {
          status: 200,
        }),
    )
    vi.stubGlobal('fetch', fetchMock)

    const seen: { event: string; data: string }[] = []
    const controller = new AbortController()
    await subscribeStream({
      path: '/v1/m/sessions/stream',
      token: 'olvs_test',
      tenant: 'tenant-1',
      signal: controller.signal,
      onMessage: (m) => seen.push(m),
    })

    expect(seen).toEqual([{ event: 'session', data: '{"n":1}' }])
    const headers = fetchMock.mock.calls[0]![1]!.headers as Headers
    expect(headers.get('Authorization')).toBe('Bearer olvs_test')
    expect(headers.get('X-Olivares-Tenant')).toBe('tenant-1')
    vi.unstubAllGlobals()
  })

  it('throws with the status on a non-OK response', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => new Response('', { status: 403 })),
    )
    const controller = new AbortController()
    await expect(
      subscribeStream({
        path: '/v1/m/health/stream',
        token: 't',
        tenant: 't1',
        signal: controller.signal,
        onMessage: () => {},
      }),
    ).rejects.toMatchObject({ status: 403 })
    vi.unstubAllGlobals()
  })
})
