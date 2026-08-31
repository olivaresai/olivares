// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// watching a sandbox run over SSE. What each cell is FOR, because a test that
// cannot go red is decoration:
//
//  - the hook cells feed a real SSE body through a stubbed fetch, so deleting the
//    consumption in stream.ts leaves them with no outputs at all. NOT all of them,
//    and the difference is the point: the Codex contrast no-op'd the subscription and
//    measured 10 red, 1 green. The survivor is "stays closed without a token", which
//    asserts that fetch is NOT called — a consumer that does not exist satisfies it by
//    construction. That is what a deny-closed control is FOR, and it is why the claim
//    here is "every positive cell dies", not "all of them";
//  - the panel cells assert the copy an operator reads, including the NON-FIRING
//    direction (a clean replay must NOT show "connection lost", or a component that
//    always shouted it would pass the "shows lost" cell);
//  - and the LAST cell drives the real view — row click → dialog → stream — because a
//    panel tested only in isolation stays green while nothing renders it. That is the
//    Trap (a seam documented, shipped and held by nothing), and it is the only
//    cell here that fails if RunStreamPanel is unwired from sandbox-view.tsx.
import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest'
import {
  act,
  renderHook,
  renderIntel,
  screen,
  userEvent,
  waitFor,
} from '@/test/intel'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import '@/features/_intel'
import { runsFixture, outputsFixture } from './fixtures'
import { useRunStream } from './stream'
import { RunStreamPanel } from './run-stream'
import type { Output } from './types'
import './i18n'

const auth = vi.hoisted(() => ({ perms: new Set<string>(['sandbox:run:read']) }))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 't1',
    can: (p: string) => auth.perms.has(p),
    confinedWorkspace: null,
  }),
}))

// Only the JSON wrappers are substituted; `runStreamPath` and `sandboxKeys` stay REAL,
// so a typo in the stream path is a failing cell rather than a green one.
const api = vi.hoisted(() => ({
  scenarios: vi.fn(),
  runs: vi.fn(),
  comparisons: vi.fn(),
  outputs: vi.fn(),
}))
vi.mock('./api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('./api')>()),
  sandboxApi: api,
}))

import { SandboxView } from './sandbox-view'

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

/**
 * Delivers every chunk and THEN errors the stream — a transport reset after the frames
 * were already read.
 *
 * It has to be `pull`, not `start`: enqueueing everything and calling `controller.error`
 * in `start` CLEARS the queue (ReadableStreamDefaultControllerError resets it), so the
 * reader would see the error and never the frames — the test would pass for a reason
 * that has nothing to do with the code under test.
 */
function streamThenError(...chunks: string[]): ReadableStream<Uint8Array> {
  const enc = new TextEncoder()
  let i = 0
  return new ReadableStream({
    pull(controller) {
      if (i < chunks.length) {
        controller.enqueue(enc.encode(chunks[i++]))
        return
      }
      controller.error(new Error('transport reset'))
    },
  })
}

const sse = (event: string, data: unknown) =>
  `event: ${event}\ndata: ${JSON.stringify(data)}\n\n`

const out = (n: number, over: Partial<Output> = {}): Output => ({
  id: `out-${n}`,
  run_ref: 'run-002',
  step_key: `step-${n}`,
  output: `synthetic output ${n}`,
  mock_hit: true,
  occurred_at: '2026-06-03T09:40:00Z',
  ...over,
})

const summaryFrame = {
  run_id: 'run-002',
  kind: 'scenario',
  runner: 'inproc-mock',
  isolated: true,
  status: 'degraded',
  steps_total: 4,
  steps_ok: 3,
  steps_error: 1,
  destroyed: true,
}

/** A body that replays `n` outputs and then ENDS CLEANLY (summary + done). */
const cleanBody = (...outs: Output[]) =>
  streamFrom(
    ': connected\n\n',
    ...outs.map((o) => sse('output', o)),
    sse('summary', summaryFrame),
    sse('done', {}),
  )

/** A body that stops mid-replay: no summary, no done — the connection dropped. */
const droppedBody = (...outs: Output[]) =>
  streamFrom(': connected\n\n', ...outs.map((o) => sse('output', o)))

const ok = (body: ReadableStream<Uint8Array>) => ({ ok: true, body }) as Response

const run002 = runsFixture[1]

beforeEach(() => {
  vi.clearAllMocks()
  useSessionStore.setState({
    token: 'olvs_test',
    sessionId: 's1',
    expiresAt: '2099-01-01T00:00:00Z',
  })
  useTenantStore.setState({ activeTenant: 't1' })
  api.runs.mockResolvedValue({ items: runsFixture, has_more: false })
  api.comparisons.mockResolvedValue({ items: [], has_more: false })
  api.scenarios.mockResolvedValue({ items: [], has_more: false })
  api.outputs.mockResolvedValue({
    items: outputsFixture['run-002'],
    has_more: false,
  })
})

afterEach(() => {
  vi.unstubAllGlobals()
  useSessionStore.setState({ token: null, sessionId: null, expiresAt: null })
})

describe('useRunStream — the replay, and what "ended" is allowed to mean', () => {
  it('delivers every output in order, then the summary, and only `done` marks it complete', async () => {
    const fetchMock = vi.fn().mockResolvedValue(ok(cleanBody(out(1), out(2))))
    vi.stubGlobal('fetch', fetchMock)

    const { result } = renderHook(() => useRunStream({ runId: 'run-002' }))
    await waitFor(() => expect(result.current.complete).toBe(true))

    expect(fetchMock).toHaveBeenCalledWith(
      '/v1/m/sandbox/runs/run-002/stream',
      expect.objectContaining({ method: 'GET' }),
    )
    expect(result.current.outputs.map((o) => o.id)).toEqual(['out-1', 'out-2'])
    expect(result.current.summary?.steps_error).toBe(1)
    // A clean replay is NOT an interruption — the non-firing direction of the flag
    // that the next cell asserts fires.
    expect(result.current.interrupted).toBe(false)
  })

  it('a body that ends WITHOUT `done` is a LOST VIEW, never a finished run', async () => {
    const fetchMock = vi.fn().mockResolvedValue(ok(droppedBody(out(1))))
    vi.stubGlobal('fetch', fetchMock)

    const { result } = renderHook(() => useRunStream({ runId: 'run-002' }))
    await waitFor(() => expect(result.current.interrupted).toBe(true))

    expect(result.current.complete).toBe(false)
    expect(result.current.outputs).toHaveLength(1)
  })

  it('a transport error AFTER `done` is not a lost view — the terminal states are exclusive', async () => {
    // The contrast reproduced this: the engine sent everything and closed, then the
    // socket broke on the next read, and the panel rendered "Replay complete" AND
    // "Connection lost" together — with the lost-hint claiming the connection died
    // before a `done` the client had already seen. Nothing is missing here, so nothing
    // may say the view was lost.
    const fetchMock = vi.fn().mockResolvedValue(
      ok(
        streamThenError(
          sse('output', out(1)),
          sse('summary', summaryFrame),
          sse('done', {}),
        ),
      ),
    )
    vi.stubGlobal('fetch', fetchMock)

    const { result } = renderHook(() => useRunStream({ runId: 'run-002' }))
    await waitFor(() => expect(result.current.complete).toBe(true))
    // Let the failing read land before asserting the flag it used to flip.
    await waitFor(() => expect(result.current.status).toBe('closed'))

    expect(result.current.interrupted).toBe(false)
    expect(result.current.outputs).toHaveLength(1)
  })

  it('counts an unreadable frame and refuses to call the replay clean', async () => {
    // A frame the client cannot read is evidence it did NOT receive. Swallowing it and
    // then honouring `done` presented a lossy replay as a whole one — the most expensive
    // defect class in this repo (clean / broken / I COULD NOT LOOK).
    const fetchMock = vi.fn().mockResolvedValue(
      ok(
        streamFrom(
          'event: output\ndata: {"id":"out-1","step_key":\n\n', // truncated JSON
          sse('output', { id: 'out-2' }), // parses, but carries no step key
          sse('output', out(3)),
          sse('done', {}),
        ),
      ),
    )
    vi.stubGlobal('fetch', fetchMock)

    const { result } = renderHook(() => useRunStream({ runId: 'run-002' }))
    await waitFor(() => expect(result.current.complete).toBe(true))

    expect(result.current.unreadable).toBe(2)
    // The readable one is still delivered: an unreadable frame is not a reason to drop
    // the evidence that DID arrive.
    expect(result.current.outputs.map((o) => o.id)).toEqual(['out-3'])
  })

  it('a transport error is an error + a lost view, and still not a completion', async () => {
    const fetchMock = vi.fn().mockRejectedValue(new TypeError('network down'))
    vi.stubGlobal('fetch', fetchMock)

    const { result } = renderHook(() => useRunStream({ runId: 'run-002' }))
    await waitFor(() => expect(result.current.status).toBe('error'))

    expect(result.current.interrupted).toBe(true)
    expect(result.current.complete).toBe(false)
  })

  it('reconnect resumes without loss and without duplicates (the engine replays from the first output)', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(ok(droppedBody(out(1), out(2))))
      // The engine has no cursor: the second connection replays 1 and 2 again and adds
      // the tail. Nothing may be lost, and nothing may be shown twice.
      .mockResolvedValueOnce(ok(cleanBody(out(1), out(2), out(3))))
    vi.stubGlobal('fetch', fetchMock)

    const { result } = renderHook(() => useRunStream({ runId: 'run-002' }))
    await waitFor(() => expect(result.current.interrupted).toBe(true))
    expect(fetchMock).toHaveBeenCalledTimes(1) // nothing reconnects by itself

    act(() => result.current.reconnect())
    await waitFor(() => expect(result.current.complete).toBe(true))

    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(result.current.outputs.map((o) => o.id)).toEqual([
      'out-1',
      'out-2',
      'out-3',
    ])
    expect(result.current.interrupted).toBe(false)
  })

  it('keeps two outputs that SHARE a step_key — dedupe is by row id, not by key', async () => {
    // Legal: the engine only fills a BLANK key (scenarios.go:259-263). Deduping on
    // step_key would silently drop the second, which is the loss this hook exists to
    // avoid; this is the control that the dedupe is not over-eager.
    const fetchMock = vi.fn().mockResolvedValue(
      ok(
        cleanBody(
          out(1, { step_key: 'same', output: 'first' }),
          out(2, { step_key: 'same', output: 'second' }),
        ),
      ),
    )
    vi.stubGlobal('fetch', fetchMock)

    const { result } = renderHook(() => useRunStream({ runId: 'run-002' }))
    await waitFor(() => expect(result.current.complete).toBe(true))

    expect(result.current.outputs.map((o) => o.output)).toEqual([
      'first',
      'second',
    ])
  })

  it('stays closed without a token — no anonymous stream open (the open is audited)', async () => {
    useSessionStore.setState({ token: null, sessionId: null, expiresAt: null })
    const fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)

    const { result } = renderHook(() => useRunStream({ runId: 'run-002' }))

    expect(result.current.status).toBe('closed')
    expect(fetchMock).not.toHaveBeenCalled()
  })
})

describe('RunStreamPanel — the copy an operator reads', () => {
  it('renders the replayed outputs, the engine summary, and labels the surface honestly', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(ok(cleanBody(out(1), out(2)))),
    )

    renderIntel(<RunStreamPanel run={run002} />)

    expect(await screen.findByText('synthetic output 1')).toBeInTheDocument()
    expect(screen.getByText('synthetic output 2')).toBeInTheDocument()
    // It never claims to be watching an execution in flight.
    expect(
      screen.getByText(/committed evidence arriving progressively/i),
    ).toBeInTheDocument()
    expect(await screen.findByText(/Replay complete/i)).toBeInTheDocument()
    expect(screen.getByText(/3 of 4 steps resolved/i)).toBeInTheDocument()
    // NON-FIRING DIRECTION: a clean replay must not mention a lost connection.
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(screen.queryByText(/Connection lost/i)).not.toBeInTheDocument()
  })

  it('says the view was LOST, not that the run finished, and offers to reconnect', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(ok(droppedBody(out(1))))
      .mockResolvedValueOnce(ok(cleanBody(out(1), out(2))))
    vi.stubGlobal('fetch', fetchMock)
    const user = userEvent.setup()

    renderIntel(<RunStreamPanel run={run002} />)

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent(/Connection lost/i)
    expect(alert).toHaveTextContent(/NOT the run finishing/i)
    // What arrived before the drop is kept, and no completion is claimed.
    expect(screen.getByText('synthetic output 1')).toBeInTheDocument()
    expect(screen.queryByText(/Replay complete/i)).not.toBeInTheDocument()
    expect(fetchMock).toHaveBeenCalledTimes(1)

    await user.click(screen.getByRole('button', { name: /Reconnect/i }))

    expect(await screen.findByText('synthetic output 2')).toBeInTheDocument()
    expect(await screen.findByText(/Replay complete/i)).toBeInTheDocument()
    await waitFor(() => expect(screen.queryByRole('alert')).toBeNull())
    // Exactly one open per operator action: opening the stream writes an audit row
    // (modules/sandbox/stream.go:134-148), so a background retry loop would forge
    // ledger entries nobody asked for.
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })

  it('a replay that lost frames is reported as PARTIAL, never as complete', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        ok(
          streamFrom(
            'event: output\ndata: {"id":"out-1",\n\n', // truncated JSON
            sse('done', {}),
          ),
        ),
      ),
    )

    renderIntel(<RunStreamPanel run={run002} />)

    expect(
      await screen.findByText(/not the whole record/i),
    ).toBeInTheDocument()
    expect(screen.getByRole('alert')).toHaveTextContent(/could not read: 1/i)
    // "No outputs recorded" is a claim about the RUN and would be false here.
    expect(screen.getByText(/No step output could be read/i)).toBeInTheDocument()
    expect(screen.queryByText('No outputs recorded')).not.toBeInTheDocument()
    // NON-FIRING DIRECTION: the clean label must not appear alongside the partial one.
    expect(screen.queryByText('Replay complete')).not.toBeInTheDocument()
  })

  it('falls back to the stored outputs when the stream delivered NOTHING, labelled as a snapshot', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('blocked')))

    renderIntel(<RunStreamPanel run={run002} />)

    expect(
      await screen.findByText(/Refund request classified/i),
    ).toBeInTheDocument()
    // The evidence is there, and it is NOT presented as the live view.
    expect(screen.getByText(/a snapshot, not the live view/i)).toBeInTheDocument()
    expect(api.outputs).toHaveBeenCalledWith('run-002')
    expect(screen.getByRole('alert')).toHaveTextContent(/Connection lost/i)
  })

  it('does not read the JSON fallback while the stream works', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(ok(cleanBody(out(1)))))

    renderIntel(<RunStreamPanel run={run002} />)

    await screen.findByText(/Replay complete/i)
    expect(api.outputs).not.toHaveBeenCalled()
  })
})

describe('SandboxView — the run row actually opens the stream', () => {
  it('opens the stream for the clicked run (the wiring, not just the panel)', async () => {
    const fetchMock = vi.fn().mockResolvedValue(ok(cleanBody(out(1))))
    vi.stubGlobal('fetch', fetchMock)
    const user = userEvent.setup()

    renderIntel(<SandboxView />)
    await screen.findByRole('grid')
    // Nothing is streamed until a run is opened.
    expect(fetchMock).not.toHaveBeenCalled()

    await user.click(screen.getByText('prompt:refund-eligibility'))

    await waitFor(() =>
      expect(fetchMock).toHaveBeenCalledWith(
        '/v1/m/sandbox/runs/run-002/stream',
        expect.objectContaining({ method: 'GET' }),
      ),
    )
    expect(await screen.findByText('synthetic output 1')).toBeInTheDocument()
  })
})
