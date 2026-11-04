// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRouter,
  Outlet,
  RouterProvider,
} from '@tanstack/react-router'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, expect, it, vi } from 'vitest'

vi.mock('@/lib/api/client', () => ({
  http: { post: vi.fn().mockResolvedValue(undefined) },
}))

const CHUNK_FAILURES = vi.hoisted(
  () =>
    [
      [
        'chunk-chromium',
        'Failed to fetch dynamically imported module: http://127.0.0.1/assets/a.js',
      ],
      [
        'chunk-firefox',
        'error loading dynamically imported module: http://127.0.0.1/assets/a.js',
      ],
      ['chunk-safari', 'Importing a module script failed.'],
      ['chunk-vite-css', 'Unable to preload CSS for /assets/a.css'],
    ] as const,
)

const authority = vi.hoisted(() => ({
  status: 'authenticated' as
    'authenticated' | 'anonymous' | 'loading' | 'error',
  allowed: true,
  tenant: 'test-tenant' as string | null,
}))

vi.mock('@/components/layout/app-layout', () => ({
  AppLayout: () => (
    <div>
      <header>Top bar</header>
      <nav>Navigation</nav>
      <main>
        <Outlet />
      </main>
    </div>
  ),
}))

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

const featureError = vi.hoisted(() => ({ message: 'render boom' }))

vi.mock('@/features/registry', async () => {
  const { lazy } = await import('react')
  // A real lazy view whose import rejects with each browser's own message.
  const chunkViews = CHUNK_FAILURES.map(([id, message]) => {
    const Failed = lazy(() => Promise.reject(new TypeError(message)))
    return {
      id,
      path: `/${id}`,
      navigation: {
        kind: 'feature',
        areaId: 'observation',
        sectionId: 'audit-recordings',
      },
      permission: 'audit:read',
      element: () => <Failed />,
    }
  })
  return {
    FEATURE_VIEWS: [
      ...chunkViews,
      {
        id: 'broken',
        path: '/broken',
        navigation: {
          kind: 'feature',
          areaId: 'observation',
          sectionId: 'audit-recordings',
        },
        permission: 'audit:read',
        element: () => {
          function BrokenView() {
            if (featureError.message) throw new Error(featureError.message)
            return <p>Recovered view</p>
          }
          return <BrokenView />
        },
      },
      {
        id: 'home',
        path: '/',
        navigation: {
          kind: 'feature',
          areaId: 'observation',
          sectionId: 'home',
        },
        permission: 'audit:read',
        element: () => <p>Home screen</p>,
      },
    ],
    NAV_AREAS: [],
    ROUTE_ALIASES: [],
    TAB_ALIASES: [],
  }
})
vi.mock('@/features/anonymous-registry', () => ({ ANONYMOUS_VIEWS: [] }))
vi.mock('./pages/login', () => ({ LoginPage: () => <p>Login screen</p> }))
vi.mock('./pages/setup', () => ({ SetupPage: () => <p>Setup screen</p> }))
vi.mock('./pages/accept-invite', () => ({
  AcceptInvitePage: () => <p>Invite screen</p>,
}))
vi.mock('./pages/settings', () => ({
  SettingsPage: () => <p>Settings screen</p>,
}))

import { http } from '@/lib/api/client'
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
  vi.mocked(http.post).mockClear()
  vi.spyOn(window, 'scrollTo').mockImplementation(() => {})
  authority.status = 'authenticated'
  authority.allowed = true
  authority.tenant = 'test-tenant'
  featureError.message = 'render boom'
})

it('keeps the shell when a feature view throws during render', async () => {
  const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
  try {
    await visit('/broken')

    expect(screen.getByText('Top bar')).toBeInTheDocument()
    expect(screen.getByText('Navigation')).toBeInTheDocument()
    expect(screen.getByText('This view crashed')).toBeInTheDocument()
    expect(
      screen.getByRole('heading', { name: 'This view crashed' }),
    ).toBeInTheDocument()
    expect(
      screen.queryByText(/render boom|private render detail/),
    ).not.toBeInTheDocument()
  } finally {
    errorSpy.mockRestore()
  }
})

it.each(['/no-such-page', '/automations'])(
  'keeps the shell and a way back for %s, an address this console does not serve',
  async (path) => {
    await visit(path)

    expect(screen.getByText('Top bar')).toBeInTheDocument()
    expect(screen.getByText('Navigation')).toBeInTheDocument()
    expect(screen.getByRole('main')).toContainElement(
      screen.getByRole('link', { name: 'Go to Now' }),
    )
  },
)

it('re-mounts on Retry without reloading and navigates home with Go to Now', async () => {
  const user = userEvent.setup()
  const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
  const reload = vi.fn()
  const realLocation = window.location
  Object.defineProperty(window, 'location', {
    configurable: true,
    value: { ...realLocation, reload },
  })
  const router = await visit('/broken')

  try {
    featureError.message = ''
    await user.click(screen.getByRole('button', { name: 'Retry' }))
    expect(await screen.findByText('Recovered view')).toBeInTheDocument()
    expect(reload).not.toHaveBeenCalled()

    await router.navigate({ to: '/' })
    expect(await screen.findByText('Home screen')).toBeInTheDocument()
    featureError.message = 'render boom'
    await router.navigate({ to: '/broken' as never })
    expect(await screen.findByText('This view crashed')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Go to Now' }))
    expect(await screen.findByText('Home screen')).toBeInTheDocument()
    expect(router.state.location.pathname).toBe('/')
  } finally {
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: realLocation,
    })
    errorSpy.mockRestore()
  }
})

it('shows a copyable error id and never the raw error', async () => {
  const user = userEvent.setup()
  const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
  const writeText = vi.fn().mockResolvedValue(undefined)
  Object.defineProperty(navigator, 'clipboard', {
    value: { writeText },
    configurable: true,
  })

  try {
    await visit('/broken')

    const errorId = screen.getByText(/Error ID:/i)
    expect(errorId).toBeInTheDocument()
    expect(errorId.textContent).toMatch(/Error ID:\s+[a-f0-9-]{8,}/i)

    const copy = screen.getByRole('button', { name: 'Copy error ID' })
    await user.click(copy)
    expect(writeText).toHaveBeenCalledTimes(1)
  } finally {
    errorSpy.mockRestore()
    Object.defineProperty(navigator, 'clipboard', {
      value: undefined,
      configurable: true,
    })
  }
})

it('logs the error and component stack once', async () => {
  const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
  try {
    await visit('/broken')
    expect(errorSpy).toHaveBeenCalled()
    const boundaryCalls = errorSpy.mock.calls.filter((call) =>
      String(call[0]).includes('[olivares boundary error'),
    )
    expect(boundaryCalls).toHaveLength(1)
    expect(boundaryCalls[0]?.[1]).toBeInstanceOf(Error)
    expect(String(boundaryCalls[0]?.[2])).toContain('BrokenView')
    expect(http.post).toHaveBeenCalledTimes(1)
    expect(http.post).toHaveBeenCalledWith(
      '/v1/console/errors',
      expect.objectContaining({
        error_id: expect.stringMatching(/^[0-9a-f]{8}$/),
        error: expect.stringContaining('render boom'),
        component_stack: expect.stringContaining('BrokenView'),
        route_path: expect.any(String),
      }),
      { sessionEffects: 'none' },
    )
  } finally {
    errorSpy.mockRestore()
  }
})

it.each(CHUNK_FAILURES)(
  'reloads the document when a view chunk fails to load (%s)',
  async (id) => {
    const user = userEvent.setup()
    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    const reload = vi.fn()
    const realLocation = window.location
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: { ...realLocation, reload },
    })

    try {
      await visit(`/${id}`)
      const retry = await screen.findByRole('button', { name: 'Retry' })
      await user.click(retry)
      expect(reload).toHaveBeenCalledTimes(1)
    } finally {
      Object.defineProperty(window, 'location', {
        configurable: true,
        value: realLocation,
      })
      errorSpy.mockRestore()
    }
  },
)
