// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A 503 THE ENGINE SENDS ON PURPOSE MUST REACH THE OPERATOR AS ITSELF.
//
// `webauthn_relying_party_unusable` is a deployment state: the engine could not
// build a relying party at all, so no attempt at this address can succeed. It was
// rendered as the panel's generic failure — "Step-up did not complete" — which
// tells an operator that something went wrong with their click. Nothing went
// wrong with the click.
//
// This drives the REAL panel: a real ApiError carrying the real code comes back
// from the options call, and the assertion is on what a person can read on the
// screen. A classifier unit test cannot show that, because the defect was in the
// routing between the classifier and the alert.
//
// The second case is the one that keeps the first honest. An ordinary refused
// ceremony (403 webauthn_verification_failed) must NOT take this branch, or a bad
// signature would be reported to the operator as a misconfiguration of their
// deployment.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const authState = vi.hoisted(() => ({
  principal: { aal: 1, amr: ['pwd'] } as Record<string, unknown> | null,
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))

const api = vi.hoisted(() => ({
  pivStatus: vi.fn(),
  webauthnAuthOptions: vi.fn(),
}))
// jsdom exposes no WebAuthn API, so the panel would answer "this browser does not
// support WebAuthn" and never reach the engine. The support probe is stubbed to
// true so the click gets as far as the options call, which is where the 503 comes
// from; nothing else about the ceremony is faked.
vi.mock('./webauthn', async (orig) => {
  const real = (await orig()) as Record<string, unknown>
  return { ...real, isWebAuthnSupported: () => true }
})
vi.mock('./api', async (orig) => {
  const real = (await orig()) as Record<string, unknown>
  return { ...real, identityApi: { ...(real.identityApi as object), ...api } }
})

import { AAL, StepUpPanel } from './assurance'
import { ApiError } from '@/lib/api/errors'
import { queryKeys } from '@/lib/api/query'
import type { Whoami } from '@/lib/api/types'
import { useSessionStore } from '@/stores/session'
import { en } from './i18n'

const actor: Whoami = {
  kind: 'user',
  user_id: 'u1',
  actor: 'u1',
  display_name: 'Operator',
  superadmin: true,
  grants: [],
  aal: 1,
}

function wrap() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  qc.setQueryData(queryKeys.whoami, actor)
  return render(
    <QueryClientProvider client={qc}>
      <StepUpPanel
        minAal={AAL.HARDWARE}
        currentAal={AAL.PASSWORD}
        action="console"
      />
    </QueryClientProvider>,
  )
}

describe('a relying party the engine cannot build', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    authState.principal = { ...actor }
    useSessionStore.setState({ credentialGeneration: 0 })
    api.pivStatus.mockResolvedValue({ presented: false })
  })

  it('reaches the operator as its own alert, with a remedy that can succeed', async () => {
    api.webauthnAuthOptions.mockRejectedValue(
      new ApiError(
        503,
        'webauthn_relying_party_unusable',
        'Passkeys cannot be used at the address this console was reached on.',
      ),
    )
    const user = userEvent.setup()
    wrap()
    await user.click(await screen.findByRole('button', { name: /passkey/i }))

    const alert = await screen.findByRole('alert')
    await waitFor(() =>
      expect(alert).toHaveTextContent(en.assurance.relyingPartyUnusable),
    )
    // The generic failure must NOT be what the operator reads.
    expect(alert).not.toHaveTextContent(en.assurance.failed)
    // And the remedy must be a configuration change and a restart, not a retry
    // at another address — which is what the previous wording told them to do
    // and which cannot repair a declared-unusable deployment.
    const text = en.assurance.relyingPartyUnusable
    expect(text).toMatch(/restart/i)
    expect(text).toMatch(/configure/i)
    expect(text).not.toMatch(/try again\.?$/i)
  })

  it('keeps an ordinary refused ceremony separate', async () => {
    api.webauthnAuthOptions.mockRejectedValue(
      new ApiError(403, 'webauthn_verification_failed', 'verification failed'),
    )
    const user = userEvent.setup()
    wrap()
    await user.click(await screen.findByRole('button', { name: /passkey/i }))

    const alert = await screen.findByRole('alert')
    await waitFor(() => expect(alert).toHaveTextContent(en.assurance.failed))
    expect(alert).not.toHaveTextContent(en.assurance.relyingPartyUnusable)
  })
})
