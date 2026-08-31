// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// E1 — the public invite-acceptance page. Covers: token read from the URL,
// client validation mirroring the server MinPasswordLen, the accept POST with the
// URL token, the coarse (non-oracle) invalid-token error, and that the post-success
// redirect is ALWAYS the internal /login (never a URL-supplied destination).
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/errors'

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

beforeEach(() => {
  vi.clearAllMocks()
})

describe('AcceptInvitePage', () => {
  it('shows the incomplete-link state when no token is in the URL', () => {
    setUrl('')
    render(<AcceptInvitePage />, { wrapper: Wrapper })
    expect(screen.getByRole('alert')).toHaveTextContent(/incomplete/i)
    // No password form without a token.
    expect(screen.queryByLabelText(/new password/i)).not.toBeInTheDocument()
  })

  //el subtitulo promete una contrasena que en el estado sin token NO existe.
  //
  // ⛔ LAS DOS DIRECCIONES, o no prueba nada. Afirmar solo la AUSENCIA la pasaria tambien
  //    borrar el subtitulo de la pantalla entera; afirmar solo la PRESENCIA la pasaria
  //    dejandolo fuera del condicional, que es justo el defecto que este testigo cierra.
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

  it('POSTs the URL token + password and redirects to the internal /login', async () => {
    setUrl('#token=olvi_sel_secret')
    mockAcceptInvite.mockResolvedValue({
      token: 'olvs_session',
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
      expect(mockNavigate).toHaveBeenCalledWith({ to: '/login' }),
    )
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
