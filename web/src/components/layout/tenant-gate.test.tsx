// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The property this file exists for: WITH ZERO ORGANIZATIONS THE CONSOLE EMITS NO
// TENANT-SCOPED REQUEST.
//
// A superadmin never gets the engine's single-membership tenant default
// (core/api/middleware.go resolveTenantValue), so with nothing selected the client
// sends no X-Olivares-Tenant and every tenant-scoped route answers 400 "tenant
// required". The estate overview alone mounts a dozen such reads, which is what an
// operator experienced as "the panel does not work".
//
// The assertion is deny-closed on the WIRE: the whole authenticated shell is
// rendered against a stubbed `fetch` (the REAL api client, the REAL AuthProvider,
// the REAL front-door view as the routed content) and every request the app makes
// must be one of the handful of tenant-INDEPENDENT paths. Anything else — today's
// /v1/m/* reads or a route added tomorrow — fails the test by name.
//
// The negative control matters as much: the same shell WITH a tenant must issue
// those tenant-scoped reads. A gate that silenced the console forever would pass
// the first test and fail this one.
import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ReactElement, ReactNode } from 'react'

// The router is not mounted in jsdom: Outlet renders whatever a test puts in the
// holder (the production view under test), the location is what a test declares,
// and Link/Navigate degrade to anchors.
const routerMock = vi.hoisted(() => ({
  outlet: (() => null) as () => ReactElement | null,
  pathname: '/',
}))
vi.mock('@tanstack/react-router', () => ({
  Outlet: () => routerMock.outlet(),
  Navigate: ({ to }: { to: string }) => <div data-testid="navigate">{to}</div>,
  Link: ({ to, children }: { to: string; children: ReactNode }) => (
    <a href={to}>{children}</a>
  ),
  useRouterState: () => routerMock.pathname,
  useNavigate: () => () => {},
}))

import { Providers } from '@/app/providers'
import { HomeView } from '@/features/home/home-view'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import { useWorkspaceStore } from '@/stores/workspace'
import { SettingsPage } from '@/app/pages/settings'
import { AppLayout } from './app-layout'

/** The only paths that carry no tenant: the engine resolves nothing tenant-shaped
 * for them, so the shell may call them with no organization selected. Everything
 * else under /v1 is tenant-scoped (or, at minimum, unproven to be safe) and must
 * not be requested — this list is the allowlist, so the check is deny-closed. */
const TENANT_INDEPENDENT = new Set([
  '/v1/server-info',
  '/v1/auth/whoami',
  '/v1/auth/logout',
  // Added 2026-08-17 by the integrator, resolving a collision between two lanes that were each
  // correct alone: Landed the PROACTIVE session refresh (the console schedules it from the
  // `expires_at` it had been storing and never reading), so the shell now calls this before a
  // tenant is chosen and these five cases went red on `['/v1/auth/refresh']`.
  //
  // It belongs on the allowlist on the ENGINE'S evidence, not on convenience: `core/api/openapi.go`
  // declares it `op("refreshToken", "Renew the calling session token (rotates the credential,
  // extends expiry)", tagAuth, …)` — the same `tagAuth` family as `/v1/auth/logout`, which is
  // already here, and it resolves nothing tenant-shaped because it renews THE CALLING SESSION.
  //
  // And the allowlist entry is not what proves it sends no tenant: the assertion below on
  // `wire.tenantHeader` is untouched and still expects an EMPTY set, so if the refresh ever
  // started claiming a tenant this test would fail on that line instead. The deny-closed shape
  // of the list is preserved — one path added with its reason, nothing widened.
  '/v1/auth/refresh',
  '/v1/system/orgs',
])

const TENANT_ID = '018f5a20-0000-7000-8000-00000000beef'

const superadminNoGrants = {
  kind: 'user',
  user_id: 'u-1',
  actor: 'admin@example.com',
  display_name: 'Admin',
  superadmin: true,
  grants: [],
}

function orgFixture(over: Partial<Record<string, unknown>> = {}) {
  return {
    id: TENANT_ID,
    tenant_id: TENANT_ID,
    name: 'Acme Corp',
    slug: 'acme-corp',
    status: 'active',
    created_at: '2026-08-05T10:00:00Z',
    ...over,
  }
}

interface Wire {
  /** Every path requested, in order (query string stripped). */
  paths: string[]
  /** Requests that carried the tenant header, as `path` → header value. */
  tenantHeader: Map<string, string>
  /** JSON bodies of the POSTs, by path. */
  posted: Map<string, unknown>
}

/** A route's answer: a JSON body (200), or an explicit status + body. A function
 * sees the request, so a route can answer differently per method or per call. */
type Reply = unknown | { status: number; body: unknown }
type Handler = Reply | ((init: RequestInit | undefined) => Reply)

function asReply(r: Reply): { status: number; body: unknown } {
  return r !== null && typeof r === 'object' && 'status' in r
    ? (r as { status: number; body: unknown })
    : { status: 200, body: r }
}

/**
 * Stub `fetch` with the engine's real contract: routes in `handlers` answer as
 * declared, and ANY other path answers exactly what the engine answers a
 * tenant-scoped route called with no tenant — 400 bad_request "tenant required".
 * A leak therefore fails the assertion AND reproduces the operator's symptom.
 */
function stubWire(handlers: Record<string, Handler>): Wire {
  const wire: Wire = { paths: [], tenantHeader: new Map(), posted: new Map() }
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const raw = String(input)
      const path = raw.split('?')[0] ?? raw
      wire.paths.push(path)
      const headers = new Headers(init?.headers)
      const tenant = headers.get('X-Olivares-Tenant')
      if (tenant) wire.tenantHeader.set(path, tenant)
      if (init?.method === 'POST' && typeof init.body === 'string')
        wire.posted.set(path, JSON.parse(init.body))
      const handler = handlers[path]
      if (handler === undefined)
        return new Response(
          JSON.stringify({
            error: { code: 'bad_request', message: 'tenant required' },
          }),
          { status: 400, headers: { 'Content-Type': 'application/json' } },
        )
      const { status, body } =
        typeof handler === 'function'
          ? asReply((handler as (i: RequestInit | undefined) => Reply)(init))
          : asReply(handler)
      return new Response(JSON.stringify(body), {
        status,
        headers: { 'Content-Type': 'application/json' },
      })
    }),
  )
  return wire
}

/** Paths that are neither tenant-independent nor allowed here. */
function scopedCalls(wire: Wire): string[] {
  return [...new Set(wire.paths)].filter((p) => !TENANT_INDEPENDENT.has(p))
}

function renderShell() {
  return render(
    <Providers>
      <AppLayout />
    </Providers>,
  )
}

/** Let every mount-time query actually reach `fetch` before judging the wire: a
 * leak that simply has not been issued YET must not read as absence. */
async function settle() {
  await act(async () => {
    await new Promise((r) => setTimeout(r, 150))
  })
}

beforeEach(() => {
  localStorage.clear()
  useSessionStore.setState({
    token: 'olvs_test',
    sessionId: 's-1',
    expiresAt: '2099-01-01T00:00:00Z',
  })
  useTenantStore.setState({ activeTenant: null })
  useWorkspaceStore.getState().clear()
  routerMock.pathname = '/'
  // The front door — the view a fresh operator actually lands on, and the one
  // that mounts the dozen tenant-scoped reads this gate exists to hold back.
  routerMock.outlet = () => <HomeView />
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe('TenantGate — zero organizations', () => {
  it('emits NO tenant-scoped request and offers the first organization', async () => {
    const wire = stubWire({
      '/v1/auth/whoami': superadminNoGrants,
      '/v1/system/orgs': { items: [], has_more: false },
    })

    renderShell()

    // The console DID load the principal and DID look at the org list before
    // deciding — this is not a vacuous pass — and every mount-time query has had
    // its chance to fire. The wire is judged FIRST and independently of what the
    // screen shows, so removing the gate fails on the property itself.
    await waitFor(() => expect(wire.paths).toContain('/v1/system/orgs'))
    expect(wire.paths).toContain('/v1/auth/whoami')
    await settle()

    // The property: not one request outside the tenant-independent allowlist.
    expect(scopedCalls(wire)).toEqual([])
    // And nothing ever went out claiming a tenant.
    expect([...wire.tenantHeader.keys()]).toEqual([])

    // The operator is taken to the one action that unblocks the console…
    expect(
      await screen.findByRole('button', { name: /create organization/i }),
    ).toBeInTheDocument()
    // …and the routed view is not mounted at all (no half-rendered dashboard).
    expect(screen.queryByRole('link', { name: /view the full/i })).toBeNull()
  })

  it('creates the first organization and lands on a working panel', async () => {
    const user = userEvent.setup()
    let created = false
    const wire = stubWire({
      '/v1/auth/whoami': superadminNoGrants,
      // The POST creates it; every LATER listing then fails. The console must
      // land on the tenant the CREATE answered with — it does not need a second
      // read to know what it just made, and a listing that stops answering must
      // not strand the operator on the screen they have already left behind.
      '/v1/system/orgs': (init: RequestInit | undefined) => {
        if (init?.method === 'POST') {
          created = true
          return { status: 201, body: orgFixture() }
        }
        return created
          ? {
              status: 500,
              body: { error: { code: 'internal', message: 'boom' } },
            }
          : { items: [], has_more: false }
      },
      '/v1/m/finops/spend/summary': { total_micro_usd: 0, truncated: false },
    })

    renderShell()

    await user.type(
      await screen.findByLabelText(/organization name/i),
      'Acme Corp',
    )
    // The slug is derived from the name, and the engine is told exactly that.
    expect(screen.getByDisplayValue('acme-corp')).toBeInTheDocument()
    await user.click(
      screen.getByRole('button', { name: /create organization/i }),
    )

    await waitFor(() =>
      expect(wire.posted.get('/v1/system/orgs')).toEqual({
        name: 'Acme Corp',
        slug: 'acme-corp',
      }),
    )
    // The tenant the ENGINE returned is selected, so the routed content mounts
    // and its reads now carry the header (they were never sent before).
    await waitFor(() =>
      expect(useTenantStore.getState().activeTenant).toBe(TENANT_ID),
    )
    await waitFor(() =>
      expect(wire.tenantHeader.get('/v1/m/finops/spend/summary')).toBe(
        TENANT_ID,
      ),
    )
  })

  it('says it could not look instead of offering to create a second organization', async () => {
    const wire = stubWire({ '/v1/auth/whoami': superadminNoGrants })
    // /v1/system/orgs is unhandled → 400. "I could not read the list" must never
    // render as "there is none".
    renderShell()

    expect(await screen.findByRole('alert')).toHaveTextContent(
      /couldn't load the organizations/i,
    )
    expect(
      screen.queryByRole('button', { name: /create organization/i }),
    ).toBeNull()
    expect(scopedCalls(wire)).toEqual([])
  })

  it('still lets /settings through — and that page asks for nothing either', async () => {
    // The named pass-through: personal preferences must stay reachable for a
    // principal with no organization (the user menu points there). The exception
    // is only sound while that page reads nothing tenant-scoped, which is what
    // the wire assertion below pins down.
    routerMock.pathname = '/settings'
    routerMock.outlet = () => <SettingsPage />
    const wire = stubWire({
      '/v1/auth/whoami': superadminNoGrants,
      '/v1/system/orgs': { items: [], has_more: false },
    })

    renderShell()

    // The real settings page rendered — not the first-organization gate.
    expect(
      await screen.findByRole('heading', { name: /settings/i }),
    ).toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: /create organization/i }),
    ).toBeNull()
    await settle()
    expect(scopedCalls(wire)).toEqual([])
  })

  it('tells a member with no membership the truth, and asks nothing', async () => {
    const wire = stubWire({
      '/v1/auth/whoami': { ...superadminNoGrants, superadmin: false },
    })
    renderShell()

    expect(
      await screen.findByText(/not a member of any organization/i),
    ).toBeInTheDocument()
    // Not even the org listing: /v1/system/orgs is superadmin-only.
    expect(wire.paths).not.toContain('/v1/system/orgs')
    expect(scopedCalls(wire)).toEqual([])
  })
})

describe('TenantGate — an organization exists', () => {
  it('selects the single organization and lets the tenant-scoped reads through', async () => {
    const wire = stubWire({
      '/v1/auth/whoami': superadminNoGrants,
      '/v1/system/orgs': { items: [orgFixture()], has_more: false },
      '/v1/m/finops/spend/summary': { total_micro_usd: 0, truncated: false },
    })

    renderShell()

    // NEGATIVE CONTROL: the front door's reads DO happen once a tenant exists —
    // the gate holds requests back, it does not delete them.
    await waitFor(() =>
      expect(wire.tenantHeader.get('/v1/m/finops/spend/summary')).toBe(
        TENANT_ID,
      ),
    )
    expect(useTenantStore.getState().activeTenant).toBe(TENANT_ID)
  })

  it('asks which one when there are several, and only then sends the header', async () => {
    const user = userEvent.setup()
    const second = '018f5a20-0000-7000-8000-0000000faded'
    const wire = stubWire({
      '/v1/auth/whoami': superadminNoGrants,
      '/v1/system/orgs': {
        items: [
          orgFixture(),
          orgFixture({
            id: second,
            tenant_id: second,
            name: 'Globex',
            slug: 'globex',
          }),
        ],
        has_more: false,
      },
      '/v1/m/finops/spend/summary': { total_micro_usd: 0, truncated: false },
    })

    renderShell()

    // While the question is open, nothing tenant-scoped is requested — judged
    // first, so a gate that fell through here fails on the property itself.
    await waitFor(() => expect(wire.paths).toContain('/v1/system/orgs'))
    await settle()
    expect(scopedCalls(wire)).toEqual([])

    // Both organizations are offered by name; nothing is picked for the operator.
    expect(
      await screen.findByRole('button', { name: /acme corp/i }),
    ).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /globex/i })).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: /globex/i }))

    await waitFor(() =>
      expect(wire.tenantHeader.get('/v1/m/finops/spend/summary')).toBe(second),
    )
  })
})
