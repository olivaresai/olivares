// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Bring your folders: the wizard offers the folders the person's tools already worked
// in, preselects the active ones, registers nothing until the person confirms, registers
// each confirmed folder once through the one folder registration, and closes with what
// was imported and what was skipped, each skip with its reason.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    can: () => true,
    isSuperadmin: true,
    activeTenant: 'tnt-a',
  }),
}))

import { agentOpsApi } from '@/features/agentops/api'
import type { FolderCandidatesDTO } from '@/features/agentops/types'
import { useTenantStore } from '@/stores/tenant'
import { BringFolders } from './bring-folders'

const recent = new Date(Date.now() - 2 * 86_400_000).toISOString()
const old = new Date(Date.now() - 90 * 86_400_000).toISOString()

const candidates: FolderCandidatesDTO = {
  tools: [
    { tool: 'claude', name: 'Claude Code', readable: true },
    { tool: 'codex', name: 'Codex', readable: false },
  ],
  folders: [
    {
      path: '/srv/work/api',
      tool: 'claude',
      last_used: recent,
      conversations: 4,
      preselected: true,
    },
    {
      path: '/srv/work/api',
      tool: 'codex',
      last_used: recent,
      conversations: 3,
      preselected: true,
    },
    {
      path: '/srv/work/old',
      tool: 'claude',
      last_used: old,
      conversations: 5,
      preselected: false,
    },
    {
      path: '/srv/gone/project',
      tool: 'claude',
      last_used: recent,
      conversations: 3,
      preselected: false,
      skip: 'missing',
    },
  ],
}

type Created = Awaited<ReturnType<typeof agentOpsApi.createWorkspace>>

function renderStep() {
  const create = vi
    .spyOn(agentOpsApi, 'createWorkspace')
    .mockImplementation((body) =>
      Promise.resolve({
        workspace_ref: `ws_${body.root_path}`,
        root_path: body.root_path,
        state: 'active',
      } as Created),
    )
  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <BringFolders onSkip={vi.fn()} />
    </QueryClientProvider>,
  )
  return create
}

beforeEach(() => {
  vi.restoreAllMocks()
  useTenantStore.setState({ activeTenant: 'tnt-a' })
  vi.spyOn(agentOpsApi, 'listFolderCandidates').mockResolvedValue(candidates)
  vi.spyOn(agentOpsApi, 'listWorkspaces').mockResolvedValue({
    items: [],
    has_more: false,
  } as Awaited<ReturnType<typeof agentOpsApi.listWorkspaces>>)
})

describe('Bring your folders', () => {
  it('preselects the active folders and registers nothing until the person confirms', async () => {
    const create = renderStep()
    expect(
      await screen.findByRole('checkbox', {
        name: 'Register /srv/work/api (Claude Code)',
      }),
    ).toBeChecked()
    // The same folder used by two tools is two rows, each its own control.
    expect(
      screen.getByRole('checkbox', { name: 'Register /srv/work/api (Codex)' }),
    ).toBeChecked()
    expect(
      screen.getByRole('checkbox', {
        name: 'Register /srv/work/old (Claude Code)',
      }),
    ).not.toBeChecked()
    expect(
      screen.getByRole('checkbox', {
        name: 'Register /srv/gone/project (Claude Code)',
      }),
    ).toBeDisabled()
    expect(screen.getByText('The folder no longer exists')).toBeInTheDocument()
    expect(
      screen.getByText(
        "Codex's records could not be read. Add folders by hand instead.",
      ),
    ).toBeInTheDocument()
    expect(create).not.toHaveBeenCalled()
  })

  it('registers each confirmed folder once and closes with what was imported and skipped', async () => {
    // The list is read again after the import: what was registered now says so.
    const refreshed: FolderCandidatesDTO = {
      ...candidates,
      folders: candidates.folders.map((f) =>
        f.path === '/srv/work/api'
          ? { ...f, preselected: false, skip: 'already_registered' }
          : f,
      ),
    }
    const read = vi
      .spyOn(agentOpsApi, 'listFolderCandidates')
      .mockResolvedValueOnce(candidates)
      .mockResolvedValue(refreshed)
    const create = renderStep()
    create.mockImplementation((body) =>
      body.root_path === '/srv/work/old'
        ? Promise.reject(new Error('/srv/work/old is not writable.'))
        : Promise.resolve({
            workspace_ref: 'ws_api',
            root_path: body.root_path,
            state: 'active',
          } as Created),
    )
    const user = userEvent.setup()
    await user.click(
      await screen.findByRole('checkbox', {
        name: 'Register /srv/work/old (Claude Code)',
      }),
    )
    // /srv/work/api is one registration though two tools used it.
    await user.click(
      screen.getByRole('button', { name: 'Register selected (2)' }),
    )
    expect(
      await screen.findByText('Registered: 1. Skipped: 2.'),
    ).toBeInTheDocument()
    await waitFor(() => expect(create).toHaveBeenCalledTimes(2))
    expect(create.mock.calls.map(([body]) => body.root_path).sort()).toEqual([
      '/srv/work/api',
      '/srv/work/old',
    ])
    expect(
      screen.getByText(/\/srv\/work\/old is not writable\./),
    ).toBeInTheDocument()
    expect(screen.getAllByText(/The folder no longer exists/)).toHaveLength(1)
    // The read after the import does not change what the closing line said.
    await waitFor(() => expect(read).toHaveBeenCalledTimes(2))
    expect(screen.getByText('Registered: 1. Skipped: 2.')).toBeInTheDocument()
    // The step is done: the list and the confirm button give way to the closing line.
    expect(
      screen.queryByRole('button', { name: /Register selected/ }),
    ).not.toBeInTheDocument()
  })
})
