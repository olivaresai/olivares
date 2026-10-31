// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// What the session's worktree branch holds against where it began (K4.A2): the changed
// paths with their status, and one path's text at the base beside its text at the
// branch tip in the existing diff view. The real CodeDiff renders here.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const auth = vi.hoisted(() => ({ canWrite: true }))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => auth.canWrite }),
}))
vi.mock('@/features/agentops/api', async (orig) => {
  const real = (await orig()) as Record<string, unknown>
  return {
    ...real,
    agentOpsApi: {
      worktreeDiff: vi.fn(),
      worktreeDiffFile: vi.fn(),
      reviewComment: vi.fn(),
    },
  }
})

import { agentOpsApi } from '@/features/agentops/api'
import { sessionTurnBody } from '@/features/agentops/session-turn'
import type {
  RunDTO,
  RunWorktreeDiffDTO,
  RunWorktreeDiffFileDTO,
} from '@/features/agentops/types'
import { reviewCommentText } from './review-comment'
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

function mount(shown: RunDTO = run) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <SessionBranchChanges run={shown} />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  auth.canWrite = true
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

    expect(
      screen.getByRole('button', { name: 'Copy README' }),
    ).toBeInTheDocument()
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

// A comment on a line goes back to the agent as its next message, through the run's
// input route, with the place it was made so the ledger can record it.
describe('SessionBranchChanges line comments', () => {
  const liveRun = { ...run, state: 'running' } as RunDTO
  const readme = {
    path: 'README',
    status: 'modified',
    original: 'base\n',
    modified: 'base\nmore handed-over work\n',
    binary: false,
    truncated: false,
  } satisfies RunWorktreeDiffFileDTO

  async function openReadme(shown: RunDTO) {
    vi.mocked(agentOpsApi.worktreeDiff).mockResolvedValue(listing())
    vi.mocked(agentOpsApi.worktreeDiffFile).mockResolvedValue(readme)
    const user = userEvent.setup()
    mount(shown)
    await user.click(await screen.findByRole('button', { name: /README/ }))
    await screen.findByRole('textbox', { name: 'At the branch tip' })
    return user
  }

  it('sends a comment on a line of the branch tip to the session with its place', async () => {
    vi.mocked(agentOpsApi.reviewComment).mockResolvedValue({ accepted: true })
    const user = await openReadme(liveRun)

    await user.type(screen.getByLabelText('Line'), '2')
    await user.type(screen.getByLabelText('Comment'), 'Why this?')
    await user.click(
      screen.getByRole('button', { name: 'Send to the session' }),
    )

    await waitFor(() =>
      expect(agentOpsApi.reviewComment).toHaveBeenCalledWith(
        'run1',
        sessionTurnBody(
          liveRun,
          reviewCommentText(
            { path: 'README', line: 2, side: 'modified' },
            'more handed-over work',
            'Why this?',
          ),
        ),
        { path: 'README', line: 2, side: 'modified' },
        undefined,
      ),
    )
    await waitFor(() =>
      expect(screen.getByLabelText('Comment')).toHaveValue(''),
    )
  })

  it('quotes a line of the base when that version is chosen', async () => {
    vi.mocked(agentOpsApi.reviewComment).mockResolvedValue({ accepted: true })
    const user = await openReadme(liveRun)

    await user.selectOptions(screen.getByLabelText('Version'), 'original')
    await user.type(screen.getByLabelText('Line'), '1')
    await user.type(screen.getByLabelText('Comment'), 'Keep this.')
    await user.click(
      screen.getByRole('button', { name: 'Send to the session' }),
    )

    await waitFor(() =>
      expect(agentOpsApi.reviewComment).toHaveBeenCalledWith(
        'run1',
        sessionTurnBody(
          liveRun,
          reviewCommentText(
            { path: 'README', line: 1, side: 'original' },
            'base',
            'Keep this.',
          ),
        ),
        { path: 'README', line: 1, side: 'original' },
        undefined,
      ),
    )
  })

  it('says why Send waits and sends nothing for a line the file does not have', async () => {
    const user = await openReadme(liveRun)
    const send = screen.getByRole('button', { name: 'Send to the session' })
    expect(send).toBeDisabled()
    expect(
      screen.getByText('Choose a line: click its number or type it.'),
    ).toBeInTheDocument()

    await user.type(screen.getByLabelText('Line'), '9')
    await user.type(screen.getByLabelText('Comment'), 'Is this here?')

    expect(
      screen.getByText('The file has no line 9 at the branch tip.'),
    ).toBeInTheDocument()
    expect(send).toBeDisabled()
    expect(agentOpsApi.reviewComment).not.toHaveBeenCalled()
  })

  it('does not offer to send for a person who may not write to sessions', async () => {
    auth.canWrite = false
    const user = await openReadme(liveRun)
    await user.type(screen.getByLabelText('Line'), '2')
    await user.type(screen.getByLabelText('Comment'), 'May I?')

    expect(
      screen.getByText(
        'You need permission to write to sessions to send a comment.',
      ),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: 'Send to the session' }),
    ).toBeDisabled()
  })

  it('does not offer to send to a session that is not running', async () => {
    const user = await openReadme(run)
    await user.type(screen.getByLabelText('Line'), '2')
    await user.type(screen.getByLabelText('Comment'), 'Too late?')

    expect(
      screen.getByText('Resume the session to send a comment.'),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: 'Send to the session' }),
    ).toBeDisabled()
  })

  it('keeps the typed comment when the engine refuses it', async () => {
    vi.mocked(agentOpsApi.reviewComment).mockRejectedValue(new Error('409'))
    const user = await openReadme(liveRun)

    await user.type(screen.getByLabelText('Line'), '2')
    await user.type(screen.getByLabelText('Comment'), 'Do not lose me')
    await user.click(
      screen.getByRole('button', { name: 'Send to the session' }),
    )

    await waitFor(() => expect(agentOpsApi.reviewComment).toHaveBeenCalled())
    expect(screen.getByLabelText('Comment')).toHaveValue('Do not lose me')
  })
})
