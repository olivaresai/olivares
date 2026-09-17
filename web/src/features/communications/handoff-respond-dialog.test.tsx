// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ACCEPT AND REJECT, through the room's own operation host: the response is bound to
// the handoff ETag, an accept sends no reason, a reject cannot submit without one,
// and the accepted receipt names a transferred ownership WITHOUT inventing a lease.
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
const api = vi.hoisted(() => ({ respondToHandoff: vi.fn() }))
vi.mock('./api', async (orig) => {
  const real = (await orig()) as Record<string, unknown>
  return { ...real, ...api }
})

import { ApiError, NetworkError } from '@/lib/api/errors'
import { HandoffResponseHost } from './handoff-response-host'
import type { HandoffRespondTarget } from './handoff-respond-dialog'
import {
  deferred,
  handoffResponseResultOf,
  HANDOFF_DELIVERY_ID,
  HANDOFF_ETAG,
  HANDOFF_ID,
  renderWithQuery,
  scopeOf,
  USER_B,
  WORK_ITEM_ID,
} from './test-harness'
import './i18n'

const targetOf = (transition: 'accept' | 'reject'): HandoffRespondTarget => ({
  handoffId: HANDOFF_ID,
  etag: HANDOFF_ETAG,
  workItemId: WORK_ITEM_ID,
  recipient: { kind: 'user', ref: USER_B },
  deliveryId: HANDOFF_DELIVERY_ID,
  transition,
})

beforeEach(() => {
  vi.clearAllMocks()
  auth.perms = new Set([
    'sessions:handoff-response:write',
    'sessions:delivery:read',
  ])
})

function mount(
  transition: 'accept' | 'reject',
  over: { canRespond?: boolean } = {},
) {
  return renderWithQuery(() => (
    <HandoffResponseHost
      scope={scopeOf()}
      canRespond={over.canRespond ?? true}
      canDeliveryRead
      target={targetOf(transition)}
      onTargetConsumed={() => {}}
      onResolved={() => {}}
    />
  ))
}

describe('accept', () => {
  it('names the WorkItem and the recipient, sends NO reason, and binds the handoff ETag', async () => {
    api.respondToHandoff.mockResolvedValue({
      result: handoffResponseResultOf(),
      replayed: false,
      etag: '"h-v2"',
    })
    const user = userEvent.setup()
    mount('accept')
    const dialog = await screen.findByRole('dialog')
    // The confirmation states WHAT is being accepted and FOR WHOM.
    expect(within(dialog).getByText(WORK_ITEM_ID)).toBeVisible()
    expect(within(dialog).getByText(`user:${USER_B}`)).toBeVisible()
    expect(within(dialog).getByText(HANDOFF_ETAG)).toBeVisible()

    await user.click(screen.getByRole('button', { name: 'Review response' }))
    await user.click(screen.getByRole('button', { name: 'Confirm acceptance' }))
    await waitFor(() => expect(api.respondToHandoff).toHaveBeenCalledTimes(1))
    const [intent] = api.respondToHandoff.mock.calls[0]
    expect(intent.body).toEqual({ transition: 'accept' })
    expect(intent.etag).toBe(HANDOFF_ETAG)
    expect(intent.handoffId).toBe(HANDOFF_ID)
  })

  it('the accepted receipt shows the advanced owner epoch and says NO lease was acquired', async () => {
    // The REAL R45 path: a vacant generation, so the response carries no fence.
    api.respondToHandoff.mockResolvedValue({
      result: handoffResponseResultOf({ owner_epoch: 2 }),
      replayed: false,
      etag: '"h-v2"',
    })
    const user = userEvent.setup()
    mount('accept')
    await screen.findByRole('dialog')
    await user.click(screen.getByRole('button', { name: 'Review response' }))
    await user.click(screen.getByRole('button', { name: 'Confirm acceptance' }))

    const receipt = await screen.findByLabelText('Receipt')
    expect(within(receipt).getByText('Responsibility accepted')).toBeVisible()
    expect(within(receipt).getByText(/owner epoch advanced/i)).toBeVisible()
    expect(within(receipt).getByText('2')).toBeVisible()
    // NO FENCE ROW AT ALL, and the sentence says why.
    expect(within(receipt).queryByText('Resulting lease fence')).toBeNull()
    expect(
      screen.getByText(
        /no execution lease was acquired and no session is running/i,
      ),
    ).toBeVisible()
  })

  it('a response that DOES carry a fence shows it, so the absence above is a real signal', async () => {
    api.respondToHandoff.mockResolvedValue({
      result: handoffResponseResultOf({ resulting_lease_fence: 7 }),
      replayed: false,
      etag: '"h-v2"',
    })
    const user = userEvent.setup()
    mount('accept')
    await screen.findByRole('dialog')
    await user.click(screen.getByRole('button', { name: 'Review response' }))
    await user.click(screen.getByRole('button', { name: 'Confirm acceptance' }))
    const receipt = await screen.findByLabelText('Receipt')
    expect(within(receipt).getByText('Resulting lease fence')).toBeVisible()
    expect(within(receipt).getByText('7')).toBeVisible()
    expect(screen.queryByText(/no execution lease was acquired/i)).toBeNull()
  })
})

describe('reject', () => {
  it('cannot submit with an EMPTY reason code', async () => {
    const user = userEvent.setup()
    mount('reject')
    await screen.findByRole('dialog')
    await user.click(screen.getByRole('button', { name: 'Review response' }))
    const problems = await screen.findByTestId('respond-problems')
    expect(problems).toHaveTextContent('A reason code is required.')
    // The reason control itself is marked and described by that message.
    const code = screen.getByLabelText('Reason code')
    expect(code).toHaveAttribute('aria-invalid', 'true')
    expect(code).toHaveAttribute('aria-required', 'true')
    const describedBy = code.getAttribute('aria-describedby') ?? ''
    const texts = describedBy
      .split(/\s+/)
      .filter(Boolean)
      .map((id) => document.getElementById(id)?.textContent ?? '')
      .join(' ')
    expect(texts).toMatch(/A reason code is required\./)
    expect(api.respondToHandoff).not.toHaveBeenCalled()
  })

  it('sends a preset code with its optional text and typed references', async () => {
    api.respondToHandoff.mockResolvedValue({
      result: handoffResponseResultOf({ state: 'rejected' }),
      replayed: false,
      etag: '"h-v2"',
    })
    const user = userEvent.setup()
    mount('reject')
    await screen.findByRole('dialog')
    await user.click(screen.getByLabelText('Reason code'))
    await user.click(
      await screen.findByRole('option', { name: 'Outside my scope' }),
    )
    await user.type(
      screen.getByLabelText('Explanation'),
      'Platform owns the freeze window.',
    )
    await user.click(screen.getByRole('button', { name: 'Add reference' }))
    await user.type(screen.getByLabelText('Kind'), 'runbook')
    await user.type(screen.getByLabelText('Reference'), 'rb-1')
    await user.click(screen.getByRole('button', { name: 'Review response' }))
    await user.click(screen.getByRole('button', { name: 'Confirm rejection' }))

    await waitFor(() => expect(api.respondToHandoff).toHaveBeenCalledTimes(1))
    expect(api.respondToHandoff.mock.calls[0][0].body).toEqual({
      transition: 'reject',
      reason: {
        code: 'outside_scope',
        text: 'Platform owns the freeze window.',
        references: [{ kind: 'runbook', ref: 'rb-1' }],
      },
    })
    const receipt = await screen.findByLabelText('Receipt')
    expect(within(receipt).getByText('Handoff rejected')).toBeVisible()
    // A rejection is a successful COMMAND and NOT a transfer.
    expect(within(receipt).getByText(/Ownership did not move/i)).toBeVisible()
  })

  it('preserves the server OPEN vocabulary: a custom code is offered and validated as a bounded token', async () => {
    api.respondToHandoff.mockResolvedValue({
      result: handoffResponseResultOf({ state: 'rejected' }),
      replayed: false,
      etag: '"h-v2"',
    })
    const user = userEvent.setup()
    mount('reject')
    await screen.findByRole('dialog')
    await user.click(screen.getByLabelText('Reason code'))
    await user.click(
      await screen.findByRole('option', { name: 'Custom code…' }),
    )
    // A shape the engine's own `boundedToken` refuses is refused here first.
    await user.type(screen.getByLabelText('Custom reason code'), 'Not A Token')
    await user.click(screen.getByRole('button', { name: 'Review response' }))
    await waitFor(() =>
      expect(screen.getByTestId('respond-problems')).toHaveTextContent(
        /must be a lowercase token/i,
      ),
    )
    expect(screen.getByLabelText('Custom reason code')).toHaveAttribute(
      'aria-invalid',
      'true',
    )
    expect(api.respondToHandoff).not.toHaveBeenCalled()

    await user.clear(screen.getByLabelText('Custom reason code'))
    await user.type(
      screen.getByLabelText('Custom reason code'),
      'blocked.by-dep',
    )
    await user.click(screen.getByRole('button', { name: 'Review response' }))
    await user.click(screen.getByRole('button', { name: 'Confirm rejection' }))
    await waitFor(() => expect(api.respondToHandoff).toHaveBeenCalledTimes(1))
    expect(api.respondToHandoff.mock.calls[0][0].body.reason.code).toBe(
      'blocked.by-dep',
    )
  })

  it('refuses a half-filled reference rather than silently dropping it', async () => {
    const user = userEvent.setup()
    mount('reject')
    await screen.findByRole('dialog')
    await user.click(screen.getByLabelText('Reason code'))
    await user.click(
      await screen.findByRole('option', { name: 'Not available' }),
    )
    await user.click(screen.getByRole('button', { name: 'Add reference' }))
    await user.type(screen.getByLabelText('Kind'), 'runbook')
    // …and no `ref`.
    await user.click(screen.getByRole('button', { name: 'Review response' }))
    await waitFor(() =>
      expect(screen.getByTestId('respond-problems')).toHaveTextContent(
        /Each reference needs a kind and a reference/i,
      ),
    )
    // The incomplete row is the one marked, not the whole form.
    expect(screen.getByLabelText('Reference')).toHaveAttribute(
      'aria-invalid',
      'true',
    )
    expect(screen.getByLabelText('Kind')).not.toHaveAttribute('aria-invalid')
    expect(api.respondToHandoff).not.toHaveBeenCalled()
  })
})

describe('the response operation belongs to the room, not to the panel', () => {
  it('an UNCERTAIN response survives closing the dialog and retries the SAME intent', async () => {
    const gate = deferred<never>()
    api.respondToHandoff.mockReturnValueOnce(gate.promise)
    const user = userEvent.setup()
    mount('accept')
    await screen.findByRole('dialog')
    await user.click(screen.getByRole('button', { name: 'Review response' }))
    await user.click(screen.getByRole('button', { name: 'Confirm acceptance' }))
    await waitFor(() => expect(api.respondToHandoff).toHaveBeenCalledTimes(1))
    const [sentIntent] = api.respondToHandoff.mock.calls[0]

    gate.reject(new NetworkError('dropped'))
    expect(await screen.findByText('The result is not known')).toBeVisible()

    await user.click(screen.getByRole('button', { name: 'Close' }))
    expect(
      await screen.findByText('A handoff operation has no confirmed result.'),
    ).toBeVisible()
    // The protected READ content never lived here, so nothing of it can leak into
    // the retained operation: only the administrative identifiers are on screen.
    expect(document.body.textContent).not.toMatch(
      /Deploy freeze needs an owner/,
    )

    api.respondToHandoff.mockResolvedValueOnce({
      result: handoffResponseResultOf({ replayed: true }),
      replayed: true,
      etag: '"h-v2"',
    })
    await user.click(screen.getByRole('button', { name: 'Show the operation' }))
    await user.click(
      screen.getByRole('button', { name: 'Retry with the same key' }),
    )
    await waitFor(() => expect(api.respondToHandoff).toHaveBeenCalledTimes(2))
    expect(api.respondToHandoff.mock.calls[1][0]).toBe(sentIntent)
    expect(
      await screen.findByText('Response already applied (replay)'),
    ).toBeVisible()
  })

  it('the bare 409 conflict class kills the intent and offers no same-key retry', async () => {
    api.respondToHandoff.mockRejectedValueOnce(
      new ApiError(409, 'internal', 'conflict', undefined, {
        error: { message: 'conflict' },
      }),
    )
    const user = userEvent.setup()
    mount('accept')
    await screen.findByRole('dialog')
    await user.click(screen.getByRole('button', { name: 'Review response' }))
    await user.click(screen.getByRole('button', { name: 'Confirm acceptance' }))
    expect(await screen.findByText('The response did not apply')).toBeVisible()
    expect(screen.getByText(/This response does not say which/i)).toBeVisible()
    expect(
      screen.queryByRole('button', { name: 'Retry with the same key' }),
    ).toBeNull()
  })

  it('a 428 names a MISSING PRECONDITION, distinctly from the 409 class', async () => {
    api.respondToHandoff.mockRejectedValueOnce(
      new ApiError(428, 'version_required', 'need If-Match'),
    )
    const user = userEvent.setup()
    mount('accept')
    await screen.findByRole('dialog')
    await user.click(screen.getByRole('button', { name: 'Review response' }))
    await user.click(screen.getByRole('button', { name: 'Confirm acceptance' }))
    await waitFor(() =>
      expect(
        document.querySelector('[data-failure-kind="version_required"]'),
      ).not.toBeNull(),
    )
    expect(document.querySelector('[data-failure-kind="conflict"]')).toBeNull()
  })

  it('a 412 plan_changed keeps its own answer: not every 412 is a handoff ETag mismatch', async () => {
    api.respondToHandoff.mockRejectedValueOnce(
      new ApiError(412, 'plan_changed', 'plan changed', undefined, {
        code: 'plan_changed',
      }),
    )
    const user = userEvent.setup()
    mount('accept')
    await screen.findByRole('dialog')
    await user.click(screen.getByRole('button', { name: 'Review response' }))
    await user.click(screen.getByRole('button', { name: 'Confirm acceptance' }))
    await waitFor(() =>
      expect(
        document.querySelector('[data-failure-kind="plan_changed"]'),
      ).not.toBeNull(),
    )
  })

  it('a 503 on the RESPONSE is uncertain, not a refusal: the same-key retry stays available', async () => {
    api.respondToHandoff.mockRejectedValueOnce(
      new ApiError(503, 'evidence_unavailable', 'no evidence', undefined, {
        code: 'evidence_unavailable',
      }),
    )
    const user = userEvent.setup()
    mount('accept')
    await screen.findByRole('dialog')
    await user.click(screen.getByRole('button', { name: 'Review response' }))
    await user.click(screen.getByRole('button', { name: 'Confirm acceptance' }))
    expect(await screen.findByText('The result is not known')).toBeVisible()
    expect(
      screen.getByRole('button', { name: 'Retry with the same key' }),
    ).toBeEnabled()
  })

  it('without the response permission the host mounts no editor at all', async () => {
    auth.perms = new Set(['sessions:delivery:read'])
    mount('accept', { canRespond: false })
    await Promise.resolve()
    // The protected editor is not rendered and then disabled: it does not exist,
    // so no reason draft or target can be held under an admission that is gone.
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(screen.queryByRole('button', { name: 'Review response' })).toBeNull()
  })
})
