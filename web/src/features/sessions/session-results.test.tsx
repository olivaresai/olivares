// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The files changed in a session's folder since it started, each with Download, and
// Download all.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const auth = vi.hoisted(() => ({ readFolders: true }))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 't1',
    can: (permission: string) =>
      permission !== 'sessions:workspace:read' || auth.readFolders,
  }),
}))
vi.mock('@/features/agentops/api', async (orig) => ({
  ...(await orig<typeof import('@/features/agentops/api')>()),
  agentOpsApi: {
    changes: vi.fn(),
    readFile: vi.fn(),
    archiveFiles: vi.fn(),
  },
}))
// The run's own file read is a raw download through the client's one raw request.
vi.mock('@/lib/api/client', async (orig) => ({
  ...(await orig<typeof import('@/lib/api/client')>()),
  apiFetchRaw: vi.fn(),
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
import { agentOpsApi } from '@/features/agentops/api'
import type { RunChangesDTO } from '@/features/agentops/types'
import { apiFetchRaw, http } from '@/lib/api/client'
import { downloadBlob } from '@/lib/api/download'
import { ApiError, NetworkError } from '@/lib/api/errors'
import { SessionResults } from './session-results'

const RUN = { run_ref: 'run_1', workspace_ref: 'ws_1' }
// Every file changed in the folder since the run began, whoever changed it.
const TITLE = 'Files changed in this folder since the session started'

function changes(files: RunChangesDTO['files'], truncated = false) {
  vi.mocked(agentOpsApi.changes).mockResolvedValue({
    folder: 'Facturas 2026',
    files,
    truncated,
  })
}

const INFORME = {
  path: 'informe.xlsx',
  size: 24 * 1024,
  modified_at: '2026-11-10T10:42:00Z',
}

function show(run: Parameters<typeof SessionResults>[0]['run'] = RUN) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <SessionResults run={run} />
    </QueryClientProvider>,
  )
}

/** The one error toast says `sentence`, drawn as the page draws it, and names the file by
 * its path as the page shows a path: whole on hover. */
async function refusedOnce(sentence: string, path: string) {
  await waitFor(() => expect(toast.error).toHaveBeenCalledTimes(1))
  const message = vi.mocked(toast.error).mock.calls[0][0] as ReactNode
  const said = render(<>{message}</>, {
    container: document.createElement('div'),
  }).container
  expect(said.textContent).toBe(sentence)
  // The message is drawn apart from the page, so no node of it is in the document:
  // the path's element is pinned by its hover and its text, never by its place.
  expect(within(said).getByTitle(path)).toHaveTextContent(path)
  // The toast is drawn on the inverse surface: the path takes the toast's own words.
  expect(said.querySelector('[data-slot="path-text"]')).toHaveClass(
    'text-inherit',
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  auth.readFolders = true
  // Today's engine: no outputs read, so the list is the changes read.
  vi.spyOn(http, 'get').mockRejectedValue(
    new ApiError(404, 'not_found', 'No route is registered at that path.'),
  )
})

describe('the results of a session', () => {
  it('says it is reading, then lists each file with its size and downloads its bytes', async () => {
    const user = userEvent.setup()
    changes([INFORME])
    vi.mocked(agentOpsApi.readFile).mockResolvedValue({
      path: 'informe.xlsx',
      size: 3,
      encoding: 'base64',
      content: 'AAEC',
    })
    show()
    expect(screen.getByRole('heading', { name: TITLE })).toBeInTheDocument()
    expect(screen.getByRole('status')).toHaveTextContent(
      'Reading the files changed in this folder…',
    )
    const list = await screen.findByRole('list', { name: TITLE })
    expect(within(list).getByRole('listitem')).toHaveTextContent(
      'informe.xlsx24 KiBDownload',
    )
    await user.click(
      within(list).getByRole('button', { name: 'Download informe.xlsx' }),
    )
    await waitFor(() => expect(downloadBlob).toHaveBeenCalledTimes(1))
    expect(agentOpsApi.readFile).toHaveBeenCalledWith('ws_1', 'informe.xlsx')
    const [blob, name] = vi.mocked(downloadBlob).mock.calls[0]
    expect(name).toBe('informe.xlsx')
    expect(blob.size).toBe(3)
  })

  it('says, politely, which file a Download saved', async () => {
    const user = userEvent.setup()
    changes([INFORME])
    vi.mocked(agentOpsApi.readFile).mockResolvedValue({
      path: 'informe.xlsx',
      size: 3,
      encoding: 'base64',
      content: 'AAEC',
    })
    show()
    await user.click(
      await screen.findByRole('button', { name: 'Download informe.xlsx' }),
    )
    const said = await screen.findByRole('status')
    expect(said.textContent).toBe('Downloaded informe.xlsx.')
    expect(within(said).getByTitle('informe.xlsx')).toHaveTextContent(
      'informe.xlsx',
    )
  })

  it('shows each listed path as the console shows a path: whole on hover', async () => {
    const DEEP = 'informes/2026/trimestre-cuarto/resumen-de-facturas.xlsx'
    changes([{ ...INFORME, path: DEEP }])
    show()
    const list = await screen.findByRole('list', { name: TITLE })
    expect(within(list).getByTitle(DEEP)).toHaveTextContent(DEEP)
  })

  it('downloads every listed file as one archive under the name the engine gives it', async () => {
    const user = userEvent.setup()
    changes([INFORME, { ...INFORME, path: 'notas/resumen.md', size: 10 }])
    const zip = new Blob(['zip'])
    vi.mocked(agentOpsApi.archiveFiles).mockResolvedValue({
      blob: zip,
      filename: 'Facturas 2026.zip',
      labels: [],
    })
    show()
    await user.click(
      await screen.findByRole('button', { name: 'Download all' }),
    )
    await waitFor(() =>
      expect(downloadBlob).toHaveBeenCalledWith(zip, 'Facturas 2026.zip'),
    )
    expect(agentOpsApi.archiveFiles).toHaveBeenCalledWith('ws_1', [
      'informe.xlsx',
      'notas/resumen.md',
    ])
  })

  it('says when no file has changed in the folder yet, and what will appear', async () => {
    changes([])
    show()
    expect(
      await screen.findByText('No file has changed in this folder yet'),
    ).toBeInTheDocument()
    expect(
      screen.getByText(
        'Files changed in this folder after the session started, by the session or anyone else, appear here to download.',
      ),
    ).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Download all' })).toBeNull()
  })

  it('says the list may be cut short, and that Download saves only the listed files', async () => {
    changes([INFORME, { ...INFORME, path: 'notas/resumen.md', size: 10 }], true)
    show()
    expect(
      await screen.findByText(
        'This list may not hold every changed file: the folder is too large to list whole. Download saves only the files listed here.',
      ),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: 'Download the 2 listed files' }),
    ).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Download all' })).toBeNull()
  })

  it('says the data-protection labels of a file it downloads, as Download all does', async () => {
    const user = userEvent.setup()
    changes([INFORME])
    vi.mocked(agentOpsApi.readFile).mockResolvedValue({
      path: 'informe.xlsx',
      size: 3,
      encoding: 'base64',
      content: 'AAEC',
      sensitivity: [{ class: 'pii' }, { class: 'pii' }, { class: 'secret' }],
    })
    show()
    await user.click(
      await screen.findByRole('button', { name: 'Download informe.xlsx' }),
    )
    await waitFor(() => expect(downloadBlob).toHaveBeenCalledTimes(1))
    expect(toast.warning).toHaveBeenCalledWith('DLP labels: pii, secret')
  })

  it("a download that never reached the engine says so in the console's own words", async () => {
    const user = userEvent.setup()
    changes([INFORME])
    vi.mocked(agentOpsApi.readFile).mockRejectedValue(
      new NetworkError('Failed to fetch'),
    )
    show()
    await user.click(
      await screen.findByRole('button', { name: 'Download informe.xlsx' }),
    )
    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith('The download did not finish.'),
    )
    expect(downloadBlob).not.toHaveBeenCalled()
  })

  it('a read that failed says so with Retry, never that there is nothing', async () => {
    const user = userEvent.setup()
    vi.mocked(agentOpsApi.changes).mockRejectedValueOnce(
      new ApiError(500, 'internal', 'boom'),
    )
    show()
    const retry = await screen.findByRole('button', { name: 'Retry' })
    expect(
      screen.queryByText('No file has changed in this folder yet'),
    ).toBeNull()
    changes([INFORME])
    await user.click(retry)
    expect(await screen.findByRole('list', { name: TITLE })).toBeInTheDocument()
  })

  it('a person the engine refuses is told they have no access, with nothing to download', async () => {
    vi.mocked(agentOpsApi.changes).mockRejectedValue(
      new ApiError(403, 'forbidden', 'forbidden'),
    )
    show()
    expect(
      await screen.findByText(
        'You do not have access to the files changed in this folder.',
      ),
    ).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Download/ })).toBeNull()
  })

  it('a person who may not read the folder sees the files and why there is no Download', async () => {
    auth.readFolders = false
    changes([INFORME])
    show()
    await screen.findByRole('list', { name: TITLE })
    expect(
      screen.getByText(
        "Downloading them needs access to the folder's files. An administrator can give it.",
      ),
    ).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Download/ })).toBeNull()
  })

  it('a file the folder lets the console read only in part is not saved shortened', async () => {
    const user = userEvent.setup()
    changes([INFORME])
    vi.mocked(agentOpsApi.readFile).mockResolvedValue({
      path: 'informe.xlsx',
      size: 1,
      encoding: 'base64',
      content: 'AA==',
      truncated: true,
    })
    show()
    await user.click(
      await screen.findByRole('button', { name: 'Download informe.xlsx' }),
    )
    await refusedOnce(
      "informe.xlsx is larger than this folder lets the console read, so it was not downloaded. Ask an administrator to raise the folder's read limit.",
      'informe.xlsx',
    )
    expect(downloadBlob).not.toHaveBeenCalled()
    // Nothing was saved, so nothing says it was.
    expect(screen.queryByText(/^Downloaded/)).toBeNull()
  })

  it.each([
    ['a folder of its own the engine made', { workspace_ref: '' }],
    ['a worktree of its own', { worktree_branch: 'task/x' }],
  ])(
    'offers nothing for a session in %s on an engine without the run outputs read, which the folder reads do not reach',
    async (_, run) => {
      changes([INFORME])
      show({ ...RUN, ...run })
      expect(screen.queryByRole('status')).toBeNull()
      await waitFor(() => expect(http.get).toHaveBeenCalledTimes(1))
      expect(screen.queryByRole('heading', { name: TITLE })).toBeNull()
      expect(agentOpsApi.changes).not.toHaveBeenCalled()
    },
  )
})

describe('on an engine with the run outputs read', () => {
  const OUTPUT = {
    name: 'informe.xlsx',
    size: 24 * 1024,
    modified_at: '2026-11-10T10:42:00Z',
  }
  const outputs = (files = [OUTPUT]) =>
    vi.spyOn(http, 'get').mockResolvedValue({ files, truncated: false })
  const NO_OWN = "This server cannot download this session's files yet."
  const GONE =
    'informe.xlsx is no longer in this folder, so it was not downloaded. The list shows the folder as it is now.'
  const REFUSED =
    'The server refused to download informe.xlsx from this folder.'
  const OUTSIDE =
    'informe.xlsx is not a file inside this folder, so it was not downloaded.'

  it('lists what the outputs read gives, and reads the changes nowhere', async () => {
    outputs()
    show()
    const list = await screen.findByRole('list', { name: TITLE })
    expect(within(list).getByRole('listitem')).toHaveTextContent(
      'informe.xlsx24 KiB',
    )
    expect(http.get).toHaveBeenCalledWith(
      '/v1/m/sessions/runs/run_1/outputs',
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    )
    expect(agentOpsApi.changes).not.toHaveBeenCalled()
  })

  it('an engine without it (404 not_found) is asked again by every session: nothing is remembered', async () => {
    changes([INFORME])
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    const results = (runRef: string) => (
      <QueryClientProvider client={qc}>
        <SessionResults run={{ ...RUN, run_ref: runRef }} />
      </QueryClientProvider>
    )
    render(results('run_1'))
    await screen.findByRole('list', { name: TITLE })
    render(results('run_2'))
    await waitFor(() => expect(agentOpsApi.changes).toHaveBeenCalledTimes(2))
    expect(http.get).toHaveBeenCalledTimes(2)
    expect(http.get).toHaveBeenLastCalledWith(
      '/v1/m/sessions/runs/run_2/outputs',
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    )
  })

  it("a 404 module_error on the outputs read is the engine's answer about the session, not a missing read: it says so with Retry and reads no changes", async () => {
    vi.spyOn(http, 'get').mockRejectedValue(
      new ApiError(404, 'module_error', 'session not found'),
    )
    show()
    await screen.findByRole('button', { name: 'Retry' })
    expect(agentOpsApi.changes).not.toHaveBeenCalled()
  })

  it.each([
    ['a folder of its own the engine made', { workspace_ref: '' }],
    ['a worktree of its own', { worktree_branch: 'task/x' }],
  ])(
    'a session in %s whose outputs read failed says what could not load, with Retry',
    async (_, run) => {
      const user = userEvent.setup()
      vi.spyOn(http, 'get')
        .mockRejectedValueOnce(new ApiError(500, 'internal', 'boom'))
        .mockResolvedValue({ files: [OUTPUT], truncated: false })
      show({ ...RUN, ...run })
      const retry = await screen.findByRole('button', { name: 'Retry' })
      expect(screen.getByRole('heading', { name: TITLE })).toBeInTheDocument()
      await user.click(retry)
      expect(
        await screen.findByRole('list', { name: TITLE }),
      ).toBeInTheDocument()
    },
  )

  it('a person the engine refuses the outputs of a session of its own is told they have no access', async () => {
    vi.spyOn(http, 'get').mockRejectedValue(
      new ApiError(403, 'forbidden', 'forbidden'),
    )
    show({ ...RUN, workspace_ref: '' })
    expect(
      await screen.findByText(
        'You do not have access to the files changed in this folder.',
      ),
    ).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Download/ })).toBeNull()
  })

  it("a session in a folder of its own saves a file through the run's own read, under its last name", async () => {
    const user = userEvent.setup()
    outputs([{ ...OUTPUT, name: 'out/informe.xlsx' }])
    const bytes = new Blob(['abc'])
    vi.mocked(apiFetchRaw).mockResolvedValue({
      blob: () => Promise.resolve(bytes),
    } as unknown as Response)
    show({ ...RUN, workspace_ref: '' })
    await user.click(
      await screen.findByRole('button', { name: 'Download out/informe.xlsx' }),
    )
    await waitFor(() =>
      expect(downloadBlob).toHaveBeenCalledWith(bytes, 'informe.xlsx'),
    )
    expect(apiFetchRaw).toHaveBeenCalledWith(
      '/v1/m/sessions/runs/run_1/outputs/file',
      { query: { path: 'out/informe.xlsx' } },
    )
    expect(agentOpsApi.readFile).not.toHaveBeenCalled()
    expect(screen.queryByRole('button', { name: 'Download all' })).toBeNull()
  })

  it("a session in the registered folder downloads from that folder, never through the run's own read", async () => {
    const user = userEvent.setup()
    outputs()
    vi.mocked(agentOpsApi.readFile).mockResolvedValue({
      path: 'informe.xlsx',
      size: 3,
      encoding: 'base64',
      content: 'AAEC',
    })
    show()
    await user.click(
      await screen.findByRole('button', { name: 'Download informe.xlsx' }),
    )
    await waitFor(() => expect(downloadBlob).toHaveBeenCalledTimes(1))
    expect(agentOpsApi.readFile).toHaveBeenCalledWith('ws_1', 'informe.xlsx')
    expect(apiFetchRaw).not.toHaveBeenCalled()
  })

  it.each([
    ['a folder of its own the engine made', { workspace_ref: '' }],
    ['a worktree of its own', { worktree_branch: 'task/x' }],
  ])(
    "a session in %s the engine refuses with 409 says so, and never reads the registered folder's copy",
    async (_, run) => {
      const user = userEvent.setup()
      outputs()
      vi.mocked(apiFetchRaw).mockRejectedValue(
        new ApiError(
          409,
          'module_error',
          'this session works in a registered folder; download its files from the folder',
        ),
      )
      show({ ...RUN, ...run })
      await user.click(
        await screen.findByRole('button', { name: 'Download informe.xlsx' }),
      )
      await refusedOnce(REFUSED, 'informe.xlsx')
      expect(agentOpsApi.readFile).not.toHaveBeenCalled()
      expect(downloadBlob).not.toHaveBeenCalled()
    },
  )

  it.each(['module_error', 'output_not_found'])(
    'a file gone since the list was read (404 %s) is said under the list, the list is read again, and Download still works for the other rows and sessions',
    async (code) => {
      const user = userEvent.setup()
      const NOTAS = { ...OUTPUT, name: 'notas.md', size: 10 }
      vi.spyOn(http, 'get')
        .mockResolvedValueOnce({ files: [OUTPUT, NOTAS], truncated: false })
        .mockResolvedValue({ files: [NOTAS], truncated: false })
      const bytes = new Blob(['abc'])
      vi.mocked(apiFetchRaw)
        .mockRejectedValueOnce(
          new ApiError(404, code, "no such file in the session's folder"),
        )
        .mockResolvedValue({
          blob: () => Promise.resolve(bytes),
        } as unknown as Response)
      const qc = new QueryClient({
        defaultOptions: { queries: { retry: false } },
      })
      const own = (runRef: string) => (
        <QueryClientProvider client={qc}>
          <SessionResults run={{ run_ref: runRef, workspace_ref: '' }} />
        </QueryClientProvider>
      )
      const first = within(render(own('run_1')).container)
      await user.click(
        await first.findByRole('button', { name: 'Download informe.xlsx' }),
      )
      // The sentence whole, its path through the console's path: whole on hover.
      const said = await first.findByRole('alert')
      expect(said.textContent).toBe(GONE)
      expect(within(said).getByTitle('informe.xlsx')).toBeInTheDocument()
      // The path reads in the sentence's own colour.
      expect(said.querySelector('[data-slot="path-text"]')).toHaveClass(
        'text-inherit',
      )
      await waitFor(() =>
        expect(
          first.queryByRole('button', { name: 'Download informe.xlsx' }),
        ).toBeNull(),
      )
      expect(http.get).toHaveBeenCalledTimes(2)
      expect(toast.error).not.toHaveBeenCalled()
      const notas = first.getByRole('button', { name: 'Download notas.md' })
      await waitFor(() => expect(notas).toBeEnabled())
      await user.click(notas)
      await waitFor(() =>
        expect(downloadBlob).toHaveBeenCalledWith(bytes, 'notas.md'),
      )
      const second = within(render(own('run_2')).container)
      await user.click(
        await second.findByRole('button', { name: 'Download notas.md' }),
      )
      await waitFor(() => expect(downloadBlob).toHaveBeenCalledTimes(2))
      expect(apiFetchRaw).toHaveBeenLastCalledWith(
        '/v1/m/sessions/runs/run_2/outputs/file',
        { query: { path: 'notas.md' } },
      )
    },
  )

  it("an engine without the run's file read (404 not_found) says it cannot download, and the next Download asks again", async () => {
    const user = userEvent.setup()
    outputs()
    vi.mocked(apiFetchRaw).mockRejectedValue(
      new ApiError(404, 'not_found', 'No route is registered at that path.'),
    )
    show({ ...RUN, workspace_ref: '' })
    const download = await screen.findByRole('button', {
      name: 'Download informe.xlsx',
    })
    await user.click(download)
    await waitFor(() => expect(toast.error).toHaveBeenCalledWith(NO_OWN))
    await waitFor(() => expect(download).toBeEnabled())
    await user.click(download)
    await waitFor(() => expect(apiFetchRaw).toHaveBeenCalledTimes(2))
    expect(http.get).toHaveBeenCalledTimes(1)
    expect(screen.queryByRole('alert')).toBeNull()
    expect(downloadBlob).not.toHaveBeenCalled()
  })

  it("a path the engine does not take as inside the folder (400) is said in the page's words, never the engine's", async () => {
    const user = userEvent.setup()
    outputs()
    vi.mocked(apiFetchRaw).mockRejectedValue(
      new ApiError(
        400,
        'module_error',
        "path must name a file inside the session's folder",
      ),
    )
    show({ ...RUN, workspace_ref: '' })
    await user.click(
      await screen.findByRole('button', { name: 'Download informe.xlsx' }),
    )
    await refusedOnce(OUTSIDE, 'informe.xlsx')
  })
})
