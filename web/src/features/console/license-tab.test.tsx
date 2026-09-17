// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactElement, ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/errors'
import { queryKeys } from '@/lib/api/query'
import './i18n'

const { api, authState } = vi.hoisted(() => ({
  api: {
    getLicense: vi.fn(),
    getActivation: vi.fn(),
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

import { consoleKeys } from './api'
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
  // Labelled Community 501 — this file is not an Enterprise activation suite.
  api.getActivation.mockRejectedValue(
    new ApiError(
      501,
      'activation_unavailable',
      'Activation is not wired on this deployment.',
    ),
  )
})

describe('LicenseTab', () => {
  it('keeps apply-versus-build detail in a closed, keyboard-openable disclosure', async () => {
    api.getLicense.mockResolvedValue(validLicense)
    const user = userEvent.setup()
    wrap(<LicenseTab />)
    expect(await screen.findByText('Acme Corp')).toBeInTheDocument()
    expect(screen.queryByText(/no data loss/i)).not.toBeInTheDocument()
    const intro = document.querySelector('[data-slot="license-intro-header"] p')
    expect(intro?.textContent ?? '').not.toMatch(/binary swap/i)
    const help = document.querySelector(
      '[data-slot="license-apply-help"]',
    ) as HTMLDetailsElement | null
    expect(help).toBeInstanceOf(HTMLDetailsElement)
    expect(help?.open).toBe(false)
    expect(help?.textContent).toMatch(/not a binary swap or a data upgrade/i)
    expect(help?.textContent).toMatch(
      /applying a verified license displays its details here/i,
    )
    expect(help?.textContent).not.toMatch(/a verified license is shown/i)
    const summary = help?.querySelector('summary')
    expect(summary).toBeInstanceOf(HTMLElement)
    expect(summary?.tagName).toBe('SUMMARY')
    await user.click(summary as HTMLElement)
    expect(help?.open).toBe(true)
  })

  it('community with no license does not claim a verified license is shown', async () => {
    api.getLicense.mockResolvedValue({
      edition: 'community',
      hot_apply: true,
      status: 'none',
      source: 'none',
      managed_externally: false,
      max_users: 0,
      seat_limit: 0,
      seat_limited: false,
      active_users: 1,
    })
    wrap(<LicenseTab />)
    expect(await screen.findByText('No license')).toBeInTheDocument()
    expect(screen.getByText('Community')).toBeInTheDocument()
    const note = screen.getByText(
      'Community edition: applying a verified license displays its details here; it does not change this build.',
    )
    expect(note.textContent).not.toMatch(/a verified license is shown/i)
    const help = document.querySelector(
      '[data-slot="license-apply-help"]',
    ) as HTMLDetailsElement | null
    expect(help?.textContent).toMatch(
      /applying a verified license displays its details here/i,
    )
    expect(help?.textContent).not.toMatch(/a verified license is shown/i)
  })

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

  it('first license read failure is unavailable, not no-license or generic ErrorState', async () => {
    api.getLicense.mockRejectedValue(
      new ApiError(500, 'internal', 'first synthetic failure'),
    )
    wrap(<LicenseTab />)
    const panel = await waitFor(() => {
      const el = document.querySelector('[data-slot="license-facts-panel"]')
      expect(el).toBeInstanceOf(HTMLElement)
      return el as HTMLElement
    })
    expect(
      within(panel).getByText(/Current license information is unavailable/i),
    ).toBeInTheDocument()
    expect(within(panel).queryByText(/something went wrong/i)).toBeNull()
    expect(within(panel).queryByText('No license')).toBeNull()
    expect(within(panel).queryByText(/Last retrieved/i)).toBeNull()
    expect(
      within(panel).getByRole('button', { name: /retry license status/i }),
    ).toBeInTheDocument()
    expect(
      document.querySelector('[data-slot="license-apply-help"]'),
    ).toBeTruthy()
    expect(panel.getAttribute('data-kind')).toBe('failed')
    expect(panel.getAttribute('data-had-prior')).toBe('false')
  })

  it('failed license refresh keeps last success historic, not current green or generic ErrorState', async () => {
    api.getLicense.mockResolvedValue(validLicense)
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    render(
      <QueryClientProvider client={qc}>
        <LicenseTab />
      </QueryClientProvider>,
    )
    expect(await screen.findByText('Acme Corp')).toBeInTheDocument()
    expect(
      document
        .querySelector('[data-slot="license-facts"]')
        ?.getAttribute('data-kind'),
    ).toBe('current')

    api.getLicense.mockRejectedValue(new ApiError(500, 'internal', 'later'))
    await act(() => qc.refetchQueries({ queryKey: consoleKeys.license() }))
    await waitFor(() =>
      expect(qc.getQueryState(consoleKeys.license())?.status).toBe('error'),
    )

    const panel = document.querySelector(
      '[data-slot="license-facts-panel"]',
    ) as HTMLElement
    expect(panel).toBeTruthy()
    expect(within(panel).queryByText(/something went wrong/i)).toBeNull()
    expect(
      within(panel).getByText(/could not be refreshed/i),
    ).toBeInTheDocument()
    expect(within(panel).getByText(/Last retrieved/i)).toBeInTheDocument()
    expect(within(panel).getByText('Acme Corp')).toBeInTheDocument()
    const historic = panel.querySelector(
      '[data-slot="license-facts"][data-kind="historic"]',
    )
    expect(historic).toBeTruthy()
    expect(
      panel.querySelector('[data-slot="license-facts"][data-kind="current"]'),
    ).toBeNull()
    expect(historic?.querySelector('.text-success')).toBeNull()
    expect(
      within(panel).queryByText(/unknown until a verified license/i),
    ).toBeNull()
    expect(
      within(panel).getByRole('button', { name: /retry license status/i }),
    ).toBeInTheDocument()
    expect(api.installLicense).not.toHaveBeenCalled()
  })

  it('license retry after a failed refresh restores current facts from the new GET', async () => {
    api.getLicense.mockResolvedValue(validLicense)
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    const user = userEvent.setup()
    render(
      <QueryClientProvider client={qc}>
        <LicenseTab />
      </QueryClientProvider>,
    )
    expect(await screen.findByText('Acme Corp')).toBeInTheDocument()
    api.getLicense.mockRejectedValue(new ApiError(500, 'internal', 'later'))
    await act(() => qc.refetchQueries({ queryKey: consoleKeys.license() }))
    await waitFor(() =>
      expect(qc.getQueryState(consoleKeys.license())?.status).toBe('error'),
    )
    const panel = await waitFor(() => {
      const el = document.querySelector('[data-slot="license-facts-panel"]')
      expect(el).toBeInstanceOf(HTMLElement)
      return el as HTMLElement
    })
    expect(
      within(panel).getByText(/could not be refreshed/i),
    ).toBeInTheDocument()

    const before = api.getLicense.mock.calls.length
    api.getLicense.mockResolvedValue(validLicense)
    await user.click(
      within(panel).getByRole('button', { name: /retry license status/i }),
    )
    await waitFor(() =>
      expect(qc.getQueryState(consoleKeys.license())?.status).toBe('success'),
    )
    expect(api.getLicense.mock.calls.length).toBeGreaterThan(before)
    const restored = document.querySelector(
      '[data-slot="license-facts-panel"]',
    ) as HTMLElement
    expect(
      restored
        .querySelector('[data-slot="license-facts"]')
        ?.getAttribute('data-kind'),
    ).toBe('current')
    expect(within(restored).queryByText(/could not be refreshed/i)).toBeNull()
    expect(within(restored).getByText('Acme Corp')).toBeInTheDocument()
    expect(api.installLicense).not.toHaveBeenCalled()
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
