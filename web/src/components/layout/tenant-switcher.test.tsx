// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
import { render, screen } from '@testing-library/react'
import { expect, it, vi } from 'vitest'
import { systemApi } from '@/lib/api/endpoints'
import { TenantSwitcher } from './tenant-switcher'

vi.mock('@/features/extensions', () => ({ PANEL_EXTENSIONS: {} }))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    isSuperadmin: true,
    grants: [
      { tenant: 'own', tenant_name: 'Default Organization', role: 'owner' },
    ],
    activeTenant: 'own',
    setActiveTenant: vi.fn(),
  }),
}))

it('shows the single organization as a label and never requests the estate', () => {
  const list = vi.spyOn(systemApi, 'listOrgs')
  render(<TenantSwitcher />)
  expect(screen.getByText('Default Organization')).toBeInTheDocument()
  expect(screen.queryByRole('button')).toBeNull()
  expect(list).not.toHaveBeenCalled()
})
