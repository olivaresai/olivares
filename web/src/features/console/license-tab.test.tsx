// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactElement, ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/errors'
import { queryKeys } from '@/lib/api/query'
import './i18n'

const { api, authState } = vi.hoisted(() => ({
  api: {
    getLicense: vi.fn(),
    installLicense: vi.fn(),
    uninstallLicense: vi.fn(),
  },
  authState: {
    isSuperadmin: true,
    principal: { aal: 3 } as { aal?: number } | null,
    can: (_p: string): boolean => true,
  },
}))

vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))
vi.mock('@/features/identity/assurance', () => ({
  AAL: { PASSWORD: 1, MFA: 2, HARDWARE: 3 },
  // Passthrough gate (AAL3 enforcement is covered by the backend -race tests).
  RequireAssurance: ({ children }: { children: ReactNode }) => <>{children}</>,
}))
vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, consoleApi: api }
})

import { LicenseTab } from './license-tab'

function wrap(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

const validLicense = {
  edition: 'enterprise',
  hot_apply: true,
  status: 'valid',
  source: 'data-dir',
  managed_externally: false,
  licensee: 'Acme Corp',
  plan: 'commercial',
  max_users: 50,
  expires_at: '2027-01-01T00:00:00Z',
  seat_limit: 50,
  seat_limited: true,
  active_users: 12,
}

beforeEach(() => {
  vi.clearAllMocks()
  authState.isSuperadmin = true
})

describe('LicenseTab', () => {
  it('renders the live edition, status and active-user usage', async () => {
    api.getLicense.mockResolvedValue(validLicense)
    wrap(<LicenseTab />)
    expect(await screen.findByText('Acme Corp')).toBeInTheDocument()
    expect(screen.getByText('Valid')).toBeInTheDocument()
    expect(screen.getByText('Enterprise')).toBeInTheDocument()
    // B10: usage, never a quota — the count stands alone, labelled "no limit".
    expect(screen.getByText('Active user accounts')).toBeInTheDocument()
    expect(screen.getByText('12')).toBeInTheDocument()
    expect(screen.getByText('self-hosted: no limit')).toBeInTheDocument()
  })

  it('never renders a seat quota, even when the DTO still carries one', async () => {
    // The engine reports unlimited, but a legacy/enterprise payload may still carry
    // seat_limit/max_users on the wire. The panel must ignore both: rendering a
    // limit nothing enforces is exactly the dishonesty B10 removed.
    api.getLicense.mockResolvedValue(validLicense) // seat_limit 50, max_users 50
    wrap(<LicenseTab />)
    expect(await screen.findByText('Acme Corp')).toBeInTheDocument()
    expect(screen.queryByText('12 / 50')).not.toBeInTheDocument()
    expect(screen.queryByText('50')).not.toBeInTheDocument()
    expect(screen.queryByText(/licensed seats/i)).not.toBeInTheDocument()
  })

  it('shows a renewal banner when the license is expired', async () => {
    api.getLicense.mockResolvedValue({ ...validLicense, status: 'expired' })
    wrap(<LicenseTab />)
    expect(await screen.findByText(/has expired/i)).toBeInTheDocument()
  })

  it('disables install and explains when managed out-of-band', async () => {
    api.getLicense.mockResolvedValue({
      ...validLicense,
      managed_externally: true,
      source: 'env-inline',
    })
    wrap(<LicenseTab />)
    expect(await screen.findByText(/managed out-of-band/i)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /install/i })).toBeDisabled()
  })

  it('retains the acknowledge round-trip when the engine signals a downgrade', async () => {
    // Since B10 no SEAT downgrade exists, but the client half of the contract is kept
    // wired (and inert): if an engine ever answers 409
    // license_downgrade_requires_acknowledge, the panel still pivots to the explicit
    // confirmation instead of a red toast, and echoes the engine's own reason.
    api.getLicense.mockResolvedValue({ ...validLicense, status: 'none' })
    api.installLicense
      .mockRejectedValueOnce(
        new ApiError(
          409,
          'license_downgrade_requires_acknowledge',
          'this change needs an explicit confirmation',
        ),
      )
      .mockResolvedValueOnce({ ...validLicense, status: 'valid' })

    const user = userEvent.setup()
    wrap(<LicenseTab />)
    await user.click(await screen.findByRole('button', { name: /install/i }))

    const dialog = await screen.findByRole('dialog')
    await user.type(
      await screen.findByPlaceholderText(/license-blob/i),
      'some-blob',
    )
    // First click → refused → acknowledge step appears.
    await user.click(within(dialog).getByRole('button', { name: /^install/i }))
    await screen.findByText(/needs an explicit confirmation/i)
    expect(api.installLicense).toHaveBeenCalledWith({
      license: 'some-blob',
      acknowledge: false,
    })

    // Second click on "Confirm downgrade" re-submits WITH acknowledge.
    await user.click(
      within(dialog).getByRole('button', { name: /confirm downgrade/i }),
    )
    await waitFor(() =>
      expect(api.installLicense).toHaveBeenCalledWith({
        license: 'some-blob',
        acknowledge: true,
      }),
    )
  })

  it('refuses to render to a non-superadmin', async () => {
    authState.isSuperadmin = false
    wrap(<LicenseTab />)
    expect(await screen.findByText(/only a superadmin/i)).toBeInTheDocument()
    expect(api.getLicense).not.toHaveBeenCalled()
  })
  /**
   * ⛔ LA CLAVE QUE SE INVALIDA NO ES LA QUE SE REGISTRA, y el comentario del propio código
   * afirmaba lo contrario: «server-info drives the read-only Settings>About status too».
   *
   * `useServerInfo` registra `queryKeys.serverInfo`, que es `['server-info']`
   * (`lib/api/query.ts`), y este panel invalidaba `['serverInfo']` — otra clave. `invalidateQueries`
   * casa por PREFIJO y estas dos no comparten ni el primer segmento, así que las DOS
   * invalidaciones del fichero no tocaban nada: tras instalar o retirar una licencia, «About»
   * seguía enseñando la edición anterior.
   *
   * EL MUTANTE: devolver el literal `['serverInfo']`. Esta casilla muere.
   */
  it('instalar una licencia invalida la clave que server-info registra', async () => {
    api.getLicense.mockResolvedValue({ ...validLicense, status: 'none' })
    api.installLicense.mockResolvedValue({ ...validLicense, status: 'valid' })

    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    qc.setQueryData(queryKeys.serverInfo, { version: 'antes' })
    expect(qc.getQueryState(queryKeys.serverInfo)?.isInvalidated).toBe(false)

    const user = userEvent.setup()
    render(
      <QueryClientProvider client={qc}>
        <LicenseTab />
      </QueryClientProvider>,
    )
    await user.click(await screen.findByRole('button', { name: /install/i }))
    const dialog = await screen.findByRole('dialog')
    await user.type(
      await screen.findByPlaceholderText(/license-blob/i),
      'some-blob',
    )
    await user.click(within(dialog).getByRole('button', { name: /^install/i }))

    await waitFor(() =>
      expect(qc.getQueryState(queryKeys.serverInfo)?.isInvalidated).toBe(true),
    )
  })
})
