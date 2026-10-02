// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import userEvent from '@testing-library/user-event'
import { renderIntel, screen, waitFor } from '@/test/intel'
import { reportingApi, type ReportSigning } from '@/features/reporting/api'
import { ApiError } from '@/lib/api/errors'
import { toast } from '@/components/ui/toaster'
import { useModulesStore } from '@/stores/modules'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true, isSuperadmin: true }),
}))
vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
  Toaster: () => null,
}))

import { ReportSigningSettings } from './report-signing-settings'

// S's seam (GET/PUT /v1/m/reporting/signing): no private key, no secret reference.
const signing: ReportSigning = {
  enabled: true,
  ready: true,
  key_id: 'olv-report-2026-10',
  public_key: 'MCowBQYDK2VwAyEAp0Vx3m0bT9qJ6Q9lYk3xg2Yy1Yh2Zc8nG4n4k5Rk6zE=',
  source: 'product',
}

beforeEach(() => {
  vi.restoreAllMocks()
})
afterEach(() => useModulesStore.getState().setOff([]))

describe('Settings > Report signing (S seam)', () => {
  it('says whether bundles are signed, where the key comes from, and the key', async () => {
    vi.spyOn(reportingApi, 'signing').mockResolvedValue(signing)
    renderIntel(<ReportSigningSettings />)
    expect(
      await screen.findByText('Downloaded bundles are signed.'),
    ).toBeInTheDocument()
    expect(screen.getByText('Made by this installation')).toBeInTheDocument()
    expect(screen.getByText('olv-report-2026-10')).toBeInTheDocument()
    expect(screen.getByText(signing.public_key!)).toBeInTheDocument()
    expect(
      screen.getByText(
        'Turning it off keeps the key, so bundles signed before can still be verified.',
      ),
    ).toBeInTheDocument()
  })

  it('on but unable to sign says why', async () => {
    vi.spyOn(reportingApi, 'signing').mockResolvedValue({
      enabled: true,
      ready: false,
      reason: 'no signing key configured',
      source: 'unset',
    })
    renderIntel(<ReportSigningSettings />)
    expect(
      await screen.findByText(
        'Downloaded bundles are not signed yet: no signing key configured.',
      ),
    ).toBeInTheDocument()
    expect(screen.getByText('No key yet')).toBeInTheDocument()
  })

  it('turning it on sends enabled:true and shows what the engine answered', async () => {
    vi.spyOn(reportingApi, 'signing').mockResolvedValue({
      enabled: false,
      ready: false,
      source: 'legacy',
    })
    const put = vi.spyOn(reportingApi, 'setSigning').mockResolvedValue(signing)
    renderIntel(<ReportSigningSettings />)
    expect(
      await screen.findByText('Downloaded bundles are not signed.'),
    ).toBeInTheDocument()
    expect(
      screen.getByText('From the legacy configuration'),
    ).toBeInTheDocument()
    await userEvent.click(
      screen.getByRole('button', { name: 'Turn on signing' }),
    )
    await waitFor(() => expect(put).toHaveBeenCalledWith(true))
    expect(
      await screen.findByText('Downloaded bundles are signed.'),
    ).toBeInTheDocument()
  })

  it('turning it off sends enabled:false', async () => {
    vi.spyOn(reportingApi, 'signing').mockResolvedValue(signing)
    const put = vi
      .spyOn(reportingApi, 'setSigning')
      .mockResolvedValue({ ...signing, enabled: false, ready: false })
    renderIntel(<ReportSigningSettings />)
    await userEvent.click(
      await screen.findByRole('button', { name: 'Turn off signing' }),
    )
    await waitFor(() => expect(put).toHaveBeenCalledWith(false))
  })

  it('a license refusal says what the engine said, not that the role is missing (09 Business)', async () => {
    vi.spyOn(reportingApi, 'signing').mockResolvedValue({
      enabled: false,
      ready: false,
      reason: 'no signing key configured',
      source: 'unset',
    })
    // R1 09 Business, PUT /v1/m/reporting/signing without a license, as answered.
    vi.spyOn(reportingApi, 'setSigning').mockRejectedValue(
      new ApiError(
        403,
        'Forbidden',
        'Using Executive & compliance reporting needs a valid Olivares license that covers it. Install one: olivares license install <file>',
      ),
    )
    renderIntel(<ReportSigningSettings />)
    await userEvent.click(
      await screen.findByRole('button', { name: 'Turn on signing' }),
    )
    expect(
      await screen.findByText(
        'Using Executive & compliance reporting needs a valid Olivares license that covers it. Install one: olivares license install <file>',
      ),
    ).toBeInTheDocument()
    expect(toast.error).not.toHaveBeenCalledWith(expect.stringMatching(/role/i))
  })

  it('with Reports off, one line and the enable action; signing is not read', async () => {
    useModulesStore.getState().setOff(['reporting'])
    const get = vi.spyOn(reportingApi, 'signing')
    renderIntel(<ReportSigningSettings />)
    expect(
      screen.getByText('Reports is not enabled on this installation.'),
    ).toBeInTheDocument()
    expect(
      await screen.findByRole('button', { name: 'Turn on Reports' }),
    ).toBeInTheDocument()
    expect(get).not.toHaveBeenCalled()
  })
})
