// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const api = vi.hoisted(() => ({
  listBackups: vi.fn(),
  listPendingRestores: vi.fn(),
  createBackup: vi.fn(),
}))

vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, drApi: { ...actual.drApi, ...api } }
})
vi.mock('@/features/shared/sse', () => ({
  useLiveStream: () => ({ status: 'connected' as const }),
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
  api.listBackups.mockResolvedValue({ items: [] })
  api.listPendingRestores.mockResolvedValue({ items: [] })
  api.createBackup.mockResolvedValue({ job_id: 'job-composition-1' })
})

describe('BackupsView composition', () => {
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
