// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// What the session's worktree branch holds against where it began (K4.A2): the changed
// paths with their status, and one path's text at the base beside its text at the
// branch tip in the existing diff view. The real CodeDiff renders here.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1' }),
}))
vi.mock('@/features/agentops/api', async (orig) => {
  const real = (await orig()) as Record<string, unknown>
  return {
    ...real,
    agentOpsApi: { worktreeDiff: vi.fn(), worktreeDiffFile: vi.fn() },
  }
})

import { agentOpsApi } from '@/features/agentops/api'
import type {
  RunDTO,
  RunWorktreeDiffDTO,
  RunWorktreeDiffFileDTO,
} from '@/features/agentops/types'
import { SessionBranchChanges } from './session-branch-changes'

const BASE = '0123456789abcdef0123456789abcdef01234567'
const HEAD = 'fedcba9876543210fedcba9876543210fedcba98'
const run = {
  run_ref: 'run1',
  state: 'stopped',
  worktree_branch: 'olivares/ab12cd34',
} as RunDTO

const listing = (
  over: Partial<RunWorktreeDiffDTO> = {},
): RunWorktreeDiffDTO => ({
  branch: 'olivares/ab12cd34',
  base: BASE,
  head: HEAD,
  files: [
    { path: 'feature.txt', status: 'added' },
    { path: 'README', status: 'modified' },
    { path: 'blob.bin', status: 'added' },
  ],
  truncated: false,
  ...over,
})

function mount() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <SessionBranchChanges run={run} />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
})

describe('SessionBranchChanges', () => {
  it('lists the changed paths with their status and the range they span', async () => {
    vi.mocked(agentOpsApi.worktreeDiff).mockResolvedValue(listing())
    mount()

    const list = await screen.findByTestId('branch-changes-list')
    expect(within(list).getByText('feature.txt')).toBeInTheDocument()
    expect(within(list).getAllByText('Added')).toHaveLength(2)
    expect(within(list).getByText('Modified')).toBeInTheDocument()
    expect(screen.getByTestId('branch-changes-range')).toHaveTextContent(
      `From ${BASE.slice(0, 12)} to ${HEAD.slice(0, 12)}`,
    )
    expect(agentOpsApi.worktreeDiff).toHaveBeenCalledWith('run1', {
      signal: expect.any(AbortSignal),
    })
  })

  it('shows a path at the base beside the branch tip in the diff view', async () => {
    vi.mocked(agentOpsApi.worktreeDiff).mockResolvedValue(listing())
    vi.mocked(agentOpsApi.worktreeDiffFile).mockResolvedValue({
      path: 'README',
      status: 'modified',
      original: 'base\n',
      modified: 'base\nmore handed-over work\n',
      binary: false,
      truncated: false,
    } satisfies RunWorktreeDiffFileDTO)
    const user = userEvent.setup()
    mount()

    await user.click(await screen.findByRole('button', { name: /README/ }))

    const left = await screen.findByRole('textbox', { name: 'At the base' })
    const right = screen.getByRole('textbox', { name: 'At the branch tip' })
    expect(left.textContent).toContain('base')
    expect(left.textContent).not.toContain('handed-over')
    expect(right.textContent).toContain('more handed-over work')
    expect(agentOpsApi.worktreeDiffFile).toHaveBeenCalledWith(
      'run1',
      'README',
      { signal: expect.any(AbortSignal) },
    )
    await user.click(screen.getByRole('button', { name: 'All branch changes' }))
    expect(await screen.findByTestId('branch-changes-list')).toBeInTheDocument()
  })

  it('shows an added file against an empty base', async () => {
    vi.mocked(agentOpsApi.worktreeDiff).mockResolvedValue(listing())
    vi.mocked(agentOpsApi.worktreeDiffFile).mockResolvedValue({
      path: 'feature.txt',
      status: 'added',
      original: '',
      modified: 'handed over\n',
      binary: false,
      truncated: false,
    })
    const user = userEvent.setup()
    mount()

    await user.click(await screen.findByRole('button', { name: /feature.txt/ }))

    expect(
      (await screen.findByRole('textbox', { name: 'At the base' })).textContent,
    ).toBe('')
    expect(
      screen.getByRole('textbox', { name: 'At the branch tip' }).textContent,
    ).toContain('handed over')
  })

  it('says a binary file is not shown instead of diffing it', async () => {
    vi.mocked(agentOpsApi.worktreeDiff).mockResolvedValue(listing())
    vi.mocked(agentOpsApi.worktreeDiffFile).mockResolvedValue({
      path: 'blob.bin',
      status: 'added',
      original: '',
      modified: '',
      binary: true,
      truncated: false,
    })
    const user = userEvent.setup()
    mount()

    await user.click(await screen.findByRole('button', { name: /blob.bin/ }))

    expect(
      await screen.findByText('Binary file, not shown.'),
    ).toBeInTheDocument()
    expect(screen.queryByRole('textbox')).not.toBeInTheDocument()
  })

  it('says when the listing or a text was cut', async () => {
    vi.mocked(agentOpsApi.worktreeDiff).mockResolvedValue(
      listing({ truncated: true }),
    )
    vi.mocked(agentOpsApi.worktreeDiffFile).mockResolvedValue({
      path: 'README',
      status: 'modified',
      original: 'a\n',
      modified: 'b\n',
      binary: false,
      truncated: true,
    })
    const user = userEvent.setup()
    mount()

    expect(
      await screen.findByText('Showing the first 3 changed files.'),
    ).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /README/ }))
    expect(
      await screen.findByText('Showing the first 64 KB of each side.'),
    ).toBeInTheDocument()
  })

  it('says a branch with nothing the workspace lacks has nothing, not an error', async () => {
    vi.mocked(agentOpsApi.worktreeDiff).mockResolvedValue(
      listing({ files: [] }),
    )
    mount()

    expect(
      await screen.findByText(
        "This branch holds nothing the workspace's current commit does not.",
      ),
    ).toBeInTheDocument()
    expect(screen.queryByText(/could not be read/)).not.toBeInTheDocument()
  })

  it('says when the branch cannot be read', async () => {
    vi.mocked(agentOpsApi.worktreeDiff).mockRejectedValue(new Error('409'))
    mount()

    expect(
      await screen.findByText(
        'The branch changes could not be read. Try again in a moment.',
      ),
    ).toBeInTheDocument()
  })
})
