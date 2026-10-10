// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { act, render, screen } from '@testing-library/react'
import type { ReactNode } from 'react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

const chunk = vi.hoisted(() => ({ loads: 0 }))
const authority = vi.hoisted(() => ({
  status: 'authenticated' as
    'authenticated' | 'anonymous' | 'loading' | 'error',
  allowed: true,
  tenant: 'test-tenant' as string | null,
}))
vi.mock('@/components/layout/app-layout', async (importOriginal) => {
  chunk.loads++
  return importOriginal()
})

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    status: authority.status,
    can: () => authority.allowed,
    principal: { agent_id: 'test-operator', superadmin: false },
    activeTenant: authority.tenant,
    grants: [],
    isSuperadmin: false,
    isAuthenticated: authority.status === 'authenticated',
    setActiveTenant: vi.fn(),
  }),
}))

vi.mock('@/features/registry', () => ({
  FEATURE_VIEWS: [
    {
      id: 'audit',
      path: '/audit',
      permission: 'audit:read',
      element: () => <p>Protected audit screen</p>,
    },
  ],
  NAV_AREAS: [],
  ROUTE_ALIASES: [],
}))
vi.mock('@/features/anonymous-registry', () => ({ ANONYMOUS_VIEWS: [] }))
vi.mock('./pages/login', () => ({ LoginPage: () => <p>Login screen</p> }))
vi.mock('./pages/setup', () => ({ SetupPage: () => <p>Setup screen</p> }))
vi.mock('./pages/accept-invite', () => ({
  AcceptInvitePage: () => <p>Invite screen</p>,
}))
vi.mock('./pages/settings', () => ({
  SettingsPage: () => <p>Settings screen</p>,
}))

// Keep the real AppLayout, TenantGate and RequirePermission. Shell decorations
// own separate tests; they do not affect route admission or import timing.
vi.mock('@/components/layout/app-frame', () => ({
  AppFrame: ({ children }: { children: ReactNode }) => <main>{children}</main>,
}))
vi.mock('@/components/layout/app-sidebar', () => ({ AppSidebar: () => null }))
vi.mock('@/components/layout/sidebar', () => ({ AreasSheet: () => null }))
vi.mock('@/components/layout/phone-bar', () => ({ PhoneBar: () => null }))
vi.mock('@/components/layout/topbar', () => ({ Topbar: () => null }))
vi.mock('@/components/layout/command-menu', () => ({ CommandMenu: () => null }))
// The destination row is a shell decoration too; it reads the whole registry this test
// replaces with one view.
vi.mock('@/components/layout/destination-sections', () => ({
  DestinationSections: () => null,
}))
vi.mock('@/components/layout/shortcuts', () => ({
  GlobalShortcuts: () => null,
}))
// Only the frame is replaced; the shell also asks the real `frameFor` which frame a
// path gets (destination-sections.tsx), and a mock without it failed every render.
vi.mock('@/components/layout/page-frames', async (original) => ({
  ...(await original<typeof import('@/components/layout/page-frames')>()),
  RouteFrame: ({ children }: { children: ReactNode }) => <>{children}</>,
}))
vi.mock('@/features/navigation/personal-navigation', () => ({
  PersonalNavigationProvider: ({ children }: { children: ReactNode }) => (
    <>{children}</>
  ),
}))
vi.mock('@/features/navigation/permitted-visit', () => ({
  SettingsVisit: () => null,
  PermittedVisit: () => null,
}))

import { routeTree } from './routes'

async function visit(path: string) {
  const router = createRouter({
    routeTree,
    history: createMemoryHistory({ initialEntries: [path] }),
    defaultPendingMs: 0,
    defaultPendingMinMs: 0,
  })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  })
  await router.load()
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
  return router
}

beforeEach(() => {
  vi.spyOn(window, 'scrollTo').mockImplementation(() => {})
  authority.status = 'authenticated'
  authority.allowed = true
  authority.tenant = 'test-tenant'
})

afterEach(() => vi.restoreAllMocks())

it('does not import the authenticated shell while registering anonymous routes', () => {
  expect(chunk.loads).toBe(0)
})

it.each([
  ['/login', 'Login screen'],
  ['/setup', 'Setup screen'],
  ['/accept-invite?token=test-fixture', 'Invite screen'],
])(
  'opens %s without importing the authenticated shell',
  async (path, content) => {
    await visit(path)
    expect(await screen.findByText(content)).toBeInTheDocument()
    expect(chunk.loads).toBe(0)
  },
)

it('loads the shell on navigation from login to settings', async () => {
  const router = await visit('/login')
  expect(await screen.findByText('Login screen')).toBeInTheDocument()
  expect(chunk.loads).toBe(0)
  await act(() => router.navigate({ to: '/settings' }))
  expect(await screen.findByText('Settings screen')).toBeInTheDocument()
  expect(chunk.loads).toBe(1)
})

it('opens an authenticated settings deep link', async () => {
  await visit('/settings?tab=profile')
  expect(await screen.findByText('Settings screen')).toBeInTheDocument()
})

it.each(['anonymous', 'error'] as const)(
  'redirects a %s protected deep link to login',
  async (status) => {
    authority.status = status
    await visit('/audit')
    expect(await screen.findByText('Login screen')).toBeInTheDocument()
    expect(screen.queryByText('Protected audit screen')).not.toBeInTheDocument()
  },
)

it('keeps protected content unmounted while authentication loads', async () => {
  authority.status = 'loading'
  await visit('/audit')
  expect(await screen.findByRole('status')).toBeInTheDocument()
  expect(screen.queryByText('Protected audit screen')).not.toBeInTheDocument()
})

it('retains the permission check on a protected deep link', async () => {
  authority.allowed = false
  await visit('/audit')
  expect(await screen.findByText('Not authorized')).toBeInTheDocument()
  expect(screen.queryByText('Protected audit screen')).not.toBeInTheDocument()
})

it('opens protected content when authentication, tenant and permission allow it', async () => {
  await visit('/audit')
  expect(await screen.findByText('Protected audit screen')).toBeInTheDocument()
})

it('retains tenant admission after loading the shell', async () => {
  authority.tenant = null
  await visit('/audit')
  expect(await screen.findByText('No organization')).toBeInTheDocument()
  expect(screen.queryByText('Protected audit screen')).not.toBeInTheDocument()
  expect(screen.queryByText('Settings screen')).not.toBeInTheDocument()
})
