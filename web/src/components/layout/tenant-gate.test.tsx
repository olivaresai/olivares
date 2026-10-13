// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, expect, it, vi } from 'vitest'
import { TenantGate } from './tenant-gate'
import { QueryClientProvider } from '@tanstack/react-query'
import { createTestQueryClient } from '@/test/intel'
import { systemApi } from '@/lib/api/endpoints'
import type { ReactNode } from 'react'

const auth = vi.hoisted(() => ({
  activeTenant: null as string | null,
  isSuperadmin: true,
  grants: [] as { tenant: string; tenant_name?: string }[],
  setActiveTenant: vi.fn(),
}))
const route = vi.hoisted(() => ({ pathname: '/' }))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => auth }))
vi.mock('@tanstack/react-router', () => ({
  useRouterState: () => route.pathname,
}))
vi.mock('@/features/extensions', () => ({ PANEL_EXTENSIONS: {} }))

const client = createTestQueryClient()
function mount(children: ReactNode) {
  return render(
    <QueryClientProvider client={client}>{children}</QueryClientProvider>,
  )
}

beforeEach(() => {
  client.clear()
  auth.activeTenant = null
  auth.grants = []
  route.pathname = '/'
  vi.clearAllMocks()
})

it('holds scoped content without an organization and offers no provisioning action', () => {
  const list = vi
    .spyOn(systemApi, 'listOrgs')
    .mockResolvedValue({ items: [], has_more: false })
  const child = vi.fn(() => <div>Scoped content</div>)
  const Child = child
  mount(
    <TenantGate>
      <Child />
    </TenantGate>,
  )
  expect(child).not.toHaveBeenCalled()
  expect(list).not.toHaveBeenCalled()
  expect(
    screen.getByText(/not a member of any organization/i),
  ).toBeInTheDocument()
  expect(
    screen.queryByRole('button', { name: /create organization/i }),
  ).toBeNull()
})

it('allows personal settings before an organization is selected', () => {
  route.pathname = '/settings'
  mount(<TenantGate>Personal settings</TenantGate>)
  expect(screen.getByText('Personal settings')).toBeInTheDocument()
})

it('waits for AuthProvider to select the single grant', () => {
  auth.grants = [{ tenant: 'own', tenant_name: 'Default Organization' }]
  mount(<TenantGate>Scoped content</TenantGate>)
  expect(screen.getByRole('status')).toBeInTheDocument()
  expect(screen.queryByText('Scoped content')).toBeNull()
})

it('offers only the caller’s grants and selects the chosen grant', async () => {
  auth.grants = [
    { tenant: 'one', tenant_name: 'Own one' },
    { tenant: 'two', tenant_name: 'Own two' },
  ]
  mount(<TenantGate>Scoped content</TenantGate>)
  await userEvent.setup().click(screen.getByRole('button', { name: 'Own two' }))
  expect(auth.setActiveTenant).toHaveBeenCalledWith('two')
  expect(screen.queryByText('Scoped content')).toBeNull()
})

it('mounts scoped content once the caller has selected an organization', () => {
  auth.activeTenant = 'own'
  mount(<TenantGate>Scoped content</TenantGate>)
  expect(screen.getByText('Scoped content')).toBeInTheDocument()
})
