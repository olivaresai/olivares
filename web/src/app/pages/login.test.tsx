// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, NetworkError } from '@/lib/api/errors'
import { queryKeys } from '@/lib/api/query'
import type { Whoami } from '@/lib/api/types'
import { useClientSettings } from '@/features/settings/preferences'
import { useSessionStore } from '@/stores/session'
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

// The HTTP boundary of the passkey sign-in. The password tests sign in through
// the mocked auth context and never reach it.
const post = vi.hoisted(() => vi.fn())
vi.mock('@/lib/api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api/client')>()
  return { ...actual, http: { ...actual.http, post } }
})

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
    expect(navigateMock).toHaveBeenCalledWith({ to: '/now' })
  })

  it('falls back to / when the operator may not open the stored destination', async () => {
    useClientSettings.setState({ startPage: 'sessions' })
    auth.can = () => false
    await signIn()
    expect(navigateMock).toHaveBeenCalledWith({ to: '/now' })
  })

  it('uses the same choice when the operator is already signed in', () => {
    auth.status = 'authenticated'
    useClientSettings.setState({ startPage: 'providers' })
    auth.can = (permission) => permission === 'sessions:provider:read'
    renderLogin()
    expect(screen.getByTestId('redirect')).toHaveTextContent('/providers')
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
  // RC10 (SC on 6e97de81): the exact deep link SC signed in from.
  it('returns to /settings?tab=appearance#appearance after the password', async () => {
    loginSearch.returnTo = '/settings?tab=appearance#appearance'
    await signIn()
    expect(navigateMock).toHaveBeenCalledWith({
      to: '/settings',
      href: '/settings?tab=appearance#appearance',
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
      expect(navigateMock).toHaveBeenCalledWith({ to: '/now' })
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

  it('shows a quiet "or" under the sign-in button, then one button per provider', () => {
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
    const or = screen.getByRole('separator', { name: 'or' })
    // Under the form: the filled button, then «or», then the other ways in.
    const submit = screen.getByRole('button', { name: 'Sign in' })
    expect(
      submit.compareDocumentPosition(or) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy()
    expect(
      or.compareDocumentPosition(sso) & Node.DOCUMENT_POSITION_FOLLOWING,
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

// The page as drawn: the lockup, then ONE sheet holding the title, the form,
// the one filled button and the quieter ways in; the status link sits under it.
describe('the sign-in page as drawn', () => {
  const sheetOf = (node: Element) => node.closest('[data-slot="auth-sheet"]')

  it('holds the one h1 and the form in one sheet', () => {
    renderLogin()
    const headings = screen.getAllByRole('heading', { level: 1 })
    expect(headings).toHaveLength(1)
    const sheets = document.querySelectorAll('[data-slot="auth-sheet"]')
    expect(sheets).toHaveLength(1)
    const sheet = sheetOf(headings[0])
    expect(sheet).not.toBeNull()
    expect(sheet).toContainElement(screen.getByLabelText('Email'))
    expect(sheet).toContainElement(screen.getByLabelText('Password'))
  })

  it('fills one button with the accent; the other ways in stay quiet', () => {
    serverInfo.data = {
      ...serverInfo.data,
      sso_providers: [
        { label: 'Okta', start_url: '/v1/auth/federation/start' },
      ],
    }
    renderLogin()
    const submit = screen.getByRole('button', { name: 'Sign in' })
    const sheet = sheetOf(submit)
    expect(sheet).not.toBeNull()
    const filled = sheet!.querySelectorAll('.bg-accent')
    expect(filled).toHaveLength(1)
    expect(filled[0]).toBe(submit)
    expect(submit).toHaveClass('w-full')
    const okta = screen.getByRole('link', { name: 'Sign in with Okta' })
    expect(sheet).toContainElement(okta)
    expect(okta).not.toHaveClass('bg-accent')
  })

  it('puts the status link under the sheet, not inside it', () => {
    renderLogin()
    const status = screen.getByRole('link', { name: 'System status' })
    expect(sheetOf(status)).toBeNull()
    expect(screen.getByRole('main')).toContainElement(status)
  })

  it('says a refused sign-in in a notice inside the sheet, announced', async () => {
    auth.login.mockRejectedValue(
      new ApiError(401, 'invalid_credentials', 'invalid credentials'),
    )
    const user = userEvent.setup()
    renderLogin()
    await user.type(screen.getByLabelText('Email'), 'ada@example.com')
    await user.type(screen.getByLabelText('Password'), 'wrong')
    await user.click(screen.getByRole('button', { name: 'Sign in' }))
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('Incorrect email or password.')
    expect(alert).toHaveAttribute('data-slot', 'notice')
    expect(alert).toHaveAttribute('data-tone', 'bad')
    expect(sheetOf(alert)).not.toBeNull()
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

// HU-R37: after the engine ended the session, the sign-in page says why, in one line.
describe('the sign-in page after a session ended', () => {
  it.each([
    ['ended', 'Your session ended. Sign in again.'],
    ['expired', 'Your session expired. Sign in again.'],
  ] as const)('says the session %s', (reason, line) => {
    useSessionStore.setState({ endReason: reason })
    renderLogin()
    expect(screen.getByText(line)).toHaveAttribute('role', 'status')
    useSessionStore.setState({ endReason: null })
  })

  it('says nothing on an ordinary visit', () => {
    useSessionStore.setState({ endReason: null })
    renderLogin()
    expect(screen.queryByText(/Your session/)).toBeNull()
  })
})

// olivares: a sign-in whose request never gets an answer. The browser hides why (a
// refused certificate looks like a stopped engine), so the page words it by how it was opened.
describe('a sign-in request that gets no answer', () => {
  const realLocation = window.location
  afterEach(() => {
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: realLocation,
    })
  })

  async function submitWhileOpenedAs(
    url: string,
    error: Error = new NetworkError('Failed to fetch'),
  ) {
    const { protocol, origin } = new URL(url)
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: { ...realLocation, protocol, origin },
    })
    auth.login.mockRejectedValue(error)
    const user = userEvent.setup()
    renderLogin()
    await user.type(screen.getByLabelText('Email'), 'ada@example.com')
    await user.type(screen.getByLabelText('Password'), 'secret')
    await user.click(screen.getByRole('button', { name: 'Sign in' }))
    return screen.findByRole('alert')
  }

  it('over http says the control plane is unreachable', async () => {
    const alert = await submitWhileOpenedAs('http://localhost:8080/login')
    expect(alert).toHaveTextContent('Control plane unreachable')
  })

  it('over https names the certificate and the next step', async () => {
    const alert = await submitWhileOpenedAs('https://192.168.8.17:8443/login')
    expect(alert).not.toHaveTextContent('Control plane unreachable')
    expect(alert).toHaveTextContent(
      'secure connection to https://192.168.8.17:8443',
    )
    expect(alert).toHaveTextContent('trust it')
    expect(alert).toHaveTextContent('tls.crt.ca')
    expect(alert).toHaveTextContent('engine is running')
  })

  it('over https a failure that is not a transport failure keeps the old line', async () => {
    const alert = await submitWhileOpenedAs(
      'https://192.168.8.17:8443/login',
      new TypeError('boom'),
    )
    expect(alert).toHaveTextContent('Control plane unreachable')
  })
})

// A passkey is offered only where the engine says this address allows it and the
// browser can run the ceremony; a success ends exactly as a password does.
describe('sign-in with a passkey', () => {
  const OPTIONS_PATH = '/v1/auth/webauthn/sign-in/options'
  const SIGN_IN_PATH = '/v1/auth/webauthn/sign-in'
  const ENGINE_WORDS = 'engine words'
  const OPTIONS = {
    publicKey: {
      challenge: 'AQID',
      rpId: 'console.example.com',
      userVerification: 'required',
      timeout: 60000,
    },
  }
  const SESSION = { csrf_token: 'c', session_id: 's', expires_at: 'soon' }
  const bytes = (...values: number[]) => new Uint8Array(values).buffer
  const assertion = {
    id: 'cred-1',
    rawId: bytes(1, 2),
    type: 'public-key',
    response: {
      clientDataJSON: bytes(3),
      authenticatorData: bytes(4),
      signature: bytes(5),
      userHandle: bytes(6),
    },
  }
  const getCredential = vi.fn()
  const passkeyButton = () =>
    screen.queryByRole('button', { name: 'Use a passkey' })
  const or = () => screen.queryAllByRole('separator', { name: 'or' })

  /** The engine: fresh options, then `signIn` answers the assertion. */
  function engine(signIn: () => Promise<unknown>) {
    post.mockImplementation(async (path: string) => {
      if (path === OPTIONS_PATH) return OPTIONS
      if (path === SIGN_IN_PATH) return signIn()
      throw new Error(`unexpected ${path}`)
    })
  }

  /** A promise the test settles by hand. */
  function deferred<T>() {
    let resolve!: (value: T) => void
    let reject!: (reason: unknown) => void
    const promise = new Promise<T>((res, rej) => {
      resolve = res
      reject = rej
    })
    return { promise, resolve, reject }
  }

  beforeEach(() => {
    serverInfo.data = {
      setup_required: false,
      version: 'v',
      passkey_sign_in: true,
    }
    vi.stubGlobal('PublicKeyCredential', function PublicKeyCredential() {})
    Object.defineProperty(navigator, 'credentials', {
      configurable: true,
      value: { get: getCredential },
    })
    getCredential.mockReset()
    getCredential.mockResolvedValue(assertion)
    post.mockReset()
    engine(async () => SESSION)
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    Object.defineProperty(navigator, 'credentials', {
      configurable: true,
      value: undefined,
    })
  })

  it.each([undefined, false])(
    'draws nothing when server-info says passkey_sign_in %s',
    (allowed) => {
      serverInfo.data = { setup_required: false, version: 'v' }
      if (allowed !== undefined) serverInfo.data.passkey_sign_in = allowed
      renderLogin()
      expect(passkeyButton()).toBeNull()
      expect(or()).toHaveLength(0)
    },
  )

  it('draws nothing when the browser cannot run a passkey', () => {
    vi.stubGlobal('PublicKeyCredential', undefined)
    renderLogin()
    expect(passkeyButton()).toBeNull()
    expect(or()).toHaveLength(0)
  })

  it('draws one quiet button under the "or", above the organization sign-in', () => {
    serverInfo.data = {
      ...serverInfo.data,
      sso_providers: [
        { label: 'Okta', start_url: '/v1/auth/federation/start' },
      ],
    }
    renderLogin()
    const passkey = passkeyButton()!
    const okta = screen.getByRole('link', { name: 'Sign in with Okta' })
    expect(or()).toHaveLength(1)
    const [line] = or()
    expect(
      line.compareDocumentPosition(passkey) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy()
    expect(
      passkey.compareDocumentPosition(okta) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy()
    expect(passkey).toHaveAttribute('type', 'button')
    expect(passkey).not.toHaveClass('bg-accent')
    expect(passkey.closest('form')).toBeNull()
  })

  it('signs in exactly as the password does: the session, then the same page', async () => {
    loginSearch.returnTo = '/audit?from=2026-09-30#entry-7'
    const user = userEvent.setup()
    renderLogin()
    await user.click(passkeyButton()!)
    await waitFor(() => expect(navigateMock).toHaveBeenCalled())
    expect(post).toHaveBeenNthCalledWith(
      1,
      OPTIONS_PATH,
      undefined,
      expect.objectContaining({ anonymous: true }),
    )
    const request = getCredential.mock.calls[0][0]
    expect(new Uint8Array(request.publicKey.challenge)).toEqual(
      new Uint8Array([1, 2, 3]),
    )
    expect(request.publicKey.userVerification).toBe('required')
    expect(post).toHaveBeenNthCalledWith(
      2,
      SIGN_IN_PATH,
      {
        credential: {
          id: 'cred-1',
          rawId: 'AQI',
          type: 'public-key',
          response: {
            clientDataJSON: 'Aw',
            authenticatorData: 'BA',
            signature: 'BQ',
            userHandle: 'Bg',
          },
        },
      },
      expect.objectContaining({
        anonymous: true,
        headers: { 'X-Olivares-Session': 'cookie' },
      }),
    )
    expect(post).toHaveBeenCalledTimes(2)
    expect(auth.login).not.toHaveBeenCalled()
    expect(auth.adoptSession).toHaveBeenCalledWith(SESSION)
    expect(navigateMock).toHaveBeenCalledWith({
      to: '/audit',
      href: '/audit?from=2026-09-30#entry-7',
      replace: true,
    })
  })

  it('returns to rest and says nothing when the person closes the sheet', async () => {
    const sheet = deferred<never>()
    getCredential.mockReturnValue(sheet.promise)
    const user = userEvent.setup()
    renderLogin()
    await user.click(passkeyButton()!)
    const busy = await screen.findByRole('button', { name: 'Signing in…' })
    expect(busy).toHaveAttribute('aria-busy', 'true')
    await act(async () => {
      sheet.reject(new DOMException('dismissed', 'NotAllowedError'))
    })
    const rest = await screen.findByRole('button', { name: 'Use a passkey' })
    expect(rest).not.toHaveAttribute('aria-busy')
    expect(screen.queryByRole('alert')).toBeNull()
    expect(post).toHaveBeenCalledTimes(1)
    expect(auth.adoptSession).not.toHaveBeenCalled()
    expect(navigateMock).not.toHaveBeenCalled()
  })

  it('holds both ways in while a passkey signs in: one sign-in at a time', async () => {
    const answer = deferred<typeof SESSION>()
    engine(() => answer.promise)
    const user = userEvent.setup()
    renderLogin()
    await user.click(passkeyButton()!)
    const busy = await screen.findByRole('button', { name: 'Signing in…' })
    expect(busy).toHaveAttribute('aria-disabled', 'true')
    expect(screen.getByRole('button', { name: 'Sign in' })).toBeDisabled()
    await user.click(busy)
    expect(getCredential).toHaveBeenCalledTimes(1)
    await act(async () => {
      answer.resolve(SESSION)
    })
    await waitFor(() => expect(navigateMock).toHaveBeenCalledTimes(1))
    expect(post).toHaveBeenCalledTimes(2)
    expect(auth.login).not.toHaveBeenCalled()
  })

  it('holds the passkey while the password signs in', async () => {
    auth.login.mockReturnValue(new Promise<never>(() => {}))
    const user = userEvent.setup()
    renderLogin()
    await user.type(screen.getByLabelText('Email'), 'ada@example.com')
    await user.type(screen.getByLabelText('Password'), 'secret')
    await user.click(screen.getByRole('button', { name: 'Sign in' }))
    await waitFor(() =>
      expect(passkeyButton()).toHaveAttribute('aria-disabled', 'true'),
    )
    await user.click(passkeyButton()!)
    expect(post).not.toHaveBeenCalled()
  })

  it.each([
    [
      'a refused passkey',
      new ApiError(401, 'invalid_credentials', ENGINE_WORDS),
      'That passkey did not sign you in. Try again, or use your password.',
    ],
    [
      'too many attempts',
      new ApiError(429, 'locked_out', ENGINE_WORDS),
      'Too many attempts. Try again in a moment.',
    ],
    [
      'no answer',
      new NetworkError('Failed to fetch'),
      'Control plane unreachable',
    ],
  ])('says the page’s own sentence for %s', async (_, failure, sentence) => {
    engine(async () => {
      throw failure
    })
    const user = userEvent.setup()
    renderLogin()
    await user.click(passkeyButton()!)
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent(sentence)
    expect(alert).not.toHaveTextContent(ENGINE_WORDS)
    // The assertion names no account: the page names no email or password.
    expect(alert).not.toHaveTextContent('Incorrect email or password.')
    expect(auth.adoptSession).not.toHaveBeenCalled()
    expect(passkeyButton()).not.toBeNull()
  })

  // The engine answered and the browser refused: a relying party it will not
  // use at this address, a failure it does not name, options it cannot read.
  // The passkey did not sign in; the control plane was reachable.
  it.each([
    ['SecurityError', new DOMException('refused', 'SecurityError')],
    ['UnknownError', new DOMException('unknown', 'UnknownError')],
    ['TypeError', new TypeError('options it cannot read')],
  ])(
    'says the passkey’s own sentence when the browser refuses with %s',
    async (_, failure) => {
      getCredential.mockRejectedValue(failure)
      const user = userEvent.setup()
      renderLogin()
      await user.click(passkeyButton()!)
      const alert = await screen.findByRole('alert')
      expect(alert).toHaveTextContent(
        'That passkey did not sign you in. Try again, or use your password.',
      )
      expect(alert).not.toHaveTextContent('Control plane unreachable')
      expect(post).toHaveBeenCalledTimes(1)
      expect(auth.adoptSession).not.toHaveBeenCalled()
      expect(passkeyButton()).not.toBeNull()
    },
  )

  it('says nothing either when the sheet is aborted', async () => {
    const sheet = deferred<never>()
    getCredential.mockReturnValue(sheet.promise)
    const user = userEvent.setup()
    renderLogin()
    await user.click(passkeyButton()!)
    await screen.findByRole('button', { name: 'Signing in…' })
    await act(async () => {
      sheet.reject(new DOMException('aborted', 'AbortError'))
    })
    const rest = await screen.findByRole('button', { name: 'Use a passkey' })
    expect(rest).not.toHaveAttribute('aria-busy')
    expect(screen.queryByRole('alert')).toBeNull()
    expect(post).toHaveBeenCalledTimes(1)
    expect(auth.adoptSession).not.toHaveBeenCalled()
  })

  it('sends an account its organization signs in to that sign-in', async () => {
    serverInfo.data = {
      ...serverInfo.data,
      sso_providers: [
        { label: 'Okta', start_url: '/v1/auth/federation/start' },
      ],
    }
    engine(async () => {
      throw new ApiError(403, 'sso_required', ENGINE_WORDS)
    })
    const user = userEvent.setup()
    renderLogin()
    await user.click(passkeyButton()!)
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent(
      "This account signs in through your organization. Use your organization's sign-in.",
    )
    expect(alert).not.toHaveTextContent(ENGINE_WORDS)
    expect(
      screen.getByRole('link', { name: 'Sign in with Okta' }),
    ).toBeInTheDocument()
  })

  it('says a passkey cannot be used at this address and withdraws the button', async () => {
    post.mockRejectedValue(
      new ApiError(503, 'webauthn_relying_party_unusable', ENGINE_WORDS),
    )
    const user = userEvent.setup()
    renderLogin()
    await user.click(passkeyButton()!)
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent(
      'Passkeys are not available at this address.',
    )
    expect(alert).not.toHaveTextContent(ENGINE_WORDS)
    expect(passkeyButton()).toBeNull()
    expect(getCredential).not.toHaveBeenCalled()
    expect(screen.getByRole('button', { name: 'Sign in' })).toBeEnabled()
    // The focus left with the button: it goes to the form's first field.
    expect(screen.getByLabelText('Email')).toHaveFocus()
  })
})
