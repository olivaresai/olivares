// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repo root.
//
// HU2-21: the read-only folders of a Sessions workspace, in the workspace's own sheet. The list
// is the engine's: read fresh, replaced whole by PATCH, then read again.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { WorkspaceDTO } from './types'

vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
  Toaster: () => null,
}))

const auth = vi.hoisted(() => ({ admin: true }))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 't1',
    can: (permission: string) =>
      permission === 'sessions:workspace:admin' ? auth.admin : true,
    principal: { aal: 1 },
  }),
}))

const api = vi.hoisted(() => ({
  listWorkspaces: vi.fn(),
  getWorkspace: vi.fn(),
  setWorkspaceReadOnlyFolders: vi.fn(),
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
  return { user, sheet }
}

describe('read-only folders of a workspace (HU2-21)', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    auth.admin = true
    api.listWorkspaces.mockResolvedValue({
      items: [workspace],
      has_more: false,
    })
    api.listFiles.mockResolvedValue({ path: '', entries: [], has_more: false })
    api.getWorkspace.mockResolvedValue({
      ...workspace,
      read_only_folders: ['/srv/reference'],
    })
  })

  it('shows the saved folders and what they protect', async () => {
    const { sheet } = await openWorkspace()
    expect(await within(sheet).findByText('/srv/reference')).toBeInTheDocument()
    expect(
      within(sheet).getByText(/protect file content and directory entries/),
    ).toBeInTheDocument()
    expect(api.getWorkspace).toHaveBeenCalledWith('ws-1')
  })

  it('an administrator replaces the whole list, then reads it again', async () => {
    api.setWorkspaceReadOnlyFolders.mockResolvedValue({
      ...workspace,
      read_only_folders: ['/srv/docs'],
    })
    const { user, sheet } = await openWorkspace()
    await within(sheet).findByText('/srv/reference')
    const reads = api.getWorkspace.mock.calls.length
    await user.click(
      within(sheet).getByRole('button', { name: 'Remove /srv/reference' }),
    )
    await user.type(
      within(sheet).getByLabelText('Folder on this server'),
      '/srv/docs',
    )
    await user.click(within(sheet).getByRole('button', { name: 'Add' }))
    await user.click(
      within(sheet).getByRole('button', { name: 'Save folders' }),
    )
    await waitFor(() =>
      expect(api.setWorkspaceReadOnlyFolders).toHaveBeenCalledWith('ws-1', [
        '/srv/docs',
      ]),
    )
    await waitFor(() =>
      expect(api.getWorkspace.mock.calls.length).toBeGreaterThan(reads),
    )
  })

  it('nothing can be added or saved while the list is still being read', async () => {
    api.getWorkspace.mockReturnValue(new Promise(() => {}))
    const { sheet } = await openWorkspace()
    await within(sheet).findByText(/protect file content and directory entries/)
    expect(within(sheet).queryByLabelText('Folder on this server')).toBeNull()
    expect(
      within(sheet).queryByRole('button', { name: 'Save folders' }),
    ).toBeNull()
    expect(within(sheet).queryByText(/No read-only folders/)).toBeNull()
    expect(api.setWorkspaceReadOnlyFolders).not.toHaveBeenCalled()
  })

  it('a failed read says so and offers no editor', async () => {
    api.getWorkspace.mockRejectedValue(new Error('read failed'))
    const { sheet } = await openWorkspace()
    expect(await within(sheet).findByRole('alert')).toBeInTheDocument()
    expect(within(sheet).queryByLabelText('Folder on this server')).toBeNull()
    expect(
      within(sheet).queryByRole('button', { name: 'Save folders' }),
    ).toBeNull()
    expect(within(sheet).queryByText(/No read-only folders/)).toBeNull()
    expect(api.setWorkspaceReadOnlyFolders).not.toHaveBeenCalled()
  })

  it('a folder is sent exactly as typed', async () => {
    api.setWorkspaceReadOnlyFolders.mockResolvedValue(workspace)
    const { user, sheet } = await openWorkspace()
    await within(sheet).findByText('/srv/reference')
    await user.type(
      within(sheet).getByLabelText('Folder on this server'),
      '/srv/notes ',
    )
    await user.click(within(sheet).getByRole('button', { name: 'Add' }))
    await user.click(
      within(sheet).getByRole('button', { name: 'Save folders' }),
    )
    await waitFor(() =>
      expect(api.setWorkspaceReadOnlyFolders).toHaveBeenCalledWith('ws-1', [
        '/srv/reference',
        '/srv/notes ',
      ]),
    )
  })

  it('a reader sees the folders and cannot change them', async () => {
    auth.admin = false
    const { sheet } = await openWorkspace()
    expect(await within(sheet).findByText('/srv/reference')).toBeInTheDocument()
    expect(
      within(sheet).queryByRole('button', { name: 'Save folders' }),
    ).toBeNull()
    expect(
      within(sheet).queryByRole('button', { name: 'Remove /srv/reference' }),
    ).toBeNull()
  })
})
