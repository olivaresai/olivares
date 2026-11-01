// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The roles page has one place for the edition's role presets: after the custom roles and before
// the groups. The Community console registers none, so its page is unchanged.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import './i18n'

const { api, authState, extensions } = vi.hoisted(() => ({
  api: {
    listRoles: vi.fn(),
    listPermGroups: vi.fn(),
    listGrants: vi.fn(),
    listInheritanceFilters: vi.fn(),
    listGroups: vi.fn(),
    listModelGroups: vi.fn(),
    listModelAccess: vi.fn(),
    listWorkspaces: vi.fn(),
    listAgentGroups: vi.fn(),
    rbacCatalog: vi.fn(),
    delegationAuthority: vi.fn(),
  },
  authState: {
    activeTenant: 't1' as string | null,
    activeRole: 'owner' as string | null,
    isSuperadmin: true,
    principal: { aal: 3 } as { aal?: number } | null,
    can: (_p: string): boolean => true,
  },
  extensions: {
    authorizationForms: undefined as AuthorizationForms | undefined,
  },
}))

vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))
vi.mock('@/features/identity/assurance', () => ({
  AAL: { PASSWORD: 1, MFA: 2, HARDWARE: 3 },
  RequireAssurance: ({ children }: { children: ReactNode }) => <>{children}</>,
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, consoleApi: api }
})
vi.mock('@/features/extensions', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/features/extensions')>()),
  PANEL_EXTENSIONS: {
    identityTabs: [],
    get authorizationForms() {
      return extensions.authorizationForms
    },
  },
}))

import type { AuthorizationForms } from '../panels'
import { RolesTab } from './roles-tab'

const emptyList = { items: [], has_more: false }

function Presets({ canAdmin }: { canAdmin: boolean }) {
  return (
    <section aria-label="Role presets">canAdmin={String(canAdmin)}</section>
  )
}

function registerPresets() {
  extensions.authorizationForms = {
    GrantForm: () => null,
    RoleForm: () => null,
    GroupForm: () => null,
    RolePresets: Presets,
  }
}

function renderRoles() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <RolesTab />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  extensions.authorizationForms = undefined
  authState.can = (_p: string) => true
  authState.isSuperadmin = true
  for (const list of [
    api.listRoles,
    api.listPermGroups,
    api.listGrants,
    api.listInheritanceFilters,
    api.listModelGroups,
    api.listModelAccess,
    api.listWorkspaces,
    api.listAgentGroups,
  ])
    list.mockResolvedValue(emptyList)
  api.listGroups.mockResolvedValue({ groups: [] })
  api.rbacCatalog.mockResolvedValue({
    kinds: ['agent'],
    tree_kinds: ['agent'],
    permissions: [],
    verbs: ['read', 'write', 'admin'],
    builtin_roles: ['viewer', 'editor', 'admin', 'owner'],
    scope_trees: ['tenant'],
  })
  api.delegationAuthority.mockResolvedValue({ superadmin: true, domains: [] })
})

describe('RolesTab — the edition role presets', () => {
  it('draws the registered presets after the custom roles and before the groups, for an admin', async () => {
    registerPresets()
    renderRoles()
    const presets = await screen.findByRole('region', { name: 'Role presets' })
    expect(presets).toHaveTextContent('canAdmin=true')
    const roles = screen.getByRole('heading', { name: 'Custom roles' })
    const groups = await screen.findByRole('heading', {
      name: 'Permission sets',
    })
    expect(
      roles.compareDocumentPosition(presets) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy()
    expect(
      presets.compareDocumentPosition(groups) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy()
  })

  it('tells the presets when the reader may not administer roles', async () => {
    registerPresets()
    authState.can = (p: string) => p !== 'governance:rbac:admin'
    renderRoles()
    expect(
      await screen.findByRole('region', { name: 'Role presets' }),
    ).toHaveTextContent('canAdmin=false')
  })

  it('draws nothing where no edition registers presets', async () => {
    renderRoles()
    await screen.findByRole('heading', { name: 'Custom roles' })
    expect(screen.queryByRole('region', { name: 'Role presets' })).toBeNull()
  })

  it('draws nothing when authorization forms register no presets', async () => {
    extensions.authorizationForms = {
      GrantForm: () => null,
      RoleForm: () => null,
      GroupForm: () => null,
    }
    renderRoles()
    await screen.findByRole('heading', { name: 'Custom roles' })
    expect(screen.queryByRole('region', { name: 'Role presets' })).toBeNull()
  })

  it('draws nothing for a reader who is not a tenant admin', async () => {
    registerPresets()
    authState.isSuperadmin = false
    authState.can = (p: string) => p !== 'tenant:admin'
    renderRoles()
    await screen.findByText(/you administer this installation|delegation/i)
    expect(screen.queryByRole('region', { name: 'Role presets' })).toBeNull()
  })
})
