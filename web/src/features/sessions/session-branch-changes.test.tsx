// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// What the session's worktree branch holds against where it began (K4.A2): the changed
// paths with their status, and one path's text at the base beside its text at the
// branch tip in the existing diff view. The real CodeDiff renders here.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor, within } from '@testing-library/react'
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
      changes: vi.fn(),
      worktreeDiff: vi.fn(),
      worktreeDiffFile: vi.fn(),
      reviewComment: vi.fn(),
    },
  }
})

import { agentOpsApi } from '@/features/agentops/api'
import { ApiError } from '@/lib/api/errors'
import { sessionTurnBody } from '@/features/agentops/session-turn'
import type {
  RunDTO,
  RunWorktreeDiffDTO,
  RunWorktreeDiffFileDTO,
} from '@/features/agentops/types'
import { usePreferencesStore } from '@/stores/preferences'
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

// What the folder's own read says each file was last written: a reviewed mark holds
// until the file is written again.
const written = (at: string) => ({
  folder: 'repo',
  files: [{ path: 'README', size: 30, modified_at: at }],
  truncated: false,
})

beforeEach(() => {
  vi.clearAllMocks()
  auth.canWrite = true
  usePreferencesStore.setState({ reviewed: [] })
  vi.mocked(agentOpsApi.changes).mockResolvedValue(
    written('2026-11-10T10:00:00Z'),
  )
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

  it('marks a reviewed file on its row, after a reload too, until the branch moves on', async () => {
    vi.mocked(agentOpsApi.worktreeDiff).mockResolvedValue(listing())
    vi.mocked(agentOpsApi.worktreeDiffFile).mockResolvedValue({
      path: 'README',
      status: 'modified',
      original: 'base\n',
      modified: 'base\nmore\n',
      binary: false,
      truncated: false,
    })
    const row = async (name: RegExp) => {
      const list = await screen.findByTestId('branch-changes-list')
      return within(list).findByRole('button', { name })
    }
    const user = userEvent.setup()
    let view = mount()

    await user.click(await row(/README/))
    await user.click(await screen.findByLabelText('Reviewed'))
    await user.click(screen.getByRole('button', { name: 'All branch changes' }))

    expect(
      await within(await row(/README/)).findByText('Reviewed'),
    ).toBeVisible()
    expect(within(await row(/feature/)).queryByText('Reviewed')).toBeNull()
    view.unmount()
    view = mount()
    expect(
      await within(await row(/README/)).findByText('Reviewed'),
    ).toBeVisible()
    view.unmount()
    // A commit made after the mark: the same row, at another head.
    vi.mocked(agentOpsApi.worktreeDiff).mockResolvedValue(
      listing({ head: 'a'.repeat(40) }),
    )
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    render(
      <QueryClientProvider client={client}>
        <SessionBranchChanges run={run} />
      </QueryClientProvider>,
    )
    await waitFor(() => expect(client.isFetching()).toBe(0))
    expect(within(await row(/README/)).queryByText('Reviewed')).toBeNull()
    // Its two commits fix what a branch shows: no write time is read for it.
    expect(agentOpsApi.changes).not.toHaveBeenCalled()
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

  it('reads a worktree session as its branch, as before', async () => {
    vi.mocked(agentOpsApi.worktreeDiff).mockResolvedValue(listing())
    mount()

    await screen.findByTestId('branch-changes-list')
    expect(
      screen.getByRole('heading', { name: 'Branch changes' }),
    ).toBeInTheDocument()
    expect(
      screen.getByText(
        "What this session's branch holds that the workspace's current commit does not. Committed work only.",
      ),
    ).toBeInTheDocument()
    expect(screen.getByTestId('branch-changes-range')).toHaveTextContent(
      `From ${BASE.slice(0, 12)} to ${HEAD.slice(0, 12)}`,
    )
    expect(screen.queryByText(/last commit|Uncommitted/)).toBeNull()
  })

  it("keeps the branch's own words when the engine refuses its branch with 409", async () => {
    vi.mocked(agentOpsApi.worktreeDiff).mockRejectedValue(
      new ApiError(409, 'conflict', "this session's branch b is gone"),
    )
    mount()

    expect(
      await screen.findByText(
        'The branch changes could not be read. Try again in a moment.',
      ),
    ).toBeInTheDocument()
    expect(screen.queryByText(/no commit yet/)).toBeNull()
  })
})

// A session that works in its Git folder directly, with no worktree of its own: the engine
// compares the folder as it is now with the folder's last commit, and the view says so.
describe('SessionBranchChanges for a session in its own Git folder', () => {
  const inFolder = { run_ref: 'run1', state: 'stopped' } as RunDTO
  const folderListing = (
    over: Partial<RunWorktreeDiffDTO> = {},
  ): RunWorktreeDiffDTO => ({
    branch: 'main',
    base: BASE,
    head: '',
    working_tree: true,
    files: [
      { path: 'README', status: 'modified', added: 1, removed: 1 },
      { path: 'sub/inner.txt', status: 'deleted', added: 0, removed: 1 },
      // A file Git does not track yet: added, and no line counts.
      { path: 'notes.txt', status: 'added' },
    ],
    truncated: false,
    ...over,
  })

  it('lists what changed since the last commit of its branch, with no explainer', async () => {
    vi.mocked(agentOpsApi.worktreeDiff).mockResolvedValue(folderListing())
    mount(inFolder)

    const list = await screen.findByTestId('branch-changes-list')
    expect(
      screen.getByRole('heading', { name: 'Uncommitted changes' }),
    ).toBeInTheDocument()
    const range = screen.getByTestId('branch-changes-range')
    expect(range).toHaveTextContent('Since the last commit on main.')
    // The branch in the sentence is a PathText: whole on hover, copied whole.
    const branch = range.querySelector('[data-slot="path-text"] code')
    expect(branch).toHaveAttribute('title', 'main')
    expect(screen.queryByText(/Committed work only/)).toBeNull()
    const readme = within(list).getByRole('button', { name: /README/ })
    expect(readme).toHaveTextContent('+1')
    expect(readme).toHaveTextContent('−1')
    expect(readme).toHaveTextContent('Modified')
    expect(
      within(list).getByRole('button', { name: /inner\.txt/ }),
    ).toHaveTextContent('Deleted')
    const added = within(list).getByRole('button', { name: /notes\.txt/ })
    expect(added).toHaveTextContent('Added')
    // A count the engine did not give, or a zero, is not written as «+0 −0».
    expect(added).not.toHaveTextContent(/[+−]\d/)
    expect(list).not.toHaveTextContent('+0')
    expect(list).not.toHaveTextContent('−0')
  })

  it('keeps a reviewed mark after a reload until the folder writes the file again', async () => {
    vi.mocked(agentOpsApi.worktreeDiff).mockResolvedValue(folderListing())
    vi.mocked(agentOpsApi.worktreeDiffFile).mockImplementation(
      async (_run, path) => ({
        path,
        status: 'modified',
        original: 'as committed\n',
        modified: 'as the agent left it\n',
        binary: false,
        truncated: false,
      }),
    )
    const row = async (name: RegExp) => {
      const list = await screen.findByTestId('branch-changes-list')
      return within(list).findByRole('button', { name })
    }
    const user = userEvent.setup()
    const mark = async (name: RegExp) => {
      await user.click(await row(name))
      await user.click(await screen.findByLabelText('Reviewed'))
      await user.click(screen.getByRole('button', { name: 'All changes' }))
    }
    let view = mount(inFolder)
    await mark(/README/)
    // The folder's read gives notes.txt no write time: its row cannot tell it is
    // unchanged and shows no mark, while its diff keeps the mark for the same text.
    await mark(/notes\.txt/)

    expect(
      await within(await row(/README/)).findByText('Reviewed'),
    ).toBeVisible()
    expect(within(await row(/notes\.txt/)).queryByText('Reviewed')).toBeNull()
    view.unmount()
    view = mount(inFolder)
    expect(
      await within(await row(/README/)).findByText('Reviewed'),
    ).toBeVisible()
    await user.click(await row(/notes\.txt/))
    expect(await screen.findByLabelText('Reviewed')).toBeChecked()
    view.unmount()
    vi.mocked(agentOpsApi.changes).mockResolvedValue(
      written('2026-11-10T10:05:00Z'),
    )
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    render(
      <QueryClientProvider client={client}>
        <SessionBranchChanges run={inFolder} />
      </QueryClientProvider>,
    )
    await waitFor(() => expect(client.isFetching()).toBe(0))
    expect(within(await row(/README/)).queryByText('Reviewed')).toBeNull()
  })

  it('names a detached last commit by its short id', async () => {
    vi.mocked(agentOpsApi.worktreeDiff).mockResolvedValue(
      folderListing({ branch: '' }),
    )
    mount(inFolder)

    const range = await screen.findByTestId('branch-changes-range')
    expect(range).toHaveTextContent(
      `Since the last commit (${BASE.slice(0, 12)}).`,
    )
  })

  it('shows a file at the last commit beside the folder, with the same diff view', async () => {
    vi.mocked(agentOpsApi.worktreeDiff).mockResolvedValue(folderListing())
    vi.mocked(agentOpsApi.worktreeDiffFile).mockResolvedValue({
      path: 'README',
      status: 'modified',
      original: 'as committed\n',
      modified: 'as the agent left it\n',
      binary: false,
      truncated: false,
    })
    const user = userEvent.setup()
    mount(inFolder)

    await user.click(await screen.findByRole('button', { name: /README/ }))

    const left = await screen.findByRole('textbox', {
      name: 'At the last commit',
    })
    const right = screen.getByRole('textbox', { name: 'In the folder' })
    expect(left.textContent).toContain('as committed')
    expect(right.textContent).toContain('as the agent left it')
    const version = screen.getByLabelText('Version')
    expect(within(version).getByText('At the last commit')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'All changes' }))
    expect(await screen.findByTestId('branch-changes-list')).toBeInTheDocument()
  })

  it('says nothing changed since the last commit, not an error', async () => {
    vi.mocked(agentOpsApi.worktreeDiff).mockResolvedValue(
      folderListing({ files: [] }),
    )
    mount(inFolder)

    expect(
      await screen.findByText('No changes since the last commit.'),
    ).toBeInTheDocument()
    expect(screen.queryByText(/could not be read/)).toBeNull()
  })

  it('says plainly that a folder with no commit yet has nothing to compare with', async () => {
    vi.mocked(agentOpsApi.worktreeDiff).mockRejectedValue(
      new ApiError(
        409,
        'no_commit',
        'this folder has no commit yet to compare with',
      ),
    )
    mount(inFolder)

    const said = await screen.findByText(
      'This folder has no commit yet, so there is nothing to compare with.',
    )
    expect(said).not.toHaveClass('text-danger')
    expect(screen.queryByText(/Try again/)).toBeNull()
    expect(screen.queryByRole('button')).toBeNull()
  })

  it('keeps asking a folder with no commit yet while the session works', async () => {
    vi.useFakeTimers()
    try {
      vi.mocked(agentOpsApi.worktreeDiff).mockRejectedValue(
        new ApiError(
          409,
          'no_commit',
          'this folder has no commit yet to compare with',
        ),
      )
      mount({ ...inFolder, state: 'running' } as RunDTO)
      // Past two polls of a working session: the agent may commit in its own shell.
      await act(async () => {
        await vi.advanceTimersByTimeAsync(12_000)
      })
      expect(
        vi.mocked(agentOpsApi.worktreeDiff).mock.calls.length,
      ).toBeGreaterThan(1)
      expect(
        screen.getByText(
          'This folder has no commit yet, so there is nothing to compare with.',
        ),
      ).toBeInTheDocument()
    } finally {
      vi.useRealTimers()
    }
  })

  // Today's engine refuses with no reason of its own: `module_error`, its code for
  // a refusal it names no further.
  it('says the general words for a 409 that names no reason, and keeps asking', async () => {
    vi.useFakeTimers()
    try {
      vi.mocked(agentOpsApi.worktreeDiff).mockRejectedValue(
        new ApiError(409, 'module_error', 'cannot compare'),
      )
      mount({ ...inFolder, state: 'running' } as RunDTO)
      await act(async () => {
        await vi.advanceTimersByTimeAsync(12_000)
      })
      expect(
        vi.mocked(agentOpsApi.worktreeDiff).mock.calls.length,
      ).toBeGreaterThan(1)
      expect(
        screen.getByText(
          'This folder cannot be compared with a commit right now.',
        ),
      ).not.toHaveClass('text-danger')
    } finally {
      vi.useRealTimers()
    }
  })

  it('says why a folder cannot be compared, for each reason the engine names', async () => {
    const reasons: [string, string][] = [
      [
        'head_unreadable',
        "Git could not name this folder's last commit, so there is nothing to compare with.",
      ],
      [
        'diff_unreadable',
        "Git could not count this folder's changes against its last commit.",
      ],
      [
        'folder_mismatch',
        "This session's folder no longer matches its workspace, so it cannot be compared.",
      ],
      // Without a reason the engine names, the words are general.
      [
        'module_error',
        'This folder cannot be compared with a commit right now.',
      ],
    ]
    for (const [code, said] of reasons) {
      vi.mocked(agentOpsApi.worktreeDiff).mockRejectedValue(
        new ApiError(409, code, 'cannot compare'),
      )
      const view = mount(inFolder)
      expect(await screen.findByText(said)).not.toHaveClass('text-danger')
      expect(screen.queryByText(/no commit yet/)).toBeNull()
      view.unmount()
    }
  })

  it('says when the list or a file was cut, without a size the folder may not have', async () => {
    vi.mocked(agentOpsApi.worktreeDiff).mockResolvedValue(
      folderListing({ truncated: true }),
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
    mount(inFolder)

    expect(
      await screen.findByText('Showing the first 3 changed files.'),
    ).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /README/ }))
    expect(
      await screen.findByText('Showing only the beginning of each side.'),
    ).toBeInTheDocument()
    expect(screen.queryByText(/64 KB/)).toBeNull()
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
