// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
/**
 * WHO OWNS A LIVE CONNECTION, AND WHEN IT STOPS BEING THEIRS.
 *
 * The production `useLiveStream`, `subscribeStream` and frame parser run here; only the
 * response body is local (a `ReadableStream` this file feeds by hand, through a `fetch`
 * double). Nothing leaves the process: no engine, no network, no provider.
 *
 * ⛔ WHAT THIS EXISTS TO PIN (ratified effective-context contract, 2026-09-08). The hook
 *    restarts only for path/token/tenant/enabled/events/query and calls the LATEST
 *    callback for every frame. A caller whose acting principal or credential can change
 *    while the URL does not — the Sessions inventory — therefore had its previous
 *    connection delivering into its new context, and on an A → B → A return the reused
 *    boundary key made that look correct. `contextKey` gives such a caller a lifetime;
 *    callers that do not pass one keep what they had.
 */
import { fireEvent, render, waitFor } from '@testing-library/react'
import { useCallback, useState } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import { useLiveStream } from './sse'

interface Frame {
  id: string
}

/** One connection the test can feed and end by hand. */
interface Conn {
  url: string
  push: (text: string) => void
  end: () => void
  aborted: () => boolean
}

let conns: Conn[] = []
let fetchSpy: ReturnType<typeof vi.spyOn>
/** When true the next connections keep their body OPEN after the request is aborted,
 * so a frame can still be delivered into the parser the way a chunk already read
 * before the abort would be. Aborting the request is the usual reason a connection
 * goes quiet; it is not a guarantee, and the guard must not depend on it. */
let deafToAbort = false

function armFetch() {
  fetchSpy = vi.spyOn(globalThis, 'fetch').mockImplementation((async (
    input: RequestInfo | URL,
    init?: RequestInit,
  ) => {
    const enc = new TextEncoder()
    let controller!: ReadableStreamDefaultController<Uint8Array>
    let closed = false
    const body = new ReadableStream<Uint8Array>({
      start(c) {
        controller = c
      },
    })
    const signal = init?.signal
    const deaf = deafToAbort
    signal?.addEventListener('abort', () => {
      if (!closed && !deaf) {
        closed = true
        try {
          controller.close()
        } catch {
          // already closed by the test
        }
      }
    })
    conns.push({
      url: String(input),
      push: (text: string) => {
        if (!closed) controller.enqueue(enc.encode(text))
      },
      end: () => {
        if (!closed) {
          closed = true
          controller.close()
        }
      },
      aborted: () => !!signal?.aborted,
    })
    return new Response(body, {
      status: 200,
      headers: { 'Content-Type': 'text/event-stream' },
    })
  }) as typeof fetch)
}

/** A consumer that records every frame it is handed, tagged with the context it was in
 * when it received it — which is exactly what must never be somebody else's. */
function Consumer({
  contextKey,
  sink,
  enabled = true,
}: {
  contextKey?: string | number
  sink: { got: { ctx: string | number | undefined; id: string }[] }
  enabled?: boolean
}) {
  const onSnapshot = useCallback(
    (snap: Frame) => {
      sink.got.push({ ctx: contextKey, id: snap.id })
    },
    [contextKey, sink],
  )
  const { status } = useLiveStream<Frame>({
    path: '/v1/m/sessions/stream',
    events: ['session'],
    enabled,
    contextKey,
    onSnapshot,
  })
  return <span data-testid="status">{status}</span>
}

const frame = (id: string) => `event: session\ndata: {"id":"${id}"}\n\n`

beforeEach(() => {
  conns = []
  deafToAbort = false
  armFetch()
  useSessionStore.setState({ token: 'sse-ctx-token' })
  useTenantStore.setState({ activeTenant: 'tenant-a' })
})

afterEach(() => {
  fetchSpy.mockRestore()
})

describe('useLiveStream — a connection belongs to the context that opened it', () => {
  it('retires the previous connection on a context change: its frames never reach the new context', async () => {
    const sink = {
      got: [] as { ctx: string | number | undefined; id: string }[],
    }
    const { rerender } = render(<Consumer contextKey={1} sink={sink} />)
    await waitFor(() => expect(conns).toHaveLength(1))
    const first = conns[0]!
    first.push(frame('under-1'))
    await waitFor(() => expect(sink.got).toHaveLength(1))
    expect(sink.got[0], 'ctx1/own-frame-delivered').toEqual({
      ctx: 1,
      id: 'under-1',
    })

    rerender(<Consumer contextKey={2} sink={sink} />)
    await waitFor(() => expect(conns).toHaveLength(2))
    expect(first.aborted(), 'ctx-change/old-connection-aborted').toBe(true)

    // The old connection speaks anyway — a chunk already read, a frame already parsed.
    first.push(frame('late-from-1'))
    conns[1]!.push(frame('under-2'))
    await waitFor(() => expect(sink.got).toHaveLength(2))
    expect(
      sink.got.map((g) => g.id),
      'ctx-change/late-old-frame-dropped',
    ).toEqual(['under-1', 'under-2'])
    expect(
      sink.got[1]!.ctx,
      'ctx-change/new-frame-belongs-to-new-context',
    ).toBe(2)
  })

  it('A → B → A retires the FIRST A connection, though the key is the same again', async () => {
    const sink = {
      got: [] as { ctx: string | number | undefined; id: string }[],
    }
    const { rerender } = render(<Consumer contextKey="A" sink={sink} />)
    await waitFor(() => expect(conns).toHaveLength(1))
    const firstA = conns[0]!

    rerender(<Consumer contextKey="B" sink={sink} />)
    await waitFor(() => expect(conns).toHaveLength(2))
    rerender(<Consumer contextKey="A" sink={sink} />)
    await waitFor(() => expect(conns).toHaveLength(3))

    // Key equality is what made this case wrong before: "A" is current again, and the
    // first A connection is NOT.
    firstA.push(frame('from-first-A'))
    conns[2]!.push(frame('from-second-A'))
    await waitFor(() => expect(sink.got).toHaveLength(1))
    expect(sink.got[0]!.id, 'a-b-a/first-A-connection-stays-retired').toBe(
      'from-second-A',
    )
    expect(firstA.aborted(), 'a-b-a/first-A-aborted').toBe(true)
  })

  it('a frame that survives the abort is still dropped once the connection is retired', async () => {
    // The abort normally ends the body, and then nothing can be delivered at all. That
    // is the comfortable case and not the one worth proving: a chunk already read — and
    // a frame already parsed out of it — is in the call stack, past every network
    // guarantee. This connection therefore ignores its abort entirely, and it is
    // retired by an UNMOUNT, which every caller does, opted in or not. That isolates
    // the delivery guard from the reconnection behaviour `contextKey` adds.
    deafToAbort = true
    const sink = {
      got: [] as { ctx: string | number | undefined; id: string }[],
    }
    const { unmount } = render(<Consumer sink={sink} />)
    await waitFor(() => expect(conns).toHaveLength(1))
    const stubborn = conns[0]!
    stubborn.push(frame('while-mounted'))
    await waitFor(() => expect(sink.got).toHaveLength(1))

    unmount()
    expect(stubborn.aborted(), 'deaf/abort-was-signalled').toBe(true)
    // …and the body is still open, so this really does reach the parser.
    stubborn.push(frame('after-retirement'))
    await new Promise((r) => setTimeout(r, 20))
    expect(
      sink.got.map((g) => g.id),
      'deaf/retired-connection-delivers-nothing',
    ).toEqual(['while-mounted'])
  })

  it('a same-context rerender does not open a second connection', async () => {
    const sink = {
      got: [] as { ctx: string | number | undefined; id: string }[],
    }
    const { rerender } = render(<Consumer contextKey={7} sink={sink} />)
    await waitFor(() => expect(conns).toHaveLength(1))
    for (let i = 0; i < 3; i++)
      rerender(<Consumer contextKey={7} sink={sink} />)
    await new Promise((r) => setTimeout(r, 20))
    expect(conns, 'same-context/no-extra-connection').toHaveLength(1)
    conns[0]!.push(frame('still-live'))
    await waitFor(() => expect(sink.got).toHaveLength(1))
  })

  it('unmounting retires the connection: a frame already in flight is dropped', async () => {
    const sink = {
      got: [] as { ctx: string | number | undefined; id: string }[],
    }
    const { unmount } = render(<Consumer contextKey={3} sink={sink} />)
    await waitFor(() => expect(conns).toHaveLength(1))
    unmount()
    conns[0]!.push(frame('after-unmount'))
    await new Promise((r) => setTimeout(r, 20))
    expect(sink.got, 'unmount/nothing-delivered').toHaveLength(0)
  })

  it('a caller that passes no contextKey keeps its behaviour, and still drops post-retirement frames', async () => {
    const sink = {
      got: [] as { ctx: string | number | undefined; id: string }[],
    }
    const { rerender, unmount } = render(<Consumer sink={sink} />)
    await waitFor(() => expect(conns).toHaveLength(1))
    // A rerender does not reconnect for the unopted caller either.
    rerender(<Consumer sink={sink} />)
    await new Promise((r) => setTimeout(r, 20))
    expect(conns, 'unopted/no-extra-connection').toHaveLength(1)
    conns[0]!.push(frame('normal'))
    await waitFor(() => expect(sink.got).toHaveLength(1))
    expect(sink.got[0]!.id, 'unopted/frames-still-delivered').toBe('normal')
    unmount()
    conns[0]!.push(frame('after-unmount'))
    await new Promise((r) => setTimeout(r, 20))
    expect(sink.got, 'unopted/no-delivery-after-retirement').toHaveLength(1)
  })

  it('reports the transport status without pretending to be open when disabled', async () => {
    const sink = {
      got: [] as { ctx: string | number | undefined; id: string }[],
    }
    const { getByTestId, rerender } = render(
      <Consumer contextKey={9} sink={sink} enabled={false} />,
    )
    expect(getByTestId('status').textContent, 'disabled/closed').toBe('closed')
    expect(conns, 'disabled/no-connection').toHaveLength(0)
    rerender(<Consumer contextKey={9} sink={sink} enabled />)
    await waitFor(() => expect(conns).toHaveLength(1))
    await waitFor(() =>
      expect(getByTestId('status').textContent, 'enabled/open').toBe('open'),
    )
  })
})

/** A stateful wrapper, so the context changes from INSIDE React rather than from a
 * rerender with new props — the shape the view actually has. */
function Switcher({
  sink,
}: {
  sink: { got: { ctx: string | number | undefined; id: string }[] }
}) {
  const [ctx, setCtx] = useState(1)
  return (
    <div>
      <button onClick={() => setCtx((c) => c + 1)}>advance</button>
      <Consumer contextKey={ctx} sink={sink} />
    </div>
  )
}

describe('useLiveStream — the owner changes from inside the tree', () => {
  it('advancing the episode retires what the previous one had open', async () => {
    const sink = {
      got: [] as { ctx: string | number | undefined; id: string }[],
    }
    const { getByText } = render(<Switcher sink={sink} />)
    await waitFor(() => expect(conns).toHaveLength(1))
    const first = conns[0]!
    fireEvent.click(getByText('advance'))
    await waitFor(() => expect(conns).toHaveLength(2))
    first.push(frame('stale'))
    conns[1]!.push(frame('current'))
    await waitFor(() => expect(sink.got).toHaveLength(1))
    expect(sink.got[0], 'episode-advance/only-current-delivers').toEqual({
      ctx: 2,
      id: 'current',
    })
  })
})
