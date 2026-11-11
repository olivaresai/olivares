// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// E1 — the public invite-acceptance page. Covers: token read from the URL,
// client validation mirroring the server MinPasswordLen, the accept POST with the
// URL token, the coarse (non-oracle) invalid-token error, and that the post-success
// redirect is ALWAYS the internal / (never a URL-supplied destination).
import {
  focusManager,
  onlineManager,
  QueryClient,
  QueryClientProvider,
} from '@tanstack/react-query'
import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, NetworkError } from '@/lib/api/errors'
import { createQueryClient } from '@/lib/api/query'

const mockNavigate = vi.fn()
vi.mock('@tanstack/react-router', () => ({
  //useUrlState follows the location, so the mock has to answer it.
  useRouterState: () => '',
  useNavigate: () => mockNavigate,
  Link: ({ to, children, ...rest }: { to: string; children: ReactNode }) => (
    <a href={to} {...rest}>
      {children}
    </a>
  ),
}))

const mockAcceptInvite = vi.fn()
const mockAdoptSession = vi.fn()
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ adoptSession: mockAdoptSession }),
}))
vi.mock('@/lib/api/endpoints', () => ({
  authApi: {
    acceptInvite: (...args: unknown[]) => mockAcceptInvite(...args),
  },
}))

import { AcceptInvitePage } from './accept-invite'

function Wrapper({ children }: { children: ReactNode }) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  return <QueryClientProvider client={qc}>{children}</QueryClientProvider>
}

function setUrl(suffix: string) {
  window.history.replaceState(null, '', `/accept-invite${suffix}`)
}

/** The engine at the HTTP boundary: its answer to POST /v1/invites/preview. Every other
 * request is a 404, as is the preview by default (an engine without the route). */
const previewFetch = vi.fn()
function previewAnswer(status: number, body: unknown) {
  previewFetch.mockImplementation(async (url: string) =>
    String(url).endsWith('/v1/invites/preview')
      ? new Response(JSON.stringify(body), {
          status,
          headers: { 'Content-Type': 'application/json' },
        })
      : new Response('{}', { status: 404 }),
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  previewAnswer(404, {})
  vi.stubGlobal('fetch', previewFetch)
})
afterEach(() => vi.unstubAllGlobals())

describe('AcceptInvitePage', () => {
  it('shows the incomplete-link state when no token is in the URL', () => {
    setUrl('')
    render(<AcceptInvitePage />, { wrapper: Wrapper })
    expect(screen.getByRole('alert')).toHaveTextContent(/incomplete/i)
    // No password form without a token.
    expect(screen.queryByLabelText(/new password/i)).not.toBeInTheDocument()
  })

  //the subtitle promises a password that does not exist without a token.
  //
  // Check both directions. An absence-only assertion would pass if the subtitle were deleted
  // from the whole screen; a presence-only assertion would pass if it stayed outside the
  // conditional, reproducing the defect this test covers.
  it('no promete una contrasena sin token, y si la promete con token', () => {
    // ⚠ NO vale /choose a password/i: `invite.weakPassword` es "Choose a password of at
    //   least 8 characters." y casaria tambien. La sonda tiene que casar el SUBTITULO.
    const promesa = /choose a password to activate/i

    setUrl('')
    const sinToken = render(<AcceptInvitePage />, { wrapper: Wrapper })
    expect(
      screen.queryByText(promesa),
      'sin token la pantalla ofrece elegir contrasena y no hay campo donde hacerlo',
    ).not.toBeInTheDocument()
    // El estado de error sigue explicandose solo: por eso no hizo falta una clave nueva.
    expect(screen.getByRole('alert')).toHaveTextContent(/incomplete/i)
    sinToken.unmount()

    setUrl('?token=t-ok')
    render(<AcceptInvitePage />, { wrapper: Wrapper })
    expect(
      screen.getByText(promesa),
      'con token desaparecio el subtitulo: el arreglo se paso de largo',
    ).toBeInTheDocument()
    expect(screen.getByLabelText(/new password/i)).toBeInTheDocument()
  })

  it('rejects a short password client-side without calling the API', async () => {
    setUrl('#token=olvi_sel_secret')
    const user = userEvent.setup()
    render(<AcceptInvitePage />, { wrapper: Wrapper })
    await user.type(screen.getByLabelText(/new password/i), 'short')
    await user.type(screen.getByLabelText(/confirm password/i), 'short')
    await user.click(screen.getByRole('button', { name: /activate account/i }))
    expect(await screen.findByRole('alert')).toHaveTextContent(/at least 8/i)
    expect(mockAcceptInvite).not.toHaveBeenCalled()
  })

  it('rejects mismatched passwords client-side without calling the API', async () => {
    setUrl('#token=olvi_sel_secret')
    const user = userEvent.setup()
    render(<AcceptInvitePage />, { wrapper: Wrapper })
    await user.type(screen.getByLabelText(/new password/i), 'long-enough-pass')
    await user.type(
      screen.getByLabelText(/confirm password/i),
      'different-pass-1',
    )
    await user.click(screen.getByRole('button', { name: /activate account/i }))
    expect(await screen.findByRole('alert')).toHaveTextContent(/do not match/i)
    expect(mockAcceptInvite).not.toHaveBeenCalled()
  })

  it('POSTs the invite proof, adopts the cookie session and opens the console', async () => {
    setUrl('#token=olvi_sel_secret')
    mockAcceptInvite.mockResolvedValue({
      csrf_token: 'csrf-session',
      session_id: 's1',
      expires_at: '2026-08-01T00:00:00Z',
    })
    const user = userEvent.setup()
    render(<AcceptInvitePage />, { wrapper: Wrapper })
    await user.type(screen.getByLabelText(/new password/i), 'long-enough-pass')
    await user.type(
      screen.getByLabelText(/confirm password/i),
      'long-enough-pass',
    )
    await user.click(screen.getByRole('button', { name: /activate account/i }))

    await waitFor(() =>
      expect(mockAcceptInvite).toHaveBeenCalledWith({
        token: 'olvi_sel_secret',
        password: 'long-enough-pass',
      }),
    )
    // The redirect target is hardcoded — a tampered URL can never change it.
    await waitFor(() =>
      expect(mockNavigate).toHaveBeenCalledWith({ to: '/', replace: true }),
    )
    expect(mockAdoptSession).toHaveBeenCalledWith({
      csrf_token: 'csrf-session',
      session_id: 's1',
      expires_at: '2026-08-01T00:00:00Z',
    })
    expect(window.location.hash).toBe('')
    expect(window.location.search).toBe('')
  })

  it('shows ONE coarse message for an invalid/expired/used token (no oracle)', async () => {
    // Legacy query links remain redeemable, but are scrubbed from browser
    // history immediately. Newly minted links always use a fragment.
    setUrl('?token=olvi_bad')
    mockAcceptInvite.mockRejectedValue(
      new ApiError(400, 'invite_invalid', 'invite invalid'),
    )
    const user = userEvent.setup()
    render(<AcceptInvitePage />, { wrapper: Wrapper })
    await user.type(screen.getByLabelText(/new password/i), 'long-enough-pass')
    await user.type(
      screen.getByLabelText(/confirm password/i),
      'long-enough-pass',
    )
    await user.click(screen.getByRole('button', { name: /activate account/i }))

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent(/not valid/i)
    // The page must never render or echo the token anywhere.
    expect(document.body.textContent).not.toContain('olvi_bad')
    expect(window.location.search).toBe('')
    expect(mockNavigate).not.toHaveBeenCalled()
  })

  it('surfaces the server weak-password rejection distinctly', async () => {
    setUrl('#token=olvi_sel_secret')
    mockAcceptInvite.mockRejectedValue(
      new ApiError(400, 'weak_password', 'password too short'),
    )
    const user = userEvent.setup()
    render(<AcceptInvitePage />, { wrapper: Wrapper })
    await user.type(screen.getByLabelText(/new password/i), 'long-enough-pass')
    await user.type(
      screen.getByLabelText(/confirm password/i),
      'long-enough-pass',
    )
    await user.click(screen.getByRole('button', { name: /activate account/i }))
    expect(await screen.findByRole('alert')).toHaveTextContent(/at least 8/i)
  })
})

// olivares: an invite whose request gets no answer is worded by how the page was opened.
describe('AcceptInvitePage: a request that gets no answer', () => {
  const realLocation = window.location
  afterEach(() => {
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: realLocation,
    })
  })

  async function submitFailing(error: Error, openedAs: string) {
    setUrl('#token=olvi_sel_secret')
    const { protocol, origin } = new URL(openedAs)
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: { ...realLocation, protocol, origin },
    })
    mockAcceptInvite.mockRejectedValue(error)
    const user = userEvent.setup()
    render(<AcceptInvitePage />, { wrapper: Wrapper })
    await user.type(screen.getByLabelText(/new password/i), 'long-enough-pass')
    await user.type(
      screen.getByLabelText(/confirm password/i),
      'long-enough-pass',
    )
    await user.click(screen.getByRole('button', { name: /activate account/i }))
    return screen.findByRole('alert')
  }

  it('over http says the control plane is unreachable', async () => {
    const alert = await submitFailing(
      new NetworkError('down'),
      'http://localhost:8080/',
    )
    expect(alert).toHaveTextContent('Control plane unreachable')
  })

  it('over https names the certificate and the next step', async () => {
    const alert = await submitFailing(
      new NetworkError('down'),
      'https://192.168.8.17:8443/',
    )
    expect(alert).not.toHaveTextContent('Control plane unreachable')
    expect(alert).toHaveTextContent('https://192.168.8.17:8443')
    expect(alert).toHaveTextContent('tls.crt.ca')
  })

  it('over https a failure that is not a transport failure keeps the old line', async () => {
    const alert = await submitFailing(
      new TypeError('boom'),
      'https://192.168.8.17:8443/',
    )
    expect(alert).toHaveTextContent('Control plane unreachable')
  })
})

// The page as drawn: the lockup, then ONE sheet holding the title, the line
// under it, the two password fields and the one filled button; a refused or an
// incomplete link is a notice inside that sheet.
describe('AcceptInvitePage as drawn', () => {
  const sheetOf = (node: Element) => node.closest('[data-slot="auth-sheet"]')

  it('holds the one h1, the form and its one filled button in one sheet', () => {
    setUrl('#token=olvi_sel_secret')
    render(<AcceptInvitePage />, { wrapper: Wrapper })
    const headings = screen.getAllByRole('heading', { level: 1 })
    expect(headings).toHaveLength(1)
    const sheets = document.querySelectorAll('[data-slot="auth-sheet"]')
    expect(sheets).toHaveLength(1)
    const sheet = sheetOf(headings[0])
    expect(sheet).not.toBeNull()
    expect(sheet).toContainElement(screen.getByLabelText(/new password/i))
    expect(sheet).toContainElement(screen.getByLabelText(/confirm password/i))
    const submit = screen.getByRole('button', { name: /activate account/i })
    const filled = sheet!.querySelectorAll('.bg-accent')
    expect(filled).toHaveLength(1)
    expect(filled[0]).toBe(submit)
    expect(submit).toHaveClass('w-full')
    const back = screen.getByRole('link', { name: /go to sign in/i })
    expect(back).not.toHaveClass('bg-accent')
  })

  it('says a refused invitation in a notice inside the sheet', async () => {
    setUrl('#token=olvi_sel_secret')
    mockAcceptInvite.mockRejectedValue(
      new ApiError(400, 'invite_invalid', 'invite invalid'),
    )
    const user = userEvent.setup()
    render(<AcceptInvitePage />, { wrapper: Wrapper })
    await user.type(screen.getByLabelText(/new password/i), 'long-enough-pass')
    await user.type(
      screen.getByLabelText(/confirm password/i),
      'long-enough-pass',
    )
    await user.click(screen.getByRole('button', { name: /activate account/i }))
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent(/not valid/i)
    expect(alert).toHaveAttribute('data-slot', 'notice')
    expect(alert).toHaveAttribute('data-tone', 'bad')
    expect(sheetOf(alert)).not.toBeNull()
  })

  it('says an incomplete link in a notice inside the sheet', () => {
    setUrl('')
    render(<AcceptInvitePage />, { wrapper: Wrapper })
    const alert = screen.getByRole('alert')
    expect(alert).toHaveTextContent(/incomplete/i)
    expect(alert).toHaveAttribute('data-slot', 'notice')
    expect(sheetOf(alert)).not.toBeNull()
    expect(document.querySelectorAll('.bg-accent')).toHaveLength(0)
  })
})

// Who invites, to which organization, with which role: read once when the page opens,
// never spending the invitation; the token goes to that route and the accept route only.
describe('AcceptInvitePage: who invites', () => {
  const INVITE = {
    email: 'ana@example.com',
    role: 'editor',
    organization: 'Kestrel Works',
    invited_by: 'Marta Quiroga',
  }
  afterEach(() => {
    focusManager.setFocused(undefined)
    onlineManager.setOnline(true)
  })
  const stored = (s: Storage) =>
    Array.from({ length: s.length }, (_, i) => {
      const key = s.key(i) ?? ''
      return `${key}=${s.getItem(key) ?? ''}`
    }).join('\n')

  function renderSettled() {
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    render(
      <QueryClientProvider client={qc}>
        <AcceptInvitePage />
      </QueryClientProvider>,
    )
    return waitFor(() => expect(qc.isFetching()).toBe(0))
  }

  it('says who invites and to which organization, then the account and its role', async () => {
    setUrl('#token=olvi_sel_secret')
    previewAnswer(200, INVITE)
    render(<AcceptInvitePage />, { wrapper: Wrapper })
    expect(
      await screen.findByText('Marta Quiroga invited you to Kestrel Works.'),
    ).toBeVisible()
    // The identity row, as drawn: the email the account is for, then the role
    // it joins as; a line of text before the password, not a field.
    const email = screen.getByText('ana@example.com')
    expect(email).toBeVisible()
    expect(screen.getByText('You will join as Editor')).toBeVisible()
    expect(screen.queryByRole('textbox', { name: 'Email' })).toBeNull()
    const password = screen.getByLabelText(/new password/i)
    expect(email.compareDocumentPosition(password)).toBe(
      Node.DOCUMENT_POSITION_FOLLOWING,
    )
    // The initials are decoration: a screen reader reads the email, not them.
    const initials = screen.getByText('AN')
    expect(initials.closest('[aria-hidden="true"]')).not.toBeNull()
    expect(screen.queryByText(/choose a password to activate/i)).toBeNull()
    // Asked once, of that route, with the token in the body and no session.
    expect(previewFetch).toHaveBeenCalledTimes(1)
    const [url, init] = previewFetch.mock.calls[0] as [string, RequestInit]
    expect(url).toMatch(/\/v1\/invites\/preview$/)
    expect(init.method).toBe('POST')
    expect(JSON.parse(String(init.body))).toEqual({ token: 'olvi_sel_secret' })
    expect(new Headers(init.headers).has('Authorization')).toBe(false)
    // The token is shown nowhere, stored nowhere, and left in no address.
    expect(document.body.textContent).not.toContain('olvi_sel_secret')
    expect(stored(localStorage)).not.toContain('olvi_sel_secret')
    expect(stored(sessionStorage)).not.toContain('olvi_sel_secret')
    expect(window.location.href).not.toContain('olvi_sel_secret')
  })

  // A password manager saves the new password under an account name, and the
  // email is a line of text: one field carries it for the browser. Out of sight
  // but not `hidden` (some managers skip a hidden field), out of the tab order,
  // and silent to a screen reader, which reads the email in the identity row.
  it('offers the account to a password manager, out of sight and out of the tab order', async () => {
    setUrl('#token=olvi_sel_secret')
    previewAnswer(200, INVITE)
    render(<AcceptInvitePage />, { wrapper: Wrapper })
    const username = await screen.findByDisplayValue('ana@example.com')
    expect(username).toHaveAttribute('type', 'email')
    expect(username).toHaveAttribute('autocomplete', 'username')
    expect(username).toHaveAttribute('readonly')
    expect(username).toHaveAttribute('tabindex', '-1')
    expect(username).toHaveAttribute('aria-hidden', 'true')
    expect(username).not.toHaveAttribute('hidden')
    // In the form, before the new password, where a manager looks for it.
    const password = screen.getByLabelText(/new password/i)
    expect(username.closest('form')).toBe(password.closest('form'))
    expect(username.compareDocumentPosition(password)).toBe(
      Node.DOCUMENT_POSITION_FOLLOWING,
    )
  })

  it('says it without a name when the engine knows none', async () => {
    setUrl('#token=olvi_sel_secret')
    previewAnswer(200, { ...INVITE, role: 'admin', invited_by: '' })
    render(<AcceptInvitePage />, { wrapper: Wrapper })
    expect(
      await screen.findByText('You are invited to Kestrel Works.'),
    ).toBeVisible()
    expect(screen.getByText('ana@example.com')).toBeVisible()
    expect(screen.getByText('You will join as Admin')).toBeVisible()
  })

  it('leaves the role unsaid when the console has no name for it', async () => {
    setUrl('#token=olvi_sel_secret')
    previewAnswer(200, { ...INVITE, role: 'auditor' })
    render(<AcceptInvitePage />, { wrapper: Wrapper })
    expect(
      await screen.findByText('Marta Quiroga invited you to Kestrel Works.'),
    ).toBeVisible()
    expect(screen.getByText('ana@example.com')).toBeVisible()
    expect(screen.queryByText(/join as/i)).toBeNull()
    expect(document.body.textContent).not.toMatch(/auditor/i)
  })

  it('refuses an invalid or spent invitation at once, with the refusal the accept path shows', async () => {
    setUrl('#token=olvi_bad')
    previewAnswer(400, {
      error: { code: 'invite_invalid', message: 'invite invalid' },
    })
    mockAcceptInvite.mockRejectedValue(
      new ApiError(400, 'invite_invalid', 'invite invalid'),
    )
    render(<AcceptInvitePage />, { wrapper: Wrapper })
    const refusal =
      'This invitation link is not valid. It may have expired or already been used — ask your administrator for a new invitation.'
    expect(await screen.findByRole('alert')).toHaveTextContent(refusal)
    expect(screen.queryByText(/invited/)).toBeNull()
    expect(screen.queryByText(/join as/)).toBeNull()
    expect(mockAcceptInvite).not.toHaveBeenCalled()
    // The same notice, never a second one: accepting anyway says it once.
    const user = userEvent.setup()
    await user.type(screen.getByLabelText(/new password/i), 'long-enough-pass')
    await user.type(
      screen.getByLabelText(/confirm password/i),
      'long-enough-pass',
    )
    await user.click(screen.getByRole('button', { name: /activate account/i }))
    await waitFor(() => expect(mockAcceptInvite).toHaveBeenCalledTimes(1))
    expect(screen.getAllByRole('alert')).toHaveLength(1)
    expect(screen.getByRole('alert')).toHaveTextContent(refusal)
    expect(document.body.textContent).not.toContain('olvi_bad')
  })

  it.each([
    [404, {}],
    [500, { error: { code: 'internal', message: 'boom' } }],
    [400, { error: { code: 'bad_request', message: 'malformed body' } }],
  ])(
    'leaves the page as it was when the engine answers %i',
    async (status, body) => {
      setUrl('#token=olvi_sel_secret')
      previewAnswer(status, body)
      await renderSettled()
      expect(previewFetch).toHaveBeenCalledTimes(1)
      expect(
        screen.getByText('Choose a password to activate your account.'),
      ).toBeVisible()
      expect(screen.queryByText(/join as/)).toBeNull()
      expect(screen.queryByRole('alert')).toBeNull()
      expect(screen.getByLabelText(/new password/i)).toBeInTheDocument()
      // Without the preview's email there is no account to offer a manager.
      expect(document.querySelector('[autocomplete="username"]')).toBeNull()
    },
  )

  it('keeps the page as it was after a network error, and accepting still works', async () => {
    setUrl('#token=olvi_sel_secret')
    previewFetch.mockRejectedValue(new TypeError('Failed to fetch'))
    mockAcceptInvite.mockResolvedValue({
      csrf_token: 'csrf-session',
      session_id: 's1',
      expires_at: '2026-08-01T00:00:00Z',
    })
    await renderSettled()
    expect(previewFetch).toHaveBeenCalledTimes(1)
    expect(
      screen.getByText('Choose a password to activate your account.'),
    ).toBeVisible()
    expect(screen.queryByText(/invited|join as/)).toBeNull()
    expect(screen.queryByRole('alert')).toBeNull()
    const user = userEvent.setup()
    await user.type(screen.getByLabelText(/new password/i), 'long-enough-pass')
    await user.type(
      screen.getByLabelText(/confirm password/i),
      'long-enough-pass',
    )
    await user.click(screen.getByRole('button', { name: /activate account/i }))
    await waitFor(() =>
      expect(mockAcceptInvite).toHaveBeenCalledWith({
        token: 'olvi_sel_secret',
        password: 'long-enough-pass',
      }),
    )
    await waitFor(() =>
      expect(mockNavigate).toHaveBeenCalledWith({ to: '/', replace: true }),
    )
  })

  it('asks nothing without a token', async () => {
    setUrl('')
    await renderSettled()
    expect(previewFetch).not.toHaveBeenCalled()
  })

  // The token is a credential until it is spent: whatever the preview answers, it is
  // asked once per page open, and a return to the tab or a reconnect never sends the
  // token again. The client is the console's own, which refetches on focus.
  it.each([
    [200, INVITE],
    [404, {}],
    [400, { error: { code: 'invite_invalid', message: 'invite invalid' } }],
  ])(
    'asks once and never again on focus or reconnect after a %i',
    async (status, body) => {
      setUrl('#token=olvi_sel_secret')
      previewAnswer(status, body)
      const qc = createQueryClient()
      render(
        <QueryClientProvider client={qc}>
          <AcceptInvitePage />
        </QueryClientProvider>,
      )
      await waitFor(() => expect(previewFetch).toHaveBeenCalledTimes(1))
      await waitFor(() => expect(qc.isFetching()).toBe(0))
      await act(async () => {
        focusManager.setFocused(false)
        focusManager.setFocused(true)
        onlineManager.setOnline(false)
        onlineManager.setOnline(true)
        await new Promise((resolve) => setTimeout(resolve, 0))
      })
      await waitFor(() => expect(qc.isFetching()).toBe(0))
      expect(previewFetch).toHaveBeenCalledTimes(1)
    },
  )
})
