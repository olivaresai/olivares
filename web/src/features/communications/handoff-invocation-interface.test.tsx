// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The invocation and outcome Interface, measured on the controller itself and on
// both dialog paths. Two rules are under test:
//
//   D1  a pre-dispatch guard refusal establishes that THIS attempt sent nothing.
//       Beside a retained earlier attempt whose transmission was never
//       established, its sentence is scoped to the latest attempt.
//   D2  review(intent) is accepted only from idle. Every other phase leaves the
//       state, generation, history flag and protected intent unchanged, so no
//       command can inherit another's uncertainty or resolve it.
//
// The controller probe drives the public Interface directly, because these are
// Interface rules: a caller's current discipline is not a substitute for them.
import { act, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { useSessionStore } from '@/stores/session'

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
import { useHandoffOperation } from './handoff-operation'
import { snapshotAuthority, useIntentGuard } from './intent'
import {
  HandoffOfferHost,
  type HandoffWorkItemReader,
} from './handoff-offer-host'
import { HandoffResponseHost } from './handoff-response-host'
import type { HandoffRespondTarget } from './handoff-respond-dialog'
import {
  CHANNEL_ID,
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
const UNSCOPED_NOT_SENT = /so this command was not sent/i
const SCOPED_NOT_SENT = /The latest attempt did not start and was not sent/i

/* ── the controller Interface, driven directly ───────────────────────────────── */

interface ProbeIntent {
  readonly authority: ReturnType<typeof snapshotAuthority>
  readonly scope: { readonly tenant: string | null }
  readonly key: string
}

function Probe({
  send,
}: {
  send: (intent: ProbeIntent) => Promise<{ ok: true }>
}) {
  const guard = useIntentGuard({ allowed: true, boundary: 'probe' })
  const op = useHandoffOperation<ProbeIntent, { ok: true }>({
    send: (intent) => send(intent),
    allowed: true,
    guard,
  })
  const intentOf = (key: string): ProbeIntent => ({
    authority: snapshotAuthority(),
    scope: { tenant: 't1' },
    key,
  })
  return (
    <div>
      <output data-testid="phase">{op.state.phase}</output>
      <output data-testid="prior">{String(op.priorUnresolvedAttempt)}</output>
      <output data-testid="intent-key">
        {'intent' in op.state ? (op.state.intent as ProbeIntent).key : '-'}
      </output>
      <button type="button" onClick={() => op.review(intentOf('k1'))}>
        review-k1
      </button>
      <button type="button" onClick={() => op.review(intentOf('k2'))}>
        review-k2
      </button>
      <button type="button" onClick={op.submit}>
        submit
      </button>
      <button type="button" onClick={op.retrySame}>
        retry
      </button>
      <button type="button" onClick={op.reset}>
        reset
      </button>
      <button type="button" onClick={op.discard}>
        discard
      </button>
    </div>
  )
}

const phase = () => screen.getByTestId('phase').textContent
const prior = () => screen.getByTestId('prior').textContent
const intentKey = () => screen.getByTestId('intent-key').textContent

/* ── host harnesses ──────────────────────────────────────────────────────────── */

const responseTarget = (): HandoffRespondTarget => ({
  handoffId: HANDOFF_ID,
  etag: HANDOFF_ETAG,
  workItemId: WORK_ITEM_ID,
  recipient: { kind: 'user', ref: USER_B },
  deliveryId: HANDOFF_DELIVERY_ID,
  transition: 'accept',
})

function mountResponseHost() {
  return renderWithQuery(() => (
    <HandoffResponseHost
      scope={scopeOf()}
      canRespond
      canDeliveryRead
      target={responseTarget()}
      onTargetConsumed={() => {}}
      onResolved={() => {}}
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

function mountOfferHost(readItem: HandoffWorkItemReader) {
  return renderWithQuery(() => (
    <HandoffOfferHost
      target={{ itemId: WORK_ITEM_ID, invocation: 1 }}
      readItem={readItem}
    />
  ))
}

async function dispatchOffer(user: ReturnType<typeof userEvent.setup>) {
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

/** Move the credential generation the intent was confirmed under, so the
 *  controller's pre-dispatch guard refuses the explicit retry with `sent: false`. */
function moveCredential() {
  act(() => {
    useSessionStore.setState({
      credentialGeneration: useSessionStore.getState().credentialGeneration + 1,
    })
  })
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
  useSessionStore.setState({ credentialGeneration: 0 })
})

/* ── D1 ──────────────────────────────────────────────────────────────────────── */

describe('D1: a refused retry states only what it establishes', () => {
  it('the response dialog scopes the sentence to the latest attempt beside retained history', async () => {
    api.respondToHandoff.mockRejectedValueOnce(new NetworkError('lost'))
    const user = userEvent.setup()
    mountResponseHost()
    await dispatchResponse(user)
    expect(await screen.findByText('The result is not known')).toBeVisible()

    moveCredential()
    await user.click(
      screen.getByRole('button', { name: 'Retry with the same key' }),
    )

    expect(await screen.findByText('Credentials changed')).toBeVisible()
    // The guard refused before any fetch, so the retry sent nothing.
    expect(api.respondToHandoff).toHaveBeenCalledTimes(1)
    // The earlier attempt keeps its own separate, neutral warning…
    expect(screen.getByText(PRIOR)).toBeVisible()
    // …and the sentence about sending nothing names the latest attempt only.
    expect(screen.getByText(SCOPED_NOT_SENT)).toBeVisible()
    expect(screen.queryByText(UNSCOPED_NOT_SENT)).toBeNull()
    // The old retry is no longer offered, and nothing claims a rollback.
    expect(
      screen.queryByRole('button', { name: 'Retry with the same key' }),
    ).toBeNull()
    expect(document.body.textContent).not.toMatch(
      /rolled back|was canceled|was cancelled/i,
    )
  })

  it('the offer dialog scopes the same sentence on its own path', async () => {
    api.offerHandoff.mockRejectedValueOnce(new NetworkError('lost'))
    const user = userEvent.setup()
    mountOfferHost(vi.fn().mockResolvedValue(workItemViewOf()))
    await screen.findByRole('dialog', { name: 'Offer handoff' })
    await dispatchOffer(user)
    expect(await screen.findByText('The result is not known')).toBeVisible()

    moveCredential()
    await user.click(
      screen.getByRole('button', { name: 'Retry with the same key' }),
    )

    expect(await screen.findByText('Credentials changed')).toBeVisible()
    expect(api.offerHandoff).toHaveBeenCalledTimes(1)
    expect(screen.getByText(PRIOR)).toBeVisible()
    expect(screen.getByText(SCOPED_NOT_SENT)).toBeVisible()
    expect(screen.queryByText(UNSCOPED_NOT_SENT)).toBeNull()
  })

  it.each(['response', 'offer'] as const)(
    'POSITIVE CONTROL: a FIRST-attempt %s refusal keeps the unscoped first-attempt text',
    async (which) => {
      const user = userEvent.setup()
      if (which === 'response') {
        mountResponseHost()
        // The guard refuses before the very first dispatch: no earlier attempt.
        await user.click(
          await screen.findByRole('button', { name: 'Review response' }),
        )
        moveCredential()
        await user.click(
          screen.getByRole('button', { name: 'Confirm acceptance' }),
        )
        expect(api.respondToHandoff).not.toHaveBeenCalled()
      } else {
        mountOfferHost(vi.fn().mockResolvedValue(workItemViewOf()))
        await screen.findByRole('dialog', { name: 'Offer handoff' })
        await user.type(screen.getByLabelText('Channel'), CHANNEL_ID)
        await user.type(
          screen.getByLabelText('Reference (ID)'),
          '0192f2c0-eeee-7000-8000-00000000000b',
        )
        await user.type(screen.getByLabelText('Summary'), 'Needs an owner')
        await user.type(
          screen.getByLabelText('Next action'),
          'Confirm the window',
        )
        await user.type(screen.getByLabelText('Response deadline'), FUTURE)
        await user.click(screen.getByRole('button', { name: 'Review offer' }))
        moveCredential()
        await user.click(
          screen.getByRole('button', { name: 'Confirm and offer' }),
        )
        expect(api.offerHandoff).not.toHaveBeenCalled()
      }
      expect(await screen.findByText('Credentials changed')).toBeVisible()
      // With no earlier uncertainty the first-attempt text is the accurate one…
      expect(screen.getByText(UNSCOPED_NOT_SENT)).toBeVisible()
      // …and neither the scoped variant nor a history warning is invented.
      expect(screen.queryByText(SCOPED_NOT_SENT)).toBeNull()
      expect(screen.queryByText(PRIOR)).toBeNull()
    },
  )

  it('a transport failure whose transmission is unknown stays distinct from a refusal', async () => {
    api.respondToHandoff.mockRejectedValueOnce(new NetworkError('lost'))
    const user = userEvent.setup()
    mountResponseHost()
    await dispatchResponse(user)
    // Unknown, not "not sent": the retry under the same key stays available.
    expect(await screen.findByText('The result is not known')).toBeVisible()
    expect(screen.queryByText(UNSCOPED_NOT_SENT)).toBeNull()
    expect(screen.queryByText(SCOPED_NOT_SENT)).toBeNull()
    expect(
      screen.getByRole('button', { name: 'Retry with the same key' }),
    ).toBeEnabled()
  })
})

/* ── D2 ──────────────────────────────────────────────────────────────────────── */

describe('D2: review begins only from idle', () => {
  it('an unknown k1 then a same-key conflict refuses review(k2) and changes nothing', async () => {
    const send = vi
      .fn()
      .mockRejectedValueOnce(new NetworkError('lost'))
      .mockRejectedValueOnce(new ApiError(409, 'internal', 'conflict'))
      .mockResolvedValueOnce({ ok: true as const })
    const user = userEvent.setup()
    renderWithQuery(() => <Probe send={send} />)
    await user.click(screen.getByRole('button', { name: 'review-k1' }))
    await user.click(screen.getByRole('button', { name: 'submit' }))
    await waitFor(() => expect(phase()).toBe('uncertain'))
    await user.click(screen.getByRole('button', { name: 'retry' }))
    await waitFor(() => expect(phase()).toBe('conflict'))
    expect(prior()).toBe('true')
    expect(send).toHaveBeenCalledTimes(2)

    // The ratified rule: review is refused outside idle.
    await user.click(screen.getByRole('button', { name: 'review-k2' }))
    expect(phase()).toBe('conflict')
    expect(prior()).toBe('true')
    // No k2 intent was adopted, so submit dispatches nothing.
    await user.click(screen.getByRole('button', { name: 'submit' }))
    expect(send).toHaveBeenCalledTimes(2)

    // Only the explicit end of the invocation permits the next one.
    await user.click(screen.getByRole('button', { name: 'reset' }))
    await waitFor(() => expect(phase()).toBe('idle'))
    expect(prior()).toBe('false')
    await user.click(screen.getByRole('button', { name: 'review-k2' }))
    await waitFor(() => expect(phase()).toBe('reviewing'))
    expect(intentKey()).toBe('k2')
    await user.click(screen.getByRole('button', { name: 'submit' }))
    await waitFor(() => expect(phase()).toBe('confirmed'))
    expect(send.mock.calls[2][0].key).toBe('k2')
    // k2's receipt resolved k2. It never had k1's history to clear.
    expect(prior()).toBe('false')
  })

  it.each(['reviewing', 'submitting', 'uncertain', 'confirmed'] as const)(
    'review is a no-op from %s: state, history and protected intent are unchanged',
    async (target) => {
      const send = vi.fn()
      let release: ((v: { ok: true }) => void) | undefined
      if (target === 'submitting') {
        send.mockReturnValueOnce(
          new Promise<{ ok: true }>((r) => {
            release = r
          }),
        )
      } else if (target === 'uncertain') {
        send.mockRejectedValueOnce(new NetworkError('lost'))
      } else if (target === 'confirmed') {
        send.mockResolvedValueOnce({ ok: true as const })
      }
      const user = userEvent.setup()
      renderWithQuery(() => <Probe send={send} />)
      await user.click(screen.getByRole('button', { name: 'review-k1' }))
      if (target !== 'reviewing') {
        await user.click(screen.getByRole('button', { name: 'submit' }))
        await waitFor(() => expect(phase()).toBe(target))
      }
      const before = {
        phase: phase(),
        prior: prior(),
        key: intentKey(),
        sends: send.mock.calls.length,
      }

      await user.click(screen.getByRole('button', { name: 'review-k2' }))

      expect(phase()).toBe(before.phase)
      expect(prior()).toBe(before.prior)
      // The protected intent is still k1's, never replaced by k2's.
      expect(intentKey()).toBe(before.key)
      expect(send.mock.calls.length).toBe(before.sends)
      release?.({ ok: true })
    },
  )

  it('an unsent review stays editable through the existing return-to-edit path', async () => {
    const send = vi.fn().mockResolvedValueOnce({ ok: true as const })
    const user = userEvent.setup()
    renderWithQuery(() => <Probe send={send} />)
    await user.click(screen.getByRole('button', { name: 'review-k1' }))
    await waitFor(() => expect(phase()).toBe('reviewing'))
    // Discard is the explicit return to editing; it ends the invocation and
    // leaves idle, from which a different command may be reviewed.
    await user.click(screen.getByRole('button', { name: 'discard' }))
    await waitFor(() => expect(phase()).toBe('idle'))
    await user.click(screen.getByRole('button', { name: 'review-k2' }))
    await waitFor(() => expect(phase()).toBe('reviewing'))
    expect(intentKey()).toBe('k2')
    expect(send).not.toHaveBeenCalled()
  })

  it('reset still refuses an unresolved operation and its late result still lands', async () => {
    let release!: (v: { ok: true }) => void
    const send = vi.fn().mockReturnValueOnce(
      new Promise<{ ok: true }>((r) => {
        release = r
      }),
    )
    const user = userEvent.setup()
    renderWithQuery(() => <Probe send={send} />)
    await user.click(screen.getByRole('button', { name: 'review-k1' }))
    await user.click(screen.getByRole('button', { name: 'submit' }))
    await waitFor(() => expect(phase()).toBe('submitting'))

    await user.click(screen.getByRole('button', { name: 'reset' }))
    expect(phase()).toBe('submitting')

    await act(async () => {
      release({ ok: true })
      await Promise.resolve()
    })
    await waitFor(() => expect(phase()).toBe('confirmed'))
  })

  it('discard during a pending send retires the callback, and a late receipt does not publish', async () => {
    let release!: (v: { ok: true }) => void
    const send = vi.fn().mockReturnValueOnce(
      new Promise<{ ok: true }>((r) => {
        release = r
      }),
    )
    const user = userEvent.setup()
    renderWithQuery(() => <Probe send={send} />)
    await user.click(screen.getByRole('button', { name: 'review-k1' }))
    await user.click(screen.getByRole('button', { name: 'submit' }))
    await waitFor(() => expect(phase()).toBe('submitting'))

    await user.click(screen.getByRole('button', { name: 'discard' }))
    await waitFor(() => expect(phase()).toBe('idle'))

    await act(async () => {
      release({ ok: true })
      await Promise.resolve()
    })
    expect(phase()).toBe('idle')
  })

  it('the exact same-key retry still re-sends the identical command and its receipt resolves it', async () => {
    const send = vi
      .fn()
      .mockRejectedValueOnce(new NetworkError('lost'))
      .mockResolvedValueOnce({ ok: true as const })
    const user = userEvent.setup()
    renderWithQuery(() => <Probe send={send} />)
    await user.click(screen.getByRole('button', { name: 'review-k1' }))
    await user.click(screen.getByRole('button', { name: 'submit' }))
    await waitFor(() => expect(phase()).toBe('uncertain'))
    expect(prior()).toBe('true')

    await user.click(screen.getByRole('button', { name: 'retry' }))
    await waitFor(() => expect(phase()).toBe('confirmed'))
    expect(send.mock.calls[1][0]).toBe(send.mock.calls[0][0])
    expect(send.mock.calls[1][0].key).toBe('k1')
    // A receipt for THIS command resolves it.
    expect(prior()).toBe('false')
  })
})
