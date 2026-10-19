// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The pin is the engine's: toggling calls its routes once, also under StrictMode, and
// the sessions are re-read afterwards (#2130).
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, renderHook, waitFor } from '@testing-library/react'
import { StrictMode, type ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { RunDTO } from '@/features/agentops/types'
import { ApiError } from '@/lib/api/errors'
import { mergeSessions } from './provenance'
import { encodePins, pinStorageKey } from './session-pins'
import { useSessionPins } from './use-session-pins'

const api = vi.hoisted(() => ({
  pin: vi.fn(),
  unpin: vi.fn(),
  listRuns: vi.fn(),
}))
const toast = vi.hoisted(() => ({ error: vi.fn() }))
vi.mock('@/features/agentops/api', async (original) => ({
  ...(await original<typeof import('@/features/agentops/api')>()),
  agentOpsApi: api,
}))
vi.mock('@/components/ui/toaster', () => ({ toast }))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 't-1',
    principal: { kind: 'user', user_id: 'u-1' },
  }),
}))

function run(over: Partial<RunDTO> = {}): RunDTO {
  return {
    run_ref: 'run-1',
    transport: 'stream-json',
    permission_mode: 'default',
    isolation: 'native',
    state: 'running',
    last_event_seq: 0,
    pep_provisioned: true,
    record_io: false,
    critical: false,
    ...over,
  }
}

let client: QueryClient
const wrapper = ({ children }: { children: ReactNode }) => (
  <StrictMode>
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  </StrictMode>
)

beforeEach(() => {
  client = new QueryClient({ defaultOptions: { mutations: { retry: false } } })
  api.pin.mockResolvedValue({ run_ref: 'run-1', pinned: true })
  api.unpin.mockResolvedValue({ run_ref: 'run-1', pinned: false })
})
afterEach(() => {
  vi.clearAllMocks()
  window.localStorage.clear()
})

describe('useSessionPins', () => {
  it('reads the pinned set from the runs the engine returned', () => {
    const sessions = mergeSessions(
      [],
      [
        run({
          run_ref: 'run-a',
          pinned: true,
          pinned_at: '2026-10-14T10:00:02Z',
        }),
        run({ run_ref: 'run-b' }),
        run({
          run_ref: 'run-c',
          pinned: true,
          pinned_at: '2026-10-14T10:00:01Z',
        }),
      ],
    )
    const { result } = renderHook(() => useSessionPins(sessions, false), {
      wrapper,
    })
    expect([...result.current.pinned]).toEqual(['run:run-c', 'run:run-a'])
  })

  it('pins the session with ONE call to the engine, also under StrictMode, then re-reads', async () => {
    const sessions = mergeSessions([], [run()])
    const refetch = vi.spyOn(client, 'invalidateQueries')
    const { result } = renderHook(() => useSessionPins(sessions, false), {
      wrapper,
    })
    act(() => result.current.toggle('run:run-1'))
    await waitFor(() => expect(refetch).toHaveBeenCalled())
    expect(api.pin).toHaveBeenCalledTimes(1)
    expect(api.pin).toHaveBeenCalledWith('run-1')
    expect(api.unpin).not.toHaveBeenCalled()
  })

  it('unpins every pinned run of a pinned session, and pins nothing', async () => {
    const sessions = mergeSessions(
      [],
      [
        run({
          run_ref: 'run-1',
          claude_session_id: 'sid-x',
          pinned: true,
          pinned_at: '2026-10-14T10:00:01Z',
        }),
        run({
          run_ref: 'run-2',
          claude_session_id: 'sid-x',
          pinned: true,
          pinned_at: '2026-10-14T10:00:02Z',
        }),
      ],
    )
    const { result } = renderHook(() => useSessionPins(sessions, false), {
      wrapper,
    })
    expect(sessions).toHaveLength(1)
    act(() => result.current.toggle(sessions[0].key))
    await waitFor(() => expect(api.unpin).toHaveBeenCalledTimes(2))
    expect(api.unpin.mock.calls.map((c) => c[0]).sort()).toEqual([
      'run-1',
      'run-2',
    ])
    expect(api.pin).not.toHaveBeenCalled()
  })

  it('says so, and calls nothing, for a session with no run to pin', () => {
    const observed = [{ key: 'live:lr-x', runs: [] }] as unknown as Parameters<
      typeof useSessionPins
    >[0]
    const { result } = renderHook(() => useSessionPins(observed, false), {
      wrapper,
    })
    act(() => result.current.toggle('live:lr-x'))
    expect(api.pin).not.toHaveBeenCalled()
    expect(api.unpin).not.toHaveBeenCalled()
    expect(toast.error).toHaveBeenCalledTimes(1)
  })

  it('acts on the session it is handed when the list does not hold it (a deep link)', async () => {
    const deepLinked = mergeSessions([], [run({ run_ref: 'run-far' })])[0]
    const { result } = renderHook(() => useSessionPins([], false), { wrapper })
    act(() => result.current.toggle(deepLinked.key, deepLinked))
    await waitFor(() => expect(api.pin).toHaveBeenCalledWith('run-far'))
    expect(toast.error).not.toHaveBeenCalled()
  })

  it('pins the run that is still listed, never a released one', async () => {
    const sessions = mergeSessions(
      [],
      [
        run({
          run_ref: 'run-gone',
          claude_session_id: 'sid-y',
          state: 'cleaned',
        }),
        run({
          run_ref: 'run-live',
          claude_session_id: 'sid-y',
          state: 'stopped',
        }),
      ],
    )
    const { result } = renderHook(() => useSessionPins(sessions, false), {
      wrapper,
    })
    act(() => result.current.toggle(sessions[0].key))
    await waitFor(() => expect(api.pin).toHaveBeenCalledTimes(1))
    expect(api.pin).toHaveBeenCalledWith('run-live')
  })

  it('ignores a second toggle while the first is still in flight', async () => {
    api.pin.mockReturnValue(new Promise(() => {}))
    const sessions = mergeSessions([], [run()])
    const { result } = renderHook(() => useSessionPins(sessions, false), {
      wrapper,
    })
    act(() => result.current.toggle('run:run-1'))
    await waitFor(() => expect(api.pin).toHaveBeenCalledTimes(1))
    act(() => result.current.toggle('run:run-1'))
    // The query client starts a mutation on a later turn of the event loop, so the
    // count is only meaningful after one has passed: without that, a second call
    // that was NOT dropped would not have started yet and the test would pass anyway.
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 25))
    })
    expect(api.pin).toHaveBeenCalledTimes(1)
  })

  it('says so when the engine refuses, and still re-reads the sessions', async () => {
    api.pin.mockRejectedValue(new Error('boom'))
    const sessions = mergeSessions([], [run()])
    const refetch = vi.spyOn(client, 'invalidateQueries')
    const { result } = renderHook(() => useSessionPins(sessions, false), {
      wrapper,
    })
    act(() => result.current.toggle('run:run-1'))
    await waitFor(() => expect(toast.error).toHaveBeenCalledTimes(1))
    expect(refetch).toHaveBeenCalled()
  })
})

// Pins this browser kept before the engine did must not be lost (#2130, linux rule 1).
describe('useSessionPins — pins the browser kept before the engine did', () => {
  const key = pinStorageKey(
    window.location.origin,
    { kind: 'user', user_id: 'u-1' } as never,
    't-1',
  ) as string
  const store = (addresses: string[]) =>
    window.localStorage.setItem(key, encodePins(addresses))
  const stored = () => window.localStorage.getItem(key)
  const settle = () =>
    act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 25))
    })

  it('pins them on the engine oldest first, in the stored tenant, then removes the old key', async () => {
    api.listRuns.mockImplementation(async (params: Record<string, string>) => ({
      items: [run({ run_ref: `run-for-${Object.values(params)[0]}` })],
    }))
    store(['run:run-1', 'live:lr-9', 'sess:sid-7', 'run:run-3'])
    const refetch = vi.spyOn(client, 'invalidateQueries')
    renderHook(() => useSessionPins([], true), { wrapper })
    await waitFor(() => expect(stored()).toBeNull())
    expect(api.pin.mock.calls).toEqual([
      ['run-1', { tenant: 't-1' }],
      ['run-for-lr-9', { tenant: 't-1' }],
      ['run-for-sid-7', { tenant: 't-1' }],
      ['run-3', { tenant: 't-1' }],
    ])
    expect(api.listRuns.mock.calls).toEqual([
      [{ live_ref: 'lr-9', limit: 20 }, { tenant: 't-1' }],
      [{ claude_session_id: 'sid-7', limit: 20 }, { tenant: 't-1' }],
    ])
    expect(refetch).toHaveBeenCalled()
  })

  it('keeps stored a session that has no run to pin (an observed one), and pins nothing for it', async () => {
    api.listRuns.mockResolvedValue({ items: [] })
    store(['sess:observed-only', 'live:lr-observed'])
    const refetch = vi.spyOn(client, 'invalidateQueries')
    renderHook(() => useSessionPins([], true), { wrapper })
    await waitFor(() => expect(refetch).toHaveBeenCalled())
    expect(api.pin).not.toHaveBeenCalled()
    expect(stored()).toBe(
      encodePins(['sess:observed-only', 'live:lr-observed']),
    )
  })

  it('moves the sessions that have a run, and keeps the observed ones stored in their order', async () => {
    api.listRuns.mockImplementation(async (params: Record<string, string>) => {
      const name = Object.values(params)[0]
      return {
        items: name.startsWith('observed')
          ? []
          : [run({ run_ref: `run-for-${name}` })],
      }
    })
    store(['sess:observed-1', 'run:run-1', 'live:observed-2', 'sess:sid-3'])
    renderHook(() => useSessionPins([], true), { wrapper })
    await waitFor(() =>
      expect(stored()).toBe(encodePins(['sess:observed-1', 'live:observed-2'])),
    )
    expect(api.pin.mock.calls.map((c) => c[0])).toEqual([
      'run-1',
      'run-for-sid-3',
    ])
  })

  it('does nothing before the engine run list has been read', async () => {
    store(['run:run-1', 'live:lr-9'])
    renderHook(() => useSessionPins([], false), { wrapper })
    await settle()
    expect(api.pin).not.toHaveBeenCalled()
    expect(api.listRuns).not.toHaveBeenCalled()
    expect(stored()).not.toBeNull()
  })

  it('keeps the old key until the pins have landed', async () => {
    api.pin.mockReturnValue(new Promise(() => {}))
    store(['run:run-1', 'run:run-2'])
    renderHook(() => useSessionPins([], true), { wrapper })
    await waitFor(() => expect(api.pin).toHaveBeenCalledTimes(1))
    await settle()
    expect(stored()).toBe(encodePins(['run:run-1', 'run:run-2']))
  })

  it('reads a browser with nothing stored without a single call', async () => {
    renderHook(() => useSessionPins([], true), { wrapper })
    await settle()
    expect(api.pin).not.toHaveBeenCalled()
    expect(api.listRuns).not.toHaveBeenCalled()
  })

  it('moves them once, however often the list re-renders while the move is in flight', async () => {
    api.pin.mockReturnValue(new Promise(() => {}))
    store(['run:run-1'])
    const { rerender } = renderHook(() => useSessionPins([], true), { wrapper })
    await waitFor(() => expect(api.pin).toHaveBeenCalledTimes(1))
    rerender()
    rerender()
    await settle()
    expect(api.pin).toHaveBeenCalledTimes(1)
  })

  it('drops a pin whose run is gone or released, and keeps one that failed for another reason', async () => {
    api.pin.mockImplementation(async (ref: string) => {
      if (ref === 'run-gone')
        throw new ApiError(404, 'not_found', 'No such run')
      if (ref === 'run-released')
        throw new ApiError(409, 'conflict', 'Released')
      if (ref === 'run-flaky') throw new ApiError(503, 'unavailable', 'Down')
      return { run_ref: ref, pinned: true }
    })
    store(['run:run-gone', 'run:run-released', 'run:run-flaky', 'run:run-ok'])
    renderHook(() => useSessionPins([], true), { wrapper })
    await waitFor(() => expect(stored()).toBe(encodePins(['run:run-flaky'])))
    expect(api.pin).toHaveBeenCalledTimes(4)
  })

  const pinnedRuns = (n: number) =>
    mergeSessions(
      [],
      Array.from({ length: n }, (_, i) =>
        run({
          run_ref: `run-e${i}`,
          pinned: true,
          pinned_at: `2026-10-14T10:00:${String(i).padStart(2, '0')}Z`,
        }),
      ),
    )

  it("fills only the slots the engine's own pins leave, newest stored first", async () => {
    store(['run:run-old', 'run:run-new'])
    renderHook(() => useSessionPins(pinnedRuns(19), true), { wrapper })
    await waitFor(() => expect(stored()).toBeNull())
    expect(api.pin.mock.calls.map((c) => c[0])).toEqual(['run-new'])
  })

  it('spends no slot on a session the engine already holds', async () => {
    store(['run:run-old', 'run:run-e3'])
    renderHook(() => useSessionPins(pinnedRuns(19), true), { wrapper })
    await waitFor(() => expect(stored()).toBeNull())
    expect(api.pin.mock.calls.map((c) => c[0])).toEqual(['run-old'])
  })

  it('lets observed sessions that stay stored take none of the free slots', async () => {
    api.listRuns.mockResolvedValue({ items: [] })
    store(['run:run-old', 'run:run-new', 'sess:observed-1', 'sess:observed-2'])
    renderHook(() => useSessionPins(pinnedRuns(18), true), { wrapper })
    await waitFor(() =>
      expect(stored()).toBe(encodePins(['sess:observed-1', 'sess:observed-2'])),
    )
    expect(api.pin.mock.calls.map((c) => c[0])).toEqual(['run-old', 'run-new'])
  })

  it('counts the engine slots by pinned run, not by row: a session with two pinned runs holds two', async () => {
    const sessions = mergeSessions(
      [],
      Array.from({ length: 20 }, (_, i) =>
        run({
          run_ref: `run-p${i}`,
          claude_session_id: `sid-${Math.floor(i / 2)}`,
          pinned: true,
          pinned_at: `2026-10-14T10:00:${String(i).padStart(2, '0')}Z`,
        }),
      ),
    )
    expect(sessions).toHaveLength(10)
    store(['run:run-old'])
    renderHook(() => useSessionPins(sessions, true), { wrapper })
    await settle()
    expect(api.pin).not.toHaveBeenCalled()
    expect(stored()).toBe(encodePins(['run:run-old']))
  })

  it('spends no slot on a stored name of a run the engine already holds under another name', async () => {
    api.listRuns.mockResolvedValue({ items: [run({ run_ref: 'run-e3' })] })
    store(['run:run-new', 'live:lr-e3'])
    renderHook(() => useSessionPins(pinnedRuns(19), true), { wrapper })
    await waitFor(() => expect(stored()).toBeNull())
    expect(api.pin.mock.calls.map((c) => c[0])).toEqual(['run-new'])
  })

  it('pins a run once when two stored names resolve to it', async () => {
    api.listRuns.mockResolvedValue({ items: [run({ run_ref: 'run-a' })] })
    store(['run:run-a', 'live:lr-a'])
    renderHook(() => useSessionPins([], true), { wrapper })
    await waitFor(() => expect(stored()).toBeNull())
    expect(api.pin.mock.calls.map((c) => c[0])).toEqual(['run-a'])
  })

  it('spends no slot on a session the engine already pins through another of its runs', async () => {
    api.listRuns.mockResolvedValue({
      items: [run({ run_ref: 'run-newer' }), run({ run_ref: 'run-e3' })],
    })
    store(['run:run-new', 'sess:sid-x'])
    renderHook(() => useSessionPins(pinnedRuns(19), true), { wrapper })
    await waitFor(() => expect(stored()).toBeNull())
    expect(api.pin.mock.calls.map((c) => c[0])).toEqual(['run-new'])
  })

  it('removes the old key when the engine is full and already holds everything stored', async () => {
    store(['run:run-e3'])
    renderHook(() => useSessionPins(pinnedRuns(20), true), { wrapper })
    await waitFor(() => expect(stored()).toBeNull())
    expect(api.pin).not.toHaveBeenCalled()
  })

  it.each([503, 404, 409])(
    'keeps a stored name whose lookup answered %i',
    async (status) => {
      api.listRuns.mockRejectedValue(new ApiError(status, 'x', 'No'))
      store(['live:lr-x', 'run:run-ok'])
      renderHook(() => useSessionPins([], true), { wrapper })
      await waitFor(() => expect(stored()).toBe(encodePins(['live:lr-x'])))
      expect(api.pin.mock.calls.map((c) => c[0])).toEqual(['run-ok'])
    },
  )

  it('keeps what stays stored in the order it was stored, whichever step kept it', async () => {
    api.listRuns.mockResolvedValue({ items: [] })
    api.pin.mockImplementation(async (ref: string) => {
      if (ref === 'run-flaky') throw new ApiError(503, 'unavailable', 'Down')
      return { run_ref: ref, pinned: true }
    })
    store(['run:run-flaky', 'sess:observed', 'run:run-ok'])
    renderHook(() => useSessionPins([], true), { wrapper })
    await waitFor(() =>
      expect(stored()).toBe(encodePins(['run:run-flaky', 'sess:observed'])),
    )
  })

  it('pins both runs when two stored names of one run share the last free slots', async () => {
    api.listRuns.mockResolvedValue({ items: [run({ run_ref: 'run-y' })] })
    store(['run:run-x', 'run:run-y', 'live:lr-y'])
    renderHook(() => useSessionPins(pinnedRuns(18), true), { wrapper })
    await waitFor(() => expect(stored()).toBeNull())
    expect(api.pin.mock.calls.map((c) => c[0])).toEqual(['run-x', 'run-y'])
  })

  it('moves nothing, and keeps the old key, when the engine already holds 20', async () => {
    store(['run:run-old'])
    renderHook(() => useSessionPins(pinnedRuns(20), true), { wrapper })
    await settle()
    expect(api.pin).not.toHaveBeenCalled()
    expect(stored()).toBe(encodePins(['run:run-old']))
  })
})
