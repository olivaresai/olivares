// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The launch dialog's opt-in "new git worktree": offered only once a folder is chosen,
// unticked by default, and sent only when ticked. A launch that does not tick it is the
// request it always was (the exact-body test in run-create-first-message.test.tsx pins
// that no `worktree` key appears).
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
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

const homeA: ProviderProfileDTO = {
  profile_ref: 'ppf_a',
  driver: 'claude',
  environment_ref: 'xenv_1',
  display_name: 'Home A',
  state: 'active',
  local_environment: true,
  operable: true,
}

function wrap() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <RunCreateDialog open onOpenChange={vi.fn()} />
    </QueryClientProvider>,
  )
}

const postedBody = () =>
  vi.mocked(agentOpsApi.createRun).mock.calls[0][0] as unknown as Record<
    string,
    unknown
  >

async function openAdvanced(user: ReturnType<typeof userEvent.setup>) {
  const toggle = await screen.findByRole('button', { name: 'Advanced options' })
  if (toggle.getAttribute('aria-expanded') !== 'true') await user.click(toggle)
  expect(
    await screen.findByText('Local requirements checked'),
  ).toBeInTheDocument()
}

async function chooseFolder(user: ReturnType<typeof userEvent.setup>) {
  await openAdvanced(user)
  await user.click(screen.getByLabelText('Folder'))
  await user.click(
    await screen.findByRole('option', { name: /Project folder/ }),
  )
}

const worktreeBox = () =>
  screen.queryByRole('checkbox', { name: 'Work in a new git worktree' })

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

describe('RunCreateDialog — new git worktree', () => {
  it('is offered only with a folder and starts unticked', async () => {
    const user = userEvent.setup()
    wrap()
    await openAdvanced(user)
    expect(worktreeBox()).not.toBeInTheDocument()
    await user.click(screen.getByLabelText('Folder'))
    await user.click(
      await screen.findByRole('option', { name: /Project folder/ }),
    )
    expect(worktreeBox()).toBeInTheDocument()
    expect(worktreeBox()).not.toBeChecked()
  })

  it('launches in the folder itself unless the box is ticked', async () => {
    const user = userEvent.setup()
    wrap()
    await chooseFolder(user)
    await user.click(screen.getByRole('button', { name: 'Start' }))
    await waitFor(() => expect(agentOpsApi.createRun).toHaveBeenCalledOnce())
    expect(postedBody().workspace_ref).toBe('folder-a')
    expect(postedBody()).not.toHaveProperty('worktree')
  })

  it('asks the engine for a worktree when the box is ticked', async () => {
    const user = userEvent.setup()
    wrap()
    await chooseFolder(user)
    await user.click(worktreeBox() as HTMLElement)
    await user.click(screen.getByRole('button', { name: 'Start' }))
    await waitFor(() => expect(agentOpsApi.createRun).toHaveBeenCalledOnce())
    expect(postedBody().workspace_ref).toBe('folder-a')
    expect(postedBody().worktree).toBe(true)
  })

  it('forgets the choice when the folder is cleared', async () => {
    const user = userEvent.setup()
    wrap()
    await chooseFolder(user)
    await user.click(worktreeBox() as HTMLElement)
    await user.click(screen.getByLabelText('Folder'))
    await user.click(
      await screen.findByRole('option', {
        name: 'Temporary folder for this session',
      }),
    )
    expect(worktreeBox()).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Start' }))
    await waitFor(() => expect(agentOpsApi.createRun).toHaveBeenCalledOnce())
    expect(postedBody().workspace_ref).toBe('')
    expect(postedBody()).not.toHaveProperty('worktree')
  })
})
