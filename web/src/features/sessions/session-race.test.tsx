// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const state = vi.hoisted(() => ({
  allowed: true,
  usage: true,
  calls: [] as string[],
}))
// Usage opens with its own read; every other permission follows `allowed`.
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 't1',
    can: (permission: string) =>
      permission === 'sessions:account:read' ? state.usage : state.allowed,
  }),
}))
vi.mock('@/features/agentops/auth-boundary', () => ({
  useAuthBoundary: () => ({ epoch: 1, tenant: 't1' }),
}))
vi.mock('@/features/agentops/work-fence', () => ({
  currentControlFence: async () => undefined,
}))
vi.mock('./session-branch-changes', () => ({
  SessionBranchChanges: ({ run }: { run: { run_ref: string } }) => (
    <div>diff {run.run_ref}</div>
  ),
}))
vi.mock('@/features/agentops/api', () => ({
  agentOpsApi: {
    getRun: vi.fn(),
    stop: vi.fn(),
    cleanup: vi.fn(),
    mergeWorktree: vi.fn(),
  },
}))
import { agentOpsApi } from '@/features/agentops/api'
import type { RunDTO } from '@/features/agentops/types'
import { useTenantStore } from '@/stores/tenant'
import { SessionRace } from './session-race'

const refs = [
  '019aaa00-0000-7000-8000-000000000001',
  '019aaa00-0000-7000-8000-000000000002',
]
const runs = refs.map((run_ref, i) => ({
  run_ref,
  name: `Choice ${i + 1}`,
  state: 'stopped',
  workspace_ref: 'ws1',
  authz_workspace_id: 'scope1',
  worktree_branch: `olivares/choice${i}`,
  cost_micro_usd: i === 0 ? 250000 : undefined,
})) as RunDTO[]

function mount() {
  return render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <SessionRace refs={refs} />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.resetAllMocks()
  state.allowed = true
  state.usage = true
  state.calls = []
  useTenantStore.getState().setActiveTenant('t1')
  vi.mocked(agentOpsApi.getRun).mockImplementation(
    async (ref) => runs[refs.indexOf(ref)],
  )
  vi.mocked(agentOpsApi.stop).mockImplementation(async (ref) => {
    state.calls.push(`stop ${ref}`)
    return runs[refs.indexOf(ref)]
  })
  vi.mocked(agentOpsApi.mergeWorktree).mockImplementation(async (ref) => {
    state.calls.push(`merge ${ref}`)
  })
  vi.mocked(agentOpsApi.cleanup).mockImplementation(async (ref) => {
    state.calls.push(`cleanup ${ref}`)
    return runs[refs.indexOf(ref)]
  })
})

describe('SessionRace', () => {
  it('shows a person Usage opens for each diff and its measured cost, preserving unknown cost', async () => {
    mount()
    expect(await screen.findByText('diff ' + refs[0])).toBeInTheDocument()
    expect(screen.getByText('diff ' + refs[1])).toBeInTheDocument()
    expect(screen.getByText('$0.25')).toBeInTheDocument()
    expect(screen.getByText('cost not reported')).toBeInTheDocument()
  })

  it('tells a person Usage does not open for no cost, nor that none was reported', async () => {
    // A member sees no money; the first choice reported $0.25, the second none.
    state.usage = false
    mount()
    expect(await screen.findByText('diff ' + refs[0])).toBeInTheDocument()
    const choices = screen.getAllByTestId('race-choice')
    expect(choices).toHaveLength(2)
    for (const choice of choices) {
      expect(choice).not.toHaveTextContent('$')
      expect(choice).not.toHaveTextContent('cost not reported')
      // Nor the estimate's note on an empty line where the cost was.
      expect(within(choice).queryByTitle(/an estimate/i)).toBeNull()
      expect(
        within(choice).getByRole('button', { name: 'Keep this one' }),
      ).toBeInTheDocument()
    }
    expect(screen.getByText('diff ' + refs[1])).toBeInTheDocument()
  })

  it('asks a person Usage opens for to review each branch and its cost', async () => {
    mount()
    expect(await screen.findByText('diff ' + refs[0])).toBeInTheDocument()
    expect(
      screen.getByText(
        'Review each branch and its cost. Commit any edits you want to keep before choosing.',
      ),
    ).toBeInTheDocument()
  })

  it('names no cost in the hint to a person Usage does not open for', async () => {
    state.usage = false
    mount()
    expect(await screen.findByText('diff ' + refs[0])).toBeInTheDocument()
    expect(
      screen.getByText(
        'Review each branch. Commit any edits you want to keep before choosing.',
      ),
    ).toBeInTheDocument()
    expect(screen.getByTestId('session-race')).not.toHaveTextContent(/cost/i)
  })

  it('confirms the discard, stops all, merges the choice, then discards only the loser', async () => {
    mount()
    await userEvent.click(
      (await screen.findAllByRole('button', { name: 'Keep this one' }))[0],
    )
    expect(state.calls).toEqual([])
    await userEvent.click(
      screen.getByRole('button', { name: 'Merge and discard the others' }),
    )
    await waitFor(() =>
      expect(state.calls).toEqual([
        `stop ${refs[0]}`,
        `stop ${refs[1]}`,
        `merge ${refs[0]}`,
        `cleanup ${refs[1]}`,
      ]),
    )
    expect(agentOpsApi.cleanup).toHaveBeenCalledWith(
      refs[1],
      true,
      expect.objectContaining({ tenant: 't1' }),
    )
  })

  it('preserves all worktrees if merging is refused', async () => {
    vi.mocked(agentOpsApi.mergeWorktree).mockRejectedValue(
      new Error('Workspace moved'),
    )
    mount()
    await userEvent.click(
      (await screen.findAllByRole('button', { name: 'Keep this one' }))[0],
    )
    await userEvent.click(
      screen.getByRole('button', { name: 'Merge and discard the others' }),
    )
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Workspace moved',
    )
    expect(agentOpsApi.cleanup).not.toHaveBeenCalled()
  })

  it('does not merge or discard when a writer has not stopped', async () => {
    vi.mocked(agentOpsApi.stop).mockResolvedValueOnce({
      ...runs[0],
      state: 'running',
    })
    mount()
    await userEvent.click(
      (await screen.findAllByRole('button', { name: 'Keep this one' }))[0],
    )
    await userEvent.click(
      screen.getByRole('button', { name: 'Merge and discard the others' }),
    )
    expect(await screen.findByRole('alert')).toHaveTextContent('did not stop')
    expect(agentOpsApi.mergeWorktree).not.toHaveBeenCalled()
    expect(agentOpsApi.cleanup).not.toHaveBeenCalled()
  })

  it('reports a merged choice and retained worktrees when cleanup is refused', async () => {
    vi.mocked(agentOpsApi.cleanup).mockRejectedValue(
      new Error('Cleanup refused'),
    )
    mount()
    await userEvent.click(
      (await screen.findAllByRole('button', { name: 'Keep this one' }))[0],
    )
    await userEvent.click(
      screen.getByRole('button', { name: 'Merge and discard the others' }),
    )
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('already merged')
    expect(alert).toHaveTextContent('Cleanup refused')
  })

  it('ends the action when its authority episode changes during a stop', async () => {
    let finish!: (run: RunDTO) => void
    vi.mocked(agentOpsApi.stop).mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          finish = resolve
        }),
    )
    mount()
    await userEvent.click(
      (await screen.findAllByRole('button', { name: 'Keep this one' }))[0],
    )
    await userEvent.click(
      screen.getByRole('button', { name: 'Merge and discard the others' }),
    )
    await waitFor(() => expect(agentOpsApi.stop).toHaveBeenCalledTimes(1))
    await act(async () => {
      useTenantStore.getState().setActiveTenant('t2')
      finish(runs[0])
    })
    await waitFor(() =>
      expect(
        screen.getByRole('button', { name: 'Merge and discard the others' }),
      ).not.toBeDisabled(),
    )
    expect(agentOpsApi.stop).toHaveBeenCalledTimes(1)
    expect(agentOpsApi.mergeWorktree).not.toHaveBeenCalled()
    expect(agentOpsApi.cleanup).not.toHaveBeenCalled()
  })

  it('refuses mixed workspaces before any write', async () => {
    vi.mocked(agentOpsApi.getRun)
      .mockResolvedValueOnce(runs[0])
      .mockResolvedValueOnce({ ...runs[1], workspace_ref: 'ws2' })
    mount()
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'same repository',
    )
    expect(screen.queryByRole('button', { name: 'Keep this one' })).toBeNull()
    expect(state.calls).toEqual([])
  })

  it('offers no destructive action without the normal session permissions', async () => {
    state.allowed = false
    mount()
    expect(await screen.findByText('diff ' + refs[0])).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Keep this one' })).toBeNull()
  })
})
