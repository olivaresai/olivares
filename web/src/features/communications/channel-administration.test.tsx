// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The administrable catalog is ITS OWN collection: explicit persisted-state filter,
// pages on the server's continuation with a changed filter starting over, archived
// rows kept and inspectable, and a refusal that clears the rows instead of leaving
// a stale page under a notice.
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/errors'

vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
  Toaster: () => null,
}))
const api = vi.hoisted(() => ({ listAdministrableChannels: vi.fn() }))
vi.mock('./api', async (orig) => {
  const real = (await orig()) as Record<string, unknown>
  return { ...real, ...api }
})
const caps = vi.hoisted(() => ({ state: null as unknown }))
vi.mock('@/lib/auth/capabilities', async (orig) => {
  const real = (await orig()) as Record<string, unknown>
  const harness = await import('./test-harness')
  caps.state = harness.capabilityState()
  return {
    ...real,
    ...harness.capabilityDoubles(
      caps.state as ReturnType<typeof capabilityState>,
    ),
  }
})

import { ChannelAdministration } from './channel-administration'
import {
  adminItemOf,
  capabilityState,
  renderWithQuery,
  scopeOf,
  WS,
} from './test-harness'
import './i18n'

const id = (n: number) =>
  `0192f2c0-bbbb-7000-8000-${String(n).padStart(12, '0')}`

function mount(onOpen = vi.fn()) {
  const r = renderWithQuery(() => (
    <ChannelAdministration scope={scopeOf()} onOpenChannel={onOpen} />
  ))
  return { ...r, onOpen }
}

beforeEach(() => {
  api.listAdministrableChannels.mockReset()
  // The collection is admitted unless a case says otherwise: these tests are about the
  // catalog's own behaviour, not about how admission is established.
  Object.assign(caps.state as object, capabilityState())
})

describe('ChannelAdministration — the administrable catalog', () => {
  it('asks with state=all and the explicit limit, follows the continuation on "Load more", and keeps archived rows inspectable', async () => {
    api.listAdministrableChannels.mockResolvedValueOnce({
      items: [
        adminItemOf({ id: id(1), name: 'Alpha', slug: 'alpha' }),
        adminItemOf({
          id: id(2),
          name: 'Bravo',
          slug: 'bravo',
          state: 'archived',
        }),
      ],
      has_more: true,
      continuation: 'c3a1.page-two',
    })
    api.listAdministrableChannels.mockResolvedValueOnce({
      items: [adminItemOf({ id: id(3), name: 'Charlie', slug: 'charlie' })],
      has_more: false,
    })
    const { onOpen } = mount()
    expect(await screen.findByText('Alpha')).toBeInTheDocument()
    expect(api.listAdministrableChannels.mock.calls[0][0]).toEqual({
      workspace_id: WS,
      state: 'all',
      limit: 50,
      continuation: undefined,
    })
    // The archived row is listed, marked, and opens like any other.
    const bravo = screen.getByRole('row', { name: /Bravo/ })
    expect(within(bravo).getByText('Archived')).toBeInTheDocument()
    const user = userEvent.setup()
    await user.click(bravo)
    expect(onOpen).toHaveBeenCalledWith(id(2))
    expect(
      screen.getByText('More administrable channels exist beyond this page'),
    ).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Load more' }))
    await waitFor(() =>
      expect(api.listAdministrableChannels).toHaveBeenCalledTimes(2),
    )
    expect(api.listAdministrableChannels.mock.calls[1][0]).toEqual({
      workspace_id: WS,
      state: 'all',
      limit: 50,
      continuation: 'c3a1.page-two',
    })
    expect(await screen.findByText('Charlie')).toBeInTheDocument()
    expect(screen.getByText('Alpha')).toBeInTheDocument()
  })

  it('changing the persisted-state filter starts a NEW query with no continuation and replaces the rows', async () => {
    api.listAdministrableChannels.mockImplementation(
      async (p: { state: string }) => ({
        items:
          p.state === 'archived'
            ? [
                adminItemOf({
                  id: id(9),
                  name: 'Old',
                  slug: 'old',
                  state: 'archived',
                }),
              ]
            : [adminItemOf({ id: id(1), name: 'Alpha', slug: 'alpha' })],
        has_more: false,
      }),
    )
    mount()
    expect(await screen.findByText('Alpha')).toBeInTheDocument()
    const user = userEvent.setup()
    await user.click(screen.getByRole('combobox', { name: 'Persisted state' }))
    await user.click(
      await screen.findByRole('option', { name: 'Archived only' }),
    )
    await waitFor(() =>
      expect(api.listAdministrableChannels).toHaveBeenCalledTimes(2),
    )
    expect(api.listAdministrableChannels.mock.calls[1][0]).toEqual({
      workspace_id: WS,
      state: 'archived',
      limit: 50,
      continuation: undefined,
    })
    expect(await screen.findByText('Old')).toBeInTheDocument()
    await waitFor(() => expect(screen.queryByText('Alpha')).toBeNull())
  })

  it('a 403 / 404 / 503 answer clears the rows and says which answer it was; nothing stale stays under the notice', async () => {
    api.listAdministrableChannels.mockResolvedValueOnce({
      items: [adminItemOf({ id: id(1), name: 'Alpha', slug: 'alpha' })],
      has_more: false,
    })
    api.listAdministrableChannels.mockRejectedValueOnce(
      new ApiError(
        503,
        'evidence_unavailable',
        'could not look',
        'req-3',
        {},
        { verdict: 'UNKNOWN', code: 'evidence_unavailable' },
      ),
    )
    mount()
    expect(await screen.findByText('Alpha')).toBeInTheDocument()
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Refresh' }))
    await waitFor(() =>
      expect(api.listAdministrableChannels).toHaveBeenCalledTimes(2),
    )
    await waitFor(() => expect(screen.queryByText('Alpha')).toBeNull())
    expect(screen.getByText(/The engine could not look/)).toBeInTheDocument()
  })

  it('opens a channel by ID for administration without any read-tier call', async () => {
    api.listAdministrableChannels.mockResolvedValue({
      items: [],
      has_more: false,
    })
    const { onOpen } = mount()
    await screen.findByText('No administrable channels')
    const user = userEvent.setup()
    await user.type(screen.getByLabelText('Administer a channel by ID'), id(7))
    await user.click(screen.getByRole('button', { name: 'Administer' }))
    expect(onOpen).toHaveBeenCalledWith(id(7))
  })
})
