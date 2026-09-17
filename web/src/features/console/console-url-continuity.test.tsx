// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// N2 — URL drives the mounted Console selection. These tests use the REAL
// @tanstack/react-router (memory history, scrollRestoration as in app/router.tsx)
// so a later navigate()/Back/Forward while ConsoleView stays mounted is the same
// mechanism a browser uses. Tab panels are stubbed: the seam under test is which
// panel a URL selects, not what any panel renders.
import { act, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
  type AnyRouter,
} from '@tanstack/react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import './i18n'

vi.mock('./people-tab', () => ({
  PeopleTab: () => <div>PeopleTab mounted</div>,
}))
vi.mock('./agents-tab', () => ({
  AgentsTab: () => <div>AgentsTab mounted</div>,
}))
vi.mock('./sso-tab', () => ({ SSOTab: () => <div>SSOTab mounted</div> }))
vi.mock('./scopes-tab', () => ({
  ScopesTab: () => <div>ScopesTab mounted</div>,
}))
vi.mock('./roles-tab', () => ({ RolesTab: () => <div>RolesTab mounted</div> }))
vi.mock('./bindings-tab', () => ({
  BindingsTab: () => <div>BindingsTab mounted</div>,
}))
vi.mock('./secrets-tab', () => ({
  SecretsTab: () => <div>SecretsTab mounted</div>,
}))
vi.mock('./connectors-tab', () => ({
  ConnectorsTab: () => <div>ConnectorsTab mounted</div>,
}))
vi.mock('./workspace-connectors-tab', () => ({
  WorkspaceConnectorsTab: () => <div>WorkspaceConnectorsTab mounted</div>,
}))
vi.mock('./api-keys-tab', () => ({
  ApiKeysTab: () => <div>ApiKeysTab mounted</div>,
}))
vi.mock('./license-tab', () => ({
  LicenseTab: () => <div>LicenseTab mounted</div>,
}))

import ConsoleView from './console-view'

const ALL_TABS = [
  ['people', 'PeopleTab mounted'],
  ['agents', 'AgentsTab mounted'],
  ['sso', 'SSOTab mounted'],
  ['scopes', 'ScopesTab mounted'],
  ['roles', 'RolesTab mounted'],
  ['bindings', 'BindingsTab mounted'],
  ['secrets', 'SecretsTab mounted'],
  ['connectors', 'ConnectorsTab mounted'],
  ['wsConnectors', 'WorkspaceConnectorsTab mounted'],
  ['apiKeys', 'ApiKeysTab mounted'],
  ['license', 'LicenseTab mounted'],
] as const

function locationOf(router: AnyRouter) {
  const loc = router.state.location
  return {
    pathname: loc.pathname,
    search: loc.searchStr,
    hash: loc.hash,
    href: loc.href,
  }
}

/** Resolves once the router has emitted `onRendered` for this path (+ optional search). */
function rendered(
  router: AnyRouter,
  pathname: string,
  searchIncludes?: string,
) {
  return new Promise<void>((resolve) => {
    const unsubscribe = router.subscribe('onRendered', (event) => {
      const loc = event.toLocation
      if (loc.pathname !== pathname) return
      if (searchIncludes && !loc.searchStr.includes(searchIncludes)) return
      unsubscribe()
      resolve()
    })
  })
}

function makeRouter(initialEntries: string[]) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const consoleRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/console',
    validateSearch: (raw: Record<string, unknown>) => ({
      tab: typeof raw.tab === 'string' ? raw.tab : undefined,
      focus: typeof raw.focus === 'string' ? raw.focus : undefined,
    }),
    component: () => <ConsoleView />,
  })
  const homeRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/',
    component: () => <h1>home</h1>,
  })
  return createRouter({
    routeTree: rootRoute.addChildren([consoleRoute, homeRoute]),
    history: createMemoryHistory({ initialEntries }),
    scrollRestoration: true,
  })
}

async function mount(initialEntries: string[]) {
  const router = makeRouter(initialEntries)
  await act(async () => {
    render(<RouterProvider router={router} />)
  })
  return router
}

afterEach(() => {
  window.history.replaceState(null, '', '/')
})

describe('ConsoleView — mounted URL continuity (real router)', () => {
  it('follows PUSH ?tab=people then ?tab=license while still mounted; Back/Forward match', async () => {
    const router = await mount(['/console?tab=roles'])
    expect(screen.getByText('RolesTab mounted')).toBeInTheDocument()
    expect(screen.getAllByRole('tab')).toHaveLength(11)

    let done = rendered(router, '/console', 'tab=people')
    await act(async () => {
      await router.navigate({
        to: '/console',
        search: { tab: 'people' },
      } as never)
      await done
    })
    expect(screen.getByText('PeopleTab mounted')).toBeInTheDocument()
    expect(screen.queryByText('RolesTab mounted')).toBeNull()
    expect(locationOf(router).search).toContain('tab=people')

    done = rendered(router, '/console', 'tab=license')
    await act(async () => {
      await router.navigate({
        to: '/console',
        search: { tab: 'license' },
      } as never)
      await done
    })
    expect(screen.getByText('LicenseTab mounted')).toBeInTheDocument()
    expect(screen.queryByText('PeopleTab mounted')).toBeNull()

    done = rendered(router, '/console', 'tab=people')
    await act(async () => {
      router.history.back()
      await done
    })
    expect(screen.getByText('PeopleTab mounted')).toBeInTheDocument()
    expect(locationOf(router).search).toContain('tab=people')

    done = rendered(router, '/console', 'tab=roles')
    await act(async () => {
      router.history.back()
      await done
    })
    expect(screen.getByText('RolesTab mounted')).toBeInTheDocument()
    expect(locationOf(router).search).toContain('tab=roles')

    done = rendered(router, '/console', 'tab=people')
    await act(async () => {
      router.history.forward()
      await done
    })
    expect(screen.getByText('PeopleTab mounted')).toBeInTheDocument()

    done = rendered(router, '/console', 'tab=license')
    await act(async () => {
      router.history.forward()
      await done
    })
    expect(screen.getByText('LicenseTab mounted')).toBeInTheDocument()
  })

  it('user tab clicks REPLACE, so Back leaves the view instead of walking tabs', async () => {
    const user = userEvent.setup()
    const router = await mount(['/', '/console?tab=roles'])
    expect(screen.getByText('RolesTab mounted')).toBeInTheDocument()

    let done = rendered(router, '/console', 'tab=people')
    await user.click(screen.getByRole('tab', { name: /users & groups/i }))
    await act(() => done)
    expect(screen.getByText('PeopleTab mounted')).toBeInTheDocument()

    done = rendered(router, '/console', 'tab=license')
    await user.click(screen.getByRole('tab', { name: /edition & license/i }))
    await act(() => done)
    expect(screen.getByText('LicenseTab mounted')).toBeInTheDocument()

    done = rendered(router, '/')
    await act(async () => {
      router.history.back()
      await done
    })
    expect(screen.getByRole('heading', { name: 'home' })).toBeInTheDocument()
    expect(screen.queryByRole('tablist')).toBeNull()
    expect(locationOf(router).pathname).toBe('/')
  })

  it('preserves unrelated search and hash across a user tab click', async () => {
    const user = userEvent.setup()
    const router = await mount(['/console?tab=roles&focus=svc_pool#panel-x'])
    expect(screen.getByText('RolesTab mounted')).toBeInTheDocument()
    expect(
      locationOf(router).hash === '#panel-x' ||
        locationOf(router).hash === 'panel-x',
    ).toBe(true)

    const done = rendered(router, '/console', 'tab=secrets')
    await user.click(screen.getByRole('tab', { name: /secrets/i }))
    await act(() => done)
    expect(screen.getByText('SecretsTab mounted')).toBeInTheDocument()
    const loc = locationOf(router)
    expect(loc.search).toContain('tab=secrets')
    expect(loc.search).toContain('focus=svc_pool')
    expect(loc.hash === '#panel-x' || loc.hash === 'panel-x').toBe(true)
  })

  it('invalid ?tab= falls back to People, clears only tab, keeps unrelated search/hash, does not loop', async () => {
    const router = await mount([
      '/console?tab=not-a-tab&focus=svc_pool#panel-x',
    ])
    await act(async () => {})
    expect(screen.getByText('PeopleTab mounted')).toBeInTheDocument()
    const loc = locationOf(router)
    expect(loc.search).not.toContain('tab=not-a-tab')
    expect(loc.search).toContain('focus=svc_pool')
    expect(loc.hash === '#panel-x' || loc.hash === 'panel-x').toBe(true)
    // Cleanup is a replace: staying on /console, not bouncing through another entry.
    expect(loc.pathname).toBe('/console')
    expect(screen.getAllByRole('tab')).toHaveLength(11)
  })

  it.each(ALL_TABS)(
    'router PUSH to ?tab=%s selects that panel while Console stays mounted',
    async (id, mounted) => {
      const router = await mount(['/console'])
      expect(screen.getByText('PeopleTab mounted')).toBeInTheDocument()
      const done = rendered(router, '/console', `tab=${id}`)
      await act(async () => {
        await router.navigate({
          to: '/console',
          search: { tab: id },
        } as never)
        await done
      })
      expect(screen.getByText(mounted)).toBeInTheDocument()
      expect(screen.getAllByRole('tab')).toHaveLength(11)
    },
  )
})
