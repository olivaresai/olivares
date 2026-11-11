// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fakeRouter } from '@/test/fake-router'

vi.mock('@tanstack/react-router', async () =>
  (await import('@/test/fake-router')).fakeRouterModule(),
)

const granted = vi.hoisted(() => ({ denied: new Set<string>() }))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 't1',
    can: (permission: string) => !granted.denied.has(permission),
  }),
}))
vi.mock('@/features/agentops/api', async (orig) => ({
  ...(await orig<typeof import('@/features/agentops/api')>()),
  agentOpsApi: {
    listWorkspaces: vi.fn(),
    getWorkspace: vi.fn(),
    listRuns: vi.fn(),
    gitStatus: vi.fn(),
    listFiles: vi.fn(),
    readFile: vi.fn(),
    writeFile: vi.fn(),
  },
}))
vi.mock('@/features/agentops/workspace-file-viewer', async (orig) => ({
  ...(await orig<typeof import('@/features/agentops/workspace-file-viewer')>()),
  FileViewer: ({ path }: { path: string }) => (
    <div data-testid="viewer">{path}</div>
  ),
}))
vi.mock('./workspace-dashboard-view', () => ({
  WorkspaceDashboardView: () => <h1>The overview</h1>,
}))

import { agentOpsApi } from '@/features/agentops/api'
import type { FileEntry, WorkspaceDTO } from '@/features/agentops/types'
import { ApiError } from '@/lib/api/errors'
import { WorkspaceView } from './workspace-files'

function folder(ref: string, name: string, root: string): WorkspaceDTO {
  return {
    workspace_ref: ref,
    name,
    root_path: root,
    mount_mode: 'rw',
    max_read_bytes: 1 << 20,
    dlp_mode: 'off',
    state: 'active',
  }
}

const REPO = folder('ws_repo', 'acme-app', '/srv/repo')
const LAB = folder('ws_lab', 'lab', '/srv/lab')
const WORKTREE = folder('ws_tree', 'calc', '/srv/worktrees/run-1')

function entry(path: string, type = 'file'): FileEntry {
  return {
    name: path.split('/').pop()!,
    path,
    type,
    size: 10,
    mode: '0644',
    mtime: '2026-11-09T10:00:00Z',
  }
}

const FILES: Record<string, FileEntry[]> = {
  '': [entry('src', 'dir'), entry('README.md')],
  src: [entry('src/deep', 'dir'), entry('src/calc.py')],
  'src/deep': [entry('src/deep/leaf.py')],
}

function show(url = '/workspace') {
  fakeRouter.reset(url)
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <WorkspaceView />
    </QueryClientProvider>,
  )
  return qc
}

const param = (key: string) =>
  new URLSearchParams(fakeRouter.searchStr()).get(key)
const tree = () => screen.findByRole('tree', { name: 'Files' })
const row = (name: string) => screen.findByRole('treeitem', { name })

beforeEach(() => {
  granted.denied.clear()
  vi.clearAllMocks()
  vi.mocked(agentOpsApi.listWorkspaces).mockResolvedValue({
    items: [REPO, WORKTREE, LAB],
    has_more: false,
  })
  vi.mocked(agentOpsApi.listRuns).mockResolvedValue({
    items: [
      {
        run_ref: 'run_1',
        workspace_ref: 'ws_repo',
        workspace_path: '/srv/worktrees/run-1',
        worktree_branch: 'review/calc',
      },
      {
        run_ref: 'run_2',
        workspace_ref: 'ws_repo',
        workspace_path: '/srv/worktrees/run-2',
        worktree_branch: 'docs/install',
      },
      { run_ref: 'run_3', workspace_ref: 'ws_lab' },
    ],
    has_more: false,
  } as never)
  vi.mocked(agentOpsApi.gitStatus).mockImplementation(
    async (ref: string) =>
      ({
        branch: 'x',
        branches: [],
        files:
          ref === 'run_1' ? [] : [{ path: 'a', index: ' ', worktree: 'M' }],
        truncated: false,
        writable: true,
      }) as never,
  )
  vi.mocked(agentOpsApi.listFiles).mockImplementation(async (_ref, params) => ({
    path: params?.path ?? '',
    has_more: false,
    entries: FILES[params?.path ?? ''] ?? [],
  }))
  vi.mocked(agentOpsApi.readFile).mockImplementation(async (_ref, path) => ({
    path,
    size: 5,
    encoding: 'utf-8',
    content: 'hello',
  }))
})

describe('the /workspace folders column', () => {
  it('lists each folder with its worktree count and opens the first one', async () => {
    show()
    const folders = await screen.findByRole('navigation', { name: 'Folders' })
    const first = await within(folders).findByRole('button', {
      name: /acme-app/,
    })
    expect(first).toHaveAttribute('aria-current', 'true')
    expect(first).toHaveTextContent('2')
    await tree()
    expect(agentOpsApi.listFiles).toHaveBeenCalledWith(
      'ws_repo',
      expect.objectContaining({ path: undefined }),
    )
    expect(within(folders).queryByRole('button', { name: /^calc/ })).toBeNull()
  })

  it('shows the selected folder worktrees indented, with their branch and git state', async () => {
    show()
    const folders = await screen.findByRole('navigation', { name: 'Folders' })
    const registered = await within(folders).findByRole('button', {
      name: /review\/calc/,
    })
    expect(await within(folders).findByTitle('docs/install')).toBeVisible()
    expect(
      await within(folders).findByText('No uncommitted changes'),
    ).toBeInTheDocument()
    expect(
      await within(folders).findByText('Uncommitted changes: 1'),
    ).toBeInTheDocument()
    expect(agentOpsApi.gitStatus).toHaveBeenCalledTimes(2)

    const user = userEvent.setup()
    await user.click(registered)
    await waitFor(() => expect(param('workspace')).toBe('ws_tree'))
  })

  it('gives folder and worktree rows a 44 px target on a phone', async () => {
    show()
    const folders = await screen.findByRole('navigation', { name: 'Folders' })
    for (const name of [/^lab/, /review\/calc/])
      expect(await within(folders).findByRole('button', { name })).toHaveClass(
        'max-md:min-h-11',
      )
  })

  it('selects another folder, leaving the old file behind', async () => {
    const user = userEvent.setup()
    show('/workspace?workspace=ws_repo&file=README.md')
    const folders = await screen.findByRole('navigation', { name: 'Folders' })
    await user.click(
      await within(folders).findByRole('button', { name: /^lab/ }),
    )
    await waitFor(() => expect(param('workspace')).toBe('ws_lab'))
    expect(param('file')).toBeNull()
  })
})

describe('the /workspace file tree', () => {
  it('expands a folder in place, listing that level on demand', async () => {
    const user = userEvent.setup()
    show()
    expect(await tree()).toBeInTheDocument()
    expect(agentOpsApi.listFiles).toHaveBeenCalledTimes(1)
    const src = await row('src')
    expect(src).toHaveAttribute('aria-expanded', 'false')

    await user.click(within(src).getByText('src'))
    expect(await row('calc.py')).toHaveAttribute('aria-level', '2')
    expect(src).toHaveAttribute('aria-expanded', 'true')
    expect(await row('README.md')).toBeInTheDocument()
    expect(agentOpsApi.listFiles).toHaveBeenCalledWith(
      'ws_repo',
      expect.objectContaining({ path: 'src' }),
    )
    expect(param('path')).toBe('src')
  })

  it('walks a level through its keyset pages', async () => {
    vi.mocked(agentOpsApi.listFiles).mockImplementation(async (_ref, params) =>
      params?.cursor
        ? { path: '', has_more: false, entries: [entry('z.txt')] }
        : {
            path: '',
            has_more: true,
            cursor: 'after-a',
            entries: [entry('a.txt')],
          },
    )
    const user = userEvent.setup()
    show()
    await user.click(await screen.findByRole('button', { name: 'Load more' }))
    expect(await row('z.txt')).toBeInTheDocument()
    expect(agentOpsApi.listFiles).toHaveBeenLastCalledWith(
      'ws_repo',
      expect.objectContaining({ cursor: 'after-a' }),
    )
  })

  it('moves with the arrow keys and opens with Enter', async () => {
    const user = userEvent.setup()
    show()
    const files = await tree()
    await row('README.md')
    act(() => files.focus())
    expect(files).toHaveAttribute(
      'aria-activedescendant',
      (await row('src')).id,
    )

    await user.keyboard('{ArrowRight}')
    expect(await row('calc.py')).toBeInTheDocument()
    await user.keyboard('{ArrowDown}{ArrowDown}')
    expect(files).toHaveAttribute(
      'aria-activedescendant',
      (await row('calc.py')).id,
    )

    await user.keyboard('{Enter}')
    await waitFor(() => expect(param('file')).toBe('src/calc.py'))
    expect(await row('calc.py')).toHaveAttribute('aria-selected', 'true')

    await user.keyboard('{ArrowLeft}')
    expect(files).toHaveAttribute(
      'aria-activedescendant',
      (await row('src')).id,
    )
    await user.keyboard('{ArrowLeft}')
    await waitFor(() =>
      expect(screen.queryByRole('treeitem', { name: 'calc.py' })).toBeNull(),
    )

    await user.keyboard('{End}')
    expect(files).toHaveAttribute(
      'aria-activedescendant',
      (await row('README.md')).id,
    )
    await user.keyboard('{Home}')
    expect(files).toHaveAttribute(
      'aria-activedescendant',
      (await row('src')).id,
    )
  })

  it('ticks the row in focus with Space, among the folder shown', async () => {
    const user = userEvent.setup()
    show()
    const files = await tree()
    await row('README.md')
    act(() => files.focus())
    await user.keyboard('{End} ')
    expect(
      await screen.findByRole('button', { name: 'Download selected (1)' }),
    ).toBeInTheDocument()
  })
})

describe('the /workspace preview', () => {
  it('selects a file in the URL and shows its breadcrumb above the preview', async () => {
    const user = userEvent.setup()
    show('/workspace?path=src')
    await user.click(within(await row('calc.py')).getByText('calc.py'))
    await waitFor(() => expect(param('file')).toBe('src/calc.py'))
    expect(await screen.findByTestId('viewer')).toHaveTextContent('src/calc.py')

    const crumbs = screen.getByRole('navigation', { name: 'Files' })
    expect(within(crumbs).getByText('calc.py')).toHaveAttribute(
      'aria-current',
      'page',
    )
    await user.click(within(crumbs).getByRole('button', { name: 'src' }))
    await waitFor(() => expect(param('file')).toBeNull())
    expect(param('path')).toBe('src')
  })

  it('opens a deep link in its folder and reveals the file', async () => {
    show('/workspace?workspace=ws_lab&file=src/deep/leaf.py')
    expect(await screen.findByTestId('viewer')).toHaveTextContent(
      'src/deep/leaf.py',
    )
    expect(await row('leaf.py')).toHaveAttribute('aria-selected', 'true')
    for (const path of ['src', 'src/deep'])
      expect(agentOpsApi.listFiles).toHaveBeenCalledWith(
        'ws_lab',
        expect.objectContaining({ path }),
      )
    const folders = screen.getByRole('navigation', { name: 'Folders' })
    expect(
      within(folders).getByRole('button', { name: /^lab/ }),
    ).toHaveAttribute('aria-current', 'true')
  })

  it('fetches the next pages of a level until a deep link file is revealed', async () => {
    vi.mocked(agentOpsApi.listFiles).mockImplementation(async (_ref, params) =>
      params?.cursor
        ? { path: '', has_more: false, entries: [entry('late.txt')] }
        : {
            path: '',
            has_more: true,
            cursor: 'next',
            entries: [entry('early.txt')],
          },
    )
    show('/workspace?workspace=ws_lab&file=late.txt')
    expect(await row('late.txt')).toHaveAttribute('aria-selected', 'true')
  })
})

describe('the /workspace git letters', () => {
  function status(files: object[], truncated = false) {
    vi.mocked(agentOpsApi.gitStatus).mockResolvedValue({
      branch: 'review/calc',
      branches: [],
      files,
      truncated,
      writable: true,
    } as never)
  }

  it('names each changed file with git words: conflict, untracked, modified', async () => {
    vi.mocked(agentOpsApi.listFiles).mockResolvedValue({
      path: '',
      has_more: false,
      entries: [
        entry('both.md'),
        entry('notes', 'dir'),
        entry('edit.md'),
        entry('odd.md'),
      ],
    })
    status([
      { path: 'both.md', index: 'U', worktree: 'U' },
      { path: 'notes/', index: '?', worktree: '?' },
      { path: 'edit.md', index: ' ', worktree: 'M' },
      { path: 'odd.md', index: 'R', worktree: ' ' },
    ])
    show('/workspace?workspace=ws_tree')

    const mark = async (name: string, word: string) =>
      within(await row(name)).findByRole('img', { name: word })
    expect(await mark('both.md', 'In conflict')).toHaveTextContent('UU')
    expect(await mark('notes', 'Untracked')).toHaveTextContent('??')
    expect(await mark('edit.md', 'Modified (not staged)')).toHaveTextContent(
      'M',
    )
    // A code the map does not know reads as its letters, never as a wrong word.
    expect(await mark('odd.md', 'R')).toHaveAttribute('title', 'R')
    expect(screen.queryByText(/Not every changed file/)).toBeNull()
  })

  it('says in one line when git listed only part of the changes', async () => {
    status([{ path: 'README.md', index: ' ', worktree: 'M' }], true)
    show('/workspace?workspace=ws_tree')

    expect(
      await screen.findByText(
        'Not every changed file is marked: Git listed only part of the changes.',
      ),
    ).toBeVisible()
  })

  it('marks nothing in a folder that is not a session worktree', async () => {
    show('/workspace?workspace=ws_lab')
    expect(await row('README.md')).toBeInTheDocument()
    expect(screen.queryByRole('img', { name: /working tree/ })).toBeNull()
  })
})

describe('the /workspace states', () => {
  it('ends a folder the person cannot see in the no-access state', async () => {
    vi.mocked(agentOpsApi.getWorkspace).mockRejectedValue(
      new ApiError(404, 'not_found', 'missing'),
    )
    const user = userEvent.setup()
    show('/workspace?workspace=ws_gone&file=a.txt')
    expect(await screen.findByText('You cannot open this folder')).toBeVisible()
    expect(screen.queryByRole('tree')).toBeNull()

    await user.click(screen.getByRole('button', { name: 'Show my folders' }))
    await waitFor(() => expect(param('workspace')).toBeNull())
    expect(param('file')).toBeNull()
    expect(await tree()).toBeInTheDocument()
  })

  it('opens a folder outside the listed page from its reference', async () => {
    vi.mocked(agentOpsApi.getWorkspace).mockResolvedValue(
      folder('ws_far', 'far', '/srv/far'),
    )
    show('/workspace?workspace=ws_far')
    expect(await tree()).toBeInTheDocument()
    expect(agentOpsApi.listFiles).toHaveBeenCalledWith(
      'ws_far',
      expect.anything(),
    )
  })

  it('teaches the page when there is no folder and offers Add folder', async () => {
    vi.mocked(agentOpsApi.listWorkspaces).mockResolvedValue({
      items: [],
      has_more: false,
    })
    show()
    expect(await screen.findByText('No folders yet')).toBeVisible()
    const add = screen.getAllByRole('link', { name: 'Add folder' })
    expect(add[0].getAttribute('href')).toBe('/agentops?tab=workspaces')
    expect(screen.queryByRole('tree')).toBeNull()
  })

  it('says the folders could not load and asks again', async () => {
    vi.mocked(agentOpsApi.listWorkspaces).mockRejectedValueOnce(
      new Error('boom'),
    )
    const user = userEvent.setup()
    show()
    expect(
      await screen.findByText('The folders could not be loaded.'),
    ).toBeVisible()
    await user.click(screen.getByRole('button', { name: 'Retry' }))
    expect(await tree()).toBeInTheDocument()
  })

  it('offers Add folder only to who may register one', async () => {
    granted.denied.add('sessions:workspace:admin')
    show()
    await tree()
    expect(screen.queryByRole('link', { name: 'Add folder' })).toBeNull()
  })

  it('leaves out worktrees when the person may not read sessions', async () => {
    granted.denied.add('sessions:run:read')
    show()
    await tree()
    expect(agentOpsApi.listRuns).not.toHaveBeenCalled()
    expect(screen.queryByText('review/calc')).toBeNull()
  })
})

describe('the /workspace overview', () => {
  it('keeps the workspace overview behind ?tab=overview and returns to the files', async () => {
    const user = userEvent.setup()
    show('/workspace?tab=overview')
    expect(
      await screen.findByRole('heading', { name: 'The overview' }),
    ).toBeVisible()
    expect(screen.queryByRole('tree')).toBeNull()

    await user.click(screen.getByRole('button', { name: 'Files' }))
    expect(await tree()).toBeInTheDocument()
    expect(param('tab')).toBeNull()
  })

  it('reaches the overview from the files', async () => {
    const user = userEvent.setup()
    show()
    await tree()
    await user.click(screen.getByRole('button', { name: 'Overview' }))
    expect(
      await screen.findByRole('heading', { name: 'The overview' }),
    ).toBeVisible()
    expect(param('tab')).toBe('overview')
  })

  it('opens the overview for who may not read session folders', async () => {
    granted.denied.add('sessions:workspace:read')
    show()
    expect(
      await screen.findByRole('heading', { name: 'The overview' }),
    ).toBeVisible()
    expect(agentOpsApi.listWorkspaces).not.toHaveBeenCalled()
  })
})
