// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Releasing a session that has a git worktree: the engine removes the worktree and its
// branch by itself when the branch is merged and the worktree is clean, and refuses
// otherwise. The release dialog offers the person's confirmation to discard unmerged or
// uncommitted work, only for a session that has a worktree, and unticked it sends the
// release the engine has always accepted.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))
vi.mock('@/features/agentops/api', async (orig) => ({
  ...(await orig<typeof import('@/features/agentops/api')>()),
  agentOpsApi: {
    cleanup: vi.fn(),
    deleteRun: vi.fn(),
    stop: vi.fn(),
    resume: vi.fn(),
  },
}))
vi.mock('@/features/agentops/turn-interrupt', () => ({
  useTurnInterrupt: () => ({
    offered: false,
    allowed: false,
    pending: false,
    fenceUnavailable: false,
    interrupt: vi.fn(),
  }),
}))
vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}))

import { agentOpsApi } from '@/features/agentops/api'
import type { RunDTO } from '@/features/agentops/types'
import type { Capability } from './provenance'
import { RunActions } from './run-actions'

const caps: Capability[] = [
  { id: 'cleanup', available: true },
  { id: 'delete', available: true },
]
const stopped = {
  run_ref: 'run_1',
  state: 'stopped',
  transport: 'stream-json',
} as unknown as RunDTO
const withWorktree = { ...stopped, worktree_branch: 'olivares/ab12cd34' }

function show(run: RunDTO) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <RunActions run={run} caps={caps} onClose={vi.fn()} />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(agentOpsApi.cleanup).mockResolvedValue({} as never)
  vi.mocked(agentOpsApi.deleteRun).mockResolvedValue({ deleted: true })
})

async function openCleanup(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByRole('button', { name: 'Clean up' }))
  return within(await screen.findByRole('dialog'))
}

describe('RunActions — a session with a git worktree', () => {
  it('releases without the confirmation unless the person ticks it', async () => {
    const user = userEvent.setup()
    show(withWorktree as RunDTO)
    const dialog = await openCleanup(user)
    expect(
      dialog.getByRole('checkbox', {
        name: 'Also discard the worktree and branch olivares/ab12cd34',
      }),
    ).not.toBeChecked()
    await user.click(dialog.getByRole('button', { name: 'Clean up' }))
    await waitFor(() =>
      expect(agentOpsApi.cleanup).toHaveBeenCalledWith('run_1', false),
    )
  })

  it('sends the confirmation when the person ticks it', async () => {
    const user = userEvent.setup()
    show(withWorktree as RunDTO)
    const dialog = await openCleanup(user)
    await user.click(
      dialog.getByRole('checkbox', {
        name: 'Also discard the worktree and branch olivares/ab12cd34',
      }),
    )
    await user.click(dialog.getByRole('button', { name: 'Clean up' }))
    await waitFor(() =>
      expect(agentOpsApi.cleanup).toHaveBeenCalledWith('run_1', true),
    )
  })

  it('carries the same confirmation into Delete, which releases first', async () => {
    const user = userEvent.setup()
    show(withWorktree as RunDTO)
    await user.click(screen.getByRole('button', { name: 'Delete' }))
    const dialog = within(await screen.findByRole('dialog'))
    await user.click(
      dialog.getByRole('checkbox', {
        name: 'Also discard the worktree and branch olivares/ab12cd34',
      }),
    )
    await user.click(dialog.getByRole('button', { name: 'Delete' }))
    await waitFor(() =>
      expect(agentOpsApi.cleanup).toHaveBeenCalledWith('run_1', true),
    )
    await waitFor(() =>
      expect(agentOpsApi.deleteRun).toHaveBeenCalledWith('run_1'),
    )
  })

  it('offers no discard for a session that is already cleaned', async () => {
    const user = userEvent.setup()
    show({ ...withWorktree, state: 'cleaned' } as RunDTO)
    await user.click(screen.getByRole('button', { name: 'Delete' }))
    const dialog = within(await screen.findByRole('dialog'))
    expect(dialog.queryByRole('checkbox')).not.toBeInTheDocument()
    await user.click(dialog.getByRole('button', { name: 'Delete' }))
    await waitFor(() =>
      expect(agentOpsApi.deleteRun).toHaveBeenCalledWith('run_1'),
    )
    expect(agentOpsApi.cleanup).not.toHaveBeenCalled()
  })

  it('offers nothing about worktrees to a session that has none', async () => {
    const user = userEvent.setup()
    show(stopped)
    const dialog = await openCleanup(user)
    expect(dialog.queryByRole('checkbox')).not.toBeInTheDocument()
    await user.click(dialog.getByRole('button', { name: 'Clean up' }))
    await waitFor(() =>
      expect(agentOpsApi.cleanup).toHaveBeenCalledWith('run_1', false),
    )
  })
})
