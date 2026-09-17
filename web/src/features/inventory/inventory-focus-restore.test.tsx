// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Inventory C3 focus: selection and restore destination are one TenantEstate episode.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { RequirePermission } from '@/components/layout/require-permission'
import { FEATURE_VIEWS } from '@/features/registry'
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

import { inventoryApi } from './api'
import { InventoryView } from './inventory-view'

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

const catalogOf = (tenant: string | null): CatalogEntry[] =>
  (tenant && CATALOG[tenant]) || []

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
  if (!e) throw new Error(`missing ${tenant}/${kind}/${id}`)
  return { entry: e, detail: { identity_id: `id-${tenant}-${id}` } }
}

const current = () => useTenantStore.getState().activeTenant

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

beforeEach(() => {
  whoamiMock.mockReset().mockResolvedValue(FULL)
  useSessionStore.setState({
    token: 'olvs_test',
    sessionId: 's1',
    expiresAt: null,
  })
  useTenantStore.setState({ activeTenant: 't1' })
  vi.mocked(inventoryApi.summary)
    .mockReset()
    .mockImplementation(async () => summaryOf(current()))
  vi.mocked(inventoryApi.entities)
    .mockReset()
    .mockImplementation(async () => ({
      items: catalogOf(current()),
      has_more: false,
    }))
  vi.mocked(inventoryApi.detail)
    .mockReset()
    .mockImplementation(async (kind: string, id: string) =>
      detailOf(current(), kind, id),
    )
  vi.mocked(inventoryApi.observations)
    .mockReset()
    .mockImplementation(async () => ({ items: [], has_more: false }))
})

afterEach(() => {
  useSessionStore.getState().clear()
  useTenantStore.getState().clear()
})

const nameCell = (name: string) =>
  screen.getByText(name).closest('td[role="gridcell"]') as HTMLElement

describe('Inventory focus restore', () => {
  it('returns focus to the catalog cell after Escape', async () => {
    const user = userEvent.setup()
    mount()
    const name = await screen.findByText('prod-orchestrator')
    const launcher = name.closest('td[role="gridcell"]') as HTMLElement
    await user.click(name)
    const dialog = await screen.findByRole('dialog')
    expect(dialog).toBeVisible()
    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    await waitFor(() => expect(document.activeElement).toBe(launcher))
  })

  it('returns focus to the catalog cell after the close button', async () => {
    const user = userEvent.setup()
    mount()
    const name = await screen.findByText('idle-agent')
    const launcher = name.closest('td[role="gridcell"]') as HTMLElement
    await user.click(name)
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: 'Close' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    await waitFor(() => expect(document.activeElement).toBe(launcher))
  })

  it('returns focus to the originating catalog cell after keyboard activation and Escape', async () => {
    const user = userEvent.setup()
    mount()
    await screen.findByText('prod-orchestrator')
    const grid = screen.getByRole('grid')
    grid.focus()
    await user.keyboard('{ArrowDown}')
    await waitFor(() => expect(document.activeElement?.tagName).toBe('TD'))
    const launcher = document.activeElement as HTMLElement
    await user.keyboard('{Enter}')
    expect(await screen.findByRole('dialog')).toBeVisible()
    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    await waitFor(() => expect(document.activeElement).toBe(launcher))
  })

  it('returns focus to the topology button after Escape', async () => {
    const user = userEvent.setup()
    mount()
    await screen.findByText('prod-orchestrator')
    await user.click(screen.getByRole('tab', { name: 'Topology' }))
    const launcher = await screen.findByRole('button', {
      name: 'prod-orchestrator',
    })
    await user.click(launcher)
    expect(await screen.findByRole('dialog')).toBeVisible()
    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    await waitFor(() => expect(document.activeElement).toBe(launcher))
  })

  it('falls back to the local estate container when the launcher is hidden', async () => {
    const user = userEvent.setup()
    mount()
    const name = await screen.findByText('prod-orchestrator')
    const launcher = name.closest('td[role="gridcell"]') as HTMLElement
    await user.click(name)
    expect(await screen.findByRole('dialog')).toBeVisible()
    launcher.style.display = 'none'
    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    await waitFor(() =>
      expect(document.activeElement).toBe(
        screen.getByTestId('inventory-estate-focus-fallback'),
      ),
    )
  })

  it('a new episode keeps restore on the new launcher, not the previous cell', async () => {
    const user = userEvent.setup()
    mount()
    const firstName = await screen.findByText('prod-orchestrator')
    const first = firstName.closest('td[role="gridcell"]') as HTMLElement
    await user.click(firstName)
    expect(await screen.findByRole('dialog')).toBeVisible()
    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    await waitFor(() => expect(document.activeElement).toBe(first))
    const secondName = screen.getByText('idle-agent')
    const second = secondName.closest('td[role="gridcell"]') as HTMLElement
    await user.click(secondName)
    expect(await screen.findByRole('dialog')).toBeVisible()
    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    await waitFor(() => expect(document.activeElement).toBe(second))
    expect(document.activeElement).not.toBe(first)
  })

  it('a late previous close does not steal focus from a newly opened episode', async () => {
    const user = userEvent.setup()
    mount()
    const first = await screen.findByText('prod-orchestrator')
    const firstCell = first.closest('td[role="gridcell"]') as HTMLElement
    await user.click(first)
    expect(await screen.findByRole('dialog')).toBeVisible()
    await user.keyboard('{Escape}')
    const second = screen.getByText('idle-agent')
    await user.click(second)
    const dialog = await screen.findByRole('dialog')
    await act(async () => {
      await new Promise((r) => setTimeout(r, 80))
    })
    expect(document.activeElement).not.toBe(firstCell)
    expect(
      dialog.contains(document.activeElement) ||
        document.activeElement === nameCell('idle-agent'),
    ).toBe(true)
  })

  it('unmounting the estate on tenant change does not restore onto the other tenant', async () => {
    const user = userEvent.setup()
    mount()
    const name = await screen.findByText('prod-orchestrator')
    const launcher = name.closest('td[role="gridcell"]') as HTMLElement
    await user.click(name)
    expect(await screen.findByRole('dialog')).toBeVisible()
    await switchTenant('t2')
    expect(await screen.findByText('billing-bot')).toBeInTheDocument()
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(document.activeElement).not.toBe(launcher)
    const t2Cell = screen.getByText('t2-orchestrator').closest('td')
    expect(document.activeElement).not.toBe(t2Cell)
    expect(document.activeElement).not.toBe(
      screen.getByTestId('inventory-estate-focus-fallback'),
    )
  })

  it('withdrawing catalog read unmounts the estate without restoring onto another tenant', async () => {
    const user = userEvent.setup()
    const qc = mount()
    const name = await screen.findByText('prod-orchestrator')
    await user.click(name)
    expect(await screen.findByRole('dialog')).toBeVisible()
    whoamiMock.mockResolvedValue(
      principal([grant('t1', []), grant('t2', [READ]), grant('t3', [])]),
    )
    await act(async () => {
      await qc.refetchQueries({ queryKey: queryKeys.whoami })
    })
    expect(
      await screen.findByText(
        'You do not have permission to view this. Ask an administrator for access.',
      ),
    ).toBeInTheDocument()
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(screen.queryByTestId('inventory-estate-focus-fallback')).toBeNull()
    expect(screen.queryByText('t2-orchestrator')).toBeNull()
  })
})
