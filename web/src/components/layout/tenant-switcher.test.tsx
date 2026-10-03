// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// RM 20:51Z (COMPATC on RC10): on PostgreSQL without the tenant inventory, GET
// /v1/system/orgs answers 501 cross_tenant_admin_pool_not_configured, and the console
// only showed the bare failure. The switcher says once how to enable the list.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/errors'
import { systemApi } from '@/lib/api/endpoints'
import i18n from '@/lib/i18n'
import { TenantSwitcher } from './tenant-switcher'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    isSuperadmin: true,
    grants: [{ tenant: 't-1', role: 'admin' }],
    activeTenant: 't-1',
    setActiveTenant: vi.fn(),
  }),
}))

function mount() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <TenantSwitcher />
    </QueryClientProvider>,
  )
}

beforeEach(() => vi.restoreAllMocks())

describe('the organization list when the database cannot enumerate it', () => {
  it('says how to enable it, once, in the switcher', async () => {
    const list = vi
      .spyOn(systemApi, 'listOrgs')
      .mockRejectedValue(
        new ApiError(
          501,
          'cross_tenant_admin_pool_not_configured',
          'enumeration not authoritative',
        ),
      )
    mount()
    await vi.waitFor(() => expect(list).toHaveBeenCalledTimes(1))
    await userEvent.setup().click(await screen.findByRole('button'))
    const note = await screen.findByText(
      i18n.t('errors:codes.cross_tenant_admin_pool_not_configured'),
    )
    expect(note).toHaveAttribute('data-slot', 'org-list-unavailable')
    expect(list).toHaveBeenCalledTimes(1)
  })

  it('says nothing of the kind for another failure', async () => {
    vi.spyOn(systemApi, 'listOrgs').mockRejectedValue(
      new ApiError(500, 'internal', 'boom'),
    )
    mount()
    await userEvent.setup().click(await screen.findByRole('button'))
    await screen.findByRole('menu')
    expect(
      document.querySelector('[data-slot="org-list-unavailable"]'),
    ).toBeNull()
  })
})
