// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// «Mark seen up to here» is bound to ONE real page: the GET mints the target of the
// page chosen and advances nothing; the PUT carries that page's LAST delivery, the
// cursor ETag the GET returned and a key of its own; reading, opening and loading
// more never mutate; a page/filter/scope change invalidates the preparation; a
// barrier is shown as a limited advance, never as completion or an Ack; after a
// committed advance the old continuation chain is dropped.
import { act, fireEvent, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, NetworkError } from '@/lib/api/errors'

const toast = vi.hoisted(() => ({
  success: vi.fn(),
  error: vi.fn(),
  warning: vi.fn(),
}))
vi.mock('@/components/ui/toaster', () => ({ toast, Toaster: () => null }))
const api = vi.hoisted(() => ({
  listInbox: vi.fn(),
  getCursorToken: vi.fn(),
  advanceCursor: vi.fn(),
}))
vi.mock('./api', async (orig) => {
  const real = (await orig()) as Record<string, unknown>
  return { ...real, ...api }
})

import { InboxTable } from './inbox-table'
import type { CursorIntent } from './intent'
import {
  inboxItemOf,
  renderWithQuery,
  scopeOf,
  USER_A,
  USER_B,
  WS,
} from './test-harness'
import './i18n'

const did = (n: number) =>
  `0192f2c0-cccc-7000-8000-${String(n).padStart(12, '0')}`
const page1 = () => ({
  items: [
    inboxItemOf({ id: did(1), delivery_seq: 1 }),
    inboxItemOf({ id: did(2), delivery_seq: 2 }),
  ],
  has_more: true,
  continuation: 'c2n1.next',
  cursor_target: 'c2n1.target-p1',
})
const page2 = () => ({
  items: [inboxItemOf({ id: did(3), delivery_seq: 3 })],
  has_more: false,
  cursor_target: 'c2n1.target-p2',
})
const minted = (version = 0) => ({
  result: {
    cursor: 'c2v2.opaque-token',
    cursor_id: 'cur-1',
    version,
    etag: `"v${version}"`,
  },
  etag: `"v${version}"`,
})
const advanced = (
  over: Partial<{
    replayed: boolean
    barrier_delivery_id: string
    barrier_reason: string
    last_seen_seq: number
  }> = {},
) => ({
  result: {
    command_id: 'cmd-1',
    cursor_id: 'cur-1',
    version: 1,
    etag: '"v1"',
    projection: {
      last_seen_seq: over.last_seen_seq ?? 2,
      ...(over.barrier_delivery_id
        ? {
            barrier_delivery_id: over.barrier_delivery_id,
            barrier_reason: over.barrier_reason ?? 'delivery_read_denied',
            barrier_since: '2026-09-07T00:00:00Z',
          }
        : {}),
    },
    audit_seq: 12,
    replayed: over.replayed ?? false,
  },
  etag: '"v1"',
})

function mount(over: { canWrite?: boolean; me?: string | null } = {}) {
  const onOpenDelivery = vi.fn()
  const r = renderWithQuery(() => (
    <InboxTable
      scope={scopeOf({ principal: USER_B })}
      canDeliveryRead
      canDeliveryWrite={over.canWrite ?? true}
      me={{ userId: over.me === undefined ? USER_B : over.me, label: 'B' }}
      onOpenDelivery={onOpenDelivery}
    />
  ))
  return { ...r, onOpenDelivery }
}

beforeEach(() => {
  for (const fn of Object.values(api)) fn.mockReset()
  toast.warning.mockReset()
  api.listInbox.mockResolvedValueOnce(page1())
  api.listInbox.mockResolvedValueOnce(page2())
  api.listInbox.mockResolvedValue(page1())
})

describe('InboxTable — the personal seen cursor', () => {
  it('reading, loading more and opening mutate nothing; the GET prepares the CHOSEN page and the PUT carries its last delivery, the cursor ETag and its own key; afterwards the chain is dropped', async () => {
    api.getCursorToken.mockResolvedValue(minted(0))
    api.advanceCursor.mockResolvedValue(advanced())
    const { qc, onOpenDelivery } = mount()
    const user = userEvent.setup()
    await screen.findByRole('button', { name: 'Load more' })
    await user.click(screen.getByRole('button', { name: 'Load more' }))
    await waitFor(() => expect(api.listInbox).toHaveBeenCalledTimes(2))
    const section = screen.getByRole('region', { name: 'Seen cursor' })
    const pages = within(section).getAllByRole('listitem')
    expect(pages).toHaveLength(2)
    expect(within(pages[0]).getByText(did(2))).toBeInTheDocument()
    expect(within(pages[1]).getByText(did(3))).toBeInTheDocument()
    // Opening a row is a read of its own; nothing here calls the cursor routes.
    await user.click(screen.getAllByRole('row')[1])
    expect(onOpenDelivery).toHaveBeenCalledWith(did(1))
    expect(api.getCursorToken).not.toHaveBeenCalled()
    expect(api.advanceCursor).not.toHaveBeenCalled()
    // Prepare page 1 (not the last loaded page): the GET names ITS target.
    await user.click(
      within(pages[0]).getByRole('button', { name: 'Mark seen up to here' }),
    )
    await waitFor(() => expect(api.getCursorToken).toHaveBeenCalledTimes(1))
    expect(api.getCursorToken.mock.calls[0][0]).toBe(USER_B)
    expect(api.getCursorToken.mock.calls[0][1]).toEqual({
      workspace_id: WS,
      target: 'c2n1.target-p1',
    })
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText(did(2))).toBeInTheDocument()
    expect(within(dialog).getByText('"v0"')).toBeInTheDocument()
    expect(within(dialog).getByText(`user:${USER_B}`)).toBeInTheDocument()
    // The opaque token is shown nowhere.
    expect(dialog.textContent).not.toContain('c2v2.opaque-token')
    expect(dialog.textContent).not.toContain('c2n1.target')
    expect(api.advanceCursor).not.toHaveBeenCalled()
    await user.click(
      within(dialog).getByRole('button', {
        name: 'Confirm: mark seen up to here',
      }),
    )
    await waitFor(() => expect(api.advanceCursor).toHaveBeenCalledTimes(1))
    const intent = api.advanceCursor.mock.calls[0][0] as CursorIntent
    expect(intent.recipient).toBe(USER_B)
    expect(intent.deliveryId).toBe(did(2))
    expect(intent.etag).toBe('"v0"')
    expect(intent.body).toEqual({
      cursor: 'c2v2.opaque-token',
      delivery_id: did(2),
    })
    expect(intent.key).toMatch(
      /^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/,
    )
    expect(
      await within(dialog).findByText('Cursor advanced'),
    ).toBeInTheDocument()
    expect(
      within(dialog).getByText('2', {
        selector: '[data-slot="cursor-last-seen-seq"]',
      }),
    ).toBeInTheDocument()
    expect(
      within(dialog).getByText(/not an acknowledgement/),
    ).toBeInTheDocument()
    // The chain is dropped: the inbox is asked again from the FIRST page, no old continuation.
    await waitFor(() => expect(api.listInbox).toHaveBeenCalledTimes(3))
    expect(api.listInbox.mock.calls[2][0].continuation).toBeUndefined()
    expect(qc.getQueryCache().findAll().length).toBeGreaterThan(0)
  })

  it('a barrier is a LIMITED advance: named with the engine\'s reason and delivery id, never "advanced" or an Ack; opening it is a fresh read, not a PUT', async () => {
    api.getCursorToken.mockResolvedValue(minted(0))
    api.advanceCursor.mockResolvedValue(
      advanced({
        barrier_delivery_id: did(1),
        barrier_reason: 'delivery_read_denied',
        last_seen_seq: 0,
      }),
    )
    const { onOpenDelivery } = mount()
    const user = userEvent.setup()
    await screen.findByRole('region', { name: 'Seen cursor' })
    await user.click(
      screen.getByRole('button', { name: 'Mark seen up to here' }),
    )
    const dialog = await screen.findByRole('dialog')
    await user.click(
      await within(dialog).findByRole('button', {
        name: 'Confirm: mark seen up to here',
      }),
    )
    expect(
      await within(dialog).findByText('Advance limited by a barrier'),
    ).toBeInTheDocument()
    expect(within(dialog).queryByText('Cursor advanced')).toBeNull()
    expect(
      within(dialog).getByText(did(1), {
        selector: '[data-slot="cursor-barrier"]',
      }),
    ).toBeInTheDocument()
    expect(within(dialog).getByText('delivery_read_denied')).toBeInTheDocument()
    await user.click(
      within(dialog).getByRole('button', { name: 'Open the barrier delivery' }),
    )
    expect(onOpenDelivery).toHaveBeenCalledWith(did(1))
    expect(api.advanceCursor).toHaveBeenCalledTimes(1)
  })

  it('a 412 kills the intention: no re-send, and the only way on is to refresh the inbox and prepare again', async () => {
    api.getCursorToken.mockResolvedValue(minted(0))
    api.advanceCursor.mockRejectedValueOnce(
      new ApiError(
        412,
        'version_mismatch',
        'stale',
        'req-2',
        {},
        { verdict: 'BROKEN', code: 'version_mismatch' },
      ),
    )
    mount()
    const user = userEvent.setup()
    await screen.findByRole('region', { name: 'Seen cursor' })
    await user.click(
      screen.getByRole('button', { name: 'Mark seen up to here' }),
    )
    const dialog = await screen.findByRole('dialog')
    await user.click(
      await within(dialog).findByRole('button', {
        name: 'Confirm: mark seen up to here',
      }),
    )
    expect(
      await within(dialog).findByText(
        'The cursor changed since it was prepared',
      ),
    ).toBeInTheDocument()
    expect(within(dialog).queryByRole('button', { name: /retry/i })).toBeNull()
    expect(
      within(dialog).queryByRole('button', {
        name: 'Confirm: mark seen up to here',
      }),
    ).toBeNull()
    await user.click(
      within(dialog).getByRole('button', { name: 'Refresh the inbox' }),
    )
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    await waitFor(() => expect(api.listInbox).toHaveBeenCalledTimes(2))
    expect(api.listInbox.mock.calls[1][0].continuation).toBeUndefined()
    expect(api.advanceCursor).toHaveBeenCalledTimes(1)
  })

  it('an ambiguous transport result is retried EXPLICITLY with the identical intention — same key, same If-Match, same body — and the replay is said as a replay', async () => {
    api.getCursorToken.mockResolvedValue(minted(0))
    api.advanceCursor.mockRejectedValueOnce(new NetworkError('dropped'))
    api.advanceCursor.mockResolvedValueOnce(advanced({ replayed: true }))
    mount()
    const user = userEvent.setup()
    await screen.findByRole('region', { name: 'Seen cursor' })
    await user.click(
      screen.getByRole('button', { name: 'Mark seen up to here' }),
    )
    const dialog = await screen.findByRole('dialog')
    await user.click(
      await within(dialog).findByRole('button', {
        name: 'Confirm: mark seen up to here',
      }),
    )
    expect(
      await within(dialog).findByText('Outcome unknown'),
    ).toBeInTheDocument()
    expect(api.getCursorToken).toHaveBeenCalledTimes(1)
    await user.click(
      within(dialog).getByRole('button', { name: 'Retry with the same key' }),
    )
    await waitFor(() => expect(api.advanceCursor).toHaveBeenCalledTimes(2))
    const [first, second] = api.advanceCursor.mock.calls.map(
      (c) => c[0] as CursorIntent,
    )
    expect(second).toBe(first)
    expect(second.key).toBe(first.key)
    expect(second.etag).toBe(first.etag)
    expect(second.body).toBe(first.body)
    // No new token was minted for the retry.
    expect(api.getCursorToken).toHaveBeenCalledTimes(1)
    expect(
      await within(dialog).findByText('Already advanced'),
    ).toBeInTheDocument()
  })

  it('a preparation is bound to its page: a refresh that returns another page target drops it before any PUT', async () => {
    api.getCursorToken.mockResolvedValue(minted(0))
    api.listInbox.mockReset()
    api.listInbox.mockResolvedValueOnce(page1())
    api.listInbox.mockResolvedValue({
      ...page1(),
      cursor_target: 'c2n1.target-OTHER',
    })
    mount()
    const user = userEvent.setup()
    await screen.findByRole('region', { name: 'Seen cursor' })
    await user.click(
      screen.getByRole('button', { name: 'Mark seen up to here' }),
    )
    const dialog = await screen.findByRole('dialog')
    await within(dialog).findByRole('button', {
      name: 'Confirm: mark seen up to here',
    })
    // The table refreshes underneath (a focus refetch, another operator, a reload).
    fireEvent.click(
      screen.getByRole('button', { name: 'Refresh', hidden: true }),
    )
    await waitFor(() => expect(api.listInbox).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(api.advanceCursor).not.toHaveBeenCalled()
    // Preparing again mints for the NEW target.
    await user.click(
      screen.getByRole('button', { name: 'Mark seen up to here' }),
    )
    await waitFor(() => expect(api.getCursorToken).toHaveBeenCalledTimes(2))
    expect(api.getCursorToken.mock.calls[1][1].target).toBe('c2n1.target-OTHER')
  })

  it('a local table filter makes the scope ambiguous: the action is disabled and says why', async () => {
    mount()
    const user = userEvent.setup()
    await screen.findByRole('region', { name: 'Seen cursor' })
    expect(
      screen.getByRole('button', { name: 'Mark seen up to here' }),
    ).toBeEnabled()
    await user.type(screen.getByPlaceholderText(/search/i), 'Deploy')
    expect(
      screen.getByRole('button', { name: 'Mark seen up to here' }),
    ).toBeDisabled()
    expect(
      screen.getAllByText(/Clear the table filter first/).length,
    ).toBeGreaterThan(0)
    expect(api.getCursorToken).not.toHaveBeenCalled()
  })

  it('is offered only for the AUTHENTICATED mailbox: another user identity, or no delivery:write, disables it', async () => {
    mount({ me: USER_A })
    await screen.findByRole('region', { name: 'Seen cursor' })
    expect(
      screen.getByRole('button', { name: 'Mark seen up to here' }),
    ).toBeDisabled()
    expect(
      screen.getByText(/not addressed to your user identity/),
    ).toBeInTheDocument()
  })

  it('without delivery:write there is no cursor section at all', async () => {
    mount({ canWrite: false })
    await screen.findAllByText('Deploy window')
    expect(screen.queryByRole('region', { name: 'Seen cursor' })).toBeNull()
    expect(
      screen.queryByRole('button', { name: 'Mark seen up to here' }),
    ).toBeNull()
  })

  it('losing delivery:write with the confirmation open closes it and sends nothing', async () => {
    api.getCursorToken.mockResolvedValue(minted(0))
    let canWrite = true
    const { rerender } = renderWithQuery(() => (
      <InboxTable
        scope={scopeOf({ principal: USER_B })}
        canDeliveryRead
        canDeliveryWrite={canWrite}
        me={{ userId: USER_B, label: 'B' }}
        onOpenDelivery={() => {}}
      />
    ))
    const user = userEvent.setup()
    await screen.findByRole('region', { name: 'Seen cursor' })
    await user.click(
      screen.getByRole('button', { name: 'Mark seen up to here' }),
    )
    const dialog = await screen.findByRole('dialog')
    await within(dialog).findByRole('button', {
      name: 'Confirm: mark seen up to here',
    })
    canWrite = false
    act(() => rerender())
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(api.advanceCursor).not.toHaveBeenCalled()
    expect(toast.warning).toHaveBeenCalled()
  })
})
