// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { queryKeys } from '@/lib/api/query'
import type { Whoami } from '@/lib/api/types'
import { useClientSettings } from '@/features/settings/preferences'
import { useTenantStore } from '@/stores/tenant'

const navigateMock = vi.fn()
vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => navigateMock,
  Navigate: ({ to }: { to: string }) => <div data-testid="redirect">{to}</div>,
  Link: ({ children, to }: { children?: ReactNode; to?: string }) => (
    <a href={to}>{children}</a>
  ),
}))

const auth = vi.hoisted(() => ({
  status: 'anonymous' as 'anonymous' | 'authenticated',
  login: vi.fn(async () => undefined),
  can: (_permission: string): boolean => true,
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => auth,
}))

vi.mock('@/lib/hooks/use-server-info', () => ({
  useServerInfo: () => ({ data: { setup_required: false, version: 'v' } }),
}))

vi.mock('@/features/identity/passkey-address', () => ({
  PasskeyAddressNotice: () => null,
}))

import { LoginPage } from './login'

beforeEach(() => {
  navigateMock.mockReset()
  auth.status = 'anonymous'
  auth.can = () => true
  auth.login.mockReset()
  auth.login.mockResolvedValue(undefined)
  localStorage.clear()
  useClientSettings.setState({
    startPage: 'home',
    confirmStop: true,
    clock: '24',
  })
  useTenantStore.setState({ activeTenant: null })
})

let queryClient: QueryClient

function renderLogin() {
  queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={queryClient}>
      <LoginPage />
    </QueryClientProvider>,
  )
}

async function signIn() {
  const user = userEvent.setup()
  renderLogin()
  await user.type(screen.getByLabelText('Email'), 'ada@example.com')
  await user.type(screen.getByLabelText('Password'), 'secret')
  await user.click(screen.getByRole('button', { name: 'Sign in' }))
  await waitFor(() => expect(navigateMock).toHaveBeenCalled())
}

describe('sign-in opens the stored start page', () => {
  it('opens a registry destination the operator may use', async () => {
    useClientSettings.setState({ startPage: 'sessions' })
    auth.can = (permission) => permission === 'sessions:live:read'
    await signIn()
    expect(navigateMock).toHaveBeenCalledWith({ to: '/sessions' })
  })

  it('falls back to / when the stored value is not a registry destination', async () => {
    useClientSettings.setState({
      startPage: 'not-a-view' as 'home',
    })
    await signIn()
    expect(navigateMock).toHaveBeenCalledWith({ to: '/' })
  })

  it('falls back to / when the operator may not open the stored destination', async () => {
    useClientSettings.setState({ startPage: 'sessions' })
    auth.can = () => false
    await signIn()
    expect(navigateMock).toHaveBeenCalledWith({ to: '/' })
  })

  it('uses the same choice when the operator is already signed in', () => {
    auth.status = 'authenticated'
    useClientSettings.setState({ startPage: 'deploy' })
    auth.can = (permission) => permission === 'deploy:deployment:read'
    renderLogin()
    expect(screen.getByTestId('redirect')).toHaveTextContent('/deploy')
  })

  it('uses the principal sign-in stored when the hook still denies', async () => {
    useClientSettings.setState({ startPage: 'sessions' })
    auth.can = () => false
    auth.login = vi.fn(async () => {
      queryClient.setQueryData(queryKeys.whoami, {
        kind: 'user',
        user_id: 'u-1',
        actor: 'ada',
        display_name: 'Ada',
        superadmin: false,
        grants: [
          {
            tenant: 't-1',
            role: 'viewer',
            permissions: ['sessions:live:read'],
          },
        ],
      } satisfies Whoami)
    })
    await signIn()
    expect(navigateMock).toHaveBeenCalledWith({ to: '/sessions' })
  })
})
