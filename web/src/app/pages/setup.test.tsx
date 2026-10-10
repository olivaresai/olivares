// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// First-boot setup creates the first ORGANIZATION and the superadmin that owns it
// in one operation, and answers with both (core/api/handlers_auth.go handleSetup,
// core/api/dto.go setupResponse). The console must SELECT the returned tenant:
// the tenant store is persisted, so by the time the operator finishes signing in
// the client already sends X-Olivares-Tenant and the console opens on a working
// panel. Without this the very first screen after setup is the "no organization"
// gate, for an organization that already exists.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const mockNavigate = vi.fn()
vi.mock('@tanstack/react-router', () => ({
  useRouterState: () => '',
  useNavigate: () => mockNavigate,
  Navigate: ({ to }: { to: string }) => <div data-testid="navigate">{to}</div>,
  Link: ({ to, children }: { to: string; children: ReactNode }) => (
    <a href={to}>{children}</a>
  ),
}))

// Setup signs the new administrator straight in. By default the sign-in is
// refused here, so the original "go to /login" path stays pinned; the auto
// sign-in test makes it succeed.
const mockLogin = vi.fn()
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ login: (...args: unknown[]) => mockLogin(...args) }),
}))

const mockSetup = vi.fn()
// What the engine's server info says; a completed setup flips it.
const engine = { setupRequired: true }
vi.mock('@/lib/api/endpoints', () => ({
  authApi: {
    setup: (...args: unknown[]) => mockSetup(...args),
    serverInfo: () =>
      Promise.resolve({
        version: 'test',
        engine: 'test',
        setup_required: engine.setupRequired,
        license: { status: 'community', licensee: '' },
      }),
  },
}))

import { SetupPage } from './setup'
import { ApiError } from '@/lib/api/errors'
import { LANGUAGE_CODES } from '@/lib/i18n'
import { useTenantStore } from '@/stores/tenant'

const authLocales = import.meta.glob('@/lib/i18n/locales/*/auth.json', {
  eager: true,
  import: 'default',
}) as Record<string, { setup: Record<string, string> }>

const errorLocales = import.meta.glob('@/lib/i18n/locales/*/errors.json', {
  eager: true,
  import: 'default',
}) as Record<string, { codes: Record<string, string> }>

const TENANT_ID = '018f5a20-0000-7000-8000-00000000beef'

function Wrapper({ children }: { children: ReactNode }) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  return <QueryClientProvider client={qc}>{children}</QueryClientProvider>
}

beforeEach(() => {
  vi.clearAllMocks()
  engine.setupRequired = true
  mockLogin.mockRejectedValue(new Error('sign-in refused'))
  localStorage.clear()
  useTenantStore.setState({ activeTenant: null })
})

describe('SetupPage', () => {
  it('selects the organization setup returned, so the console lands usable', async () => {
    const user = userEvent.setup()
    mockSetup.mockResolvedValue({
      id: 'u-1',
      email: 'admin@example.com',
      status: 'active',
      is_superadmin: true,
      created_at: '2026-08-05T10:00:00Z',
      organization: {
        id: TENANT_ID,
        tenant_id: TENANT_ID,
        name: 'Default Organization',
        slug: 'default',
        status: 'active',
        created_at: '2026-08-05T10:00:00Z',
      },
    })

    render(<SetupPage />, { wrapper: Wrapper })

    await user.type(await screen.findByLabelText(/setup token/i), 'olv_tok')
    await user.type(
      screen.getByLabelText(/administrator email/i),
      'admin@example.com',
    )
    await user.type(screen.getByLabelText(/password/i), 'correct-horse-battery')
    await user.click(
      screen.getByRole('button', { name: /create administrator/i }),
    )

    await waitFor(() => expect(mockSetup).toHaveBeenCalledTimes(1))
    // The tenant the ENGINE reported — never one the console assembled.
    await waitFor(() =>
      expect(useTenantStore.getState().activeTenant).toBe(TENANT_ID),
    )
    expect(mockNavigate).toHaveBeenCalledWith({ to: '/login' })
  })

  it('signs the new administrator in with what they typed and opens the setup wizard', async () => {
    const user = userEvent.setup()
    mockSetup.mockResolvedValue({
      id: 'u-1',
      email: 'admin@example.com',
      status: 'active',
      is_superadmin: true,
      created_at: '2026-08-05T10:00:00Z',
    })
    mockLogin.mockResolvedValue({
      token: 'session',
      session_id: 's',
      expires_at: '',
    })

    render(<SetupPage />, { wrapper: Wrapper })
    await user.type(await screen.findByLabelText(/setup token/i), 'olv_tok')
    await user.type(
      screen.getByLabelText(/administrator email/i),
      'admin@example.com',
    )
    await user.type(screen.getByLabelText(/password/i), 'correct-horse-battery')
    await user.click(
      screen.getByRole('button', { name: /create administrator/i }),
    )

    await waitFor(() =>
      expect(mockNavigate).toHaveBeenCalledWith({ to: '/onboarding' }),
    )
    expect(mockLogin).toHaveBeenCalledWith({
      email: 'admin@example.com',
      password: 'correct-horse-battery',
    })
    expect(mockNavigate).not.toHaveBeenCalledWith({ to: '/login' })
  })

  // /onboarding loads lazily, so this page is still mounted when its refreshed server
  // info says setup is done; the submitter's own navigation decides, not the door.
  it('keeps the new administrator on the way to the setup wizard while it loads', async () => {
    const user = userEvent.setup()
    mockSetup.mockImplementation(() => {
      engine.setupRequired = false
      return Promise.resolve({
        id: 'u-1',
        email: 'admin@example.com',
        status: 'active',
        is_superadmin: true,
        created_at: '2026-08-05T10:00:00Z',
      })
    })
    mockLogin.mockResolvedValue({
      token: 'session',
      session_id: 's',
      expires_at: '',
    })

    render(<SetupPage />, { wrapper: Wrapper })
    await user.type(await screen.findByLabelText(/setup token/i), 'olv_tok')
    await user.type(
      screen.getByLabelText(/administrator email/i),
      'admin@example.com',
    )
    await user.type(screen.getByLabelText(/password/i), 'correct-horse-battery')
    await user.click(
      screen.getByRole('button', { name: /create administrator/i }),
    )

    await waitFor(() =>
      expect(mockNavigate).toHaveBeenCalledWith({ to: '/onboarding' }),
    )
    // Let the server-info refresh that follows the navigation settle.
    await new Promise((resolve) => setTimeout(resolve, 50))
    expect(screen.queryByTestId('navigate')).toBeNull()
    expect(mockNavigate).not.toHaveBeenCalledWith({ to: '/login' })
  })

  it('sends a visitor who arrives after setup to the sign-in page', async () => {
    engine.setupRequired = false
    render(<SetupPage />, { wrapper: Wrapper })
    expect((await screen.findByTestId('navigate')).textContent).toBe('/login')
  })

  it('opens the setup wizard when the session travels in the browser cookie', async () => {
    const user = userEvent.setup()
    mockSetup.mockResolvedValue({
      id: 'u-1',
      email: 'admin@example.com',
      status: 'active',
      is_superadmin: true,
      created_at: '2026-08-05T10:00:00Z',
    })
    mockLogin.mockResolvedValue({
      csrf_token: 'c',
      session_id: 's',
      expires_at: '',
    })

    render(<SetupPage />, { wrapper: Wrapper })
    await user.type(await screen.findByLabelText(/setup token/i), 'olv_tok')
    await user.type(
      screen.getByLabelText(/administrator email/i),
      'admin@example.com',
    )
    await user.type(screen.getByLabelText(/password/i), 'correct-horse-battery')
    await user.click(
      screen.getByRole('button', { name: /create administrator/i }),
    )

    await waitFor(() =>
      expect(mockNavigate).toHaveBeenCalledWith({ to: '/onboarding' }),
    )
    expect(mockNavigate).not.toHaveBeenCalledWith({ to: '/login' })
  })

  it('falls back to the sign-in page when the new session needs a second factor', async () => {
    const user = userEvent.setup()
    mockSetup.mockResolvedValue({
      id: 'u-1',
      email: 'admin@example.com',
      status: 'active',
      is_superadmin: true,
      created_at: '2026-08-05T10:00:00Z',
    })
    mockLogin.mockResolvedValue({ mfa_required: true, mfa_token: 'm' })

    render(<SetupPage />, { wrapper: Wrapper })
    await user.type(await screen.findByLabelText(/setup token/i), 'olv_tok')
    await user.type(
      screen.getByLabelText(/administrator email/i),
      'admin@example.com',
    )
    await user.type(screen.getByLabelText(/password/i), 'correct-horse-battery')
    await user.click(
      screen.getByRole('button', { name: /create administrator/i }),
    )

    await waitFor(() =>
      expect(mockNavigate).toHaveBeenCalledWith({ to: '/login' }),
    )
  })

  it('leaves the selection empty when setup reports no organization', async () => {
    const user = userEvent.setup()
    // An engine that answers without an organization is not a reason to invent a
    // tenant id: the console proceeds with none and the gate takes over.
    mockSetup.mockResolvedValue({
      id: 'u-1',
      email: 'admin@example.com',
      status: 'active',
      is_superadmin: true,
      created_at: '2026-08-05T10:00:00Z',
    })

    render(<SetupPage />, { wrapper: Wrapper })

    await user.type(await screen.findByLabelText(/setup token/i), 'olv_tok')
    await user.type(
      screen.getByLabelText(/administrator email/i),
      'admin@example.com',
    )
    await user.type(screen.getByLabelText(/password/i), 'correct-horse-battery')
    await user.click(
      screen.getByRole('button', { name: /create administrator/i }),
    )

    await waitFor(() =>
      expect(mockNavigate).toHaveBeenCalledWith({ to: '/login' }),
    )
    expect(useTenantStore.getState().activeTenant).toBeNull()
  })

  //the wizard is the FIRST screen of a self-hosted install, and a first boot
  // that fails by CONFIGURATION must say what to configure. The engine now answers
  // 501 cross_tenant_admin_pool_not_configured instead of a mute 500 (core/api/
  // errors.go), but the console renders `errors:codes.<code>` and falls back to
  // "Something went wrong." for a code it has no entry for — so a backend fix alone
  // would still have reached the operator as the generic string.
  it('tells the operator what to provision when the engine has no cross-tenant admin pool', async () => {
    const user = userEvent.setup()
    mockSetup.mockRejectedValue(
      new ApiError(
        501,
        'cross_tenant_admin_pool_not_configured',
        'This deployment has no cross-tenant admin database pool.',
      ),
    )

    render(<SetupPage />, { wrapper: Wrapper })

    await user.type(await screen.findByLabelText(/setup token/i), 'olv_tok')
    await user.type(
      screen.getByLabelText(/administrator email/i),
      'admin@example.com',
    )
    await user.type(screen.getByLabelText(/password/i), 'correct-horse-battery')
    await user.click(
      screen.getByRole('button', { name: /create administrator/i }),
    )

    // The two things the operator has to DO — provision the role, then point the
    // server at it. Without both, the screen is sympathy rather than a remedy.
    //
    // The command is asserted, not just a filename: the first draft of this remedy
    // named deploy/postgres/01-app-role.sql, whose admin-role block is commented out
    // end to end, so following the screen got you nothing (F8).
    const shown = await screen.findByText(/--admin-dsn/)
    expect(shown.textContent).toMatch(/olivares db init/)
    // The engine's own first remedy (core/api/errors.go): install the tenant inventory on
    // the database it already uses; the admin role stays the alternative.
    expect(shown.textContent).toMatch(/--install-directory-inventory/)
    expect(shown.textContent).toMatch(/--admin-role/)
    expect(shown.textContent).toMatch(/NOSUPERUSER BYPASSRLS/)
    expect(screen.queryByText(/Something went wrong/i)).toBeNull()
  })

  // #469: the engine prints the token on its STARTUP OUTPUT (cmd/olivares/cmd_serve.go
  // announceSetup), not in its structured log. A terminal shows it, but a container
  // keeps it in `docker logs` and the packaged service in the journal, and the README
  // Compose path takes a replacement from `first-boot --new-token`. The screen used
  // to name only the terminal and to say the token was "not written to the log".
  it('says where the setup token is on every install path', async () => {
    render(<SetupPage />, { wrapper: Wrapper })
    const field = await screen.findByLabelText(/setup token/i)
    const hint = document.getElementById(
      field.getAttribute('aria-describedby')!.split(' ')[0],
    )!.textContent!
    expect(hint).toMatch(/`docker logs`/)
    expect(hint).toMatch(/`journalctl -u olivares`/)
    expect(hint).toMatch(/`olivares first-boot --new-token`/)
    expect(screen.queryByText(/not written to the log/i)).toBeNull()
    expect(screen.queryByText(/terminal where Olivares started/i)).toBeNull()
  })

  it('points a rejected token at the replacement first-boot prints', async () => {
    const user = userEvent.setup()
    mockSetup.mockRejectedValue(new ApiError(403, 'forbidden', 'forbidden'))

    render(<SetupPage />, { wrapper: Wrapper })
    await user.type(await screen.findByLabelText(/setup token/i), 'olst_old')
    await user.type(
      screen.getByLabelText(/administrator email/i),
      'admin@example.com',
    )
    await user.type(screen.getByLabelText(/password/i), 'correct-horse-battery')
    await user.click(
      screen.getByRole('button', { name: /create administrator/i }),
    )

    expect((await screen.findByRole('alert')).textContent).toMatch(
      /`olivares first-boot --new-token`/,
    )
  })

  // Only the engine's plain `forbidden` is the token refusal (core/api/handlers_auth.go
  // handleSetup). A 403 with its own code, such as residency_violation from provisioning
  // the first organization, is not a token problem and must not send the operator to
  // mint one.
  it('keeps the token advice for the token refusal only', async () => {
    const user = userEvent.setup()
    mockSetup.mockRejectedValue(
      new ApiError(403, 'residency_violation', 'residency violation'),
    )

    render(<SetupPage />, { wrapper: Wrapper })
    await user.type(await screen.findByLabelText(/setup token/i), 'olst_tok')
    await user.type(
      screen.getByLabelText(/administrator email/i),
      'admin@example.com',
    )
    await user.type(screen.getByLabelText(/password/i), 'correct-horse-battery')
    await user.click(
      screen.getByRole('button', { name: /create administrator/i }),
    )

    expect((await screen.findByRole('alert')).textContent).not.toMatch(
      /setup token/i,
    )
  })

  // Commands are not translated, so every locale must name the same three routes.
  it('names the same token routes in every locale', () => {
    expect(Object.keys(authLocales).sort()).toEqual(
      LANGUAGE_CODES.map((l) => `/src/lib/i18n/locales/${l}/auth.json`).sort(),
    )
    for (const [file, { setup }] of Object.entries(authLocales)) {
      for (const cmd of [
        '`docker logs`',
        '`journalctl -u olivares`',
        '`olivares first-boot --new-token`',
      ])
        expect(setup.tokenHint, `${file} setup.tokenHint`).toContain(cmd)
      expect(setup.invalidToken, `${file} setup.invalidToken`).toContain(
        '`olivares first-boot --new-token`',
      )
    }
  })

  // #530: a token file the engine cannot read is not a wrong token. The engine answers
  // 503 setup_token_unreadable; without a catalog entry the screen would fall back to
  // "Something went wrong.", and the token advice would send the operator to retype it.
  it('names the remedy when the engine cannot read its setup token', async () => {
    const user = userEvent.setup()
    mockSetup.mockRejectedValue(
      new ApiError(503, 'setup_token_unreadable', 'setup token unreadable'),
    )

    render(<SetupPage />, { wrapper: Wrapper })
    await user.type(await screen.findByLabelText(/setup token/i), 'olst_tok')
    await user.type(
      screen.getByLabelText(/administrator email/i),
      'admin@example.com',
    )
    await user.type(screen.getByLabelText(/password/i), 'correct-horse-battery')
    await user.click(
      screen.getByRole('button', { name: /create administrator/i }),
    )

    const shown = (await screen.findByRole('alert')).textContent
    expect(shown).toMatch(/cannot read its setup token file/)
    expect(shown).toMatch(
      /`olivares first-boot --data-dir <data directory> --new-token`/,
    )
    expect(shown).not.toMatch(/Something went wrong/i)
  })

  it('names the same unreadable-token command in every locale', () => {
    expect(Object.keys(errorLocales).sort()).toEqual(
      LANGUAGE_CODES.map(
        (l) => `/src/lib/i18n/locales/${l}/errors.json`,
      ).sort(),
    )
    for (const [file, { codes }] of Object.entries(errorLocales))
      expect(
        codes.setup_token_unreadable,
        `${file} codes.setup_token_unreadable`,
      ).toContain(
        '`olivares first-boot --data-dir <data directory> --new-token`',
      )
  })
})
