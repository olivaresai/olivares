// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The workspace file browser against git HEAD: a file edited in the
// console shows what differs from HEAD, before anything is published.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))
vi.mock('@/features/agentops/api', async (orig) => ({
  ...(await orig<typeof import('@/features/agentops/api')>()),
  agentOpsApi: { listFiles: vi.fn(), readFile: vi.fn(), writeFile: vi.fn() },
}))
vi.mock('@/components/ui/code-editor', () => ({
  CodeEditor: ({ value }: { value: string }) => (
    <div data-testid="editor">{value}</div>
  ),
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
import { agentOpsApi } from './api'
import type { FileReadResponse, WorkspaceDTO } from './types'
import { WorkspaceBrowser } from './workspace-browser'

const workspace: WorkspaceDTO = {
  workspace_ref: 'ws_1',
  name: 'repo',
  root_path: '/srv/repo',
  mount_mode: 'rw',
  max_read_bytes: 1 << 20,
  dlp_mode: 'off',
  state: 'active',
}

function text(path: string, content: string): FileReadResponse {
  return { path, size: content.length, encoding: 'utf-8', content }
}

function show() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <WorkspaceBrowser workspace={workspace} />
    </QueryClientProvider>,
  )
  return qc
}

// Every request has been answered and rendered: what is absent now is absent for good.
const settled = (qc: QueryClient) =>
  waitFor(() => expect(qc.isFetching()).toBe(0))

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(agentOpsApi.listFiles).mockResolvedValue({
    path: '',
    has_more: false,
    entries: ['README.md', 'docs.md', 'new.txt'].map((name) => ({
      name,
      path: name,
      type: 'file',
      size: 10,
      mode: '0644',
      mtime: '2026-10-06T10:00:00Z',
    })),
  })
})

describe('WorkspaceBrowser — against git HEAD', () => {
  it('compares a file edited in the console with what HEAD holds', async () => {
    vi.mocked(agentOpsApi.readFile).mockImplementation(async (_ws, path, rev) =>
      text(
        path,
        rev === 'HEAD' ? '# Title\n' : '# Title\nedited in the console\n',
      ),
    )
    const user = userEvent.setup()
    show()
    await user.click(await screen.findByText('README.md'))
    expect(await screen.findByTestId('editor')).toHaveTextContent(
      'edited in the console',
    )

    await user.click(
      await screen.findByRole('button', { name: 'Changes since HEAD' }),
    )
    expect(screen.getByTestId('diff-original')).toHaveTextContent(/^# Title$/)
    expect(screen.getByTestId('diff-modified')).toHaveTextContent(
      'edited in the console',
    )
    expect(agentOpsApi.readFile).toHaveBeenCalledWith(
      'ws_1',
      'README.md',
      'HEAD',
    )
  })

  it('shows a file with no HEAD version as it always did, with no switch and no error', async () => {
    vi.mocked(agentOpsApi.readFile).mockImplementation(
      async (_ws, path, rev) => {
        if (rev === 'HEAD') throw new ApiError(404, 'not_found', 'not in HEAD')
        return text(path, 'never committed\n')
      },
    )
    const user = userEvent.setup()
    const qc = show()
    await user.click(await screen.findByText('new.txt'))
    expect(await screen.findByTestId('editor')).toHaveTextContent(
      'never committed',
    )
    await waitFor(() =>
      expect(agentOpsApi.readFile).toHaveBeenCalledWith(
        'ws_1',
        'new.txt',
        'HEAD',
      ),
    )
    await settled(qc)
    expect(
      screen.queryByRole('button', { name: 'Changes since HEAD' }),
    ).toBeNull()
    expect(screen.queryByRole('alert')).toBeNull()
    expect(screen.queryByText('Could not compare with HEAD.')).toBeNull()
  })

  it('says the comparison could not be made when the workspace refuses or HEAD fails', async () => {
    for (const status of [403, 500]) {
      vi.mocked(agentOpsApi.readFile).mockImplementation(
        async (_ws, path, rev) => {
          if (rev === 'HEAD') throw new ApiError(status, 'refused', 'refused')
          return text(path, 'current\n')
        },
      )
      const user = userEvent.setup()
      const qc = show()
      await user.click(await screen.findByText('README.md'))
      await settled(qc)
      expect(
        await screen.findByText('Could not compare with HEAD.'),
      ).toBeInTheDocument()
      expect(screen.getByTestId('editor')).toHaveTextContent('current')
      expect(
        screen.queryByRole('button', { name: 'Changes since HEAD' }),
      ).toBeNull()
      cleanup()
    }
  })

  it('does not ask HEAD about a binary or cut-off file, which cannot be compared', async () => {
    vi.mocked(agentOpsApi.readFile).mockImplementation(
      async (_ws, path, rev) => {
        if (rev === 'HEAD') return text(path, 'unexpected')
        return path === 'README.md'
          ? { ...text(path, 'cut'), truncated: true }
          : { ...text(path, 'AAAA'), encoding: 'base64' }
      },
    )
    const user = userEvent.setup()
    const qc = show()
    await user.click(await screen.findByText('README.md'))
    await screen.findByTestId('editor')
    await settled(qc)
    await user.click(screen.getByText('new.txt'))
    await screen.findByText(/binary/i)
    await settled(qc)
    expect(agentOpsApi.readFile).not.toHaveBeenCalledWith(
      expect.anything(),
      expect.anything(),
      'HEAD',
    )
  })

  it('opens the next file on its plain view, not on the Changes view of the last', async () => {
    vi.mocked(agentOpsApi.readFile).mockImplementation(async (_ws, path, rev) =>
      text(path, rev === 'HEAD' ? 'before\n' : 'after\n'),
    )
    const user = userEvent.setup()
    show()
    await user.click(await screen.findByText('README.md'))
    await user.click(
      await screen.findByRole('button', { name: 'Changes since HEAD' }),
    )
    expect(screen.getByTestId('code-diff')).toBeInTheDocument()
    await user.click(screen.getByText('docs.md'))
    await waitFor(() =>
      expect(agentOpsApi.readFile).toHaveBeenCalledWith(
        'ws_1',
        'docs.md',
        'HEAD',
      ),
    )
    expect(
      await screen.findByRole('button', { name: 'Changes since HEAD' }),
    ).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'File' })).toHaveAttribute(
      'aria-pressed',
      'true',
    )
    expect(screen.queryByTestId('code-diff')).toBeNull()
  })

  it('does not offer the comparison while the file is being edited', async () => {
    vi.mocked(agentOpsApi.readFile).mockImplementation(async (_ws, path, rev) =>
      text(path, rev === 'HEAD' ? 'old\n' : 'new\n'),
    )
    const user = userEvent.setup()
    show()
    await user.click(await screen.findByText('README.md'))
    await screen.findByRole('button', { name: 'Changes since HEAD' })
    await user.click(screen.getByRole('button', { name: 'Edit' }))
    expect(
      screen.queryByRole('button', { name: 'Changes since HEAD' }),
    ).toBeNull()
    expect(screen.getByTestId('editor')).toHaveTextContent('new')
  })

  it('does not read HEAD again after a save, which cannot change it', async () => {
    vi.mocked(agentOpsApi.readFile).mockImplementation(async (_ws, path, rev) =>
      text(path, rev === 'HEAD' ? 'old\n' : 'new\n'),
    )
    vi.mocked(agentOpsApi.writeFile).mockResolvedValue({
      path: 'README.md',
      size: 4,
      sha256: 'x',
      created: false,
    })
    const headReads = () =>
      vi
        .mocked(agentOpsApi.readFile)
        .mock.calls.filter(([, , rev]) => rev === 'HEAD').length
    const fileReads = () =>
      vi
        .mocked(agentOpsApi.readFile)
        .mock.calls.filter(([, , rev]) => rev === undefined).length
    const user = userEvent.setup()
    const qc = show()
    await user.click(await screen.findByText('README.md'))
    await screen.findByRole('button', { name: 'Changes since HEAD' })
    expect(headReads()).toBe(1)
    expect(fileReads()).toBe(1)

    await user.click(screen.getByRole('button', { name: 'Edit' }))
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(agentOpsApi.writeFile).toHaveBeenCalled())
    // The saved file is read again (that is what the save invalidates); HEAD is not.
    await waitFor(() => expect(fileReads()).toBeGreaterThanOrEqual(2))
    await settled(qc)
    expect(headReads()).toBe(1)
  })
})

describe('WorkspaceBrowser scope retirement', () => {
  it('retires the selected file and unsaved editor when its workspace changes', async () => {
    vi.mocked(agentOpsApi.readFile).mockImplementation(async (ws, path) =>
      text(path, ws === 'ws_1' ? 'checkout A' : 'checkout B'),
    )
    const user = userEvent.setup()
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    const subject = (ws: WorkspaceDTO) => (
      <QueryClientProvider client={qc}>
        <WorkspaceBrowser workspace={ws} />
      </QueryClientProvider>
    )
    const view = render(subject(workspace))
    await user.click(await screen.findByText('README.md'))
    await user.click(await screen.findByRole('button', { name: 'Edit' }))
    expect(
      await screen.findByRole('button', { name: 'Save' }),
    ).toBeInTheDocument()
    const second = {
      ...workspace,
      workspace_ref: 'ws_2',
      root_path: '/srv/worktree',
    }
    view.rerender(subject(second))
    await settled(qc)
    expect(screen.queryByRole('button', { name: 'Save' })).toBeNull()
    expect(screen.queryByTestId('editor')).toBeNull()
    expect(agentOpsApi.writeFile).not.toHaveBeenCalled()
    await user.click(await screen.findByText('README.md'))
    expect(await screen.findByTestId('editor')).toHaveTextContent('checkout B')
  })
})
