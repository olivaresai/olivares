// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const auth = vi.hoisted(() => ({
  activeTenant: null as string | null,
  can: (_p: string): boolean => true,
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => auth }))

const api = vi.hoisted(() => ({
  status: vi.fn(),
  enrol: vi.fn(),
  activate: vi.fn(),
  remove: vi.fn(),
  policy: vi.fn(),
  setPolicy: vi.fn(),
}))
vi.mock('./api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('./api')>()),
  identityKeys: (await importOriginal<typeof import('./api')>()).identityKeys,
  totpApi: api,
}))

import { TOTPTab } from './totp'
import './i18n'

const enrolmentDTO = {
  secret: 'JBSWY3DPEHPK3PXP',
  uri: 'otpauth://totp/Olivares%20AI:ops@example.com?secret=JBSWY3DPEHPK3PXP',
  algorithm: 'SHA1',
  digits: 6,
  period: 30,
  qr_png_base64:
    'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==',
}

function renderTab() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <TOTPTab />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  auth.activeTenant = null
  auth.can = () => true
  api.status.mockResolvedValue({ enrolled: false, recovery_codes_remaining: 0 })
  api.policy.mockResolvedValue({ require_for_admins: false })
})

describe('TOTP factor tab', () => {
  it('renders the not-enrolled state', async () => {
    renderTab()
    expect(await screen.findByText('Not enrolled')).toBeInTheDocument()
    expect(api.status).toHaveBeenCalled()
  })

  it('walks enrolment: QR, activation, one-time recovery codes', async () => {
    api.enrol.mockResolvedValue(enrolmentDTO)
    api.activate.mockResolvedValue({
      recovery_codes: ['AAAAAA-BBBBBB-CCCCCC-DDDDDD'],
    })
    const user = userEvent.setup()
    renderTab()

    await user.click(await screen.findByRole('button', { name: 'Enroll a factor' }))
    // The provisioning material is shown once: QR + manual-entry secret.
    expect(await screen.findByAltText(/QR code/i)).toBeInTheDocument()
    expect(screen.getByText('JBSWY3DPEHPK3PXP')).toBeInTheDocument()

    await user.type(screen.getByLabelText(/Code from your app/i), '123456')
    await user.click(screen.getByRole('button', { name: 'Activate' }))
    // The recovery codes appear exactly once with the save acknowledgement.
    expect(
      await screen.findByText('AAAAAA-BBBBBB-CCCCCC-DDDDDD'),
    ).toBeInTheDocument()
    expect(api.activate).toHaveBeenCalledWith({ code: '123456' })

    await user.click(screen.getByRole('button', { name: /I saved the codes/i }))
    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Enroll a factor' })).toBeInTheDocument(),
    )
  })

  it('surfaces a wrong activation code as the panel error, not a crash', async () => {
    api.enrol.mockResolvedValue(enrolmentDTO)
    api.activate.mockRejectedValue(
      Object.assign(new Error('auth: TOTP verification failed'), {
        status: 401,
      }),
    )
    const user = userEvent.setup()
    renderTab()
    await user.click(await screen.findByRole('button', { name: 'Enroll a factor' }))
    await user.type(screen.getByLabelText(/Code from your app/i), '000000')
    await user.click(screen.getByRole('button', { name: 'Activate' }))
    expect(await screen.findByRole('alert')).toHaveTextContent(/TOTP verification failed/i)
  })

  it('renders the deployment policy for system administrators', async () => {
    api.policy.mockResolvedValue({ require_for_admins: true })
    renderTab()
    expect(await screen.findByText('Required')).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: 'Turn off' }),
    ).toBeInTheDocument()
  })

  it('hides the policy section from non-system-admin principals', async () => {
    auth.can = () => false
    renderTab()
    await screen.findByText('Not enrolled')
    expect(screen.queryByText(/Require TOTP for administrators/i)).not.toBeInTheDocument()
    expect(api.policy).not.toHaveBeenCalled()
  })
})
