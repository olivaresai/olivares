// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
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
  it('offers Initialize Git for a folder that is not a repository, in the panel and as the header button', async () => {
    const { ApiError } = await import('@/lib/api/errors')
    vi.mocked(agentOpsApi.gitStatus).mockRejectedValue(
      new ApiError(
        404,
        'not_found',
        'this session folder is not a Git repository',
      ),
    )
    const user = userEvent.setup()
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    render(
      <QueryClientProvider client={client}>
        <SessionGit run={run} variant="init" />
      </QueryClientProvider>,
    )
    await user.click(
      await screen.findByRole('button', { name: 'Initialize Git' }),
    )
    await waitFor(() =>
      expect(agentOpsApi.gitAction).toHaveBeenCalledWith(
        'run_1',
        'init',
        {},
        expect.objectContaining({ tenant: 't1' }),
      ),
    )
  })
  it('shows no Initialize Git where the folder is a repository', async () => {
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    render(
      <QueryClientProvider client={client}>
        <SessionGit run={run} variant="init" />
      </QueryClientProvider>,
    )
    await waitFor(() => expect(agentOpsApi.gitStatus).toHaveBeenCalled())
    expect(
      screen.queryByRole('button', { name: 'Initialize Git' }),
    ).not.toBeInTheDocument()
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
    // The panel's read-only sentence is Commit's reason, said once.
    expect(
      screen.getByRole('button', { name: 'Commit' }),
    ).toHaveAccessibleDescription(
      'Git changes are unavailable with this session’s permissions.',
    )
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
  it('asks for the Git list again when a turn ends', async () => {
    const { client, rerender } = show({ ...run, state: 'running' })
    await screen.findByLabelText('Branch')
    expect(agentOpsApi.gitStatus).toHaveBeenCalledTimes(1)
    // The engine shows a session that answered its turn and waits for the person as idle.
    rerender(
      <QueryClientProvider client={client}>
        <SessionGit run={{ ...run, state: 'idle' }} />
      </QueryClientProvider>,
    )
    // Well inside the 5 s poll: only the end of the turn can ask.
    await waitFor(() => expect(agentOpsApi.gitStatus).toHaveBeenCalledTimes(2))
  })
  it('marks the uncommitted changes to be read again when a turn ends', async () => {
    const { client, rerender } = show({ ...run, state: 'running' })
    await screen.findByLabelText('Branch')
    const diff = agentOpsKeys.runWorktreeDiff('t1', run.run_ref)
    client.setQueryData(diff, { files: [{ path: 'README.md' }] })
    rerender(
      <QueryClientProvider client={client}>
        <SessionGit run={{ ...run, state: 'idle' }} />
      </QueryClientProvider>,
    )
    // A list read before the turn ended may hold files the turn has since committed.
    await waitFor(() =>
      expect(client.getQueryState(diff)?.isInvalidated).toBe(true),
    )
  })
  it('with nothing staged, Commit is disabled and says why', async () => {
    vi.mocked(agentOpsApi.gitStatus)
      .mockResolvedValueOnce(status)
      .mockResolvedValue({
        ...status,
        files: [{ path: 'README.md', index: 'M', worktree: ' ' }],
      })
    const user = userEvent.setup()
    show()
    const commit = () => screen.getByRole('button', { name: 'Commit' })
    await screen.findByRole('button', { name: 'Stage README.md' })
    expect(commit()).toBeDisabled()
    expect(commit()).toHaveAccessibleDescription(
      'Nothing is staged yet. Stage a file to commit it.',
    )
    await user.click(screen.getByRole('button', { name: 'Stage README.md' }))
    await waitFor(() =>
      expect(commit()).toHaveAccessibleDescription('Write a commit message.'),
    )
    await user.type(screen.getByLabelText('Commit message'), 'Explain it')
    expect(commit()).toBeEnabled()
    expect(commit()).not.toHaveAccessibleDescription()
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
