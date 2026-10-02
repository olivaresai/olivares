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
const loginSearch = vi.hoisted(() => ({
  returnTo: undefined as string | undefined,
}))
vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => navigateMock,
  useSearch: () => loginSearch,
  Navigate: ({ to, href }: { to?: string; href?: string }) => (
    <div data-testid="redirect">{href ?? to}</div>
  ),
  Link: ({ children, to }: { children?: ReactNode; to?: string }) => (
    <a href={to}>{children}</a>
  ),
}))

const auth = vi.hoisted(() => ({
  status: 'anonymous' as 'anonymous' | 'authenticated',
  login: vi.fn(
    async () =>
      ({ csrf_token: 't', session_id: 's', expires_at: 'soon' }) as const,
  ),
  adoptSession: vi.fn(async () => undefined),
  can: (_permission: string): boolean => true,
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => auth,
}))

const serverInfo = vi.hoisted(() => ({
  data: { setup_required: false, version: 'v' } as Record<string, unknown>,
}))
vi.mock('@/lib/hooks/use-server-info', () => ({
  useServerInfo: () => serverInfo,
}))

vi.mock('@/features/identity/passkey-address', () => ({
  PasskeyAddressNotice: () => null,
}))

import { LoginPage } from './login'

beforeEach(() => {
  navigateMock.mockReset()
  serverInfo.data = { setup_required: false, version: 'v' }
  loginSearch.returnTo = undefined
  auth.status = 'anonymous'
  auth.can = () => true
  auth.login.mockReset()
  auth.adoptSession.mockReset()
  auth.adoptSession.mockResolvedValue(undefined)
  auth.login.mockResolvedValue({
    csrf_token: 't',
    session_id: 's',
    expires_at: 'soon',
  })
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
      return { csrf_token: 't', session_id: 's', expires_at: 'soon' } as const
    })
    await signIn()
    expect(navigateMock).toHaveBeenCalledWith({ to: '/sessions' })
  })
})

describe('sign-in returns to the requested console page', () => {
  it('prefers the deep link, including query and fragment, over the saved start page', async () => {
    loginSearch.returnTo = '/audit?from=2026-09-30#entry-7'
    useClientSettings.setState({ startPage: 'sessions' })
    await signIn()
    expect(navigateMock).toHaveBeenCalledWith({
      to: '/audit',
      href: '/audit?from=2026-09-30#entry-7',
      replace: true,
    })
  })
  it.each([
    '//attacker.example',
    '/\\attacker.example',
    '/v1/auth/logout',
    '/login',
  ])(
    'uses the normal start page for an unsafe return path (%s)',
    async (path) => {
      loginSearch.returnTo = path
      await signIn()
      expect(navigateMock).toHaveBeenCalledWith({ to: '/' })
    },
  )
  it('keeps the requested page when the cookie already signed the person in', () => {
    auth.status = 'authenticated'
    loginSearch.returnTo = '/settings#appearance'
    renderLogin()
    expect(screen.getByTestId('redirect')).toHaveTextContent(
      '/settings#appearance',
    )
  })
})

describe('SSO sign-in buttons (server-info sso_providers)', () => {
  const START = '/v1/auth/federation/start?browser_session=1&return_to=%2F'

  it('shows no SSO button when the engine lists no provider', () => {
    renderLogin()
    expect(screen.queryByRole('separator', { name: 'or' })).toBeNull()
    expect(
      screen.queryByRole('link', { name: /single sign-on|sign in with/i }),
    ).toBeNull()
  })

  it('shows one button per provider above the password form, then a quiet "or"', () => {
    serverInfo.data = {
      ...serverInfo.data,
      sso_providers: [
        { label: 'Single sign-on', start_url: START },
        {
          label: 'Keycloak',
          start_url: START.replace('start?', 'start?idp=kc&'),
        },
      ],
    }
    renderLogin()
    const sso = screen.getByRole('link', { name: 'Single sign-on' })
    const kc = screen.getByRole('link', { name: 'Sign in with Keycloak' })
    expect(sso).toHaveAttribute('href', START)
    expect(kc.getAttribute('href')).toContain('idp=kc')
    expect(screen.getByRole('separator', { name: 'or' })).toBeInTheDocument()
    // Above the form: the buttons come first in document order.
    const email = screen.getByLabelText('Email')
    expect(
      kc.compareDocumentPosition(email) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy()
  })

  it('keeps the returnTo the password sign-in uses and always asks for the cookie session', () => {
    loginSearch.returnTo = '/sessions?tab=live'
    serverInfo.data = {
      ...serverInfo.data,
      sso_providers: [
        { label: 'Okta', start_url: '/v1/auth/federation/start?return_to=%2F' },
      ],
    }
    renderLogin()
    const href = screen
      .getByRole('link', { name: 'Sign in with Okta' })
      .getAttribute('href') as string
    const url = new URL(href, window.location.origin)
    expect(url.pathname).toBe('/v1/auth/federation/start')
    expect(url.searchParams.get('browser_session')).toBe('1')
    expect(url.searchParams.get('return_to')).toBe('/sessions?tab=live')
  })

  it('offers no button for a start address on another origin', () => {
    serverInfo.data = {
      ...serverInfo.data,
      sso_providers: [
        { label: 'Evil', start_url: 'https://idp.example/start' },
        { label: 'Also', start_url: '//idp.example/start' },
      ],
    }
    renderLogin()
    expect(screen.queryByRole('link', { name: /sign in with/i })).toBeNull()
    expect(screen.queryByRole('separator', { name: 'or' })).toBeNull()
  })
})

// The engine's email rule, not the browser's (Root, ID 2026-10-02): an internal-domain
// address the engine and the CLI accept signs in from the console too, and a malformed one
// is held here with the engine's own sentence.
describe('sign-in email: the engine rule', () => {
  it('an internal-domain address is sent to the engine', async () => {
    const user = userEvent.setup()
    renderLogin()
    await user.type(screen.getByLabelText('Email'), 'ops@corp.internal')
    await user.type(screen.getByLabelText('Password'), 'secret')
    await user.click(screen.getByRole('button', { name: /^sign in$/i }))
    await waitFor(() => expect(auth.login).toHaveBeenCalled())
    expect(screen.queryByText('Enter a valid email address.')).toBeNull()
  })

  it('"bad@" is not sent, and says why', async () => {
    const user = userEvent.setup()
    renderLogin()
    await user.type(screen.getByLabelText('Email'), 'bad@')
    await user.type(screen.getByLabelText('Password'), 'secret')
    await user.click(screen.getByRole('button', { name: /^sign in$/i }))
    expect(
      await screen.findByText('Enter a valid email address.'),
    ).toBeInTheDocument()
    expect(auth.login).not.toHaveBeenCalled()
  })
})
