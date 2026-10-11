// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createElement } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import i18n from '@/lib/i18n'
import { AuthProvider } from '@/lib/auth/context'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'

vi.mock('./extensions', async () => {
  const { default: i18n } = await import('@/lib/i18n')
  i18n.addResource(
    'en',
    'auth',
    'extensionFixture.signIn',
    'Sign in with fixture',
  )
  i18n.addResource(
    'es',
    'auth',
    'extensionFixture.signIn',
    'Acceder con ejemplo',
  )
  return {
    PANEL_EXTENSIONS: {
      capabilitiesTabs: [],
      complianceTabs: [],
      reportingCards: [],
      licenseCards: [],
      scopesCards: [],
    },
    FEATURE_EXTENSIONS: [],
    EXTENSION_ROUTES: [],
    ANONYMOUS_FEATURE_EXTENSIONS: [
      {
        id: 'anonymous-extension-fixture',
        path: '/sign-in-fixture',
        element: () =>
          createElement(
            'main',
            null,
            createElement('h1', null, 'Fixture sign-in'),
          ),
        loginLabel: () => i18n.t('auth:extensionFixture.signIn'),
      },
      {
        id: 'public-extension-fixture',
        path: '/public-fixture',
        element: () =>
          createElement(
            'main',
            null,
            createElement('h1', null, 'Public fixture'),
          ),
      },
    ],
    ANONYMOUS_EXTENSION_ROUTES: [
      {
        id: 'anonymous-extension-fixture',
        path: '/sign-in-fixture',
        heading: 'Fixture sign-in',
      },
      {
        id: 'public-extension-fixture',
        path: '/public-fixture',
        heading: 'Public fixture',
      },
    ],
  }
})

vi.mock('@/lib/hooks/use-server-info', () => ({
  useServerInfo: () => ({
    data: { setup_required: false, version: 'fixture' },
  }),
}))

import { routeTree } from '@/app/routes'

let client: QueryClient
let fetchSpy: ReturnType<typeof vi.spyOn>
let scrollSpy: ReturnType<typeof vi.spyOn>

beforeEach(async () => {
  useSessionStore.getState().clear()
  useTenantStore.getState().clear()
  await i18n.changeLanguage('en')
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  fetchSpy = vi
    .spyOn(globalThis, 'fetch')
    .mockRejectedValue(new Error('Unexpected request'))
  scrollSpy = vi.spyOn(window, 'scrollTo').mockImplementation(() => {})
})

afterEach(async () => {
  client.clear()
  fetchSpy.mockRestore()
  scrollSpy.mockRestore()
  await i18n.changeLanguage('en')
})

function open(path: string) {
  const router = createRouter({
    routeTree,
    history: createMemoryHistory({ initialEntries: [path] }),
  })
  render(
    <QueryClientProvider client={client}>
      <AuthProvider>
        <RouterProvider router={router} />
      </AuthProvider>
    </QueryClientProvider>,
  )
  return router
}

describe('anonymous extension sign-in choices', () => {
  it('renders a direct anonymous route without requesting identity or protected data', async () => {
    const router = open('/sign-in-fixture')
    expect(
      await screen.findByRole('heading', { name: 'Fixture sign-in' }),
    ).toBeVisible()
    expect(router.state.location.pathname).toBe('/sign-in-fixture')
    expect(fetchSpy).not.toHaveBeenCalled()
  })

  it('adds an ordinary internal link after the local sign-in form', async () => {
    const router = open('/login')
    const choice = await screen.findByRole('link', {
      name: 'Sign in with fixture',
    })
    expect(choice).toHaveAttribute('href', '/sign-in-fixture')
    expect(choice.closest('form')).toBeNull()
    expect(screen.getByRole('button', { name: 'Sign in' })).toBeVisible()
    expect(
      screen.queryByRole('link', { name: 'Public fixture' }),
    ).not.toBeInTheDocument()
    await userEvent.setup().click(choice)
    expect(
      await screen.findByRole('heading', { name: 'Fixture sign-in' }),
    ).toBeVisible()
    expect(router.state.location.pathname).toBe('/sign-in-fixture')
    expect(fetchSpy).not.toHaveBeenCalled()
  })

  it('evaluates the choice label again when the native login language changes', async () => {
    open('/login')
    expect(
      await screen.findByRole('link', { name: 'Sign in with fixture' }),
    ).toBeVisible()
    await i18n.changeLanguage('es')
    expect(
      await screen.findByRole('link', { name: 'Acceder con ejemplo' }),
    ).toBeVisible()
    expect(
      screen.queryByRole('link', { name: 'Sign in with fixture' }),
    ).not.toBeInTheDocument()
  })

  it('mounts public extensions that do not offer a login choice', async () => {
    open('/public-fixture')
    expect(
      await screen.findByRole('heading', { name: 'Public fixture' }),
    ).toBeVisible()
    expect(fetchSpy).not.toHaveBeenCalled()
  })

  it('retains the native anonymous refusal on an authenticated feature route', async () => {
    const router = open('/providers')
    expect(
      await screen.findByRole('link', { name: 'Sign in with fixture' }),
    ).toBeVisible()
    await waitFor(() => expect(router.state.location.pathname).toBe('/login'))
    expect(fetchSpy).not.toHaveBeenCalled()
  })
})
