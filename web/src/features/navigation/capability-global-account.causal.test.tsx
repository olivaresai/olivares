// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE KNOWN UNSUPPORTED GLOBAL ACCOUNT, MEASURED AS REQUESTS RATHER THAN AS INTENT.
//
// The defect this covers is not a wrong string: it is that a declared question the caller
// can never have answered was submitted anyway, every five seconds, for as long as the
// route stayed on screen. So the property measured here is the REQUEST COUNT through the
// real hook, the real QueryClient and the real transport over twelve seconds — more than
// two whole retry intervals — next to the same twelve seconds for an ordinary member,
// which must keep its cadence exactly as it was.
//
// ⛔ AND THE HAZARD THIS FILE EXISTS FOR: a suppressed question is DECLARED. The route
//    already had a branch that reads "no question" as "nothing to gate" and renders the
//    protected child — correct for the no-workspace selection prompt, and a permit if a
//    suppression reached it. Both halves are measured, on the same view, in the same file.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, renderHook, screen } from '@testing-library/react'
import type { ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const WS = '0192f2c0-aaaa-7000-8000-000000000001'
const CH = '0192f2c0-bbbb-7000-8000-000000000001'
const ADMIN_VIEW = 'communicationsAdministration'
const TENANT = 't1'

const auth = vi.hoisted(() => ({
  superadmin: false,
  perms: new Set<string>(),
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    can: (p: string) => auth.perms.has(p),
    isSuperadmin: auth.superadmin,
    principal: {
      kind: 'user',
      user_id: '0192f2c0-eeee-7000-8000-00000000000a',
      actor: 'user:0192f2c0-eeee-7000-8000-00000000000a',
      superadmin: auth.superadmin,
    },
    activeTenant: TENANT,
  }),
}))

const routerSearch = vi.hoisted(() => ({ value: '' }))
vi.mock('@tanstack/react-router', () => ({
  useRouterState: ({ select }: { select: (s: unknown) => unknown }) =>
    select({ location: { searchStr: routerSearch.value, pathname: '/x' } }),
}))

import { RequirePermission } from '@/components/layout/require-permission'
import { configureApiClient, __resetRefreshState } from '@/lib/api/client'
import { queryKeys } from '@/lib/api/query'
import { CAPABILITY_RETRY_MS } from '@/lib/auth/capabilities'
import { FEATURE_VIEWS, type FeatureView } from '@/features/registry'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import { useWorkspaceStore } from '@/stores/workspace'
import {
  useRouteAccess,
  useViewAccess,
  type RouteAccess,
} from './authorization'

const view = (id: string): FeatureView => {
  const found = FEATURE_VIEWS.find((v) => v.id === id)
  if (!found) throw new Error(`no such view: ${id}`)
  return found
}
const ADMINISTRATION = view(ADMIN_VIEW)
/** A view whose authority is the whoami reflection: it declares no question at all. */
const REFLECTED_VIEW = FEATURE_VIEWS.find(
  (v) => v.capability === undefined && !!v.permission,
)!

const clients: QueryClient[] = []
function mounted() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  clients.push(client)
  client.setQueryData(queryKeys.whoami, {
    kind: 'user',
    user_id: '0192f2c0-eeee-7000-8000-00000000000a',
    actor: 'user:0192f2c0-eeee-7000-8000-00000000000a',
    superadmin: auth.superadmin,
  })
  const Wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  )
  return { client, Wrapper }
}

async function drain() {
  for (let i = 0; i < 4; i++)
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1)
    })
}
/** Twelve seconds: more than two whole visible retry intervals. */
async function twelveSeconds() {
  for (let i = 0; i < 12; i++)
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1_000)
    })
}
const refused = () =>
  new Response(JSON.stringify({ code: 'route_decision_unavailable' }), {
    status: 503,
    headers: { 'Content-Type': 'application/json' },
  })

function route(): RouteAccess {
  const { Wrapper } = mounted()
  const hook = renderHook(
    () => useRouteAccess(ADMINISTRATION, routerSearch.value),
    { wrapper: Wrapper },
  )
  return hook.result.current
}

beforeEach(() => {
  auth.superadmin = false
  auth.perms = new Set<string>()
  routerSearch.value = ''
  useWorkspaceStore.setState({ activeWorkspace: WS })
  useTenantStore.setState({ activeTenant: TENANT })
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
  for (const c of clients.splice(0)) c.clear()
  __resetRefreshState()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

describe('the global account asks nothing, and that is not a permit', () => {
  it('submits NO capability request across twelve seconds', async () => {
    vi.useFakeTimers()
    auth.superadmin = true
    const fetchSpy = vi.fn(async () => refused())
    vi.stubGlobal('fetch', fetchSpy)
    const { Wrapper } = mounted()
    const hook = renderHook(
      () => useRouteAccess(ADMINISTRATION, routerSearch.value),
      { wrapper: Wrapper },
    )
    await drain()
    await twelveSeconds()

    expect(fetchSpy).toHaveBeenCalledTimes(0)
    expect(hook.result.current.kind).toBe('unavailable')
    expect(hook.result.current.globalAccount).toBe(true)
    // UNKNOWN is retained and nothing was asked: no answer, no submitted question.
    expect(hook.result.current.observed).toBe('unknown')
    expect(hook.result.current.question).toBeNull()
    hook.unmount()
  })

  it('CONTROL: an ordinary member keeps its cadence over the same twelve seconds', async () => {
    vi.useFakeTimers()
    const fetchSpy = vi.fn(async () => refused())
    vi.stubGlobal('fetch', fetchSpy)
    const { Wrapper } = mounted()
    const hook = renderHook(
      () => useRouteAccess(ADMINISTRATION, routerSearch.value),
      { wrapper: Wrapper },
    )
    await drain()
    const first = fetchSpy.mock.calls.length
    await twelveSeconds()

    expect(first).toBe(1)
    // Twelve seconds spans two whole intervals of the one visible cadence.
    expect(fetchSpy.mock.calls.length).toBeGreaterThanOrEqual(
      1 + Math.floor(12_000 / CAPABILITY_RETRY_MS),
    )
    expect(hook.result.current.kind).toBe('unavailable')
    expect(hook.result.current.globalAccount).toBe(false)
    hook.unmount()
  })

  it('never mounts the protected child, and says which account can ask', async () => {
    vi.useFakeTimers()
    auth.superadmin = true
    const fetchSpy = vi.fn(async () => refused())
    vi.stubGlobal('fetch', fetchSpy)
    const { Wrapper } = mounted()
    render(
      <Wrapper>
        <RequirePermission view={ADMINISTRATION}>
          <div data-slot="protected-child">the collection</div>
        </RequirePermission>
      </Wrapper>,
    )
    await drain()
    await twelveSeconds()

    expect(document.querySelector('[data-slot="protected-child"]')).toBeNull()
    expect(
      document.querySelector('[data-slot="capability-global-account"]'),
    ).not.toBeNull()
    // It is not rendered as a refusal, and it promises no retry it will not make.
    expect(document.querySelector('[data-slot="capability-retry"]')).toBeNull()
    expect(screen.queryByRole('heading', { name: /forbidden/i })).toBeNull()
    expect(fetchSpy).toHaveBeenCalledTimes(0)
  })

  it('a valid entity deep link is suppressed for the same family, and asks nothing', async () => {
    vi.useFakeTimers()
    auth.superadmin = true
    routerSearch.value = `?admin_channel=${CH}`
    const fetchSpy = vi.fn(async () => refused())
    vi.stubGlobal('fetch', fetchSpy)
    const { Wrapper } = mounted()
    const hook = renderHook(
      () => useRouteAccess(ADMINISTRATION, routerSearch.value),
      { wrapper: Wrapper },
    )
    await drain()
    await twelveSeconds()

    expect(fetchSpy).toHaveBeenCalledTimes(0)
    expect(hook.result.current.kind).toBe('unavailable')
    expect(hook.result.current.globalAccount).toBe(true)
    hook.unmount()
  })

  it('NO-WORKSPACE IS NOT SUPPRESSION: the selection prompt still renders', async () => {
    vi.useFakeTimers()
    auth.superadmin = true
    useWorkspaceStore.setState({ activeWorkspace: null })
    const fetchSpy = vi.fn(async () => refused())
    vi.stubGlobal('fetch', fetchSpy)
    const decision = route()
    await drain()

    // The pre-existing case: nothing was DECLARED, so there is no protected child to
    // gate and the room keeps rendering its own prompt.
    expect(decision.kind).toBe('permitted')
    expect(decision.globalAccount).toBe(false)
    expect(decision.question).toBeNull()
    expect(fetchSpy).toHaveBeenCalledTimes(0)
  })

  it('a reflection-only route is unchanged in both directions', async () => {
    vi.useFakeTimers()
    auth.superadmin = true
    const fetchSpy = vi.fn(async () => refused())
    vi.stubGlobal('fetch', fetchSpy)
    const { Wrapper } = mounted()
    const hook = renderHook(
      () => useRouteAccess(REFLECTED_VIEW, routerSearch.value),
      { wrapper: Wrapper },
    )
    await drain()
    expect(hook.result.current.kind).toBe('forbidden')
    expect(hook.result.current.globalAccount).toBe(false)

    auth.perms = new Set([REFLECTED_VIEW.permission!])
    hook.rerender()
    await drain()
    expect(hook.result.current.kind).toBe('permitted')
    expect(fetchSpy).toHaveBeenCalledTimes(0)
    hook.unmount()
  })
})

describe('the account moves and the rendered authority moves with it', () => {
  it('global → member reobserves in the current context; member → global drops it at once', async () => {
    vi.useFakeTimers()
    auth.superadmin = true
    const fetchSpy = vi.fn(
      async () =>
        new Response(
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
        ),
    )
    vi.stubGlobal('fetch', fetchSpy)
    const { Wrapper } = mounted()
    const hook = renderHook(
      () => useRouteAccess(ADMINISTRATION, routerSearch.value),
      { wrapper: Wrapper },
    )
    await drain()
    expect(fetchSpy).toHaveBeenCalledTimes(0)
    expect(hook.result.current.kind).toBe('unavailable')

    // The member signs in: the declared question is submitted in the CURRENT context and
    // the answer it gets is the member's own.
    auth.superadmin = false
    hook.rerender()
    await drain()
    expect(fetchSpy.mock.calls.length).toBeGreaterThanOrEqual(1)
    expect(hook.result.current.kind).toBe('permitted')

    // And back: the positive that was on screen is dropped in the same render, without
    // waiting for an answer that is never going to be asked for.
    const asked = fetchSpy.mock.calls.length
    auth.superadmin = true
    hook.rerender()
    await drain()
    expect(hook.result.current.kind).toBe('unavailable')
    expect(hook.result.current.globalAccount).toBe(true)
    expect(hook.result.current.observed).toBe('unknown')
    expect(fetchSpy.mock.calls.length).toBe(asked)
    hook.unmount()
  })

  it('navigation keeps the installed link and reports UNKNOWN, asking nothing', async () => {
    vi.useFakeTimers()
    auth.superadmin = true
    const fetchSpy = vi.fn(async () => refused())
    vi.stubGlobal('fetch', fetchSpy)
    const { Wrapper } = mounted()
    const hook = renderHook(() => useViewAccess(), { wrapper: Wrapper })
    await drain()
    await twelveSeconds()

    expect(hook.result.current.state(ADMINISTRATION)).toBe('unknown')
    // Unknown is not an established refusal, so the table of contents is unchanged.
    expect(hook.result.current.navigable(ADMINISTRATION)).toBe(true)
    expect(fetchSpy).toHaveBeenCalledTimes(0)
    hook.unmount()
  })
})
