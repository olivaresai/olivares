// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The snooze and done marks are the engine's (#2480): a change calls its routes and the
// sessions are re-read afterwards, success or not.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, renderHook, waitFor } from '@testing-library/react'
import type { ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { RunDTO } from '@/features/agentops/types'
import { ApiError } from '@/lib/api/errors'
import { useSessionMarks } from './use-session-marks'

const api = vi.hoisted(() => ({
  snooze: vi.fn(),
  unsnooze: vi.fn(),
  markDone: vi.fn(),
  unmarkDone: vi.fn(),
}))
const toast = vi.hoisted(() => ({ error: vi.fn() }))
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
