// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A NEW SESSION GETS ITS TITLE FROM ITS FIRST MESSAGE: who is asked, when, and how often.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { renderHook, waitFor } from '@testing-library/react'
import type { ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { RunDTO } from '@/features/agentops/types'
import { usePreferencesStore } from '@/stores/preferences'
import { AUTO_TITLE_RETRY_MS, useAutoTitle } from './use-auto-title'

const auth = vi.hoisted(() => ({
  activeTenant: 'tnt-demo' as string | null,
  can: (_: string): boolean => true,
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => auth }))

const api = vi.hoisted(() => ({ generateTitle: vi.fn() }))
vi.mock('@/features/agentops/api', async (importOriginal) => {
  const real = await importOriginal<typeof import('@/features/agentops/api')>()
  return {
    ...real,
    agentOpsApi: { ...(real.agentOpsApi as object), ...api },
  }
})

function run(over: Partial<RunDTO> = {}): RunDTO {
  return {
    run_ref: 'run-auto',
    transport: 'stream-json',
    permission_mode: 'default',
    isolation: 'native',
    state: 'running',
    last_event_seq: 1,
    last_activity_at: '2026-10-29T10:00:00Z',
    pep_provisioned: true,
    record_io: false,
    critical: false,
    ...over,
  }
}

function setup(initial: RunDTO | undefined) {
  const qc = new QueryClient()
  const invalidate = vi.spyOn(qc, 'invalidateQueries')
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={qc}>{children}</QueryClientProvider>
  )
  const hook = renderHook(({ r }) => useAutoTitle(r), {
    wrapper,
    initialProps: { r: initial },
  })
  return { ...hook, invalidate }
}

// The hook remembers when it last asked about each session, for the whole page: each
// test names its own so none inherits another's wait.
let n = 0
const fresh = (over: Partial<RunDTO> = {}) =>
  run({ run_ref: `run-auto-${++n}`, ...over })

beforeEach(() => {
  vi.resetAllMocks()
  auth.activeTenant = 'tnt-demo'
  auth.can = () => true
  api.generateTitle.mockResolvedValue(run({ title: 'made' }))
  usePreferencesStore.setState({ titleStyle: 'sentence' })
})
afterEach(() => vi.useRealTimers())

describe('useAutoTitle', () => {
  it('asks once for a session with neither a name nor a title, in the kept style', async () => {
    usePreferencesStore.setState({ titleStyle: 'short' })
    const r = fresh()
    const { invalidate } = setup(r)
    await waitFor(() =>
      expect(api.generateTitle).toHaveBeenCalledWith(r.run_ref, {
        style: 'short',
        if_untitled: true,
      }),
    )
    // The title shows once the list is read again.
    await waitFor(() => expect(invalidate).toHaveBeenCalled())
    expect(api.generateTitle).toHaveBeenCalledTimes(1)
  })

  it('leaves a session that has a name or a title, or no run, alone', () => {
    setup(fresh({ name: 'deploy-api' }))
    setup(fresh({ title: 'Fix the login bug' }))
    setup(undefined)
    expect(api.generateTitle).not.toHaveBeenCalled()
    // A blank name and a blank title are neither.
    setup(fresh({ name: '  ', title: '  ' }))
    expect(api.generateTitle).toHaveBeenCalledTimes(1)
  })

  it('does not ask for a person who may not write the session or not read its output', () => {
    auth.can = (permission) => permission !== 'sessions:run:write'
    setup(fresh())
    auth.can = (permission) => permission !== 'sessions:output:read'
    setup(fresh())
    expect(api.generateTitle).not.toHaveBeenCalled()
  })

  it('waits before asking about the same session again, then asks once more', () => {
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(new Date('2026-10-29T10:00:00Z'))
    const r = fresh()
    const { rerender } = setup(r)
    expect(api.generateTitle).toHaveBeenCalledTimes(1)
    // More activity right after: the engine said there was nothing to cut from yet, and
    // it is not asked again at every poll.
    rerender({ r: { ...r, last_activity_at: '2026-10-29T10:00:05Z' } })
    expect(api.generateTitle).toHaveBeenCalledTimes(1)
    vi.setSystemTime(Date.now() + AUTO_TITLE_RETRY_MS + 1)
    rerender({ r: { ...r, last_activity_at: '2026-10-29T10:01:00Z' } })
    expect(api.generateTitle).toHaveBeenCalledTimes(2)
  })

  it('takes a refusal quietly: the menu item is where a person is told why', async () => {
    api.generateTitle.mockRejectedValue(new Error('no message yet'))
    const { invalidate } = setup(fresh())
    await waitFor(() => expect(api.generateTitle).toHaveBeenCalledTimes(1))
    // Nothing thrown, and nothing refreshed for a title that did not come.
    await Promise.resolve()
    expect(invalidate).not.toHaveBeenCalled()
  })
})
