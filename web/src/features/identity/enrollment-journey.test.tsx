// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  act,
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest'
import { AuthProvider } from '@/lib/auth/context'
import { configureApiClient } from '@/lib/api/client'
import { queryKeys } from '@/lib/api/query'
import type { Whoami } from '@/lib/api/types'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import { useStepUpStore } from '@/stores/step-up'
import { StepUpHost } from '@/components/layout/step-up-host'
import { ConnectorsTab } from '@/features/console/connectors-tab'
import { StepUpPanel } from './assurance'
import { PrivilegedLoginTab } from './privileged-login'
import { createStepUpOwner } from '@/stores/step-up'

// Observe captured host props while rendering the REAL panel. No gate passthrough.
const captured = vi.hoisted(() => ({
  panels: [] as Array<{
    onElevated?: () => void
    onUnenrolled?: () => void
    isCurrentRequest?: () => boolean
  }>,
}))
vi.mock('./assurance', async (original) => {
  const actual = await original<typeof import('./assurance')>()
  return {
    ...actual,
    StepUpPanel: (props: React.ComponentProps<typeof actual.StepUpPanel>) => {
      if (props.isCurrentRequest) captured.panels.push(props)
      return <actual.StepUpPanel {...props} />
    },
  }
})
import '@/features/console/i18n'

const principal = (aal = 1): Whoami => ({
  user_id: 'u1',
  kind: 'user',
  actor: 'u1',
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
let enrolled: boolean
let serverAal: number
let whoamiResult: () => Response | Promise<Response>
let qc: QueryClient
let calls: string[]
let pivPresented: boolean
let overrides: Map<string, (init?: RequestInit) => Response | Promise<Response>>
const get = vi.fn()
const create = vi.fn()
const unauthorized = vi.fn()

function mount(
  ui = (
    <>
      <ConnectorsTab />
      <StepUpHost />
    </>
  ),
) {
  return render(
    <QueryClientProvider client={qc}>
      <AuthProvider>{ui}</AuthProvider>
    </QueryClientProvider>,
  )
}
const authenticate = () =>
  screen.getByRole('button', {
    name: /authenticate with (a )?(passkey|security key)/i,
  })
async function openAdd() {
  const user = userEvent.setup()
  await user.click(
    await screen.findByRole('button', { name: /add connector/i }),
  )
  return user
}
async function settle() {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 20))
  })
}

beforeEach(() => {
  enrolled = false
  serverAal = 1
  calls = []
  pivPresented = false
  overrides = new Map()
  captured.panels = []
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
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init?: RequestInit) => {
      const call = `${init?.method ?? 'GET'} ${url}`
      calls.push(call)
      const override = overrides.get(call)
      if (override) return override(init)
      if (url === '/v1/auth/whoami') return whoamiResult()
      if (url === '/v1/auth/piv/status')
        return json({ presented: pivPresented })
      if (url.endsWith('/authenticate/options'))
        return enrolled ? json(options) : error(400, 'no_webauthn_credential')
      if (url.endsWith('/register/options')) return json(options)
      if (url.endsWith('/register')) {
        enrolled = true
        return json({ ok: true })
      }
      if (url.endsWith('/authenticate') || url.endsWith('/piv/elevate')) {
        serverAal = 3
        return json({ ok: true, aal: 3 })
      }
      if (call === 'GET /v1/console/connectors')
        return json({
          connectors: [
            {
              kind: 'fixture',
              title: 'Fixture',
              fields_known: true,
              fields: [],
            },
          ],
        })
      if (call === 'GET /v1/console/sources') return json({ sources: [] })
      if (call === 'PUT /v1/console/connectors')
        return serverAal < 3
          ? error(403, 'step_up_required')
          : json({ applied: true })
      if (url === '/v1/auth/webauthn/credentials')
        return json({
          items: enrolled ? [{ id: 'key1', name: 'Existing key' }] : [],
        })
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

describe('Add Connector enrollment through the real gate, auth reader and transport', () => {
  it('keeps the Add title and description visible while gated', async () => {
    mount()
    await openAdd()
    expect(
      screen.getByRole('dialog', { name: /add connector/i }),
    ).toHaveAccessibleDescription()
  })
  it('registers at AAL1, authenticates explicitly, then sends one PUT only on Save', async () => {
    mount()
    const user = await openAdd()
    await user.click(authenticate())
    await user.type(
      await screen.findByRole('textbox', { name: /passkey name/i }),
      'Laptop',
    )
    await user.click(
      screen.getByRole('button', { name: /^register passkey$/i }),
    )
    await screen.findByText(/passkey registered/i)
    expect(qc.getQueryData<Whoami>(queryKeys.whoami)?.aal).toBe(1)
    expect(calls.filter((c) => c.endsWith('/authenticate'))).toHaveLength(0)
    expect(calls.filter((c) => c.startsWith('PUT'))).toHaveLength(0)
    expect(authenticate()).toHaveFocus()
    await user.click(authenticate())
    const name = await screen.findByRole('textbox', { name: /^name/i })
    expect(name).toHaveFocus()
    expect(calls.filter((c) => c.startsWith('PUT'))).toHaveLength(0)
    await user.type(name, 'fixture-one')
    await user.type(screen.getByRole('textbox', { name: /^tenant/i }), 't1')
    await user.click(screen.getByRole('button', { name: /^save/i }))
    await waitFor(() =>
      expect(calls.filter((c) => c.startsWith('PUT'))).toHaveLength(1),
    )
  })
  it('control: enrolled stable authentication reveals the form', async () => {
    enrolled = true
    mount()
    const user = await openAdd()
    await user.click(authenticate())
    expect(await screen.findByRole('textbox', { name: /^name/i })).toBeVisible()
    expect(calls.filter((c) => c === 'GET /v1/auth/whoami')).toHaveLength(1)
    expect(calls.filter((c) => c.startsWith('PUT'))).toHaveLength(0)
  })
  it('does not report elevation when the new whoami still says AAL1', async () => {
    enrolled = true
    whoamiResult = () => json(principal(1))
    const onElevated = vi.fn()
    mount(
      <StepUpPanel
        action="console"
        minAal={3}
        currentAal={1}
        onElevated={onElevated}
      />,
    )
    await userEvent.click(authenticate())
    await waitFor(() => expect(calls).toContain('GET /v1/auth/whoami'))
    await settle()
    expect(onElevated).not.toHaveBeenCalled()
    expect(qc.getQueryData<Whoami>(queryKeys.whoami)?.aal).toBe(1)
  })
})

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason: unknown) => void
  const promise = new Promise<T>((yes, no) => {
    resolve = yes
    reject = no
  })
  return { promise, resolve, reject }
}
const phases = [
  'auth options',
  'get',
  'authenticate',
  'piv',
  'whoami',
  'register options',
  'create',
  'register',
] as const
type Phase = (typeof phases)[number]
function hold(phase: Phase) {
  const network = deferred<Response>()
  const browser = deferred<typeof credential>()
  const register = ['register options', 'create', 'register'].includes(phase)
  const path =
    phase === 'auth options'
      ? 'POST /v1/auth/webauthn/authenticate/options'
      : phase === 'authenticate'
        ? 'POST /v1/auth/webauthn/authenticate'
        : phase === 'piv'
          ? 'POST /v1/auth/piv/elevate'
          : phase === 'whoami'
            ? 'GET /v1/auth/whoami'
            : phase === 'register options'
              ? 'POST /v1/auth/webauthn/register/options'
              : 'POST /v1/auth/webauthn/register'
  if (phase === 'get') get.mockReturnValue(browser.promise)
  else if (phase === 'create') create.mockReturnValue(browser.promise)
  else overrides.set(path, () => network.promise)
  return {
    register,
    reached: () =>
      phase === 'get'
        ? get.mock.calls.length > 0
        : phase === 'create'
          ? create.mock.calls.length > 0
          : calls.includes(path),
    release: async () => {
      await act(async () => {
        if (phase === 'get' || phase === 'create') browser.resolve(credential)
        else if (phase.includes('options')) network.resolve(json(options))
        else if (phase === 'whoami') network.resolve(json(principal(3)))
        else {
          if (phase === 'register') enrolled = true
          else serverAal = 3
          network.resolve(json({ ok: true }))
        }
      })
    },
  }
}
async function reach(phase: Phase) {
  const barrier = hold(phase)
  enrolled = !barrier.register
  pivPresented = phase === 'piv'
  const view = mount()
  const user = await openAdd()
  await user.click(
    phase === 'piv'
      ? await screen.findByRole('button', { name: /PIV\/CAC/i })
      : authenticate(),
  )
  if (barrier.register) {
    await user.type(
      await screen.findByRole('textbox', { name: /passkey name/i }),
      'Laptop',
    )
    await user.click(
      screen.getByRole('button', { name: /^register passkey$/i }),
    )
  }
  await waitFor(() => expect(barrier.reached()).toBe(true))
  return { ...barrier, view, user }
}
const ceremonyCalls = () =>
  calls.filter((c) => /webauthn|whoami|piv\/elevate/.test(c))

describe('each await belongs to a still-live Add intent', () => {
  for (const phase of phases) {
    it(`control: live ${phase} continuation reaches its next stage`, async () => {
      const barrier = await reach(phase)
      await barrier.release()
      if (barrier.register) {
        await screen.findByText(/passkey registered/i)
        expect(qc.getQueryData<Whoami>(queryKeys.whoami)?.aal).toBe(1)
      } else
        expect(
          await screen.findByRole('textbox', { name: /^name/i }),
        ).toBeVisible()
      expect(calls.filter((c) => c.startsWith('PUT'))).toHaveLength(0)
    })
    for (const movement of [
      'unmount',
      'credential generation',
      'tenant ABA',
      'principal ABA',
      'close/reopen',
    ] as const) {
      it(`${movement} at ${phase} retires the old continuation permanently`, async () => {
        const barrier = await reach(phase)
        const atBarrier = ceremonyCalls()
        if (movement === 'unmount') barrier.view.unmount()
        else if (movement === 'close/reopen') {
          await barrier.user.keyboard('{Escape}')
          await openAdd()
        } else
          await act(async () => {
            if (movement === 'credential generation')
              useSessionStore.getState().setSession({
                token: 'fixture-rotated',
                sessionId: 'fixture-session',
                expiresAt: '',
              })
            if (movement === 'tenant ABA') {
              useTenantStore.getState().setActiveTenant('t2')
              useTenantStore.getState().setActiveTenant('t1')
            }
            if (movement === 'principal ABA') {
              qc.setQueryData(queryKeys.whoami, {
                ...principal(),
                user_id: 'u2',
              })
              qc.setQueryData(queryKeys.whoami, principal())
            }
          })
        await barrier.release()
        await settle()
        expect(ceremonyCalls()).toEqual(atBarrier)
        expect(calls.filter((c) => c.startsWith('PUT'))).toHaveLength(0)
        expect(qc.getQueryData<Whoami>(queryKeys.whoami)?.aal).toBe(1)
        if (phase === 'register') expect(enrolled).toBe(true) // server result persists; only continuation was cancelled
        if (phase === 'auth options') expect(get).not.toHaveBeenCalled()
        if (phase === 'register options') expect(create).not.toHaveBeenCalled()
        if (movement !== 'unmount' && movement !== 'close/reopen')
          expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
      })
    }
  }
})

describe('fresh post-elevation whoami is required', () => {
  for (const method of ['webauthn', 'piv'] as const) {
    for (const response of [
      'absent',
      '500',
      '401',
      '403',
      'AAL1',
      'wrong principal',
    ] as const) {
      it(`${method} success plus ${response} whoami cannot consume a Save demand`, async () => {
        enrolled = true
        pivPresented = true
        whoamiResult = () =>
          response === 'absent'
            ? new Response(null, { status: 204 })
            : response === 'AAL1'
              ? json(principal(1))
              : response === 'wrong principal'
                ? json({ ...principal(3), user_id: 'someone-else' })
                : error(
                    Number(response),
                    response === '401' ? 'unauthenticated' : 'refused',
                  )
        const { user, request } = await rejectedSave()
        await user.click(
          method === 'webauthn'
            ? authenticate()
            : await screen.findByRole('button', { name: /PIV\/CAC/i }),
        )
        await waitFor(() =>
          expect(calls.filter((c) => c === 'GET /v1/auth/whoami')).toHaveLength(
            1,
          ),
        )
        await settle()
        expect(
          screen.queryByRole('textbox', { name: /^name/i }),
        ).not.toBeInTheDocument()
        expect(calls.filter((c) => c.startsWith('PUT'))).toHaveLength(1)
        expect(useStepUpStore.getState().request?.instance).toBe(
          request.instance,
        )
        expect(qc.getQueryData(queryKeys.whoami)).toEqual(principal(3))
        expect(unauthorized).not.toHaveBeenCalled()
        if (response === '401') expect(authenticate()).toBeDisabled()
      })
    }
  }
  for (const barrierNumber of [1, 2]) {
    for (const retired of [false, true]) {
      it(`whoami cancellation await ${barrierNumber}: ${retired ? 'retirement prevents completion' : 'live control completes'}`, async () => {
        enrolled = true
        const { user, request } = await rejectedSave()
        const pause = deferred<void>()
        const realCancel = qc.cancelQueries.bind(qc)
        let cancellations = 0
        let reached = false
        vi.spyOn(qc, 'cancelQueries').mockImplementation(async (...args) => {
          await realCancel(...args)
          if (++cancellations === barrierNumber) {
            reached = true
            await pause.promise
          }
        })
        await user.click(authenticate())
        await waitFor(() => expect(reached).toBe(true))
        expect(calls.filter((c) => c === 'GET /v1/auth/whoami')).toHaveLength(
          barrierNumber - 1,
        )
        if (retired) await user.keyboard('{Escape}')
        await act(async () => {
          pause.resolve()
        })
        await settle()
        expect(calls.filter((c) => c.startsWith('PUT'))).toHaveLength(
          retired ? 1 : 2,
        )
        expect(useStepUpStore.getState().request?.instance).not.toBe(
          request.instance,
        )
        expect(calls.filter((c) => c === 'GET /v1/auth/whoami')).toHaveLength(
          retired ? barrierNumber - 1 : 1,
        )
      })
    }
  }
  it('does not use a prior in-flight whoami or let it overwrite the fresh proof', async () => {
    enrolled = true
    const old = deferred<Whoami>()
    const prior = qc
      .fetchQuery({
        queryKey: queryKeys.whoami,
        queryFn: () => old.promise,
        staleTime: 0,
      })
      .catch(() => undefined)
    mount()
    const user = await openAdd()
    await user.click(authenticate())
    expect(await screen.findByRole('textbox', { name: /^name/i })).toBeVisible()
    expect(calls.filter((c) => c === 'GET /v1/auth/whoami')).toHaveLength(1)
    await act(async () => {
      old.resolve({ ...principal(3), user_id: 'old-response' })
      await prior
    })
    expect(qc.getQueryData(queryKeys.whoami)).toEqual(principal(3))
  })
  it('cached AAL3 arriving during options does not bypass a challenged gate', async () => {
    const barrier = await reach('auth options')
    await act(async () => {
      qc.setQueryData(queryKeys.whoami, principal(3))
    })
    expect(
      screen.queryByRole('textbox', { name: /^name/i }),
    ).not.toBeInTheDocument()
    expect(get).not.toHaveBeenCalled()
    await barrier.release()
    expect(await screen.findByRole('textbox', { name: /^name/i })).toBeVisible()
  })
})

async function rejectedSave() {
  qc.setQueryData(queryKeys.whoami, principal(3))
  const view = mount()
  const user = await openAdd()
  await user.type(
    await screen.findByRole('textbox', { name: /^name/i }),
    'current-name',
  )
  await user.type(screen.getByRole('textbox', { name: /^tenant/i }), 't1')
  await user.click(screen.getByRole('button', { name: /^save/i }))
  await screen.findByText(/step-up authentication required/i)
  await waitFor(() => expect(captured.panels.length).toBeGreaterThan(0))
  expect(calls.filter((c) => c.startsWith('PUT'))).toHaveLength(1)
  return {
    view,
    user,
    request: useStepUpStore.getState().request!,
    callback: captured.panels.at(-1)!.onElevated!,
  }
}

describe('owned Save demands and captured host callbacks', () => {
  for (const method of ['webauthn', 'piv'] as const) {
    it(`unenrolled permanently drops the captured retry, including after ${method}`, async () => {
      pivPresented = true
      const { user, request, callback } = await rejectedSave()
      const retry = request.retry!
      await user.click(authenticate())
      await screen.findByRole('textbox', { name: /passkey name/i })
      expect(useStepUpStore.getState().request?.retry).toBeUndefined()
      if (method === 'webauthn') {
        await user.type(
          screen.getByRole('textbox', { name: /passkey name/i }),
          'Laptop',
        )
        await user.click(
          screen.getByRole('button', { name: /^register passkey$/i }),
        )
        await screen.findByText(/passkey registered/i)
      }
      await user.click(
        method === 'webauthn'
          ? authenticate()
          : screen.getByRole('button', { name: /PIV\/CAC/i }),
      )
      const name = await screen.findByRole('textbox', { name: /^name/i })
      await act(async () => {
        retry()
        callback()
      })
      await settle()
      expect(calls.filter((c) => c.startsWith('PUT'))).toHaveLength(1)
      expect(name).toHaveValue('current-name')
      expect(screen.getAllByRole('dialog')).toHaveLength(1)
      await user.clear(name)
      await user.type(name, 'reviewed-name')
      await user.click(screen.getByRole('button', { name: /^save/i }))
      await waitFor(() =>
        expect(calls.filter((c) => c.startsWith('PUT'))).toHaveLength(2),
      )
      const puts = vi
        .mocked(fetch)
        .mock.calls.filter(([, init]) => init?.method === 'PUT')
      expect(JSON.parse(String(puts[1][1]?.body)).name).toBe('reviewed-name')
    })
  }
  it('control: enrolled stable Save resumes once after real host verification', async () => {
    enrolled = true
    const { user, callback } = await rejectedSave()
    await user.click(authenticate())
    await waitFor(() =>
      expect(calls.filter((c) => c.startsWith('PUT'))).toHaveLength(2),
    )
    await act(async () => {
      callback()
      callback()
    })
    await settle()
    expect(calls.filter((c) => c.startsWith('PUT'))).toHaveLength(2)
  })
  it('closing the Save owner revokes the already-published retry and captured completion', async () => {
    enrolled = true
    const { user, request, callback } = await rejectedSave()
    await user.keyboard('{Escape}')
    expect(useStepUpStore.getState().request).toBeNull()
    await act(async () => {
      request.retry?.()
      callback()
    })
    await settle()
    expect(calls.filter((c) => c.startsWith('PUT'))).toHaveLength(1)
  })
  it('an old callback cannot consume or clear a replacement demand', async () => {
    const { user, request, callback } = await rejectedSave()
    await user.keyboard('{Escape}')
    const retry = vi.fn()
    await act(async () => {
      useStepUpStore
        .getState()
        .require({ action: 'identity', owner: createStepUpOwner(qc), retry })
    })
    const replacement = useStepUpStore.getState().request!
    await screen.findByRole('dialog', { name: /needs an elevated session/i })
    await act(async () => {
      request.retry?.()
      callback()
    })
    expect(useStepUpStore.getState().request?.instance).toBe(
      replacement.instance,
    )
    expect(retry).not.toHaveBeenCalled()
    expect(calls.filter((c) => c.startsWith('PUT'))).toHaveLength(1)
  })
  for (const phase of [
    'auth options',
    'get',
    'authenticate',
    'piv',
    'whoami',
  ] as const) {
    it(`a replacement demand during ${phase} survives the old response and callbacks`, async () => {
      enrolled = true
      pivPresented = phase === 'piv'
      const { user, request, callback } = await rejectedSave()
      const barrier = hold(phase)
      await user.click(
        phase === 'piv'
          ? await screen.findByRole('button', { name: /PIV\/CAC/i })
          : authenticate(),
      )
      await waitFor(() => expect(barrier.reached()).toBe(true))
      const before = ceremonyCalls()
      await user.keyboard('{Escape}')
      const nextRetry = vi.fn()
      await act(async () => {
        useStepUpStore.getState().require({
          action: 'identity',
          owner: createStepUpOwner(qc),
          retry: nextRetry,
        })
      })
      const replacement = useStepUpStore.getState().request!.instance
      await barrier.release()
      await act(async () => {
        request.retry?.()
        callback()
      })
      await settle()
      expect(useStepUpStore.getState().request?.instance).toBe(replacement)
      expect(nextRetry).not.toHaveBeenCalled()
      expect(ceremonyCalls()).toEqual(before)
      expect(calls.filter((c) => c.startsWith('PUT'))).toHaveLength(1)
    })
  }
  it('closing Add preserves another operation’s demand', async () => {
    const view = mount(<ConnectorsTab />)
    await openAdd()
    const owner = createStepUpOwner(qc)
    await act(async () => {
      useStepUpStore.getState().require({ action: 'other', owner })
    })
    const instance = useStepUpStore.getState().request!.instance
    await userEvent.keyboard('{Escape}')
    expect(useStepUpStore.getState().request?.instance).toBe(instance)
    view.unmount()
    owner.retire()
  })
})

describe('registration outcomes and independent factors', () => {
  it.each(['network', '500', 'empty'] as const)(
    'unknown register finish (%s) never resends or automatically authenticates',
    async (outcome) => {
      overrides.set('POST /v1/auth/webauthn/register', () => {
        enrolled = true // server may have persisted before its response was lost
        if (outcome === 'network')
          throw new TypeError('fixture connection lost')
        return outcome === '500'
          ? error(500, 'internal')
          : new Response(null, { status: 204 })
      })
      mount()
      const user = await openAdd()
      await user.click(authenticate())
      await user.type(
        await screen.findByRole('textbox', { name: /passkey name/i }),
        'Laptop',
      )
      await user.click(
        screen.getByRole('button', { name: /^register passkey$/i }),
      )
      await screen.findByText(/registration could not be confirmed/i)
      await settle()
      expect(
        calls.filter((c) => c === 'POST /v1/auth/webauthn/register'),
      ).toHaveLength(1)
      expect(calls.filter((c) => c.endsWith('/authenticate'))).toHaveLength(0)
      expect(
        screen.queryByRole('button', { name: /^register passkey$/i }),
      ).not.toBeInTheDocument()
      await user.click(authenticate())
      expect(
        await screen.findByRole('textbox', { name: /^name/i }),
      ).toBeVisible()
      expect(calls.filter((c) => c.startsWith('PUT'))).toHaveLength(0)
    },
  )
  it('unknown authentication finish offers an explicit session check without resending', async () => {
    enrolled = true
    overrides.set('POST /v1/auth/webauthn/authenticate', () => {
      serverAal = 3
      throw new TypeError('lost response')
    })
    mount()
    const user = await openAdd()
    await user.click(authenticate())
    await screen.findByText(/authentication could not be confirmed/i)
    expect(calls).not.toContain('GET /v1/auth/whoami')
    await user.click(screen.getByRole('button', { name: /check session/i }))
    expect(await screen.findByRole('textbox', { name: /^name/i })).toBeVisible()
    expect(calls.filter((c) => c.endsWith('/authenticate'))).toHaveLength(1)
  })
  it.each(['NotAllowedError', 'null', 'verification refusal'] as const)(
    '%s during registration does not elevate or resend',
    async (failure) => {
      if (failure === 'NotAllowedError')
        create.mockRejectedValue(
          new DOMException('dismissed', 'NotAllowedError'),
        )
      else if (failure === 'null') create.mockResolvedValue(null)
      else
        overrides.set('POST /v1/auth/webauthn/register', () =>
          error(403, 'webauthn_verification_failed'),
        )
      mount()
      const user = await openAdd()
      await user.click(authenticate())
      await user.type(
        await screen.findByRole('textbox', { name: /passkey name/i }),
        'Laptop',
      )
      await user.click(
        screen.getByRole('button', { name: /^register passkey$/i }),
      )
      await waitFor(() => expect(create).toHaveBeenCalledTimes(1))
      await settle()
      expect(calls.filter((c) => c.endsWith('/register'))).toHaveLength(
        failure === 'verification refusal' ? 1 : 0,
      )
      expect(calls.filter((c) => c.endsWith('/authenticate'))).toHaveLength(0)
      expect(qc.getQueryData<Whoami>(queryKeys.whoami)?.aal).toBe(1)
    },
  )
  for (const method of ['webauthn', 'piv'] as const) {
    for (const failure of [
      '401',
      'verification refusal',
      'not served',
    ] as const) {
      it(`${method} final ${failure} does not verify, resume or resend`, async () => {
        enrolled = true
        pivPresented = true
        const path =
          method === 'webauthn'
            ? 'POST /v1/auth/webauthn/authenticate'
            : 'POST /v1/auth/piv/elevate'
        const code =
          failure === '401' ? 401 : failure === 'not served' ? 501 : 403
        overrides.set(path, () =>
          error(
            code,
            failure === '401'
              ? 'unauthenticated'
              : 'webauthn_verification_failed',
          ),
        )
        const { user, request } = await rejectedSave()
        await user.click(
          method === 'webauthn'
            ? authenticate()
            : await screen.findByRole('button', { name: /PIV\/CAC/i }),
        )
        await waitFor(() => expect(calls).toContain(path))
        await settle()
        expect(calls.filter((c) => c === path)).toHaveLength(1)
        expect(calls).not.toContain('GET /v1/auth/whoami')
        expect(calls.filter((c) => c.startsWith('PUT'))).toHaveLength(1)
        expect(useStepUpStore.getState().request?.instance).toBe(
          request.instance,
        )
        expect(unauthorized).not.toHaveBeenCalled()
        if (failure === '401') {
          expect(authenticate()).toBeDisabled()
          expect(
            screen.getByRole('button', { name: /PIV\/CAC/i }),
          ).toBeDisabled()
          expect(screen.getByText(/sign in again/i)).toBeVisible()
        }
        if (failure === 'not served')
          expect(
            screen.getByText(/backend endpoint is not live yet/i),
          ).toBeVisible()
      })
    }
  }
  it('a concurrent first enrollment refusal requests Authenticate, not another registration', async () => {
    overrides.set('POST /v1/auth/webauthn/register', () => {
      enrolled = true
      return error(403, 'step_up_required')
    })
    mount()
    const user = await openAdd()
    await user.click(authenticate())
    await user.type(
      await screen.findByRole('textbox', { name: /passkey name/i }),
      'Laptop',
    )
    await user.click(
      screen.getByRole('button', { name: /^register passkey$/i }),
    )
    await screen.findByText(/existing passkey must authenticate/i)
    expect(authenticate()).toHaveFocus()
    expect(
      screen.queryByRole('button', { name: /^register passkey$/i }),
    ).not.toBeInTheDocument()
    expect(calls.filter((c) => c.endsWith('/register'))).toHaveLength(1)
  })
  it('presented PIV works with WebAuthn unavailable', async () => {
    pivPresented = true
    vi.stubGlobal('PublicKeyCredential', undefined)
    mount()
    const user = await openAdd()
    await user.click(authenticate())
    await screen.findByText(/does not support WebAuthn/i)
    await user.click(screen.getByRole('button', { name: /PIV\/CAC/i }))
    expect(await screen.findByRole('textbox', { name: /^name/i })).toBeVisible()
    expect(create).not.toHaveBeenCalled()
    expect(get).not.toHaveBeenCalled()
  })
  it('missing create gives honest enrollment recovery and keeps presented PIV usable', async () => {
    pivPresented = true
    Object.defineProperty(navigator, 'credentials', {
      configurable: true,
      value: { get },
    })
    mount()
    const user = await openAdd()
    await user.click(authenticate())
    await screen.findByText(/cannot register a passkey/i)
    expect(
      screen.queryByRole('button', { name: /^register passkey$/i }),
    ).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /PIV\/CAC/i }))
    expect(await screen.findByRole('textbox', { name: /^name/i })).toBeVisible()
  })
  it('the shared console action name does not opt generic panels into enrollment', async () => {
    mount(<StepUpPanel action="console" minAal={3} currentAal={1} />)
    await userEvent.click(authenticate())
    await screen.findByText(/no passkey is registered/i)
    expect(
      screen.queryByRole('textbox', { name: /passkey name/i }),
    ).not.toBeInTheDocument()
  })
  it.each([401, 501])(
    'additional-key final %s has explicit recovery without another registration',
    async (status) => {
      qc.setQueryData(queryKeys.whoami, principal(3))
      serverAal = 3
      enrolled = true
      overrides.set('POST /v1/auth/webauthn/register', () =>
        error(status, status === 401 ? 'unauthenticated' : 'not_served'),
      )
      mount(
        <>
          <PrivilegedLoginTab />
          <StepUpHost />
        </>,
      )
      const user = userEvent.setup()
      await user.click(
        await screen.findByRole('button', { name: /^register passkey$/i }),
      )
      const dialog = screen.getByRole('dialog')
      await user.type(within(dialog).getByRole('textbox'), 'Additional key')
      await user.click(
        within(dialog).getByRole('button', { name: /^register passkey$/i }),
      )
      expect(
        await within(dialog).findByText(
          status === 401
            ? /sign in again/i
            : /backend endpoint is not live yet/i,
        ),
      ).toBeVisible()
      if (status === 401)
        expect(
          within(dialog).getByRole('button', { name: /^register passkey$/i }),
        ).toBeDisabled()
      expect(calls.filter((c) => c.endsWith('/register'))).toHaveLength(1)
      expect(calls.filter((c) => c.endsWith('/authenticate'))).toHaveLength(0)
      expect(unauthorized).not.toHaveBeenCalled()
    },
  )
  it('additional-key unknown finish keeps registration disabled with applicable recovery copy', async () => {
    qc.setQueryData(queryKeys.whoami, principal(3))
    serverAal = 3
    enrolled = true
    overrides.set('POST /v1/auth/webauthn/register', () => {
      throw new TypeError('lost response')
    })
    mount(
      <>
        <PrivilegedLoginTab />
        <StepUpHost />
      </>,
    )
    const user = userEvent.setup()
    await user.click(
      await screen.findByRole('button', { name: /^register passkey$/i }),
    )
    const dialog = screen.getByRole('dialog')
    await user.type(within(dialog).getByRole('textbox'), 'Additional key')
    await user.click(
      within(dialog).getByRole('button', { name: /^register passkey$/i }),
    )
    expect(
      await within(dialog).findByText(
        /close this dialog and check your passkeys/i,
      ),
    ).toBeVisible()
    expect(
      within(dialog).getByRole('button', { name: /^register passkey$/i }),
    ).toBeDisabled()
    expect(calls.filter((c) => c.endsWith('/register'))).toHaveLength(1)
    expect(calls.filter((c) => c.endsWith('/authenticate'))).toHaveLength(0)
  })
  it('additional-key registration still authenticates with an existing factor and starts fresh options', async () => {
    enrolled = true
    overrides.set('POST /v1/auth/webauthn/register/options', () =>
      serverAal < 3 ? error(403, 'step_up_required') : json(options),
    )
    mount(
      <>
        <PrivilegedLoginTab />
        <StepUpHost />
      </>,
    )
    const user = userEvent.setup()
    await user.click(
      await screen.findByRole('button', { name: /^register passkey$/i }),
    )
    const dialog = screen.getByRole('dialog')
    await user.type(within(dialog).getByRole('textbox'), 'Additional key')
    await user.click(
      within(dialog).getByRole('button', { name: /^register passkey$/i }),
    )
    await waitFor(() =>
      expect(useStepUpStore.getState().request).not.toBeNull(),
    )
    const host = await screen.findByRole('dialog', {
      name: /needs an elevated session/i,
    })
    await user.click(
      within(host).getByRole('button', { name: /authenticate with passkey/i }),
    )
    await waitFor(() =>
      expect(
        calls.filter((c) => c === 'POST /v1/auth/webauthn/register'),
      ).toHaveLength(1),
    )
    expect(
      calls.filter((c) => c === 'POST /v1/auth/webauthn/register/options'),
    ).toHaveLength(2)
    expect(create).toHaveBeenCalledTimes(1)
    expect(qc.getQueryData<Whoami>(queryKeys.whoami)?.aal).toBe(3)
  })
})
