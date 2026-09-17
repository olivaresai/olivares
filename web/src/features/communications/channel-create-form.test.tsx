// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Initial grants are EXPLICIT — "add my account" adds a row with every bit unticked —
// the wire body is exactly what the form shows, and a transport failure is an UNKNOWN
// outcome with no retry: the route has no idempotency key.
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { NetworkError } from '@/lib/api/errors'

vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
  Toaster: () => null,
}))
const api = vi.hoisted(() => ({
  createChannel: vi.fn(),
  listMembers: vi.fn(),
  listAgents: vi.fn(),
}))
vi.mock('./api', async (orig) => {
  const real = (await orig()) as Record<string, unknown>
  return { ...real, ...api }
})

import { ChannelCreateForm, localToIso } from './channel-create-form'
import {
  channelOf,
  renderWithQuery,
  scopeOf,
  USER_A,
  USER_B,
  WS,
} from './test-harness'
import type { ChannelCreateInput } from './types'
import './i18n'

function mount(over: { canRead?: boolean; onCheck?: () => void } = {}) {
  return renderWithQuery(() => (
    <ChannelCreateForm
      scope={scopeOf()}
      canChannelRead={over.canRead ?? true}
      canChannelWrite
      canUserRead={false}
      canAgentRead={false}
      me={{ userId: USER_A, label: 'Ada' }}
      onCheckCatalog={over.onCheck}
    />
  ))
}

beforeEach(() => {
  api.createChannel.mockReset()
  api.listMembers.mockResolvedValue({ items: [], has_more: false })
  api.listAgents.mockResolvedValue({ items: [], has_more: false })
})

describe('ChannelCreateForm — explicit grants', () => {
  it('"add my account" adds a subject with EVERY bit unticked; the body carries exactly the ticked bits', async () => {
    api.createChannel.mockResolvedValue({
      result: {
        channel: channelOf({ slug: 'ops' }),
        grants: [
          {
            id: 'g1',
            tenant_id: 't1',
            workspace_id: WS,
            version: 1,
            channel_id: channelOf().id,
            subject: { kind: 'user', ref: USER_A },
            can_read: true,
            can_write: false,
            can_admin: false,
            state: 'active',
            generation: 1,
            granted_by: { kind: 'user', ref: USER_A },
            created_at: '2026-09-06T10:00:00Z',
            updated_at: '2026-09-06T10:00:00Z',
          },
        ],
        etag: '"v1"',
        audit_seq: 5,
      },
      etag: '"v1"',
    })
    mount()
    const user = userEvent.setup()
    await user.type(screen.getByLabelText(/^Slug/), 'ops')
    await user.type(screen.getByLabelText(/^Name/), 'Ops')
    await user.click(
      screen.getByRole('button', { name: 'Add my account as a subject' }),
    )
    const row = screen
      .getByRole('textbox', { name: 'Reference (ID)' })
      .closest('[data-slot="grant-row"]')!
    expect(screen.getByRole('textbox', { name: 'Reference (ID)' })).toHaveValue(
      USER_A,
    )
    for (const bit of ['Read', 'Write', 'Admin']) {
      expect(
        within(row as HTMLElement).getByRole('checkbox', { name: bit }),
      ).not.toBeChecked()
    }
    // Submitting with no bit is a KNOWN-invalid intent: refused here, no request.
    await user.click(screen.getByRole('button', { name: 'Create channel' }))
    expect(
      await screen.findByText('Every grant needs at least one bit.'),
    ).toBeInTheDocument()
    expect(api.createChannel).not.toHaveBeenCalled()
    await user.click(
      within(row as HTMLElement).getByRole('checkbox', { name: 'Read' }),
    )
    await user.click(screen.getByRole('button', { name: 'Add grant' }))
    const refs = screen.getAllByRole('textbox', { name: 'Reference (ID)' })
    await user.type(refs[1], USER_B)
    const row2 = refs[1].closest('[data-slot="grant-row"]') as HTMLElement
    await user.click(within(row2).getByRole('checkbox', { name: 'Write' }))
    await user.click(screen.getByRole('button', { name: 'Create channel' }))
    await waitFor(() => expect(api.createChannel).toHaveBeenCalledTimes(1))
    const body = (
      api.createChannel.mock.calls[0][0] as { body: ChannelCreateInput }
    ).body
    expect(body).toEqual({
      workspace_id: WS,
      slug: 'ops',
      name: 'Ops',
      kind: 'coordination',
      sensitivity: 'internal',
      content_protection: 'application_sealed',
      default_ack_policy: 'none',
      default_ack_timeout_ms: 0,
      default_wake: 'none',
      max_fanout: 1,
      max_automation_depth: 0,
      initial_grants: [
        {
          subject: { kind: 'user', ref: USER_A },
          can_read: true,
          can_write: false,
          can_admin: false,
        },
        {
          subject: { kind: 'user', ref: USER_B },
          can_read: false,
          can_write: true,
          can_admin: false,
        },
      ],
    })
    // The explicit tenant, plus the surface's half of the dispatch guard (a function
    // the transport runs before each fetch): never a bearer, never a session id.
    expect(api.createChannel.mock.calls[0][1]).toMatchObject({ tenant: 't1' })
    expect(typeof api.createChannel.mock.calls[0][1].guard).toBe('function')
    expect(Object.keys(api.createChannel.mock.calls[0][1]).sort()).toEqual([
      'guard',
      'tenant',
    ])
    expect(await screen.findByText('Channel created')).toBeInTheDocument()
    expect(screen.getByText(/not a durable list/)).toBeInTheDocument()
    expect(screen.getByText('5')).toBeInTheDocument()
  })

  it('a transport failure is an UNKNOWN outcome: one request, no retry, the slug to look for, and the catalog offered', async () => {
    api.createChannel.mockRejectedValueOnce(new NetworkError('down'))
    const onCheck = vi.fn()
    mount({ onCheck })
    const user = userEvent.setup()
    await user.type(screen.getByLabelText(/^Slug/), 'ops-2')
    await user.type(screen.getByLabelText(/^Name/), 'Ops 2')
    await user.click(
      screen.getByRole('button', { name: 'Add my account as a subject' }),
    )
    await user.click(screen.getByRole('checkbox', { name: 'Read' }))
    await user.click(screen.getByRole('button', { name: 'Create channel' }))
    expect(await screen.findByText('Outcome unknown')).toBeInTheDocument()
    expect(screen.getByText(/slug “ops-2”/)).toBeInTheDocument()
    await new Promise((r) => setTimeout(r, 20))
    expect(api.createChannel).toHaveBeenCalledTimes(1)
    await user.click(screen.getByRole('button', { name: 'Check the catalog' }))
    expect(onCheck).toHaveBeenCalledTimes(1)
  })

  it('without channel:read the form says the catalog will not show the result', () => {
    mount({ canRead: false })
    expect(
      screen.getByText(/channel:write without channel:read/),
    ).toBeInTheDocument()
  })

  it('localToIso turns a local datetime into RFC 3339 and refuses garbage', () => {
    expect(localToIso('')).toBeNull()
    expect(localToIso('not-a-date')).toBeNull()
    expect(localToIso('2026-09-06T10:30')).toMatch(
      /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/,
    )
  })
})
