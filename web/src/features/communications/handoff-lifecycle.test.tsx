// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Operation identity, authority lifetime and recovery, measured through the real
// hosts. The displayed target and the submitted intent are one lifecycle; protected
// state follows admission; a transmitted command stays unresolved regardless of what
// a later observation says; and a resolved operation does not disable the next one.
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
import { HandoffOfferHost } from './handoff-offer-host'
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

const OTHER_HANDOFF = '0192f2c0-8888-7000-8000-0000000000ff'
const OTHER_WORK_ITEM = '0192f2c0-7777-7000-8000-0000000000ff'
const FUTURE = '2026-12-31T10:00'

const targetOf = (
  transition: 'accept' | 'reject',
  over: Partial<HandoffRespondTarget> = {},
): HandoffRespondTarget => ({
  handoffId: HANDOFF_ID,
  etag: HANDOFF_ETAG,
  workItemId: WORK_ITEM_ID,
  recipient: { kind: 'user', ref: USER_B },
  deliveryId: HANDOFF_DELIVERY_ID,
  transition,
  ...over,
})

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
  api.listChannels.mockResolvedValue({
    items: [
      {
        id: CHANNEL_ID,
        name: 'Ops',
        slug: 'ops',
        my_access: { read: true, write: true, admin: false },
      },
    ],
    has_more: false,
  })
  api.listMembers.mockResolvedValue({ items: [], has_more: false })
  api.listAgents.mockResolvedValue({ items: [], has_more: false })
})

/** A response host whose target and admission the test drives. */
function responseHarness(initial: HandoffRespondTarget | null) {
  const state = {
    target: initial,
    canRespond: true,
    canDeliveryRead: true,
  }
  const rendered = renderWithQuery(() => (
    <HandoffResponseHost
      scope={scopeOf()}
      canRespond={state.canRespond}
      canDeliveryRead={state.canDeliveryRead}
      target={state.target}
      onTargetConsumed={() => {}}
      onResolved={() => {}}
    />
  ))
  return { state, ...rendered }
}

/* ── 1. One explicit invocation owns one reviewed intent ─────────────────────── */

describe('operation identity', () => {
  it('a new reject target after an unsent accept review dispatches the NEW one', async () => {
    const { state, rerender } = responseHarness(targetOf('accept'))
    const user = userEvent.setup()
    await user.click(
      await screen.findByRole('button', { name: 'Review response' }),
    )
    // The accept is reviewed but unsent; the operator hides the panel.
    await user.keyboard('{Escape}')

    state.target = targetOf('reject', {
      handoffId: OTHER_HANDOFF,
      workItemId: OTHER_WORK_ITEM,
    })
    rerender()

    const dialog = await screen.findByRole('dialog', { name: 'Reject handoff' })
    // The editor describes the NEW target…
    expect(within(dialog).getByText(OTHER_HANDOFF)).toBeVisible()
    expect(within(dialog).getByText(OTHER_WORK_ITEM)).toBeVisible()
    // …and the stale accept review is gone, so there is nothing to confirm yet.
    expect(
      screen.queryByRole('button', { name: 'Confirm acceptance' }),
    ).toBeNull()
    expect(
      screen.getByRole('button', { name: 'Review response' }),
    ).toBeVisible()

    api.respondToHandoff.mockResolvedValue({
      result: handoffResponseResultOf({ state: 'rejected' }),
      replayed: false,
      etag: '"h-v2"',
    })
    await user.click(screen.getByLabelText('Reason code'))
    await user.click(
      await screen.findByRole('option', { name: 'Not available' }),
    )
    await user.click(screen.getByRole('button', { name: 'Review response' }))
    await user.click(screen.getByRole('button', { name: 'Confirm rejection' }))
    await waitFor(() => expect(api.respondToHandoff).toHaveBeenCalledTimes(1))

    // What was displayed and what travelled agree.
    const [intent] = api.respondToHandoff.mock.calls[0]
    expect(intent.handoffId).toBe(OTHER_HANDOFF)
    expect(intent.workItemId).toBe(OTHER_WORK_ITEM)
    expect(intent.body.transition).toBe('reject')
  })

  it('an UNRESOLVED command is never replaced by a new target; the operator is told', async () => {
    const gate = deferred<never>()
    api.respondToHandoff.mockReturnValueOnce(gate.promise)
    const { state, rerender } = responseHarness(targetOf('accept'))
    const user = userEvent.setup()
    await user.click(
      await screen.findByRole('button', { name: 'Review response' }),
    )
    await user.click(screen.getByRole('button', { name: 'Confirm acceptance' }))
    await waitFor(() => expect(api.respondToHandoff).toHaveBeenCalledTimes(1))
    gate.reject(new NetworkError('dropped'))
    expect(await screen.findByText('The result is not known')).toBeVisible()

    state.target = targetOf('reject', { handoffId: OTHER_HANDOFF })
    rerender()

    // The retained operation is shown, not silently re-aimed.
    expect(
      await screen.findByText('Finish the current response first'),
    ).toBeVisible()
    expect(
      screen.getByRole('button', { name: 'Retry with the same key' }),
    ).toBeVisible()
    // The dialog still describes the operation that is actually outstanding.
    const dialog = screen.getByRole('dialog')
    expect(within(dialog).getByText(HANDOFF_ID)).toBeVisible()
    expect(within(dialog).queryByText(OTHER_HANDOFF)).toBeNull()

    // Retrying sends the original command, not the newly requested one.
    api.respondToHandoff.mockResolvedValueOnce({
      result: handoffResponseResultOf(),
      replayed: true,
      etag: '"h-v2"',
    })
    await user.click(
      screen.getByRole('button', { name: 'Retry with the same key' }),
    )
    await waitFor(() => expect(api.respondToHandoff).toHaveBeenCalledTimes(2))
    expect(api.respondToHandoff.mock.calls[1][0]).toBe(
      api.respondToHandoff.mock.calls[0][0],
    )
  })

  it('the offer host replaces an unsent target and refuses to replace an unresolved one', async () => {
    const readItem = vi.fn().mockResolvedValue(workItemViewOf())
    const gate = deferred<never>()
    const state = { target: { itemId: WORK_ITEM_ID, invocation: 1 } }
    const { rerender } = renderWithQuery(() => (
      <HandoffOfferHost target={state.target} readItem={readItem} />
    ))
    const user = userEvent.setup()
    await screen.findByRole('dialog')

    // A second invocation of the same item is a new editor action.
    state.target = { itemId: WORK_ITEM_ID, invocation: 2 }
    rerender()
    await waitFor(() => expect(readItem).toHaveBeenCalledTimes(2))
    expect(screen.queryByText('Finish the current offer first')).toBeNull()

    // Now make an operation unresolved and try again.
    api.offerHandoff.mockReturnValueOnce(gate.promise)
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
    await waitFor(() => expect(api.offerHandoff).toHaveBeenCalledTimes(1))
    gate.reject(new NetworkError('dropped'))
    expect(await screen.findByText('The result is not known')).toBeVisible()

    state.target = { itemId: OTHER_WORK_ITEM, invocation: 3 }
    rerender()
    expect(
      await screen.findByText('Finish the current offer first'),
    ).toBeVisible()
    // No read of the newly requested item happened: the host did not adopt it.
    expect(readItem).toHaveBeenCalledTimes(2)
  })
})

/* ── 2. Protected content follows current admission ──────────────────────────── */

describe('authority lifetime', () => {
  it.each(['draft', 'review', 'receipt'] as const)(
    'losing delivery-read in the %s state evicts protected content and leaves only a neutral notice',
    async (stage) => {
      const { state, rerender } = responseHarness(targetOf('reject'))
      const user = userEvent.setup()
      await user.click(await screen.findByLabelText('Reason code'))
      await user.click(await screen.findByRole('option', { name: 'Other' }))
      await user.type(
        screen.getByLabelText('Explanation'),
        'Protected rejection body',
      )
      if (stage !== 'draft') {
        await user.click(
          screen.getByRole('button', { name: 'Review response' }),
        )
      }
      if (stage === 'receipt') {
        api.respondToHandoff.mockResolvedValue({
          result: handoffResponseResultOf({ state: 'rejected' }),
          replayed: false,
          etag: '"h-v2"',
        })
        await user.click(
          screen.getByRole('button', { name: 'Confirm rejection' }),
        )
        await screen.findByText('Handoff rejected')
      }
      // The artefact each stage actually shows, before admission is lost.
      if (stage === 'draft') {
        expect(
          screen.getByDisplayValue('Protected rejection body'),
        ).toBeVisible()
      } else if (stage === 'review') {
        expect(screen.getByLabelText('Review before responding')).toBeVisible()
      } else {
        expect(screen.getByText('Handoff rejected')).toBeVisible()
      }

      state.canDeliveryRead = false
      rerender()

      // No draft, no reviewed intent, no receipt, no target identifiers.
      expect(screen.queryByDisplayValue('Protected rejection body')).toBeNull()
      expect(document.body.textContent).not.toContain(
        'Protected rejection body',
      )
      expect(screen.queryByRole('dialog')).toBeNull()
      expect(screen.queryByText('Handoff rejected')).toBeNull()
      expect(document.body.textContent).not.toContain(HANDOFF_ID)
      // Only the neutral notice.
      expect(
        screen.getByText(
          /Local tracking ended because access to this context changed/i,
        ),
      ).toBeVisible()

      // Regaining the read does not resurrect any of it.
      state.canDeliveryRead = true
      rerender()
      expect(screen.queryByDisplayValue('Protected rejection body')).toBeNull()
      expect(screen.queryByText('Handoff rejected')).toBeNull()
    },
  )

  it('a late success after admission loss cannot repopulate protected state', async () => {
    const gate = deferred<{
      result: ReturnType<typeof handoffResponseResultOf>
      replayed: boolean
      etag: string
    }>()
    api.respondToHandoff.mockReturnValueOnce(gate.promise)
    const { state, rerender } = responseHarness(targetOf('accept'))
    const user = userEvent.setup()
    await user.click(
      await screen.findByRole('button', { name: 'Review response' }),
    )
    await user.click(screen.getByRole('button', { name: 'Confirm acceptance' }))
    await waitFor(() => expect(api.respondToHandoff).toHaveBeenCalledTimes(1))

    state.canDeliveryRead = false
    rerender()

    gate.resolve({
      result: handoffResponseResultOf(),
      replayed: false,
      etag: '"h-v2"',
    })
    await new Promise((r) => setTimeout(r, 0))
    expect(screen.queryByText('Responsibility accepted')).toBeNull()
    expect(document.body.textContent).not.toContain(HANDOFF_ETAG)
    // …and the notice says a command had already been sent.
    expect(
      screen.getByText(/Ending local tracking did not cancel or roll it back/i),
    ).toBeVisible()
  })

  it('a late failure after admission loss cannot repopulate protected state either', async () => {
    const gate = deferred<never>()
    api.respondToHandoff.mockReturnValueOnce(gate.promise)
    const { state, rerender } = responseHarness(targetOf('accept'))
    const user = userEvent.setup()
    await user.click(
      await screen.findByRole('button', { name: 'Review response' }),
    )
    await user.click(screen.getByRole('button', { name: 'Confirm acceptance' }))
    await waitFor(() => expect(api.respondToHandoff).toHaveBeenCalledTimes(1))

    state.canDeliveryRead = false
    rerender()
    gate.reject(new ApiError(409, 'internal', 'conflict'))
    await new Promise((r) => setTimeout(r, 0))
    expect(screen.queryByText('The response did not apply')).toBeNull()
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('POSITIVE CONTROL: a still-admitted panel hide keeps the operation and its draft', async () => {
    const { rerender } = responseHarness(targetOf('reject'))
    const user = userEvent.setup()
    await user.click(await screen.findByLabelText('Reason code'))
    await user.click(await screen.findByRole('option', { name: 'Other' }))
    await user.type(screen.getByLabelText('Explanation'), 'Still admitted body')
    await user.click(screen.getByRole('button', { name: 'Review response' }))
    // Hiding the panel while admitted keeps everything.
    await user.keyboard('{Escape}')
    rerender()
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(
      screen.queryByText(/Local tracking ended/i),
      'an admitted hide is not a tracking loss',
    ).toBeNull()
  })
})

/* ── 3. Transmission uncertainty survives later observations ─────────────────── */

describe('transmission uncertainty', () => {
  it('an abort after dispatch stays uncertain and does not restore an editable review', async () => {
    api.respondToHandoff.mockRejectedValueOnce(
      new DOMException('Dropped after dispatch', 'AbortError'),
    )
    const user = userEvent.setup()
    responseHarness(targetOf('accept'))
    await user.click(
      await screen.findByRole('button', { name: 'Review response' }),
    )
    await user.click(screen.getByRole('button', { name: 'Confirm acceptance' }))
    await waitFor(() => expect(api.respondToHandoff).toHaveBeenCalledTimes(1))

    expect(
      await screen.findByRole('button', { name: 'Retry with the same key' }),
    ).toBeVisible()
    expect(
      screen.queryByRole('button', { name: 'Confirm acceptance' }),
    ).toBeNull()
  })

  it('a 409 on the same-key retry ends retry eligibility WITHOUT claiming the first attempt did not apply', async () => {
    api.respondToHandoff
      .mockRejectedValueOnce(new NetworkError('response lost'))
      .mockRejectedValueOnce(new ApiError(409, 'internal', 'conflict'))
    const user = userEvent.setup()
    responseHarness(targetOf('accept'))
    await user.click(
      await screen.findByRole('button', { name: 'Review response' }),
    )
    await user.click(screen.getByRole('button', { name: 'Confirm acceptance' }))
    await user.click(
      await screen.findByRole('button', { name: 'Retry with the same key' }),
    )
    await waitFor(() => expect(api.respondToHandoff).toHaveBeenCalledTimes(2))

    // The heading is scoped to the request that returned it…
    expect(await screen.findByText('This attempt did not apply')).toBeVisible()
    // …and the unqualified statement about the command is not made.
    expect(screen.queryByText('The response did not apply')).toBeNull()
    // The earlier transmission is still openly unresolved.
    expect(
      screen.getByText(
        /An earlier attempt was transmitted and its outcome was never confirmed/i,
      ),
    ).toBeVisible()
    // The old retry is no longer offered.
    expect(
      screen.queryByRole('button', { name: 'Retry with the same key' }),
    ).toBeNull()
    // Nothing claims a rollback or a cancellation.
    expect(document.body.textContent).not.toMatch(
      /rolled back|was canceled|was cancelled/i,
    )
  })

  it('a guard refusal BEFORE the first send is distinguishable from a refusal after one', async () => {
    const { StaleIntentError } = await import('./intent')
    api.respondToHandoff.mockRejectedValueOnce(
      new StaleIntentError('credential'),
    )
    const user = userEvent.setup()
    responseHarness(targetOf('accept'))
    await user.click(
      await screen.findByRole('button', { name: 'Review response' }),
    )
    await user.click(screen.getByRole('button', { name: 'Confirm acceptance' }))
    // The client cannot tell a first-fetch refusal from a 401-replay refusal, so
    // the outcome stays unknown rather than being reported as unsent.
    expect(await screen.findByText('Credentials changed')).toBeVisible()
    expect(screen.getByText(/the result has not been confirmed/i)).toBeVisible()
    expect(
      screen.queryByText(/did not start, so this command was not sent/i),
    ).toBeNull()
  })

  it('the same-key retry re-sends the identical command identity', async () => {
    api.respondToHandoff
      .mockRejectedValueOnce(new NetworkError('lost'))
      .mockResolvedValueOnce({
        result: handoffResponseResultOf(),
        replayed: true,
        etag: '"h-v2"',
      })
    const user = userEvent.setup()
    responseHarness(targetOf('accept'))
    await user.click(
      await screen.findByRole('button', { name: 'Review response' }),
    )
    await user.click(screen.getByRole('button', { name: 'Confirm acceptance' }))
    await user.click(
      await screen.findByRole('button', { name: 'Retry with the same key' }),
    )
    await waitFor(() => expect(api.respondToHandoff).toHaveBeenCalledTimes(2))
    const [first] = api.respondToHandoff.mock.calls[0]
    const [second] = api.respondToHandoff.mock.calls[1]
    expect(second).toBe(first)
    expect(second.key).toBe(first.key)
    expect(second.etag).toBe(first.etag)
    expect(second.scope).toEqual(first.scope)
    expect(second.authority).toEqual(first.authority)
  })
})

/* ── 4. Completed and refused operations do not disable future work ──────────── */

describe('recovery', () => {
  it.each(['confirmed', 'conflict', 'refused'] as const)(
    'after a %s response a different target can be reviewed and sent',
    async (outcome) => {
      if (outcome === 'confirmed') {
        api.respondToHandoff.mockResolvedValue({
          result: handoffResponseResultOf(),
          replayed: false,
          etag: '"h-v2"',
        })
      } else {
        api.respondToHandoff.mockRejectedValue(
          outcome === 'conflict'
            ? new ApiError(409, 'internal', 'conflict')
            : new ApiError(403, 'forbidden', 'refused'),
        )
      }
      const { state, rerender } = responseHarness(targetOf('accept'))
      const user = userEvent.setup()
      await user.click(
        await screen.findByRole('button', { name: 'Review response' }),
      )
      await user.click(
        screen.getByRole('button', { name: 'Confirm acceptance' }),
      )
      await waitFor(() => expect(api.respondToHandoff).toHaveBeenCalledTimes(1))
      await waitFor(() =>
        expect(
          screen.queryByRole('button', { name: 'Submitting response…' }),
        ).toBeNull(),
      )

      // A retained receipt or failure can also be left behind explicitly.
      expect(
        screen.getByRole('button', { name: 'Start a new response' }),
      ).toBeVisible()

      state.target = targetOf('accept', {
        handoffId: OTHER_HANDOFF,
        etag: '"different-handoff-v1"',
      })
      rerender()

      const review = await screen.findByRole('button', {
        name: 'Review response',
      })
      expect(review).toBeVisible()
      api.respondToHandoff.mockResolvedValue({
        result: handoffResponseResultOf(),
        replayed: false,
        etag: '"h-v3"',
      })
      await user.click(review)
      await user.click(
        screen.getByRole('button', { name: 'Confirm acceptance' }),
      )
      await waitFor(() => expect(api.respondToHandoff).toHaveBeenCalledTimes(2))
      // The new command carries the NEW validator, never a rebased old one.
      expect(api.respondToHandoff.mock.calls[1][0].etag).toBe(
        '"different-handoff-v1"',
      )
    },
  )

  it('offer conflict recovery REREADS the item before a new review can bind a validator', async () => {
    const readItem = vi
      .fn()
      .mockResolvedValueOnce(workItemViewOf({ etag: '"w-v3"' }))
      .mockResolvedValueOnce(workItemViewOf({ etag: '"w-v9"', owner_epoch: 4 }))
    api.offerHandoff.mockRejectedValueOnce(
      new ApiError(409, 'internal', 'conflict'),
    )
    const user = userEvent.setup()
    renderWithQuery(() => (
      <HandoffOfferHost
        target={{ itemId: WORK_ITEM_ID, invocation: 1 }}
        readItem={readItem}
      />
    ))
    await screen.findByRole('dialog')
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
    expect(await screen.findByText('The offer did not apply')).toBeVisible()
    expect(readItem).toHaveBeenCalledTimes(1)

    await user.click(screen.getByRole('button', { name: 'Start a new offer' }))
    // Recovery is an observation, not a state-label reset: a fresh invocation
    // rereads the item and rebuilds the editor.
    await waitFor(() => expect(readItem).toHaveBeenCalledTimes(2))
    expect(await screen.findByText('"w-v9"')).toBeVisible()
    expect(screen.getByLabelText('Channel')).toHaveValue('')

    api.offerHandoff.mockResolvedValueOnce({
      result: handoffOfferResultOf(),
      replayed: false,
      status: 201,
      etag: '"h-v1"',
    })
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
    await waitFor(() => expect(api.offerHandoff).toHaveBeenCalledTimes(2))
    // The new validator and epoch come from that one fresh read.
    const [retried] = api.offerHandoff.mock.calls[1]
    expect(retried.etag).toBe('"w-v9"')
    expect(retried.body.expected_owner_epoch).toBe(4)
  })

  it('stopping local tracking retires the callback and claims no rollback', async () => {
    const gate = deferred<{
      result: ReturnType<typeof handoffResponseResultOf>
      replayed: boolean
      etag: string
    }>()
    api.respondToHandoff
      .mockRejectedValueOnce(new NetworkError('lost'))
      .mockReturnValueOnce(gate.promise)
    const user = userEvent.setup()
    responseHarness(targetOf('accept'))
    await user.click(
      await screen.findByRole('button', { name: 'Review response' }),
    )
    await user.click(screen.getByRole('button', { name: 'Confirm acceptance' }))
    await user.click(
      await screen.findByRole('button', { name: 'Retry with the same key' }),
    )
    await waitFor(() => expect(api.respondToHandoff).toHaveBeenCalledTimes(2))
    // Stopping tracking is reachable while the retry is still in flight.
    await user.click(
      await screen.findByRole('button', { name: 'Stop tracking this locally' }),
    )
    gate.resolve({
      result: handoffResponseResultOf(),
      replayed: false,
      etag: '"h-v2"',
    })
    await new Promise((r) => setTimeout(r, 0))

    // The retired callback does not publish a receipt into the cleared state…
    expect(screen.queryByText('Responsibility accepted')).toBeNull()
    // …the invocation ended, so its editor, target and validator are gone…
    expect(screen.queryByRole('button', { name: 'Review response' })).toBeNull()
    expect(document.body.textContent).not.toContain(HANDOFF_ETAG)
    // …and the neutral limit is stated where the operator can read it.
    expect(
      screen.getByText(
        /You stopped tracking a command that may already have been sent/i,
      ),
    ).toBeVisible()
    expect(document.body.textContent).not.toMatch(
      /rolled back|was canceled|was cancelled/i,
    )
  })
})

/* ── 8. Local tracking limits ────────────────────────────────────────────────── */

describe('tracking limits', () => {
  it('states that hiding retains tracking and that stopping it cannot cancel a sent command', async () => {
    responseHarness(targetOf('accept'))
    await screen.findByRole('dialog')
    const limits = document.querySelector(
      '[data-slot="handoff-respond-tracking-limits"]',
    ) as HTMLElement
    expect(limits).toHaveTextContent(/Hiding this panel keeps local tracking/i)
    expect(limits).toHaveTextContent(
      /cannot cancel or roll back a command already sent/i,
    )
    expect(limits).toHaveTextContent(/full reload ends this local observer/i)
    expect(limits).toHaveTextContent(/no command-status lookup here/i)
  })

  it('allows Escape while the command is pending and keeps tracking it', async () => {
    const gate = deferred<never>()
    api.respondToHandoff.mockReturnValueOnce(gate.promise)
    const user = userEvent.setup()
    responseHarness(targetOf('accept'))
    await user.click(
      await screen.findByRole('button', { name: 'Review response' }),
    )
    await user.click(screen.getByRole('button', { name: 'Confirm acceptance' }))
    await waitFor(() => expect(api.respondToHandoff).toHaveBeenCalledTimes(1))

    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    // The host keeps tracking and offers the way back.
    expect(
      screen.getByText('A handoff operation is on the wire.'),
    ).toBeVisible()
    await user.click(screen.getByRole('button', { name: 'Show the operation' }))
    expect(await screen.findByRole('dialog')).toBeVisible()
    // The classification did not change while hidden.
    // The pending control is still the pending control: hiding the panel did not
    // reclassify the outcome.
    const pending = screen
      .getAllByRole('button')
      .find((b) => b.textContent?.includes('Responding'))
    expect(pending, 'the submitting control must still be shown').toBeDefined()
    expect(pending).toBeDisabled()
  })
})
