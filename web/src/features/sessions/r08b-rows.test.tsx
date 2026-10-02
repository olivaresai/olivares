// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// HU-R24 on the engine's real 08b rows (r08b-rows.fixture.ts): five running sessions are 15
// live rows. Now must count five, and a session's evidence must read the rows its hook and its
// turn usage were written to.
import type { ReactNode } from 'react'
import { QueryClientProvider } from '@tanstack/react-query'
import { renderHook, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createTestQueryClient } from '@/test/intel'
import { deriveUsage } from '@/features/executive/derive'
import { liveForTiles } from '@/features/home/home-view'
import { mergeSessions } from './provenance'
import {
  r08bCliKey,
  r08bLive,
  r08bRuns,
  r08bStoppedRun,
} from './r08b-rows.fixture'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))
vi.mock('@/features/shared', async (original) => ({
  ...(await original<typeof import('@/features/shared')>()),
  useLiveStream: () => ({ status: 'open' }),
}))
const api = vi.hoisted(() => ({
  liveById: vi.fn(),
  liveOne: vi.fn(),
  live: vi.fn(),
  getRun: vi.fn(),
  listRuns: vi.fn(),
}))
vi.mock('./api', async (original) => {
  const real = await original<typeof import('./api')>()
  return {
    ...real,
    sessionsApi: {
      ...real.sessionsApi,
      liveById: api.liveById,
      liveOne: api.liveOne,
      live: api.live,
    },
  }
})
vi.mock('@/features/agentops/api', async (original) => {
  const real = await original<typeof import('@/features/agentops/api')>()
  return {
    ...real,
    agentOpsApi: {
      ...real.agentOpsApi,
      getRun: api.getRun,
      listRuns: api.listRuns,
    },
  }
})

import { useSessionResolution } from './use-session-resolution'

const cliRun = r08bRuns.find((r) => r.name === 'cli-key')!
const byLiveRef = new Map(r08bLive.map((l) => [l.live_ref, l]))
const legacyBySessionRef = new Map(
  r08bLive
    .filter((l) => l.attribution === 'legacy')
    .map((l) => [l.session_ref, l]),
)

describe('08b real rows: five sessions, fifteen live rows (HU-R24)', () => {
  it('fold into five sessions, each with its hook row and its usage row', () => {
    const sessions = mergeSessions(r08bLive, r08bRuns)
    expect(sessions).toHaveLength(5)
    for (const s of sessions) {
      expect(s.live?.attribution).toBe('managed')
      expect(s.echoes?.map((e) => e.session_ref).sort()).toEqual(
        [s.live!.run_ref, s.live!.canonical_sid].sort(),
      )
    }
  })

  it('Now counts five live sessions, and four after one is stopped', () => {
    const live = { items: r08bLive, has_more: false }
    const now = deriveUsage(
      undefined,
      liveForTiles(live, mergeSessions(r08bLive, r08bRuns)),
    )
    expect(now.liveNow).toBe(5)
    const runs = r08bRuns.map((r) =>
      r.run_ref === r08bStoppedRun.run_ref ? r08bStoppedRun : r,
    )
    const after = deriveUsage(
      undefined,
      liveForTiles(live, mergeSessions(r08bLive, runs)),
    )
    expect(after.liveNow).toBe(4)
  })

  it('a session idle between turns is still a live session', () => {
    const idle = r08bLive.map((l) =>
      l.attribution === 'managed' && l.run_ref !== cliRun.run_ref
        ? { ...l, cc_state: 'idle' }
        : l,
    )
    const now = deriveUsage(
      undefined,
      liveForTiles(
        { items: idle, has_more: false },
        mergeSessions(idle, r08bRuns),
      ),
    )
    expect(now.liveNow).toBe(5)
    expect(now.liveIdle).toBe(4)
  })
})

describe('the open session reads its echo rows (HU-R24: Last turns 0)', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    api.getRun.mockResolvedValue(cliRun)
    api.listRuns.mockResolvedValue({ items: [cliRun], has_more: false })
    api.liveById.mockImplementation(async (ref: string) => byLiveRef.get(ref))
    // The list route filtered by session_ref answers 200 with the rows it has (none yet
    // for a session whose hook has not written): the card never reads a bare id (FH 057).
    api.live.mockImplementation(async (params?: { session_ref?: string }) => {
      const row = params?.session_ref
        ? legacyBySessionRef.get(params.session_ref)
        : undefined
      return { items: row ? [row] : [], has_more: false }
    })
  })

  it('the card opened from cli-key’s run carries its two echo rows', async () => {
    const client = createTestQueryClient()
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    )
    const { result } = renderHook(
      () => useSessionResolution({ runRef: cliRun.run_ref }),
      { wrapper },
    )
    await waitFor(() =>
      expect(
        result.current.session.echoes?.map((e) => e.live_ref).sort(),
      ).toEqual([r08bCliKey.echoByRun, r08bCliKey.echoByCanonical].sort()),
    )
    expect(result.current.session.live?.live_ref).toBe(r08bCliKey.managed)
    expect(api.live).toHaveBeenCalledWith(
      expect.objectContaining({ session_ref: cliRun.run_ref }),
    )
    expect(api.liveOne).not.toHaveBeenCalled()
  })

  it('a session with no hook or usage row yet reads an empty list, never a 404 (FH 057)', async () => {
    // FH 057 on 09: GET /v1/m/sessions/live/<run_ref> answered 404 for a session whose hook
    // had not written; the same session answered 200 by its other routes.
    api.live.mockResolvedValue({ items: [], has_more: false })
    const client = createTestQueryClient()
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    )
    const { result } = renderHook(
      () => useSessionResolution({ runRef: cliRun.run_ref }),
      { wrapper },
    )
    await waitFor(() =>
      expect(api.live).toHaveBeenCalledWith(
        expect.objectContaining({ session_ref: cliRun.run_ref }),
      ),
    )
    await waitFor(() =>
      expect(result.current.session.live?.live_ref).toBe(r08bCliKey.managed),
    )
    expect(result.current.session.echoes ?? []).toEqual([])
    expect(api.liveOne).not.toHaveBeenCalled()
  })
})
