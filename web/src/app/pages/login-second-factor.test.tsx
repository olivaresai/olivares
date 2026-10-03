// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { useClientSettings } from '@/features/settings/preferences'
import { useTenantStore } from '@/stores/tenant'
import { SecondFactorPanel } from '@/features/identity/totp-login'

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
  login: vi.fn(),
  adoptSession: vi.fn(async () => undefined),
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

const totp = vi.hoisted(() => ({
  enrol: vi.fn(),
  activate: vi.fn(),
  challenge: vi.fn(),
}))
vi.mock('@/features/identity/api', () => ({
  totpApi: totp,
}))

import { LoginPage } from './login'

const challenge = {
  mfa_required: true as const,
  mfa_token: 'olvm_PENDING',
  enrolment_required: false,
  pending_expires_in_s: 300,
}

beforeEach(() => {
  navigateMock.mockReset()
  loginSearch.returnTo = undefined
  auth.status = 'anonymous'
  auth.can = () => true
  auth.login.mockReset()
  auth.adoptSession.mockReset()
  auth.adoptSession.mockResolvedValue(undefined)
  localStorage.clear()
  vi.clearAllMocks()
  // Once-queued enrol mocks from a previous test must not leak into the next
  // one's mount effect (a queued rejection hides the QR of a healthy run).
  totp.enrol.mockReset()
  totp.activate.mockReset()
  totp.challenge.mockReset()
  useClientSettings.setState({
    startPage: 'home',
    confirmStop: true,
    clock: '24',
  })
  useTenantStore.setState({ activeTenant: null })
})

function renderLogin() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <LoginPage />
    </QueryClientProvider>,
  )
}

async function submitPassword() {
  const user = userEvent.setup()
  renderLogin()
  await user.type(screen.getByLabelText('Email'), 'ada@example.com')
  await user.type(screen.getByLabelText('Password'), 'secret')
  await user.click(screen.getByRole('button', { name: 'Sign in' }))
}

describe('the second-factor login leg', () => {
  it('swaps the form for the code challenge and completes the login', async () => {
    auth.login.mockResolvedValue(challenge)
    totp.challenge.mockResolvedValue({
      csrf_token: 't2',
      session_id: 's2',
      expires_at: 'later',
    })
    const user = userEvent.setup()
    await submitPassword()

    // No session adopted yet: the password verified, the login did not.
    expect(auth.adoptSession).not.toHaveBeenCalled()
    const code = await screen.findByLabelText(/Authenticator code/i)
    expect(screen.queryByLabelText('Password')).not.toBeInTheDocument()

    await user.type(code, '123456')
    await user.click(screen.getByRole('button', { name: 'Verify and sign in' }))
    expect(totp.challenge).toHaveBeenCalledWith({
      mfa_token: 'olvm_PENDING',
      code: '123456',
    })
    await waitFor(() => expect(auth.adoptSession).toHaveBeenCalled())
    await waitFor(() => expect(navigateMock).toHaveBeenCalled())
  })

  it('accepts a recovery code through the alternate entry', async () => {
    auth.login.mockResolvedValue(challenge)
    totp.challenge.mockResolvedValue({
      csrf_token: 't3',
      session_id: 's3',
      expires_at: 'later',
    })
    const user = userEvent.setup()
    await submitPassword()

    await user.click(await screen.findByText(/Use a recovery code/i))
    await user.type(screen.getByLabelText(/Recovery code/i), 'AAAAAA-BBBBBB')
    await user.click(screen.getByRole('button', { name: 'Verify and sign in' }))
    expect(totp.challenge).toHaveBeenCalledWith({
      mfa_token: 'olvm_PENDING',
      recovery_code: 'AAAAAA-BBBBBB',
    })
    await waitFor(() => expect(navigateMock).toHaveBeenCalled())
  })

  it('keeps the challenge open on a wrong code with the honest error', async () => {
    auth.login.mockResolvedValue(challenge)
    totp.challenge.mockRejectedValue(
      Object.assign(new Error('auth: TOTP verification failed'), {
        status: 401,
      }),
    )
    const user = userEvent.setup()
    await submitPassword()
    await user.type(
      await screen.findByLabelText(/Authenticator code/i),
      '000000',
    )
    await user.click(screen.getByRole('button', { name: 'Verify and sign in' }))
    expect(await screen.findByRole('alert')).toBeInTheDocument()
    expect(auth.adoptSession).not.toHaveBeenCalled()
  })

  // The transport refuses a second enrolment with the consumed credential.
  it('shows the recovery codes after a forced enrolment and never re-enrols', async () => {
    auth.login.mockResolvedValue({ ...challenge, enrolment_required: true })
    totp.enrol
      .mockResolvedValueOnce({
        secret: 'JBSWY3DPEHPK3PXP',
        uri: 'otpauth://totp/Olivares%20AI:ada@example.com?secret=JBSWY3DPEHPK3PXP',
        algorithm: 'SHA1',
        digits: 6,
        period: 30,
        qr_png_base64:
          'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==',
      })
      .mockRejectedValueOnce(
        Object.assign(new Error('auth: TOTP verification failed'), {
          status: 401,
        }),
      )
    totp.activate.mockResolvedValue({
      csrf_token: 't9',
      session_id: 's9',
      expires_at: 'later',
      recovery_codes: ['ZZZZZZ-YYYYYY-XXXXXX-WWWWWW'],
    })
    const user = userEvent.setup()
    await submitPassword()

    await user.type(
      await screen.findByLabelText(/Authenticator code/i),
      '123456',
    )
    await user.click(screen.getByRole('button', { name: 'Activate' }))
    expect(
      await screen.findByText('ZZZZZZ-YYYYYY-XXXXXX-WWWWWW'),
    ).toBeInTheDocument()
    expect(totp.enrol).toHaveBeenCalledTimes(1)
    expect(auth.adoptSession).not.toHaveBeenCalled()

    await user.click(screen.getByRole('button', { name: /I saved the codes/i }))
    await waitFor(() => expect(auth.adoptSession).toHaveBeenCalled())
    expect(
      screen.queryByText('ZZZZZZ-YYYYYY-XXXXXX-WWWWWW'),
    ).not.toBeInTheDocument()
    expect(screen.queryByAltText(/QR code/i)).not.toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: 'Activate' }),
    ).not.toBeInTheDocument()
  })

  it('hands off the activated session once when the parent renders again', async () => {
    totp.enrol.mockResolvedValueOnce({
      secret: 'JBSWY3DPEHPK3PXP',
      uri: 'otpauth://totp/test',
      algorithm: 'SHA1',
      digits: 6,
      period: 30,
      qr_png_base64: '',
    })
    totp.activate.mockResolvedValueOnce({
      csrf_token: 't-once',
      session_id: 's-once',
      expires_at: 'later',
      recovery_codes: ['AAAAAA-BBBBBB-CCCCCC-DDDDDD'],
    })
    const done = vi.fn()
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    const view = () => (
      <QueryClientProvider client={qc}>
        <SecondFactorPanel
          challenge={{ ...challenge, enrolment_required: true }}
          onDone={(session) => done(session)}
          onRestart={() => undefined}
        />
      </QueryClientProvider>
    )
    const { rerender } = render(view())
    const user = userEvent.setup()
    await user.type(
      await screen.findByLabelText(/Authenticator code/i),
      '123456',
    )
    await user.click(screen.getByRole('button', { name: 'Activate' }))
    await user.click(
      await screen.findByRole('button', { name: /I saved the codes/i }),
    )
    await waitFor(() => expect(done).toHaveBeenCalledTimes(1))
    rerender(view())
    expect(done).toHaveBeenCalledTimes(1)
    expect(totp.enrol).toHaveBeenCalledTimes(1)
    expect(totp.activate).toHaveBeenCalledTimes(1)
  })

  it('runs the forced enrolment, reveals the codes once, then signs in', async () => {
    auth.login.mockResolvedValue({ ...challenge, enrolment_required: true })
    totp.enrol.mockResolvedValue({
      secret: 'JBSWY3DPEHPK3PXP',
      uri: 'otpauth://totp/Olivares%20AI:ada@example.com?secret=JBSWY3DPEHPK3PXP',
      algorithm: 'SHA1',
      digits: 6,
      period: 30,
      qr_png_base64:
        'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==',
    })
    totp.activate.mockResolvedValue({
      csrf_token: 't4',
      session_id: 's4',
      expires_at: 'later',
      recovery_codes: ['AAAAAA-BBBBBB-CCCCCC-DDDDDD'],
    })
    const user = userEvent.setup()
    await submitPassword()

    // The enrolment starts from the pending credential on its own.
    expect(await screen.findByAltText(/QR code/i)).toBeInTheDocument()
    expect(totp.enrol).toHaveBeenCalledWith({ mfa_token: 'olvm_PENDING' })

    await user.type(screen.getByLabelText(/Authenticator code/i), '123456')
    await user.click(screen.getByRole('button', { name: 'Activate' }))
    expect(
      await screen.findByText('AAAAAA-BBBBBB-CCCCCC-DDDDDD'),
    ).toBeInTheDocument()
    // No session is adopted until the codes are acknowledged.
    expect(auth.adoptSession).not.toHaveBeenCalled()

    await user.click(screen.getByRole('button', { name: /I saved the codes/i }))
    await waitFor(() => expect(auth.adoptSession).toHaveBeenCalled())
    await waitFor(() => expect(navigateMock).toHaveBeenCalled())
  })
})

it('keeps the requested page through the second-factor challenge', async () => {
  loginSearch.returnTo = '/audit?from=2026-09-30#entry-7'
  auth.login.mockResolvedValue(challenge)
  totp.challenge.mockResolvedValue({
    csrf_token: 'csrf_fixture',
    session_id: 'sid',
    expires_at: 'later',
  })
  const user = userEvent.setup()
  await submitPassword()
  expect(navigateMock).not.toHaveBeenCalled()
  await user.type(await screen.findByLabelText(/Authenticator code/i), '123456')
  await user.click(screen.getByRole('button', { name: 'Verify and sign in' }))
  await waitFor(() =>
    expect(navigateMock).toHaveBeenCalledWith({
      to: '/audit',
      href: '/audit?from=2026-09-30#entry-7',
      replace: true,
    }),
  )
})
