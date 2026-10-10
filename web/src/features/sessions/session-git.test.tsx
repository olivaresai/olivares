// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { agentOpsApi } from '@/features/agentops/api'
import type { RunDTO } from '@/features/agentops/types'
import { SessionGit } from './session-git'

let writable = true
let activeTenant = 't1'
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant, can: () => writable }),
}))
vi.mock('@/features/agentops/api', async (orig) => ({
  ...(await orig<typeof import('@/features/agentops/api')>()),
  agentOpsApi: { gitStatus: vi.fn(), gitAction: vi.fn(), getRun: vi.fn() },
}))
const run = {
  run_ref: 'run_1',
  state: 'stopped',
  workspace_path: '/repo',
} as RunDTO
const status = {
  branch: 'main',
  branches: ['main', 'topic'],
  writable: true,
  files: [{ path: 'README.md', index: ' ', worktree: 'M' }],
  truncated: false,
}
function show(value: RunDTO = run) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const view = render(
    <QueryClientProvider client={client}>
      <SessionGit run={value} />
    </QueryClientProvider>,
  )
  return { ...view, client }
}
beforeEach(() => {
  vi.clearAllMocks()
  writable = true
  activeTenant = 't1'
  vi.mocked(agentOpsApi.gitStatus).mockResolvedValue(status)
  vi.mocked(agentOpsApi.gitAction).mockResolvedValue({ ok: true })
})
describe('Session Git', () => {
  it('shows status and stages the selected file', async () => {
    const user = userEvent.setup()
    show()
    await user.click(
      await screen.findByRole('button', { name: 'Stage README.md' }),
    )
    await waitFor(() =>
      expect(agentOpsApi.gitAction).toHaveBeenCalledWith(
        'run_1',
        'stage',
        {
          paths: ['README.md'],
        },
        expect.objectContaining({ tenant: 't1', sessionEffects: 'none' }),
      ),
    )
    expect(await screen.findByLabelText('Branch')).toHaveValue('main')
  })
  it('unstages without discarding the working file', async () => {
    vi.mocked(agentOpsApi.gitStatus).mockResolvedValue({
      ...status,
      files: [{ path: 'README.md', index: 'M', worktree: ' ' }],
    })
    const user = userEvent.setup()
    show()
    await user.click(
      await screen.findByRole('button', { name: 'Unstage README.md' }),
    )
    await waitFor(() =>
      expect(agentOpsApi.gitAction).toHaveBeenCalledWith(
        'run_1',
        'unstage',
        {
          paths: ['README.md'],
        },
        expect.objectContaining({ tenant: 't1', sessionEffects: 'none' }),
      ),
    )
  })
  it('commits staged changes with a message and clears it after success', async () => {
    vi.mocked(agentOpsApi.gitStatus).mockResolvedValue({
      ...status,
      files: [{ path: 'README.md', index: 'M', worktree: ' ' }],
    })
    const user = userEvent.setup()
    show()
    const message = await screen.findByLabelText('Commit message')
    expect(screen.getByRole('button', { name: 'Commit' })).toBeDisabled()
    await user.type(message, 'Explain this change')
    await user.click(screen.getByRole('button', { name: 'Commit' }))
    await waitFor(() =>
      expect(agentOpsApi.gitAction).toHaveBeenCalledWith(
        'run_1',
        'commit',
        {
          message: 'Explain this change',
        },
        expect.objectContaining({ tenant: 't1', sessionEffects: 'none' }),
      ),
    )
    await waitFor(() => expect(message).toHaveValue(''))
  })
  it('switches to a local branch and creates a new branch', async () => {
    const user = userEvent.setup()
    show()
    await user.selectOptions(await screen.findByLabelText('Branch'), 'topic')
    await waitFor(() =>
      expect(agentOpsApi.gitAction).toHaveBeenCalledWith(
        'run_1',
        'branch',
        {
          name: 'topic',
          create: false,
        },
        expect.objectContaining({ tenant: 't1', sessionEffects: 'none' }),
      ),
    )
    await user.click(screen.getByRole('button', { name: 'New branch' }))
    await user.type(screen.getByLabelText('Branch name'), 'session/topic')
    await user.click(screen.getByRole('button', { name: 'Create branch' }))
    await waitFor(() =>
      expect(agentOpsApi.gitAction).toHaveBeenCalledWith(
        'run_1',
        'branch',
        {
          name: 'session/topic',
          create: true,
        },
        expect.objectContaining({ tenant: 't1', sessionEffects: 'none' }),
      ),
    )
  })
  it('keeps failures visible and preserves an unsubmitted commit message', async () => {
    vi.mocked(agentOpsApi.gitStatus).mockResolvedValue({
      ...status,
      files: [{ path: 'README.md', index: 'M', worktree: ' ' }],
    })
    vi.mocked(agentOpsApi.gitAction).mockRejectedValue(
      new Error('Git refused the commit'),
    )
    const user = userEvent.setup()
    show()
    await user.type(
      await screen.findByLabelText('Commit message'),
      'Keep this message',
    )
    await user.click(screen.getByRole('button', { name: 'Commit' }))
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Git refused the commit',
    )
    expect(screen.getByLabelText('Commit message')).toHaveValue(
      'Keep this message',
    )
  })
  it('disables writes for read-only sessions and for readers', async () => {
    writable = false
    show()
    expect(
      await screen.findByRole('button', { name: 'Stage README.md' }),
    ).toBeDisabled()
    expect(screen.getByLabelText('Branch')).toBeDisabled()
    expect(agentOpsApi.gitAction).not.toHaveBeenCalled()
  })
  it('shows unavailable and status errors explicitly', async () => {
    vi.mocked(agentOpsApi.gitStatus).mockRejectedValue(
      new Error('Git status unavailable'),
    )
    show()
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Git status unavailable',
    )
  })
  it('reads the new index after staging rather than painting an optimistic result', async () => {
    vi.mocked(agentOpsApi.gitStatus)
      .mockResolvedValueOnce(status)
      .mockResolvedValue({
        ...status,
        files: [{ path: 'README.md', index: 'M', worktree: ' ' }],
      })
    const user = userEvent.setup()
    show()
    await user.click(
      await screen.findByRole('button', { name: 'Stage README.md' }),
    )
    expect(
      await screen.findByRole('button', { name: 'Unstage README.md' }),
    ).toBeEnabled()
    expect(
      screen.queryByRole('button', { name: 'Stage README.md' }),
    ).not.toBeInTheDocument()
  })
  it('honors the server folder policy even for a console writer', async () => {
    vi.mocked(agentOpsApi.gitStatus).mockResolvedValue({
      ...status,
      writable: false,
    })
    show()
    expect(
      await screen.findByRole('button', { name: 'Stage README.md' }),
    ).toBeDisabled()
  })
  it('keeps a focused draft usable during background status refresh', async () => {
    const user = userEvent.setup()
    const { client } = show()
    const input = await screen.findByLabelText('Commit message')
    await user.type(input, 'Keep ')
    vi.mocked(agentOpsApi.gitStatus).mockImplementation(
      () => new Promise(() => {}),
    )
    await act(async () => {
      void client.invalidateQueries({
        predicate: (q) => q.queryKey.includes('git'),
      })
    })
    expect(input).toBeEnabled()
    expect(input).toHaveFocus()
    await user.type(input, 'typing')
    expect(input).toHaveValue('Keep typing')
  })
  it.each(['run', 'tenant'] as const)(
    'retires drafts at the %s boundary',
    async (boundary) => {
      const user = userEvent.setup()
      const { client, rerender } = show()
      await user.type(
        await screen.findByLabelText('Commit message'),
        'Private draft',
      )
      if (boundary === 'tenant') activeTenant = 't2'
      rerender(
        <QueryClientProvider client={client}>
          <SessionGit
            run={boundary === 'run' ? { ...run, run_ref: 'run_2' } : run}
          />
        </QueryClientProvider>,
      )
      expect(await screen.findByLabelText('Commit message')).toHaveValue('')
    },
  )
  it.each(['active', 'ended'] as const)(
    'uses the current %s work lease at dispatch',
    async (state) => {
      const stamped = {
        ...run,
        work_item_id: 'wi',
        work_lease_fence: 6,
        work_lease_state: 'active' as const,
      }
      vi.mocked(agentOpsApi.getRun).mockResolvedValue({
        ...stamped,
        work_lease_state: state,
      })
      const user = userEvent.setup()
      show(stamped)
      await user.click(
        await screen.findByRole('button', { name: 'Stage README.md' }),
      )
      await waitFor(() =>
        expect(agentOpsApi.getRun).toHaveBeenCalledWith(
          'run_1',
          expect.objectContaining({ tenant: 't1', sessionEffects: 'none' }),
        ),
      )
      expect(agentOpsApi.gitAction).toHaveBeenCalledWith(
        'run_1',
        'stage',
        state === 'active'
          ? { paths: ['README.md'], work_lease_fence: 6 }
          : { paths: ['README.md'] },
        expect.objectContaining({ tenant: 't1', sessionEffects: 'none' }),
      )
    },
  )
  it('abandons a pending fenced action when its owner unmounts', async () => {
    let answer!: (value: RunDTO) => void
    vi.mocked(agentOpsApi.getRun).mockImplementation(
      () =>
        new Promise((resolve) => {
          answer = resolve
        }),
    )
    const user = userEvent.setup()
    const { unmount } = show({
      ...run,
      work_item_id: 'wi',
      work_lease_fence: 6,
    })
    await user.click(
      await screen.findByRole('button', { name: 'Stage README.md' }),
    )
    await waitFor(() => expect(agentOpsApi.getRun).toHaveBeenCalled())
    unmount()
    await act(async () =>
      answer({
        ...run,
        work_item_id: 'wi',
        work_lease_fence: 6,
        work_lease_state: 'active',
      }),
    )
    expect(agentOpsApi.gitAction).not.toHaveBeenCalled()
  })
})
