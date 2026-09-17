// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The current attempt and any unresolved prior attempt are separate facts, and
// ending an invocation ends the inputs that belonged to it. Measured through the
// real response and offer hosts with only their transport boundary replaced.
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const auth = vi.hoisted(() => ({ perms: new Set<string>() }))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 't1',
    can: (p: string) => auth.perms.has(p),
    isSuperadmin: false,
    principal: {
      kind: 'user',
      user_id: '0192f2c0-eeee-7000-8000-00000000000b',
      actor: 'user:b',
      display_name: 'Bo',
      superadmin: false,
      grants: [],
    },
  }),
}))
vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
  Toaster: () => null,
}))
const api = vi.hoisted(() => ({
  respondToHandoff: vi.fn(),
  offerHandoff: vi.fn(),
  listChannels: vi.fn(),
  listMembers: vi.fn(),
  listAgents: vi.fn(),
}))
vi.mock('./api', async (orig) => {
  const real = (await orig()) as Record<string, unknown>
  return { ...real, ...api }
})
const scopeState = vi.hoisted(() => ({ over: {} as Record<string, unknown> }))
vi.mock('./boundary', async (orig) => {
  const real = (await orig()) as Record<string, unknown>
  const harness = await import('./test-harness')
  return {
    ...real,
    useCommunicationsScope: () => harness.scopeOf(scopeState.over),
  }
})

import { ApiError, NetworkError } from '@/lib/api/errors'
import {
  HandoffOfferHost,
  type HandoffWorkItemReader,
} from './handoff-offer-host'
import { HandoffResponseHost } from './handoff-response-host'
import type { HandoffRespondTarget } from './handoff-respond-dialog'
import {
  CHANNEL_ID,
  deferred,
  handoffOfferResultOf,
  handoffResponseResultOf,
  HANDOFF_DELIVERY_ID,
  HANDOFF_ETAG,
  HANDOFF_ID,
  renderWithQuery,
  scopeOf,
  USER_B,
  WORK_ITEM_ID,
  workItemViewOf,
} from './test-harness'
import './i18n'

const FUTURE = '2026-12-31T10:00'
const PRIOR =
  /An earlier attempt was transmitted and its outcome was never confirmed/i

const targetOf = (transition: 'accept' | 'reject'): HandoffRespondTarget => ({
  handoffId: HANDOFF_ID,
  etag: HANDOFF_ETAG,
  workItemId: WORK_ITEM_ID,
  recipient: { kind: 'user', ref: USER_B },
  deliveryId: HANDOFF_DELIVERY_ID,
  transition,
})

function responseHarness(over: { onRequestFreshDetail?: () => void } = {}) {
  return renderWithQuery(() => (
    <HandoffResponseHost
      scope={scopeOf()}
      canRespond
      canDeliveryRead
      target={targetOf('accept')}
      onTargetConsumed={() => {}}
      onResolved={() => {}}
      onRequestFreshDetail={over.onRequestFreshDetail}
    />
  ))
}

async function dispatchResponse(user: ReturnType<typeof userEvent.setup>) {
  await user.click(
    await screen.findByRole('button', { name: 'Review response' }),
  )
  await user.click(screen.getByRole('button', { name: 'Confirm acceptance' }))
  await waitFor(() => expect(api.respondToHandoff).toHaveBeenCalledTimes(1))
}

function offerHarness(readItem: HandoffWorkItemReader) {
  return renderWithQuery(() => (
    <HandoffOfferHost
      target={{ itemId: WORK_ITEM_ID, invocation: 1 }}
      readItem={readItem}
    />
  ))
}

async function fillAndSendOffer(user: ReturnType<typeof userEvent.setup>) {
  await user.type(screen.getByLabelText('Channel'), CHANNEL_ID)
  await user.type(
    screen.getByLabelText('Reference (ID)'),
    '0192f2c0-eeee-7000-8000-00000000000b',
  )
  await user.type(screen.getByLabelText('Summary'), 'Needs an owner')
  await user.type(screen.getByLabelText('Next action'), 'Confirm the window')
  await user.type(screen.getByLabelText('Response deadline'), FUTURE)
  await user.click(screen.getByRole('button', { name: 'Review offer' }))
  await user.click(screen.getByRole('button', { name: 'Confirm and offer' }))
}

beforeEach(() => {
  vi.clearAllMocks()
  scopeState.over = {}
  auth.perms = new Set([
    'sessions:handoff-response:write',
    'sessions:delivery:read',
    'sessions:message-send:write',
    'sessions:channel:read',
    'user:read',
  ])
  api.listChannels.mockResolvedValue({ items: [], has_more: false })
  api.listMembers.mockResolvedValue({ items: [], has_more: false })
  api.listAgents.mockResolvedValue({ items: [], has_more: false })
})

/* ── D1: a first definitive answer is not an earlier unconfirmed transmission ── */

describe('outcome history is separate from the current attempt', () => {
  it.each([
    [409, 'internal', 'conflict', 'The response did not apply'],
    [412, 'version_mismatch', 'stale', 'The response did not apply'],
    [403, 'forbidden', 'refused', 'The response was refused'],
    [400, 'invalid', 'bad body', 'The response was refused'],
  ] as const)(
    'a first definitive %s response reports only that result',
    async (status, code, message, heading) => {
      api.respondToHandoff.mockRejectedValueOnce(
        new ApiError(status, code, message),
      )
      const user = userEvent.setup()
      responseHarness()
      await dispatchResponse(user)
      expect(await screen.findByText(heading)).toBeVisible()
      // No earlier attempt existed, so none is claimed…
      expect(screen.queryByText(PRIOR)).toBeNull()
      // …and the heading is the unscoped one, because nothing qualifies it.
      expect(screen.queryByText('This attempt did not apply')).toBeNull()
      expect(screen.queryByText('This attempt was refused')).toBeNull()
    },
  )

  it('a first definitive offer conflict reports only that result', async () => {
    api.offerHandoff.mockRejectedValueOnce(
      new ApiError(409, 'internal', 'conflict'),
    )
    const user = userEvent.setup()
    offerHarness(vi.fn().mockResolvedValue(workItemViewOf()))
    await screen.findByRole('dialog', { name: 'Offer handoff' })
    await fillAndSendOffer(user)
    expect(await screen.findByText('The offer did not apply')).toBeVisible()
    expect(screen.queryByText(PRIOR)).toBeNull()
  })

  it('a pending dispatch alone sets no permanent historical warning', async () => {
    const gate = deferred<{
      result: ReturnType<typeof handoffResponseResultOf>
      replayed: boolean
      etag: string
    }>()
    api.respondToHandoff.mockReturnValueOnce(gate.promise)
    const user = userEvent.setup()
    responseHarness()
    await dispatchResponse(user)
    // While pending, the phase itself says the request is on the wire. The
    // disabled control's name carries a non-breaking ellipsis, so it is matched
    // by content rather than by an exact accessible name.
    const pending = screen
      .getAllByRole('button')
      .find((b) => b.textContent?.includes('Responding'))
    expect(pending, 'the pending control must be shown').toBeDefined()
    expect(pending).toBeDisabled()
    expect(screen.queryByText(PRIOR)).toBeNull()
    // And a clean receipt leaves no history behind.
    gate.resolve({
      result: handoffResponseResultOf(),
      replayed: false,
      etag: '"h-v2"',
    })
    expect(await screen.findByText('Responsibility accepted')).toBeVisible()
    expect(screen.queryByText(PRIOR)).toBeNull()
  })

  it('a pre-dispatch guard refusal stays distinct from a possible transmission', async () => {
    const { StaleIntentError } = await import('./intent')
    // A refusal raised by the transport guard: the client cannot tell a first-fetch
    // refusal from a 401-replay refusal, so the outcome is unknown, not unsent.
    api.respondToHandoff.mockRejectedValueOnce(
      new StaleIntentError('credential'),
    )
    const user = userEvent.setup()
    responseHarness()
    await dispatchResponse(user)
    expect(await screen.findByText('Credentials changed')).toBeVisible()
    expect(screen.getByText(/the result has not been confirmed/i)).toBeVisible()
    expect(
      screen.queryByText(/did not start, so this command was not sent/i),
    ).toBeNull()
  })

  it('an actually unknown attempt then a same-key conflict scopes the heading and keeps the history', async () => {
    api.respondToHandoff
      .mockRejectedValueOnce(new NetworkError('response lost'))
      .mockRejectedValueOnce(new ApiError(409, 'internal', 'conflict'))
    const user = userEvent.setup()
    responseHarness()
    await dispatchResponse(user)
    // While the current attempt is itself unknown, its own state says so and the
    // history line would only repeat it.
    expect(await screen.findByText('The result is not known')).toBeVisible()
    expect(screen.queryByText(PRIOR)).toBeNull()

    const [firstIntent] = api.respondToHandoff.mock.calls[0]
    await user.click(
      screen.getByRole('button', { name: 'Retry with the same key' }),
    )
    await waitFor(() => expect(api.respondToHandoff).toHaveBeenCalledTimes(2))
    // The retry is the identical command.
    expect(api.respondToHandoff.mock.calls[1][0]).toBe(firstIntent)

    expect(await screen.findByText('This attempt did not apply')).toBeVisible()
    expect(screen.queryByText('The response did not apply')).toBeNull()
    expect(screen.getByText(PRIOR)).toBeVisible()
  })

  it('an actually unknown attempt then a same-key guard refusal keeps the history', async () => {
    const { StaleIntentError } = await import('./intent')
    api.respondToHandoff
      .mockRejectedValueOnce(new NetworkError('response lost'))
      .mockRejectedValueOnce(new StaleIntentError('credential'))
    const user = userEvent.setup()
    responseHarness()
    await dispatchResponse(user)
    await user.click(
      await screen.findByRole('button', { name: 'Retry with the same key' }),
    )
    await waitFor(() => expect(api.respondToHandoff).toHaveBeenCalledTimes(2))
    expect(await screen.findByText('Credentials changed')).toBeVisible()
    expect(document.body.textContent).not.toMatch(
      /rolled back|was canceled|was cancelled/i,
    )
  })

  it('an exact same-key success resolves the command and clears the history', async () => {
    api.respondToHandoff
      .mockRejectedValueOnce(new NetworkError('response lost'))
      .mockResolvedValueOnce({
        result: handoffResponseResultOf(),
        replayed: true,
        etag: '"h-v2"',
      })
    const user = userEvent.setup()
    responseHarness()
    await dispatchResponse(user)
    const [firstIntent] = api.respondToHandoff.mock.calls[0]
    await user.click(
      await screen.findByRole('button', { name: 'Retry with the same key' }),
    )
    await waitFor(() => expect(api.respondToHandoff).toHaveBeenCalledTimes(2))
    const [second] = api.respondToHandoff.mock.calls[1]
    // Identical command id, key, body, validator and captured scope.
    expect(second).toBe(firstIntent)
    expect(second.key).toBe(firstIntent.key)
    expect(second.etag).toBe(firstIntent.etag)
    expect(second.body).toBe(firstIntent.body)
    expect(second.scope).toEqual(firstIntent.scope)

    expect(
      await screen.findByText('Response already applied (replay)'),
    ).toBeVisible()
    // A receipt for THIS command resolves it, so no warning survives.
    expect(screen.queryByText(PRIOR)).toBeNull()
  })
})

/* ── D2: end the old invocation before any new command ───────────────────────── */

describe('invocation renewal', () => {
  it('Start a new response ends the invocation and asks the detail owner to refresh', async () => {
    api.respondToHandoff.mockRejectedValueOnce(
      new ApiError(409, 'internal', 'conflict'),
    )
    const refresh = vi.fn()
    const user = userEvent.setup()
    responseHarness({ onRequestFreshDetail: refresh })
    await dispatchResponse(user)
    await screen.findByText('The response did not apply')

    await user.click(
      screen.getByRole('button', { name: 'Start a new response' }),
    )
    // The old target, validator and editor are gone: a new response has to be
    // invoked again from a freshly read Delivery detail.
    expect(screen.queryByRole('button', { name: 'Review response' })).toBeNull()
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(document.body.textContent).not.toContain(HANDOFF_ETAG)
    expect(document.body.textContent).not.toContain(HANDOFF_ID)
    // The open detail is asked for a fresh read; the host never reads it itself.
    expect(refresh).toHaveBeenCalledTimes(1)
    // Nothing was unresolved before this conflict, so no stop-tracking limit is
    // claimed either.
    expect(
      screen.queryByText(
        /You stopped tracking a command that may already have been sent/i,
      ),
    ).toBeNull()
  })

  it('Start a new response after an unknown attempt still states the neutral limit', async () => {
    api.respondToHandoff
      .mockRejectedValueOnce(new NetworkError('response lost'))
      .mockRejectedValueOnce(new ApiError(409, 'internal', 'conflict'))
    const user = userEvent.setup()
    responseHarness()
    await dispatchResponse(user)
    await user.click(
      await screen.findByRole('button', { name: 'Retry with the same key' }),
    )
    await waitFor(() => expect(api.respondToHandoff).toHaveBeenCalledTimes(2))
    await screen.findByText('This attempt did not apply')

    await user.click(
      screen.getByRole('button', { name: 'Start a new response' }),
    )
    // Ending the invocation loses the local observation of the earlier attempt,
    // and that limit is stated rather than silently dropped.
    expect(
      screen.getByText(
        /You stopped tracking a command that may already have been sent/i,
      ),
    ).toBeVisible()
    expect(document.body.textContent).not.toMatch(
      /rolled back|was canceled|was cancelled/i,
    )
  })

  it('Stop tracking an unresolved response ends it, states the limit and claims no rollback', async () => {
    const retry = deferred<never>()
    api.respondToHandoff
      .mockRejectedValueOnce(new NetworkError('first response lost'))
      .mockReturnValueOnce(retry.promise)
    const refresh = vi.fn()
    const user = userEvent.setup()
    responseHarness({ onRequestFreshDetail: refresh })
    await dispatchResponse(user)
    await user.click(
      await screen.findByRole('button', { name: 'Retry with the same key' }),
    )
    await waitFor(() => expect(api.respondToHandoff).toHaveBeenCalledTimes(2))
    await user.click(
      screen.getByRole('button', { name: 'Stop tracking this locally' }),
    )

    expect(screen.queryByRole('button', { name: 'Review response' })).toBeNull()
    expect(document.body.textContent).not.toContain(HANDOFF_ETAG)
    expect(
      screen.getByText(
        /You stopped tracking a command that may already have been sent/i,
      ),
    ).toBeVisible()
    expect(document.body.textContent).not.toMatch(
      /rolled back|was canceled|was cancelled/i,
    )
    expect(refresh).toHaveBeenCalledTimes(1)
  })

  it('a late success after stop-tracking cannot publish a receipt', async () => {
    const retry = deferred<{
      result: ReturnType<typeof handoffResponseResultOf>
      replayed: boolean
      etag: string
    }>()
    api.respondToHandoff
      .mockRejectedValueOnce(new NetworkError('lost'))
      .mockReturnValueOnce(retry.promise)
    const user = userEvent.setup()
    responseHarness()
    await dispatchResponse(user)
    await user.click(
      await screen.findByRole('button', { name: 'Retry with the same key' }),
    )
    await waitFor(() => expect(api.respondToHandoff).toHaveBeenCalledTimes(2))
    await user.click(
      screen.getByRole('button', { name: 'Stop tracking this locally' }),
    )
    retry.resolve({
      result: handoffResponseResultOf(),
      replayed: false,
      etag: '"h-v2"',
    })
    await new Promise((r) => setTimeout(r, 0))
    expect(screen.queryByText('Responsibility accepted')).toBeNull()
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('a late failure after stop-tracking cannot publish a refusal', async () => {
    const retry = deferred<never>()
    api.respondToHandoff
      .mockRejectedValueOnce(new NetworkError('lost'))
      .mockReturnValueOnce(retry.promise)
    const user = userEvent.setup()
    responseHarness()
    await dispatchResponse(user)
    await user.click(
      await screen.findByRole('button', { name: 'Retry with the same key' }),
    )
    await waitFor(() => expect(api.respondToHandoff).toHaveBeenCalledTimes(2))
    await user.click(
      screen.getByRole('button', { name: 'Stop tracking this locally' }),
    )
    retry.reject(new ApiError(409, 'internal', 'conflict'))
    await new Promise((r) => setTimeout(r, 0))
    expect(screen.queryByText('The response did not apply')).toBeNull()
    expect(screen.queryByText('This attempt did not apply')).toBeNull()
  })

  it('Stop tracking an unresolved offer rereads the item and clears the draft', async () => {
    const retry = deferred<never>()
    api.offerHandoff
      .mockRejectedValueOnce(new NetworkError('first response lost'))
      .mockReturnValueOnce(retry.promise)
    const readItem = vi
      .fn()
      .mockResolvedValueOnce(workItemViewOf({ etag: '"w-v3"' }))
      .mockResolvedValueOnce(workItemViewOf({ etag: '"w-v9"', owner_epoch: 4 }))
    const user = userEvent.setup()
    offerHarness(readItem)
    await screen.findByRole('dialog', { name: 'Offer handoff' })
    await fillAndSendOffer(user)
    await user.click(
      await screen.findByRole('button', { name: 'Retry with the same key' }),
    )
    await waitFor(() => expect(api.offerHandoff).toHaveBeenCalledTimes(2))
    await user.click(
      screen.getByRole('button', { name: 'Stop tracking this locally' }),
    )

    // A new invocation: the item is read again and the draft is rebuilt.
    await waitFor(() => expect(readItem).toHaveBeenCalledTimes(2))
    expect(screen.getByLabelText('Channel')).toHaveValue('')
    expect(await screen.findByText('"w-v9"')).toBeVisible()
    expect(
      screen.getByText(
        /You stopped tracking a command that may already have been sent/i,
      ),
    ).toBeVisible()

    // And the next offer binds the NEW snapshot, never the old one.
    api.offerHandoff.mockResolvedValueOnce({
      result: handoffOfferResultOf(),
      replayed: false,
      status: 201,
      etag: '"h-v1"',
    })
    await fillAndSendOffer(user)
    await waitFor(() => expect(api.offerHandoff).toHaveBeenCalledTimes(3))
    const [third] = api.offerHandoff.mock.calls[2]
    expect(third.etag).toBe('"w-v9"')
    expect(third.body.expected_owner_epoch).toBe(4)
  })

  it('a new invocation does not inherit the previous one as its own history', async () => {
    api.respondToHandoff.mockRejectedValueOnce(new NetworkError('lost'))
    const user = userEvent.setup()
    responseHarness()
    await dispatchResponse(user)
    expect(await screen.findByText('The result is not known')).toBeVisible()
    await user.click(
      screen.getByRole('button', { name: 'Stop tracking this locally' }),
    )
    expect(
      screen.getByText(
        /You stopped tracking a command that may already have been sent/i,
      ),
    ).toBeVisible()

    // The offer host stands in for "the next command in this room": a separate
    // controller must not present the previous invocation's uncertainty.
    api.offerHandoff.mockRejectedValueOnce(
      new ApiError(409, 'internal', 'conflict'),
    )
    offerHarness(vi.fn().mockResolvedValue(workItemViewOf()))
    await screen.findByRole('dialog', { name: 'Offer handoff' })
    await fillAndSendOffer(user)
    expect(await screen.findByText('The offer did not apply')).toBeVisible()
    expect(screen.queryByText(PRIOR)).toBeNull()
  })
})

/* ── D3: eviction and the reset invariant ────────────────────────────────────── */

describe('protected controller state', () => {
  it('reset refuses an unresolved operation instead of retiring its callback', async () => {
    const gate = deferred<{
      result: ReturnType<typeof handoffResponseResultOf>
      replayed: boolean
      etag: string
    }>()
    api.respondToHandoff.mockReturnValueOnce(gate.promise)
    const user = userEvent.setup()
    responseHarness()
    await dispatchResponse(user)
    // While pending, the only offered ending is the explicit stop-tracking act:
    // no reset control exists that could invalidate the completion callback and
    // leave this pending state on screen.
    expect(
      screen.queryByRole('button', { name: 'Start a new response' }),
    ).toBeNull()
    expect(
      screen.getByRole('button', { name: 'Stop tracking this locally' }),
    ).toBeVisible()
    // The pending operation still resolves normally.
    gate.resolve({
      result: handoffResponseResultOf(),
      replayed: false,
      etag: '"h-v2"',
    })
    expect(await screen.findByText('Responsibility accepted')).toBeVisible()
  })

  it('admission loss evicts every protected artefact and keeps only a neutral notice', async () => {
    const state = { canDeliveryRead: true }
    const rendered = renderWithQuery(() => (
      <HandoffResponseHost
        scope={scopeOf()}
        canRespond
        canDeliveryRead={state.canDeliveryRead}
        target={targetOf('reject')}
        onTargetConsumed={() => {}}
        onResolved={() => {}}
      />
    ))
    const user = userEvent.setup()
    await user.click(await screen.findByLabelText('Reason code'))
    await user.click(await screen.findByRole('option', { name: 'Other' }))
    await user.type(screen.getByLabelText('Explanation'), 'Protected body')
    await user.click(screen.getByRole('button', { name: 'Review response' }))
    const review = await screen.findByLabelText('Review before responding')
    expect(within(review).getByText(HANDOFF_ETAG)).toBeVisible()

    state.canDeliveryRead = false
    rendered.rerender()

    expect(document.body.textContent).not.toContain('Protected body')
    expect(document.body.textContent).not.toContain(HANDOFF_ETAG)
    expect(document.body.textContent).not.toContain(HANDOFF_ID)
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(
      screen.getByText(
        /Local tracking ended because access to this context changed/i,
      ),
    ).toBeVisible()
  })

  it('POSITIVE CONTROL: an admitted panel hide retains the operation and reopens it', async () => {
    const gate = deferred<never>()
    api.respondToHandoff.mockReturnValueOnce(gate.promise)
    const user = userEvent.setup()
    responseHarness()
    await dispatchResponse(user)
    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(
      screen.getByText('A handoff operation is on the wire.'),
    ).toBeVisible()
    await user.click(screen.getByRole('button', { name: 'Show the operation' }))
    expect(await screen.findByRole('dialog')).toBeVisible()
    gate.reject(new NetworkError('lost'))
    expect(await screen.findByText('The result is not known')).toBeVisible()
  })
})
