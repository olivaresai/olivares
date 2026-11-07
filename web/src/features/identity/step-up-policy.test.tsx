// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from '@tanstack/react-router'
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { StepUpHost } from '@/components/layout/step-up-host'
import { PreferencesSync } from '@/features/console/preferences-sync'
import { PersonalNavigationProvider } from '@/features/navigation/personal-navigation'
import { configureApiClient } from '@/lib/api/client'
import { queryKeys } from '@/lib/api/query'
import { AuthProvider } from '@/lib/auth/context'
import { useSessionStore } from '@/stores/session'
import { useStepUpStore } from '@/stores/step-up'
import { useTenantStore } from '@/stores/tenant'
import { StepUpPolicySetting } from './step-up-policy'

// Raising the policy to Passkey from a password session: the engine answers
// step_up_required, the person passes the passkey ceremony, and the console
// sends the change again. The setting sits inside the shell's personal
// navigation and preference sync, as in the console, so the elevated whoami
// the ceremony publishes reaches them. Host, panel, whoami reader and API
// client are real; only the network and the authenticator are fixtures.
const bytes = new Uint8Array([1]).buffer
let elevated: boolean
let stored: string
let puts: number
const principal = () => ({
  kind: 'user',
  user_id: 'u1',
  actor: 'u1',
  display_name: 'Operator',
  superadmin: true,
  grants: [],
  aal: elevated ? 3 : 1,
  amr: elevated ? ['pwd', 'hwk'] : ['pwd'],
  admin_step_up: stored,
  step_up_satisfied: true,
})
const json = (value: unknown, status = 200) =>
  new Response(JSON.stringify(value), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })

beforeEach(() => {
  elevated = false
  stored = 'none'
  puts = 0
  useStepUpStore.getState().clear()
  useTenantStore.setState({ activeTenant: 't1' })
  useSessionStore
    .getState()
    .setSession({ csrfToken: 'fixture', sessionId: 'fixture', expiresAt: '' })
  configureApiClient({
    getToken: () => null,
    getCSRFToken: () => useSessionStore.getState().csrfToken,
    getTenant: () => useTenantStore.getState().activeTenant,
    onUnauthorized: vi.fn(),
    getExpiresAt: () => null,
    refreshSession: async () => false,
  })
  vi.stubGlobal('PublicKeyCredential', function PublicKeyCredential() {})
  Object.defineProperty(navigator, 'credentials', {
    configurable: true,
    value: {
      get: async () => ({
        id: 'key1',
        type: 'public-key',
        rawId: bytes,
        response: {
          clientDataJSON: bytes,
          authenticatorData: bytes,
          signature: bytes,
        },
      }),
    },
  })
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init?: RequestInit) => {
      if (url === '/v1/auth/whoami') return json(principal())
      if (url === '/v1/m/consoleviews/ui-state')
        return json({ stored: true, preferences: {} })
      if (url === '/v1/auth/piv/status') return json({ presented: false })
      if (url === '/v1/auth/webauthn/authenticate/options')
        return json({ publicKey: { challenge: 'AQ' } })
      if (url === '/v1/auth/webauthn/authenticate') {
        elevated = true
        return json({ ok: true, aal: 3 })
      }
      if (url === '/v1/auth/step-up-policy' && init?.method === 'PUT') {
        puts++
        if (!elevated)
          return json(
            { error: { code: 'step_up_required', message: 'fixture' } },
            403,
          )
        stored = JSON.parse(String(init.body)).admin_step_up
        return json({ admin_step_up: stored })
      }
      if (url === '/v1/auth/step-up-policy')
        return json({ admin_step_up: stored })
      throw new Error(`Unhandled fixture request: ${url}`)
    }),
  )
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

it('saves Passkey once the passkey step-up it asked for passes', async () => {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  qc.setQueryData(queryKeys.whoami, principal())
  const root = createRootRoute({
    component: () => (
      <AuthProvider>
        <PersonalNavigationProvider>
          <PreferencesSync>
            <Outlet />
          </PreferencesSync>
        </PersonalNavigationProvider>
        <StepUpHost />
      </AuthProvider>
    ),
  })
  const settings = createRoute({
    getParentRoute: () => root,
    path: '/settings',
    component: StepUpPolicySetting,
  })
  const router = createRouter({
    routeTree: root.addChildren([settings]),
    history: createMemoryHistory({ initialEntries: ['/settings'] }),
  })
  render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
  const user = userEvent.setup()
  await user.click(await screen.findByRole('radio', { name: 'Passkey' }))
  await user.click(
    await screen.findByRole('button', { name: 'Authenticate with passkey' }),
  )
  await waitFor(() => expect(puts).toBe(2))
  expect(stored).toBe('passkey')
  await waitFor(() =>
    expect(screen.getByRole('radio', { name: 'Passkey' })).toHaveAttribute(
      'aria-checked',
      'true',
    ),
  )
})
