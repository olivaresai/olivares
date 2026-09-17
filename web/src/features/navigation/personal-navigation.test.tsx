// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
// Real QueryClient, AuthProvider, stores, router and route guard. Only API transport
// and protected page bodies are local fixtures; this is not an engine user journey.
import { StrictMode, useLayoutEffect, type ReactNode } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from '@tanstack/react-router'
import {
  act,
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { AuthProvider, useAuth } from '@/lib/auth/context'
import { queryKeys } from '@/lib/api/query'
import { authApi } from '@/lib/api/endpoints'
import { configureApiClient } from '@/lib/api/client'
import type { Whoami } from '@/lib/api/types'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import { useWorkspaceStore } from '@/stores/workspace'
import { useCommandStore } from '@/stores/command'
import { FEATURE_VIEWS } from '@/features/registry'
import { SettingsVisit } from './permitted-visit'
import { TenantGate } from '@/components/layout/tenant-gate'
import {
  useChannelDraftCapsule,
  type ChannelDraftCapsule,
} from '@/features/communications/channel-admin-continuity'
import { RequirePermission } from '@/components/layout/require-permission'
import {
  FavoriteButton,
  PersonalNavigation,
} from '@/components/layout/personal-navigation'
import { CommandMenu } from '@/components/layout/command-menu'
import { TooltipProvider } from '@/components/ui/tooltip'
import {
  PersonalNavigationProvider,
  usePersonalNavigation,
} from './personal-navigation'
import { favoriteStorageKey, personalLink } from './personal-navigation-store'

const A: Whoami = {
  kind: 'user',
  user_id: 'fixture-a',
  actor: 'user:fixture-a',
  display_name: 'Fixture A',
  superadmin: false,
  grants: [
    { tenant: 'tenant-a', role: 'viewer', permissions: ['sessions:run:read'] },
    { tenant: 'tenant-b', role: 'viewer', permissions: [] },
  ],
}
const home = personalLink('home')!
const clients: QueryClient[] = []
let latest: NonNullable<ReturnType<typeof usePersonalNavigation>>
function Probe() {
  const personal = usePersonalNavigation()!
  useLayoutEffect(() => {
    latest = personal
  }, [personal])
  const { principal } = useAuth()
  return (
    <output
      data-testid="saved"
      data-owner={principal?.user_id}
      data-ready={personal.available}
      data-recents={personal.recents.map((v) => v.id).join(',')}
    >
      {personal.favorites.map((v) => v.id).join(',')}
    </output>
  )
}
let capsule: ChannelDraftCapsule
function FixtureBody({ id }: { id: string }) {
  const current = useChannelDraftCapsule()
  useLayoutEffect(() => {
    capsule = current
  }, [current])
  return <p>Admitted fixture {id}</p>
}
function Shell({ children }: { children: ReactNode }) {
  const auth = useAuth()
  if (auth.status !== 'authenticated') return <p>Fixture signed out</p>
  return (
    <PersonalNavigationProvider>
      <Probe />
      {children}
    </PersonalNavigationProvider>
  )
}
async function mount({
  principal = A,
  path = '/agentops',
  compact = false,
} = {}) {
  vi.mocked(authApi.whoami).mockResolvedValue(principal)
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  })
  clients.push(client)
  client.setQueryData(queryKeys.whoami, principal)
  const root = createRootRoute({
    component: () => (
      <AuthProvider>
        <TooltipProvider>
          <Shell>
            <FavoriteButton />
            <PersonalNavigation collapsed={compact} />
            <CommandMenu />
            <TenantGate>
              <Outlet />
              <SettingsVisit />
            </TenantGate>
          </Shell>
        </TooltipProvider>
      </AuthProvider>
    ),
  })
  const routes = FEATURE_VIEWS.filter((v) =>
    [
      'home',
      'agentops',
      'communicationsAdministration',
      'session-viewer',
    ].includes(v.id),
  ).map((v) =>
    createRoute({
      getParentRoute: () => root,
      path: v.path,
      component: () => (
        <RequirePermission view={v}>
          <FixtureBody id={v.id} />
        </RequirePermission>
      ),
    }),
  )
  const settings = createRoute({
    getParentRoute: () => root,
    path: '/settings',
    component: () => <p>Admitted utility settings</p>,
  })
  const publicRoutes = [
    '/login',
    '/accept-invite',
    '/areas/ai',
    '/invalid',
  ].map((path) =>
    createRoute({
      getParentRoute: () => root,
      path,
      component: () => <p>Excluded fixture</p>,
    }),
  )
  const router = createRouter({
    routeTree: root.addChildren([...routes, settings, ...publicRoutes]),
    history: createMemoryHistory({ initialEntries: [path] }),
  })
  const result = render(
    <StrictMode>
      <QueryClientProvider client={client}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </StrictMode>,
  )
  await waitFor(() =>
    expect(screen.getByTestId('saved')).toHaveAttribute('data-ready', 'true'),
  )
  return { client, router, ...result }
}
beforeEach(() => {
  localStorage.clear()
  useSessionStore.getState().setSession({
    token: 'local-defensive-fixture',
    sessionId: 'fixture',
    expiresAt: '',
  })
  useTenantStore.setState({ activeTenant: 'tenant-a' })
  useWorkspaceStore.setState({ activeWorkspace: null })
  useCommandStore.setState({ open: false })
  configureApiClient({
    getToken: () => null,
    getTenant: () => null,
    onUnauthorized: () => {},
  })
  vi.spyOn(authApi, 'whoami').mockResolvedValue(A)
  vi.spyOn(authApi, 'logout').mockResolvedValue(undefined)
})
afterEach(() => {
  cleanup()
  clients.splice(0).forEach((c) => c.clear())
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

it('explicit keyboard save/remove/clear and reload leave the deep-link URL authoritative', async () => {
  const user = userEvent.setup()
  const first = await mount({ path: '/agentops?filter=fixture#kept' })
  await screen.findByText('Admitted fixture agentops')
  const star = screen.getByRole('button', {
    name: 'Add Operate sessions to favorites',
  })
  star.focus()
  await user.keyboard(' ')
  expect(screen.getByTestId('saved')).toHaveTextContent('agentops')
  expect(first.router.state.location.href).toBe('/agentops?filter=fixture#kept')
  first.unmount()
  const second = await mount({ path: '/?chosen=url#still-here' })
  expect(screen.getByTestId('saved')).toHaveTextContent('agentops')
  expect(second.router.state.location.href).toBe('/?chosen=url#still-here')
  const opener = screen.getByRole('button', { name: 'View all (1)' })
  opener.focus()
  await user.keyboard('{Enter}')
  const dialog = screen.getByRole('dialog')
  await user.click(
    within(dialog).getByRole('button', {
      name: 'Remove Operate sessions from favorites',
    }),
  )
  expect(
    within(dialog).getByRole('heading', { name: 'Favorites' }),
  ).toHaveFocus()
  expect(screen.getByTestId('saved')).toBeEmptyDOMElement()
  await user.keyboard('{Escape}')
  expect(opener).toHaveFocus()
  act(() => latest.setFavorite(home, true))
  await user.click(screen.getByRole('button', { name: 'View all (1)' }))
  await user.click(screen.getByRole('button', { name: 'Clear favorites' }))
  expect(screen.getByTestId('saved')).toBeEmptyDOMElement()
  expect(second.router.state.location.href).toBe('/?chosen=url#still-here')
})

it('isolates tenant/user transitions and refuses callbacks captured before a batched round trip', async () => {
  const { client } = await mount()
  act(() => latest.setFavorite(home, true))
  const old = latest
  act(() => {
    useTenantStore.getState().setActiveTenant('tenant-b')
    useTenantStore.getState().setActiveTenant('tenant-a')
  })
  act(() => old.clearFavorites())
  // The departed verification is retired too; returning to A now verifies afresh.
  await waitFor(() =>
    expect(screen.getByTestId('saved')).toHaveTextContent('home'),
  )
  const firstKey = favoriteStorageKey(window.location.origin, A, 'tenant-a')!
  act(() => useTenantStore.getState().setActiveTenant('tenant-b'))
  await waitFor(() => expect(screen.getByTestId('saved')).toBeEmptyDOMElement())
  act(() => old.setFavorite(personalLink('settings')!, true))
  expect(JSON.parse(localStorage.getItem(firstKey)!).favorites).toEqual([home])
  vi.mocked(authApi.whoami).mockResolvedValue({
    ...A,
    user_id: 'fixture-b',
    actor: 'user:fixture-b',
  })
  act(() =>
    client.setQueryData(queryKeys.whoami, {
      ...A,
      user_id: 'fixture-b',
      actor: 'user:fixture-b',
    }),
  )
  expect(screen.getByTestId('saved')).toBeEmptyDOMElement()
  await waitFor(() =>
    expect(screen.getByTestId('saved')).toHaveAttribute(
      'data-owner',
      'fixture-b',
    ),
  )
  await waitFor(() =>
    expect(screen.getByTestId('saved')).toHaveAttribute('data-ready', 'true'),
  )
  act(() => latest.setFavorite(personalLink('settings')!, true))
  expect(screen.getByTestId('saved')).toHaveTextContent('settings')
  const before = latest
  act(() => {
    client.setQueryData(queryKeys.whoami, A)
    client.setQueryData(queryKeys.whoami, {
      ...A,
      user_id: 'fixture-b',
      actor: 'user:fixture-b',
    })
  })
  act(() => before.clearFavorites())
  const secondKey = favoriteStorageKey(
    window.location.origin,
    { ...A, user_id: 'fixture-b' },
    'tenant-b',
  )!
  expect(JSON.parse(localStorage.getItem(secondKey)!).favorites).toEqual([
    personalLink('settings'),
  ])
  await waitFor(() =>
    expect(screen.getByTestId('saved')).toHaveTextContent('settings'),
  )
})

it('clears active memory on logout and never persists an unrecognized principal', async () => {
  const { unmount } = await mount({ principal: { ...A, kind: 'token' } })
  await waitFor(() => expect(recentIds()).toEqual(['agentops']))
  act(() => latest.setFavorite(home, true))
  expect(screen.getByTestId('saved')).toHaveTextContent('home')
  expect(
    Object.keys(localStorage).filter((k) =>
      k.startsWith('olivares.navigation'),
    ),
  ).toEqual([])
  const old = latest
  act(() => useSessionStore.getState().clear())
  await screen.findByText('Fixture signed out')
  act(() => old.setFavorite(personalLink('settings')!, true))
  unmount()
  useSessionStore.getState().setSession({
    token: 'second-fixture',
    sessionId: 'fixture-2',
    expiresAt: '',
  })
  await mount({ principal: { ...A, kind: 'token' } })
  expect(screen.getByTestId('saved')).toBeEmptyDOMElement()
})

it('hides a revoked permission from favorites and palette without deleting the saved preference', async () => {
  const { client } = await mount()
  act(() => latest.setFavorite(personalLink('agentops')!, true))
  const key = favoriteStorageKey(window.location.origin, A, 'tenant-a')!
  act(() =>
    client.setQueryData(queryKeys.whoami, {
      ...A,
      grants: A.grants.map((g) => ({ ...g, permissions: [] })),
    }),
  )
  await waitFor(() => expect(screen.getByTestId('saved')).toBeEmptyDOMElement())
  expect(localStorage.getItem(key)).toContain('agentops')
  expect(
    screen.queryByRole('button', { name: /Operate sessions to favorites/ }),
  ).not.toBeInTheDocument()
  act(() => useCommandStore.getState().setOpen(true))
  expect(
    within(screen.getByRole('dialog')).queryByRole('option', {
      name: /Operate sessions/,
    }),
  ).not.toBeInTheDocument()
})

it('uses actual capability answers: unknown is a link, denial hides it, neither admits protected content', async () => {
  const admin = FEATURE_VIEWS.find(
    (v) => v.id === 'communicationsAdministration',
  )!
  useWorkspaceStore.setState({
    activeWorkspace: '0192f2c0-aaaa-7000-8000-000000000001',
  })
  let answer: (value: Response) => void = () => {}
  vi.stubGlobal(
    'fetch',
    vi.fn(
      () =>
        new Promise<Response>((resolve) => {
          answer = resolve
        }),
    ),
  )
  const { client } = await mount({ path: admin.path })
  act(() => latest.setFavorite(personalLink(admin.id)!, true))
  expect(screen.getByTestId('saved')).toHaveTextContent(admin.id)
  expect(
    screen.queryByText(`Admitted fixture ${admin.id}`),
  ).not.toBeInTheDocument()
  expect(
    document.querySelector('[data-slot="capability-checking"]'),
  ).toBeInTheDocument()
  await act(async () =>
    answer(
      new Response(
        JSON.stringify({
          schema_version: 2,
          results: [
            {
              id: 'q',
              kind: 'surface',
              state: 'not_reachable',
              code: 'not_permitted',
              refresh_after_ms: 30000,
            },
          ],
        }),
        { status: 200 },
      ),
    ),
  )
  await waitFor(() => expect(screen.getByTestId('saved')).toBeEmptyDOMElement())
  expect(
    screen.queryByText(`Admitted fixture ${admin.id}`),
  ).not.toBeInTheDocument()
  // A later independently valid admission restores the personal link even though the
  // whoami fixture never had sessions:channel:admin.
  vi.stubGlobal(
    'fetch',
    vi.fn(
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
                refresh_after_ms: 30000,
              },
            ],
          }),
          { status: 200 },
        ),
    ),
  )
  await act(async () => {
    await client.invalidateQueries({ queryKey: ['auth-capabilities'] })
  })
  await screen.findByText(`Admitted fixture ${admin.id}`)
  expect(screen.getByTestId('saved')).toHaveTextContent(admin.id)
})

it('compact manager has keyboard access to every saved feature beyond the four-row preview', async () => {
  const user = userEvent.setup()
  await mount({ compact: true })
  act(() => {
    for (const v of FEATURE_VIEWS) {
      const link = personalLink(v.id)
      if (link) latest.setFavorite(link, true)
    }
    latest.setFavorite(personalLink('settings')!, true)
  })
  const all = latest.favorites.length
  expect(all).toBeGreaterThan(4)
  const trigger = screen.getByRole('button', { name: 'Manage favorites' })
  trigger.focus()
  await user.keyboard('{Enter}')
  expect(within(screen.getByRole('dialog')).getAllByRole('link')).toHaveLength(
    all,
  )
  await user.keyboard('{Escape}')
  expect(trigger).toHaveFocus()
})

it('quarantines cached whoami across credential movement and rejects the late identity answer', async () => {
  const { client } = await mount()
  act(() => latest.setFavorite(home, true))
  const old = latest
  let resolveFirst: (p: Whoami) => void = () => {}
  let resolveSecond: (p: Whoami) => void = () => {}
  vi.mocked(authApi.whoami)
    .mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          resolveFirst = resolve
        }),
    )
    .mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          resolveSecond = resolve
        }),
    )
  const reads = vi.spyOn(Storage.prototype, 'getItem')
  const writes = vi.spyOn(Storage.prototype, 'setItem')
  const firstKey = favoriteStorageKey(window.location.origin, A, 'tenant-a')!
  await act(async () =>
    useSessionStore.getState().setSession({
      token: 'defensive-second',
      sessionId: 'fixture-2',
      expiresAt: '',
    }),
  )
  expect(screen.getByTestId('saved')).toBeEmptyDOMElement()
  expect(screen.getByTestId('saved')).toHaveAttribute('data-ready', 'false')
  act(() => old.clearFavorites())
  expect(reads.mock.calls.some(([key]) => key === firstKey)).toBe(false)
  expect(writes.mock.calls.some(([key]) => key === firstKey)).toBe(false)
  await act(async () =>
    useSessionStore.getState().setSession({
      token: 'defensive-third',
      sessionId: 'fixture-3',
      expiresAt: '',
    }),
  )
  await act(async () => resolveFirst(A))
  expect(screen.getByTestId('saved')).toHaveAttribute('data-ready', 'false')
  await act(async () =>
    resolveSecond({ ...A, user_id: 'fixture-b', actor: 'user:fixture-b' }),
  )
  expect(screen.getByTestId('saved')).toHaveAttribute('data-ready', 'false')
  vi.mocked(authApi.whoami).mockResolvedValue({
    ...A,
    user_id: 'fixture-b',
    actor: 'user:fixture-b',
  })
  act(() =>
    client.setQueryData(queryKeys.whoami, {
      ...A,
      user_id: 'fixture-b',
      actor: 'user:fixture-b',
    }),
  )
  await waitFor(() =>
    expect(screen.getByTestId('saved')).toHaveAttribute('data-ready', 'true'),
  )
  expect(screen.getByTestId('saved')).toHaveAttribute('data-owner', 'fixture-b')
  expect(screen.getByTestId('saved')).toBeEmptyDOMElement()
  expect(reads.mock.calls.some(([key]) => key === firstKey)).toBe(false)
  await act(async () => latest.setFavorite(personalLink('settings')!, true))
  await waitFor(() =>
    expect(screen.getByTestId('saved')).toHaveTextContent('settings'),
  )
})

const recentIds = () => latest.recents.map((v) => v.id)

it('records real permitted mounts once, preserves URL, removes/clears by keyboard and starts fresh on reload', async () => {
  const user = userEvent.setup()
  const { router, unmount } = await mount({
    path: '/agentops?filter=fixture#kept',
  })
  await waitFor(() => expect(recentIds()).toEqual(['agentops']))
  const whoamiReads = vi.mocked(authApi.whoami).mock.calls.length
  await act(async () => {
    await router.navigate({ to: '/' })
  })
  expect(recentIds()).toEqual(['home', 'agentops'])
  await act(async () => {
    await router.navigate({ to: '/agentops?filter=fixture#kept' as never })
  })
  expect(recentIds()).toEqual(['agentops', 'home'])
  expect(router.state.location.href).toBe('/agentops?filter=fixture#kept')
  expect(vi.mocked(authApi.whoami).mock.calls).toHaveLength(whoamiReads)
  const trigger = screen.getByRole('button', { name: 'View recent (2)' })
  trigger.focus()
  await user.keyboard('{Enter}')
  await user.click(
    screen.getByRole('button', {
      name: 'Remove Operate sessions from recent modules',
    }),
  )
  expect(recentIds()).toEqual(['home'])
  expect(
    screen.getByRole('button', { name: 'Remove Overview from recent modules' }),
  ).toHaveFocus()
  await user.click(screen.getByRole('button', { name: 'Clear recent modules' }))
  expect(recentIds()).toEqual([])
  expect(screen.getByRole('heading', { name: 'Recent' })).toHaveFocus()
  await user.keyboard('{Escape}')
  await act(async () => {
    await router.invalidate()
  })
  expect(recentIds()).toEqual([])
  expect(router.state.location.href).toBe('/agentops?filter=fixture#kept')
  // A new module visit after explicit clearing is useful again.
  await act(async () => {
    await router.navigate({ to: '/settings' })
  })
  expect(latest.recents).toEqual([{ kind: 'utility', id: 'settings' }])
  expect(
    Object.keys(localStorage).filter((key) => key.includes('navigation')),
  ).toEqual([])
  unmount()
  await mount({ path: '/' })
  await waitFor(() => expect(recentIds()).toEqual(['home']))
})

it('keeps the same identity history on renewal, clears departures and rejects old owners including batched logout/login', async () => {
  const { client, router } = await mount()
  await waitFor(() => expect(recentIds()).toEqual(['agentops']))
  await act(async () => {
    await router.navigate({ to: '/' })
  })
  const beforeRenewal = latest
  await act(async () =>
    useSessionStore.getState().setSession({
      token: 'defensive-renewal',
      sessionId: 'fixture',
      expiresAt: '',
    }),
  )
  await waitFor(() => expect(latest.available).toBe(true))
  expect(recentIds()).toEqual(['home', 'agentops'])
  act(() => beforeRenewal.clearRecents())
  expect(recentIds()).toEqual(['home', 'agentops'])
  const old = latest
  act(() => {
    useTenantStore.getState().setActiveTenant('tenant-b')
    useTenantStore.getState().setActiveTenant('tenant-a')
  })
  await waitFor(() => expect(recentIds()).toEqual(['home']))
  act(() => {
    latest.clearRecents()
    old.recordVisit('home')
    old.removeRecent('home')
  })
  expect(recentIds()).toEqual([])
  await act(async () => {
    await router.navigate({ to: '/agentops' as never })
  })
  const beforeLogout = latest
  act(() => {
    useSessionStore.getState().clear()
    useSessionStore.getState().setSession({
      token: 'defensive-login',
      sessionId: 'fixture',
      expiresAt: '',
    })
  })
  await waitFor(() => expect(latest.available).toBe(true))
  expect(recentIds()).toEqual(['agentops'])
  act(() => {
    latest.clearRecents()
    beforeLogout.recordVisit('agentops')
    beforeLogout.clearRecents()
  })
  expect(recentIds()).toEqual([])
  const b = { ...A, user_id: 'fixture-b', actor: 'user:fixture-b' }
  vi.mocked(authApi.whoami).mockResolvedValue(b)
  act(() => client.setQueryData(queryKeys.whoami, b))
  await waitFor(() => expect(latest.available).toBe(true))
  await waitFor(() =>
    expect(screen.getByTestId('saved')).toHaveAttribute(
      'data-owner',
      'fixture-b',
    ),
  )
  expect(recentIds()).toEqual(['agentops'])
  act(() => {
    latest.clearRecents()
    old.recordVisit('home')
    beforeLogout.recordVisit('agentops')
  })
  expect(recentIds()).toEqual([])
})

it('does not count denied, unknown, public, invalid or detail URLs, including permitted exact entity admission', async () => {
  const admin = FEATURE_VIEWS.find(
    (v) => v.id === 'communicationsAdministration',
  )!
  useWorkspaceStore.setState({
    activeWorkspace: '0192f2c0-aaaa-7000-8000-000000000001',
  })
  let reply: (value: Response) => void = () => {}
  vi.stubGlobal(
    'fetch',
    vi.fn(
      () =>
        new Promise<Response>((resolve) => {
          reply = resolve
        }),
    ),
  )
  const { router, client } = await mount({ path: admin.path })
  expect(recentIds()).toEqual([])
  expect(
    document.querySelector('[data-slot="capability-checking"]'),
  ).toBeInTheDocument()
  await act(async () =>
    reply(
      new Response(
        JSON.stringify({
          schema_version: 2,
          results: [
            {
              id: 'q',
              kind: 'surface',
              state: 'not_reachable',
              code: 'not_permitted',
              refresh_after_ms: 30000,
            },
          ],
        }),
        { status: 200 },
      ),
    ),
  )
  expect(recentIds()).toEqual([])
  expect(
    screen.queryByText(`Admitted fixture ${admin.id}`),
  ).not.toBeInTheDocument()
  const entity = '0192f2c0-aaaa-7000-8000-000000000099'
  vi.stubGlobal(
    'fetch',
    vi.fn(async (_url, init) => {
      const question = JSON.parse(init!.body as string).questions[0]
      return new Response(
        JSON.stringify({
          schema_version: 2,
          results: [
            {
              id: question.id,
              kind: question.kind,
              state: question.kind === 'operation' ? 'allowed' : 'reachable',
              code: question.kind === 'operation' ? 'authorized' : 'admitted',
              refresh_after_ms: 30000,
            },
          ],
        }),
        { status: 200 },
      )
    }),
  )
  await act(async () => {
    await router.navigate({ to: `${admin.path}?admin_channel=${entity}` })
  })
  await screen.findByText(`Admitted fixture ${admin.id}`)
  expect(recentIds()).toEqual([])
  for (const path of [
    `/session-viewer/${entity}`,
    '/login',
    '/accept-invite?token=defensive-fixture',
    '/invalid',
    '/areas/ai',
  ]) {
    await act(async () => {
      await router.navigate({ to: path })
    })
    act(() => latest.recordVisit('home'))
    expect(recentIds()).toEqual([])
  }
  await act(async () => {
    await client.invalidateQueries({ queryKey: ['auth-capabilities'] })
    await router.navigate({ to: admin.path })
  })
  await waitFor(() => expect(recentIds()).toEqual([admin.id]))
})

it('preserves the real continuity owner across an unknown gap and does not re-add a cleared recent on re-admission', async () => {
  const admin = FEATURE_VIEWS.find(
    (v) => v.id === 'communicationsAdministration',
  )!
  useWorkspaceStore.setState({
    activeWorkspace: '0192f2c0-aaaa-7000-8000-000000000001',
  })
  let response: 'positive' | 'malformed' | 'denied' = 'positive'
  vi.stubGlobal(
    'fetch',
    vi.fn(
      async () =>
        new Response(
          JSON.stringify(
            response === 'malformed'
              ? {}
              : {
                  schema_version: 2,
                  results: [
                    {
                      id: 'q',
                      kind: 'surface',
                      state:
                        response === 'positive' ? 'reachable' : 'not_reachable',
                      code:
                        response === 'positive' ? 'admitted' : 'not_permitted',
                      refresh_after_ms: 30000,
                    },
                  ],
                },
          ),
          { status: 200 },
        ),
    ),
  )
  const { client, router } = await mount({ path: admin.path })
  await waitFor(() => expect(recentIds()).toEqual([admin.id]))
  const hold = {
    edits: { name: 'local unsent fixture' },
    confirming: false,
    submitting: false,
  }
  act(() => capsule.publish('local-fixture-channel', hold, capsule.generation))
  expect(
    capsule.take('local-fixture-channel', capsule.generation)?.hold,
  ).toEqual(hold)
  act(() => latest.clearRecents())
  response = 'malformed'
  await act(async () => {
    await client.invalidateQueries({ queryKey: ['auth-capabilities'] })
  })
  await waitFor(() =>
    expect(
      screen.queryByText(`Admitted fixture ${admin.id}`),
    ).not.toBeInTheDocument(),
  )
  expect(
    document.querySelector('[data-slot="capability-unavailable"]'),
  ).toBeInTheDocument()
  expect(recentIds()).toEqual([])
  response = 'positive'
  await act(async () => {
    await client.invalidateQueries({ queryKey: ['auth-capabilities'] })
  })
  await screen.findByText(`Admitted fixture ${admin.id}`)
  expect(
    capsule.take('local-fixture-channel', capsule.generation)?.hold,
  ).toEqual(hold)
  expect(recentIds()).toEqual([])
  await act(async () => {
    await router.navigate({ to: '/' })
    await router.navigate({ to: admin.path })
  })
  await waitFor(() => expect(recentIds()).toEqual([admin.id, 'home']))
  response = 'denied'
  await act(async () => {
    await client.invalidateQueries({ queryKey: ['auth-capabilities'] })
  })
  await waitFor(() => expect(recentIds()).toEqual(['home']))
  act(() => useCommandStore.getState().setOpen(true))
  expect(
    within(screen.getByRole('dialog')).queryByRole('option', {
      name: /Channel administration/,
    }),
  ).not.toBeInTheDocument()
})

it('Settings uses its real tenant-free utility admission; a feature behind TenantGate is not visited', async () => {
  useTenantStore.setState({ activeTenant: null })
  const { router } = await mount({
    principal: { ...A, grants: [] },
    path: '/settings',
  })
  await screen.findByText('Admitted utility settings')
  await waitFor(() =>
    expect(latest.recents).toEqual([{ kind: 'utility', id: 'settings' }]),
  )
  await act(async () => {
    await router.navigate({ to: '/' })
  })
  expect(screen.queryByText('Admitted fixture home')).not.toBeInTheDocument()
  expect(recentIds()).toEqual(['settings'])
})

it("records the guard's permitted no-workspace placeholder without claiming a capability or data read", async () => {
  const admin = FEATURE_VIEWS.find(
    (v) => v.id === 'communicationsAdministration',
  )!
  const transport = vi.fn(() =>
    Promise.reject(
      new Error('No capability question exists without a workspace'),
    ),
  )
  vi.stubGlobal('fetch', transport)
  await mount({ path: admin.path })
  await screen.findByText(`Admitted fixture ${admin.id}`)
  await waitFor(() => expect(recentIds()).toEqual([admin.id]))
  expect(transport).not.toHaveBeenCalled()
})
