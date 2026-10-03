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
  cleanup,
  createTestQueryClient,
  renderIntel,
  screen,
  waitFor,
} from '@/test/intel'
import { RequirePermission } from '@/components/layout/require-permission'
import { FEATURE_VIEWS } from '@/features/registry'
import { AuthProvider, useAuth } from '@/lib/auth/context'
import type { Whoami } from '@/lib/api/types'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import { BudgetsTab } from './finops-view'
import type { AdmissionReconciliation } from './types'

/**
 * The admission line beside the budgets: an operator with budget read sees the holds
 * nobody settled, the drift, and every counter recovery left outstanding, each under
 * its own name and only when it is not zero. It says nothing when there is nothing to
 * say, and an operator without budget read never reaches it.
 */

const TENANT = 't-admission-line'

const api = vi.hoisted(() => ({
  whoami: vi.fn(),
  budgets: vi.fn(),
  budgetStatus: vi.fn(),
  alerts: vi.fn(),
  admissionReconciliation: vi.fn(),
}))
vi.mock('@/lib/api/endpoints', async (original) => ({
  ...(await original<typeof import('@/lib/api/endpoints')>()),
  authApi: { whoami: api.whoami },
}))
vi.mock('./api', async (original) => ({
  ...(await original<typeof import('./api')>()),
  finopsApi: api,
}))

const finopsView = FEATURE_VIEWS.find((view) => view.id === 'finops')!

function BudgetSurface() {
  const { can } = useAuth()
  return (
    <RequirePermission view={finopsView}>
      <output data-testid="ready" />
      <BudgetsTab canWrite={can('finops:budget:write')} />
    </RequirePermission>
  )
}

function principal(budgetRead: boolean): Whoami {
  return {
    kind: 'user',
    user_id: 'budget-reader',
    actor: 'budget-reader',
    display_name: 'Budget reader',
    superadmin: false,
    grants: [
      {
        tenant: TENANT,
        role: 'viewer',
        permissions: [
          'finops:spend:read',
          ...(budgetRead ? ['finops:budget:read'] : []),
        ],
      },
    ],
  }
}

const quiet: AdmissionReconciliation = {
  swept_expired: 0,
  active: 0,
  committed: 7,
  released: 2,
  expired_unsettled: 0,
  active_lapsed: 0,
  idempotency_orphans: 0,
  pending_retired: 0,
  owed_released: 0,
  owed_cleared: 0,
  owed_remaining: 0,
  legacy_pending: 0,
  legacy_retired: 0,
  legacy_owes_release: 0,
  legacy_stop: 'absent',
  unresolved: 0,
  undecodable: 0,
  undecodable_cleared: 0,
  frontier_blocked: 0,
  corrupt: 0,
  drift: false,
  note: 'reservation ledger matches commits and releases',
}

async function renderBudgets(budgetRead: boolean) {
  api.whoami.mockResolvedValue(principal(budgetRead))
  api.budgets.mockResolvedValue({ items: [], has_more: false })
  api.budgetStatus.mockResolvedValue(undefined)
  api.alerts.mockResolvedValue({ items: [], has_more: false })
  useSessionStore.setState({
    csrfToken: 'fixture-only-admission-session',
    sessionId: 'fixture-only-admission-session-id',
    expiresAt: null,
  })
  useTenantStore.getState().setActiveTenant(TENANT)
  const qc = createTestQueryClient()
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
  renderIntel(<RouterProvider router={router} />, { queryClient: qc })
  await screen.findByTestId('ready')
}

afterEach(() => {
  cleanup()
  useSessionStore.getState().clear()
  useTenantStore.getState().clear()
  vi.clearAllMocks()
})

it('reports unsettled holds and the drift beside the budgets they belong to', async () => {
  api.admissionReconciliation.mockResolvedValue({
    ...quiet,
    active: 3,
    active_lapsed: 2,
    idempotency_orphans: 1,
    drift: true,
    note: 'reservation ledger drifted from caller settlement',
  } satisfies AdmissionReconciliation)

  await renderBudgets(true)

  const line = await screen.findByTestId('admission-drift')
  expect(line).toHaveTextContent('Active ledger rows: 3')
  expect(line).toHaveTextContent('drifted from what its callers settled')
})

it('names each outstanding counter, corrupt rows included, and no counter at zero', async () => {
  api.admissionReconciliation.mockResolvedValue({
    ...quiet,
    corrupt: 2,
    unresolved: 1,
    legacy_pending: 1,
    undecodable: 3,
    legacy_owes_release: 4,
    drift: true,
  } satisfies AdmissionReconciliation)

  await renderBudgets(true)

  const line = await screen.findByTestId('admission-drift')
  expect(line).toHaveTextContent(
    'Admission rows failing their integrity check: 2',
  )
  expect(line).toHaveTextContent('Recovery writes still undecided: 1')
  expect(line).toHaveTextContent('Claims an earlier build left: 1')
  expect(line).toHaveTextContent('Rows whose owed list does not decode: 3')
  expect(line).toHaveTextContent('Releases an earlier build owes: 4')
  expect(line).not.toHaveTextContent('Holds still owed')
  expect(line).not.toHaveTextContent('Active ledger rows')
})

it('speaks when recovery left work the drift flag does not count', async () => {
  api.admissionReconciliation.mockResolvedValue({
    ...quiet,
    owed_remaining: 2,
    frontier_blocked: 1,
  } satisfies AdmissionReconciliation)

  await renderBudgets(true)

  const line = await screen.findByTestId('admission-drift')
  expect(line).toHaveTextContent('Holds still owed: 2')
  expect(line).toHaveTextContent('Holds the attempt lifecycle blocks: 1')
  expect(line).not.toHaveTextContent('drifted')
})

it('says nothing when there is nothing to say', async () => {
  api.admissionReconciliation.mockResolvedValue(quiet)

  await renderBudgets(true)

  await waitFor(() => expect(api.admissionReconciliation).toHaveBeenCalled())
  expect(screen.queryByTestId('admission-drift')).toBeNull()
})

it('is not reachable without the budget read its neighbors require', async () => {
  api.admissionReconciliation.mockResolvedValue({
    ...quiet,
    active: 3,
    corrupt: 1,
    drift: true,
  } satisfies AdmissionReconciliation)

  await renderBudgets(false)

  await waitFor(() =>
    expect(screen.queryByTestId('admission-drift')).toBeNull(),
  )
  expect(api.admissionReconciliation).not.toHaveBeenCalled()
})
