// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, expect, it, vi } from 'vitest'
import { consoleApi } from '@/features/console/api'
import { http } from '@/lib/api'
import { ApiError } from '@/lib/api/errors'
import { identityApi } from '@/features/identity/api'
import { StepUpPolicySetting } from '@/features/identity/step-up-policy'
import { SettingsPage } from './settings'

const oneKey = vi.hoisted(() => ({
  items: [
    {
      id: 'key-one',
      name: 'My laptop',
      created_at: '2026-10-01T00:00:00Z',
    },
  ],
}))
const state = vi.hoisted(() => ({
  admin: true,
  satisfied: true,
  amr: ['pwd'] as string[],
}))
vi.mock('@tanstack/react-router', async () => ({
  useNavigate: () => vi.fn(),
  useRouter: () => undefined,
  useRouterState: () => undefined,
  Link: (await import('@/test/fake-router')).fakeRouterModule().Link,
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    principal: {
      kind: 'user',
      user_id: 'user-one',
      aal: 1,
      amr: state.amr,
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
    webauthnCredentials: vi.fn(async () => oneKey),
    authSessions: vi.fn(async () => ({ items: [] })),
    authSessionDelete: vi.fn(async () => undefined),
    pairSession: vi.fn(async () => ({
      token: 'one-time-secret',
      expires_at: '2026-10-28T00:00:00Z',
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
  state.amr = ['pwd']
  vi.mocked(identityApi.webauthnCredentials).mockResolvedValue(oneKey)
  vi.mocked(identityApi.authSessions).mockClear()
  vi.mocked(identityApi.authSessions).mockResolvedValue({ items: [] })
  vi.mocked(identityApi.authSessionDelete).mockClear()
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
    await userEvent.click(screen.getByRole('button', { name: 'Sign-in' }))
    expect(await screen.findByText('My laptop')).toBeInTheDocument()
    expect(screen.getByText('Session lifetime')).toBeInTheDocument()
    expect(screen.getByText('12 hours')).toBeInTheDocument()
    expect(screen.getByText('Password', { selector: 'p' })).toBeInTheDocument()
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

it('lists and ends other sign-ins without ending the current one', async () => {
  let sessions = [
    {
      id: 'session-current',
      created_at: '2026-10-24T09:00:00Z',
      last_used_at: '2026-10-24T10:00:00Z',
      kind: 'browser' as const,
      current: true,
      expires_at: '2026-10-25T09:00:00Z',
    },
    {
      id: 'session-other',
      created_at: '2026-10-23T09:00:00Z',
      last_used_at: '2026-10-24T08:00:00Z',
      kind: 'token' as const,
      current: false,
      expires_at: '2026-10-24T09:00:00Z',
    },
    {
      id: 'session-other-two',
      created_at: '2026-10-22T09:00:00Z',
      last_used_at: null,
      kind: 'unknown' as const,
      current: false,
      expires_at: '2026-10-23T09:00:00Z',
    },
  ]
  vi.mocked(identityApi.authSessions).mockImplementation(async () => ({
    items: sessions,
  }))
  vi.mocked(identityApi.authSessionDelete).mockImplementation(async (id) => {
    sessions = sessions.filter((session) => session.id !== id)
  })
  const user = userEvent.setup()
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <SettingsPage />
    </QueryClientProvider>,
  )
  await user.click(screen.getByRole('button', { name: 'Sign-in' }))

  expect(await screen.findByText('Your sign-ins')).toBeInTheDocument()
  expect(screen.getByText('Browser')).toBeInTheDocument()
  expect(screen.getByText('Token')).toBeInTheDocument()
  expect(screen.getByText('This session')).toBeInTheDocument()

  await user.click(
    screen.getByRole('button', { name: /^End Token sign-in from / }),
  )
  let dialog = await screen.findByRole('dialog', { name: 'End sign-in?' })
  await user.click(within(dialog).getByRole('button', { name: 'End' }))
  await waitFor(() =>
    expect(identityApi.authSessionDelete).toHaveBeenCalledWith('session-other'),
  )

  await user.click(screen.getByRole('button', { name: 'End all others' }))
  dialog = await screen.findByRole('dialog', {
    name: 'End all other sign-ins?',
  })
  await user.click(
    within(dialog).getByRole('button', { name: 'End all others' }),
  )
  await waitFor(() =>
    expect(identityApi.authSessionDelete).toHaveBeenCalledWith(
      'session-other-two',
    ),
  )
  expect(identityApi.authSessionDelete).not.toHaveBeenCalledWith(
    'session-current',
  )
  expect(screen.getByText('This session')).toBeInTheDocument()
})

it('waits for every end-all request before reporting partial failure', async () => {
  let sessions = [
    {
      id: 'session-current',
      created_at: '2026-10-24T09:00:00Z',
      last_used_at: '2026-10-24T10:00:00Z',
      kind: 'browser' as const,
      current: true,
      expires_at: '2026-10-25T09:00:00Z',
    },
    {
      id: 'session-slow',
      created_at: '2026-10-23T09:00:00Z',
      last_used_at: '2026-10-24T08:00:00Z',
      kind: 'token' as const,
      current: false,
      expires_at: '2026-10-24T09:00:00Z',
    },
    {
      id: 'session-failure',
      created_at: '2026-10-22T09:00:00Z',
      last_used_at: null,
      kind: 'unknown' as const,
      current: false,
      expires_at: '2026-10-23T09:00:00Z',
    },
  ]
  vi.mocked(identityApi.authSessions).mockImplementation(async () => ({
    items: sessions,
  }))
  vi.mocked(identityApi.authSessionDelete).mockImplementation(async (id) => {
    if (id === 'session-failure') throw new Error('offline')
    await new Promise<void>((resolve) => {
      setTimeout(() => {
        sessions = sessions.filter((session) => session.id !== id)
        resolve()
      }, 25)
    })
  })
  const user = userEvent.setup()
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <SettingsPage />
    </QueryClientProvider>,
  )
  await user.click(screen.getByRole('button', { name: 'Sign-in' }))
  await user.click(
    await screen.findByRole('button', { name: 'End all others' }),
  )
  const dialog = await screen.findByRole('dialog', {
    name: 'End all other sign-ins?',
  })
  await user.click(
    within(dialog).getByRole('button', { name: 'End all others' }),
  )

  expect(await screen.findByRole('alert')).toHaveTextContent(
    '1 sign-in could not be ended. Review the list and try again.',
  )
  await waitFor(() => expect(identityApi.authSessions).toHaveBeenCalledTimes(2))
  expect(identityApi.authSessionDelete).toHaveBeenCalledWith('session-slow')
  expect(identityApi.authSessionDelete).toHaveBeenCalledWith('session-failure')
  expect(identityApi.authSessionDelete).not.toHaveBeenCalledWith(
    'session-current',
  )
  expect(screen.queryByText('Sign-in ended.')).not.toBeInTheDocument()
  expect(
    screen.queryByRole('button', { name: /^End Token sign-in from / }),
  ).not.toBeInTheDocument()
  expect(
    screen.getByRole('button', { name: /^End Unknown sign-in from / }),
  ).toBeInTheDocument()
  expect(screen.getByText('This session')).toBeInTheDocument()
})

const signIns = (kinds: Array<'token' | 'unknown'>) => [
  {
    id: 'session-current',
    created_at: '2026-10-24T09:00:00Z',
    last_used_at: '2026-10-24T10:00:00Z',
    kind: 'browser' as const,
    current: true,
    expires_at: '2026-10-25T09:00:00Z',
  },
  ...kinds.map((kind, i) => ({
    id: `session-${kind}-${i}`,
    created_at: '2026-10-23T09:00:00Z',
    last_used_at: null,
    kind,
    current: false,
    expires_at: '2026-10-24T09:00:00Z',
  })),
]

async function openSignIn() {
  const user = userEvent.setup()
  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <SettingsPage />
    </QueryClientProvider>,
  )
  await user.click(screen.getByRole('button', { name: 'Sign-in' }))
  await screen.findByText('Your sign-ins')
  return user
}

it('treats a sign-in already ended elsewhere as ended', async () => {
  let sessions = signIns(['token', 'unknown'])
  vi.mocked(identityApi.authSessions).mockImplementation(async () => ({
    items: sessions,
  }))
  vi.mocked(identityApi.authSessionDelete).mockImplementation(async (id) => {
    sessions = sessions.filter((session) => session.id !== id)
    if (id === 'session-token-0')
      throw new ApiError(404, 'not_found', 'not found')
  })
  const user = await openSignIn()
  await user.click(
    await screen.findByRole('button', { name: 'End all others' }),
  )
  const dialog = await screen.findByRole('dialog', {
    name: 'End all other sign-ins?',
  })
  await user.click(
    within(dialog).getByRole('button', { name: 'End all others' }),
  )
  await waitFor(() =>
    expect(
      screen.queryByRole('button', { name: 'End all others' }),
    ).not.toBeInTheDocument(),
  )
  expect(screen.queryByRole('alert')).not.toBeInTheDocument()
})

it('closes the dialog, reports the failure and refreshes when ending one sign-in fails', async () => {
  const sessions = signIns(['token'])
  vi.mocked(identityApi.authSessions).mockImplementation(async () => ({
    items: sessions,
  }))
  vi.mocked(identityApi.authSessionDelete).mockRejectedValue(
    new ApiError(500, 'internal', 'The engine could not end that sign-in.'),
  )
  const user = await openSignIn()
  await user.click(
    await screen.findByRole('button', { name: /^End Token sign-in from / }),
  )
  const dialog = await screen.findByRole('dialog', { name: 'End sign-in?' })
  await user.click(within(dialog).getByRole('button', { name: 'End' }))
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'The engine could not end that sign-in.',
  )
  expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  await waitFor(() => expect(identityApi.authSessions).toHaveBeenCalledTimes(2))
})

it('offers no End all others when the list cannot be refreshed', async () => {
  let sessions = signIns(['token', 'unknown'])
  vi.mocked(identityApi.authSessions)
    .mockResolvedValueOnce({ items: sessions })
    .mockRejectedValue(new Error('offline'))
  vi.mocked(identityApi.authSessionDelete).mockImplementation(async (id) => {
    sessions = sessions.filter((session) => session.id !== id)
  })
  const user = await openSignIn()
  await user.click(
    await screen.findByRole('button', { name: /^End Token sign-in from / }),
  )
  const dialog = await screen.findByRole('dialog', { name: 'End sign-in?' })
  await user.click(within(dialog).getByRole('button', { name: 'End' }))
  await waitFor(() =>
    expect(
      screen.queryByRole('button', { name: 'End all others' }),
    ).not.toBeInTheDocument(),
  )
  expect(
    screen.queryByRole('button', { name: /^End Unknown sign-in from / }),
  ).not.toBeInTheDocument()
})

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
  await userEvent.click(screen.getByRole('button', { name: 'Sign-in' }))
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
  await userEvent.click(screen.getByRole('button', { name: 'Sign-in' }))
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
  await userEvent.click(screen.getByRole('button', { name: 'Sign-in' }))
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
  await userEvent.click(screen.getByRole('button', { name: 'Sign-in' }))
  expect(
    await screen.findByText(
      'Single sign-on is requested, but its identity provider is inactive. This requirement does not block password sign-in.',
    ),
  ).toBeInTheDocument()
})

it('creates a named one-time device link and shows the paired device name', async () => {
  vi.mocked(identityApi.authSessions).mockResolvedValue({
    items: [
      {
        id: 'paired-device',
        name: 'Lost phone',
        created_at: '2026-10-27T00:00:00Z',
        last_used_at: null,
        kind: 'token',
        current: false,
        expires_at: '2026-10-28T00:00:00Z',
      },
    ],
  })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <SettingsPage />
    </QueryClientProvider>,
  )
  await userEvent.click(screen.getByRole('button', { name: 'Sign-in' }))
  expect(await screen.findByText('Lost phone')).toBeInTheDocument()
  // Nothing has been typed yet: the empty field is not an error, and the
  // button is a normal one. The browser itself refuses an empty submit.
  expect(screen.queryByText(/This field is required/)).not.toBeInTheDocument()
  // A static title would show "required" on hover even once a name is typed.
  expect(screen.getByLabelText('Device name')).not.toHaveAttribute('title')
  expect(screen.getByRole('button', { name: 'Pair a device' })).toBeEnabled()
  await userEvent.click(screen.getByRole('button', { name: 'Pair a device' }))
  expect(identityApi.pairSession).not.toHaveBeenCalled()
  await userEvent.type(screen.getByLabelText('Device name'), 'My phone')
  await userEvent.click(screen.getByRole('button', { name: 'Pair a device' }))
  expect(await screen.findByLabelText('One-time link')).toHaveValue(
    `${window.location.origin}/pair#pair=one-time-secret`,
  )
  expect(identityApi.pairSession).toHaveBeenCalledWith('My phone')
})

async function openSignInSettings() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <SettingsPage />
    </QueryClientProvider>,
  )
  await userEvent.click(screen.getByRole('button', { name: 'Sign-in' }))
}

it('names the session assurance in plain words and never points back at its own page', async () => {
  // No passkey yet, so the register hint is on the page too.
  vi.mocked(identityApi.webauthnCredentials).mockResolvedValue({ items: [] })
  await openSignInSettings()
  expect(await screen.findByText('Standard sign-in')).toBeInTheDocument()
  expect(screen.getByText(/Register a passkey/)).toBeInTheDocument()
  expect(screen.getAllByText('Password').length).toBeGreaterThan(1)
  expect(document.body).not.toHaveTextContent(/AAL\d/)
  expect(screen.queryByText('pwd')).not.toBeInTheDocument()
  expect(
    screen.getByText(/asks for no extra check before administrative actions/),
  ).not.toHaveTextContent('Sign-in')
})

it('names no standard-body level in the passkey delete prompt', async () => {
  await openSignInSettings()
  await userEvent.click(await screen.findByRole('button', { name: 'Delete' }))
  expect(
    await screen.findByText(/another registered passkey/),
  ).not.toHaveTextContent(/AAL\d/)
})

// The level is stored for every primary sign-in, so its words must hold for
// each of these sessions, not only a password one.
it.each([
  [['pwd', 'totp'], 'Authenticator code'],
  [['sso'], 'Single sign-on'],
  [['external'], 'External sign-in'],
  [['pairing'], 'Paired device'],
])('does not claim a password for a %s session', async (amr, method) => {
  state.amr = amr
  await openSignInSettings()
  // The method name can also title a section of the page, so allow several.
  expect((await screen.findAllByText(method)).length).toBeGreaterThan(0)
  expect(screen.queryByText(/Password only/)).not.toBeInTheDocument()
  expect(screen.getByText('Standard sign-in')).toBeInTheDocument()
})

it('tells apart two browser sign-ins by the address they came from', async () => {
  vi.mocked(identityApi.authSessions).mockResolvedValue({
    items: ['203.0.113.7', '198.51.100.9'].map((ip, i) => ({
      id: `browser-${i}`,
      name: '',
      created_at: '2026-10-27T00:00:00Z',
      last_used_at: null,
      kind: 'browser' as const,
      created_ip: ip,
      current: false,
      expires_at: '2026-10-28T00:00:00Z',
    })),
  })
  await openSignInSettings()
  expect(await screen.findByText('203.0.113.7')).toBeInTheDocument()
  expect(
    screen.getByRole('button', { name: /End Browser 198\.51\.100\.9 sign-in/ }),
  ).toBeInTheDocument()
})
