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

const mockSetup = vi.fn()
vi.mock('@/lib/api/endpoints', () => ({
  authApi: {
    setup: (...args: unknown[]) => mockSetup(...args),
    serverInfo: () =>
      Promise.resolve({
        version: 'test',
        engine: 'test',
        setup_required: true,
        license: { status: 'community', licensee: '' },
      }),
  },
}))

import { SetupPage } from './setup'
import { ApiError } from '@/lib/api/errors'
import { useTenantStore } from '@/stores/tenant'

const TENANT_ID = '018f5a20-0000-7000-8000-00000000beef'

function Wrapper({ children }: { children: ReactNode }) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  return <QueryClientProvider client={qc}>{children}</QueryClientProvider>
}

beforeEach(() => {
  vi.clearAllMocks()
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
    expect(shown.textContent).toMatch(/--admin-role/)
    expect(shown.textContent).toMatch(/NOSUPERUSER BYPASSRLS/)
    expect(screen.queryByText(/Something went wrong/i)).toBeNull()
  })
})
