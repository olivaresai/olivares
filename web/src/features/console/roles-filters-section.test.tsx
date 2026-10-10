// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import axe from 'axe-core'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import './i18n'

const { api } = vi.hoisted(() => ({
  api: {
    listInheritanceFilters: vi.fn(),
    createInheritanceFilter: vi.fn(),
    removeInheritanceFilter: vi.fn(),
  },
}))

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't-1', can: () => true }),
}))
vi.mock('@/features/identity/assurance', () => ({
  AAL: { PASSWORD: 1, MFA: 2, HARDWARE: 3 },
  RequireAssurance: ({ children }: { children: ReactNode }) => <>{children}</>,
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, consoleApi: api }
})

import { InheritanceFiltersSection } from './roles-filters-section'

const catalog = {
  kinds: ['agent', 'session', 'resource', 'models:keys'],
  tree_kinds: ['agent', 'session', 'resource'],
  permissions: [],
  verbs: ['read', 'write', 'admin'],
  builtin_roles: ['viewer', 'editor', 'admin', 'owner'],
  scope_trees: ['tenant', 'workspace', 'agent_group', 'folder'],
  subject_kinds: ['user', 'role', 'group'],
}

function renderSection(canAdmin = true) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  return render(
    <QueryClientProvider client={client}>
      <InheritanceFiltersSection
        catalog={catalog}
        workspaces={[{ slug: 'payments', name: 'Payments' }]}
        agentGroups={[{ slug: 'ops', name: 'Operations' }]}
        canAdmin={canAdmin}
      />
    </QueryClientProvider>,
  )
}

const listed = {
  items: [
    {
      id: 'f-1',
      scope_tree: 'workspace',
      scope_ref: 'payments',
      scope_class: 'session',
      created_by: 'u-admin',
    },
    {
      id: 'f-2',
      scope_tree: 'folder',
      scope_ref: 'res-9',
      scope_class: 'resource',
      created_by: 'u-admin',
    },
  ],
  has_more: false,
}

describe('InheritanceFiltersSection', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    api.listInheritanceFilters.mockResolvedValue(listed)
  })

  it('shows each filter with its node and class', async () => {
    const { container } = renderSection()
    const row = (await screen.findByText('Workspace: payments')).closest('tr')!
    expect(within(row).getByText('session')).toBeInTheDocument()
    const folder = screen.getByText('Folder: res-9').closest('tr')!
    expect(within(folder).getByText('resource')).toBeInTheDocument()
    const result = await axe.run(container)
    expect(result.violations).toEqual([])
  })

  it('sets a filter on an agent group, offering only the agent class', async () => {
    api.createInheritanceFilter.mockResolvedValue({ id: 'f-3' })
    const user = userEvent.setup()
    renderSection()
    await user.click(await screen.findByRole('button', { name: 'New filter' }))
    const dialog = await screen.findByRole('dialog')

    await user.click(within(dialog).getByRole('combobox', { name: 'Node' }))
    await user.click(screen.getByRole('option', { name: 'Agent-group' }))
    await user.click(
      within(dialog).getByRole('button', { name: /Agent-group/ }),
    )
    await user.click(screen.getByRole('option', { name: 'Operations' }))
    await user.click(
      within(dialog).getByRole('combobox', { name: 'Kind of resource' }),
    )
    expect(screen.getAllByRole('option').map((o) => o.textContent)).toEqual([
      'agent',
    ])
    await user.click(screen.getByRole('option', { name: 'agent' }))
    await user.click(within(dialog).getByRole('button', { name: 'Set filter' }))

    await waitFor(() =>
      expect(api.createInheritanceFilter).toHaveBeenCalledWith({
        scope_tree: 'agent_group',
        scope_ref: 'ops',
        scope_class: 'agent',
      }),
    )
  })

  it('cannot submit before a node and a class are chosen', async () => {
    const user = userEvent.setup()
    renderSection()
    await user.click(await screen.findByRole('button', { name: 'New filter' }))
    const dialog = await screen.findByRole('dialog')
    expect(
      within(dialog).getByRole('button', { name: 'Set filter' }),
    ).toBeDisabled()
  })

  it('clears the chosen node and class when the node type changes', async () => {
    const user = userEvent.setup()
    renderSection()
    await user.click(await screen.findByRole('button', { name: 'New filter' }))
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: /Workspace/ }))
    await user.click(screen.getByRole('option', { name: 'Payments' }))
    await user.click(
      within(dialog).getByRole('combobox', { name: 'Kind of resource' }),
    )
    await user.click(screen.getByRole('option', { name: 'session' }))
    const submit = within(dialog).getByRole('button', { name: 'Set filter' })
    expect(submit).toBeEnabled()

    await user.click(within(dialog).getByRole('combobox', { name: 'Node' }))
    await user.click(screen.getByRole('option', { name: 'Agent-group' }))
    expect(submit).toBeDisabled()
    await user.click(
      within(dialog).getByRole('button', { name: /Agent-group/ }),
    )
    await user.click(screen.getByRole('option', { name: 'Operations' }))
    expect(submit).toBeDisabled()
  })

  it('shows the engine refusal in the form', async () => {
    api.createInheritanceFilter.mockRejectedValue(
      new Error(
        'only an admin of the node may set or remove its inheritance filter',
      ),
    )
    const user = userEvent.setup()
    renderSection()
    await user.click(await screen.findByRole('button', { name: 'New filter' }))
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: /Workspace/ }))
    await user.click(screen.getByRole('option', { name: 'Payments' }))
    await user.click(
      within(dialog).getByRole('combobox', { name: 'Kind of resource' }),
    )
    await user.click(screen.getByRole('option', { name: 'session' }))
    await user.click(within(dialog).getByRole('button', { name: 'Set filter' }))
    expect(
      await within(dialog).findByText(/only an admin of the node/),
    ).toBeInTheDocument()
  })

  it('shows the engine refusal in the remove dialog', async () => {
    api.removeInheritanceFilter.mockRejectedValue(
      new Error(
        'only an admin of the node may set or remove its inheritance filter',
      ),
    )
    const user = userEvent.setup()
    renderSection()
    const row = (await screen.findByText('Folder: res-9')).closest('tr')!
    await user.click(within(row).getByRole('button', { name: 'Remove' }))
    const confirm = await screen.findByRole('dialog')
    await user.click(within(confirm).getByRole('button', { name: 'Remove' }))
    expect(
      await within(confirm).findByText(/only an admin of the node/),
    ).toBeInTheDocument()
  })

  it('removes the row it was asked to, and nothing on cancel', async () => {
    api.removeInheritanceFilter.mockResolvedValue(undefined)
    const user = userEvent.setup()
    renderSection()
    const row = (await screen.findByText('Folder: res-9')).closest('tr')!
    await user.click(within(row).getByRole('button', { name: 'Remove' }))
    await user.click(
      within(await screen.findByRole('dialog')).getByRole('button', {
        name: 'Cancel',
      }),
    )
    expect(api.removeInheritanceFilter).not.toHaveBeenCalled()
    await user.click(within(row).getByRole('button', { name: 'Remove' }))
    await user.click(
      within(await screen.findByRole('dialog')).getByRole('button', {
        name: 'Remove',
      }),
    )
    await waitFor(() =>
      expect(api.removeInheritanceFilter).toHaveBeenCalledWith('f-2'),
    )
  })

  it('removes a filter only after confirmation', async () => {
    api.removeInheritanceFilter.mockResolvedValue(undefined)
    const user = userEvent.setup()
    renderSection()
    const row = (await screen.findByText('Workspace: payments')).closest('tr')!
    await user.click(within(row).getByRole('button', { name: 'Remove' }))
    expect(api.removeInheritanceFilter).not.toHaveBeenCalled()
    const confirm = await screen.findByRole('dialog')
    await user.click(within(confirm).getByRole('button', { name: 'Remove' }))
    await waitFor(() =>
      expect(api.removeInheritanceFilter).toHaveBeenCalledWith('f-1'),
    )
  })

  it('offers no write control to a reader', async () => {
    renderSection(false)
    expect(await screen.findByText('Workspace: payments')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'New filter' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Remove' })).toBeNull()
  })
})
