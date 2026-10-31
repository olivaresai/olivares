// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fakeRouter } from '@/test/fake-router'

vi.mock('@tanstack/react-router', async () =>
  (await import('@/test/fake-router')).fakeRouterModule(),
)
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))
vi.mock('@/features/agentops/api', async (orig) => ({
  ...(await orig<typeof import('@/features/agentops/api')>()),
  agentOpsApi: {
    listFiles: vi.fn(),
    listWorkspaces: vi.fn(),
    getWorkspace: vi.fn(),
    readFile: vi.fn(),
    writeFile: vi.fn(),
  },
}))
vi.mock('@/components/ui/code-editor', () => ({
  CodeEditor: ({
    value,
    onChange,
  }: {
    value: string
    onChange?: (value: string) => void
  }) => (
    <>
      <div data-testid="editor">{value}</div>
      {onChange && (
        <button onClick={() => onChange(`${value}\nchanged during save`)}>
          Change draft
        </button>
      )}
    </>
  ),
}))
vi.mock('@/features/shared/head-diff', () => ({
  HeadDiff: ({ children }: { children: ReactNode }) => <>{children}</>,
}))

import { ApiError } from '@/lib/api/errors'
import { agentOpsApi } from './api'
import type { FileEntry, FileReadResponse, WorkspaceDTO } from './types'
import { WorkspaceBrowser } from './workspace-browser'
import { WorkspacesPanel } from './workspaces-panel'

const workspace: WorkspaceDTO = {
  workspace_ref: 'ws_1',
  name: 'repo',
  root_path: '/srv/repo',
  mount_mode: 'rw',
  max_read_bytes: 1 << 20,
  dlp_mode: 'off',
  state: 'active',
}

function entry(path: string, type = 'file'): FileEntry {
  return {
    name: path.split('/').pop()!,
    path,
    type,
    size: 10,
    mode: '0644',
    mtime: '2026-10-24T10:00:00Z',
  }
}

function text(path: string, content: string): FileReadResponse {
  return { path, size: content.length, encoding: 'utf-8', content }
}

function show(url?: string) {
  if (url) fakeRouter.reset(url)
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <WorkspaceBrowser workspace={workspace} />
    </QueryClientProvider>,
  )
  return qc
}

beforeEach(() => {
  fakeRouter.reset('/workspaces')
  vi.clearAllMocks()
  vi.mocked(agentOpsApi.listFiles).mockImplementation(async (_ws, params) => ({
    path: params?.path ?? '',
    has_more: false,
    entries: params?.path
      ? [entry('docs/README.md'), entry('docs/other.md')]
      : [
          entry('docs', 'dir'),
          entry('README.md'),
          entry('page.html'),
          entry('pixel.png'),
          entry('photo.jpg'),
          entry('motion.gif'),
          entry('image.webp'),
          entry('vector.svg'),
          entry('blob.bin'),
        ],
  }))
  vi.mocked(agentOpsApi.readFile).mockImplementation(async (_ws, path, rev) =>
    text(path, rev === 'HEAD' ? '# Before\n' : `# ${path}\n\nRendered text.\n`),
  )
})

describe('WorkspaceBrowser previews', () => {
  it('opens Markdown rendered and toggles to its source', async () => {
    vi.mocked(agentOpsApi.readFile).mockImplementation(async (_ws, path) =>
      text(path, '# Guide\n\nA **rendered** document.\n'),
    )
    const user = userEvent.setup()
    show()
    await user.click(await screen.findByText('README.md'))

    expect(
      await screen.findByRole('heading', { name: 'Guide', level: 2 }),
    ).toBeInTheDocument()
    expect(screen.queryByTestId('editor')).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Source' }))
    expect(screen.getByTestId('editor')).toHaveTextContent(
      'A **rendered** document.',
    )
    await user.click(screen.getByRole('button', { name: 'Rendered' }))
    expect(
      await screen.findByRole('heading', { name: 'Guide', level: 2 }),
    ).toBeInTheDocument()
  })

  it('opens HTML in a fully sandboxed iframe and keeps a Source toggle', async () => {
    vi.mocked(agentOpsApi.readFile).mockImplementation(async (_ws, path) =>
      text(path, '<h1>Safe document</h1><script>fetch("/script-ran")</script>'),
    )
    const user = userEvent.setup()
    show()
    await user.click(await screen.findByText('page.html'))

    const frame = await screen.findByTitle('HTML preview: page.html')
    expect(frame).toHaveAttribute('sandbox', '')
    expect(frame).toHaveAttribute('srcdoc', expect.stringContaining('<h1>'))
    await user.click(screen.getByRole('button', { name: 'Source' }))
    expect(screen.getByTestId('editor')).toHaveTextContent('Safe document')
  })

  it('shows PNG, JPG, GIF, WebP and SVG files as images', async () => {
    const user = userEvent.setup()
    show()
    for (const [name, mime] of [
      ['pixel.png', 'image/png'],
      ['photo.jpg', 'image/jpeg'],
      ['motion.gif', 'image/gif'],
      ['image.webp', 'image/webp'],
    ]) {
      vi.mocked(agentOpsApi.readFile).mockResolvedValue({
        path: name,
        size: 8,
        encoding: 'base64',
        content: 'aGVsbG8=',
      })
      await user.click(await screen.findByText(name))
      expect(await screen.findByRole('img', { name })).toHaveAttribute(
        'src',
        `data:${mime};base64,aGVsbG8=`,
      )
    }

    vi.mocked(agentOpsApi.readFile).mockImplementation(async (_ws, path) =>
      text(path, '<svg xmlns="http://www.w3.org/2000/svg"></svg>'),
    )
    await user.click(await screen.findByText('vector.svg'))
    expect(
      await screen.findByRole('img', { name: 'vector.svg' }),
    ).toHaveAttribute('src', expect.stringMatching(/^data:image\/svg\+xml/))
    expect(agentOpsApi.readFile).not.toHaveBeenCalledWith(
      expect.anything(),
      expect.anything(),
      'HEAD',
    )
  })
  it('keeps SVG source editable while showing it as an image by default', async () => {
    vi.mocked(agentOpsApi.readFile).mockImplementation(async (_ws, path) =>
      text(path, '<svg xmlns="http://www.w3.org/2000/svg"></svg>'),
    )
    const user = userEvent.setup()
    show()
    await user.click(await screen.findByText('vector.svg'))
    expect(await screen.findByRole('img', { name: 'vector.svg' })).toBeVisible()
    await user.click(screen.getByRole('button', { name: 'Edit' }))
    expect(screen.getByTestId('editor')).toHaveTextContent('<svg')
    expect(screen.getByRole('button', { name: 'Save' })).toBeInTheDocument()
  })

  it('keeps Download and the binary fallback for other binary files', async () => {
    vi.mocked(agentOpsApi.readFile).mockResolvedValue({
      path: 'blob.bin',
      size: 4,
      encoding: 'base64',
      content: 'AAECAw==',
    })
    const user = userEvent.setup()
    show()
    await user.click(await screen.findByText('blob.bin'))
    expect(await screen.findByText(/Binary file/)).toBeVisible()
    expect(screen.getByRole('button', { name: 'Download' })).toBeInTheDocument()
    expect(screen.queryByRole('img')).toBeNull()
    expect(screen.queryByTestId('editor')).toBeNull()
  })
})

describe('WorkspaceBrowser file URL', () => {
  it('does not save a draft through Back into a different file', async () => {
    vi.mocked(agentOpsApi.readFile).mockImplementation(async (_ws, path) =>
      text(path, path === 'docs/README.md' ? '# File A\n' : '# File B\n'),
    )
    const user = userEvent.setup()
    show('/workspaces?path=docs&file=docs/README.md')
    await user.click(await screen.findByRole('button', { name: 'Source' }))
    await user.click(await screen.findByText('other.md'))
    await user.click(await screen.findByRole('button', { name: 'Source' }))
    await user.click(await screen.findByRole('button', { name: 'Edit' }))
    expect(screen.getByTestId('editor')).toHaveTextContent('File B')

    act(() => fakeRouter.back())
    expect(await screen.findByText('docs/README.md')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Save' })).toBeNull()
    expect(agentOpsApi.writeFile).not.toHaveBeenCalled()
    act(() => fakeRouter.forward())
    expect(await screen.findByTestId('editor')).toHaveTextContent('File B')
  })

  it('keeps edits made while a save is in flight', async () => {
    vi.mocked(agentOpsApi.readFile).mockImplementation(async (_ws, path) =>
      text(path, '# Original\n'),
    )
    let finish!: (value: {
      path: string
      size: number
      sha256: string
      created: boolean
    }) => void
    vi.mocked(agentOpsApi.writeFile).mockImplementation(
      () =>
        new Promise((resolve) => {
          finish = resolve
        }),
    )
    const user = userEvent.setup()
    const qc = show('/workspaces?file=README.md')
    await user.click(await screen.findByRole('button', { name: 'Source' }))
    await user.click(await screen.findByRole('button', { name: 'Edit' }))
    await user.click(await screen.findByRole('button', { name: 'Save' }))
    await user.click(
      await screen.findByRole('button', { name: 'Change draft' }),
    )
    finish({ path: 'README.md', size: 1, sha256: 'x', created: false })

    await waitFor(() => expect(qc.isMutating()).toBe(0))
    expect(screen.getByTestId('editor')).toHaveTextContent(
      'changed during save',
    )
    expect(screen.getByRole('button', { name: 'Save' })).toBeInTheDocument()
  })

  it('clears file parameters written for a different workspace on remount', async () => {
    show('/workspaces?workspace=ws_2&path=docs&file=docs/README.md')
    expect(
      await screen.findByText('Select a file to view or edit.'),
    ).toBeVisible()
    const params = new URLSearchParams(fakeRouter.searchStr())
    expect(params.has('workspace')).toBe(false)
    expect(params.has('path')).toBe(false)
    expect(params.has('file')).toBe(false)
    expect(agentOpsApi.readFile).not.toHaveBeenCalled()
  })

  it('restores path and file beside the session Files parameters', async () => {
    vi.mocked(agentOpsApi.readFile).mockImplementation(async (_ws, path) =>
      text(path, path === 'docs/README.md' ? '# Shared\n' : '# Other\n'),
    )
    show(
      '/sessions?session=run%3A1&panel=files&path=docs&file=docs%2FREADME.md&still=yes',
    )

    expect(
      await screen.findByRole('heading', { name: 'Shared', level: 2 }),
    ).toBeInTheDocument()
    expect(agentOpsApi.listFiles).toHaveBeenCalledWith(
      'ws_1',
      expect.objectContaining({ path: 'docs' }),
    )
    await userEvent.click(await screen.findByText('other.md'))
    expect(
      await screen.findByRole('heading', { name: 'Other', level: 2 }),
    ).toBeInTheDocument()
    expect(
      Object.fromEntries(new URLSearchParams(fakeRouter.searchStr())),
    ).toMatchObject({
      session: 'run:1',
      panel: 'files',
      path: 'docs',
      file: 'docs/other.md',
      still: 'yes',
    })

    act(() => fakeRouter.back())
    expect(
      await screen.findByRole('heading', { name: 'Shared', level: 2 }),
    ).toBeInTheDocument()
    act(() => fakeRouter.forward())
    expect(
      await screen.findByRole('heading', { name: 'Other', level: 2 }),
    ).toBeInTheDocument()
  })

  it('pushes folder and file navigation and follows Back and Forward', async () => {
    const user = userEvent.setup()
    show('/workspaces?still=yes')
    await user.click(await screen.findByText('docs'))
    expect(
      Object.fromEntries(new URLSearchParams(fakeRouter.searchStr())),
    ).toMatchObject({
      path: 'docs',
      still: 'yes',
    })
    expect(new URLSearchParams(fakeRouter.searchStr()).has('file')).toBe(false)

    await user.click(await screen.findByText('README.md'))
    expect(
      await screen.findByRole('heading', { name: 'docs/README.md', level: 2 }),
    ).toBeInTheDocument()
    expect(new URLSearchParams(fakeRouter.searchStr()).get('file')).toBe(
      'docs/README.md',
    )
    act(() => fakeRouter.back())
    expect(
      await screen.findByText('Select a file to view or edit.'),
    ).toBeVisible()
    expect(new URLSearchParams(fakeRouter.searchStr()).has('file')).toBe(false)
    act(() => fakeRouter.forward())
    expect(
      await screen.findByRole('heading', { name: 'docs/README.md', level: 2 }),
    ).toBeInTheDocument()
  })

  it('offers Retry after a file read error and shows the recovered file', async () => {
    vi.mocked(agentOpsApi.readFile)
      .mockRejectedValueOnce(new ApiError(500, 'read_failed', 'Read failed'))
      .mockImplementation(async (_ws, path) => text(path, '# Recovered\n'))
    const user = userEvent.setup()
    show('/workspaces?file=README.md')

    expect(await screen.findByRole('alert')).toHaveTextContent('Read failed')
    await user.click(screen.getByRole('button', { name: 'Retry' }))
    expect(
      await screen.findByRole('heading', { name: 'Recovered', level: 2 }),
    ).toBeInTheDocument()
    await waitFor(() =>
      expect(
        vi
          .mocked(agentOpsApi.readFile)
          .mock.calls.filter(([, , rev]) => rev === undefined),
      ).toHaveLength(2),
    )
  })
})

describe('WorkspacesPanel file links', () => {
  it('restores the workspace sheet and selected document from the URL', async () => {
    vi.mocked(agentOpsApi.listWorkspaces).mockResolvedValue({
      items: [],
      has_more: true,
    })
    vi.mocked(agentOpsApi.getWorkspace).mockResolvedValue(workspace)
    vi.mocked(agentOpsApi.readFile).mockImplementation(async (_ws, path) =>
      text(path, '# Shared\n'),
    )
    fakeRouter.reset(
      '/sessions?tab=workspaces&workspace=ws_1&path=docs&file=docs%2FREADME.md',
    )
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    render(
      <QueryClientProvider client={qc}>
        <WorkspacesPanel />
      </QueryClientProvider>,
    )
    expect(
      await screen.findByRole('heading', { name: 'Shared', level: 2 }),
    ).toBeInTheDocument()
    expect(agentOpsApi.getWorkspace).toHaveBeenCalledWith(
      'ws_1',
      expect.objectContaining({ tenant: 't1' }),
    )
  })

  it('clears a closed workspace file before opening another workspace', async () => {
    const second = { ...workspace, workspace_ref: 'ws_2', name: 'other' }
    vi.mocked(agentOpsApi.listWorkspaces).mockResolvedValue({
      items: [workspace, second],
      has_more: false,
    })
    vi.mocked(agentOpsApi.getWorkspace).mockImplementation(async (ref) =>
      ref === 'ws_1' ? workspace : second,
    )
    vi.mocked(agentOpsApi.readFile).mockImplementation(async (_ws, path) =>
      text(path, '# First workspace\n'),
    )
    const user = userEvent.setup()
    fakeRouter.reset('/workspaces')
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    render(
      <QueryClientProvider client={qc}>
        <WorkspacesPanel />
      </QueryClientProvider>,
    )
    const openButtons = await screen.findAllByRole('button', {
      name: 'Browse files',
    })
    await user.click(openButtons[0])
    await user.click(await screen.findByText('README.md'))
    expect(
      await screen.findByRole('heading', { name: 'First workspace', level: 2 }),
    ).toBeInTheDocument()
    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())

    await user.click(openButtons[1])
    expect(
      await screen.findByText('Select a file to view or edit.'),
    ).toBeVisible()
    const params = new URLSearchParams(fakeRouter.searchStr())
    expect(params.get('workspace')).toBe('ws_2')
    expect(params.has('path')).toBe(false)
    expect(params.has('file')).toBe(false)
    expect(agentOpsApi.readFile).not.toHaveBeenCalledWith(
      'ws_2',
      expect.anything(),
    )
  })
})
