// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { ApiError } from '@/lib/api/errors'
import { VaultsView } from './vaults-view'

const authState = vi.hoisted(() => ({
  activeTenant: 't1' as string | null,
  can: (_permission: string): boolean => true,
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))

const api = vi.hoisted(() => ({
  listVaults: vi.fn(),
  createVault: vi.fn(),
  listVaultShares: vi.fn(),
  shareVault: vi.fn(),
  revokeVaultShare: vi.fn(),
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, governanceApi: { ...actual.governanceApi, ...api } }
})

function wrap(ui: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

const FINANCE = { id: 'v1', name: 'Finance' }

beforeEach(() => {
  vi.resetAllMocks()
  authState.can = () => true
})

describe('VaultsView', () => {
  it('lists the shared vaults the API returns', async () => {
    api.listVaults.mockResolvedValue({ vaults: [FINANCE] })

    wrap(<VaultsView />)

    expect(await screen.findByText('Finance')).toBeInTheDocument()
  })

  it('shows the edition line instead of a list when the deployment answers 501', async () => {
    api.listVaults.mockRejectedValue(
      new ApiError(501, 'business_required', 'shared vaults require Business'),
    )

    wrap(<VaultsView />)

    expect(
      await screen.findByText(/This is part of Olivares AI Business\./),
    ).toBeInTheDocument()
    expect(document.body).not.toHaveTextContent(/business_required|\b501\b/)
    expect(screen.queryByRole('button', { name: 'Create vault' })).toBeNull()
  })

  it('shares a vault with a user at the chosen role, then revokes that grant by subject', async () => {
    api.listVaults.mockResolvedValue({ vaults: [FINANCE] })
    api.listVaultShares.mockResolvedValue({
      vault: 'v1',
      name: 'Finance',
      share_by: 'owner',
      shares: [{ subject_kind: 'user', subject_ref: 'ana', role: 'editor' }],
    })
    api.shareVault.mockResolvedValue({
      subject_kind: 'user',
      subject_ref: 'ana',
      role: 'editor',
    })
    api.revokeVaultShare.mockResolvedValue(undefined)
    const user = userEvent.setup()
    wrap(<VaultsView />)

    await user.click(await screen.findByRole('button', { name: 'Shares' }))
    await user.type(await screen.findByLabelText('Shared with'), 'ana')
    await user.selectOptions(screen.getByLabelText('Role'), 'editor')
    await user.click(screen.getByRole('button', { name: 'Share' }))

    expect(api.shareVault).toHaveBeenCalledWith('v1', {
      subject_kind: 'user',
      subject_ref: 'ana',
      role: 'editor',
    })

    await user.click(await screen.findByRole('button', { name: 'Revoke' }))

    expect(api.revokeVaultShare).not.toHaveBeenCalled()
    await user.click(
      within(screen.getByRole('dialog')).getByRole('button', {
        name: 'Revoke',
      }),
    )

    expect(api.revokeVaultShare).toHaveBeenCalledWith('v1', {
      subject_kind: 'user',
      subject_ref: 'ana',
    })
  })
})
