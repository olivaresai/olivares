// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Pages are the SERVER's: an explicit limit within 200, "load more" on the opaque
// continuation the previous page returned, and the truncation said out loud. The
// inbox shows no unread count and invents no history.
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
  Toaster: () => null,
}))
const api = vi.hoisted(() => ({ listChannels: vi.fn(), listInbox: vi.fn() }))
vi.mock('./api', async (orig) => {
  const real = (await orig()) as Record<string, unknown>
  return { ...real, ...api }
})

import { ChannelCatalog } from './channel-catalog'
import { InboxTable } from './inbox-table'
import {
  catalogItemOf,
  inboxItemOf,
  renderWithQuery,
  scopeOf,
  WS,
} from './test-harness'
import './i18n'

const id = (n: number) =>
  `0192f2c0-cccc-7000-8000-${String(n).padStart(12, '0')}`

beforeEach(() => {
  api.listChannels.mockReset()
  api.listInbox.mockReset()
})

describe('catalog pagination', () => {
  it('asks for the explicit limit, follows the opaque continuation on "Load more", and stops when has_more is false', async () => {
    api.listChannels.mockResolvedValueOnce({
      items: [
        catalogItemOf({ id: id(1), name: 'One', slug: 'one' }),
        catalogItemOf({ id: id(2), name: 'Two', slug: 'two' }),
      ],
      has_more: true,
      continuation: 'c3n1.page-two',
    })
    api.listChannels.mockResolvedValueOnce({
      items: [catalogItemOf({ id: id(3), name: 'Three', slug: 'three' })],
      has_more: false,
    })
    renderWithQuery(() => (
      <ChannelCatalog
        scope={scopeOf()}
        canChannelRead
        canChannelWrite={false}
        onOpenChannel={() => {}}
        onOpenById={() => {}}
        onCreate={() => {}}
      />
    ))
    expect(await screen.findByText('One')).toBeInTheDocument()
    expect(api.listChannels.mock.calls[0][0]).toEqual({
      workspace_id: WS,
      limit: 50,
      continuation: undefined,
    })
    expect(
      screen.getByText('More channels exist beyond this page'),
    ).toBeInTheDocument()
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Load more' }))
    await waitFor(() => expect(api.listChannels).toHaveBeenCalledTimes(2))
    expect(api.listChannels.mock.calls[1][0]).toEqual({
      workspace_id: WS,
      limit: 50,
      continuation: 'c3n1.page-two',
    })
    expect(await screen.findByText('Three')).toBeInTheDocument()
    expect(screen.getByText('One')).toBeInTheDocument()
    await waitFor(() =>
      expect(
        screen.queryByText('More channels exist beyond this page'),
      ).toBeNull(),
    )
    expect(screen.queryByRole('button', { name: 'Load more' })).toBeNull()
  })
})

describe('inbox pagination', () => {
  it('pages the personal mailbox on its own continuation and paints no unread count', async () => {
    api.listInbox.mockResolvedValueOnce({
      items: [inboxItemOf({ id: id(1) })],
      has_more: true,
      continuation: 'c2n1.next',
      cursor_target: 'c2n1.target',
    })
    api.listInbox.mockResolvedValueOnce({
      items: [inboxItemOf({ id: id(2) })],
      has_more: false,
    })
    const { result } = renderWithQuery(() => (
      <InboxTable scope={scopeOf()} canDeliveryRead onOpenDelivery={() => {}} />
    ))
    expect(
      await screen.findByText('More deliveries exist beyond this page'),
    ).toBeInTheDocument()
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Load more' }))
    await waitFor(() => expect(api.listInbox).toHaveBeenCalledTimes(2))
    expect(api.listInbox.mock.calls[1][0]).toEqual({
      workspace_id: WS,
      limit: 50,
      continuation: 'c2n1.next',
    })
    expect(result.container.textContent).not.toMatch(/unread/i)
    expect(result.container.textContent).not.toContain('c2n1.target')
  })
})
