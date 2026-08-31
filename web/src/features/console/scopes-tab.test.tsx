// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactElement, ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import './i18n'

const { api, authState } = vi.hoisted(() => ({
  api: {
    listWorkspaces: vi.fn(),
    createWorkspace: vi.fn(),
    updateWorkspace: vi.fn(),
    listAgentGroups: vi.fn(),
    createAgentGroup: vi.fn(),
    updateAgentGroup: vi.fn(),
    deleteAgentGroup: vi.fn(),
    listAgentGroupMembers: vi.fn(),
    addAgentGroupMember: vi.fn(),
    removeAgentGroupMember: vi.fn(),
    listAgents: vi.fn(),
  },
  authState: {
    activeTenant: 't1' as string | null,
    activeRole: 'owner' as string | null,
    isSuperadmin: true,
    principal: { aal: 3 } as { aal?: number } | null,
    can: (_p: string): boolean => true,
  },
}))

vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))
vi.mock('@/lib/hooks/use-workspace-filter', () => ({
  useWorkspaceFilter: () => ({ workspaceId: undefined, queryKey: '__all__' }),
}))
vi.mock('@/features/identity/assurance', () => ({
  AAL: { PASSWORD: 1, MFA: 2, HARDWARE: 3 },
  RequireAssurance: ({ children }: { children: ReactNode }) => <>{children}</>,
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, consoleApi: api }
})

import { ScopesTab } from './scopes-tab'

const emptyList = { items: [], has_more: false }
const workspace = {
  id: 'ws1',
  tenant_id: 't1',
  name: 'Engineering',
  slug: 'eng',
  status: 'active',
  is_default: true,
  created_at: '',
  updated_at: '',
  version: 1,
}
const group = {
  id: 'g1',
  tenant_id: 't1',
  workspace_id: 'ws1',
  name: 'Build agents',
  slug: 'build-agents',
  description: 'old description',
  status: 'active',
  created_at: '',
  updated_at: '',
  version: 1,
}
const agent = {
  id: 'a1',
  tenant_id: 't1',
  workspace_id: 'ws1',
  name: 'Build bot',
  kind: 'claude-code',
  status: 'active',
  created_at: '',
  updated_at: '',
  version: 1,
}

function wrap(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  vi.clearAllMocks()
  authState.activeRole = 'owner'
  authState.isSuperadmin = true
  authState.can = (_p: string) => true
  api.listWorkspaces.mockResolvedValue({ items: [workspace], has_more: false })
  api.listAgentGroups.mockResolvedValue(emptyList)
  api.listAgentGroupMembers.mockResolvedValue(emptyList)
  api.listAgents.mockResolvedValue(emptyList)
})

describe('ScopesTab agent-groups', () => {
  it('shows a forbidden state and does not read groups without agent read access', async () => {
    authState.can = (permission: string) => permission !== 'agent:read'
    wrap(<ScopesTab />)

    expect(
      await screen.findByText(/need agent read access/i),
    ).toBeInTheDocument()
    expect(api.listAgentGroups).not.toHaveBeenCalled()
  })

  it('renders a group load error with retry', async () => {
    api.listAgentGroups.mockRejectedValue(new Error('boom'))
    wrap(<ScopesTab />)

    expect(await screen.findByRole('alert')).toBeInTheDocument()
  })

  it('creates a workspace-scoped agent group', async () => {
    api.createAgentGroup.mockResolvedValue(group)
    const user = userEvent.setup()
    wrap(<ScopesTab />)

    await user.click(
      await screen.findByRole('button', { name: /new agent-group/i }),
    )
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByLabelText(/^name/i), 'Build agents')
    await user.type(within(dialog).getByLabelText(/^slug/i), 'build-agents')
    await user.click(
      within(dialog).getByRole('combobox', { name: /workspace/i }),
    )
    await user.click(screen.getByRole('option', { name: /engineering/i }))
    await user.click(
      within(dialog).getByRole('button', { name: /new agent-group/i }),
    )

    await waitFor(() =>
      expect(api.createAgentGroup).toHaveBeenCalledWith(
        expect.objectContaining({
          name: 'Build agents',
          slug: 'build-agents',
          workspace_id: 'ws1',
          status: 'active',
        }),
      ),
    )
  })

  it('renames a workspace with PATCH', async () => {
    api.updateWorkspace.mockResolvedValue({ ...workspace, name: 'Platform' })
    const user = userEvent.setup()
    wrap(<ScopesTab />)

    const row = (await screen.findByText('Engineering')).closest('tr')!
    await user.click(within(row).getByRole('button', { name: /rename/i }))
    const dialog = await screen.findByRole('dialog', {
      name: /rename workspace/i,
    })
    await user.clear(within(dialog).getByLabelText(/^name/i))
    await user.type(within(dialog).getByLabelText(/^name/i), 'Platform')
    await user.click(within(dialog).getByRole('button', { name: /save name/i }))

    await waitFor(() =>
      expect(api.updateWorkspace).toHaveBeenCalledWith('ws1', {
        name: 'Platform',
      }),
    )
  })

  it('edits an agent group with PATCH', async () => {
    api.listAgentGroups.mockResolvedValue({ items: [group], has_more: false })
    api.updateAgentGroup.mockResolvedValue({ ...group, name: 'Deploy agents' })
    const user = userEvent.setup()
    wrap(<ScopesTab />)

    const row = (await screen.findByText('Build agents')).closest('tr')!
    await user.click(within(row).getByRole('button', { name: /^edit$/i }))
    const dialog = await screen.findByRole('dialog')
    await user.clear(within(dialog).getByLabelText(/^name/i))
    await user.type(within(dialog).getByLabelText(/^name/i), 'Deploy agents')
    await user.click(
      within(dialog).getByRole('button', { name: /save agent-group/i }),
    )

    // The edit carries the group's workspace scope (now editable, not immutable):
    // unchanged here, it sends the current 'ws1' so the backend keeps the scope.
    await waitFor(() =>
      expect(api.updateAgentGroup).toHaveBeenCalledWith('g1', {
        name: 'Deploy agents',
        description: 'old description',
        status: 'active',
        workspace_id: 'ws1',
      }),
    )
  })

  it('adds and removes group members by agent id', async () => {
    api.listAgentGroups.mockResolvedValue({ items: [group], has_more: false })
    api.listAgentGroupMembers.mockResolvedValue({
      items: [{ id: 'm1', group_id: 'g1', agent_id: 'a1' }],
      has_more: false,
    })
    api.listAgents.mockResolvedValue({ items: [agent], has_more: false })
    api.addAgentGroupMember.mockResolvedValue({
      id: 'm2',
      group_id: 'g1',
      agent_id: 'a2',
    })
    api.removeAgentGroupMember.mockResolvedValue(undefined)
    const user = userEvent.setup()
    wrap(<ScopesTab />)

    const row = (await screen.findByText('Build agents')).closest('tr')!
    await user.click(within(row).getByRole('button', { name: /members/i }))
    const dialog = await screen.findByRole('dialog')
    expect(await within(dialog).findByText('Build bot')).toBeInTheDocument()

    await user.type(within(dialog).getByLabelText(/agent id/i), 'a2')
    await user.click(
      within(dialog).getByRole('button', { name: /add member/i }),
    )
    await waitFor(() =>
      expect(api.addAgentGroupMember).toHaveBeenCalledWith('g1', 'a2'),
    )

    await user.click(within(dialog).getByRole('button', { name: /^remove$/i }))
    const confirm = await screen.findByRole('dialog', {
      name: /remove group member/i,
    })
    await user.click(within(confirm).getByRole('button', { name: /^remove$/i }))
    await waitFor(() =>
      expect(api.removeAgentGroupMember).toHaveBeenCalledWith('g1', 'a1'),
    )
  })
})

/**
 * ⛔ EL SEXTO AVISO, con su testigo. El contraste (F-02) midio que el trinquete acepta un
 *    aviso cualquiera POR FEATURE, asi que ninguno de los seis estaba realmente cubierto: quitar
 *    uno no ponia nada rojo. Reproducido y ahora cerrado lista por lista.
 */
describe('ScopesTab — la lista de workspaces declara su recorte', () => {
  it('con has_more sale UN aviso, y sin el ninguno', async () => {
    api.listWorkspaces.mockResolvedValue({ items: [workspace], has_more: true })
    wrap(<ScopesTab />)
    await screen.findByText(workspace.name)
    expect(screen.getAllByText(/there are more/i)).toHaveLength(1)

    cleanup()
    api.listWorkspaces.mockResolvedValue({
      items: [workspace],
      has_more: false,
    })
    wrap(<ScopesTab />)
    await screen.findByText(workspace.name)
    expect(screen.queryByText(/there are more/i)).not.toBeInTheDocument()
  })
})
