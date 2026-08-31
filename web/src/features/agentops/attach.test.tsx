// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { renderHook, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import { useRunAttach } from './attach'
import type { AttachFrame, AttachLag } from './types'

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
})
