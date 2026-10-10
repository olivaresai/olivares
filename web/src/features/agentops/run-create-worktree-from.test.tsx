// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The launch dialog opened for the work a handoff names (K4.A2): the worktree is chosen
// for the person, the commit it starts at is on screen, and that start is sent with the
// worktree. A dialog opened without a start is the dialog it always was: ticking the
// worktree sends no start.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))
vi.mock('./api', async (orig) => {
  const real = (await orig()) as Record<string, unknown>
  return {
    ...real,
    agentOpsApi: {
      createRun: vi.fn(),
      listWorkspaces: vi.fn(),
      listProfiles: vi.fn(),
      profileLaunchReadiness: vi.fn(),
    },
  }
})
vi.mock('@/features/workspace-templates/api', () => ({
  templatesApi: { list: vi.fn(), apply: vi.fn() },
  templatesKeys: {
    list: (t: string | null, p?: unknown) => ['tpl', t, 'list', p ?? null],
    detail: (t: string | null, id: string) => ['tpl', t, 'detail', id],
  },
}))
vi.mock('@tanstack/react-router', async (orig) => ({
  ...(await orig<typeof import('@tanstack/react-router')>()),
  useNavigate: () => vi.fn(),
}))
vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}))

import { templatesApi } from '@/features/workspace-templates/api'
import { agentOpsApi } from './api'
import { fixtureReadiness } from './launch-readiness.fixture'
import { RunCreateDialog } from './run-create-dialog'
import type { ProviderProfileDTO } from './types'

const SHA = '0123456789abcdef0123456789abcdef01234567'

const homeA: ProviderProfileDTO = {
  profile_ref: 'ppf_a',
  driver: 'claude',
  environment_ref: 'xenv_1',
  display_name: 'Home A',
  state: 'active',
  local_environment: true,
  operable: true,
}

function wrap(initialWorktreeFrom?: string) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <RunCreateDialog
        open
        onOpenChange={vi.fn()}
        initialWorktreeFrom={initialWorktreeFrom}
      />
    </QueryClientProvider>,
  )
}

const postedBody = () =>
  vi.mocked(agentOpsApi.createRun).mock.calls[0][0] as unknown as Record<
    string,
    unknown
  >

async function chooseFolder(user: ReturnType<typeof userEvent.setup>) {
  const toggle = await screen.findByRole('button', { name: 'Advanced options' })
  if (toggle.getAttribute('aria-expanded') !== 'true') await user.click(toggle)
  expect(
    await screen.findByText('Local requirements checked'),
  ).toBeInTheDocument()
  await user.click(screen.getByLabelText('Folder'))
  await user.click(
    await screen.findByRole('option', { name: /Project folder/ }),
  )
}

const worktreeBox = () =>
  screen.getByRole('checkbox', { name: 'Work in a new git worktree' })

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(agentOpsApi.listWorkspaces).mockResolvedValue({
    items: [
      {
        workspace_ref: 'folder-a',
        name: 'Project folder',
        state: 'active',
        root_path: '/project',
        mount_mode: 'rw',
        max_read_bytes: 1024,
        dlp_mode: 'off',
      },
    ],
    has_more: false,
  })
  vi.mocked(agentOpsApi.listProfiles).mockResolvedValue({
    items: [homeA],
    has_more: false,
  })
  vi.mocked(templatesApi.list).mockResolvedValue({ items: [], has_more: false })
  vi.mocked(agentOpsApi.createRun).mockResolvedValue({} as never)
  vi.mocked(agentOpsApi.profileLaunchReadiness).mockImplementation(
    async (ref: string) => fixtureReadiness({ profile_ref: ref }),
  )
})

describe('RunCreateDialog — opened for the work a handoff names', () => {
  it('blocks Start and Enter until a folder is chosen', async () => {
    const user = userEvent.setup()
    wrap(SHA)
    await screen.findByText('Local requirements checked')
    const start = screen.getByRole('button', { name: 'Start' })
    expect(start).toBeDisabled()
    expect(
      screen.getByText('Choose a repository folder for the worktree.'),
    ).toBeInTheDocument()
    await user.click(start)
    fireEvent.submit(start.closest('form')!)
    expect(agentOpsApi.createRun).not.toHaveBeenCalled()
    await chooseFolder(user)
    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Start' })).toBeEnabled(),
    )
    await user.click(screen.getByRole('button', { name: 'Start' }))
    await waitFor(() => expect(agentOpsApi.createRun).toHaveBeenCalledOnce())
    expect(postedBody()).toMatchObject({
      workspace_ref: 'folder-a',
      worktree: true,
      worktree_from: SHA,
    })
  })

  it('shows the requested start before a folder is chosen', async () => {
    wrap(SHA)
    await screen.findByText('Local requirements checked')
    expect(worktreeBox()).toBeChecked()
    expect(screen.getByTestId('create-worktree-from')).toHaveTextContent(
      `Starts at ${SHA}`,
    )
  })

  it('keeps the worktree request when the folder is cleared', async () => {
    const user = userEvent.setup()
    wrap(SHA)
    await chooseFolder(user)
    await user.click(screen.getByLabelText('Folder'))
    await user.click(
      await screen.findByRole('option', {
        name: 'Temporary folder for this session',
      }),
    )
    expect(screen.getByRole('button', { name: 'Start' })).toBeDisabled()
    expect(worktreeBox()).toBeChecked()
    expect(screen.getByTestId('create-worktree-from')).toHaveTextContent(SHA)
  })

  it('allows the ordinary temporary-folder launch when the worktree is explicitly unticked', async () => {
    const user = userEvent.setup()
    wrap(SHA)
    await screen.findByText('Local requirements checked')
    await user.click(worktreeBox())
    expect(screen.queryByTestId('create-worktree-from')).not.toBeInTheDocument()
    const start = screen.getByRole('button', { name: 'Start' })
    await waitFor(() => expect(start).toBeEnabled())
    await user.click(start)
    await waitFor(() => expect(agentOpsApi.createRun).toHaveBeenCalledOnce())
    expect(postedBody().workspace_ref).toBe('')
    expect(postedBody()).not.toHaveProperty('worktree')
    expect(postedBody()).not.toHaveProperty('worktree_from')
  })

  it('has the worktree chosen and says where it starts', async () => {
    const user = userEvent.setup()
    wrap(SHA)
    await chooseFolder(user)
    expect(worktreeBox()).toBeChecked()
    expect(screen.getByTestId('create-worktree-from')).toHaveTextContent(
      `Starts at ${SHA}`,
    )
  })

  it('sends the start with the worktree', async () => {
    const user = userEvent.setup()
    wrap(SHA)
    await chooseFolder(user)
    await user.click(screen.getByRole('button', { name: 'Start' }))
    await waitFor(() => expect(agentOpsApi.createRun).toHaveBeenCalledOnce())
    expect(postedBody().workspace_ref).toBe('folder-a')
    expect(postedBody().worktree).toBe(true)
    expect(postedBody().worktree_from).toBe(SHA)
  })

  it('sends neither when the person unticks the worktree', async () => {
    const user = userEvent.setup()
    wrap(SHA)
    await chooseFolder(user)
    await user.click(worktreeBox())
    // The line that says where it starts goes with the choice it belongs to.
    expect(screen.queryByTestId('create-worktree-from')).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Start' }))
    await waitFor(() => expect(agentOpsApi.createRun).toHaveBeenCalledOnce())
    expect(postedBody()).not.toHaveProperty('worktree')
    expect(postedBody()).not.toHaveProperty('worktree_from')
  })

  it('sends no start when it was opened without one', async () => {
    const user = userEvent.setup()
    wrap()
    await chooseFolder(user)
    expect(screen.queryByTestId('create-worktree-from')).not.toBeInTheDocument()
    await user.click(worktreeBox())
    await user.click(screen.getByRole('button', { name: 'Start' }))
    await waitFor(() => expect(agentOpsApi.createRun).toHaveBeenCalledOnce())
    expect(postedBody().worktree).toBe(true)
    expect(postedBody()).not.toHaveProperty('worktree_from')
  })

  it('clears an ordinary worktree choice when its folder is cleared', async () => {
    const user = userEvent.setup()
    wrap()
    await chooseFolder(user)
    await user.click(worktreeBox())
    await user.click(screen.getByLabelText('Folder'))
    await user.click(
      await screen.findByRole('option', {
        name: 'Temporary folder for this session',
      }),
    )
    expect(
      screen.queryByRole('checkbox', { name: 'Work in a new git worktree' }),
    ).not.toBeInTheDocument()
    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Start' })).toBeEnabled(),
    )
    await user.click(screen.getByRole('button', { name: 'Start' }))
    await waitFor(() => expect(agentOpsApi.createRun).toHaveBeenCalledOnce())
    expect(postedBody().workspace_ref).toBe('')
    expect(postedBody()).not.toHaveProperty('worktree')
  })
})
