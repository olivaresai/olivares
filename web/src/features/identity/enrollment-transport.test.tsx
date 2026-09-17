// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import {
  QueryClient,
  QueryClientProvider,
  QueryObserver,
} from '@tanstack/react-query'
import { act, cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { configureApiClient } from '@/lib/api/client'
import { authApi } from '@/lib/api/endpoints'
import { queryKeys } from '@/lib/api/query'
import type { Whoami } from '@/lib/api/types'
import {
  createStepUpOwner,
  type StepUpAttempt,
  type StepUpOwner,
} from '@/stores/step-up'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import { identityApi } from './api'

// No AuthProvider whoami reader: this proves that a disabled or absent query reader
// is not mistaken for a successful refetch. Panel, wrappers and HTTP client are real.
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ principal: { user_id: 'u1', aal: 1 } }),
}))
import { StepUpPanel } from './assurance'

const actor: Whoami = {
  kind: 'user',
  user_id: 'u1',
  actor: 'u1',
  display_name: 'Operator',
  superadmin: true,
  grants: [],
  aal: 1,
}
const response = (status = 200, body: unknown = { ok: true }) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
const denied = () =>
  response(401, {
    error: { code: 'unauthenticated', message: 'fixture refusal' },
  })
const unauthorized = vi.fn()
const refresh = vi.fn()
let qc: QueryClient
let owners: StepUpOwner[]
let expires: string | null
function owner() {
  const value = createStepUpOwner(qc)
  owners.push(value)
  return value
}
function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((r) => {
    resolve = r
  })
  return { promise, resolve }
}
const routes: Array<[string, (options: StepUpAttempt) => Promise<unknown>]> = [
  ['register options', (opts) => identityApi.webauthnRegisterOptions(opts)],
  [
    'register finish',
    (opts) => identityApi.webauthnRegister({ id: 'fixture' }, 'Laptop', opts),
  ],
  ['authentication options', (opts) => identityApi.webauthnAuthOptions(opts)],
  [
    'authentication finish',
    (opts) => identityApi.webauthnAuthenticate({ id: 'fixture' }, opts),
  ],
  ['PIV', (opts) => identityApi.pivElevate(opts)],
  ['whoami', (opts) => authApi.whoami(opts)],
]
beforeEach(() => {
  qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  qc.setQueryData(queryKeys.whoami, actor)
  owners = []
  expires = null
  unauthorized.mockReset()
  refresh.mockReset().mockResolvedValue(true)
  useTenantStore.setState({ activeTenant: 't1' })
  useSessionStore
    .getState()
    .setSession({ token: 'fixture-one', sessionId: 'fixture', expiresAt: '' })
  configureApiClient({
    getToken: () => useSessionStore.getState().token,
    getTenant: () => useTenantStore.getState().activeTenant,
    getExpiresAt: () => expires,
    refreshSession: refresh,
    onUnauthorized: unauthorized,
  })
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => response()),
  )
})
afterEach(() => {
  cleanup()
  owners.forEach((o) => o.retire())
  qc.clear()
  vi.unstubAllGlobals()
})

describe('owned ceremony transport reuses actual dispatch and session isolation', () => {
  for (const [name, call] of routes) {
    it(`${name} dispatches under its live context with isolated defaults`, async () => {
      expires = new Date(Date.now() + 1_000).toISOString()
      const attempt = owner().begin()
      await call(attempt)
      expect(fetch).toHaveBeenCalledTimes(1)
      expect(refresh).not.toHaveBeenCalled()
      expect(vi.mocked(fetch).mock.calls[0][1]?.signal).toBe(attempt.signal)
      const headers = new Headers(vi.mocked(fetch).mock.calls[0][1]?.headers)
      expect(headers.get('X-Olivares-Tenant')).toBe('t1')
    })
    it(`${name} refuses actual dispatch after A→B→A retirement`, async () => {
      const attempt = owner().begin()
      useTenantStore.getState().setActiveTenant('t2')
      useTenantStore.getState().setActiveTenant('t1')
      await expect(call(attempt)).rejects.toMatchObject({ name: 'AbortError' })
      expect(fetch).not.toHaveBeenCalled()
      expect(refresh).not.toHaveBeenCalled()
    })
    it(`${name} late401 cannot clear a replacement credential or replay`, async () => {
      const pending = deferred<Response>()
      vi.mocked(fetch).mockReturnValue(pending.promise)
      const attempt = owner().begin()
      const result = call(attempt).catch((err: unknown) => err)
      expect(fetch).toHaveBeenCalledTimes(1)
      useSessionStore.getState().setSession({
        token: 'fixture-two',
        sessionId: 'fixture',
        expiresAt: '',
      })
      pending.resolve(denied())
      expect(await result).toMatchObject({ name: 'AbortError' })
      expect(refresh).not.toHaveBeenCalled()
      expect(unauthorized).not.toHaveBeenCalled()
      expect(useSessionStore.getState().token).toBe('fixture-two')
      expect(fetch).toHaveBeenCalledTimes(1)
    })
  }
  it('also checks retirement after reading a late401 body', async () => {
    const body = deferred<string>()
    vi.mocked(fetch).mockResolvedValue({
      status: 401,
      ok: false,
      headers: new Headers(),
      text: () => body.promise,
    } as Response)
    const attempt = owner().begin()
    const result = identityApi.pivElevate(attempt).catch((err: unknown) => err)
    await Promise.resolve()
    useSessionStore
      .getState()
      .setSession({ token: 'fixture-two', sessionId: 'fixture', expiresAt: '' })
    body.resolve(JSON.stringify({ error: { code: 'unauthenticated' } }))
    expect(await result).toMatchObject({ name: 'AbortError' })
    expect(unauthorized).not.toHaveBeenCalled()
    expect(refresh).not.toHaveBeenCalled()
  })
  it('a new attempt permanently retires the earlier dispatch authority', async () => {
    const scope = owner()
    const old = scope.begin()
    const next = scope.begin()
    await expect(
      identityApi.webauthnRegisterOptions(old),
    ).rejects.toMatchObject({ name: 'AbortError' })
    await identityApi.webauthnRegisterOptions(next)
    expect(fetch).toHaveBeenCalledTimes(1)
    expect(old.current()).toBe(false)
  })
  for (const phase of ['preventive refresh', '401 replay'] as const) {
    for (const retire of [false, true]) {
      it(`${phase}: ${retire ? 'retired context sends no new fetch' : 'stable default consumer still dispatches'}`, async () => {
        // Defaults are intentionally preserved for other wrapper consumers. Unlike
        // the owned panel, this call does not opt into sessionEffects='none'.
        const renewing = deferred<boolean>()
        refresh.mockReturnValue(renewing.promise)
        if (phase === 'preventive refresh')
          expires = new Date(Date.now() + 1_000).toISOString()
        else
          vi.mocked(fetch)
            .mockResolvedValueOnce(denied())
            .mockResolvedValue(response())
        const attempt = owner().begin()
        const result = identityApi
          .webauthnAuthOptions({
            signal: attempt.signal,
            dispatchGuard: attempt.dispatchGuard,
          })
          .catch((err: unknown) => err)
        await waitFor(() => expect(refresh).toHaveBeenCalledTimes(1))
        expect(fetch).toHaveBeenCalledTimes(
          phase === 'preventive refresh' ? 0 : 1,
        )
        if (retire) {
          useTenantStore.getState().setActiveTenant('t2')
          useTenantStore.getState().setActiveTenant('t1')
        }
        renewing.resolve(true)
        const answer = await result
        if (retire) expect(answer).toMatchObject({ name: 'AbortError' })
        else expect(answer).toEqual({ ok: true })
        expect(fetch).toHaveBeenCalledTimes(
          (phase === 'preventive refresh' ? 0 : 1) + (retire ? 0 : 1),
        )
      })
    }
  }
  it('closed identity options do not forward anonymous, body or tenant overrides', async () => {
    const opts = {
      ...owner().begin(),
      anonymous: true,
      tenant: 'other',
      body: { bad: true },
      headers: { Authorization: 'bad' },
    }
    await identityApi.webauthnRegister({ id: 'fixture' }, 'Laptop', opts)
    const [, init] = vi.mocked(fetch).mock.calls[0]
    expect(new Headers(init?.headers).get('Authorization')).toBe(
      'Bearer fixture-one',
    )
    expect(new Headers(init?.headers).get('X-Olivares-Tenant')).toBe('t1')
    expect(JSON.parse(String(init?.body))).toEqual({
      credential: { id: 'fixture' },
      name: 'Laptop',
    })
  })
})

describe('panel verification with no active whoami query', () => {
  for (const reader of ['absent', 'disabled'] as const) {
    for (const valid of [false, true]) {
      it(`${reader} reader: ${valid ? 'new successful HTTP response validates' : 'empty response never completes'}`, async () => {
        let detach: (() => void) | undefined
        if (reader === 'disabled') {
          const observer = new QueryObserver(qc, {
            queryKey: queryKeys.whoami,
            queryFn: () => authApi.whoami(),
            enabled: false,
          })
          detach = observer.subscribe(() => {})
        }
        const bytes = new Uint8Array([1]).buffer
        vi.stubGlobal('PublicKeyCredential', function PublicKeyCredential() {})
        Object.defineProperty(navigator, 'credentials', {
          configurable: true,
          value: {
            get: async () => ({
              id: 'fixture',
              type: 'public-key',
              rawId: bytes,
              response: {
                clientDataJSON: bytes,
                authenticatorData: bytes,
                signature: bytes,
              },
            }),
          },
        })
        const whoami = vi.fn(() =>
          valid
            ? response(200, { ...actor, aal: 3 })
            : new Response(null, { status: 204 }),
        )
        vi.mocked(fetch).mockImplementation(async (path) => {
          if (String(path).endsWith('/piv/status'))
            return response(200, { presented: false })
          if (String(path).endsWith('/options'))
            return response(200, { publicKey: { challenge: 'AQ' } })
          if (String(path).endsWith('/whoami')) return whoami()
          return response()
        })
        const onElevated = vi.fn()
        render(
          <QueryClientProvider client={qc}>
            <StepUpPanel
              action="console"
              currentAal={1}
              minAal={3}
              onElevated={onElevated}
            />
          </QueryClientProvider>,
        )
        await userEvent.click(
          screen.getByRole('button', { name: /authenticate with passkey/i }),
        )
        await waitFor(() => expect(whoami).toHaveBeenCalledTimes(1))
        await act(async () => {
          await new Promise((r) => setTimeout(r, 20))
        })
        expect(onElevated).toHaveBeenCalledTimes(valid ? 1 : 0)
        expect(qc.getQueryData<Whoami>(queryKeys.whoami)?.aal).toBe(
          valid ? 3 : 1,
        )
        detach?.()
      })
    }
  }
})
