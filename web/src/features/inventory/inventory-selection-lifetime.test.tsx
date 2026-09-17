// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Inventory — the selected entry belongs to the tenant it was selected in (2026-09-08).
//
// The view keeps the selected CatalogEntry in React state while every read keys by
// `activeTenant`. On the base (`32c48665e6`) a selection made under T1 outlived a switch
// to T2: the sheet stayed open on T1's card and its point read re-keyed to T2 with T1's
// kind/id, so the same id in T2 was read and shown under T1's name, and an id absent
// from T2 was reported as "no longer in the catalog". Each causal below says whether it
// REPRODUCES A DEFECT of that base or is a POSITIVE CONTROL the base already satisfied.
//
// Composition: the real AuthProvider, the real tenant and session stores, the real route
// gate (RequirePermission over the registry's own `inventory` view), the real
// InventoryView with its tabs, DataTable and sheet, the real `inventoryKeys` and a real
// QueryClient. The tenant switch is the store setter the TenantSwitcher receives through
// `useAuth().setActiveTenant` (lib/auth/context.tsx, components/layout/tenant-switcher.tsx).
//
// Two doubles, both named here and nowhere else:
//   · AUTH DOUBLE — `authApi.whoami` answers with a fixture principal whose per-tenant
//     permission sets carry the real `inventory:catalog:read` (modules/inventory/api.go:24,
//     features/registry.tsx). `can()` is the real membership test over that answer.
//   · API DOUBLE — `inventoryApi.*` answers per tenant, reading the tenant store at call
//     time the way the transport reads it for X-Olivares-Tenant (lib/api/client.ts), and
//     records which tenant each read was made under. Nothing here reaches an engine, so
//     nothing here is evidence about HTTP authorization: the engine checks the permission
//     per request, and this file measures what the console asks for and shows.
//
// Until whoami answers, the gate refuses (no principal is no permission); every causal
// waits for content before asserting, so that initial state is never mistaken for a
// withdrawal. The shell gates routes on the authenticated status before they render.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { RequirePermission } from '@/components/layout/require-permission'
import { FEATURE_VIEWS } from '@/features/registry'
import { ApiError } from '@/lib/api/errors'
import { queryKeys } from '@/lib/api/query'
import type { Grant, Whoami } from '@/lib/api/types'
import { AuthProvider } from '@/lib/auth/context'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import type { CatalogEntry, EntityDetail, InventorySummary } from './types'

const whoamiMock = vi.hoisted(() => vi.fn())

vi.mock('@/lib/api/endpoints', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api/endpoints')>()
  return {
    ...actual,
    authApi: {
      ...actual.authApi,
      whoami: (...a: unknown[]) => whoamiMock(...a),
      logout: () => Promise.resolve(),
    },
  }
})

vi.mock('@tanstack/react-router', () => ({
  useRouterState: () => '',
  Link: ({ children, to }: { children: ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
  useRouter: () => undefined,
}))

// Only the transport is doubled; the real `inventoryKeys` stay so the real cache is
// what proves which tenant a read was keyed under.
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return {
    ...actual,
    inventoryApi: {
      summary: vi.fn(),
      entities: vi.fn(),
      detail: vi.fn(),
      observations: vi.fn(),
    },
  }
})

import { inventoryApi, inventoryKeys } from './api'
import { InventoryView } from './inventory-view'

/* ── fixtures ─────────────────────────────────────────────────────────────────── */

const READ = 'inventory:catalog:read'

const INVENTORY_VIEW = FEATURE_VIEWS.find((v) => v.id === 'inventory')!

const grant = (tenant: string, permissions: string[]): Grant => ({
  tenant,
  role: 'viewer',
  permissions,
})

const principal = (grants: Grant[]): Whoami => ({
  kind: 'user',
  user_id: 'u1',
  actor: 'u1',
  display_name: 'Operator',
  superadmin: false,
  grants,
})

/** Catalog read in T1 and T2; member of T3 without the catalog permission. */
const FULL = principal([
  grant('t1', [READ]),
  grant('t2', [READ]),
  grant('t3', []),
])

const entry = (
  kind: string,
  id: string,
  name: string,
  ref: string,
  status = 'active',
): CatalogEntry => ({
  kind,
  entity_id: id,
  name,
  ref,
  status,
  signal_sources: ['otel'],
  hosts: ['host-1'],
  first_seen: '2026-06-03T10:00:00Z',
  last_seen: '2026-06-04T07:00:00Z',
  occurrence_count: 1,
})

/** T2 holds the SAME kind and id as T1's first entry, under another name. */
const CATALOG: Record<string, CatalogEntry[]> = {
  t1: [
    entry('agent', 'a1', 'prod-orchestrator', 'sess-orch'),
    entry('agent', 'a2', 'idle-agent', 'sess-idle', 'stale'),
  ],
  t2: [
    entry('agent', 'a1', 't2-orchestrator', 'sess-t2-orch'),
    entry('agent', 'b1', 'billing-bot', 'sess-bill'),
  ],
}

const PROJECTION: Record<string, Record<string, unknown>> = {
  't1/agent/a1': { identity_id: 'identity-7' },
  't1/agent/a2': { identity_id: 'identity-8' },
  't2/agent/a1': { identity_id: 'identity-99' },
  't2/agent/b1': { identity_id: 'identity-42' },
}

const forbidden = (tenant: string | null) =>
  new ApiError(403, 'forbidden', `no catalog read in ${tenant}`, 'req-403')

const catalogOf = (tenant: string | null): CatalogEntry[] => {
  const items = tenant ? CATALOG[tenant] : undefined
  if (!items) throw forbidden(tenant)
  return items
}

const summaryOf = (tenant: string | null): InventorySummary => {
  const items = catalogOf(tenant)
  const by_kind: InventorySummary['by_kind'] = {}
  for (const e of items) {
    const kc = (by_kind[e.kind] ??= { active: 0, stale: 0, total: 0 })
    if (e.status === 'stale') kc.stale += 1
    else kc.active += 1
    kc.total += 1
  }
  return { by_kind, by_source: { otel: items.length }, total: items.length }
}

const detailOf = (
  tenant: string | null,
  kind: string,
  id: string,
): EntityDetail => {
  const e = catalogOf(tenant).find((x) => x.kind === kind && x.entity_id === id)
  if (!e) throw new ApiError(404, 'not_found', 'not found', 'req-404')
  return { entry: e, detail: PROJECTION[`${tenant}/${kind}/${id}`] }
}

/* ── the API double's ledger ──────────────────────────────────────────────────── */

type Read = {
  op: 'summary' | 'entities' | 'detail' | 'observations'
  tenant: string | null
  kind?: string
  id?: string
}
const reads: Read[] = []

/** The tenant a request would carry: read at call time, like the transport does. */
const current = () => useTenantStore.getState().activeTenant

const detailReads = () =>
  reads
    .filter((r) => r.op === 'detail')
    .map((r) => `${r.tenant}/${r.kind}/${r.id}`)

/* ── composition ──────────────────────────────────────────────────────────────── */

function mount() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <AuthProvider>
        <RequirePermission view={INVENTORY_VIEW}>
          <InventoryView />
        </RequirePermission>
      </AuthProvider>
    </QueryClientProvider>,
  )
  return qc
}

const switchTenant = (tenant: string) =>
  act(() => useTenantStore.getState().setActiveTenant(tenant))

/** Give any would-be refetch or re-key every chance to fire before asserting it did not. */
const settle = () =>
  act(async () => {
    await new Promise((r) => setTimeout(r, 30))
  })

const openSheet = async (
  user: ReturnType<typeof userEvent.setup>,
  name: string,
) => {
  await user.click(await screen.findByText(name))
  return screen.findByRole('dialog')
}

const ROUTE_REFUSAL =
  'You do not have permission to view this. Ask an administrator for access.'

beforeEach(() => {
  reads.length = 0
  whoamiMock.mockReset().mockResolvedValue(FULL)
  useSessionStore.setState({
    token: 'olvs_test',
    sessionId: 's1',
    expiresAt: null,
  })
  useTenantStore.setState({ activeTenant: 't1' })
  vi.mocked(inventoryApi.summary)
    .mockReset()
    .mockImplementation(async () => {
      const tenant = current()
      reads.push({ op: 'summary', tenant })
      return summaryOf(tenant)
    })
  vi.mocked(inventoryApi.entities)
    .mockReset()
    .mockImplementation(async () => {
      const tenant = current()
      reads.push({ op: 'entities', tenant })
      return { items: catalogOf(tenant), has_more: false }
    })
  vi.mocked(inventoryApi.detail)
    .mockReset()
    .mockImplementation(async (kind: string, id: string) => {
      const tenant = current()
      reads.push({ op: 'detail', tenant, kind, id })
      return detailOf(tenant, kind, id)
    })
  vi.mocked(inventoryApi.observations)
    .mockReset()
    .mockImplementation(async (kind: string, id: string) => {
      const tenant = current()
      reads.push({ op: 'observations', tenant, kind, id })
      return { items: [], has_more: false }
    })
})

afterEach(() => {
  useSessionStore.getState().clear()
  useTenantStore.getState().clear()
})

describe('Inventory selection: tenant lifetime', () => {
  it('REPRODUCES A DEFECT: a selection made under T1 is retired by a switch to T2 — the sheet closes and no T2 point read carries T1 entry', async () => {
    const user = userEvent.setup()
    const qc = mount()
    const sheet = await openSheet(user, 'prod-orchestrator')
    expect(await within(sheet).findByText(/identity-7/)).toBeInTheDocument()
    expect(detailReads()).toEqual(['t1/agent/a1'])

    await switchTenant('t2')
    expect(await screen.findByText('billing-bot')).toBeInTheDocument()
    await settle()

    // On the base the sheet stayed open on T1's card and a second point read went
    // out keyed by T2 with T1's kind/id.
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(detailReads()).toEqual(['t1/agent/a1'])
    await waitFor(() =>
      expect(
        qc.getQueryData(inventoryKeys.detail('t1', 'agent', 'a1')),
      ).toBeUndefined(),
    )
    await waitFor(() =>
      expect(
        qc.getQueryData(inventoryKeys.observations('t1', 'agent', 'a1')),
      ).toBeUndefined(),
    )
  })

  it('REPRODUCES A DEFECT: the same kind/id in T2 is read only when selected in T2, under T2 own card — never through T1 selection', async () => {
    const user = userEvent.setup()
    const qc = mount()
    const first = await openSheet(user, 'prod-orchestrator')
    expect(await within(first).findByText(/identity-7/)).toBeInTheDocument()

    await switchTenant('t2')
    expect(await screen.findByText('t2-orchestrator')).toBeInTheDocument()
    await settle()
    // On the base: T1's title over T2's projection (identity-99) in one open sheet.
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(detailReads()).toEqual(['t1/agent/a1'])

    // The authorized reselection in T2: T2's card, T2's projection, T2's key.
    const sheet = await openSheet(user, 't2-orchestrator')
    expect(within(sheet).getByText('sess-t2-orch')).toBeInTheDocument()
    expect(await within(sheet).findByText(/identity-99/)).toBeInTheDocument()
    expect(within(sheet).queryByText('prod-orchestrator')).toBeNull()
    expect(within(sheet).queryByText('identity-7')).toBeNull()
    expect(detailReads()).toEqual(['t1/agent/a1', 't2/agent/a1'])
    await waitFor(() =>
      expect(
        qc.getQueryData(inventoryKeys.detail('t1', 'agent', 'a1')),
      ).toBeUndefined(),
    )
    expect(qc.getQueryData(inventoryKeys.detail('t2', 'agent', 'a1'))).toEqual(
      detailOf('t2', 'agent', 'a1'),
    )
  })

  it('REPRODUCES A DEFECT: a T1 point read still in flight at the switch lands under T1 key only and is never shown; a T1→T2→T1 bounce does not resurrect the selection', async () => {
    let resolveLate!: (d: EntityDetail) => void
    const late = new Promise<EntityDetail>((r) => {
      resolveLate = r
    })
    vi.mocked(inventoryApi.detail).mockImplementation(
      async (kind: string, id: string) => {
        const tenant = current()
        reads.push({ op: 'detail', tenant, kind, id })
        if (tenant === 't1' && id === 'a1') return late
        return detailOf(tenant, kind, id)
      },
    )
    const user = userEvent.setup()
    const qc = mount()
    const sheet = await openSheet(user, 'prod-orchestrator')
    expect(within(sheet).getByText('sess-orch')).toBeInTheDocument()
    expect(within(sheet).queryByText(/identity-/)).toBeNull()

    await switchTenant('t2')
    expect(await screen.findByText('billing-bot')).toBeInTheDocument()
    expect(screen.queryByRole('dialog')).toBeNull()

    await act(async () => {
      resolveLate(detailOf('t1', 'agent', 'a1'))
      await late
    })
    await settle()
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(screen.queryByText(/identity-7/)).toBeNull()
    await waitFor(() =>
      expect(
        qc.getQueryData(inventoryKeys.detail('t1', 'agent', 'a1')),
      ).toBeUndefined(),
    )
    expect(
      qc.getQueryData(inventoryKeys.detail('t2', 'agent', 'a1')),
    ).toBeUndefined()
    expect(detailReads()).toEqual(['t1/agent/a1'])

    await switchTenant('t1')
    expect(await screen.findByText('prod-orchestrator')).toBeInTheDocument()
    await settle()
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(detailReads()).toEqual(['t1/agent/a1'])

    // An authorized reselection under T1 is the only way back to that card.
    const again = await openSheet(user, 'prod-orchestrator')
    expect(await within(again).findByText(/identity-7/)).toBeInTheDocument()
    expect(within(again).queryByText(/identity-99/)).toBeNull()
  })

  it('POSITIVE CONTROL (route boundary): withdrawing inventory:catalog:read in the current tenant unmounts the view, the open sheet with it, and stops further reads', async () => {
    const user = userEvent.setup()
    const qc = mount()
    const sheet = await openSheet(user, 'prod-orchestrator')
    expect(await within(sheet).findByText(/identity-7/)).toBeInTheDocument()
    const before = reads.length

    // The engine's next whoami no longer lists the permission in T1.
    whoamiMock.mockResolvedValue(
      principal([grant('t1', []), grant('t2', [READ]), grant('t3', [])]),
    )
    await act(async () => {
      await qc.refetchQueries({ queryKey: queryKeys.whoami })
    })

    expect(await screen.findByText(ROUTE_REFUSAL)).toBeInTheDocument()
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(screen.queryByText('prod-orchestrator')).toBeNull()
    await settle()
    expect(reads.length).toBe(before)
  })

  it('POSITIVE CONTROL (route boundary): switching to a tenant whose grant lacks inventory:catalog:read refuses the route — no sheet, no read under that tenant', async () => {
    const user = userEvent.setup()
    mount()
    const sheet = await openSheet(user, 'prod-orchestrator')
    expect(await within(sheet).findByText(/identity-7/)).toBeInTheDocument()

    await switchTenant('t3')
    expect(await screen.findByText(ROUTE_REFUSAL)).toBeInTheDocument()
    expect(screen.queryByRole('dialog')).toBeNull()
    await settle()
    expect(reads.filter((r) => r.tenant === 't3')).toEqual([])
  })

  it('REPRODUCES A DEFECT, and guards what stays: a switch retires ONLY the selection — the facets, the active tab and the tiles survive it, and selection and reselection keep working in either tenant', async () => {
    const user = userEvent.setup()
    mount()
    let sheet = await openSheet(user, 'prod-orchestrator')
    expect(await within(sheet).findByText(/identity-7/)).toBeInTheDocument()
    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    sheet = await openSheet(user, 'idle-agent')
    expect(await within(sheet).findByText(/identity-8/)).toBeInTheDocument()
    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())

    // Controls set under T1: a kind facet and the topology tab, then a card.
    await user.click(screen.getByRole('combobox', { name: 'All kinds' }))
    await user.click(await screen.findByRole('option', { name: 'Agent' }))
    await user.click(screen.getByRole('tab', { name: 'Topology' }))
    expect(await screen.findByText('Who acts')).toBeInTheDocument()
    sheet = await openSheet(user, 'idle-agent')
    expect(await within(sheet).findByText(/identity-8/)).toBeInTheDocument()

    await switchTenant('t2')
    expect(await screen.findByText('t2-orchestrator')).toBeInTheDocument()
    await settle()
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(screen.getByRole('tab', { name: 'Topology' })).toHaveAttribute(
      'aria-selected',
      'true',
    )
    expect(
      screen.getByRole('combobox', { name: 'All kinds' }),
    ).toHaveTextContent('Agent')
    expect(await screen.findByText('Entities')).toBeInTheDocument()

    // And a T2 selection from the topology is T2's card with T2's projection.
    sheet = await openSheet(user, 'billing-bot')
    expect(within(sheet).getByText('sess-bill')).toBeInTheDocument()
    expect(await within(sheet).findByText(/identity-42/)).toBeInTheDocument()
    expect(detailReads()).toEqual([
      't1/agent/a1',
      't1/agent/a2',
      't1/agent/a2',
      't2/agent/b1',
    ])
  })
})
