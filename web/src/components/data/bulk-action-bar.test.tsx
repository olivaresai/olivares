// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const toast = vi.hoisted(() => ({
  success: vi.fn(),
  error: vi.fn(),
  warning: vi.fn(),
}))
vi.mock('@/components/ui/toaster', () => ({ toast }))

import { BulkActionBar } from './bulk-action-bar'

beforeEach(() => {
  toast.success.mockReset()
  toast.error.mockReset()
  toast.warning.mockReset()
})

describe('BulkActionBar', () => {
  it('runs items sequentially and announces per-item progress', async () => {
    let resolveFirst!: () => void
    let resolveSecond!: () => void
    const run = vi
      .fn<(id: string) => Promise<void>>()
      .mockImplementationOnce(
        () =>
          new Promise<void>((resolve) => {
            resolveFirst = resolve
          }),
      )
      .mockImplementationOnce(
        () =>
          new Promise<void>((resolve) => {
            resolveSecond = resolve
          }),
      )
    const user = userEvent.setup()

    render(
      <BulkActionBar
        selectedIds={['item-1', 'item-2']}
        onClear={vi.fn()}
        actions={[{ id: 'enable', label: 'Enable selected', run }]}
      />,
    )

    await user.click(screen.getByRole('button', { name: 'Enable selected' }))
    expect(screen.getByRole('status')).toHaveTextContent('Processing 0 of 2')
    expect(run).toHaveBeenCalledTimes(1)
    expect(run).toHaveBeenNthCalledWith(1, 'item-1')

    await act(async () => resolveFirst())
    await waitFor(() => {
      expect(screen.getByRole('status')).toHaveTextContent('Processing 1 of 2')
      expect(run).toHaveBeenCalledTimes(2)
    })
    expect(run).toHaveBeenNthCalledWith(2, 'item-2')

    await act(async () => resolveSecond())
    await waitFor(() =>
      expect(toast.success).toHaveBeenCalledWith('2 succeeded, 0 failed'),
    )
    expect(screen.getByRole('status')).toHaveTextContent('2 selected')
  })

  it('continues after an item fails and reports the partial result', async () => {
    const run = vi
      .fn<(id: string) => Promise<void>>()
      .mockResolvedValueOnce()
      .mockRejectedValueOnce(new Error('item failed'))
    const user = userEvent.setup()

    render(
      <BulkActionBar
        selectedIds={['item-1', 'item-2']}
        onClear={vi.fn()}
        actions={[{ id: 'disable', label: 'Disable selected', run }]}
      />,
    )

    await user.click(screen.getByRole('button', { name: 'Disable selected' }))

    await waitFor(() => expect(run).toHaveBeenCalledTimes(2))
    expect(toast.error).toHaveBeenCalledWith('Something went wrong.', {
      description: '1 succeeded, 1 failed',
    })
    expect(toast.success).not.toHaveBeenCalled()
  })

  it('requires confirmation before a destructive action', async () => {
    const run = vi.fn<(id: string) => Promise<void>>().mockResolvedValue()
    const user = userEvent.setup()

    render(
      <BulkActionBar
        selectedIds={['item-1', 'item-2']}
        onClear={vi.fn()}
        actions={[
          {
            id: 'delete',
            label: 'Delete selected',
            destructive: true,
            run,
          },
        ]}
      />,
    )

    await user.click(screen.getByRole('button', { name: 'Delete selected' }))
    expect(run).not.toHaveBeenCalled()

    const dialog = screen.getByRole('dialog', {
      name: 'Confirm bulk action',
    })
    expect(dialog).toHaveTextContent(
      '“Delete selected” will run separately for 2 selected items.',
    )

    await user.click(
      within(dialog).getByRole('button', { name: 'Delete selected' }),
    )
    await waitFor(() => expect(run).toHaveBeenCalledTimes(2))
  })
})
