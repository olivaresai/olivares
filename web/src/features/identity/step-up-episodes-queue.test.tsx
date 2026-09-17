// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import {
  MutationCache,
  onlineManager,
  QueryClient,
  QueryClientProvider,
} from '@tanstack/react-query'
import { act, cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AuthProvider } from '@/lib/auth/context'
import { configureApiClient } from '@/lib/api/client'
import { queryKeys } from '@/lib/api/query'
import { ConnectorsTab } from '@/features/console/connectors-tab'
import { StepUpHost } from '@/components/layout/step-up-host'
import { createStepUpOwner, useStepUpStore } from '@/stores/step-up'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import '@/features/console/i18n'

// Observe the real gate's callback at element creation. Every element, gate, panel,
// hook and transport still executes normally; no assurance/mutation passthrough.
const callbacks = vi.hoisted(() => ({
  gate: [] as Array<() => void>,
  observe(type: unknown, props: unknown) {
    const p = props as {
      onElevated?: () => void
      isCurrentRequest?: () => boolean
    } | null
    if (
      typeof type === 'function' &&
      type.name === 'StepUpPanel' &&
      p?.onElevated &&
      !p.isCurrentRequest
    )
      this.gate.push(p.onElevated)
  },
}))
vi.mock('react/jsx-runtime', async (load) => {
  const actual = await load<typeof import('react/jsx-runtime')>()
  return {
    ...actual,
    jsx: (...args: Parameters<typeof actual.jsx>) => {
      callbacks.observe(args[0], args[1])
      return actual.jsx(...args)
    },
    jsxs: (...args: Parameters<typeof actual.jsxs>) => {
      callbacks.observe(args[0], args[1])
      return actual.jsxs(...args)
    },
  }
})
vi.mock('react/jsx-dev-runtime', async (load) => {
  const actual = await load<typeof import('react/jsx-dev-runtime')>()
  return {
    ...actual,
    jsxDEV: (...args: Parameters<typeof actual.jsxDEV>) => {
      callbacks.observe(args[0], args[1])
      return actual.jsxDEV(...args)
    },
  }
})

const actor = (aal = 3) => ({
  kind: 'user',
  user_id: 'u1',
  actor: 'u1',
  display_name: 'Operator',
  superadmin: true,
  grants: [],
  aal,
})
const bytes = new Uint8Array([1]).buffer
const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
const refusal = (status: number, code: string) =>
  json({ error: { code, message: code } }, status)
function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((r) => {
    resolve = r
  })
  return { promise, resolve }
}
type Barrier =
  'offline queue' | 'before onMutate' | 'preventive refresh' | '401 replay'
let qc: QueryClient
let phase: Barrier | undefined
let queued: ReturnType<typeof deferred<void>>
let renewing: ReturnType<typeof deferred<boolean>>
let queueReached: boolean
let elevated: boolean
let putCount: number
let accepted: number
let whoamiCount: number
let expires: string | null
const refresh = vi.fn()
const unauthorized = vi.fn()
const replacements: ReturnType<typeof createStepUpOwner>[] = []

beforeEach(() => {
  phase = undefined
  elevated = false
  queueReached = false
  putCount = 0
  accepted = 0
  whoamiCount = 0
  expires = null
  callbacks.gate = []
  queued = deferred<void>()
  renewing = deferred<boolean>()
  onlineManager.setOnline(true)
  qc = new QueryClient({
    mutationCache: new MutationCache({
      onMutate: async () => {
        if (phase === 'before onMutate' && elevated) {
          queueReached = true
          await queued.promise
        }
      },
    }),
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  qc.setQueryData(queryKeys.whoami, actor())
  useStepUpStore.getState().clear()
  useTenantStore.setState({ activeTenant: 't1' })
  useSessionStore
    .getState()
    .setSession({ token: 'fixture', sessionId: 'fixture', expiresAt: '' })
  refresh.mockReset().mockImplementation(() => renewing.promise)
  unauthorized.mockReset()
  configureApiClient({
    getToken: () => useSessionStore.getState().token,
    getTenant: () => useTenantStore.getState().activeTenant,
    getExpiresAt: () => expires,
    onUnauthorized: unauthorized,
    refreshSession: refresh,
  })
  vi.stubGlobal('PublicKeyCredential', function PublicKeyCredential() {})
  Object.defineProperty(navigator, 'credentials', {
    configurable: true,
    value: {
      get: async () => ({
        id: 'key1',
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
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init?: RequestInit) => {
      if (url === '/v1/auth/piv/status') return json({ presented: false })
      if (url === '/v1/auth/webauthn/authenticate/options')
        return json({ publicKey: { challenge: 'AQ' } })
      if (url === '/v1/auth/webauthn/authenticate') {
        elevated = true
        return json({ ok: true, aal: 3 })
      }
      if (url === '/v1/auth/whoami') {
        whoamiCount++
        if (phase === 'offline queue') onlineManager.setOnline(false)
        if (phase === 'preventive refresh')
          expires = new Date(Date.now() + 1_000).toISOString()
        return json(actor(3))
      }
      if (url === '/v1/console/connectors' && init?.method === 'PUT') {
        putCount++
        if (!elevated) return refusal(403, 'step_up_required')
        if (phase === '401 replay' && putCount === 2)
          return refusal(401, 'unauthenticated')
        accepted++
        return json({ applied: true })
      }
      if (url === '/v1/console/connectors')
        return json({
          connectors: [{ kind: 'fixture', fields_known: true, fields: [] }],
        })
      if (url === '/v1/console/sources') return json({ sources: [] })
      throw new Error(`Unhandled fixture request: ${url}`)
    }),
  )
})
afterEach(async () => {
  cleanup()
  replacements.splice(0).forEach((o) => o.retire())
  queued.resolve()
  renewing.resolve(true)
  onlineManager.setOnline(true)
  await qc.resumePausedMutations()
  qc.clear()
  vi.unstubAllGlobals()
})
function app(showOwner = true) {
  return (
    <QueryClientProvider client={qc}>
      <AuthProvider>
        {showOwner && <ConnectorsTab />}
        <StepUpHost />
      </AuthProvider>
    </QueryClientProvider>
  )
}
const authenticate = () =>
  screen.getByRole('button', { name: /authenticate with passkey/i })
async function openAdd() {
  const user = userEvent.setup()
  await user.click(
    await screen.findByRole('button', { name: /add connector/i }),
  )
  return user
}
const setAal = async (aal: number) => {
  await act(async () => {
    qc.setQueryData(queryKeys.whoami, actor(aal))
    await new Promise((resolve) => setTimeout(resolve, 0)) // deliver the real query observer notification
  })
}

describe('IR1: proof belongs to the current real Add gate challenge', () => {
  it('control: initially sufficient AAL shows the form without a ceremony or PUT', async () => {
    render(app())
    await openAdd()
    expect(await screen.findByRole('textbox', { name: /^name/i })).toBeVisible()
    expect(whoamiCount).toBe(0)
    expect(putCount).toBe(0)
  })
  for (const proof of [
    'incidental cache',
    'old callback',
    'fresh whoami',
  ] as const) {
    it(`second episode: ${proof === 'fresh whoami' ? 'control accepts fresh whoami' : `rejects ${proof}`}`, async () => {
      await setAal(1)
      render(app())
      const user = await openAdd()
      expect(callbacks.gate.length).toBeGreaterThan(0)
      const old = callbacks.gate.at(-1)!
      await user.click(authenticate())
      expect(
        await screen.findByRole('textbox', { name: /^name/i }),
      ).toBeVisible()
      expect(whoamiCount).toBe(1)
      await setAal(1)
      expect(authenticate()).toBeVisible()
      if (proof === 'fresh whoami') {
        await user.click(authenticate())
        expect(
          await screen.findByRole('textbox', { name: /^name/i }),
        ).toBeVisible()
        expect(whoamiCount).toBe(2)
      } else {
        if (proof === 'old callback')
          await act(async () => {
            old()
          })
        await setAal(3)
        expect(
          screen.queryByRole('textbox', { name: /^name/i }),
        ).not.toBeInTheDocument()
        expect(authenticate()).toBeVisible()
        expect(whoamiCount).toBe(1)
      }
      expect(putCount).toBe(0)
    })
  }
})

describe('IR2: the consumed Save retains its original dispatch authority', () => {
  for (const barrier of [
    'offline queue',
    'before onMutate',
    'preventive refresh',
    '401 replay',
  ] as const) {
    for (const movement of [
      'live',
      'close',
      'unmount',
      'tenant ABA',
      'credential generation',
      'replacement demand',
      'replacement cleared',
    ] as const) {
      it(`${barrier}: ${movement === 'live' ? 'control sends one accepted Save' : `${movement} prevents subsequent PUT`}`, async () => {
        phase = barrier
        const view = render(app())
        const user = await openAdd()
        await user.type(
          screen.getByRole('textbox', { name: /^name/i }),
          'queued-save',
        )
        await user.type(screen.getByRole('textbox', { name: /^tenant/i }), 't1')
        await user.click(screen.getByRole('button', { name: /^save/i }))
        await user.click(
          await screen.findByRole('button', {
            name: /authenticate with passkey/i,
          }),
        )
        if (barrier === 'offline queue')
          await waitFor(() =>
            expect(qc.getMutationCache().getAll().at(-1)?.state.isPaused).toBe(
              true,
            ),
          )
        else if (barrier === 'before onMutate')
          await waitFor(() => expect(queueReached).toBe(true))
        else await waitFor(() => expect(refresh).toHaveBeenCalledTimes(1))
        const mutation = qc.getMutationCache().getAll().at(-1)!
        expect(mutation.state.status).toBe('pending')
        expect(useStepUpStore.getState().request).toBeNull() // consume has really happened
        expect(whoamiCount).toBe(1)
        const atBarrier = putCount
        expect(atBarrier).toBe(barrier === '401 replay' ? 2 : 1)
        expect(accepted).toBe(0)
        if (movement === 'live') {
          const name = screen.getByRole('textbox', { name: /^name/i })
          await user.clear(name)
          await user.type(name, 'edited-after-queue')
        }
        if (movement === 'close') await user.keyboard('{Escape}')
        else if (movement === 'unmount') view.rerender(app(false))
        else
          await act(async () => {
            if (movement === 'tenant ABA') {
              useTenantStore.getState().setActiveTenant('t2')
              useTenantStore.getState().setActiveTenant('t1')
            } else if (movement === 'credential generation') {
              useSessionStore.getState().setSession({
                token: 'fixture-new',
                sessionId: 'fixture',
                expiresAt: '',
              })
            }
          })
        let replacement: number | undefined
        const replacementRetry = vi.fn()
        if (
          movement === 'replacement demand' ||
          movement === 'replacement cleared'
        )
          await act(async () => {
            const owner = createStepUpOwner(qc)
            replacements.push(owner)
            expect(
              useStepUpStore.getState().require({
                action: 'identity',
                owner,
                retry: replacementRetry,
              }),
            ).toBe(true)
            replacement = useStepUpStore.getState().request!.instance
            if (movement === 'replacement cleared')
              useStepUpStore.getState().clear(replacement)
          })
        await act(async () => {
          expires = null
          queued.resolve()
          renewing.resolve(true)
          onlineManager.setOnline(true)
          await qc.resumePausedMutations()
        })
        await waitFor(() => expect(mutation.state.status).not.toBe('pending'))
        expect(putCount - atBarrier).toBe(movement === 'live' ? 1 : 0)
        expect(accepted).toBe(movement === 'live' ? 1 : 0)
        expect(replacementRetry).not.toHaveBeenCalled()
        if (movement === 'replacement demand')
          expect(useStepUpStore.getState().request?.instance).toBe(replacement)
        if (movement === 'replacement cleared')
          expect(useStepUpStore.getState().request).toBeNull()
        if (movement === 'live') {
          const puts = vi
            .mocked(fetch)
            .mock.calls.filter(([, init]) => init?.method === 'PUT')
          expect(JSON.parse(String(puts.at(-1)![1]?.body)).name).toBe(
            'queued-save',
          )
        }
        expect(unauthorized).not.toHaveBeenCalled()
      })
    }
  }
})
