// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { afterEach, expect, it, vi } from 'vitest'
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from '@tanstack/react-router'
import {
  act,
  cleanup,
  createTestQueryClient,
  renderIntel,
  screen,
  userEvent,
  waitFor,
} from '@/test/intel'
import { RequirePermission } from '@/components/layout/require-permission'
import { FEATURE_VIEWS } from '@/features/registry'
import { AuthProvider, useAuth } from '@/lib/auth/context'
import { queryKeys } from '@/lib/api/query'
import type { Whoami } from '@/lib/api/types'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import { finopsKeys } from './api'
import { BudgetsTab } from './finops-view'
import raw from './evidence-fixtures.json'

const api = vi.hoisted(() => ({
  whoami: vi.fn(),
  budgets: vi.fn(),
  budgetStatus: vi.fn(),
  alerts: vi.fn(),
}))
vi.mock('@/lib/api/endpoints', async (original) => ({
  ...(await original<typeof import('@/lib/api/endpoints')>()),
  authApi: { whoami: api.whoami },
}))
vi.mock('./api', async (original) => ({
  ...(await original<typeof import('./api')>()),
  finopsApi: api,
}))

// Keep the real route permission and Auth/RBAC consumer, while isolating the
// real budgets tab from the unrelated spend/chart queries in FinOpsView.
const finopsView = FEATURE_VIEWS.find((view) => view.id === 'finops')!
function BudgetSurface() {
  const { activeTenant, can } = useAuth()
  return (
    <RequirePermission view={finopsView}>
      <output
        data-testid="spend-surface"
        data-tenant={activeTenant}
        data-budget-read={can('finops:budget:read')}
      />
      <BudgetsTab canWrite={can('finops:budget:write')} />
    </RequirePermission>
  )
}

afterEach(() => {
  cleanup()
  useSessionStore.getState().clear()
  useTenantStore.getState().clear()
  vi.clearAllMocks()
})

it('retires budget content and observers on read withdrawal within the same tenant, then recovers without the old reference', async () => {
  const tenant = raw.exact.alerts.items[0].amount_evidence.envelope.tenant_id
  const reference = raw.exact.alerts.items[0].id
  const principal = (budgetRead: boolean): Whoami => ({
    kind: 'user',
    user_id: 'budget-reader',
    actor: 'budget-reader',
    display_name: 'Budget reader',
    superadmin: false,
    grants: [
      {
        tenant,
        role: 'viewer',
        permissions: [
          'finops:spend:read',
          ...(budgetRead ? ['finops:budget:read'] : []),
        ],
      },
    ],
  })
  api.whoami.mockResolvedValue(principal(true))
  api.budgets.mockResolvedValue({ items: [raw.exact.status], has_more: false })
  api.budgetStatus.mockResolvedValue(raw.exact.status)
  api.alerts.mockImplementation((params?: { alert_id?: string }) =>
    Promise.resolve(
      params?.alert_id === reference
        ? raw.exact.alerts
        : { items: [], has_more: false },
    ),
  )
  // Synthetic session only; no refresh timer or HTTP login in this fixture.
  useSessionStore.setState({
    token: 'fixture-only-budget-session',
    sessionId: 'fixture-only-budget-session-id',
    expiresAt: null,
  })
  useTenantStore.getState().setActiveTenant(tenant)
  const qc = createTestQueryClient()
  // Retain inactive cache entries: zero observers must come from unmounting,
  // not from an artificial cache clear/GC hiding the authority defect.
  qc.setDefaultOptions({ queries: { retry: false, gcTime: Infinity } })
  expect(finopsView.permission).toBe('finops:spend:read')
  const root = createRootRoute({
    component: () => (
      <AuthProvider>
        <Outlet />
      </AuthProvider>
    ),
  })
  const route = createRoute({
    getParentRoute: () => root,
    path: finopsView.path,
    component: BudgetSurface,
  })
  const router = createRouter({
    routeTree: root.addChildren([route]),
    history: createMemoryHistory({ initialEntries: [finopsView.path] }),
  })
  const view = renderIntel(<RouterProvider router={router} />, {
    queryClient: qc,
  })
  const surface = await screen.findByTestId('spend-surface')
  await userEvent.type(
    await screen.findByLabelText('Alert reference'),
    reference,
  )
  await userEvent.click(screen.getByRole('button', { name: 'Look up' }))
  await waitFor(() => expect(screen.getAllByText('$12.00')).toHaveLength(2))
  expect(screen.queryByRole('button', { name: 'New budget' })).toBeNull()
  const keys = [
    finopsKeys.budgets(tenant),
    finopsKeys.budgetStatus(tenant, raw.exact.status.id),
    finopsKeys.alerts(tenant, { alert_id: reference }),
  ]
  for (const key of keys) {
    expect(
      qc.getQueryCache().find({ queryKey: key })?.getObserversCount(),
    ).toBe(1)
  }
  const callsBeforeDenial = [
    api.budgets.mock.calls.length,
    api.budgetStatus.mock.calls.length,
    api.alerts.mock.calls.length,
  ]

  // A fresh whoami response updates the actual AuthProvider; no mock can(),
  // tenant switch, logout or direct rerender drives the withdrawal.
  api.whoami.mockResolvedValue(principal(false))
  await act(async () => {
    await qc.refetchQueries({ queryKey: queryKeys.whoami })
  })
  await waitFor(() =>
    expect(surface).toHaveAttribute('data-budget-read', 'false'),
  )
  expect(screen.getByTestId('spend-surface')).toBe(surface)
  expect(surface).toHaveAttribute('data-tenant', tenant)
  expect(screen.queryByLabelText('Alert reference')).toBeNull()
  expect(screen.queryByRole('button', { name: 'Look up' })).toBeNull()
  expect(screen.queryAllByText('$12.00')).toHaveLength(0)
  for (const key of keys) {
    const query = qc.getQueryCache().find({ queryKey: key })
    expect(query?.state.data).toBeDefined()
    expect(query?.getObserversCount()).toBe(0)
  }
  await act(async () => {
    await qc.invalidateQueries({ queryKey: ['finops', tenant] })
  })
  expect([
    api.budgets.mock.calls.length,
    api.budgetStatus.mock.calls.length,
    api.alerts.mock.calls.length,
  ]).toEqual(callsBeforeDenial)

  api.whoami.mockResolvedValue(principal(true))
  await act(async () => {
    await qc.refetchQueries({ queryKey: queryKeys.whoami })
  })
  const input = await screen.findByLabelText('Alert reference')
  expect(input).toHaveValue('')
  expect(screen.queryByRole('button', { name: 'Clear' })).toBeNull()
  expect(screen.queryByRole('button', { name: 'New budget' })).toBeNull()
  await waitFor(() =>
    expect(api.alerts).toHaveBeenLastCalledWith(undefined, { tenant }),
  )
  expect(
    qc.getQueryCache().find({ queryKey: keys[2] })?.getObserversCount(),
  ).toBe(0)
  await userEvent.type(input, reference)
  await userEvent.click(screen.getByRole('button', { name: 'Look up' }))
  await waitFor(() => expect(screen.getAllByText('$12.00')).toHaveLength(2))
  expect(api.alerts).toHaveBeenLastCalledWith(
    { alert_id: reference },
    { tenant },
  )
  expect(screen.getByTestId('spend-surface')).toBe(surface)
  expect(surface).toHaveAttribute('data-tenant', tenant)
  // The actual registered route is also authoritative: losing spend read
  // unmounts its child even when budget read remains available.
  const withoutSpend = principal(true)
  withoutSpend.grants[0].permissions = ['finops:budget:read']
  api.whoami.mockResolvedValue(withoutSpend)
  await act(async () => {
    await qc.refetchQueries({ queryKey: queryKeys.whoami })
  })
  await waitFor(() => expect(screen.queryByTestId('spend-surface')).toBeNull())
  expect(screen.queryByLabelText('Alert reference')).toBeNull()
  view.unmount()
  qc.clear()
})
