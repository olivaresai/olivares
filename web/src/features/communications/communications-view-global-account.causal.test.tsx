// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// CAN3 — THE COMMUNICATIONS ROOM'S OWN ADMINISTRATION QUESTION, MEASURED AS REQUESTS.
//
// The defect is not a string and not a rendered state: it is that this room submitted a
// declared question the caller can never have answered, every five seconds, for as long
// as it stayed on screen. CAN2's final sealed embedded run measured it on the real
// product — `/communications`, an explicit `superadmin === true` credential, FOUR
// `POST /v1/auth/capabilities` answered 503 at 22:01:18, :23, :28 and :33 across a
// twelve-second hold, while the same run's `/communications/administration` route (CAN1)
// made zero. So the property measured here is the REQUEST COUNT of that same room, over
// the same twelve seconds, through the REAL hook, a real QueryClient and the real
// transport — beside an ordinary member who must keep its cadence exactly as it was.
//
// ⛔ WHY THE REAL HOOK AND NOT THE FEATURE'S CAPABILITY DOUBLE. The double answers a null
//    question `unknown`, which is the real contract — but it answers it because the
//    double says so. What is on trial is whether a suppressed question puts BYTES on the
//    wire, and a stand-in for the transport cannot testify about the transport. The
//    surface consequences (the tab, the collection) are measured through the ordinary
//    providers in communications-view.test.tsx; this file measures the wire.
//
// ⛔ AND SUPPRESSION IS NOT THE ONLY WAY TO ASK NOTHING, which is the hazard the last two
//    cases exist for. An absent principal already asks nothing — there is no context to
//    ask under — and an undetermined `superadmin` is not a global account. Reading either
//    as this family would turn "I do not know yet" into a permanent verdict, so both are
//    measured against the account that IS explicit.
import { act, render, screen } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

/** The one principal of this file, declared where the hoisted context mock can read it. */
const USER = vi.hoisted(() => '0192f2c0-eeee-7000-8000-00000000000a')

const auth = vi.hoisted(() => ({
  perms: new Set<string>(),
  tenant: 't1' as string | null,
  /** `true` | `false` | `undefined` — the third is an UNDETERMINED flag, not a global
   *  account, and `null` below is a principal that has not resolved at all. */
  superadmin: undefined as boolean | undefined,
  resolved: true,
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: auth.tenant,
    can: (p: string) => auth.perms.has(p),
    isSuperadmin: auth.superadmin === true,
    principal: auth.resolved
      ? {
          kind: 'user',
          user_id: USER,
          actor: `user:${USER}`,
          display_name: 'Ada',
          ...(auth.superadmin === undefined
            ? {}
            : { superadmin: auth.superadmin }),
          grants: [],
        }
      : null,
  }),
}))
vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
  Toaster: () => null,
}))
vi.mock('@/lib/hooks/use-url-state', () => ({
  useUrlState: () => [{}, vi.fn()],
}))
const api = vi.hoisted(() => ({
  listChannels: vi.fn(),
  listInbox: vi.fn(),
  getChannel: vi.fn(),
  getDelivery: vi.fn(),
  getMessage: vi.fn(),
  createChannel: vi.fn(),
  sendNotice: vi.fn(),
  ackDelivery: vi.fn(),
  listMembers: vi.fn(),
  listAgents: vi.fn(),
  listAdministrableChannels: vi.fn(),
  listChannelGrants: vi.fn(),
  updateChannel: vi.fn(),
  grantChannel: vi.fn(),
  revokeChannelGrant: vi.fn(),
  getCursorToken: vi.fn(),
  advanceCursor: vi.fn(),
  listHandoffInbox: vi.fn(),
  getHandoffDetail: vi.fn(),
  respondToHandoff: vi.fn(),
}))
vi.mock('./api', async (orig) => {
  const real = (await orig()) as Record<string, unknown>
  return { ...real, ...api }
})

import { queryKeys } from '@/lib/api/query'
import { configureApiClient, __resetRefreshState } from '@/lib/api/client'
import { CAPABILITY_RETRY_MS } from '@/lib/auth/capabilities'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import { useWorkspaceStore } from '@/stores/workspace'
import { CHANNEL_ADMINISTRATION_SURFACE } from './capabilities'
import { CommunicationsView } from './communications-view'
import { adminItemOf, WS } from './test-harness'
import './i18n'

const CR = 'sessions:channel:read'
const CAPABILITIES_PATH = '/v1/auth/capabilities'

/* ── the transport, and the only thing counted through it ─────────────────────── */

/** The CAN2 answer, verbatim in shape: the evidence producer cannot scope this credential
 *  family to a tenant, so the tenant self-capability route is 503 for it. */
const unavailable = () =>
  new Response(JSON.stringify({ code: 'route_decision_unavailable' }), {
    status: 503,
    headers: { 'Content-Type': 'application/json' },
  })

/** A real 200 the admission rule accepts: the surface positive pair with a live budget. */
const reachable = () =>
  new Response(
    JSON.stringify({
      schema_version: 2,
      results: [
        {
          id: 'q',
          kind: 'surface',
          state: 'reachable',
          code: 'admitted',
          refresh_after_ms: 30_000,
        },
      ],
    }),
    { status: 200, headers: { 'Content-Type': 'application/json' } },
  )

/** The ESTABLISHED surface refusal. The engine looked and said no — a different fact
 *  from a question nobody submitted, and it keeps its own visible cadence. */
const notReachable = () =>
  new Response(
    JSON.stringify({
      schema_version: 2,
      results: [
        {
          id: 'q',
          kind: 'surface',
          state: 'not_reachable',
          code: 'not_permitted',
        },
      ],
    }),
    { status: 200, headers: { 'Content-Type': 'application/json' } },
  )

type FetchArgs = [input: RequestInfo | URL, init?: RequestInit]

/** Every request this page load put on the wire, so the count below is of ONE route and
 *  not of "some fetch happened". */
function transport(answer: () => Response) {
  const calls: FetchArgs[] = []
  const spy = vi.fn(async (...args: FetchArgs) => {
    calls.push(args)
    return answer()
  })
  vi.stubGlobal('fetch', spy)
  const isCapability = ([input]: FetchArgs) =>
    String(input) === CAPABILITIES_PATH
  return {
    spy,
    /** How many CAPABILITY questions were submitted. */
    asked: () => calls.filter(isCapability).length,
    /** The parsed body of the nth submitted question. */
    body: (n: number) =>
      JSON.parse(String(calls.filter(isCapability)[n][1]?.body)) as {
        schema_version: number
        questions: {
          id: string
          kind: string
          operation: string
          workspace_id?: string
          selectors?: unknown
        }[]
      },
  }
}

const clients: QueryClient[] = []

/** The room, mounted on a real client. `whoami` is seeded to the SAME principal the
 *  mocked context reports: the hook's live re-read comes from the cache, and a test whose
 *  two halves disagree would measure a context mismatch instead of this rule. */
function room(entrance: 'catalog' | 'administration' = 'catalog') {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  clients.push(client)
  if (auth.resolved) seedWhoami(client)
  const Wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  )
  const result = render(
    <Wrapper>
      <CommunicationsView entrance={entrance} />
    </Wrapper>,
  )
  return {
    client,
    result,
    rerender: () =>
      result.rerender(
        <Wrapper>
          <CommunicationsView entrance={entrance} />
        </Wrapper>,
      ),
  }
}

function seedWhoami(client: QueryClient) {
  client.setQueryData(queryKeys.whoami, {
    kind: 'user',
    user_id: USER,
    actor: `user:${USER}`,
    display_name: 'Ada',
    superadmin: auth.superadmin === true,
    grants: [],
  })
}

async function drain() {
  for (let i = 0; i < 4; i++)
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1)
    })
}
/** Twelve seconds: the CAN2 hold, and more than two whole visible retry intervals. */
async function twelveSeconds() {
  for (let i = 0; i < 12; i++)
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1_000)
    })
}
/** What CAN2 measured for the global account: four questions in that hold. */
const MEMBER_CADENCE = 1 + Math.floor(12_000 / CAPABILITY_RETRY_MS)

const administrationTab = () =>
  screen.queryByRole('tab', { name: 'Administration' })

beforeEach(() => {
  vi.useFakeTimers()
  auth.perms = new Set([CR])
  auth.tenant = 't1'
  auth.superadmin = false
  auth.resolved = true
  for (const fn of Object.values(api)) fn.mockReset()
  api.listChannels.mockResolvedValue({ items: [], has_more: false })
  api.listAdministrableChannels.mockResolvedValue({
    items: [adminItemOf({ name: 'Ops', slug: 'ops' })],
    has_more: false,
  })
  useWorkspaceStore.setState({
    activeWorkspace: WS,
    activeWorkspaceName: 'Billing',
  })
  useTenantStore.setState({ activeTenant: 't1' })
  useSessionStore.setState({ credentialGeneration: 0 })
  configureApiClient({
    getToken: () => null,
    getTenant: () => null,
    onUnauthorized: () => {},
    refreshSession: undefined,
    getExpiresAt: undefined,
  })
})
afterEach(() => {
  for (const c of clients.splice(0)) c.clear()
  __resetRefreshState()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

describe('CAN2 reproduced: the global account room submits nothing', () => {
  it('puts ZERO capability questions on the wire across the same twelve-second hold', async () => {
    auth.superadmin = true
    const wire = transport(unavailable)
    room('catalog')
    await drain()
    await twelveSeconds()

    // CAN2 measured four here, at five-second intervals.
    expect(wire.asked()).toBe(0)
    // A question nobody asked is not an admission: the tab stays shut and the protected
    // collection is never mounted, so it never reads either.
    expect(administrationTab()).toBeNull()
    expect(api.listAdministrableChannels).not.toHaveBeenCalled()
    // …and NOTHING ELSE about the room moved: its own catalog read is untouched.
    expect(api.listChannels).toHaveBeenCalledTimes(1)
  })

  it('CONTROL — an ordinary member keeps its cadence over the same twelve seconds, on the EXACT existing question', async () => {
    const wire = transport(unavailable)
    room('catalog')
    await drain()
    const atSettle = wire.asked()
    await twelveSeconds()

    expect(atSettle).toBe(1)
    expect(wire.asked()).toBeGreaterThanOrEqual(MEMBER_CADENCE)
    // The workspace question this caller has always sent, unchanged: the mounted surface
    // pattern for THIS workspace, with no selector family.
    expect(wire.body(0)).toEqual({
      schema_version: 2,
      questions: [
        {
          id: 'q',
          kind: 'surface',
          operation: CHANNEL_ADMINISTRATION_SURFACE,
          workspace_id: WS,
        },
      ],
    })
  })

  it('AN ESTABLISHED REFUSAL IS NOT A SUPPRESSION: the member asks, is refused, and keeps asking', async () => {
    const wire = transport(notReachable)
    room('catalog')
    await drain()
    await twelveSeconds()

    // The engine looked and said no. The consequence on screen is the same shut tab as
    // the suppressed account's — and the wire says they are different facts.
    expect(administrationTab()).toBeNull()
    expect(api.listAdministrableChannels).not.toHaveBeenCalled()
    expect(wire.asked()).toBeGreaterThanOrEqual(MEMBER_CADENCE)
  })

  it('A RESOURCE ERROR IS NOT AN ACCOUNT FACT: a 503 on the room’s own catalog read changes nothing about the question', async () => {
    api.listChannels.mockRejectedValue(new Error('evidence_unavailable'))
    const wire = transport(unavailable)
    room('catalog')
    await drain()
    await twelveSeconds()

    // The member's collection failed; the member is still a member, and the declared
    // question is still submitted on its own cadence.
    expect(wire.asked()).toBeGreaterThanOrEqual(MEMBER_CADENCE)
  })
})

describe('the account moves and the rendered admission moves with it', () => {
  it('member → global drops the positive in the same render, reusing nothing and asking nothing more', async () => {
    // An EMPTY permission set: this room's only authority here is the admission the
    // engine decides, so the administration tab is also the only one the room can open —
    // which is what makes the protected collection's mount observable.
    auth.perms = new Set<string>()
    const wire = transport(reachable)
    const mounted = room('administration')
    await drain()

    // The member was admitted: the tab is offered and the protected collection loaded.
    expect(wire.asked()).toBeGreaterThanOrEqual(1)
    expect(administrationTab()).not.toBeNull()
    expect(api.listAdministrableChannels).toHaveBeenCalledTimes(1)
    const asked = wire.asked()
    const read = api.listAdministrableChannels.mock.calls.length

    // The global account arrives at the SAME boundary — same principal id, tenant and
    // credential — so the room is not remounted and the live positive is right there to
    // be reused. It is not: the answer belonged to a question this account never asks.
    auth.superadmin = true
    mounted.rerender()
    await drain()
    await twelveSeconds()

    expect(administrationTab()).toBeNull()
    expect(wire.asked()).toBe(asked)
    expect(api.listAdministrableChannels.mock.calls.length).toBe(read)
  })

  it('global → member submits the declared question again, in the CURRENT context', async () => {
    auth.perms = new Set<string>()
    auth.superadmin = true
    const wire = transport(reachable)
    const mounted = room('administration')
    await drain()
    await twelveSeconds()
    expect(wire.asked()).toBe(0)
    expect(administrationTab()).toBeNull()

    auth.superadmin = false
    mounted.rerender()
    await drain()

    expect(wire.asked()).toBeGreaterThanOrEqual(1)
    expect(administrationTab()).not.toBeNull()
    expect(api.listAdministrableChannels).toHaveBeenCalledTimes(1)
  })
})

describe('what is NOT this family', () => {
  it('AN UNDETERMINED FLAG IS NOT A GLOBAL ACCOUNT: the question is submitted and keeps its cadence', async () => {
    // A resolved principal whose payload carries no `superadmin` field at all. The
    // explicit flag is the whole rule; anything short of `=== true` is an ordinary
    // caller, and inferring the family from its absence would suppress an account the
    // engine can answer.
    auth.superadmin = undefined
    const wire = transport(unavailable)
    room('catalog')
    await drain()
    await twelveSeconds()

    expect(wire.asked()).toBeGreaterThanOrEqual(MEMBER_CADENCE)
  })

  it('AN UNRESOLVED PRINCIPAL IS NOT A GLOBAL ACCOUNT EITHER, and nothing latches: the member it becomes asks', async () => {
    auth.resolved = false
    const wire = transport(unavailable)
    const mounted = room('catalog')
    await drain()
    await twelveSeconds()

    // Nothing was asked — but for the hook's own reason, an absent authority to ask
    // under, and NOT because this room classified an absence as the global family.
    expect(wire.asked()).toBe(0)

    auth.resolved = true
    auth.superadmin = false
    seedWhoami(mounted.client)
    mounted.rerender()
    await drain()

    expect(wire.asked()).toBeGreaterThanOrEqual(1)
  })
})
