// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The connection of a browser terminal: what it asks the server, in what order, and what it
// does when the connection drops. The production hook and `subscribeStream` run here; only
// the network is local (a `fetch` double this file feeds by hand, and the module's API
// object, whose calls the assertions read).
import { act, renderHook, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('@/features/agentops/api', async (orig) => ({
  ...(await orig<typeof import('@/features/agentops/api')>()),
  agentOpsApi: {
    startTerminal: vi.fn(),
    terminalInput: vi.fn(),
    terminalSize: vi.fn(),
    closeTerminal: vi.fn(),
  },
}))

import { agentOpsApi } from '@/features/agentops/api'
import { ApiError } from '@/lib/api/errors'
import { useTerminalSession } from './use-terminal-session'

interface Conn {
  url: string
  push: (text: string) => void
  end: () => void
}
let conns: Conn[] = []
let nextStatus: number[] = []

const b64 = (s: string) => btoa(s)
const out = (o: number, text: string) =>
  `event: out\ndata: {"o":${o},"d":"${b64(text)}"}\n\n`

function armFetch() {
  vi.spyOn(globalThis, 'fetch').mockImplementation((async (
    input: RequestInfo | URL,
    init?: RequestInit,
  ) => {
    const status = nextStatus.shift() ?? 200
    if (status !== 200) return new Response('{}', { status })
    const enc = new TextEncoder()
    let controller!: ReadableStreamDefaultController<Uint8Array>
    let closed = false
    const body = new ReadableStream<Uint8Array>({
      start(c) {
        controller = c
      },
    })
    init?.signal?.addEventListener('abort', () => {
      if (!closed) {
        closed = true
        controller.close()
      }
    })
    conns.push({
      url: String(input),
      push: (text) => {
        if (!closed) controller.enqueue(enc.encode(text))
      },
      end: () => {
        if (!closed) {
          closed = true
          controller.close()
        }
      },
    })
    return new Response(body, {
      status: 200,
      headers: { 'Content-Type': 'text/event-stream' },
    })
  }) as typeof fetch)
}

const decoder = new TextDecoder()
function setup() {
  const seen: string[] = []
  const hook = renderHook(() =>
    useTerminalSession('ws_1', (b) => seen.push(decoder.decode(b))),
  )
  return { seen, ...hook }
}

beforeEach(() => {
  vi.clearAllMocks()
  window.sessionStorage.clear()
  conns = []
  nextStatus = []
  armFetch()
  vi.mocked(agentOpsApi.startTerminal).mockResolvedValue({
    terminal: 'term_1',
    confinement: 'landlock',
    idle_seconds: 900,
    reconnect_seconds: 60,
  })
  vi.mocked(agentOpsApi.terminalInput).mockResolvedValue({})
  vi.mocked(agentOpsApi.terminalSize).mockResolvedValue({})
  vi.mocked(agentOpsApi.closeTerminal).mockResolvedValue({ closed: true })
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('useTerminalSession', () => {
  it('opens a shell at the size it is given and shows its output once, in order', async () => {
    const { result, seen } = setup()
    await act(() => result.current.open(30, 100))
    expect(agentOpsApi.startTerminal).toHaveBeenCalledWith('ws_1', {
      rows: 30,
      cols: 100,
    })
    await waitFor(() => expect(conns).toHaveLength(1))
    expect(conns[0].url).toBe(
      '/v1/m/sessions/workspaces/ws_1/terminals/term_1/stream?from=0',
    )
    act(() => conns[0].push(out(0, 'one ') + out(4, 'two')))
    await waitFor(() => expect(seen.join('')).toBe('one two'))
    expect(result.current.state).toBe('live')
  })

  it('resumes after a dropped connection from the offset it already showed', async () => {
    const { result, seen } = setup()
    await act(() => result.current.open(24, 80))
    await waitFor(() => expect(conns).toHaveLength(1))
    act(() => conns[0].push(out(0, 'abc')))
    await waitFor(() => expect(seen.join('')).toBe('abc'))
    act(() => conns[0].end())
    await waitFor(() => expect(conns).toHaveLength(2), { timeout: 3000 })
    expect(conns[1].url).toContain('from=3')
    // The server replays from its kept tail: bytes already shown are not shown twice.
    act(() => conns[1].push(out(0, 'abcdef')))
    await waitFor(() => expect(seen.join('')).toBe('abcdef'))
  })

  it('ends with the exit code when the shell exits', async () => {
    const { result } = setup()
    await act(() => result.current.open(24, 80))
    await waitFor(() => expect(conns).toHaveLength(1))
    act(() => conns[0].push('event: exit\ndata: {"code":3}\n\n'))
    await waitFor(() => expect(result.current.state).toBe('ended'))
    expect(result.current.exitCode).toBe(3)
    expect(window.sessionStorage.length).toBe(0)
  })

  it('sends what is typed one request at a time, in the order typed', async () => {
    const { result } = setup()
    await act(() => result.current.open(24, 80))
    await waitFor(() => expect(conns).toHaveLength(1))
    const order: string[] = []
    let release!: () => void
    vi.mocked(agentOpsApi.terminalInput).mockImplementation(
      async (_r, _id, data) => {
        order.push(`start:${data}`)
        if (order.length === 1)
          await new Promise<void>((r) => {
            release = r
          })
        order.push(`done:${data}`)
        return {}
      },
    )
    act(() => {
      result.current.send('l')
      result.current.send('s')
      result.current.send('\r')
    })
    await waitFor(() => expect(order).toEqual(['start:l']))
    act(() => release())
    // What was typed while the first request was out goes in one request, after it.
    await waitFor(() =>
      expect(order).toEqual(['start:l', 'done:l', 'start:s\r', 'done:s\r']),
    )
  })

  it('sends the new size after a short pause, once', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    try {
      const { result } = setup()
      await act(() => result.current.open(24, 80))
      await waitFor(() => expect(conns).toHaveLength(1))
      act(() => {
        result.current.resize(30, 90)
        result.current.resize(31, 91)
      })
      await act(() => vi.advanceTimersByTimeAsync(300))
      expect(agentOpsApi.terminalSize).toHaveBeenCalledTimes(1)
      expect(agentOpsApi.terminalSize).toHaveBeenCalledWith('ws_1', 'term_1', {
        rows: 31,
        cols: 91,
      })
    } finally {
      vi.useRealTimers()
    }
  })

  it('closes the shell on the server and forgets it', async () => {
    const { result } = setup()
    await act(() => result.current.open(24, 80))
    await waitFor(() => expect(conns).toHaveLength(1))
    await act(() => result.current.close())
    expect(agentOpsApi.closeTerminal).toHaveBeenCalledWith('ws_1', 'term_1')
    expect(result.current.state).toBe('ended')
    expect(window.sessionStorage.length).toBe(0)
  })

  it('tells a step-up from a refusal and from too many terminals, and starts nothing', async () => {
    const { result } = setup()
    vi.mocked(agentOpsApi.startTerminal).mockRejectedValueOnce(
      new ApiError(403, 'step_up_required', 'confirm'),
    )
    await act(() => result.current.open(24, 80))
    expect(result.current.problem).toEqual({ kind: 'step_up' })
    vi.mocked(agentOpsApi.startTerminal).mockRejectedValueOnce(
      new ApiError(429, 'too_many', 'busy'),
    )
    await act(() => result.current.open(24, 80))
    expect(result.current.problem).toEqual({ kind: 'busy' })
    vi.mocked(agentOpsApi.startTerminal).mockRejectedValueOnce(
      new ApiError(403, 'forbidden', 'no'),
    )
    await act(() => result.current.open(24, 80))
    expect(result.current.problem).toEqual({ kind: 'refused' })
    expect(result.current.state).toBe('idle')
    expect(conns).toHaveLength(0)
  })

  it('takes back the shell it had before a reload, and never starts one to do it', async () => {
    window.sessionStorage.setItem('olivares.terminal..ws_1', 'term_9')
    const { result, seen } = setup()
    expect(result.current.canResume).toBe(true)
    act(() => {
      result.current.resume()
    })
    await waitFor(() => expect(conns).toHaveLength(1))
    expect(conns[0].url).toContain('/terminals/term_9/stream?from=0')
    act(() => conns[0].push(out(0, 'back')))
    await waitFor(() => expect(seen.join('')).toBe('back'))
    expect(agentOpsApi.startTerminal).not.toHaveBeenCalled()
  })

  it('ends quietly when the server no longer holds the shell it resumes', async () => {
    window.sessionStorage.setItem('olivares.terminal..ws_1', 'term_gone')
    nextStatus = [404]
    const { result } = setup()
    act(() => {
      result.current.resume()
    })
    await waitFor(() => expect(result.current.state).toBe('idle'))
    expect(result.current.exitCode).toBeNull()
    expect(agentOpsApi.startTerminal).not.toHaveBeenCalled()
    expect(window.sessionStorage.length).toBe(0)
  })

  it('keeps asking for as long as the server keeps the shell, and takes it back when the network returns', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    try {
      const { result, seen } = setup()
      await act(() => result.current.open(24, 80))
      await waitFor(() => expect(conns).toHaveLength(1))
      act(() => conns[0].push(out(0, 'before ')))
      await waitFor(() => expect(seen.join('')).toBe('before '))
      // The network is gone for far longer than a few quick retries, inside the 60 s window:
      // ten refusals take 0.5 + 1 + 8 x 2 = 17.5 s of backoff.
      nextStatus = Array.from({ length: 10 }, () => 500)
      act(() => conns[0].end())
      await act(() => vi.advanceTimersByTimeAsync(10_000))
      expect(result.current.state).toBe('reconnecting')
      expect(window.sessionStorage.length).toBe(1)
      await act(() => vi.advanceTimersByTimeAsync(20_000))
      await waitFor(() => expect(conns).toHaveLength(2))
      act(() => conns[1].push(out(0, 'before after')))
      await waitFor(() => expect(seen.join('')).toBe('before after'))
      expect(result.current.state).toBe('live')
    } finally {
      vi.useRealTimers()
    }
  })

  it('gives up once the server has dropped the shell, after its window', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    try {
      vi.mocked(agentOpsApi.startTerminal).mockResolvedValue({
        terminal: 'term_1',
        confinement: 'landlock',
        idle_seconds: 900,
        reconnect_seconds: 10,
      })
      const { result } = setup()
      await act(() => result.current.open(24, 80))
      await waitFor(() => expect(conns).toHaveLength(1))
      nextStatus = Array.from({ length: 50 }, () => 500)
      act(() => conns[0].end())
      await act(() => vi.advanceTimersByTimeAsync(30_000))
      expect(result.current.state).toBe('ended')
      expect(window.sessionStorage.length).toBe(0)
    } finally {
      vi.useRealTimers()
    }
  })
})

it('chunks a large Unicode paste without losing or splitting characters', async () => {
  const { result } = setup()
  await act(() => result.current.open(24, 80))
  const paste = 'a'.repeat(65535) + '😀'.repeat(20000) + '\r'
  await act(() => result.current.send(paste))
  await waitFor(() => {
    const chunks = vi
      .mocked(agentOpsApi.terminalInput)
      .mock.calls.map((call) => call[2])
    expect(chunks.join('')).toBe(paste)
    expect(chunks.length).toBeGreaterThan(1)
    for (const chunk of chunks) {
      expect(new TextEncoder().encode(chunk).length).toBeLessThanOrEqual(65536)
      expect(new TextDecoder().decode(new TextEncoder().encode(chunk))).toBe(
        chunk,
      )
    }
  })
})
