// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// the regression: a fresh /login on an engine with consoleviews off adopts the session
// before the authenticated shell (app-layout) mounts. The module list must already be
// known there, or the sign-in reads and saves a module that answers module_not_enabled.
import { act, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Whoami } from '@/lib/api/types'

vi.mock('@/components/ui/toaster', () => ({
  toast: Object.assign(vi.fn(), { dismiss: vi.fn() }),
  Toaster: () => null,
}))
vi.mock('@/lib/auth/browser-session', () => ({
  // The page-load restore reads server-info first (providers.tsx); the rest is not under test.
  restoreOnPageLoad: vi.fn((serverInfo: () => Promise<unknown>) =>
    serverInfo().then(() => undefined),
  ),
  retryWhileUnreachable: vi.fn().mockResolvedValue(undefined),
}))
vi.mock('@/features/agentops/auth-boundary', () => ({
  AuthBoundaryCustody: () => null,
}))
vi.mock('@/components/layout/step-up-host', () => ({
  StepUpHost: () => null,
}))

import { Providers } from '@/app/providers'
import { useAuth } from '@/lib/auth/context'
import { useModulesStore } from '@/stores/modules'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'

const json = (body: unknown) =>
  Promise.resolve(
    new Response(JSON.stringify(body), {
      status: 200,
      headers: { 'content-type': 'application/json' },
    }),
  )
const user: Whoami = {
  kind: 'user',
  user_id: 'fresh-user',
  actor: 'fresh-user',
  display_name: 'Fresh User',
  superadmin: false,
  grants: [{ tenant: 'org-a', role: 'admin' }] as Whoami['grants'],
}
let paths: string[]

function SignIn() {
  const { adoptSession, principal } = useAuth()
  return (
    <>
      <output data-testid="principal">
        {principal?.user_id ?? 'anonymous'}
      </output>
      <button
        onClick={() =>
          void adoptSession({
            csrf_token: 'olvs_fresh',
            session_id: 'fresh-session',
            expires_at: new Date(Date.now() + 3_600_000).toISOString(),
          })
        }
      >
        Sign in
      </button>
    </>
  )
}

beforeEach(() => {
  paths = []
  vi.spyOn(globalThis, 'fetch').mockImplementation((url) => {
    const path = String(url)
    paths.push(path)
    if (path === '/v1/server-info')
      return json({
        setup_required: false,
        modules_not_enabled: ['consoleviews'],
        communication_ready: true,
      })
    if (path === '/v1/auth/whoami') return json(user)
    throw new Error(`Unexpected synthetic request: ${path}`)
  })
})
afterEach(() => {
  vi.restoreAllMocks()
  useModulesStore.setState({ off: new Set() })
  useSessionStore.getState().clear()
  useTenantStore.getState().clear()
})

describe('a fresh sign-in with consoleviews off', () => {
  it('reads and saves nothing in consoleviews, before any shell has mounted', async () => {
    render(
      <Providers>
        <SignIn />
      </Providers>,
    )
    // /login has loaded server-info by the time anyone types a password.
    await waitFor(() =>
      expect(useModulesStore.getState().off.has('consoleviews')).toBe(true),
    )
    await act(async () =>
      screen.getByRole('button', { name: 'Sign in' }).click(),
    )
    await waitFor(() =>
      expect(screen.getByTestId('principal').textContent).toBe('fresh-user'),
    )
    expect(paths.filter((p) => p.startsWith('/v1/m/consoleviews'))).toEqual([])
  })
})
