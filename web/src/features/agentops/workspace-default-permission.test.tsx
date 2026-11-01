// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// the regression: a folder stores the permission its sessions start under. It is set in the folder's
// own sheet, read fresh and saved on its own; Full is offered only to someone who may run it.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fakeRouter } from '@/test/fake-router'
import type { WorkspaceDTO } from './types'

vi.mock('@tanstack/react-router', async () =>
  (await import('@/test/fake-router')).fakeRouterModule(),
)

vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
  Toaster: () => null,
}))

const auth = vi.hoisted(() => ({ admin: true, runAdmin: true }))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 't1',
    can: (permission: string) =>
      permission === 'sessions:workspace:admin'
        ? auth.admin
        : permission === 'sessions:run:admin'
          ? auth.runAdmin
          : true,
    principal: { aal: 1 },
  }),
}))

const api = vi.hoisted(() => ({
  listWorkspaces: vi.fn(),
  getWorkspace: vi.fn(),
  setWorkspaceDefaultPermission: vi.fn(),
  listFiles: vi.fn(),
}))
vi.mock('./api', async (orig) => {
  const real = (await orig()) as Record<string, unknown>
  return { ...real, agentOpsApi: { ...(real.agentOpsApi as object), ...api } }
})

import { WorkspacesPanel } from './workspaces-panel'

const workspace: WorkspaceDTO = {
  workspace_ref: 'ws-1',
  name: 'Repo',
  root_path: '/srv/repo',
  mount_mode: 'rw',
  max_read_bytes: 1048576,
  dlp_mode: 'label',
  state: 'active',
}

async function openWorkspace() {
  const user = userEvent.setup()
  render(
    <QueryClientProvider
      client={
        new QueryClient({
          defaultOptions: {
            queries: { retry: false },
            mutations: { retry: false },
          },
        })
      }
    >
      <WorkspacesPanel />
    </QueryClientProvider>,
  )
  await user.click(await screen.findByRole('button', { name: 'Browse files' }))
  const sheet = await screen.findByRole('dialog')
  const group = await within(sheet).findByRole('radiogroup', {
    name: 'Default permission',
  })
  return { user, sheet, group }
}

describe("a folder's default permission", () => {
  beforeEach(() => {
    fakeRouter.reset('/workspaces')
    vi.clearAllMocks()
    auth.admin = true
    auth.runAdmin = true
    api.listWorkspaces.mockResolvedValue({
      items: [workspace],
      has_more: false,
    })
    api.listFiles.mockResolvedValue({ path: '', entries: [], has_more: false })
    api.getWorkspace.mockResolvedValue({
      ...workspace,
      default_permission: 'supervised',
    })
  })

  it('shows the saved default, and the one after it as not chosen', async () => {
    const { group } = await openWorkspace()
    await waitFor(() =>
      expect(
        within(group).getByRole('radio', { name: 'Supervised' }),
      ).toBeChecked(),
    )
    expect(
      within(group).getByRole('radio', { name: 'No default' }),
    ).not.toBeChecked()
  })

  it('an administrator picks another one and saves it by its name', async () => {
    api.setWorkspaceDefaultPermission.mockResolvedValue({
      ...workspace,
      default_permission: 'auto-accept-edits',
    })
    const { user, sheet, group } = await openWorkspace()
    await waitFor(() =>
      expect(
        within(group).getByRole('radio', { name: 'Supervised' }),
      ).toBeChecked(),
    )
    await user.click(
      within(group).getByRole('radio', { name: 'Auto-accept edits' }),
    )
    await user.click(
      within(sheet).getByRole('button', { name: 'Save default' }),
    )
    await waitFor(() =>
      expect(api.setWorkspaceDefaultPermission).toHaveBeenCalledWith(
        'ws-1',
        'auto-accept-edits',
      ),
    )
  })

  it('takes the default away with an empty name', async () => {
    api.setWorkspaceDefaultPermission.mockResolvedValue(workspace)
    const { user, sheet, group } = await openWorkspace()
    await waitFor(() =>
      expect(
        within(group).getByRole('radio', { name: 'Supervised' }),
      ).toBeChecked(),
    )
    await user.click(within(group).getByRole('radio', { name: 'No default' }))
    await user.click(
      within(sheet).getByRole('button', { name: 'Save default' }),
    )
    await waitFor(() =>
      expect(api.setWorkspaceDefaultPermission).toHaveBeenCalledWith(
        'ws-1',
        '',
      ),
    )
  })

  it('offers Full and Auto only to someone who may run them', async () => {
    auth.runAdmin = false
    const { group } = await openWorkspace()
    expect(within(group).queryByRole('radio', { name: 'Full' })).toBeNull()
    expect(within(group).queryByRole('radio', { name: 'Auto' })).toBeNull()
    expect(
      within(group).getByRole('radio', { name: 'Supervised' }),
    ).toBeInTheDocument()
  })

  it('shows Full to a run administrator in the warning style', async () => {
    const { group } = await openWorkspace()
    expect(within(group).getByText('Full')).toHaveClass('text-warning')
  })

  it('shows the default read-only to someone who cannot administer the folder', async () => {
    auth.admin = false
    const { sheet, group } = await openWorkspace()
    await waitFor(() =>
      expect(
        within(group).getByRole('radio', { name: 'Supervised' }),
      ).toBeChecked(),
    )
    expect(within(group).getByRole('radio', { name: 'Auto' })).toBeDisabled()
    expect(
      within(sheet).queryByRole('button', { name: 'Save default' }),
    ).toBeNull()
  })
})
