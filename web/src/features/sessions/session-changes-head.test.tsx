// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The Session changes pane against git HEAD: a changed file opens as its
// current text, with a Changes view of what differs from HEAD when HEAD has the file.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))
vi.mock('@/features/agentops/api', async (orig) => ({
  ...(await orig<typeof import('@/features/agentops/api')>()),
  agentOpsApi: { changes: vi.fn(), changedFile: vi.fn() },
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

function show() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <SessionChanges run={run} />
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
    expect(await screen.findByTestId('changes-file')).toHaveTextContent(
      'changed by the agent',
    )

    await user.click(
      await screen.findByRole('button', { name: 'Changes since HEAD' }),
    )
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
      await screen.findByText('Could not compare with HEAD.'),
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

  it('opens the next file on its plain view, not on the Changes view of the last', async () => {
    vi.mocked(agentOpsApi.changedFile).mockImplementation(
      async (_run, path, opts) =>
        file(path, opts?.rev === 'HEAD' ? 'before\n' : 'after\n'),
    )
    const user = userEvent.setup()
    show()
    await user.click(await screen.findByText('README.md'))
    await user.click(
      await screen.findByRole('button', { name: 'Changes since HEAD' }),
    )
    expect(screen.getByTestId('code-diff')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'All changes' }))
    await user.click(await screen.findByText('new.txt'))
    expect(
      await screen.findByRole('button', { name: 'Changes since HEAD' }),
    ).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'File' })).toHaveAttribute(
      'aria-pressed',
      'true',
    )
    expect(screen.queryByTestId('code-diff')).toBeNull()
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
