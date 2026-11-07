// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The snooze, done and unread marks are the engine's: a change calls its
// routes and the sessions are re-read afterwards, success or not. Opening a session marks it
// read.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, renderHook, waitFor } from '@testing-library/react'
import type { ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { RunDTO } from '@/features/agentops/types'
import { ApiError } from '@/lib/api/errors'
import { useMarkSeen, useSessionMarks } from './use-session-marks'
import './i18n'

const api = vi.hoisted(() => ({
  snooze: vi.fn(),
  unsnooze: vi.fn(),
  markDone: vi.fn(),
  unmarkDone: vi.fn(),
  markUnread: vi.fn(),
  markRead: vi.fn(),
  getRun: vi.fn(),
}))
const toast = vi.hoisted(() => ({
  success: vi.fn(),
  error: vi.fn(),
  warning: vi.fn(),
  dismiss: vi.fn(),
}))
vi.mock('@/features/agentops/api', async (original) => ({
  ...(await original<typeof import('@/features/agentops/api')>()),
  agentOpsApi: api,
}))
vi.mock('@/components/ui/toaster', () => ({ toast }))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't-1' }),
}))

function run(over: Partial<RunDTO> = {}): RunDTO {
  return {
    run_ref: 'run-1',
    name: 'nightly',
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
  <QueryClientProvider client={client}>{children}</QueryClientProvider>
)

beforeEach(() => {
  client = new QueryClient({ defaultOptions: { mutations: { retry: false } } })
  for (const call of Object.values(api))
    call.mockResolvedValue({ run_ref: 'run-1', done: false })
})
afterEach(() => vi.clearAllMocks())

describe('useSessionMarks', () => {
  it('snoozes the run the engine still lists until the moment asked, then re-reads', async () => {
    const invalidate = vi.spyOn(client, 'invalidateQueries')
    const { result } = renderHook(() => useSessionMarks(), { wrapper })
    const before = Date.now()
    act(() =>
      result.current.snooze(
        { runs: [run({ run_ref: 'run-gone', state: 'cleaned' }), run()] },
        7 * 24 * 3_600_000,
      ),
    )
    await waitFor(() => expect(invalidate).toHaveBeenCalled())
    expect(api.snooze).toHaveBeenCalledTimes(1)
    const [ref, until] = api.snooze.mock.calls[0] as [string, string]
    expect(ref).toBe('run-1')
    expect(Date.parse(until) - before).toBeGreaterThanOrEqual(
      7 * 24 * 3_600_000,
    )
  })

  it('clears a mark on every run that carries it', async () => {
    const { result } = renderHook(() => useSessionMarks(), { wrapper })
    const session = {
      runs: [
        run({ run_ref: 'run-a', snoozed_until: '2030-01-02T09:00:00Z' }),
        run({
          run_ref: 'run-b',
          snoozed_until: '2030-01-02T09:00:00Z',
          done: true,
        }),
        run({ run_ref: 'run-c' }),
      ],
    }
    act(() => result.current.unsnooze(session))
    await waitFor(() => expect(api.unsnooze).toHaveBeenCalledTimes(2))
    expect(api.unsnooze.mock.calls.map((c) => c[0])).toEqual(['run-a', 'run-b'])
    act(() => result.current.setDone(session, false))
    await waitFor(() => expect(api.unmarkDone).toHaveBeenCalledWith('run-b'))
    expect(api.unmarkDone).toHaveBeenCalledTimes(1)
  })

  it("says the engine's refusal and re-reads anyway", async () => {
    api.markDone.mockRejectedValue(
      new ApiError(
        409,
        'conflict',
        'a released session cannot be snoozed or marked done',
      ),
    )
    const invalidate = vi.spyOn(client, 'invalidateQueries')
    const { result } = renderHook(() => useSessionMarks(), { wrapper })
    act(() => result.current.setDone({ runs: [run()] }, true))
    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith(
        'a released session cannot be snoozed or marked done',
      ),
    )
    expect(invalidate).toHaveBeenCalled()
  })
})

describe('useSessionMarks — Undo (a notice for 5 seconds, no question first)', () => {
  const UNTIL = '2030-01-02T09:00:00Z'

  /** The notice the last action raised: its text and its Undo. */
  const notice = () => {
    const [text, options] = toast.success.mock.calls.at(-1) as [
      string,
      { duration: number; action: { label: string; onClick: () => void } },
    ]
    return { text, ...options }
  }

  it('snoozes with an Undo that ends the snooze it made', async () => {
    api.snooze.mockResolvedValue({
      run_ref: 'run-1',
      snoozed_until: UNTIL,
      done: false,
    })
    const { result } = renderHook(() => useSessionMarks(), { wrapper })
    act(() => result.current.snooze({ runs: [run()] }, 3_600_000))
    await waitFor(() => expect(toast.success).toHaveBeenCalled())
    expect(notice().text).toBe('Snoozed "nightly"')
    expect(notice().duration).toBe(5000)
    expect(notice().action.label).toBe('Undo')

    api.getRun.mockResolvedValue(run({ snoozed_until: UNTIL }))
    notice().action.onClick()
    await waitFor(() => expect(api.unsnooze).toHaveBeenCalledWith('run-1'))
  })

  it('puts the previous snooze time back when there was one', async () => {
    api.snooze.mockResolvedValue({
      run_ref: 'run-1',
      snoozed_until: UNTIL,
      done: false,
    })
    const before = '2030-01-01T09:00:00Z'
    const { result } = renderHook(() => useSessionMarks(), { wrapper })
    act(() =>
      result.current.snooze(
        { runs: [run({ snoozed_until: before })] },
        3_600_000,
      ),
    )
    await waitFor(() => expect(toast.success).toHaveBeenCalled())
    api.getRun.mockResolvedValue(run({ snoozed_until: UNTIL }))
    notice().action.onClick()
    await waitFor(() => expect(api.snooze).toHaveBeenCalledTimes(2))
    expect(api.snooze).toHaveBeenLastCalledWith('run-1', before)
    expect(api.unsnooze).not.toHaveBeenCalled()
  })

  it('refuses Undo, and changes nothing, when the snooze is no longer the one it made', async () => {
    api.snooze.mockResolvedValue({
      run_ref: 'run-1',
      snoozed_until: UNTIL,
      done: false,
    })
    const { result } = renderHook(() => useSessionMarks(), { wrapper })
    act(() => result.current.snooze({ runs: [run()] }, 3_600_000))
    await waitFor(() => expect(toast.success).toHaveBeenCalled())
    // Another device snoozed it for longer meanwhile.
    api.getRun.mockResolvedValue(run({ snoozed_until: '2030-02-01T09:00:00Z' }))
    notice().action.onClick()
    await waitFor(() =>
      expect(toast.warning).toHaveBeenCalledWith(
        'This session changed; nothing was undone',
      ),
    )
    expect(api.unsnooze).not.toHaveBeenCalled()
  })

  it('marks done with an Undo that marks it not done', async () => {
    api.markDone.mockResolvedValue({ run_ref: 'run-1', done: true })
    const { result } = renderHook(() => useSessionMarks(), { wrapper })
    act(() => result.current.setDone({ runs: [run()] }, true))
    await waitFor(() => expect(toast.success).toHaveBeenCalled())
    expect(notice().text).toBe('Marked "nightly" done')

    api.getRun.mockResolvedValue(run({ done: true }))
    notice().action.onClick()
    await waitFor(() => expect(api.unmarkDone).toHaveBeenCalledWith('run-1'))
  })
})

describe('unread', () => {
  it('marks the run unread, and reads every run that carries the mark', async () => {
    const { result } = renderHook(() => useSessionMarks(), { wrapper })
    act(() => result.current.setUnread({ runs: [run()] }, true))
    await waitFor(() => expect(api.markUnread).toHaveBeenCalledWith('run-1'))
    const session = {
      runs: [
        run({ run_ref: 'run-a', unread: true }),
        run({ run_ref: 'run-b' }),
        run({ run_ref: 'run-c', unread: true }),
      ],
    }
    act(() => result.current.setUnread(session, false))
    await waitFor(() => expect(api.markRead).toHaveBeenCalledTimes(2))
    expect(api.markRead.mock.calls.map((c) => c[0])).toEqual(['run-a', 'run-c'])
  })

  it('marks a session read when it is opened and when it does something new, never after Mark unread alone', async () => {
    const at = (last_activity_at: string) => ({
      runs: [run({ last_activity_at })],
    })
    const { rerender } = renderHook(
      ({ session }) => useMarkSeen(session, true),
      { wrapper, initialProps: { session: at('2026-11-02T10:00:00Z') } },
    )
    await waitFor(() => expect(api.markRead).toHaveBeenCalledWith('run-1'))
    // The same activity read again (the list refetched after Mark unread): nothing to say.
    rerender({
      session: {
        runs: [run({ last_activity_at: '2026-11-02T10:00:00Z', unread: true })],
      },
    })
    expect(api.markRead).toHaveBeenCalledTimes(1)
    // New activity while the session is open is seen too.
    rerender({ session: at('2026-11-02T10:05:00Z') })
    await waitFor(() => expect(api.markRead).toHaveBeenCalledTimes(2))
  })

  it('says nothing while the session cannot be read', () => {
    renderHook(() => useMarkSeen({ runs: [run()] }, false), { wrapper })
    expect(api.markRead).not.toHaveBeenCalled()
  })
})
