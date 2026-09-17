// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The Ack is CAS on the version the operator READ, a 412 is never answered by a
// resend, a late Ack is said to be late, and received content is painted as inert
// text — a script tag, an action reference and an HTML anchor all land as glyphs.
import { act, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, NetworkError } from '@/lib/api/errors'

vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
  Toaster: () => null,
}))
const api = vi.hoisted(() => ({ getDelivery: vi.fn(), ackDelivery: vi.fn() }))
vi.mock('./api', async (orig) => {
  const real = (await orig()) as Record<string, unknown>
  return { ...real, ...api }
})

import { DeliverySheet } from './delivery-sheet'
import type { AckIntent } from './intent'
import {
  DELIVERY_ID,
  deferred,
  readOf,
  renderWithQuery,
  scopeOf,
} from './test-harness'
import './i18n'

const ackResult = (
  over: Partial<{ replayed: boolean; late: boolean; version: number }> = {},
) => ({
  result: {
    command_id: 'cmd',
    ack_id: 'ack-1',
    delivery_id: DELIVERY_ID,
    message_id: 'm',
    event_id: 'ev',
    version: over.version ?? 2,
    etag: `"v${over.version ?? 2}"`,
    state: 'acknowledged',
    late: over.late ?? false,
    audit_seq: 9,
    replayed: over.replayed ?? false,
    fulfillment: {
      state: 'fulfilled',
      required: 1,
      acknowledged: 1,
      viable: 1,
      unmet: 0,
    },
  },
  etag: `"v${over.version ?? 2}"`,
})

function mount(over: { canWrite?: boolean; canMessageRead?: boolean } = {}) {
  const onOpenMessage = vi.fn()
  const r = renderWithQuery(() => (
    <DeliverySheet
      open
      onOpenChange={() => {}}
      deliveryId={DELIVERY_ID}
      scope={scopeOf()}
      canDeliveryRead
      canDeliveryWrite={over.canWrite ?? true}
      canMessageRead={over.canMessageRead ?? true}
      onOpenMessage={onOpenMessage}
    />
  ))
  return { ...r, onOpenMessage }
}

beforeEach(() => {
  api.getDelivery.mockReset()
  api.ackDelivery.mockReset()
})

describe('DeliverySheet — explicit Ack with the read version', () => {
  it('sends If-Match "v<read version>" with its own key, then re-reads the durable state', async () => {
    api.getDelivery.mockResolvedValueOnce(readOf({ version: 1 }))
    api.getDelivery.mockResolvedValueOnce(
      readOf({ version: 2, acknowledged_at: '2026-09-06T11:00:00Z' }),
    )
    api.ackDelivery.mockResolvedValue(ackResult())
    mount()
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'Acknowledge' }))
    const panel = await screen.findByText('Acknowledge this delivery')
    expect(panel).toBeInTheDocument()
    expect(screen.getByText('"v1"')).toBeInTheDocument()
    await user.click(
      screen.getByRole('button', { name: 'Confirm acknowledgement' }),
    )
    await waitFor(() => expect(api.ackDelivery).toHaveBeenCalledTimes(1))
    const intent = api.ackDelivery.mock.calls[0][0] as AckIntent
    expect(intent.etag).toBe('"v1"')
    expect(intent.deliveryId).toBe(DELIVERY_ID)
    expect(intent.key).toMatch(/^[0-9a-f-]{36}$/)
    expect(
      await screen.findByText('Acknowledged', { selector: 'p' }),
    ).toBeInTheDocument()
    // The durable row, read again: version 2 and the acknowledged instant.
    await waitFor(() => expect(api.getDelivery).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(screen.getByText('v2')).toBeInTheDocument())
  })

  it('a 412 is a CONFLICT: nothing is re-sent, the intent dies, and re-reading offers a new Ack on the fresh version', async () => {
    api.getDelivery.mockResolvedValueOnce(readOf({ version: 1 }))
    api.ackDelivery.mockRejectedValueOnce(
      new ApiError(
        412,
        'version_mismatch',
        'stale',
        'req-9',
        {},
        { verdict: 'BROKEN', code: 'version_mismatch' },
      ),
    )
    mount()
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'Acknowledge' }))
    await user.click(
      screen.getByRole('button', { name: 'Confirm acknowledgement' }),
    )
    const conflict = await screen.findByText(
      'The delivery changed since you read it',
    )
    expect(conflict).toBeInTheDocument()
    expect(api.ackDelivery).toHaveBeenCalledTimes(1)
    // No automatic retry with a fresh ETag: the only offer is to re-read.
    expect(
      screen.queryByRole('button', { name: 'Retry with the same key' }),
    ).toBeNull()
    api.getDelivery.mockResolvedValueOnce(readOf({ version: 2 }))
    await user.click(screen.getByRole('button', { name: 'Re-read' }))
    await waitFor(() => expect(api.getDelivery).toHaveBeenCalledTimes(2))
    expect(await screen.findByText('v2')).toBeInTheDocument()
    expect(
      screen.queryByText('The delivery changed since you read it'),
    ).toBeNull()
    api.ackDelivery.mockResolvedValue(ackResult({ version: 3 }))
    await user.click(screen.getByRole('button', { name: 'Acknowledge' }))
    expect(screen.getByText('"v2"')).toBeInTheDocument()
    await user.click(
      screen.getByRole('button', { name: 'Confirm acknowledgement' }),
    )
    await waitFor(() => expect(api.ackDelivery).toHaveBeenCalledTimes(2))
    const second = api.ackDelivery.mock.calls[1][0] as AckIntent
    const first = api.ackDelivery.mock.calls[0][0] as AckIntent
    expect(second.etag).toBe('"v2"')
    expect(second.key).not.toBe(first.key)
  })

  it('the served engine\'s bare 409 {"error":{"message":"conflict"}} for a stale Ack is the same CONFLICT: nothing re-sent, re-read offered', async () => {
    api.getDelivery.mockResolvedValueOnce(readOf({ version: 1 }))
    // Measured 2026-09-06: modules/sessions writeStoreError → core/api moduleErrorMessage.
    api.ackDelivery.mockRejectedValueOnce(
      new ApiError(
        409,
        'internal',
        'conflict',
        'req-10',
        {},
        {
          error: { message: 'conflict' },
        },
      ),
    )
    mount()
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'Acknowledge' }))
    await user.click(
      screen.getByRole('button', { name: 'Confirm acknowledgement' }),
    )
    expect(
      await screen.findByText('The delivery changed since you read it'),
    ).toBeInTheDocument()
    expect(
      document.querySelector('[data-failure-kind="conflict"]'),
    ).not.toBeNull()
    expect(api.ackDelivery).toHaveBeenCalledTimes(1)
    expect(
      screen.queryByRole('button', { name: 'Retry with the same key' }),
    ).toBeNull()
    api.getDelivery.mockResolvedValueOnce(readOf({ version: 2 }))
    await user.click(screen.getByRole('button', { name: 'Re-read' }))
    await waitFor(() => expect(api.getDelivery).toHaveBeenCalledTimes(2))
    expect(await screen.findByText('v2')).toBeInTheDocument()
    expect(api.ackDelivery).toHaveBeenCalledTimes(1)
  })

  it('an ambiguous transport result retries with the SAME key and If-Match; a replay is said to be a replay', async () => {
    api.getDelivery.mockResolvedValue(readOf({ version: 1 }))
    api.ackDelivery.mockRejectedValueOnce(new NetworkError('down'))
    api.ackDelivery.mockResolvedValueOnce(ackResult({ replayed: true }))
    mount()
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'Acknowledge' }))
    await user.click(
      screen.getByRole('button', { name: 'Confirm acknowledgement' }),
    )
    expect(await screen.findByText('Outcome unknown')).toBeInTheDocument()
    await user.click(
      screen.getByRole('button', { name: 'Retry with the same key' }),
    )
    await waitFor(() => expect(api.ackDelivery).toHaveBeenCalledTimes(2))
    const [a, b] = api.ackDelivery.mock.calls.map((c) => c[0] as AckIntent)
    expect(b.key).toBe(a.key)
    expect(b.etag).toBe(a.etag)
    expect(await screen.findByText('Already acknowledged')).toBeInTheDocument()
  })

  it('a LATE Ack is shown as late and never as fulfillment', async () => {
    api.getDelivery.mockResolvedValue(
      readOf({ version: 1, ack_due_at: '2026-09-01T00:00:00Z' }),
    )
    api.ackDelivery.mockResolvedValue(ackResult({ late: true }))
    mount()
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'Acknowledge' }))
    await user.click(
      screen.getByRole('button', { name: 'Confirm acknowledgement' }),
    )
    const late = await screen.findByText(/Late acknowledgement/)
    expect(late).toBeInTheDocument()
    expect(late.closest('[data-slot="ack-late"]')).not.toBeNull()
  })

  it('hostile content lands as inert text: no script, no anchor, no button for an action reference', async () => {
    const hostile = readOf({
      subject: '<img src=x onerror=alert(1)> subject',
      blocks: [
        {
          type: 'text',
          format: 'markdown',
          text: '<script>window.__pwned=1</script>[click](javascript:alert(1))',
        },
        {
          type: 'action_ref',
          code: 'deploy.now',
          text: 'Run it',
          reference: { kind: 'tool', ref: 'javascript:alert(2)' },
        },
        {
          type: 'reference',
          reference: { kind: 'url', ref: 'https://example.invalid/<a href=x>' },
        },
      ],
    })
    api.getDelivery.mockResolvedValue(hostile)
    const { result } = mount()
    const content = await screen.findByLabelText('Content')
    expect(
      within(content).getByText(
        '<script>window.__pwned=1</script>[click](javascript:alert(1))',
      ),
    ).toBeInTheDocument()
    expect(result.container.querySelector('script')).toBeNull()
    expect(result.container.querySelector('img')).toBeNull()
    expect(within(content).queryByRole('link')).toBeNull()
    expect(within(content).queryByRole('button')).toBeNull()
    expect((window as unknown as { __pwned?: number }).__pwned).toBeUndefined()
    expect(within(content).getByText('deploy.now')).toBeInTheDocument()
    expect(
      within(content).getByText('markdown, shown as text'),
    ).toBeInTheDocument()
  })

  it('a re-read refused with 404/503 REPLACES the content; nothing stale stays under the notice', async () => {
    api.getDelivery.mockResolvedValueOnce(readOf())
    mount()
    const user = userEvent.setup()
    expect(await screen.findByText('Deploy window')).toBeInTheDocument()
    api.getDelivery.mockRejectedValueOnce(
      new ApiError(404, 'not_found', 'gone', 'r', {}, { code: 'not_found' }),
    )
    await user.click(screen.getByRole('button', { name: 'Re-read' }))
    await waitFor(() => expect(screen.queryByText('Deploy window')).toBeNull())
    expect(
      screen.getByText('Not found, or not visible to this principal.'),
    ).toBeInTheDocument()
    api.getDelivery.mockRejectedValueOnce(
      new ApiError(
        503,
        'evidence_unavailable',
        'unknown',
        'r',
        {},
        { verdict: 'UNKNOWN', code: 'evidence_unavailable' },
      ),
    )
    await user.click(screen.getByRole('button', { name: 'Re-read' }))
    expect(await screen.findByText(/could not look/)).toBeInTheDocument()
    expect(screen.queryByText('Deploy window')).toBeNull()
    expect(screen.queryByText('No deliveries')).toBeNull()
  })

  it('a late read landing after the scope moved is discarded, not painted; the new scope reads afresh', async () => {
    const first = deferred<ReturnType<typeof readOf>>()
    api.getDelivery.mockReturnValueOnce(first.promise)
    api.getDelivery.mockResolvedValue(readOf({ subject: 'Fresh scope' }))
    // The room remounts on its scope key in production (CommunicationsView); the
    // sheet follows the same key here, so a scope move is the same event.
    let scope = scopeOf()
    const { rerender } = renderWithQuery(() => (
      <DeliverySheet
        key={scope.key}
        open
        onOpenChange={() => {}}
        deliveryId={DELIVERY_ID}
        scope={scope}
        canDeliveryRead
        canDeliveryWrite
        canMessageRead
        onOpenMessage={() => {}}
      />
    ))
    await waitFor(() => expect(api.getDelivery).toHaveBeenCalledTimes(1))
    const staleSignal = api.getDelivery.mock.calls[0][2] as AbortSignal
    scope = scopeOf({ epoch: 2, key: 'other|t1|c1|w:x' })
    rerender()
    expect(staleSignal.aborted).toBe(true)
    act(() => {
      first.resolve(readOf({ subject: 'Stale scope' }))
    })
    await waitFor(() => expect(api.getDelivery).toHaveBeenCalledTimes(2))
    expect(await screen.findByText('Fresh scope')).toBeInTheDocument()
    expect(screen.queryByText('Stale scope')).toBeNull()
  })

  it('without delivery:write the Ack is not offered; without message:read the message is not opened', async () => {
    api.getDelivery.mockResolvedValue(readOf())
    const { onOpenMessage } = mount({ canWrite: false, canMessageRead: false })
    expect(await screen.findByText('Deploy window')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Acknowledge' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Open message' })).toBeNull()
    expect(onOpenMessage).not.toHaveBeenCalled()
  })

  it('a permission lost with the confirmation open closes it and dispatches nothing, even for a queued click', async () => {
    api.getDelivery.mockResolvedValue(readOf({ version: 1 }))
    api.ackDelivery.mockResolvedValue(ackResult())
    let canWrite = true
    const { rerender } = renderWithQuery(() => (
      <DeliverySheet
        open
        onOpenChange={() => {}}
        deliveryId={DELIVERY_ID}
        scope={scopeOf()}
        canDeliveryRead
        canDeliveryWrite={canWrite}
        canMessageRead
        onOpenMessage={() => {}}
      />
    ))
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'Acknowledge' }))
    const confirm = screen.getByRole('button', {
      name: 'Confirm acknowledgement',
    })
    canWrite = false
    rerender()
    await waitFor(() =>
      expect(screen.queryByText('Acknowledge this delivery')).toBeNull(),
    )
    // The stale element's click still reaches React: the dispatch judges NOW.
    act(() => {
      confirm.click()
    })
    await new Promise((r) => setTimeout(r, 20))
    expect(api.ackDelivery).not.toHaveBeenCalled()
  })
})
