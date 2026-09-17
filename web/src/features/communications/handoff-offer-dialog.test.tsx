// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE OFFER, through the real host: a fresh work-item read, an exact workspace
// match, a frozen intent, and a receipt that does not claim a transfer.
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
      user_id: '0192f2c0-eeee-7000-8000-00000000000a',
      actor: 'user:a',
      display_name: 'Ada',
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

import { HandoffOfferHost } from './handoff-offer-host'
import {
  CHANNEL_ID,
  deferred,
  handoffOfferResultOf,
  renderWithQuery,
  WORK_ITEM_ID,
  workItemViewOf,
  WS,
  WS2,
} from './test-harness'
import './i18n'

const FUTURE = '2026-12-31T10:00'

beforeEach(() => {
  vi.clearAllMocks()
  scopeState.over = {}
  auth.perms = new Set([
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

const target = { itemId: WORK_ITEM_ID, invocation: 1 }

/** Fill the minimum a valid offer needs, leaving the deadline to the caller. */
async function fillOffer(
  user: ReturnType<typeof userEvent.setup>,
  over: { deadline?: string; channel?: string } = {},
) {
  await user.type(screen.getByLabelText('Channel'), over.channel ?? CHANNEL_ID)
  await user.type(
    screen.getByLabelText('Reference (ID)'),
    '0192f2c0-eeee-7000-8000-00000000000b',
  )
  await user.type(screen.getByLabelText('Summary'), 'Freeze needs an owner')
  await user.type(screen.getByLabelText('Next action'), 'Confirm the window')
  if (over.deadline !== undefined) {
    await user.type(screen.getByLabelText('Response deadline'), over.deadline)
  }
}

describe('the offer reads the work item FRESH through the adapter', () => {
  it('asks the adapter with the requested id and an abort signal, and never uses a cached body', async () => {
    const readItem = vi.fn().mockResolvedValue(workItemViewOf())
    renderWithQuery(() => (
      <HandoffOfferHost target={target} readItem={readItem} />
    ))
    await waitFor(() =>
      expect(readItem).toHaveBeenCalledWith(
        WORK_ITEM_ID,
        expect.any(AbortSignal),
      ),
    )
    const dialog = await screen.findByRole('dialog')
    // The read's own facts are on screen: identity, current owner, epoch and the
    // server ETag that will become the precondition.
    expect(within(dialog).getByText('Freeze the deploy window')).toBeVisible()
    expect(within(dialog).getByText('"w-v3"')).toBeVisible()
  })

  it('re-reads on demand, and a REPLACEMENT read replaces the previous answer rather than merging with it', async () => {
    const readItem = vi
      .fn()
      .mockResolvedValueOnce(workItemViewOf({ title: 'First read' }))
      .mockResolvedValueOnce(
        workItemViewOf({ title: 'Second read', etag: '"w-v9"' }),
      )
    const user = userEvent.setup()
    renderWithQuery(() => (
      <HandoffOfferHost target={target} readItem={readItem} />
    ))
    expect(await screen.findByText('First read')).toBeVisible()
    await user.click(screen.getByRole('button', { name: 'Re-read' }))
    expect(await screen.findByText('Second read')).toBeVisible()
    await waitFor(() => expect(screen.queryByText('First read')).toBeNull())
    expect(screen.getByText('"w-v9"')).toBeVisible()
  })
})

describe('the exact selected-workspace match', () => {
  it('refuses to offer an item of ANOTHER workspace, says which one to select, and sends nothing', async () => {
    const readItem = vi
      .fn()
      .mockResolvedValue(workItemViewOf({ workspace_id: WS2 }))
    renderWithQuery(() => (
      <HandoffOfferHost target={target} readItem={readItem} />
    ))
    const banner = await screen
      .findByRole('alert', {
        name: undefined,
      })
      .catch(() => null)
    void banner
    const mismatch = document.querySelector(
      '[data-slot="handoff-offer-workspace-mismatch"]',
    ) as HTMLElement | null
    expect(
      mismatch,
      'the workspace mismatch banner must be rendered',
    ).not.toBeNull()
    expect(mismatch).toHaveTextContent("Select the item's workspace")
    expect(mismatch).toHaveTextContent(
      new RegExp(`belongs to workspace ${WS2}`),
    )
    expect(mismatch).toHaveTextContent(/never selects one for you/i)
    // The form is not offered at all, so there is nothing to confirm…
    expect(screen.queryByLabelText('Summary')).toBeNull()
    // …and the review control is disabled rather than merely unhelpful.
    expect(screen.getByRole('button', { name: 'Review offer' })).toBeDisabled()
    expect(api.offerHandoff).not.toHaveBeenCalled()
  })

  it('refuses with NO explicit workspace, and never selects one on the operator behalf', async () => {
    scopeState.over = { workspace: null, workspaceName: null }
    const readItem = vi.fn().mockResolvedValue(workItemViewOf())
    renderWithQuery(() => (
      <HandoffOfferHost target={target} readItem={readItem} />
    ))
    expect(
      await screen.findByText(/No explicit workspace is selected/i),
    ).toBeVisible()
    expect(screen.getByRole('button', { name: 'Review offer' })).toBeDisabled()
    expect(api.offerHandoff).not.toHaveBeenCalled()
  })

  it('POSITIVE CONTROL: the SAME item in the selected workspace can be reviewed', async () => {
    const readItem = vi
      .fn()
      .mockResolvedValue(workItemViewOf({ workspace_id: WS }))
    renderWithQuery(() => (
      <HandoffOfferHost target={target} readItem={readItem} />
    ))
    await screen.findByRole('dialog')
    expect(screen.queryByText(/Select the item's workspace/i)).toBeNull()
    expect(screen.getByRole('button', { name: 'Review offer' })).toBeEnabled()
  })

  it('refuses when the read carried NO ETag: there is no precondition, and none is invented', async () => {
    const readItem = vi.fn().mockResolvedValue(workItemViewOf({ etag: null }))
    renderWithQuery(() => (
      <HandoffOfferHost target={target} readItem={readItem} />
    ))
    expect(
      await screen.findByText(/carried no ETag, so there is no precondition/i),
    ).toBeVisible()
    expect(screen.getByRole('button', { name: 'Review offer' })).toBeDisabled()
  })

  it('refuses when the adapter answered about a DIFFERENT item', async () => {
    const readItem = vi
      .fn()
      .mockResolvedValue(
        workItemViewOf({ id: '0192f2c0-7777-7000-8000-0000000000ff' }),
      )
    renderWithQuery(() => (
      <HandoffOfferHost target={target} readItem={readItem} />
    ))
    expect(
      await screen.findByText(/returned a different work item/i),
    ).toBeVisible()
    expect(screen.getByRole('button', { name: 'Review offer' })).toBeDisabled()
  })
})

describe('confirmation binds the same read ETag and owner epoch, and the receipt stays honest', () => {
  it('freezes the intent, sends ONE operation on a double click, and says a response is pending', async () => {
    const readItem = vi.fn().mockResolvedValue(workItemViewOf())
    api.offerHandoff.mockResolvedValue({
      result: handoffOfferResultOf(),
      replayed: false,
      status: 201,
      etag: '"h-v1"',
    })
    const user = userEvent.setup()
    renderWithQuery(() => (
      <HandoffOfferHost target={target} readItem={readItem} />
    ))
    await screen.findByRole('dialog')
    await fillOffer(user, { deadline: FUTURE })
    await user.click(screen.getByRole('button', { name: 'Review offer' }))

    // The review shows what will travel — including BOTH coordinates of the read.
    const review = await screen.findByLabelText('Review before offering')
    expect(within(review).getByText('"w-v3"')).toBeVisible()
    expect(within(review).getByText(WORK_ITEM_ID)).toBeVisible()

    const confirm = screen.getByRole('button', { name: 'Confirm and offer' })
    await user.dblClick(confirm)
    await waitFor(() => expect(api.offerHandoff).toHaveBeenCalledTimes(1))

    const [intent] = api.offerHandoff.mock.calls[0]
    expect(intent.etag).toBe('"w-v3"')
    expect(intent.body.expected_owner_epoch).toBe(1)
    expect(intent.body.work_item_id).toBe(WORK_ITEM_ID)
    expect(intent.body.channel_id).toBe(CHANNEL_ID)
    // The deadline travels as RFC 3339 UTC with seconds, not as the local string.
    expect(intent.body.ack_deadline).toMatch(
      /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$/,
    )

    const receipt = await screen.findByLabelText('Receipt')
    expect(within(receipt).getByText('Offer created')).toBeVisible()
    // THE SENTENCE THIS TEST EXISTS FOR.
    expect(
      within(receipt).getByText(/Ownership has not transferred/i),
    ).toBeVisible()
    expect(within(receipt).queryByText(/lease/i)).toBeNull()
  })

  it('a replay is SAID to be a replay, and still does not claim a transfer', async () => {
    const readItem = vi.fn().mockResolvedValue(workItemViewOf())
    api.offerHandoff.mockResolvedValue({
      result: handoffOfferResultOf({ replayed: true }),
      replayed: true,
      status: 200,
      etag: '"h-v1"',
    })
    const user = userEvent.setup()
    renderWithQuery(() => (
      <HandoffOfferHost target={target} readItem={readItem} />
    ))
    await screen.findByRole('dialog')
    await fillOffer(user, { deadline: FUTURE })
    await user.click(screen.getByRole('button', { name: 'Review offer' }))
    await user.click(screen.getByRole('button', { name: 'Confirm and offer' }))
    expect(
      await screen.findByText('Offer already applied (replay)'),
    ).toBeVisible()
    expect(screen.getByText(/Ownership has not transferred/i)).toBeVisible()
  })

  it('refuses an EMPTY deadline and a PAST one, and never silently chooses or extends one', async () => {
    const readItem = vi.fn().mockResolvedValue(workItemViewOf())
    const user = userEvent.setup()
    renderWithQuery(() => (
      <HandoffOfferHost target={target} readItem={readItem} />
    ))
    await screen.findByRole('dialog')
    // The field starts EMPTY: the console picked nothing.
    expect(screen.getByLabelText('Response deadline')).toHaveValue('')
    await fillOffer(user)
    await user.click(screen.getByRole('button', { name: 'Review offer' }))
    const problems = await screen.findByTestId('offer-problems')
    expect(problems).toHaveTextContent('A response deadline is required.')
    // The same problem is also attached to the field it belongs to.
    expect(screen.getByLabelText('Response deadline')).toHaveAttribute(
      'aria-invalid',
      'true',
    )
    expect(api.offerHandoff).not.toHaveBeenCalled()

    await user.type(
      screen.getByLabelText('Response deadline'),
      '2020-01-01T10:00',
    )
    await user.click(screen.getByRole('button', { name: 'Review offer' }))
    await waitFor(() =>
      expect(screen.getByTestId('offer-problems')).toHaveTextContent(
        'The response deadline must be later than the current time.',
      ),
    )
    expect(api.offerHandoff).not.toHaveBeenCalled()
  })

  it('shows the deadline in BOTH coordinates before it is confirmed', async () => {
    const readItem = vi.fn().mockResolvedValue(workItemViewOf())
    const user = userEvent.setup()
    renderWithQuery(() => (
      <HandoffOfferHost target={target} readItem={readItem} />
    ))
    await screen.findByRole('dialog')
    await user.type(screen.getByLabelText('Response deadline'), FUTURE)
    const review = await screen.findByText(/sent as .*Z\./)
    expect(review).toBeVisible()
    expect(review.textContent).toMatch(/The server clock is final/)
  })
})

describe('the operation outlives the dialog', () => {
  it('keeps an UNCERTAIN operation when the panel is closed, offers to reopen it, and retries the SAME intent', async () => {
    const readItem = vi.fn().mockResolvedValue(workItemViewOf())
    const gate = deferred<never>()
    api.offerHandoff.mockReturnValueOnce(gate.promise)
    const user = userEvent.setup()
    renderWithQuery(() => (
      <HandoffOfferHost target={target} readItem={readItem} />
    ))
    await screen.findByRole('dialog')
    await fillOffer(user, { deadline: FUTURE })
    await user.click(screen.getByRole('button', { name: 'Review offer' }))
    await user.click(screen.getByRole('button', { name: 'Confirm and offer' }))
    await waitFor(() => expect(api.offerHandoff).toHaveBeenCalledTimes(1))
    const [sentIntent] = api.offerHandoff.mock.calls[0]

    // The transport drops the response.
    gate.reject(new (await import('@/lib/api/errors')).NetworkError('dropped'))
    expect(await screen.findByText('The result is not known')).toBeVisible()
    expect(screen.getByText(/A timeout is not a rollback/i)).toBeVisible()

    // Closing the panel — through the dialog's own close control, which is the only
    // one — does NOT turn the transmitted request into a draft.
    await user.click(screen.getByRole('button', { name: 'Close' }))
    const retained = await screen.findByText(
      'A handoff operation has no confirmed result.',
    )
    expect(retained).toBeVisible()

    // Reopening and retrying re-sends the IDENTICAL object under the same key.
    api.offerHandoff.mockResolvedValueOnce({
      result: handoffOfferResultOf({ replayed: true }),
      replayed: true,
      status: 200,
      etag: '"h-v1"',
    })
    await user.click(screen.getByRole('button', { name: 'Show the operation' }))
    await user.click(
      screen.getByRole('button', { name: 'Retry with the same key' }),
    )
    await waitFor(() => expect(api.offerHandoff).toHaveBeenCalledTimes(2))
    expect(api.offerHandoff.mock.calls[1][0]).toBe(sentIntent)
  })

  it('a CONFLICT kills the intent and requires a newly reviewed one; the ETag is never replaced and resent', async () => {
    const readItem = vi.fn().mockResolvedValue(workItemViewOf())
    const { ApiError } = await import('@/lib/api/errors')
    api.offerHandoff.mockRejectedValueOnce(
      new ApiError(409, 'internal', 'conflict', undefined, {
        error: { message: 'conflict' },
      }),
    )
    const user = userEvent.setup()
    renderWithQuery(() => (
      <HandoffOfferHost target={target} readItem={readItem} />
    ))
    await screen.findByRole('dialog')
    await fillOffer(user, { deadline: FUTURE })
    await user.click(screen.getByRole('button', { name: 'Review offer' }))
    await user.click(screen.getByRole('button', { name: 'Confirm and offer' }))

    expect(await screen.findByText('The offer did not apply')).toBeVisible()
    expect(screen.getByText(/This response does not say which/i)).toBeVisible()
    // No retry control at all: the only forward path is a new review.
    expect(
      screen.queryByRole('button', { name: 'Retry with the same key' }),
    ).toBeNull()
    expect(
      screen.getByRole('button', { name: 'Start a new offer' }),
    ).toBeVisible()
  })

  it('a credential change mid-flight uses NEUTRAL copy and never claims that nothing was applied', async () => {
    const readItem = vi.fn().mockResolvedValue(workItemViewOf())
    const { StaleIntentError } = await import('./intent')
    api.offerHandoff.mockRejectedValueOnce(new StaleIntentError('credential'))
    const user = userEvent.setup()
    renderWithQuery(() => (
      <HandoffOfferHost target={target} readItem={readItem} />
    ))
    await screen.findByRole('dialog')
    await fillOffer(user, { deadline: FUTURE })
    await user.click(screen.getByRole('button', { name: 'Review offer' }))
    await user.click(screen.getByRole('button', { name: 'Confirm and offer' }))

    expect(await screen.findByText('Credentials changed')).toBeVisible()
    expect(screen.getByText(/the result has not been confirmed/i)).toBeVisible()
    // The inherited transport sentence must NOT reach this surface: the shared
    //    client may have sent a first leg, taken a 401 and refused the replay.
    expect(screen.queryByText(/nothing was sent/i)).toBeNull()
  })
})

describe('the offer is independent of the directory and of the work permissions', () => {
  it('offers manual channel and recipient entry with NO directory permission at all', async () => {
    auth.perms = new Set(['sessions:message-send:write'])
    const readItem = vi.fn().mockResolvedValue(workItemViewOf())
    renderWithQuery(() => (
      <HandoffOfferHost target={target} readItem={readItem} />
    ))
    await screen.findByRole('dialog')
    // No catalog read is even attempted…
    expect(api.listChannels).not.toHaveBeenCalled()
    expect(screen.queryByLabelText('Pick a channel')).toBeNull()
    // …and BOTH references remain typeable, which is the point.
    expect(screen.getByLabelText('Channel')).toBeEnabled()
    expect(screen.getByLabelText('Reference (ID)')).toBeEnabled()
    expect(
      screen.getByText(/channel catalog is not readable with this permission/i),
    ).toBeVisible()
  })

  it('session is offered as a supported recipient kind and labelled as a session reference', async () => {
    const readItem = vi.fn().mockResolvedValue(workItemViewOf())
    const user = userEvent.setup()
    renderWithQuery(() => (
      <HandoffOfferHost target={target} readItem={readItem} />
    ))
    await screen.findByRole('dialog')
    await user.click(screen.getByLabelText('Kind'))
    expect(
      await screen.findByRole('option', { name: 'Session' }),
    ).toBeInTheDocument()
  })

  it('without the send permission the dialog says so and offers no confirmation', async () => {
    auth.perms = new Set(['sessions:channel:read'])
    const readItem = vi.fn().mockResolvedValue(workItemViewOf())
    renderWithQuery(() => (
      <HandoffOfferHost target={target} readItem={readItem} />
    ))
    expect(
      await screen.findByText(/requires sessions:message-send:write/i),
    ).toBeVisible()
    expect(screen.getByRole('button', { name: 'Review offer' })).toBeDisabled()
  })
})

describe('accessible naming and error association on the composed offer dialog', () => {
  it('names the dialog visibly and associates its validation errors as an alert', async () => {
    const readItem = vi.fn().mockResolvedValue(workItemViewOf())
    const user = userEvent.setup()
    renderWithQuery(() => (
      <HandoffOfferHost target={target} readItem={readItem} />
    ))
    const dialog = await screen.findByRole('dialog', { name: 'Offer handoff' })
    expect(within(dialog).getByText('Offer handoff')).toBeVisible()

    await user.click(screen.getByRole('button', { name: 'Review offer' }))
    // The summary is announced, and every offending field is marked and described
    // by its own message rather than only coloured.
    const summary = await screen.findByTestId('offer-problems')
    expect(summary).toHaveAttribute('role', 'alert')
    expect(summary).toHaveTextContent('A channel is required.')
    expect(summary).toHaveTextContent('A response deadline is required.')
    for (const label of [
      'Channel',
      'Summary',
      'Next action',
      'Response deadline',
    ]) {
      const control = screen.getByLabelText(label)
      expect(control, `${label} must be marked invalid`).toHaveAttribute(
        'aria-invalid',
        'true',
      )
      const describedBy = control.getAttribute('aria-describedby') ?? ''
      const ids = describedBy.split(/\s+/).filter(Boolean)
      const texts = ids
        .map((id) => document.getElementById(id)?.textContent ?? '')
        .join(' ')
      expect(texts, `${label} must be described by its own error`).toMatch(
        /required/i,
      )
    }
    expect(api.offerHandoff).not.toHaveBeenCalled()
  })

  it('the offer can be completed and confirmed without a pointer', async () => {
    const readItem = vi.fn().mockResolvedValue(workItemViewOf())
    api.offerHandoff.mockResolvedValue({
      result: handoffOfferResultOf(),
      replayed: false,
      status: 201,
      etag: '"h-v1"',
    })
    const user = userEvent.setup()
    renderWithQuery(() => (
      <HandoffOfferHost target={target} readItem={readItem} />
    ))
    await screen.findByRole('dialog')

    // Tabbing from the top of the trapped dialog walks the FORM fields in order.
    const walked: string[] = []
    for (let i = 0; i < 8; i++) {
      await user.tab()
      const active = document.activeElement as HTMLElement | null
      if (active)
        walked.push(active.getAttribute('aria-label') ?? active.id ?? '')
    }
    expect(walked.join(' | ')).toMatch(/-channel/)
    expect(walked.join(' | ')).toMatch(/Kind/)

    // Every field is typed into through the keyboard, with no click at all…
    screen.getByLabelText('Channel').focus()
    await user.keyboard(CHANNEL_ID)
    screen.getByLabelText('Reference (ID)').focus()
    await user.keyboard('0192f2c0-eeee-7000-8000-00000000000b')
    screen.getByLabelText('Summary').focus()
    await user.keyboard('Freeze needs an owner')
    screen.getByLabelText('Next action').focus()
    await user.keyboard('Confirm the window')
    // `skipClick` keeps this keyboard-only — `type` would otherwise click first —
    // so the field has to be FOCUSED first, exactly as Tab would leave it.
    const deadline = screen.getByLabelText('Response deadline')
    deadline.focus()
    await user.type(deadline, FUTURE, { skipClick: true })

    // …and both footer actions are operated with Enter.
    const review = screen.getByRole('button', { name: 'Review offer' })
    review.focus()
    expect(document.activeElement).toBe(review)
    await user.keyboard('{Enter}')
    // If validation had refused, this is where the case fails loudly rather than
    // silently proving nothing.
    expect(
      screen.queryByRole('alert')?.textContent ?? '',
      'keyboard-only entry must satisfy the same validation a pointer would',
    ).toBe('')
    const confirm = await screen.findByRole('button', {
      name: 'Confirm and offer',
    })
    confirm.focus()
    await user.keyboard('{Enter}')
    await waitFor(() => expect(api.offerHandoff).toHaveBeenCalledTimes(1))
  })
})
