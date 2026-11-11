// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ComponentProps } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('@tanstack/react-router', async () =>
  (await import('@/test/fake-router')).fakeRouterModule(),
)
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))
vi.mock('@/features/agentops/api', async (orig) => ({
  ...(await orig<typeof import('@/features/agentops/api')>()),
  agentOpsApi: { readFile: vi.fn(), statFile: vi.fn(), moveFile: vi.fn() },
}))

vi.mock('@/lib/api/download', async (orig) => ({
  ...(await orig<typeof import('@/lib/api/download')>()),
  downloadBlob: vi.fn(),
}))

import { downloadBlob } from '@/lib/api/download'
import { ApiError } from '@/lib/api/errors'
import { agentOpsApi } from './api'
import './i18n'
import type { FileReadResponse, WorkspaceDTO } from './types'
import { FileViewer } from './workspace-file-viewer'

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

function binary(path: string, content: string): FileReadResponse {
  return { path, size: content.length, encoding: 'base64', content }
}

type ViewerProps = Partial<ComponentProps<typeof FileViewer>> & {
  path: string
  file: FileReadResponse
}

function viewerTree(qc: QueryClient, props: ViewerProps) {
  return (
    <QueryClientProvider client={qc}>
      <FileViewer
        workspace={workspace}
        writable={false}
        editing={false}
        editValue=""
        onEdit={() => {}}
        onCancel={() => {}}
        onChange={() => {}}
        onSave={() => {}}
        saving={false}
        onDeleted={() => {}}
        onMoved={() => {}}
        {...props}
      />
    </QueryClientProvider>
  )
}

function show(props: ViewerProps) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const view = render(viewerTree(qc, props))
  return {
    ...view,
    update: (next: ViewerProps) => view.rerender(viewerTree(qc, next)),
  }
}

beforeEach(() => {
  vi.mocked(downloadBlob).mockClear()
  vi.mocked(agentOpsApi.statFile).mockClear()
  vi.mocked(agentOpsApi.moveFile).mockClear()
  vi.mocked(agentOpsApi.readFile).mockImplementation(async (_ws, path) =>
    text(path, 'committed\n'),
  )
})

describe('FileViewer rendered documents', () => {
  it('renders an .htm file in a frame with no script and no origin', async () => {
    show({ path: 'page.htm', file: text('page.htm', '<h1>Page</h1>') })

    const frame = await screen.findByTitle('HTML preview: page.htm')
    expect(frame).toHaveAttribute('sandbox', '')
    expect(frame).toHaveAttribute('srcdoc', expect.stringContaining('<h1>'))
    expect(frame).not.toHaveAttribute('src')
  })

  it('renders a .markdown file as Markdown', async () => {
    show({
      path: 'notes.markdown',
      file: text('notes.markdown', '# Guide\n'),
    })

    expect(
      await screen.findByRole('heading', { name: 'Guide', level: 2 }),
    ).toBeInTheDocument()
    expect(screen.queryByRole('textbox')).toBeNull()
  })

  it('never paints HTML written inside Markdown as elements', async () => {
    const doc = '# Guide\n\n<img src="x" alt="planted" onerror="alert(1)">\n'
    show({ path: 'a.md', file: text('a.md', doc) })

    expect(
      await screen.findByRole('heading', { name: 'Guide', level: 2 }),
    ).toBeInTheDocument()
    expect(screen.queryByRole('img', { name: 'planted' })).toBeNull()
  })

  it('offers a PDF by its name and size to download, never framed', async () => {
    const pdf = binary('docs/guide.pdf', btoa('%PDF-1.7 hello'))
    const { container } = show({
      path: 'docs/guide.pdf',
      file: { ...pdf, size: 2048 },
    })

    const note = screen.getByText(
      'The console does not preview PDF files. Download the file to read it.',
    ).parentElement!
    expect(within(note).getByTitle('docs/guide.pdf')).toBeVisible()
    expect(within(note).getByText('2 KiB')).toBeVisible()
    expect(container.querySelector('iframe, embed, object')).toBeNull()
    expect(downloadBlob).not.toHaveBeenCalled()

    const user = userEvent.setup()
    await user.click(within(note).getByRole('button', { name: 'Download' }))
    expect(downloadBlob).toHaveBeenCalledTimes(1)
    const [blob, name] = vi.mocked(downloadBlob).mock.calls[0]!
    expect(name).toBe('guide.pdf')
    expect(blob.type).toBe('application/octet-stream')
    expect(blob.size).toBe('%PDF-1.7 hello'.length)
  })

  it('never offers a PDF cut at the read limit as if it were whole', () => {
    const pdf = binary('docs/guide.pdf', btoa('%PDF-1.7 cut'))
    const { container } = show({
      path: 'docs/guide.pdf',
      file: { ...pdf, size: 12 << 20, truncated: true },
    })

    // The toolbar names the path too: the box is the one that says the limit.
    const limit = screen.getByText(/read limit of 1 MiB/)
    expect(limit).toBeVisible()
    const note = limit.parentElement!
    expect(within(note).getByTitle('docs/guide.pdf')).toBeVisible()
    expect(within(note).getByText('12 MiB')).toBeVisible()
    expect(screen.queryByRole('button', { name: 'Download' })).toBeNull()
    expect(container.querySelector('iframe, embed, object')).toBeNull()
  })
})

describe('FileViewer download', () => {
  it('offers no Download for a text file cut at the read limit', () => {
    show({
      path: 'big.log',
      file: {
        ...text('big.log', 'first lines'),
        size: 9 << 20,
        truncated: true,
      },
    })

    expect(screen.getByText(/read limit of 1 MiB/)).toBeVisible()
    expect(screen.queryByRole('button', { name: 'Download' })).toBeNull()
    expect(screen.queryByRole('button', { name: /more/i })).toBeNull()
  })
})

describe('FileViewer writes', () => {
  const cut = {
    ...text('big.md', '# The first part'),
    size: 9 << 20,
    truncated: true,
  }

  it('offers no Edit for a file cut at the read limit, so nothing replaces it', () => {
    const onEdit = vi.fn()
    show({ path: 'big.md', file: cut, writable: true, onEdit })

    expect(screen.getByText(/read limit of 1 MiB/)).toBeVisible()
    expect(screen.queryByRole('button', { name: 'Edit' })).toBeNull()
    expect(onEdit).not.toHaveBeenCalled()
  })

  it('offers no Save for a cut file even with a draft open', () => {
    const onSave = vi.fn()
    show({ path: 'big.md', file: cut, writable: true, editing: true, onSave })

    expect(screen.queryByRole('button', { name: 'Save' })).toBeNull()
    expect(screen.getByRole('button', { name: 'Cancel' })).toBeVisible()
    expect(onSave).not.toHaveBeenCalled()
  })

  it('keeps Move and Delete in the menu of a cut file, but no Download', async () => {
    const user = userEvent.setup()
    show({ path: 'big.md', file: cut, writable: true })

    await user.click(screen.getByRole('button', { name: 'More actions' }))
    const menu = await screen.findByRole('menu')
    expect(within(menu).getByRole('menuitem', { name: /Move/ })).toBeVisible()
    expect(within(menu).getByRole('menuitem', { name: 'Delete' })).toBeVisible()
    expect(
      within(menu).queryByRole('menuitem', { name: 'Download' }),
    ).toBeNull()
  })
})

describe('FileViewer move', () => {
  it('refuses to move onto a name that exists, sending no move', async () => {
    vi.mocked(agentOpsApi.statFile).mockResolvedValue({
      name: 'b.md',
      path: 'b.md',
      type: 'file',
      size: 1,
      mode: '0644',
      mtime: '2026-11-11T00:00:00Z',
    })
    const user = userEvent.setup()
    show({ path: 'a.md', file: text('a.md', '# A\n'), writable: true })
    await user.click(screen.getByRole('button', { name: /Move/ }))
    const dialog = await screen.findByRole('dialog')
    const to = within(dialog).getByRole('textbox')
    await user.clear(to)
    await user.type(to, 'b.md')
    await user.click(within(dialog).getByRole('button', { name: /Move/ }))

    expect(await within(dialog).findByRole('alert')).toHaveTextContent(
      'b.md already exists in this folder.',
    )
    expect(to).toHaveFocus()
    expect(to).toHaveAttribute('aria-invalid', 'true')
    expect(to).toHaveAccessibleDescription(
      'b.md already exists in this folder.',
    )
    expect(agentOpsApi.moveFile).not.toHaveBeenCalled()
  })

  it('says the name is taken when the engine refuses the move', async () => {
    vi.mocked(agentOpsApi.statFile).mockRejectedValue(
      new ApiError(404, 'not_found', 'missing'),
    )
    vi.mocked(agentOpsApi.moveFile).mockRejectedValue(
      new ApiError(409, 'conflict', 'A file with this name already exists.'),
    )
    const user = userEvent.setup()
    show({ path: 'a.md', file: text('a.md', '# A\n'), writable: true })
    await user.click(screen.getByRole('button', { name: /Move/ }))
    const dialog = await screen.findByRole('dialog')
    const to = within(dialog).getByRole('textbox')
    await user.clear(to)
    await user.type(to, 'b.md')
    await user.click(within(dialog).getByRole('button', { name: /Move/ }))

    expect(await within(dialog).findByRole('alert')).toHaveTextContent(
      'b.md already exists in this folder.',
    )
    expect(to).toHaveFocus()
  })

  it('moves nothing when it cannot tell whether the name is free', async () => {
    vi.mocked(agentOpsApi.statFile).mockRejectedValue(
      new ApiError(500, 'internal', 'stat failed'),
    )
    const user = userEvent.setup()
    show({ path: 'a.md', file: text('a.md', '# A\n'), writable: true })
    await user.click(screen.getByRole('button', { name: /Move/ }))
    const dialog = await screen.findByRole('dialog')
    const to = within(dialog).getByRole('textbox')
    await user.clear(to)
    await user.type(to, 'b.md')
    const move = within(dialog).getByRole('button', { name: /Move/ })
    await user.click(move)

    await waitFor(() => expect(agentOpsApi.statFile).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(move).toBeEnabled())
    expect(agentOpsApi.moveFile).not.toHaveBeenCalled()
  })
})

describe('FileViewer source view', () => {
  it('counts the lines of a whole text file beside its path', () => {
    show({ path: 'a.txt', file: text('a.txt', 'one\ntwo\nthree\n') })
    expect(screen.getByText('Lines: 3')).toBeVisible()
  })

  it('counts no lines of a file cut at the read limit', () => {
    const cut = { ...text('a.txt', 'one\ntwo'), size: 9 << 20, truncated: true }
    show({ path: 'a.txt', file: cut })
    expect(screen.getByText(/read limit of 1 MiB/)).toBeVisible()
    expect(screen.queryByText(/^Lines:/)).toBeNull()
  })

  it('counts no lines of a binary file', () => {
    show({ path: 'blob.bin', file: binary('blob.bin', 'AAECAw==') })
    expect(screen.getByText(/Binary file/)).toBeVisible()
    expect(screen.queryByText(/^Lines:/)).toBeNull()
  })

  it('numbers the lines of a read-only file', async () => {
    show({ path: 'a.txt', file: text('a.txt', 'one\ntwo\nthree') })

    expect(await screen.findByText('3')).toBeInTheDocument()
  })
})
