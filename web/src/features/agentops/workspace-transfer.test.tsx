// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fakeRouter } from '@/test/fake-router'

const auth = vi.hoisted(() => ({ write: true }))

vi.mock('@tanstack/react-router', async () =>
  (await import('@/test/fake-router')).fakeRouterModule(),
)
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => auth.write }),
}))
vi.mock('@/features/agentops/api', async (orig) => ({
  ...(await orig<typeof import('@/features/agentops/api')>()),
  agentOpsApi: {
    listFiles: vi.fn(),
    statFile: vi.fn(),
    writeFile: vi.fn(),
    mkdir: vi.fn(),
    archiveFiles: vi.fn(),
    readFile: vi.fn(),
  },
}))
vi.mock('@/lib/api/download', async (orig) => ({
  ...(await orig<typeof import('@/lib/api/download')>()),
  downloadBlob: vi.fn(),
}))
vi.mock('@/components/ui/toaster', async (orig) => ({
  ...(await orig<typeof import('@/components/ui/toaster')>()),
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
}))

import { toast } from '@/components/ui/toaster'
import { downloadBlob } from '@/lib/api/download'
import { ApiError } from '@/lib/api/errors'
import { agentOpsApi } from './api'
import type { FileEntry, WorkspaceDTO } from './types'
import { WorkspaceBrowser } from './workspace-browser'

const workspace: WorkspaceDTO = {
  workspace_ref: 'ws_1',
  name: 'repo',
  root_path: '/srv/repo',
  mount_mode: 'rw',
  max_read_bytes: 8,
  dlp_mode: 'off',
  state: 'active',
}
const readOnly: WorkspaceDTO = { ...workspace, mount_mode: 'ro' }
const READ_ONLY_REASON = 'This folder is read-only, so files cannot be added.'

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

function show(ws: WorkspaceDTO = workspace, url = '/workspaces') {
  fakeRouter.reset(url)
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <WorkspaceBrowser workspace={ws} />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  auth.write = true
  vi.clearAllMocks()
  vi.mocked(agentOpsApi.listFiles).mockImplementation(async (_ws, params) => ({
    path: params?.path ?? '',
    has_more: false,
    entries: params?.path
      ? [entry('docs/a.md'), entry('docs/b.md')]
      : [entry('docs', 'dir'), entry('README.md'), entry('page.html')],
  }))
  vi.mocked(agentOpsApi.statFile).mockRejectedValue(
    new ApiError(404, 'not_found', 'missing'),
  )
  vi.mocked(agentOpsApi.writeFile).mockResolvedValue({
    path: 'x',
    size: 1,
    sha256: 'h',
    created: true,
  })
  vi.mocked(agentOpsApi.archiveFiles).mockResolvedValue({
    blob: new Blob(['zip']),
    filename: 'docs.zip',
    labels: [],
  })
})

describe('WorkspaceBrowser upload', () => {
  it('writes a picked file into the folder shown and states the size limit', async () => {
    const user = userEvent.setup()
    show(workspace, '/workspaces?path=docs')
    expect(await screen.findByText('Up to 8 B per file')).toBeInTheDocument()

    const file = new File(['hello'], 'notes.txt', { type: 'text/plain' })
    await user.upload(screen.getByTestId('upload-input'), file)

    await waitFor(() =>
      expect(agentOpsApi.writeFile).toHaveBeenCalledWith(
        'ws_1',
        'docs/notes.txt',
        file,
        // The file browser's upload carries no start authority.
        undefined,
      ),
    )
    expect(toast.success).toHaveBeenCalledWith('Uploaded: 1')
  })

  it('shows the DLP labels the engine put on an upload', async () => {
    vi.mocked(agentOpsApi.writeFile).mockResolvedValue({
      path: 'x',
      size: 1,
      sha256: 'h',
      created: true,
      sensitivity: [{ class: 'secret.credential' }],
    })
    const user = userEvent.setup()
    show()
    await screen.findByText('README.md')
    await user.upload(
      screen.getByTestId('upload-input'),
      new File(['k'], 'key.env'),
    )

    await waitFor(() =>
      expect(toast.warning).toHaveBeenCalledWith(
        'DLP labels: secret.credential',
      ),
    )
  })

  it('states the limit the engine enforces, not a larger registered one', async () => {
    show({ ...workspace, max_read_bytes: 128 * 1024 * 1024 })
    expect(await screen.findByText('Up to 64 MiB per file')).toBeInTheDocument()
  })

  it('writes a file dropped on the tree', async () => {
    show()
    const tree = await screen.findByRole('tree')
    const file = new File(['hi'], 'dropped.txt')
    fireEvent.drop(tree, { dataTransfer: { files: [file] } })

    await waitFor(() =>
      expect(agentOpsApi.writeFile).toHaveBeenCalledWith(
        'ws_1',
        'dropped.txt',
        file,
        // The file browser's upload carries no start authority.
        undefined,
      ),
    )
  })

  it('leaves an existing entry alone and says so', async () => {
    vi.mocked(agentOpsApi.statFile).mockResolvedValue(entry('README.md'))
    const user = userEvent.setup()
    show()
    await screen.findByText('README.md')
    await user.upload(
      screen.getByTestId('upload-input'),
      new File(['new'], 'README.md'),
    )

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith(
        'README.md already exists and was not replaced.',
      ),
    )
    expect(agentOpsApi.writeFile).not.toHaveBeenCalled()
  })

  it('does not send a file over the workspace size limit', async () => {
    const user = userEvent.setup()
    show()
    await screen.findByText('README.md')
    await user.upload(
      screen.getByTestId('upload-input'),
      new File(['0123456789'], 'big.bin'),
    )

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith(
        'big.bin is over the 8 B limit and was not uploaded.',
      ),
    )
    expect(agentOpsApi.statFile).not.toHaveBeenCalled()
    expect(agentOpsApi.writeFile).not.toHaveBeenCalled()
  })

  it('refuses a read-only folder with the reason on screen', async () => {
    const user = userEvent.setup()
    show(readOnly)
    await screen.findByText('README.md')

    expect(screen.getByText(READ_ONLY_REASON)).toBeInTheDocument()
    const button = screen.getByRole('button', { name: 'Upload' })
    expect(button).toHaveAttribute('aria-disabled', 'true')
    await user.click(button)

    fireEvent.drop(screen.getByRole('tree'), {
      dataTransfer: { files: [new File(['x'], 'x.txt')] },
    })
    expect(toast.error).toHaveBeenCalledWith(READ_ONLY_REASON)
    expect(agentOpsApi.writeFile).not.toHaveBeenCalled()
  })

  it('says why when the caller may not write', async () => {
    auth.write = false
    show()
    await screen.findByText('README.md')
    expect(
      screen.getByText('You do not have write access to this folder.'),
    ).toBeInTheDocument()
  })
})

describe('WorkspaceBrowser New file and New folder', () => {
  async function create(kind: 'New file' | 'New folder', name: string) {
    const user = userEvent.setup()
    show()
    await user.click(await screen.findByRole('button', { name: kind }))
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByRole('textbox'), name)
    await user.click(within(dialog).getByRole('button', { name: 'Create' }))
    return dialog
  }

  it('refuses a name the folder already holds, asked fresh, sending no write', async () => {
    // Not in the listing the tree read: only the file system can say it exists.
    vi.mocked(agentOpsApi.statFile).mockResolvedValue(entry('late.md'))
    const dialog = await create('New file', 'late.md')

    expect(await within(dialog).findByRole('alert')).toHaveTextContent(
      'late.md already exists in this folder.',
    )
    const field = within(dialog).getByRole('textbox')
    expect(field).toHaveFocus()
    expect(field).toHaveAttribute('aria-invalid', 'true')
    expect(field).toHaveAccessibleDescription(
      'late.md already exists in this folder.',
    )
    expect(vi.mocked(agentOpsApi.statFile).mock.calls[0]![1]).toBe('late.md')
    expect(agentOpsApi.writeFile).not.toHaveBeenCalled()
  })

  it('creates a new name as before', async () => {
    await create('New file', 'notes.md')

    await waitFor(() => expect(agentOpsApi.writeFile).toHaveBeenCalledTimes(1))
    const [, path, body, opts] = vi.mocked(agentOpsApi.writeFile).mock.calls[0]!
    expect(path).toBe('notes.md')
    expect(body).toBe('')
    expect(opts?.create).toBe(true)
  })

  it('creates nothing when it cannot tell whether the name is free', async () => {
    vi.mocked(agentOpsApi.statFile).mockRejectedValue(
      new ApiError(500, 'internal', 'stat failed'),
    )
    await create('New file', 'notes.md')

    await waitFor(() => expect(toast.error).toHaveBeenCalled())
    expect(agentOpsApi.writeFile).not.toHaveBeenCalled()
  })

  it('refuses a folder over a name that exists, sending no mkdir', async () => {
    vi.mocked(agentOpsApi.statFile).mockResolvedValue(entry('docs', 'dir'))
    const dialog = await create('New folder', 'docs')

    expect(await within(dialog).findByRole('alert')).toHaveTextContent(
      'docs already exists in this folder.',
    )
    expect(agentOpsApi.mkdir).not.toHaveBeenCalled()
  })

  it('says the name is taken when the engine refuses the create', async () => {
    // The name appeared after the check: the engine's create-only write refuses it.
    vi.mocked(agentOpsApi.writeFile).mockRejectedValue(
      new ApiError(409, 'conflict', 'A file with this name already exists.'),
    )
    const dialog = await create('New file', 'notes.md')

    expect(await within(dialog).findByRole('alert')).toHaveTextContent(
      'notes.md already exists in this folder.',
    )
    expect(within(dialog).getByRole('textbox')).toHaveFocus()
  })

  it('shows the engine remedy calmly if a write ever lacks its precondition', async () => {
    vi.mocked(agentOpsApi.writeFile).mockRejectedValue(
      new ApiError(428, 'precondition_required', 'Read the file first.'),
    )
    await create('New file', 'notes.md')

    await waitFor(() =>
      expect(toast.warning).toHaveBeenCalledWith(
        'This file cannot be saved until it is opened again.',
      ),
    )
    expect(toast.error).not.toHaveBeenCalled()
  })

  it('creates no folder when it cannot tell whether the name is free', async () => {
    vi.mocked(agentOpsApi.statFile).mockRejectedValue(
      new ApiError(500, 'internal', 'stat failed'),
    )
    await create('New folder', 'docs')

    await waitFor(() => expect(toast.error).toHaveBeenCalled())
    expect(agentOpsApi.mkdir).not.toHaveBeenCalled()
  })
})

describe('WorkspaceBrowser archive download', () => {
  it('downloads the ticked entries, a folder among them, as one zip', async () => {
    const user = userEvent.setup()
    show()
    await screen.findByText('README.md')
    expect(screen.queryByRole('button', { name: /^Download/ })).toBeNull()

    await user.click(screen.getByRole('checkbox', { name: 'Select docs' }))
    await user.click(screen.getByRole('checkbox', { name: 'Select README.md' }))
    await user.click(
      screen.getByRole('button', { name: 'Download selected (2)' }),
    )

    await waitFor(() =>
      expect(agentOpsApi.archiveFiles).toHaveBeenCalledWith('ws_1', [
        'docs',
        'README.md',
      ]),
    )
    await waitFor(() =>
      expect(downloadBlob).toHaveBeenCalledWith(expect.any(Blob), 'docs.zip'),
    )
  })

  it('shows the DLP labels of the files in the zip', async () => {
    vi.mocked(agentOpsApi.archiveFiles).mockResolvedValue({
      blob: new Blob(['zip']),
      filename: 'files.zip',
      labels: ['secret.credential', 'pii.email'],
    })
    const user = userEvent.setup()
    show()
    await screen.findByText('README.md')
    await user.click(screen.getByRole('checkbox', { name: 'Select README.md' }))
    await user.click(
      screen.getByRole('button', { name: 'Download selected (1)' }),
    )

    await waitFor(() =>
      expect(toast.warning).toHaveBeenCalledWith(
        'DLP labels: secret.credential, pii.email',
      ),
    )
  })

  it('drops a tick when its entry is gone or its folder was left', async () => {
    const user = userEvent.setup()
    show()
    await screen.findByText('README.md')
    await user.click(screen.getByRole('checkbox', { name: 'Select README.md' }))
    expect(
      screen.getByRole('button', { name: 'Download selected (1)' }),
    ).toBeInTheDocument()

    // The entry is deleted elsewhere: the next listing no longer has it.
    vi.mocked(agentOpsApi.listFiles).mockResolvedValue({
      path: '',
      has_more: false,
      entries: [entry('docs', 'dir')],
    })
    await user.click(screen.getByRole('button', { name: 'Refresh' }))
    await waitFor(() =>
      expect(screen.queryByText('README.md')).not.toBeInTheDocument(),
    )
    expect(
      screen.queryByRole('button', { name: /^Download selected/ }),
    ).toBeNull()
  })

  it('does not bring a tick back on returning to the folder', async () => {
    const user = userEvent.setup()
    show()
    await screen.findByText('README.md')
    await user.click(screen.getByRole('checkbox', { name: 'Select README.md' }))
    await user.click(screen.getByText('docs'))
    await screen.findByText('a.md')
    await user.click(screen.getByRole('button', { name: 'root' }))
    await screen.findByText('README.md')

    expect(
      screen.getByRole('checkbox', { name: 'Select README.md' }),
    ).not.toBeChecked()
  })

  it('downloads the folder shown when nothing is ticked', async () => {
    const user = userEvent.setup()
    show(workspace, '/workspaces?path=docs')
    await screen.findByText('a.md')
    await user.click(screen.getByRole('button', { name: 'Download folder' }))

    await waitFor(() =>
      expect(agentOpsApi.archiveFiles).toHaveBeenCalledWith('ws_1', ['docs']),
    )
  })

  it('shows the engine refusal instead of a file', async () => {
    vi.mocked(agentOpsApi.archiveFiles).mockRejectedValue(
      new ApiError(403, 'forbidden', 'archive denied by workspace DLP policy'),
    )
    const user = userEvent.setup()
    show()
    await screen.findByText('README.md')
    await user.click(screen.getByRole('checkbox', { name: 'Select README.md' }))
    await user.click(
      screen.getByRole('button', { name: 'Download selected (1)' }),
    )

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith(
        'archive denied by workspace DLP policy',
      ),
    )
    expect(downloadBlob).not.toHaveBeenCalled()
  })

  it('still downloads from a read-only folder', async () => {
    const user = userEvent.setup()
    show(readOnly)
    await screen.findByText('README.md')
    await user.click(screen.getByRole('checkbox', { name: 'Select README.md' }))
    expect(
      screen.getByRole('button', { name: 'Download selected (1)' }),
    ).toBeEnabled()
  })
})
