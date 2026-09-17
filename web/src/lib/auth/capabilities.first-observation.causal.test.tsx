// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { StrictMode, type ReactNode } from 'react'
import { renderToString } from 'react-dom/server'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, cleanup, renderHook } from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { configureApiClient, __resetRefreshState } from '@/lib/api/client'
import { queryKeys } from '@/lib/api/query'
import { useWorkspaceStore } from '@/stores/workspace'
import { useTenantStore } from '@/stores/tenant'
import { useSessionStore } from '@/stores/session'
import {
  capabilityQuestion,
  createCapabilityPermit,
  liveCapabilityContext,
  useCapability,
  useCapabilityPreflight,
} from './capabilities'

const principal = vi.hoisted(() => ({
  kind: 'user',
  actor: 'user:first-observation-a',
  user_id: 'first-observation-a',
  memberships: [],
}))
// The final rendered principal is deliberately unchanged. The real QueryClient sees
// both writes; no render of the intermediate principal may be needed to invalidate A.
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ principal, can: () => false }),
}))

const WORKSPACE = '0192f2c0-aaaa-7000-8000-000000000001'
const question = capabilityQuestion({
  kind: 'surface',
  operation: 'GET /v1/m/sessions/channels/administration',
  workspaceId: WORKSPACE,
})
const clients: QueryClient[] = []
function mounted() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  clients.push(client)
  client.setQueryData(queryKeys.whoami, principal)
  const Wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  )
  return { client, Wrapper }
}
function roundTrip(client: QueryClient) {
  client.setQueryData(queryKeys.whoami, {
    ...principal,
    actor: 'user:first-observation-b',
    user_id: 'first-observation-b',
  })
  client.setQueryData(queryKeys.whoami, principal)
}
function positive() {
  return new Response(
    JSON.stringify({
      schema_version: 2,
      results: [
        {
          id: 'q',
          kind: 'surface',
          state: 'reachable',
          code: 'admitted',
          refresh_after_ms: 30_000,
        },
      ],
    }),
    { status: 200, headers: { 'Content-Type': 'application/json' } },
  )
}
function suspendedTransport() {
  const pending: Array<(response: Response) => void> = []
  const fetch = vi.fn(
    () => new Promise<Response>((resolve) => pending.push(resolve)),
  )
  vi.stubGlobal('fetch', fetch)
  return { fetch, pending }
}
async function drain() {
  for (let i = 0; i < 4; i++)
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1)
    })
}

beforeEach(() => {
  vi.useFakeTimers()
  useWorkspaceStore.setState({ activeWorkspace: WORKSPACE })
  useTenantStore.setState({ activeTenant: 'first-observation' })
  useSessionStore.setState({ credentialGeneration: 0 })
  configureApiClient({
    getToken: () => null,
    getTenant: () => null,
    onUnauthorized: () => {},
    refreshSession: undefined,
    getExpiresAt: undefined,
  })
})
afterEach(() => {
  cleanup()
  for (const client of clients.splice(0)) client.clear()
  __resetRefreshState()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

it('rejects the first suspended answer after principal A → B → A, without priming', async () => {
  const { client, Wrapper } = mounted()
  const { fetch, pending } = suspendedTransport()
  const hook = renderHook(() => useCapability(question), { wrapper: Wrapper })
  await drain()
  expect(fetch).toHaveBeenCalledTimes(1)
  expect(hook.result.current.permit).toBeNull()
  const oldKey = client
    .getQueryCache()
    .findAll({ queryKey: ['auth-capabilities'] })
    .find((query) => query.state.fetchStatus === 'fetching')!.queryKey

  await act(async () => {
    roundTrip(client)
    pending[0](positive())
  })
  await drain()
  expect(hook.result.current.access).toBe('checking')
  expect(hook.result.current.permit).toBeNull()
  expect(fetch).toHaveBeenCalledTimes(2)
  expect(
    client.getQueryCache().find({ queryKey: oldKey, exact: true }),
  ).toBeUndefined()

  // The replacement answer is a real positive control, not a permanently closed gate.
  await act(async () => pending[1](positive()))
  await drain()
  expect(hook.result.current.access).toBe('reachable')
  expect(hook.result.current.permit?.isCurrent()).toBe(true)
  hook.unmount()
})

it('keeps repeated identical whoami reads stable and the first answer usable', async () => {
  const { client, Wrapper } = mounted()
  const fetch = vi.fn(async () => positive())
  vi.stubGlobal('fetch', fetch)
  const hook = renderHook(() => useCapability(question), { wrapper: Wrapper })
  await drain()
  const permit = hook.result.current.permit
  expect(permit?.isCurrent()).toBe(true)
  await act(async () => {
    for (let i = 0; i < 3; i++) {
      client.setQueryData(queryKeys.whoami, { ...principal })
      expect(liveCapabilityContext(client)).toEqual(permit?.context)
    }
  })
  hook.rerender()
  await drain()
  expect(permit?.isCurrent()).toBe(true)
  expect(fetch).toHaveBeenCalledTimes(1)
  hook.unmount()
})

it('isolates principal history between two live QueryClients', async () => {
  const a = mounted()
  const b = mounted()
  const fetch = vi.fn(async () => positive())
  vi.stubGlobal('fetch', fetch)
  const first = renderHook(() => useCapability(question), {
    wrapper: a.Wrapper,
  })
  const second = renderHook(() => useCapability(question), {
    wrapper: b.Wrapper,
  })
  await drain()
  const beforeA = first.result.current.permit
  const beforeB = second.result.current.permit
  expect(beforeA?.isCurrent()).toBe(true)
  expect(beforeB?.isCurrent()).toBe(true)
  expect(fetch).toHaveBeenCalledTimes(2)
  await act(async () => roundTrip(a.client))
  await drain()
  expect(beforeA?.isCurrent()).toBe(false)
  expect(first.result.current.permit?.isCurrent()).toBe(true)
  expect(beforeB?.isCurrent()).toBe(true)
  expect(second.result.current.permit).toBe(beforeB)
  expect(fetch).toHaveBeenCalledTimes(3)
  first.unmount()
  second.unmount()
})

it('installs no cache observer and captures no authority in an uncommitted render', () => {
  const { client, Wrapper } = mounted()
  const subscribe = vi.spyOn(client.getQueryCache(), 'subscribe')
  const fetch = vi.fn()
  vi.stubGlobal('fetch', fetch)
  function Probe() {
    const observation = useCapability(question)
    const preflight = useCapabilityPreflight()
    expect(observation.permit).toBeNull()
    expect(preflight.context).toBeNull()
    expect(preflight.live()).toBeNull()
    return null
  }
  renderToString(
    <Wrapper>
      <Probe />
    </Wrapper>,
  )
  expect(subscribe).not.toHaveBeenCalled()
  expect(fetch).not.toHaveBeenCalled()
})

it('preserves principal history between StrictMode mounts for a saved permit', async () => {
  const { client, Wrapper } = mounted()
  const subscribe = vi.spyOn(client.getQueryCache(), 'subscribe')
  const fetch = vi.fn(async () => positive())
  vi.stubGlobal('fetch', fetch)
  const StrictWrapper = ({ children }: { children: ReactNode }) => (
    <StrictMode>
      <Wrapper>{children}</Wrapper>
    </StrictMode>
  )
  const hook = renderHook(
    () => ({
      observation: useCapability(question),
      preflight: useCapabilityPreflight(),
    }),
    { wrapper: StrictWrapper },
  )
  await drain()
  const saved = hook.result.current.observation.permit
  expect(saved?.isCurrent()).toBe(true)
  hook.unmount()
  // No component exists and the permit is not read between the two writes.
  roundTrip(client)
  expect(saved?.isCurrent()).toBe(false)
  const remount = renderHook(() => useCapability(question), {
    wrapper: StrictWrapper,
  })
  await drain()
  expect(remount.result.current.permit?.isCurrent()).toBe(true)
  expect(subscribe).toHaveBeenCalledTimes(1)
  remount.unmount()
})

it('arms a standalone preflight before its first request and refuses a stale answer', async () => {
  const { client, Wrapper } = mounted()
  const { fetch, pending } = suspendedTransport()
  const hook = renderHook(() => useCapabilityPreflight(), { wrapper: Wrapper })
  expect(hook.result.current.context).not.toBeNull()
  const before = hook.result.current.context
  const stale = hook.result.current.request(question)
  expect(fetch).toHaveBeenCalledTimes(1)
  await act(async () => {
    roundTrip(client)
    pending[0](positive())
  })
  expect(await stale).toBeNull()
  expect(hook.result.current.context?.lifetime).toBeGreaterThan(
    before!.lifetime,
  )
  const fresh = hook.result.current.request(question)
  await act(async () => pending[1](positive()))
  expect((await fresh)?.isCurrent()).toBe(true)
  hook.unmount()
})

it('preserves direct imperative context capture without a React consumer', () => {
  const { client } = mounted()
  const live = () => liveCapabilityContext(client)
  const context = live()!
  const permit = createCapabilityPermit(
    context,
    question,
    performance.now() + 1000,
    live,
  )
  expect(live()).toEqual(context)
  expect(permit.isCurrent()).toBe(true)
  roundTrip(client)
  expect(live()!.lifetime).toBeGreaterThan(context.lifetime)
  expect(permit.isCurrent()).toBe(false)
  const replacement = createCapabilityPermit(
    live()!,
    question,
    performance.now() + 1000,
    live,
  )
  expect(replacement.isCurrent()).toBe(true)
})
