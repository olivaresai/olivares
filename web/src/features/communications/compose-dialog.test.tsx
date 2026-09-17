// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ONE key per intention: confirming freezes it, an ambiguous result retries the
// same object, discarding is the only way back to editing and the next confirmation
// is a new key. A replay reads as a replay; a refusal is the server's, verbatim.
import { act, fireEvent, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { StrictMode, useState } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, NetworkError } from '@/lib/api/errors'

vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
  Toaster: () => null,
}))
const api = vi.hoisted(() => ({
  sendNotice: vi.fn(),
  listMembers: vi.fn(),
  listAgents: vi.fn(),
}))
vi.mock('./api', async (orig) => {
  const real = (await orig()) as Record<string, unknown>
  return { ...real, ...api }
})

import { ComposeDialog } from './compose-dialog'
import type { SendIntent } from './intent'
import {
  CHANNEL_ID,
  renderWithQuery,
  scopeOf,
  USER_A,
  USER_B,
} from './test-harness'
import './i18n'

const publish = (replayed = false) => ({
  result: {
    verdict: 'LIMPIO',
    code: 'published',
    command_id: 'cmd',
    channel_id: CHANNEL_ID,
    message_id: 'm1',
    delivery_id: 'd1',
    event_id: 'ev',
    version: 1,
    state: 'published',
    delivery_count: 1,
    required_count: 1,
    ack_quorum: 1,
    fulfillment: {
      state: 'pending',
      required: 1,
      acknowledged: 0,
      viable: 1,
      unmet: 1,
    },
    audience_hash: 'ah',
    payload_digest: 'pd',
    plan_hash: 'ph',
    audit_seq: 3,
    replayed,
  },
  replayed,
  status: replayed ? 200 : 201,
})

function mount(over: { canSend?: boolean } = {}) {
  let canSend = over.canSend ?? true
  const r = renderWithQuery(() => (
    <ComposeDialog
      open
      onOpenChange={() => {}}
      channel={{ id: CHANNEL_ID, name: 'Ops', slug: 'ops' }}
      scope={scopeOf()}
      canSend={canSend}
      canUserRead={false}
      canAgentRead={false}
      me={{ userId: USER_A, label: 'Ada' }}
    />
  ))
  return {
    ...r,
    revoke: () => {
      canSend = false
      r.rerender()
    },
  }
}

async function fillAndConfirm(user: ReturnType<typeof userEvent.setup>) {
  await user.type(screen.getByLabelText('Reference (ID)'), USER_B)
  await user.type(screen.getByLabelText(/^Subject/), 'Deploy window')
  await user.type(screen.getByLabelText('Text'), 'Freeze at 18:00')
  await user.click(screen.getByRole('button', { name: 'Compose' }))
  expect(await screen.findByText('Confirm the intention')).toBeInTheDocument()
}

beforeEach(() => {
  api.sendNotice.mockReset()
  api.listMembers.mockResolvedValue({ items: [], has_more: false })
  api.listAgents.mockResolvedValue({ items: [], has_more: false })
})

describe('ComposeDialog — immutable intention', () => {
  it('confirming freezes the body and target; sending carries exactly that intent', async () => {
    api.sendNotice.mockResolvedValue(publish())
    mount()
    const user = userEvent.setup()
    await fillAndConfirm(user)
    await user.click(screen.getByRole('button', { name: 'Confirm and send' }))
    await waitFor(() => expect(api.sendNotice).toHaveBeenCalledTimes(1))
    const intent = api.sendNotice.mock.calls[0][0] as SendIntent
    expect(Object.isFrozen(intent)).toBe(true)
    expect(intent.body).toEqual({
      channel_id: CHANNEL_ID,
      recipient: { kind: 'user', ref: USER_B },
      content: {
        subject: 'Deploy window',
        blocks: [{ type: 'text', format: 'plain', text: 'Freeze at 18:00' }],
      },
      urgency: 'normal',
    })
    expect(api.sendNotice.mock.calls[0][1]).toMatchObject({ tenant: 't1' })
    expect(
      await screen.findByText('Notice published', { selector: 'p' }),
    ).toBeInTheDocument()
    expect(screen.getByText(intent.key)).toBeInTheDocument()
  })

  it('an ambiguous result retries the SAME key and body; discarding and re-confirming mints a NEW key', async () => {
    api.sendNotice.mockRejectedValueOnce(new NetworkError('down'))
    api.sendNotice.mockResolvedValueOnce(publish(true))
    api.sendNotice.mockResolvedValueOnce(publish())
    mount()
    const user = userEvent.setup()
    await fillAndConfirm(user)
    await user.click(screen.getByRole('button', { name: 'Confirm and send' }))
    expect(await screen.findByText('Outcome unknown')).toBeInTheDocument()
    await user.click(
      screen.getByRole('button', { name: 'Retry with the same key' }),
    )
    await waitFor(() => expect(api.sendNotice).toHaveBeenCalledTimes(2))
    const [first, second] = api.sendNotice.mock.calls.map(
      (c) => c[0] as SendIntent,
    )
    expect(second).toBe(first)
    expect(
      await screen.findByText('Already published', { selector: 'p' }),
    ).toBeInTheDocument()
    // Editing is only possible after an explicit discard: no editor is on screen.
    expect(screen.queryByLabelText('Text')).toBeNull()
  })

  it('discard returns to editing and the next confirmation is a different key', async () => {
    api.sendNotice.mockRejectedValueOnce(new NetworkError('down'))
    api.sendNotice.mockResolvedValueOnce(publish())
    mount()
    const user = userEvent.setup()
    await fillAndConfirm(user)
    await user.click(screen.getByRole('button', { name: 'Confirm and send' }))
    expect(await screen.findByText('Outcome unknown')).toBeInTheDocument()
    const firstKey = (api.sendNotice.mock.calls[0][0] as SendIntent).key
    await user.click(
      screen.getByRole('button', { name: 'Discard this intention and edit' }),
    )
    expect(await screen.findByLabelText('Text')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Compose' }))
    await user.click(
      await screen.findByRole('button', { name: 'Confirm and send' }),
    )
    await waitFor(() => expect(api.sendNotice).toHaveBeenCalledTimes(2))
    expect((api.sendNotice.mock.calls[1][0] as SendIntent).key).not.toBe(
      firstKey,
    )
  })

  it('a refusal is shown as the server phrased it, with its code, and nothing is retried', async () => {
    api.sendNotice.mockRejectedValueOnce(
      new ApiError(
        403,
        'forbidden',
        'forbidden',
        'req-4',
        {},
        { verdict: 'BROKEN', code: 'forbidden' },
      ),
    )
    mount()
    const user = userEvent.setup()
    await fillAndConfirm(user)
    await user.click(screen.getByRole('button', { name: 'Confirm and send' }))
    expect(await screen.findByText('Send refused')).toBeInTheDocument()
    expect(
      screen.getByText('The engine refused this for the current principal.'),
    ).toBeInTheDocument()
    expect(screen.getByText('req-4')).toBeInTheDocument()
    expect(api.sendNotice).toHaveBeenCalledTimes(1)
    expect(
      screen.queryByRole('button', { name: 'Retry with the same key' }),
    ).toBeNull()
  })

  it('validation refuses known-invalid intents without a request', async () => {
    mount()
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Compose' }))
    expect(
      await screen.findByText('A recipient reference is required.'),
    ).toBeInTheDocument()
    expect(screen.getByText('A subject is required.')).toBeInTheDocument()
    expect(screen.getByText('A text block needs text.')).toBeInTheDocument()
    expect(api.sendNotice).not.toHaveBeenCalled()
  })

  it('refuses what the engine refuses: raw HTML under markdown, a spaced status code, an over-long subject — without a request', async () => {
    mount()
    const user = userEvent.setup()
    await user.type(screen.getByLabelText('Reference (ID)'), USER_B)
    await user.type(screen.getByLabelText(/^Subject/), 'x'.repeat(257))
    await user.type(screen.getByLabelText('Text'), 'plain is fine')
    await user.click(screen.getByRole('button', { name: 'Compose' }))
    expect(
      await screen.findByText('The subject must be at most 256 bytes.'),
    ).toBeInTheDocument()
    expect(api.sendNotice).not.toHaveBeenCalled()
  })

  it('losing the send permission with the confirmation open closes it; nothing is dispatched', async () => {
    api.sendNotice.mockResolvedValue(publish())
    const { revoke } = mount()
    const user = userEvent.setup()
    await fillAndConfirm(user)
    revoke()
    await waitFor(() =>
      expect(screen.queryByText('Confirm the intention')).toBeNull(),
    )
    expect(await screen.findByLabelText('Text')).toBeInTheDocument()
    expect(api.sendNotice).not.toHaveBeenCalled()
    expect(screen.getByRole('button', { name: 'Compose' })).toBeDisabled()
  })
})

// Exercise the real controlled Dialog, portal, focus scope and autofocus events.
// This host only owns open/presence; it never captures or restores focus for it.
function FocusHost({
  present,
  firstOrigin,
}: {
  present: boolean
  firstOrigin: boolean
}) {
  const [open, setOpen] = useState(false)
  return (
    <>
      {firstOrigin && (
        <button onClick={() => setOpen(true)}>First origin</button>
      )}
      <button onClick={() => setOpen(true)}>Second origin</button>
      {present && (
        <ComposeDialog
          open={open}
          onOpenChange={setOpen}
          channel={{ id: CHANNEL_ID, name: 'Ops', slug: 'ops' }}
          scope={scopeOf()}
          canSend
          canUserRead={false}
          canAgentRead={false}
          me={{ userId: USER_A, label: 'Ada' }}
        />
      )}
    </>
  )
}

function mountFocusHost(strict: boolean) {
  let present = true
  let firstOrigin = true
  const result = renderWithQuery(() => {
    const host = <FocusHost present={present} firstOrigin={firstOrigin} />
    return strict ? <StrictMode>{host}</StrictMode> : host
  })
  return {
    removeDialog: () => {
      present = false
      result.rerender()
    },
    removeFirstOrigin: () => {
      firstOrigin = false
      result.rerender()
    },
  }
}

describe.each([false, true])(
  'ComposeDialog — focus lifecycle (StrictMode=%s)',
  (strict) => {
    it('captures the real keyboard origin before autofocus, keeps focus inside while editing and restores it on Escape', async () => {
      mountFocusHost(strict)
      const user = userEvent.setup()
      const first = screen.getByRole('button', { name: 'First origin' })
      first.focus()
      expect(first).toHaveFocus()
      await user.keyboard('{Enter}')
      const dialog = await screen.findByRole('dialog')
      expect(dialog.contains(document.activeElement)).toBe(true)
      await user.type(
        screen.getByLabelText(/^Subject/),
        'Focus survives a render',
      )
      await user.tab()
      expect(dialog.contains(document.activeElement)).toBe(true)
      await user.keyboard('{Escape}')
      await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
      await waitFor(() => expect(first).toHaveFocus())
      expect(api.sendNotice).not.toHaveBeenCalled()
    })

    it('captures a fresh origin on reopening and restores it after Cancel', async () => {
      mountFocusHost(strict)
      const user = userEvent.setup()
      const first = screen.getByRole('button', { name: 'First origin' })
      const second = screen.getByRole('button', { name: 'Second origin' })
      await user.click(first)
      await user.click(screen.getByRole('button', { name: 'Cancel' }))
      await waitFor(() => expect(first).toHaveFocus())
      await user.click(second)
      expect(screen.getByRole('dialog').contains(document.activeElement)).toBe(
        true,
      )
      await user.click(screen.getByRole('button', { name: 'Cancel' }))
      await waitFor(() => expect(second).toHaveFocus())
    })

    it('restores the connected origin when the open component unmounts', async () => {
      const host = mountFocusHost(strict)
      const user = userEvent.setup()
      const first = screen.getByRole('button', { name: 'First origin' })
      await user.click(first)
      expect(screen.getByRole('dialog').contains(document.activeElement)).toBe(
        true,
      )
      host.removeDialog()
      await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
      await waitFor(() => expect(first).toHaveFocus())
    })

    it('does not focus a removed origin and can capture the next connected origin', async () => {
      const host = mountFocusHost(strict)
      const user = userEvent.setup()
      const first = screen.getByRole('button', { name: 'First origin' })
      const second = screen.getByRole('button', { name: 'Second origin' })
      await user.click(first)
      const restore = vi.spyOn(first, 'focus')
      host.removeFirstOrigin()
      await user.keyboard('{Escape}')
      await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
      expect(first.isConnected).toBe(false)
      expect(restore).not.toHaveBeenCalled()
      await user.click(second)
      await user.keyboard('{Escape}')
      await waitFor(() => expect(second).toHaveFocus())
      restore.mockRestore()
    })

    it('does not let a deferred close steal focus from a new opening', async () => {
      mountFocusHost(strict)
      const user = userEvent.setup()
      const first = screen.getByRole('button', { name: 'First origin' })
      const second = screen.getByRole('button', { name: 'Second origin' })
      await user.click(first)
      const restoreFirst = vi.spyOn(first, 'focus')
      vi.useFakeTimers()
      try {
        fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
        expect(screen.queryByRole('dialog')).toBeNull()
        second.focus()
        fireEvent.click(second)
        const reopened = screen.getByRole('dialog')
        expect(reopened.contains(document.activeElement)).toBe(true)
        act(() => vi.advanceTimersByTime(1))
        expect(restoreFirst).not.toHaveBeenCalled()
        expect(reopened.contains(document.activeElement)).toBe(true)
        fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
        act(() => vi.advanceTimersByTime(1))
        expect(second).toHaveFocus()
      } finally {
        vi.useRealTimers()
        restoreFirst.mockRestore()
      }
    })
  },
)
