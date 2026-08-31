// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactElement, ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import './i18n'

const { api, authState } = vi.hoisted(() => ({
  api: {
    getRegistry: vi.fn(),
    listOrgs: vi.fn(),
    setOrgRegion: vi.fn(),
  },
  authState: { can: (_p: string): boolean => true },
}))

vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))
vi.mock('@/features/identity/assurance', () => ({
  AAL: { PASSWORD: 1, MFA: 2, HARDWARE: 3 },
  RequireAssurance: ({ children }: { children: ReactNode }) => <>{children}</>,
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, residencyApi: api }
})

import { ResidencyView } from './residency-view'

const orgs = [
  {
    id: 'o1',
    tenant_id: 't-eu',
    name: 'Acme EU',
    slug: 'acme-eu',
    status: 'active',
    data_region: 'eu',
    created_at: '',
  },
  {
    id: 'o2',
    tenant_id: 't-none',
    name: 'Globex',
    slug: 'globex',
    status: 'active',
    created_at: '',
  },
]

function wrap(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  vi.clearAllMocks()
  authState.can = () => true
  api.getRegistry.mockResolvedValue({
    home_region: 'eu',
    regions: ['eu', 'us'],
    enforces: true,
  })
  api.listOrgs.mockResolvedValue({ items: orgs, has_more: false })
  api.setOrgRegion.mockResolvedValue({ ...orgs[1], data_region: 'eu' })
})

describe('ResidencyView', () => {
  it('forbids a non-superadmin', () => {
    authState.can = () => false
    wrap(<ResidencyView />)
    expect(screen.queryByText('Acme EU')).not.toBeInTheDocument()
    expect(api.getRegistry).not.toHaveBeenCalled()
    expect(api.listOrgs).not.toHaveBeenCalled()
  })

  it('lists orgs and shows the registry posture in the header', async () => {
    wrap(<ResidencyView />)
    expect(await screen.findByText('Acme EU')).toBeInTheDocument()
    // Pinned org shows the region; unpinned shows the Unpinned badge.
    const euRow = screen.getByText('Acme EU').closest('tr')!
    expect(within(euRow).getByText('eu')).toBeInTheDocument()
    const globexRow = screen.getByText('Globex').closest('tr')!
    expect(within(globexRow).getByText(/unpinned/i)).toBeInTheDocument()
    const posture = screen.getByLabelText(/residency registry posture/i)
    expect(within(posture).getByText('eu')).toBeInTheDocument()
    expect(within(posture).getByText('Enforced')).toBeInTheDocument()
    expect(api.getRegistry).toHaveBeenCalledTimes(1)
  })

  it('renders registry options plus clear-pin and sends the selected region after review', async () => {
    const user = userEvent.setup()
    wrap(<ResidencyView />)

    const globexRow = (await screen.findByText('Globex')).closest('tr')!
    await user.click(
      within(globexRow).getByRole('button', { name: /set region/i }),
    )
    const dialog = await screen.findByRole('dialog')
    await user.click(
      within(dialog).getByRole('combobox', { name: /region code/i }),
    )
    expect(
      await screen.findByRole('option', { name: /clear pin/i }),
    ).toBeInTheDocument()
    expect(screen.getByRole('option', { name: 'eu' })).toBeInTheDocument()
    await user.click(screen.getByRole('option', { name: 'us' }))
    // Step 1 → review; nothing written yet.
    await user.click(
      within(dialog).getByRole('button', { name: /review change/i }),
    )
    expect(api.setOrgRegion).not.toHaveBeenCalled()
    expect(
      within(dialog).getByText(/pin globex to region us/i),
    ).toBeInTheDocument()
    // Step 2 → apply.
    await user.click(
      within(dialog).getByRole('button', { name: /apply change/i }),
    )
    await waitFor(() =>
      expect(api.setOrgRegion).toHaveBeenCalledWith('t-none', 'us'),
    )
  })

  it('clears a pin with the explicit clear-pin option', async () => {
    const user = userEvent.setup()
    wrap(<ResidencyView />)

    const euRow = (await screen.findByText('Acme EU')).closest('tr')!
    await user.click(within(euRow).getByRole('button', { name: /set region/i }))
    const dialog = await screen.findByRole('dialog')
    await user.click(
      within(dialog).getByRole('combobox', { name: /region code/i }),
    )
    await user.click(await screen.findByRole('option', { name: /clear pin/i }))
    await user.click(
      within(dialog).getByRole('button', { name: /review change/i }),
    )
    expect(
      within(dialog).getByText(/clear the region pin/i),
    ).toBeInTheDocument()
    await user.click(
      within(dialog).getByRole('button', { name: /apply change/i }),
    )
    await waitFor(() =>
      expect(api.setOrgRegion).toHaveBeenCalledWith('t-eu', ''),
    )
  })

  it('keeps the honest free-text fallback when the registry is empty', async () => {
    api.getRegistry.mockResolvedValue({
      home_region: '',
      regions: [],
      enforces: false,
    })
    const user = userEvent.setup()
    wrap(<ResidencyView />)

    const globexRow = (await screen.findByText('Globex')).closest('tr')!
    await user.click(
      within(globexRow).getByRole('button', { name: /set region/i }),
    )
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByLabelText(/region code/i)).toHaveAttribute(
      'type',
      'text',
    )
    expect(
      within(dialog).getByText(/single-region\/default posture/i),
    ).toBeInTheDocument()
    expect(
      within(dialog).queryByRole('combobox', { name: /region code/i }),
    ).toBeNull()
  })
})
