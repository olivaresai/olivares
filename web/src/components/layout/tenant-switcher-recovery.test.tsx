// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/errors'
import { TenantSwitcher } from './tenant-switcher'

vi.mock('@/features/extensions', () => ({
  PANEL_EXTENSIONS: {
    useOrganizationInventory: () => ({
      error: new ApiError(
        501,
        'cross_tenant_admin_pool_not_configured',
        'Unavailable',
      ),
    }),
  },
}))
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

it('offers copyable recovery commands in a dialog outside the organization menu', async () => {
  const user = userEvent.setup()
  render(<TenantSwitcher />)
  await user.click(screen.getByRole('button', { name: 'Default Organization' }))
  const menu = await screen.findByRole('menu')
  expect(menu.textContent).not.toContain('`')
  expect(
    within(menu).queryByRole('button', { name: 'Copy command' }),
  ).toBeNull()
  await user.click(within(menu).getByRole('menuitem', { name: /Learn more/ }))
  const dialog = await screen.findByRole('dialog', { name: 'Organization' })
  expect(dialog.textContent).not.toContain('`')
  expect(dialog.querySelectorAll('code')).toHaveLength(2)
  expect(
    within(dialog).getAllByRole('button', { name: 'Copy command' }),
  ).toHaveLength(2)
  expect(dialog.closest('[role="menu"]')).toBeNull()
  await user.keyboard('{Escape}')
  expect(screen.queryByRole('dialog')).toBeNull()
  expect(
    screen.getByRole('button', { name: 'Default Organization' }),
  ).toHaveFocus()
})
