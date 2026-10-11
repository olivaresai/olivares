// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { DRJob } from './types'

const stream = vi.hoisted(() => ({
  snapshot: undefined as ((job: DRJob) => void) | undefined,
}))

const api = vi.hoisted(() => ({
  listBackups: vi.fn(),
  listPendingRestores: vi.fn(),
  createBackup: vi.fn(),
  approveRestore: vi.fn(),
}))

vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, drApi: { ...actual.drApi, ...api } }
})
vi.mock('@/features/shared/sse', () => ({
  useLiveStream: (options: { onSnapshot: (job: DRJob) => void }) => {
    stream.snapshot = options.onSnapshot
    return { status: 'open' as const }
  },
}))

import { BackupsView } from './backups-view'
import './i18n'

function show() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  return render(
    <QueryClientProvider client={client}>
      <BackupsView />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  stream.snapshot = undefined
  api.listBackups.mockResolvedValue({ items: [] })
  api.listPendingRestores.mockResolvedValue({ items: [] })
  api.createBackup.mockResolvedValue({ job_id: 'job-composition-1' })
  api.approveRestore.mockResolvedValue({ job_id: 'job-restore-1' })
})

describe('BackupsView composition', () => {
  it('keeps the last approved restore visible until its terminal refresh', async () => {
    api.listPendingRestores.mockResolvedValueOnce({
      items: [
        {
          request_id: 'request-1',
          upload_id: 'upload-1',
          initiator: 'other-admin',
          created_at: '2026-10-10T07:00:00Z',
        },
      ],
    })
    const user = userEvent.setup()
    show()
    await user.click(
      await screen.findByRole('button', {
        name: 'Approve & Restore',
      }),
    )
    await user.type(
      screen.getByLabelText('Passphrase', { exact: true }),
      'composition-passphrase-42',
    )
    await user.click(screen.getByRole('button', { name: 'Approve & Restore' }))
    const confirm = screen.getByRole('button', {
      name: 'Approve & Restore Now',
    })
    expect(confirm).toBeDisabled()
    await user.type(
      screen.getByRole('textbox', { name: /confirmation phrase/i }),
      'RESTORE',
    )
    await user.click(confirm)
    await waitFor(() =>
      expect(api.approveRestore).toHaveBeenCalledWith('upload-1', {
        request_id: 'request-1',
        passphrase: 'composition-passphrase-42',
      }),
    )
    // The acceptance-time query consumes the last queue row, not its running dialog.
    await waitFor(() =>
      expect(api.listPendingRestores).toHaveBeenCalledTimes(2),
    )
    expect(await screen.findByRole('progressbar')).toBeVisible()
    act(() =>
      stream.snapshot?.({
        id: 'job-restore-1',
        kind: 'restore',
        status: 'completed',
        phase: 'restart_required',
        progress: 100,
        created_at: '',
        notes: 'Restart required; previous estate preserved.',
      }),
    )
    expect(
      await screen.findByText('Restart required; previous estate preserved.'),
    ).toBeVisible()
    await waitFor(() =>
      expect(api.listPendingRestores).toHaveBeenCalledTimes(3),
    )
    await user.click(screen.getByText('Close', { selector: 'button' }))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(screen.queryByText('other-admin')).not.toBeInTheDocument()
  })

  it('shows the completed backup and its download action without reloading', async () => {
    const user = userEvent.setup()
    show()
    expect(
      await screen.findByText('No backup snapshots created yet'),
    ).toBeVisible()
    await user.click(screen.getByRole('button', { name: /create backup/i }))
    await user.type(
      await screen.findByLabelText(/passphrase/i),
      'composition-passphrase-42',
    )
    await user.click(screen.getByRole('button', { name: /start backup/i }))
    await screen.findByRole('progressbar')
    // The acceptance-time refresh has finished while the snapshot does not exist.
    expect(api.listBackups).toHaveBeenCalledTimes(2)
    expect(
      screen.queryByRole('button', { name: /^Download / }),
    ).not.toBeInTheDocument()
    api.listBackups.mockResolvedValue({
      items: [
        {
          id: 'completed-backup',
          filename: 'completed.drbundle',
          created_at: '2026-10-10T07:00:00Z',
          size_bytes: 2048,
          engine: 'sqlite',
          tenant_count: 1,
          notes: '',
        },
      ],
    })
    act(() =>
      stream.snapshot?.({
        id: 'job-composition-1',
        kind: 'backup',
        status: 'completed',
        phase: 'done',
        progress: 100,
        created_at: '',
      }),
    )
    await user.click(screen.getByText('Close', { selector: 'button' }))
    expect(
      await screen.findByRole('button', {
        name: 'Download completed.drbundle',
      }),
    ).toBeEnabled()
    expect(
      screen.queryByText('No backup snapshots created yet'),
    ).not.toBeInTheDocument()
  })

  it('opens the real trigger dialog, starts a backup, and pivots to job progress', async () => {
    const user = userEvent.setup()
    show()

    const createButton = screen.getByRole('button', { name: /create backup/i })
    expect(
      createButton,
      'Rendered: the real BackupsView must expose its backup trigger',
    ).toBeEnabled()
    await user.click(createButton)

    const passphrase = await screen.findByLabelText(/passphrase/i)
    await user.type(passphrase, 'composition-passphrase-42')
    const start = screen.getByRole('button', { name: /start backup/i })
    expect(
      start,
      'Rendered: the parent-triggered dialog must expose the enabled start action',
    ).toBeEnabled()
    await user.click(start)

    await waitFor(() =>
      expect(
        api.createBackup,
        'Fired: the parent composition must dispatch drApi.createBackup',
      ).toHaveBeenCalledWith({
        notes: '',
        passphrase: 'composition-passphrase-42',
      }),
    )
    expect(
      await screen.findByRole('progressbar'),
      'Effect: the accepted backup job must replace the form with live progress',
    ).toBeVisible()
  })
})
