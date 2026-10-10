// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient } from '@tanstack/react-query'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { configureApiClient, __resetRefreshState } from '@/lib/api/client'
import { queryKeys } from '@/lib/api/query'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import { createStepUpOwner, type StepUpOwner } from '@/stores/step-up'
import {
  sentTurnsPartition,
  useSentTurns,
} from '@/features/sessions/sent-turns'
import { launchSession } from './session-launch'
import type { CreateRunRequest } from './types'

let owner: StepUpOwner
let qc: QueryClient
const reply = (status: number) =>
  new Response(
    JSON.stringify(
      status === 202
        ? { accepted: true }
        : { error: { code: 'conflict', message: 'Child starting' } },
    ),
    { status, headers: { 'Content-Type': 'application/json' } },
  )
const created = (driver: string) =>
  new Response(JSON.stringify({ run_ref: 'run_1', provider_driver: driver }), {
    status: 201,
    headers: { 'Content-Type': 'application/json' },
  })
const run: CreateRunRequest = {
  name: '',
  transport: 'stream-json',
  permission_mode: 'default',
  effort: '',
  workspace_ref: '',
  isolation: 'native',
  env_allow: [],
  provider_profile_ref: 'ppf_a',
}

beforeEach(() => {
  vi.useFakeTimers()
  useTenantStore.setState({ activeTenant: 'tenant-a' })
  useSessionStore.setState({ credentialGeneration: 1 })
  useSentTurns.setState({ byRun: {} })
  qc = new QueryClient()
  qc.setQueryData(queryKeys.whoami, { user_id: 'operator-a' })
  owner = createStepUpOwner(qc)
  configureApiClient({
    getToken: () => null,
    getTenant: () => useTenantStore.getState().activeTenant,
    getExpiresAt: undefined,
    refreshSession: undefined,
    onUnauthorized: () => {},
  })
})

afterEach(() => {
  owner.retire()
  qc.clear()
  vi.useRealTimers()
  vi.unstubAllGlobals()
  configureApiClient({
    getToken: () => null,
    getTenant: () => null,
    getExpiresAt: undefined,
    refreshSession: undefined,
  })
  __resetRefreshState()
})

/** Runs what is due now, without moving the clock past a retry, until `n` requests left. */
async function untilDispatched(fetch: ReturnType<typeof vi.fn>, n: number) {
  for (let i = 0; i < 50 && fetch.mock.calls.length < n; i++)
    await vi.advanceTimersByTimeAsync(0)
}

describe.each(['claude', 'codex'])(
  'a %s launch whose authority retires while its first message is retried',
  (driver) => {
    const launch = (attempt: ReturnType<StepUpOwner['begin']>) =>
      launchSession(
        { run, message: 'original prompt' },
        {
          signal: attempt.signal,
          dispatchGuard: attempt.dispatchGuard,
          tenant: owner.tenant,
        },
      )

    it.each(['credential', 'tenant', 'principal', 'attempt'])(
      'stops retries after the %s changes following a failed input',
      async (change) => {
        const attempt = owner.begin()
        const fetch = vi
          .fn()
          .mockResolvedValueOnce(created(driver))
          .mockResolvedValueOnce(reply(409))
          .mockImplementation(async () => reply(202))
        vi.stubGlobal('fetch', fetch)
        // Observe rejection before advancing timers, so a prompt cancellation is handled.
        const result = launch(attempt).then(
          () => 'sent',
          (err: Error) => err.name,
        )
        await untilDispatched(fetch, 2)
        // The run, then its first input, which the child refused while starting.
        expect(fetch).toHaveBeenCalledTimes(2)
        for (const [, init] of fetch.mock.calls as [string, RequestInit][])
          expect(new Headers(init.headers).get('X-Olivares-Tenant')).toBe(
            'tenant-a',
          )
        if (change === 'credential')
          useSessionStore.setState({ credentialGeneration: 2 })
        if (change === 'tenant')
          useTenantStore.setState({ activeTenant: 'tenant-b' })
        if (change === 'principal')
          qc.setQueryData(queryKeys.whoami, { user_id: 'operator-b' })
        if (change === 'attempt') owner.begin()
        await vi.runAllTimersAsync()
        expect(fetch).toHaveBeenCalledTimes(2)
        expect(await result).toBe('AbortError')
        expect(useSentTurns.getState().byRun).toEqual({})
        expect((fetch.mock.calls[1][1] as RequestInit).signal).toBe(
          attempt.signal,
        )
      },
    )

    it('retries and notes the accepted message while its owner remains current', async () => {
      const attempt = owner.begin()
      const fetch = vi
        .fn()
        .mockResolvedValueOnce(created(driver))
        .mockResolvedValueOnce(reply(409))
        .mockResolvedValueOnce(reply(202))
      vi.stubGlobal('fetch', fetch)
      const sent = launch(attempt)
      await vi.runAllTimersAsync()
      await sent
      expect(fetch).toHaveBeenCalledTimes(3)
      expect(useSentTurns.getState().byRun.run_1).toEqual([
        { partition: sentTurnsPartition(), text: 'original prompt' },
      ])
    })

    it('blocks dispatch if authority changes while the HTTP client refreshes', async () => {
      const attempt = owner.begin()
      configureApiClient({
        getExpiresAt: () => new Date(Date.now() + 1000).toISOString(),
        refreshSession: async () => {
          useSessionStore.setState({ credentialGeneration: 2 })
          return true
        },
      })
      const fetch = vi.fn().mockImplementation(async () => created(driver))
      vi.stubGlobal('fetch', fetch)
      await expect(launch(attempt)).rejects.toMatchObject({
        name: 'AbortError',
      })
      expect(fetch).not.toHaveBeenCalled()
      expect(useSentTurns.getState().byRun).toEqual({})
    })

    it('blocks the first message if authority changes while the HTTP client refreshes after the run', async () => {
      const attempt = owner.begin()
      // The run is created; the client then refreshes before the first input, and the
      // refresh moves the credential.
      const fetch = vi.fn().mockImplementation(async () => {
        configureApiClient({
          getExpiresAt: () => new Date(Date.now() + 1000).toISOString(),
          refreshSession: async () => {
            useSessionStore.setState({ credentialGeneration: 2 })
            return true
          },
        })
        return created(driver)
      })
      vi.stubGlobal('fetch', fetch)
      const result = launch(attempt).then(
        () => 'sent',
        (err: Error) => err.name,
      )
      await vi.runAllTimersAsync()
      expect(await result).toBe('AbortError')
      expect(fetch).toHaveBeenCalledTimes(1)
      expect(useSentTurns.getState().byRun).toEqual({})
    })
  },
)
