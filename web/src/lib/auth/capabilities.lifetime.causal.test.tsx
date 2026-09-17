// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE LOCAL CONTEXT LIFETIME, THE FINITE CADENCE AND THE SENTENCE THAT CLOSES AN ACT —
// with the REAL hook, a REAL QueryClient, the REAL shared transport, the real intent
// guard and the real administration form. No capability double appears in this file: the
// three defects it covers all lived in the seam BETWEEN those pieces, which is exactly
// the seam a double replaces.
//
// The stimulus that matters is a ROUND TRIP inside one React batch — workspace A → B → A
// with no commit in between. Every value comparison in the module is blind to it, and on
// the frozen source it revived an in-flight answer, revived a saved permit and let one
// real PATCH leave under a withdrawn authority.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  act,
  render,
  renderHook,
  screen,
  waitFor,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ReactNode } from 'react'

const principal = vi.hoisted(() => ({
  kind: 'user',
  actor: 'user:0192f2c0-eeee-7000-8000-00000000000a',
  user_id: '0192f2c0-eeee-7000-8000-00000000000a',
  display_name: 'A',
  superadmin: false,
  grants: [] as unknown[],
}))
const notices = vi.hoisted(() => ({
  warning: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
}))
vi.mock('@/components/ui/toaster', () => ({
  toast: notices,
  Toaster: () => null,
}))
// `useAuth` requires its provider; the principal it reflects is fixed here so the only
// thing that moves in these cases is the thing under test.
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ principal, can: () => false }),
}))

import { configureApiClient, http, __resetRefreshState } from '@/lib/api/client'
import { queryKeys } from '@/lib/api/query'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import { useWorkspaceStore } from '@/stores/workspace'
import {
  capabilityQuestion,
  capabilityScopeKey,
  createCapabilityPermit,
  liveCapabilityContext,
  useCapability,
  CAPABILITY_RETRY_MS,
} from './capabilities'
import {
  permittedDispatchGuard,
  useIntentGuard,
} from '@/features/communications/intent'
import { ChannelConfigForm } from '@/features/communications/channel-config-form'
import { channelOf, scopeOf } from '@/features/communications/test-harness'
import '@/features/communications/i18n'

const A = '0192f2c0-aaaa-7000-8000-000000000001'
const B = '0192f2c0-aaaa-7000-8000-000000000002'
const CH = '0192f2c0-bbbb-7000-8000-000000000001'
const TENANT = 't1'

const surface = () =>
  capabilityQuestion({
    kind: 'surface',
    operation: 'GET /v1/m/sessions/channels/administration',
    workspaceId: useWorkspaceStore.getState().activeWorkspace,
  })
const patch = () =>
  capabilityQuestion({
    kind: 'operation',
    operation: 'PATCH /v1/m/sessions/channels',
    workspaceId: A,
    body: { channel_id: CH },
  })

const answer = (over: Record<string, unknown>) => ({
  schema_version: 2,
  results: [{ id: 'q', ...over }],
})
const positive = (kind: 'surface' | 'operation', budget = 30_000) =>
  answer(
    kind === 'surface'
      ? { kind, state: 'reachable', code: 'admitted', refresh_after_ms: budget }
      : {
          kind,
          state: 'allowed',
          code: 'authorized',
          refresh_after_ms: budget,
        },
  )
const body = (payload: unknown, status = 200) =>
  new Response(JSON.stringify(payload), {
    status,
    headers: { 'Content-Type': 'application/json' },
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
/** Four turns of the timer/microtask wheel: enough for a query to start, settle and
 *  re-render, and short enough that a repeating cadence is not silently exercised. */
async function drain() {
  for (let i = 0; i < 4; i++)
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1)
    })
}

beforeEach(() => {
  notices.warning.mockClear()
  useWorkspaceStore.setState({ activeWorkspace: A })
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

describe('the local context lifetime binds a round trip the values cannot see', () => {
  it('does not admit an in-flight A answer after a BATCHED workspace A → B → A', async () => {
    vi.useFakeTimers()
    let release!: (r: Response) => void
    const fetchSpy = vi.fn(() => new Promise<Response>((r) => (release = r)))
    vi.stubGlobal('fetch', fetchSpy)
    const { client, Wrapper } = mounted()
    const hook = renderHook(() => useCapability(surface()), {
      wrapper: Wrapper,
    })
    await drain()
    expect(fetchSpy).toHaveBeenCalledTimes(1)
    const first = capabilityScopeKey(
      liveCapabilityContext(client) as NonNullable<
        ReturnType<typeof liveCapabilityContext>
      >,
    )

    // Two writes, one batch, no commit in between — and the answer to the FIRST A lands
    // in the same act. Its values match the live ones again; its lifetime never can.
    await act(async () => {
      useWorkspaceStore.setState({ activeWorkspace: B })
      useWorkspaceStore.setState({ activeWorkspace: A })
      release(body(positive('surface')))
    })
    await drain()

    expect(hook.result.current.access).not.toBe('reachable')
    expect(hook.result.current.permit).toBeNull()
    // The dead partition is gone and a NEW observation is under way: the round trip is a
    // movement like any other, not a screen that never recovers.
    expect(client.getQueriesData({ queryKey: first })).toHaveLength(0)
    expect(fetchSpy.mock.calls.length).toBeGreaterThan(1)
    hook.unmount()
  })

  it('CONTROL: a stable context asks once and keeps its positive', async () => {
    vi.useFakeTimers()
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => body(positive('surface'))),
    )
    const { Wrapper } = mounted()
    const hook = renderHook(() => useCapability(surface()), {
      wrapper: Wrapper,
    })
    await drain()
    expect(hook.result.current.access).toBe('reachable')
    expect(hook.result.current.permit?.isCurrent()).toBe(true)
    hook.unmount()
  })

  it('CONTROL: a separately committed A → B → A observes three times and ends positive', async () => {
    vi.useFakeTimers()
    const fetchSpy = vi.fn(async () => body(positive('surface')))
    vi.stubGlobal('fetch', fetchSpy)
    const { Wrapper } = mounted()
    const hook = renderHook(() => useCapability(surface()), {
      wrapper: Wrapper,
    })
    await drain()
    await act(async () => useWorkspaceStore.setState({ activeWorkspace: B }))
    await drain()
    await act(async () => useWorkspaceStore.setState({ activeWorkspace: A }))
    await drain()
    expect(fetchSpy.mock.calls.length).toBeGreaterThanOrEqual(3)
    expect(hook.result.current.access).toBe('reachable')
    hook.unmount()
  })

  it('a saved permit dies across a batched round trip and STAYS dead', async () => {
    const { client } = mounted()
    const live = () => liveCapabilityContext(client)
    const permit = createCapabilityPermit(
      live() as NonNullable<ReturnType<typeof live>>,
      patch(),
      performance.now() + 10_000,
      live,
    )
    expect(permit.isCurrent()).toBe(true)
    await act(async () => {
      useWorkspaceStore.setState({ activeWorkspace: B })
      useWorkspaceStore.setState({ activeWorkspace: A })
    })
    expect(permit.isCurrent()).toBe(false)
    // Asking again — from a screen that re-rendered, from a retry, from anywhere — does
    // not bring it back.
    expect(permit.isCurrent()).toBe(false)
    expect(useWorkspaceStore.getState().activeWorkspace).toBe(A)
  })

  it('the composed real intent guard sends ZERO mutation bytes after a batched round trip', async () => {
    const { client, Wrapper } = mounted()
    const hook = renderHook(
      () => useIntentGuard({ allowed: true, boundary: `${A}|fixed` }),
      { wrapper: Wrapper },
    )
    const signal = hook.result.current.begin()
    expect(signal).not.toBeNull()
    const live = () => liveCapabilityContext(client)
    const permit = createCapabilityPermit(
      live() as NonNullable<ReturnType<typeof live>>,
      patch(),
      performance.now() + 10_000,
      live,
    )
    const guard = permittedDispatchGuard(hook.result.current, permit)
    await act(async () => {
      useWorkspaceStore.setState({ activeWorkspace: B })
      useWorkspaceStore.setState({ activeWorkspace: A })
    })

    const fetchSpy = vi.fn(async () => body({ ok: true }))
    vi.stubGlobal('fetch', fetchSpy)
    let refused = false
    try {
      await http.patch(
        '/v1/m/sessions/channels',
        { channel_id: CH },
        { signal: signal!, tenant: TENANT, dispatchGuard: guard },
      )
    } catch {
      refused = true
    }
    // ⛔ THE MEASURED DEFECT: one real PATCH left here on the frozen source. The guard is
    //    the last check before the bytes, so "the values match again" was the whole
    //    distance between a withdrawn authority and a request.
    expect(refused).toBe(true)
    expect(fetchSpy).not.toHaveBeenCalled()
    hook.unmount()
  })

  it('CONTROL: the same composed guard lets a send through while nothing has moved', async () => {
    const { client, Wrapper } = mounted()
    const hook = renderHook(
      () => useIntentGuard({ allowed: true, boundary: `${A}|fixed` }),
      { wrapper: Wrapper },
    )
    const signal = hook.result.current.begin()
    const live = () => liveCapabilityContext(client)
    const permit = createCapabilityPermit(
      live() as NonNullable<ReturnType<typeof live>>,
      patch(),
      performance.now() + 10_000,
      live,
    )
    const guard = permittedDispatchGuard(hook.result.current, permit)
    const fetchSpy = vi.fn(async () => body({ ok: true }))
    vi.stubGlobal('fetch', fetchSpy)
    await http.patch(
      '/v1/m/sessions/channels',
      { channel_id: CH },
      { signal: signal!, tenant: TENANT, dispatchGuard: guard },
    )
    expect(fetchSpy).toHaveBeenCalledTimes(1)
    hook.unmount()
  })
})

describe('an expired positive keeps a finite cadence instead of switching itself off', () => {
  it('stays unknown AND keeps asking after a render that happens past the deadline', async () => {
    vi.useFakeTimers()
    let now = 1_000
    vi.spyOn(performance, 'now').mockImplementation(() => now)
    const fetchSpy = vi.fn(async () => body(positive('surface', 100)))
    vi.stubGlobal('fetch', fetchSpy)
    const { Wrapper } = mounted()
    const hook = renderHook(() => useCapability(surface()), {
      wrapper: Wrapper,
    })
    await drain()
    expect(hook.result.current.access).toBe('reachable')

    // Expiry BEFORE the render, render BEFORE the timers: the ordering a suspended tab
    // produces. The screen correctly stops showing a positive — and on the frozen source
    // that same render computed a zero interval, which TanStack reads as "no interval",
    // so the recheck the text promises never happened again.
    now = 1_200
    const asked = fetchSpy.mock.calls.length
    hook.rerender()
    await drain()
    expect(hook.result.current.access).toBe('unknown')
    expect(hook.result.current.permit).toBeNull()

    await act(async () => {
      await vi.advanceTimersByTimeAsync(3 * CAPABILITY_RETRY_MS)
    })
    expect(fetchSpy.mock.calls.length).toBeGreaterThan(asked)
    hook.unmount()
  })

  it('CONTROL: a positive whose deadline timer runs first refreshes normally', async () => {
    vi.useFakeTimers()
    const fetchSpy = vi.fn(async () => body(positive('surface', 100)))
    vi.stubGlobal('fetch', fetchSpy)
    const { Wrapper } = mounted()
    const hook = renderHook(() => useCapability(surface()), {
      wrapper: Wrapper,
    })
    await drain()
    await act(async () => {
      await vi.advanceTimersByTimeAsync(350)
    })
    expect(fetchSpy.mock.calls.length).toBeGreaterThan(1)
    hook.unmount()
  })
})

describe('the sentence that closes a confirmation says only what was established', () => {
  const DIAGNOSIS = /permission behind it was lost/

  /** Open a real confirmation on the real form, over the real hook and transport, with
   *  the capability answer under the case's control. */
  async function confirming(answers: () => unknown) {
    const { client, Wrapper } = mounted()
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => body(answers())),
    )
    render(
      <ChannelConfigForm
        channel={channelOf({ id: CH })}
        etag={'"v2"'}
        scope={{ ...scopeOf(), workspace: A, tenant: TENANT, key: `${A}|k` }}
        reading={false}
        onReread={() => {}}
        onApplied={() => {}}
      />,
      { wrapper: Wrapper },
    )
    const user = userEvent.setup()
    await waitFor(() => expect(screen.getByLabelText(/^name/i)).toBeEnabled())
    const name = screen.getByLabelText(/^name/i)
    await user.clear(name)
    await user.type(name, 'Renamed under review')
    await user.click(screen.getByRole('button', { name: 'Review changes' }))
    expect(
      screen.getByRole('button', { name: 'Confirm changes' }),
    ).toBeInTheDocument()
    return { client }
  }

  async function loseAuthority(client: QueryClient) {
    await act(async () => {
      await client.invalidateQueries({ queryKey: ['auth-capabilities'] })
    })
    await waitFor(() =>
      expect(
        screen.queryByRole('button', { name: 'Confirm changes' }),
      ).toBeNull(),
    )
    return notices.warning.mock.calls.map((c) => String(c[0]))
  }

  it('a CONCEALED non-verdict closes it without diagnosing a permission loss', async () => {
    let concealed = false
    const { client } = await confirming(() =>
      concealed
        ? answer({
            kind: 'operation',
            state: 'undisclosed',
            code: 'not_disclosed',
          })
        : positive('operation'),
    )
    concealed = true
    const said = await loseAuthority(client)
    // ⛔ THE ENGINE PUBLISHED NO CAUSE. Naming one is a fabrication, and for a concealed
    //    answer it is the very inference the concealment exists to prevent.
    expect(said.join(' ')).not.toMatch(DIAGNOSIS)
    expect(said.join(' ')).toContain('no current answer')
    expect(said.join(' ')).toMatch(/nothing was sent/i)
  })

  it('a LOCAL unknown — a transport that did not answer — closes it neutrally too', async () => {
    let broken = false
    const { client } = await confirming(() => positive('operation'))
    broken = true
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => {
        if (broken) throw new TypeError('Failed to fetch')
        return body(positive('operation'))
      }),
    )
    const said = await loseAuthority(client)
    expect(said.join(' ')).not.toMatch(DIAGNOSIS)
    expect(said.join(' ')).toContain('no current answer')
  })

  it('a closure while the act is ALREADY SENDING promises nothing about sending', async () => {
    let concealed = false
    const { client, Wrapper } = mounted()
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string) => {
        if (String(url).includes('/v1/auth/capabilities'))
          return body(
            concealed
              ? answer({
                  kind: 'operation',
                  state: 'undisclosed',
                  code: 'not_disclosed',
                })
              : positive('operation'),
          )
        // The mutation itself never comes back: the act is in flight, and this render
        // cannot know whether its bytes reached the engine.
        return new Promise<Response>(() => {})
      }),
    )
    render(
      <ChannelConfigForm
        channel={channelOf({ id: CH })}
        etag={'"v2"'}
        scope={{ ...scopeOf(), workspace: A, tenant: TENANT, key: `${A}|k` }}
        reading={false}
        onReread={() => {}}
        onApplied={() => {}}
      />,
      { wrapper: Wrapper },
    )
    const user = userEvent.setup()
    await waitFor(() => expect(screen.getByLabelText(/^name/i)).toBeEnabled())
    const name = screen.getByLabelText(/^name/i)
    await user.clear(name)
    await user.type(name, 'Renamed while sending')
    await user.click(screen.getByRole('button', { name: 'Review changes' }))
    await user.click(screen.getByRole('button', { name: 'Confirm changes' }))
    // The act is in flight: the form is in its submitting phase.
    await screen.findByText('Applying…')

    concealed = true
    const said = await loseAuthority(client)
    expect(said.join(' ')).not.toMatch(DIAGNOSIS)
    // ⛔ AND NOT THE OTHER FABRICATION EITHER. `capability.notConfirmed` and the neutral
    //    pre-send line both state that nothing was sent, which is true where the
    //    preflight refused BEFORE the request and false here.
    expect(said.join(' ')).not.toMatch(/nothing was sent/i)
    expect(said.join(' ')).toMatch(/not established here/i)
  })

  it('CONTROL: an ESTABLISHED denial is the one case that may be named', async () => {
    let denied = false
    const { client } = await confirming(() =>
      denied
        ? answer({ kind: 'operation', state: 'denied', code: 'not_permitted' })
        : positive('operation'),
    )
    denied = true
    const said = await loseAuthority(client)
    // The sentence is not removed from the console; it is restricted to the fact that
    // makes it true. The engine decided and said so.
    expect(said.join(' ')).toMatch(DIAGNOSIS)
  })
})
