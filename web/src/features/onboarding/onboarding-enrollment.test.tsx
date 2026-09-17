// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// EJ01/EJ03 first hour, Q01 (no authority change), Q09 (identity continuity).
// The onboarding route mounts the REAL gate (RequireAssurance → StepUpPanel), the
// REAL ceremony owner (enrollPasskey) and the REAL API client over a fetch fixture.
// Nothing here passes the gate through: a test that mocked RequireAssurance would
// never observe `allowEnrollment`, which is the whole subject.
//
// The requirement: an operator WITHOUT a passkey must be able to complete the first
// privileged onboarding action. Registration is not authentication — it only makes
// the credential exist. The privileged form stays closed until an explicit
// authenticate + a fresh authoritative whoami reports the SAME principal at AAL3.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, cleanup, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

// The route's own stepper links out to console pages. Only the router is stood in
// for — every identity boundary under test (the gate, the panel, the ceremony,
// the API client) is the real one.
vi.mock('@tanstack/react-router', () => ({
  useRouterState: () => '',
  Link: ({ to, children }: { to: string; children: ReactNode }) => (
    <a href={to}>{children}</a>
  ),
}))

import { AuthProvider } from '@/lib/auth/context'
import { configureApiClient } from '@/lib/api/client'
import { queryKeys } from '@/lib/api/query'
import type { Whoami } from '@/lib/api/types'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import { useStepUpStore } from '@/stores/step-up'
import './i18n'
import '@/features/identity/i18n'
import { OnboardingView } from './onboarding-view'

const principal = (aal = 1, userId = 'u1'): Whoami => ({
  user_id: userId,
  kind: 'user',
  actor: userId,
  display_name: 'Operator',
  superadmin: true,
  grants: [],
  aal,
  amr: ['pwd'],
})
const bytes = new Uint8Array([1, 2, 3]).buffer
const credential = {
  id: 'key1',
  type: 'public-key',
  rawId: bytes,
  response: {
    clientDataJSON: bytes,
    attestationObject: bytes,
    authenticatorData: bytes,
    signature: bytes,
    userHandle: null,
  },
}
const options = {
  publicKey: {
    challenge: 'AQID',
    rp: { name: 'Fixture' },
    user: { id: 'AQID', name: 'operator', displayName: 'Operator' },
    pubKeyCredParams: [{ type: 'public-key', alg: -7 }],
  },
}
const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
const error = (status: number, code: string) =>
  json({ error: { code, message: code } }, status)

const defaultWs = {
  id: 'w0',
  tenant_id: 't1',
  name: 'Default',
  slug: 'default',
  status: 'active',
  is_default: true,
  created_at: '',
  updated_at: '',
  version: 1,
}

let enrolled: boolean
let serverAal: number
let whoamiResult: () => Response | Promise<Response>
let registerResult: () => Response | Promise<Response>
let authOptionsResult: (() => Response | Promise<Response>) | null
let qc: QueryClient
let calls: string[]
const get = vi.fn()
const create = vi.fn()
const unauthorized = vi.fn()

function mount() {
  return render(
    <QueryClientProvider client={qc}>
      <AuthProvider>
        <OnboardingView />
      </AuthProvider>
    </QueryClientProvider>,
  )
}
/** Every query is scoped to step 2, the first privileged onboarding action. The
 *  route mounts a gate per privileged step, so an unscoped query would not say
 *  WHICH step offered what. */
function step(): HTMLElement {
  const title = screen.getByText(/create your first workspace/i)
  const card = title.closest('div.min-w-0')
  if (!(card instanceof HTMLElement))
    throw new Error('workspace step card not found')
  return card
}
/** The route resolves its authoritative reads before any step card exists. */
async function findStep(): Promise<HTMLElement> {
  await screen.findByText(/create your first workspace/i)
  return step()
}
const findAuthenticate = async () =>
  within(await findStep()).findByRole('button', {
    name: /authenticate with (a )?(passkey|security key)/i,
  })
const authenticate = () =>
  within(step()).getByRole('button', {
    name: /authenticate with (a )?(passkey|security key)/i,
  })
const registerButton = () =>
  within(step()).getByRole('button', { name: /^register passkey$/i })
const workspaceForm = () =>
  within(step()).queryByRole('button', { name: /^create workspace$/i })
async function settle() {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 20))
  })
}
/** Exact calls only: `/authenticate/options` contains `/authenticate`, and a
 *  substring count would report a challenge fetch as a finish. */
const countOf = (call: string) => calls.filter((c) => c === call).length

beforeEach(() => {
  enrolled = false
  serverAal = 1
  calls = []
  authOptionsResult = null
  try {
    localStorage.removeItem('olivares.onboarding.dismissed')
  } catch {
    /* ignore */
  }
  qc = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  qc.setQueryData(queryKeys.whoami, principal())
  useStepUpStore.getState().clear()
  useTenantStore.setState({ activeTenant: 't1' })
  useSessionStore.getState().setSession({
    token: 'fixture',
    sessionId: 'fixture-session',
    expiresAt: '',
  })
  configureApiClient({
    getToken: () => useSessionStore.getState().token,
    getTenant: () => useTenantStore.getState().activeTenant,
    onUnauthorized: unauthorized,
    getExpiresAt: () => null,
    refreshSession: async () => false,
  })
  vi.stubGlobal('PublicKeyCredential', function PublicKeyCredential() {})
  Object.defineProperty(navigator, 'credentials', {
    configurable: true,
    value: { get, create },
  })
  get.mockReset().mockResolvedValue(credential)
  create.mockReset().mockResolvedValue(credential)
  unauthorized.mockReset()
  whoamiResult = () => json(principal(serverAal))
  registerResult = () => {
    enrolled = true
    return json({ ok: true })
  }
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init?: RequestInit) => {
      const call = `${init?.method ?? 'GET'} ${url}`
      calls.push(call)
      if (url === '/v1/auth/whoami') return whoamiResult()
      if (url === '/v1/auth/piv/status') return json({ presented: false })
      if (url.endsWith('/authenticate/options'))
        return authOptionsResult
          ? authOptionsResult()
          : enrolled
            ? json(options)
            : error(400, 'no_webauthn_credential')
      if (url.endsWith('/register/options')) return json(options)
      if (url.endsWith('/webauthn/register')) return registerResult()
      if (url.endsWith('/authenticate') || url.endsWith('/piv/elevate')) {
        serverAal = 3
        return json({ ok: true, aal: 3 })
      }
      if (url.startsWith('/v1/console/setup-status'))
        return json({
          completed: false,
          steps: [
            { id: 'database', completed: true },
            { id: 'connectors', completed: false },
            { id: 'identity', completed: false },
            { id: 'users', completed: false },
          ],
        })
      if (url.startsWith('/v1/workspaces')) {
        if (init?.method === 'POST') return json(defaultWs)
        return json({ items: [defaultWs] })
      }
      if (url.startsWith('/v1/members')) return json({ items: [] })
      if (url.startsWith('/v1/console/sources')) return json({ sources: [] })
      if (url.startsWith('/v1/console/connectors'))
        return json({ connectors: [] })
      if (url.includes('/distribution'))
        return json({ surface: 'managed-settings', scopes: [] })
      throw new Error(`Unhandled fixture request: ${call}`)
    }),
  )
})
afterEach(() => {
  cleanup()
  qc.clear()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

describe('first-credential enrollment inside the onboarding privileged panel', () => {
  it('offers enrollment when the engine reports no credential for this account', async () => {
    const user = userEvent.setup()
    mount()
    await user.click(await findAuthenticate())
    // The engine's own refusal is what opens the offer: nothing is inferred here.
    expect(
      await within(step()).findByText(/no passkey is registered/i),
    ).toBeVisible()
    expect(
      within(step()).getByRole('textbox', { name: /passkey name/i }),
    ).toBeVisible()
    expect(registerButton()).toBeVisible()
    // Still gated: the offer is not an elevation.
    expect(workspaceForm()).not.toBeInTheDocument()
  })

  it('registration alone leaves the privileged action unavailable until a fresh authenticated whoami', async () => {
    const user = userEvent.setup()
    mount()
    await user.click(await findAuthenticate())
    await user.type(
      await within(step()).findByRole('textbox', { name: /passkey name/i }),
      'Laptop',
    )
    await user.click(registerButton())
    await within(step()).findByText(/passkey registered/i)
    // Registration established a credential and NOTHING else.
    expect(workspaceForm()).not.toBeInTheDocument()
    expect(qc.getQueryData<Whoami>(queryKeys.whoami)?.aal).toBe(1)
    expect(countOf('POST /v1/auth/webauthn/authenticate')).toBe(0)
    expect(calls.filter((c) => c === 'POST /v1/workspaces')).toHaveLength(0)
    const whoamiBefore = countOf('GET /v1/auth/whoami')
    // The explicit second ceremony, then the authoritative re-read.
    await user.click(authenticate())
    expect(
      await within(step()).findByRole('button', {
        name: /^create workspace$/i,
      }),
    ).toBeVisible()
    expect(countOf('GET /v1/auth/whoami')).toBeGreaterThan(whoamiBefore)
    const elevated = qc.getQueryData<Whoami>(queryKeys.whoami)
    expect(elevated?.aal).toBe(3)
    expect(elevated?.user_id).toBe('u1')
    expect(calls.filter((c) => c === 'POST /v1/workspaces')).toHaveLength(0)
  })

  it('does not resend the attestation after an unknown registration finish', async () => {
    registerResult = () => error(500, 'boom')
    const user = userEvent.setup()
    mount()
    await user.click(await findAuthenticate())
    await user.type(
      await within(step()).findByRole('textbox', { name: /passkey name/i }),
      'Laptop',
    )
    await user.click(registerButton())
    expect(
      await within(step()).findByText(/registration could not be confirmed/i),
    ).toBeVisible()
    await settle()
    expect(countOf('POST /v1/auth/webauthn/register')).toBe(1)
    expect(create).toHaveBeenCalledTimes(1)
    expect(workspaceForm()).not.toBeInTheDocument()
  })

  it('keeps the refusal and the existing guidance when the browser cannot register', async () => {
    // A browser that can answer an assertion but cannot create a credential: the
    // offer must name that, not hand out a control the browser will refuse.
    Object.defineProperty(navigator, 'credentials', {
      configurable: true,
      value: { get },
    })
    const user = userEvent.setup()
    mount()
    await user.click(await findAuthenticate())
    expect(
      await within(step()).findByText(
        /this browser cannot register a passkey/i,
      ),
    ).toBeVisible()
    expect(
      within(step()).queryByRole('button', { name: /^register passkey$/i }),
    ).not.toBeInTheDocument()
    expect(countOf('POST /v1/auth/webauthn/register')).toBe(0)
    expect(workspaceForm()).not.toBeInTheDocument()
  })

  it('keeps the refusal when the deployment has no usable relying party', async () => {
    authOptionsResult = () => error(503, 'webauthn_relying_party_unusable')
    const user = userEvent.setup()
    mount()
    await user.click(await findAuthenticate())
    expect(
      await within(step()).findByText(
        /passkeys are not available on this deployment/i,
      ),
    ).toBeVisible()
    expect(
      within(step()).queryByRole('button', { name: /^register passkey$/i }),
    ).not.toBeInTheDocument()
    expect(workspaceForm()).not.toBeInTheDocument()
  })

  it('refuses to open the action when the verified principal is not the one that registered', async () => {
    const user = userEvent.setup()
    mount()
    await user.click(await findAuthenticate())
    await user.type(
      await within(step()).findByRole('textbox', { name: /passkey name/i }),
      'Laptop',
    )
    await user.click(registerButton())
    await within(step()).findByText(/passkey registered/i)
    // The authoritative read answers for a DIFFERENT principal at the required AAL.
    whoamiResult = () => json(principal(3, 'u2'))
    await user.click(authenticate())
    expect(
      await within(step()).findByText(/session could not be verified/i),
    ).toBeVisible()
    expect(workspaceForm()).not.toBeInTheDocument()
  })
})
