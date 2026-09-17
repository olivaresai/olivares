// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ONE CARD MUST NOT SAY "NOT AVAILABLE HERE" AND OFFER IT ANYWAY.
//
// At http://127.0.0.1:18761 the step-up card showed the address notice —
// "Passkeys are not available at this address", correct, with the localhost
// alternative — directly above a button reading "Authenticate with passkey".
// The operator was handed a true explanation and a contradictory action on the
// same card, and the click it invited is refused by the BROWSER: no request
// leaves, no engine log explains it, and the panel reports a failure that looks
// like their own doing.
//
// WHAT THESE TESTS PIN, and it is deliberately narrow:
//   · the ADDRESS reading — and only the address reading — withdraws the passkey
//     action, at the control AND at the callback behind it;
//   · a browser with no WebAuthn API at a GOOD address is NOT that case, and
//     keeps the action it has always had;
//   · every other way out of the card stays exactly where it was: the localhost
//     alternative, the PIV elevation, the session check.
//
// NO CEREMONY IS FAKED ANYWHERE IN THIS FILE. The positive cases prove the flow
// is preserved by showing the request REACHES the engine and then letting the
// engine refuse it. A fixture that answered "ok" would be asserting an
// authentication this console is not entitled to grant.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const authState = vi.hoisted(() => ({
  principal: { aal: 1, amr: ['pwd'] } as Record<string, unknown> | null,
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))

const api = vi.hoisted(() => ({
  pivStatus: vi.fn(),
  pivElevate: vi.fn(),
  webauthnAuthOptions: vi.fn(),
}))
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

const originalSecure = window.isSecureContext

/** Drive the panel from a real address, exactly as the notice's own suite does:
 *  jsdom's location is not writable, so the getter is replaced. `api` decides
 *  whether the browser exposes a WebAuthn implementation at all — which is a
 *  fact about the BROWSER and is kept independent of the address on purpose. */
function atLocation(href: string, secureContext: boolean, webauthnApi = true) {
  vi.spyOn(window, 'location', 'get').mockReturnValue({
    ...window.location,
    href,
  } as Location)
  Object.defineProperty(window, 'isSecureContext', {
    value: secureContext,
    configurable: true,
  })
  if (webauthnApi) {
    vi.stubGlobal('PublicKeyCredential', function () {} as unknown)
    Object.defineProperty(navigator, 'credentials', {
      value: {
        get: () => Promise.resolve(null),
        create: () => Promise.resolve(null),
      },
      configurable: true,
    })
  } else {
    vi.stubGlobal('PublicKeyCredential', undefined)
  }
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

function passkeyButton() {
  return screen.getByRole('button', { name: /passkey/i })
}

beforeEach(() => {
  vi.clearAllMocks()
  authState.principal = { ...actor }
  useSessionStore.setState({ credentialGeneration: 0 })
  api.pivStatus.mockResolvedValue({ presented: false })
})

afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  Object.defineProperty(window, 'isSecureContext', {
    value: originalSecure,
    configurable: true,
  })
})

describe('an address the same card refuses', () => {
  it('withdraws the passkey action it cannot honour, and says why', async () => {
    // The reported address, verbatim.
    atLocation('http://127.0.0.1:18761/identity?tab=login', true)
    const user = userEvent.setup()
    wrap()

    // The notice is the premise of this test: without it there is no
    // contradiction to correct, and the assertions below would be vacuous.
    const notice = screen.getByTestId('passkey-address-notice')
    expect(notice).toHaveTextContent(en.assurance.address.addressTitle)

    const button = passkeyButton()
    expect(button).toHaveAttribute('aria-disabled', 'true')
    // The explanation is not merely nearby: the control points at it, so it is
    // announced with the control rather than only read by whoever can see it.
    expect(button).toHaveAttribute('aria-describedby', notice.id)
    expect(notice.id).toBeTruthy()
    void user
  })

  // SEPARATE FROM THE ASSERTION ABOVE ON PURPOSE. If the two shared a test, the
  // withdrawn control would fail first and this — the defect's actual cost, a
  // challenge requested at an address that can never return an assertion —
  // would never be reached, let alone reported.
  it('refuses the activation at the callback, not only at the control', async () => {
    atLocation('http://127.0.0.1:18761/identity?tab=login', true)
    const user = userEvent.setup()
    wrap()
    await user.click(passkeyButton())
    expect(api.webauthnAuthOptions).not.toHaveBeenCalled()
  })

  it('keeps the same-device alternative it offers', () => {
    atLocation('http://127.0.0.1:18761/identity?tab=login', true)
    wrap()
    const link = screen.getByTestId('passkey-address-localhost')
    expect(link).toHaveAttribute('href', 'http://localhost:18761')
  })

  it('keeps PIV reachable, which is the way out that still works', async () => {
    atLocation('http://127.0.0.1:18761/identity?tab=login', true)
    api.pivStatus.mockResolvedValue({ presented: true })
    // The engine refuses it. That is the point: this asserts the request LEAVES,
    // not that an elevation succeeded.
    api.pivElevate.mockRejectedValue(new ApiError(403, 'piv_denied', 'denied'))
    const user = userEvent.setup()
    wrap()

    const piv = await screen.findByRole('button', {
      name: en.assurance.authenticatePiv,
    })
    expect(piv).not.toHaveAttribute('aria-disabled')
    await user.click(piv)
    await waitFor(() => expect(api.pivElevate).toHaveBeenCalledTimes(1))
  })

  it('withdraws it at a routable IP, which has no localhost to offer', async () => {
    atLocation('http://10.0.0.7:8443/identity?tab=login', false)
    const user = userEvent.setup()
    wrap()
    expect(screen.queryByTestId('passkey-address-localhost')).toBeNull()
    expect(passkeyButton()).toHaveAttribute('aria-disabled', 'true')
    await user.click(passkeyButton())
    expect(api.webauthnAuthOptions).not.toHaveBeenCalled()
  })
})

// THE BROWSER'S TWO ANSWERS ARE NOT THE ADDRESS'S, AND NEITHER WITHDRAWS AN
// ACTION. Both cases below are at a host that CAN be a relying party, so the
// action stays; what refuses the ceremony is the client in front of the
// operator, whose remedy is another browser or another scheme — not another
// deployment. The notice keeps naming which of the two it is.
describe('what the browser reports is not what the address is', () => {
  it('keeps the action when no WebAuthn API is exposed, and answers about the browser', async () => {
    atLocation('https://console.example.com/identity?tab=login', true, false)
    const user = userEvent.setup()
    wrap()

    const button = passkeyButton()
    expect(button).not.toHaveAttribute('aria-disabled')
    await user.click(button)
    expect(await screen.findByRole('alert')).toHaveTextContent(
      en.assurance.unsupported,
    )
  })

  it('keeps it in an insecure context at a usable host, and still says so', () => {
    // MEASURED, and the reason this suite refuses to gate the action on the
    // flag: jsdom answers isSecureContext=false at a host every real browser
    // treats as potentially trustworthy. An action gated on that answer would
    // vanish at an address that works. Explaining is safe; withdrawing is not.
    atLocation('http://console.example.com/identity?tab=login', false)
    wrap()
    expect(
      screen.getByText(new RegExp(en.assurance.address.insecureTitle, 'i')),
    ).toBeInTheDocument()
    expect(passkeyButton()).not.toHaveAttribute('aria-disabled')
  })
})

describe('an address where a passkey can run', () => {
  it('starts the ceremony at a configured relying party', async () => {
    atLocation('https://console.example.com/identity?tab=login', true)
    api.webauthnAuthOptions.mockRejectedValue(
      new ApiError(403, 'webauthn_verification_failed', 'refused'),
    )
    const user = userEvent.setup()
    wrap()

    expect(screen.queryByTestId('passkey-address-notice')).toBeNull()
    const button = passkeyButton()
    expect(button).not.toHaveAttribute('aria-disabled')
    await user.click(button)
    await waitFor(() =>
      expect(api.webauthnAuthOptions).toHaveBeenCalledTimes(1),
    )
  })

  it('starts it at supported localhost, which the engine accepts', async () => {
    atLocation('http://localhost:18761/identity?tab=login', true)
    api.webauthnAuthOptions.mockRejectedValue(
      new ApiError(403, 'webauthn_verification_failed', 'refused'),
    )
    const user = userEvent.setup()
    wrap()

    expect(screen.queryByTestId('passkey-address-notice')).toBeNull()
    expect(passkeyButton()).not.toHaveAttribute('aria-disabled')
    await user.click(passkeyButton())
    await waitFor(() =>
      expect(api.webauthnAuthOptions).toHaveBeenCalledTimes(1),
    )
  })
})
