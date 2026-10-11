// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, renderHook, waitFor } from '@testing-library/react'
import { useEffect, useMemo, type ReactNode } from 'react'
import { beforeEach, afterEach, expect, it, vi } from 'vitest'
import { configureApiClient } from '@/lib/api/client'
import { queryKeys } from '@/lib/api/query'
import { usePreferencesStore } from '@/stores/preferences'
import { useModulesStore } from '@/stores/modules'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import { createPersonalNavigationSession } from '@/features/navigation/personal-navigation-store'
import { useSidebarSync } from './sidebar-mode'

let client: QueryClient
let writes: { person: string; tenant: string | null; sidebar: string }[]
let person: string
let read: () => Promise<Response>
let write: () => Promise<Response>
function response(sidebar = 'full') {
  return Response.json({ stored: true, sidebar })
}
function deferred() {
  let resolve!: (r: Response) => void
  const promise = new Promise<Response>((r) => {
    resolve = r
  })
  return { promise, resolve }
}
function identify(id: string, tenant = 't1') {
  person = id
  useTenantStore.getState().setActiveTenant(tenant)
  useSessionStore.setState((s) => ({
    credentialGeneration: s.credentialGeneration + 1,
  }))
  client.setQueryData(queryKeys.whoami, { kind: 'user', user_id: id })
}
function mount() {
  return renderHook(
    ({ tenant, user }) => {
      const scope = useMemo(() => {
        const generation = useSessionStore.getState().credentialGeneration
        const key = JSON.stringify([window.location.origin, tenant, user])
        const session = createPersonalNavigationSession({
          key,
          current: () => {
            const identity = client.getQueryData<{
              kind: string
              user_id: string
            }>(queryKeys.whoami)
            return (
              useTenantStore.getState().activeTenant === tenant &&
              useSessionStore.getState().credentialGeneration === generation &&
              identity?.kind === 'user' &&
              identity.user_id === user
            )
          },
        })
        return { key, tenant, generation, ...session }
      }, [tenant, user])
      useEffect(() => {
        const invalidate = () => {
          scope.live()
        }
        const offTenant = useTenantStore.subscribe(invalidate)
        const offSession = useSessionStore.subscribe(invalidate)
        const offIdentity = client.getQueryCache().subscribe(invalidate)
        return () => {
          offTenant()
          offSession()
          offIdentity()
          scope.revoke()
        }
      }, [scope])
      useSidebarSync(scope)
    },
    {
      initialProps: { tenant: 't1', user: 'alice' },
      wrapper: ({ children }: { children: ReactNode }) => (
        <QueryClientProvider client={client}>{children}</QueryClientProvider>
      ),
    },
  )
}
beforeEach(() => {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  writes = []
  read = async () => response()
  write = async () => response()
  useModulesStore.getState().setOff([])
  usePreferencesStore.setState({
    sidebarCollapsed: false,
    sidebarUnsent: false,
    sidebarChoices: {},
    sidebarOwner: null,
  })
  identify('alice')
  configureApiClient({
    getToken: () => null,
    getTenant: () => useTenantStore.getState().activeTenant,
  })
  vi.stubGlobal(
    'fetch',
    vi.fn(async (_input, init: RequestInit) => {
      if (init.method === 'PUT') {
        writes.push({
          person,
          tenant: new Headers(init.headers).get('X-Olivares-Tenant'),
          ...JSON.parse(String(init.body)),
        })
        return write()
      }
      return read()
    }),
  )
})
afterEach(() => {
  client.clear()
  vi.unstubAllGlobals()
})

it('does not send Alice’s pending preference to Bob and resends it only to Alice', async () => {
  write = async () =>
    Response.json(
      { error: { code: 'internal', message: 'offline' } },
      { status: 500 },
    )
  const hook = mount()
  await act(async () => {})
  act(() => usePreferencesStore.getState().toggleSidebar())
  await waitFor(() => expect(writes).toHaveLength(1))
  await act(async () => {})
  write = async () => response()
  act(() => {
    identify('bob')
    hook.rerender({ tenant: 't1', user: 'bob' })
  })
  await act(async () => {})
  expect(writes).toHaveLength(1)
  expect(usePreferencesStore.getState().sidebarCollapsed).toBe(false)
  act(() => {
    identify('alice')
    hook.rerender({ tenant: 't1', user: 'alice' })
  })
  await waitFor(() => expect(writes).toHaveLength(2))
  expect(writes[1]).toMatchObject({ person: 'alice', sidebar: 'rail' })
})

it.each(['person', 'tenant', 'module', 'credential'] as const)(
  'revokes queued PUTs and late completions on %s movement before React cleanup',
  async (change) => {
    const pending = deferred()
    write = () => pending.promise
    const hook = mount()
    await act(async () => {})
    act(() => usePreferencesStore.getState().toggleSidebar())
    await waitFor(() => expect(writes).toHaveLength(1))
    act(() => usePreferencesStore.getState().toggleSidebar())
    act(() => {
      if (change === 'module')
        useModulesStore.getState().setOff(['consoleviews'])
      else if (change === 'tenant')
        useTenantStore.getState().setActiveTenant('t2')
      else if (change === 'person')
        client.setQueryData(queryKeys.whoami, { kind: 'user', user_id: 'bob' })
      else
        useSessionStore.setState((s) => ({
          credentialGeneration: s.credentialGeneration + 1,
        }))
    })
    await act(async () => {
      pending.resolve(response())
      await pending.promise
    })
    hook.unmount()
    expect(writes).toHaveLength(1)
  },
)

it('keeps a toggle pending when the initial GET fails', async () => {
  const pending = deferred()
  read = () => pending.promise
  const first = mount()
  act(() => usePreferencesStore.getState().toggleSidebar())
  await act(async () => {
    pending.resolve(Response.json({}, { status: 503 }))
    await pending.promise
  })
  first.unmount()
  read = async () => response('full')
  mount()
  await waitFor(() => expect(writes).toHaveLength(1))
  expect(writes[0]).toMatchObject({ person: 'alice', sidebar: 'rail' })
})

it('starts on module enable and preserves changes made while it was off', async () => {
  useModulesStore.getState().setOff(['consoleviews'])
  mount()
  act(() => usePreferencesStore.getState().toggleSidebar())
  expect(writes).toEqual([])
  act(() => useModulesStore.getState().setOff([]))
  await waitFor(() => expect(writes).toHaveLength(1))
  expect(writes[0].sidebar).toBe('rail')
})

it('does not apply a GET that completed after an identity round trip before React committed', async () => {
  const pending = deferred()
  read = () => pending.promise
  mount()
  act(() => {
    client.setQueryData(queryKeys.whoami, { kind: 'user', user_id: 'bob' })
    client.setQueryData(queryKeys.whoami, { kind: 'user', user_id: 'alice' })
  })
  await act(async () => {
    pending.resolve(response('rail'))
    await pending.promise
  })
  expect(usePreferencesStore.getState().sidebarCollapsed).toBe(false)
  expect(writes).toEqual([])
})

it('a successful older PUT cannot clear a newer choice, even when the widths match again', async () => {
  const pending = deferred()
  write = () => pending.promise
  mount()
  await act(async () => {})
  act(() => usePreferencesStore.getState().toggleSidebar())
  await waitFor(() => expect(writes).toHaveLength(1))
  act(() => {
    usePreferencesStore.getState().toggleSidebar()
    usePreferencesStore.getState().toggleSidebar()
  })
  const newer = deferred()
  write = () => newer.promise
  await act(async () => {
    pending.resolve(response('rail'))
    await pending.promise
  })
  await waitFor(() => expect(writes).toHaveLength(2))
  expect(usePreferencesStore.getState().sidebarUnsent).toBe(true)
  await act(async () => {
    newer.resolve(Response.json({}, { status: 503 }))
    await newer.promise
  })
  await waitFor(() => expect(writes).toHaveLength(3))
})

it('revokes a module off/on round trip and restarts with its owned pending choice', async () => {
  const pending = deferred()
  write = () => pending.promise
  mount()
  await act(async () => {})
  act(() => usePreferencesStore.getState().toggleSidebar())
  await waitFor(() => expect(writes).toHaveLength(1))
  act(() => usePreferencesStore.getState().toggleSidebar())
  write = async () => response()
  act(() => {
    useModulesStore.getState().setOff(['consoleviews'])
    useModulesStore.getState().setOff([])
  })
  await waitFor(() => expect(writes).toHaveLength(2))
  await act(async () => {
    pending.resolve(response())
    await pending.promise
  })
  expect(writes).toHaveLength(2)
  expect(writes[1].sidebar).toBe('full')
})

it('the engine stored full wins over an unowned legacy folded browser copy', async () => {
  usePreferencesStore.setState({ sidebarCollapsed: true })
  mount()
  await act(async () => {})
  expect(usePreferencesStore.getState().sidebarCollapsed).toBe(false)
  expect(writes).toEqual([])
})
