// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The Session changes pane against git HEAD: a changed file opens as its
// current text, with a Changes view of what differs from HEAD when HEAD has the file.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import {
  File,
  FileBraces,
  FileCode,
  FileCog,
  FileImage,
  type LucideIcon,
} from 'lucide-react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))
vi.mock('@/features/agentops/api', async (orig) => ({
  ...(await orig<typeof import('@/features/agentops/api')>()),
  agentOpsApi: {
    changes: vi.fn(),
    changedFile: vi.fn(),
    runCheckpoints: vi.fn(),
    reviewComment: vi.fn(),
    gitStatus: vi.fn(),
    worktreeDiff: vi.fn(),
    worktreeDiffFile: vi.fn(),
  },
}))
vi.mock('@/components/ui/code-diff', () => ({
  CodeDiff: ({
    original,
    modified,
  }: {
    original: string
    modified: string
  }) => (
    <div data-testid="code-diff">
      <span data-testid="diff-original">{original}</span>
      <span data-testid="diff-modified">{modified}</span>
    </div>
  ),
}))

import { ApiError } from '@/lib/api/errors'
import { agentOpsApi } from '@/features/agentops/api'
import type { RunDTO } from '@/features/agentops/types'
import { SessionChanges } from './session-changes'

const run = { run_ref: 'run_1', state: 'stopped' } as unknown as RunDTO

function file(path: string, text: string) {
  return { path, size: text.length, text, binary: false, truncated: false }
}

function show(shown: RunDTO = run) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <SessionChanges run={shown} />
    </QueryClientProvider>,
  )
  return qc
}

// Every request has been answered and rendered: what is absent now is absent for good.
const settled = (qc: QueryClient) =>
  waitFor(() => expect(qc.isFetching()).toBe(0))

function notFound() {
  return new ApiError(404, 'not_found', 'the file is not in HEAD')
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(agentOpsApi.runCheckpoints).mockResolvedValue({
    available: false,
    turns: [],
    files: [],
    truncated: false,
  })
  vi.mocked(agentOpsApi.changes).mockResolvedValue({
    folder: 'repo',
    files: [
      { path: 'README.md', size: 30, modified_at: '2026-10-06T10:00:00Z' },
      { path: 'new.txt', size: 9, modified_at: '2026-10-06T10:01:00Z' },
    ],
    truncated: false,
  })
})

describe('SessionChanges — against git HEAD', () => {
  it('asks the engine for HEAD of the opened file and offers its diff', async () => {
    vi.mocked(agentOpsApi.changedFile).mockImplementation(
      async (_run, path, opts) =>
        opts?.rev === 'HEAD'
          ? file(path, '# Title\n')
          : file(path, '# Title\nchanged by the agent\n'),
    )
    const user = userEvent.setup()
    show()
    await user.click(await screen.findByText('README.md'))
    expect(
      screen.getByRole('button', { name: 'Copy README.md' }),
    ).toBeInTheDocument()
    await screen.findByTestId('diff-original')
    expect(screen.getByTestId('diff-original')).toHaveTextContent(/^# Title$/)
    expect(screen.getByTestId('diff-modified')).toHaveTextContent(
      'changed by the agent',
    )
    expect(agentOpsApi.changedFile).toHaveBeenCalledWith(
      'run_1',
      'README.md',
      expect.objectContaining({ rev: 'HEAD' }),
    )
  })

  it('shows a file HEAD does not have as plain text, with no switch and no error', async () => {
    vi.mocked(agentOpsApi.changedFile).mockImplementation(
      async (_run, path, opts) => {
        if (opts?.rev === 'HEAD') throw notFound()
        return file(path, 'created by the agent\n')
      },
    )
    const user = userEvent.setup()
    const qc = show()
    await user.click(await screen.findByText('new.txt'))
    expect(await screen.findByTestId('changes-file')).toHaveTextContent(
      'created by the agent',
    )
    await waitFor(() =>
      expect(agentOpsApi.changedFile).toHaveBeenCalledWith(
        'run_1',
        'new.txt',
        expect.objectContaining({ rev: 'HEAD' }),
      ),
    )
    await settled(qc)
    expect(
      screen.queryByRole('button', { name: 'Changes since HEAD' }),
    ).toBeNull()
    expect(screen.queryByText(/could not|error/i)).toBeNull()
  })

  it('says the comparison could not be made when HEAD fails for another reason', async () => {
    vi.mocked(agentOpsApi.changedFile).mockImplementation(
      async (_run, path, opts) => {
        if (opts?.rev === 'HEAD')
          throw new ApiError(500, 'internal', 'filesystem error')
        return file(path, 'current\n')
      },
    )
    const user = userEvent.setup()
    const qc = show()
    await user.click(await screen.findByText('README.md'))
    await settled(qc)
    expect(
      await screen.findByText(
        'The changes could not be read. Try again in a moment.',
      ),
    ).toBeInTheDocument()
    expect(screen.getByTestId('changes-file')).toHaveTextContent('current')
    expect(
      screen.queryByRole('button', { name: 'Changes since HEAD' }),
    ).toBeNull()
  })

  it('does not ask HEAD about a binary or cut-off file, which cannot be compared', async () => {
    vi.mocked(agentOpsApi.changedFile).mockImplementation(
      async (_run, path, opts) =>
        opts?.rev === 'HEAD'
          ? file(path, 'unexpected')
          : {
              ...file(path, 'cut text'),
              truncated: path === 'README.md',
              binary: path === 'new.txt',
            },
    )
    const user = userEvent.setup()
    const qc = show()
    await user.click(await screen.findByText('README.md'))
    await screen.findByTestId('changes-file')
    await settled(qc)
    await user.click(screen.getByRole('button', { name: 'All changes' }))
    await user.click(await screen.findByText('new.txt'))
    await screen.findByText(/binary/i)
    await settled(qc)
    expect(agentOpsApi.changedFile).not.toHaveBeenCalledWith(
      expect.anything(),
      expect.anything(),
      expect.objectContaining({ rev: 'HEAD' }),
    )
  })

  it('opens each file directly on its diff', async () => {
    vi.mocked(agentOpsApi.changedFile).mockImplementation(
      async (_run, path, opts) =>
        file(path, opts?.rev === 'HEAD' ? 'before\n' : 'after\n'),
    )
    const user = userEvent.setup()
    show()
    const opener = await screen.findByRole('button', { name: /README.md/ })
    await user.click(opener)
    expect(await screen.findByTestId('code-diff')).toBeInTheDocument()
    await user.keyboard('{Escape}')
    await waitFor(() => expect(opener).toHaveFocus())
    await user.click(await screen.findByText('new.txt'))
    expect(await screen.findByTestId('code-diff')).toBeInTheDocument()
  })

  it('offers no diff against a truncated side, which would compare cut texts', async () => {
    vi.mocked(agentOpsApi.changedFile).mockImplementation(
      async (_run, path, opts) => ({
        ...file(path, opts?.rev === 'HEAD' ? 'head part' : 'current part'),
        truncated: opts?.rev === 'HEAD',
      }),
    )
    const user = userEvent.setup()
    const qc = show()
    await user.click(await screen.findByText('README.md'))
    await screen.findByTestId('changes-file')
    await waitFor(() =>
      expect(agentOpsApi.changedFile).toHaveBeenCalledTimes(2),
    )
    await settled(qc)
    expect(
      screen.queryByRole('button', { name: 'Changes since HEAD' }),
    ).toBeNull()
  })
})

describe('SessionChanges — file kinds', () => {
  it("marks each changed file with its kind's icon, by its extension", async () => {
    vi.mocked(agentOpsApi.changes).mockResolvedValue({
      folder: 'repo',
      files: [
        'main.go',
        'package.json',
        'logo.png',
        'LICENSE',
        '.env',
        '.gitignore',
      ].map((path) => ({
        path,
        size: 1,
        modified_at: '2026-10-06T10:00:00Z',
      })),
      truncated: false,
    })
    show()
    // The shapes lucide draws for an icon, to tell one glyph from another.
    const shapes = (Icon: LucideIcon) =>
      render(<Icon />).container.querySelector('svg')!.innerHTML
    const drawn = async (path: string) =>
      (await screen.findByText(path)).closest('button')!.querySelector('svg')!
        .innerHTML
    expect(await drawn('main.go')).toBe(shapes(FileCode))
    expect(await drawn('package.json')).toBe(shapes(FileBraces))
    expect(await drawn('logo.png')).toBe(shapes(FileImage))
    // A name the table does not know keeps the plain file glyph.
    expect(await drawn('LICENSE')).toBe(shapes(File))
    // A dotfile is named by its extension alone.
    expect(await drawn('.env')).toBe(shapes(FileCog))
    expect(await drawn('.gitignore')).toBe(shapes(FileCog))
  })
})

it('opens a file diff and scopes two edits to the latest or a chosen turn', async () => {
  vi.mocked(agentOpsApi.runCheckpoints).mockResolvedValue({
    available: true,
    turns: [1, 2].map((seq) => ({
      seq,
      prompt: `Edit ${seq}`,
      clipped: false,
      at: '2026-11-07T10:00:00Z',
      open: seq === 2,
      truncated: false,
      files: [{ path: 'README.md', status: 'modified' as const }],
    })),
    files: [{ path: 'README.md', status: 'modified' }],
    truncated: false,
  })
  vi.mocked(agentOpsApi.changedFile).mockImplementation(
    async (_run, path, opts) => {
      const query = opts as { turn?: number; side?: string; rev?: string }
      return file(
        path,
        query.turn === 1 && query.side === 'start'
          ? 'initial\n'
          : (query.turn === 1 && query.side === 'end') ||
              (query.turn === 2 && query.side === 'start')
            ? 'first edit\n'
            : 'second edit\n',
      )
    },
  )
  const user = userEvent.setup()
  show()
  await user.click(
    await within(await screen.findByTestId('changes-list')).findByText(
      'README.md',
    ),
  )
  expect(await screen.findByTestId('diff-original')).toHaveTextContent(
    'initial',
  )
  expect(screen.getByTestId('diff-modified')).toHaveTextContent('second edit')
  await user.selectOptions(
    screen.getByRole('combobox', { name: 'Changes scope' }),
    'latest',
  )
  await user.click(
    await within(await screen.findByTestId('changes-list')).findByText(
      'README.md',
    ),
  )
  expect(await screen.findByTestId('diff-original')).toHaveTextContent(
    'first edit',
  )
  expect(screen.getByTestId('diff-modified')).toHaveTextContent('second edit')
  await user.selectOptions(
    screen.getByRole('combobox', { name: 'Changes scope' }),
    '1',
  )
  await user.click(
    await within(await screen.findByTestId('changes-list')).findByText(
      'README.md',
    ),
  )
  expect(await screen.findByTestId('diff-original')).toHaveTextContent(
    'initial',
  )
  expect(screen.getByTestId('diff-modified')).toHaveTextContent('first edit')
})

it('reviews turn 2 in a wide dialog and sends its line and version to the session', async () => {
  const turn = (seq: number, path: string) => ({
    seq,
    prompt: `Turn ${seq}`,
    clipped: false,
    at: '2026-11-09T12:00:00Z',
    open: false,
    files: [{ path, status: 'modified' as const }],
    truncated: false,
  })
  vi.mocked(agentOpsApi.runCheckpoints).mockResolvedValue({
    available: true,
    turns: [turn(1, 'one.txt'), turn(2, 'two.txt')],
    files: [turn(1, 'one.txt'), turn(2, 'two.txt')].flatMap((t) => t.files),
    truncated: false,
  })
  vi.mocked(agentOpsApi.changedFile).mockImplementation(
    async (_run, path, opts) =>
      file(path, opts?.side === 'start' ? 'before\n' : 'after\n'),
  )
  vi.mocked(agentOpsApi.reviewComment).mockResolvedValue({ accepted: true })
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <SessionChanges run={{ ...run, state: 'running' }} />
    </QueryClientProvider>,
  )
  const user = userEvent.setup()
  await screen.findByRole('option', { name: 'Turn 2' })
  await user.selectOptions(
    screen.getByRole('combobox', { name: 'Changes scope' }),
    '2',
  )
  expect(screen.queryByText('one.txt')).toBeNull()
  await user.click(await screen.findByText('two.txt'))
  expect(
    await screen.findByRole('dialog', { name: 'two.txt' }),
  ).toBeInTheDocument()
  await screen.findByTestId('code-diff')
  expect(agentOpsApi.changedFile).toHaveBeenCalledWith(
    'run_1',
    'two.txt',
    expect.objectContaining({ turn: 2, side: 'start' }),
  )
  expect(agentOpsApi.changedFile).toHaveBeenCalledWith(
    'run_1',
    'two.txt',
    expect.objectContaining({ turn: 2, side: 'end' }),
  )
  await user.click(screen.getByLabelText('Reviewed'))
  await user.click(screen.getByRole('button', { name: 'Maximize' }))
  expect(screen.getByRole('button', { name: 'Restore size' })).toBeVisible()
  await user.type(screen.getByLabelText('Line'), '1')
  await user.type(
    screen.getByLabelText('Comment'),
    'Please explain this change',
  )
  await user.click(screen.getByRole('button', { name: 'Send to the session' }))
  await waitFor(() =>
    expect(agentOpsApi.reviewComment).toHaveBeenCalledWith(
      'run_1',
      expect.any(Object),
      { path: 'two.txt', line: 1, side: 'modified' },
      undefined,
    ),
  )
  expect(
    JSON.stringify(vi.mocked(agentOpsApi.reviewComment).mock.calls.at(-1)?.[1]),
  ).toContain('two.txt, line 1')
  await user.click(screen.getByRole('button', { name: 'All changes' }))
  await user.click(await screen.findByText('two.txt'))
  await screen.findByTestId('code-diff')
  expect(screen.getByLabelText('Reviewed')).toBeChecked()
  await user.click(screen.getByRole('button', { name: 'All changes' }))
  vi.mocked(agentOpsApi.gitStatus).mockResolvedValue({
    branch: 'main',
    branches: ['main'],
    writable: true,
    truncated: false,
    files: [
      { path: 'deleted.txt', index: ' ', worktree: 'D' },
      { path: 'untracked.txt', index: '?', worktree: '?' },
    ],
  })
  vi.mocked(agentOpsApi.changedFile).mockImplementation(
    async (_run, path, opts) => {
      if (
        (path === 'deleted.txt' && !opts?.rev) ||
        (path === 'untracked.txt' && opts?.rev === 'HEAD')
      )
        throw notFound()
      return file(path, opts?.rev === 'HEAD' ? 'removed text\n' : 'new text\n')
    },
  )
  await user.selectOptions(
    screen.getByRole('combobox', { name: 'Changes scope' }),
    'working',
  )
  await user.click(await screen.findByText('deleted.txt'))
  expect(await screen.findByTestId('diff-original')).toHaveTextContent(
    'removed text',
  )
  expect(screen.getByTestId('diff-modified')).toBeEmptyDOMElement()
  await user.click(screen.getByRole('button', { name: 'All changes' }))
  await user.click(await screen.findByText('untracked.txt'))
  expect(await screen.findByTestId('diff-original')).toBeEmptyDOMElement()
  expect(screen.getByTestId('diff-modified')).toHaveTextContent('new text')
})

it('keeps a file marked reviewed after a reload until its content changes', async () => {
  let text = '# Title\nchanged\n'
  vi.mocked(agentOpsApi.changedFile).mockImplementation(
    async (_run, path, opts) =>
      file(path, opts?.rev === 'HEAD' ? '# Title\n' : text),
  )
  const user = userEvent.setup()
  const open = async () => {
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    const view = render(
      <QueryClientProvider client={qc}>
        <SessionChanges run={run} />
      </QueryClientProvider>,
    )
    await user.click(await screen.findByText('README.md'))
    await screen.findByTestId('code-diff')
    return view
  }
  let view = await open()
  await user.click(screen.getByLabelText('Reviewed'))
  view.unmount()
  view = await open()
  expect(screen.getByLabelText('Reviewed')).toBeChecked()
  view.unmount()
  text = '# Title\nchanged again\n'
  await open()
  expect(screen.getByLabelText('Reviewed')).not.toBeChecked()
})

it('shows the lines added and removed beside each changed file', async () => {
  vi.mocked(agentOpsApi.runCheckpoints).mockResolvedValue({
    available: true,
    files: [
      { path: 'README.md', status: 'modified', added: 1, removed: 1 },
      { path: 'new.txt', status: 'added', added: 1 },
    ],
    truncated: false,
    turns: [
      {
        seq: 1,
        prompt: 'Turn 1',
        clipped: false,
        at: '2026-11-09T12:00:00Z',
        open: true,
        files: [
          { path: 'README.md', status: 'modified', added: 1, removed: 1 },
          { path: 'new.txt', status: 'added', added: 1 },
        ],
        truncated: false,
      },
    ],
  })
  show()
  const list = await screen.findByTestId('changes-list')
  const readme = within(list).getByText('README.md').closest('button')!
  expect(readme).toHaveTextContent('+1')
  expect(readme).toHaveTextContent('−1')
  const added = within(list).getByText('new.txt').closest('button')!
  expect(added).toHaveTextContent('+1')
  expect(added).not.toHaveTextContent('−')
})

// A session that works in its Git folder directly offers its uncommitted changes against
// the folder's last commit, in the same view as a worktree branch; a folder that is not a
// Git repository (404) keeps the scopes it has today.
describe('SessionChanges — a session in its own Git folder', () => {
  const inFolder = { ...run, workspace_path: '/srv/repo' } as RunDTO
  const scopes = () =>
    screen.getAllByRole('option').map((option) => option.textContent)

  it('offers its uncommitted changes against the last commit', async () => {
    vi.mocked(agentOpsApi.worktreeDiff).mockResolvedValue({
      branch: 'main',
      base: '0123456789abcdef0123456789abcdef01234567',
      head: '',
      working_tree: true,
      files: [{ path: 'notes.txt', status: 'added' }],
      truncated: false,
    })
    const user = userEvent.setup()
    show(inFolder)

    await user.selectOptions(
      screen.getByRole('combobox', { name: 'Changes scope' }),
      await screen.findByRole('option', { name: 'Uncommitted changes' }),
    )

    const range = await screen.findByTestId('branch-changes-range')
    expect(range).toHaveTextContent('Since the last commit on main.')
    expect(
      within(screen.getByTestId('branch-changes-list')).getByText('notes.txt'),
    ).toBeInTheDocument()
  })

  it('offers it for a folder with no commit yet, to say so', async () => {
    vi.mocked(agentOpsApi.worktreeDiff).mockRejectedValue(
      new ApiError(
        409,
        'conflict',
        'this folder has no commit yet to compare with',
      ),
    )
    const user = userEvent.setup()
    show(inFolder)

    await user.selectOptions(
      screen.getByRole('combobox', { name: 'Changes scope' }),
      await screen.findByRole('option', { name: 'Uncommitted changes' }),
    )

    expect(
      await screen.findByText(
        'This folder has no commit yet, so there is nothing to compare with.',
      ),
    ).toBeInTheDocument()
  })

  it('keeps the scopes of today for a folder that is not a Git repository', async () => {
    vi.mocked(agentOpsApi.worktreeDiff).mockRejectedValue(
      new ApiError(
        404,
        'not_found',
        'this session has no worktree of its own to compare',
      ),
    )
    const qc = show(inFolder)

    await waitFor(() => expect(agentOpsApi.worktreeDiff).toHaveBeenCalled())
    await settled(qc)
    expect(scopes()).toEqual([
      'Since session started',
      'Working tree',
      'Latest turn',
    ])
  })
})
