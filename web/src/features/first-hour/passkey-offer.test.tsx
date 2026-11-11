// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The passkey offer on the first screen. After a sign-in with a password, a person with
// no passkey is offered one, once, where the engine says this address allows signing in
// with a passkey and the browser can make one. «Add a passkey» runs the console's one
// registration (the engine's options, the browser, the engine); «Not now» is kept with
// the person's notices; a directory's sign-in is never offered one.
import { act, render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const auth = vi.hoisted(() => ({
  principal: null as { amr?: string[]; superadmin?: boolean } | null,
  activeTenant: 't1',
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => auth }))
const toast = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn() }))
vi.mock('@/components/ui/toaster', () => ({ toast, Toaster: () => null }))

import { identityKeys } from '@/features/identity/api'
import { queryKeys } from '@/lib/api/query'
import { preferenceStorage } from '@/lib/preference-storage'
import { PasskeyOffer } from './passkey-offer'

const BODY =
  'Sign in next time with a passkey: your fingerprint, face or screen lock instead of a password.'
const LIST = identityKeys.webauthnCredentials('t1')
const DISMISSED = 'olivares.notices.dismissed'
const PASSKEY = {
  id: 'pk-1',
  name: 'Laptop',
  created_at: '2026-11-11T08:00:00Z',
  backup_eligible: true,
}
const OPTIONS = {
  publicKey: {
    challenge: 'AQID',
    rp: { id: 'console.example.com', name: 'Console' },
    user: { id: 'BAU', name: 'ana@example.com', displayName: 'Ana' },
    pubKeyCredParams: [{ type: 'public-key', alg: -7 }],
  },
}
const REFUSAL = {
  error: { code: 'webauthn_verification_failed', message: 'engine words' },
}
const bytes = (...values: number[]) => new Uint8Array(values).buffer
const created = {
  id: 'cred-1',
  rawId: bytes(1, 2),
  type: 'public-key',
  response: { clientDataJSON: bytes(3), attestationObject: bytes(4) },
}
const createCredential = vi.fn()

/** The engine at the HTTP boundary: server-info, the person's passkeys and the
 * registration, which saves the passkey it accepts. */
let engine: {
  info: Record<string, unknown>
  passkeys: unknown[]
  listStatus: number
  registerStatus: number
}
const requests: { method: string; path: string; body: unknown }[] = []
const asked = (path: string) => requests.filter((r) => r.path === path).length
const posted = (path: string) => requests.find((r) => r.path === path)?.body

function answer(status: number, body: unknown) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

async function engineFetch(input: RequestInfo | URL, init?: RequestInit) {
  const path = new URL(String(input), 'https://console.example.com').pathname
  const body = init?.body ? JSON.parse(String(init.body)) : undefined
  requests.push({ method: init?.method ?? 'GET', path, body })
  if (path === '/v1/server-info') return answer(200, engine.info)
  if (path === '/v1/auth/webauthn/credentials')
    return answer(engine.listStatus, { items: engine.passkeys })
  if (path === '/v1/auth/webauthn/register/options') return answer(200, OPTIONS)
  if (path === '/v1/auth/webauthn/register') {
    if (engine.registerStatus !== 200)
      return answer(engine.registerStatus, REFUSAL)
    engine.passkeys = [PASSKEY]
    return answer(200, { ok: true })
  }
  return answer(404, {})
}

function renderOffer() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const view = render(
    <QueryClientProvider client={client}>
      <PasskeyOffer />
    </QueryClientProvider>,
  )
  return { client, ...view }
}

const addButton = () => screen.queryByRole('button', { name: 'Add a passkey' })
const findAdd = () => screen.findByRole('button', { name: 'Add a passkey' })

beforeEach(() => {
  vi.clearAllMocks()
  requests.length = 0
  engine = {
    info: { setup_required: false, version: 'v', passkey_sign_in: true },
    passkeys: [],
    listStatus: 200,
    registerStatus: 200,
  }
  auth.principal = { amr: ['pwd'], superadmin: false }
  preferenceStorage.removeItem(DISMISSED)
  vi.stubGlobal('fetch', vi.fn(engineFetch))
  vi.stubGlobal('PublicKeyCredential', function PublicKeyCredential() {})
  Object.defineProperty(navigator, 'credentials', {
    configurable: true,
    value: { get: vi.fn(), create: createCredential },
  })
  createCredential.mockResolvedValue(created)
})

afterEach(() => {
  vi.unstubAllGlobals()
  Object.defineProperty(navigator, 'credentials', {
    configurable: true,
    value: undefined,
  })
})

describe('the passkey offer after a password sign-in', () => {
  it.each([
    ['a member', false],
    ['an administrator', true],
  ])(
    'offers %s one passkey where the address and the browser allow it',
    async (_who, superadmin) => {
      auth.principal = { amr: ['pwd'], superadmin }
      renderOffer()
      expect(await screen.findByText(BODY)).toBeInTheDocument()
      expect(addButton()).toBeInTheDocument()
      expect(screen.getByRole('button', { name: 'Not now' })).toBeVisible()
    },
  )

  it('offers nothing to a person who already has a passkey', async () => {
    engine.passkeys = [PASSKEY]
    const { client } = renderOffer()
    await waitFor(() => expect(client.getQueryData(LIST)).toBeDefined())
    expect(asked('/v1/auth/webauthn/credentials')).toBe(1)
    expect(addButton()).toBeNull()
    expect(screen.queryByText(BODY)).toBeNull()
  })

  it.each([
    ['false: an IP address, or a name with no trusted certificate', false],
    ['absent: a build with no passkey sign-in', undefined],
  ])(
    'offers nothing where server-info says passkey_sign_in is %s',
    async (_where, allowed) => {
      engine.info = { setup_required: false, version: 'v' }
      if (allowed !== undefined) engine.info.passkey_sign_in = allowed
      const { client } = renderOffer()
      await waitFor(() =>
        expect(client.getQueryData(queryKeys.serverInfo)).toBeDefined(),
      )
      expect(asked('/v1/auth/webauthn/credentials')).toBe(0)
      expect(addButton()).toBeNull()
    },
  )

  it('offers nothing and asks nothing when the browser cannot make a passkey', async () => {
    vi.stubGlobal('PublicKeyCredential', undefined)
    renderOffer()
    await act(async () => {})
    expect(requests).toHaveLength(0)
    expect(addButton()).toBeNull()
  })

  it.each([
    ['through a directory (SSO)', ['sso', 'mfa']],
    ['with a passkey', ['webauthn']],
    ['by an engine that names no method', undefined],
  ])(
    'offers nothing and asks nothing after a sign-in %s',
    async (_how, amr) => {
      auth.principal = { amr, superadmin: false }
      renderOffer()
      await act(async () => {})
      expect(requests).toHaveLength(0)
      expect(addButton()).toBeNull()
    },
  )

  it('offers nothing when the passkeys could not be read', async () => {
    engine.listStatus = 500
    const { client } = renderOffer()
    await waitFor(() =>
      expect(client.getQueryState(LIST)?.status).toBe('error'),
    )
    expect(addButton()).toBeNull()
  })

  it('adds a passkey through the console registration: options, the browser, then the engine', async () => {
    const user = userEvent.setup()
    renderOffer()
    await user.click(await findAdd())
    await waitFor(() =>
      expect(toast.success).toHaveBeenCalledWith(
        'Passkey added. Use it to sign in next time.',
      ),
    )
    expect(asked('/v1/auth/webauthn/register/options')).toBe(1)
    expect(createCredential).toHaveBeenCalledTimes(1)
    const options = createCredential.mock.calls[0][0]
    expect([...new Uint8Array(options.publicKey.challenge)]).toEqual([1, 2, 3])
    expect(posted('/v1/auth/webauthn/register')).toEqual({
      credential: {
        id: 'cred-1',
        rawId: 'AQI',
        type: 'public-key',
        response: { clientDataJSON: 'Aw', attestationObject: 'BA' },
      },
      name: expect.stringMatching(/\S/),
    })
    // The list read again has the new passkey, so the offer is gone.
    await waitFor(() => expect(addButton()).toBeNull())
    expect(toast.error).not.toHaveBeenCalled()
  })

  it('returns to rest and says nothing when the person closes the browser sheet', async () => {
    createCredential.mockRejectedValue(
      new DOMException('The sheet was closed.', 'NotAllowedError'),
    )
    const user = userEvent.setup()
    renderOffer()
    await user.click(await findAdd())
    await waitFor(() => expect(asked('/v1/auth/webauthn/credentials')).toBe(2))
    expect(asked('/v1/auth/webauthn/register')).toBe(0)
    expect(toast.error).not.toHaveBeenCalled()
    expect(toast.success).not.toHaveBeenCalled()
    expect(await findAdd()).not.toHaveAttribute('aria-busy')
  })

  it('says the passkey was not added when the engine refuses it, and keeps the offer', async () => {
    engine.registerStatus = 403
    const user = userEvent.setup()
    renderOffer()
    await user.click(await findAdd())
    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith(
        'The passkey was not added. Try again, or add one later in Settings.',
      ),
    )
    expect(toast.success).not.toHaveBeenCalled()
    await waitFor(() => expect(asked('/v1/auth/webauthn/credentials')).toBe(2))
    expect(addButton()).toBeInTheDocument()
  })

  it('never says «not added» when the engine’s answer was lost: the passkey may be saved', async () => {
    engine.registerStatus = 502
    const user = userEvent.setup()
    renderOffer()
    await user.click(await findAdd())
    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith(
        'The passkey could not be confirmed. It may have been added: look in Settings before adding another.',
      ),
    )
    expect(toast.error).toHaveBeenCalledTimes(1)
    expect(toast.success).not.toHaveBeenCalled()
  })

  it('puts it away on «Not now» and remembers that with the person’s notices', async () => {
    const user = userEvent.setup()
    const first = renderOffer()
    await user.click(await screen.findByRole('button', { name: 'Not now' }))
    expect(addButton()).toBeNull()
    expect(screen.queryByText(BODY)).toBeNull()
    expect(preferenceStorage.getItem(DISMISSED)).toContain('"passkey-offer"')
    expect(asked('/v1/auth/webauthn/register/options')).toBe(0)

    // The next page open, with the same person's notices: nothing asked, nothing drawn.
    first.unmount()
    requests.length = 0
    renderOffer()
    await act(async () => {})
    expect(requests).toHaveLength(0)
    expect(addButton()).toBeNull()
  })
})
