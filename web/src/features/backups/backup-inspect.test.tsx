// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// E4d — the Inspect action is real: the sheet fetches getBackup(id) and
// renders the decoded manifest (engine, tenants, sealed key names) verbatim.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactElement } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const { api } = vi.hoisted(() => ({
  api: {
    getBackup: vi.fn(),
  },
}))

vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, drApi: { ...actual.drApi, ...api } }
})

import { BackupInspectSheet } from './backup-inspect-sheet'
import { BackupList } from './backup-list'

function wrap(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => vi.clearAllMocks())

const detail = {
  id: 'bk_1',
  filename: 'olivares-2026-07-18.drbundle',
  size_bytes: 2048,
  manifest: {
    engine: 'sqlite',
    created_at: '2026-07-18T09:00:00Z',
    tenants: [
      {
        tenant: 'tenant-a',
        head_seq: 2,
        head_hash: 'aa',
        checkpoints: 1,
        verified_at_backup: true,
      },
      {
        tenant: 'tenant-b',
        head_seq: 0,
        head_hash: '',
        checkpoints: 0,
        verified_at_backup: true,
      },
    ],
    keys: [
      { file: 'audit.enc', name: 'audit-signing', role: 'audit' },
      { file: 'license.enc', name: 'license', role: 'other' },
    ],
  },
}

describe('BackupInspectSheet', () => {
  it('fetches and renders the manifest for the inspected backup', async () => {
    api.getBackup.mockResolvedValue(detail)
    wrap(<BackupInspectSheet backupId="bk_1" onClose={vi.fn()} />)

    await waitFor(() => expect(api.getBackup).toHaveBeenCalledWith('bk_1'))
    expect(
      await screen.findByText('olivares-2026-07-18.drbundle'),
    ).toBeInTheDocument()
    expect(screen.getByText('sqlite')).toBeInTheDocument()
    expect(screen.getByText('tenant-a')).toBeInTheDocument()
    expect(screen.getByText('tenant-b')).toBeInTheDocument()
    expect(screen.getByText('audit-signing')).toBeInTheDocument()
    expect(screen.getByText('2.0 KB')).toBeInTheDocument()
  })

  it('fetches nothing while closed', () => {
    wrap(<BackupInspectSheet backupId={null} onClose={vi.fn()} />)
    expect(api.getBackup).not.toHaveBeenCalled()
  })
})

describe('BackupList inspect action', () => {
  it('clicking the Eye opens the manifest sheet for that row', async () => {
    api.getBackup.mockResolvedValue(detail)
    const user = userEvent.setup()
    wrap(
      <BackupList
        data={[
          {
            id: 'bk_1',
            filename: 'olivares-2026-07-18.drbundle',
            size_bytes: 2048,
            created_at: '2026-07-18T09:00:00Z',
            engine: 'sqlite',
            tenant_count: 2,
            notes: '',
          },
        ]}
        isLoading={false}
        onRetry={vi.fn()}
      />,
    )

    await user.click(
      screen.getByRole('button', {
        name: 'Inspect olivares-2026-07-18.drbundle',
      }),
    )
    await waitFor(() => expect(api.getBackup).toHaveBeenCalledWith('bk_1'))
    expect(await screen.findByText('tenant-a')).toBeInTheDocument()
  })
})
