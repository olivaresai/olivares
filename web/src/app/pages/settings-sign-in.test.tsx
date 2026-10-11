// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, expect, it, vi } from 'vitest'
import { consoleApi } from '@/features/console/api'
import { http } from '@/lib/api'
import { identityApi } from '@/features/identity/api'
import { StepUpPolicySetting } from '@/features/identity/step-up-policy'
import { SettingsPage } from './settings'

const state = vi.hoisted(() => ({ admin: true, satisfied: true }))
vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => vi.fn(),
  useRouterState: () => undefined,
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    principal: {
      kind: 'user',
      user_id: 'user-one',
      aal: 1,
      amr: ['pwd'],
      admin_step_up: 'none',
      step_up_satisfied: state.satisfied,
      session_ttl_seconds: 43200,
    },
    isSuperadmin: state.admin,
    can: () => state.admin,
    activeTenant: 'tenant-one',
    grants: [],
  }),
}))
vi.mock('@/features/console/api', async (original) => ({
  ...(await original<typeof import('@/features/console/api')>()),
  consoleApi: {
    getSSO: vi.fn(async () => ({
      require_sso: false,
      enforced_by: 'unavailable',
    })),
  },
}))
vi.mock('@/lib/hooks/use-server-info', () => ({
  useServerInfo: () => ({ data: { sso_providers: [] } }),
}))
vi.mock('@/features/identity/api', async (original) => ({
  ...(await original<typeof import('@/features/identity/api')>()),
  identityApi: {
    pivStatus: vi.fn(async () => ({ configured: false, presented: false })),
    webauthnCredentials: vi.fn(async () => ({
      items: [
        {
          id: 'key-one',
          name: 'My laptop',
          created_at: '2026-10-01T00:00:00Z',
        },
      ],
    })),
  },
  totpApi: {
    status: vi.fn(async () => ({
      enrolled: false,
      recovery_codes_remaining: 0,
    })),
    policy: vi.fn(async () => ({ require_for_admins: false })),
  },
}))
vi.mock('@/lib/api', async (original) => ({
  ...(await original<typeof import('@/lib/api')>()),
  http: { get: vi.fn(async () => ({ admin_step_up: 'none' })) },
}))

beforeEach(() => {
  state.admin = true
  state.satisfied = true
  window.history.replaceState(null, '', '/settings')
})

it.each([true, false])(
  'shows real sign-in settings and own passkeys (administrator=%s)',
  async (admin) => {
    state.admin = admin
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    render(
      <QueryClientProvider client={client}>
        <SettingsPage />
      </QueryClientProvider>,
    )
    await userEvent.click(
      screen.getByRole('button', { name: 'Sign-in and security' }),
    )
    expect(await screen.findByText('My laptop')).toBeInTheDocument()
    expect(screen.getByText('Session lifetime')).toBeInTheDocument()
    expect(screen.getByText('12 hours')).toBeInTheDocument()
    expect(screen.getByText('Password')).toBeInTheDocument()
    expect(
      screen.queryByText(/not edited from this screen yet/),
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole('radiogroup', {
        name: 'Extra check for administrative actions',
      }) !== null,
    ).toBe(admin)
  },
)

it('does not report Off or allow changes when the current policy cannot be read', async () => {
  vi.mocked(http.get).mockRejectedValueOnce(new Error('offline'))
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <StepUpPolicySetting />
    </QueryClientProvider>,
  )
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'could not be read',
  )
  expect(screen.queryByRole('radio', { name: 'Off' })).not.toBeInTheDocument()
})

it('does not report an empty passkey list when reading credentials fails', async () => {
  vi.mocked(identityApi.webauthnCredentials).mockRejectedValueOnce(
    new Error('offline'),
  )
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <SettingsPage />
    </QueryClientProvider>,
  )
  await userEvent.click(
    screen.getByRole('button', { name: 'Sign-in and security' }),
  )
  expect(
    await screen.findByRole('button', { name: /retry/i }),
  ).toBeInTheDocument()
  expect(screen.queryByText('No passkeys registered')).not.toBeInTheDocument()
  expect(
    screen.queryByRole('button', { name: 'Register passkey' }),
  ).not.toBeInTheDocument()
})

it('reports the configured password restriction instead of assuming password sign-in', async () => {
  vi.mocked(consoleApi.getSSO).mockResolvedValueOnce({
    require_sso: true,
    enforced_by: 'enterprise',
    status: 'active',
    protocol: 'oidc',
  } as Awaited<ReturnType<typeof consoleApi.getSSO>>)
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <SettingsPage />
    </QueryClientProvider>,
  )
  await userEvent.click(
    screen.getByRole('button', { name: 'Sign-in and security' }),
  )
  expect(
    await screen.findByText(
      'Single sign-on is required. Local password sign-in is blocked.',
    ),
  ).toBeInTheDocument()
})

it('offers the existing step-up remedy alongside factor management', async () => {
  state.satisfied = false
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <SettingsPage />
    </QueryClientProvider>,
  )
  await userEvent.click(
    screen.getByRole('button', { name: 'Sign-in and security' }),
  )
  expect(
    await screen.findByRole('button', { name: 'Authenticate with passkey' }),
  ).toBeInTheDocument()
})

it('does not claim a disabled identity provider blocks password sign-in', async () => {
  vi.mocked(consoleApi.getSSO).mockResolvedValueOnce({
    require_sso: true,
    enforced_by: 'enterprise',
    status: 'disabled',
    protocol: 'oidc',
  } as Awaited<ReturnType<typeof consoleApi.getSSO>>)
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <SettingsPage />
    </QueryClientProvider>,
  )
  await userEvent.click(
    screen.getByRole('button', { name: 'Sign-in and security' }),
  )
  expect(
    await screen.findByText(
      'Single sign-on is requested, but its identity provider is inactive. This requirement does not block password sign-in.',
    ),
  ).toBeInTheDocument()
})
