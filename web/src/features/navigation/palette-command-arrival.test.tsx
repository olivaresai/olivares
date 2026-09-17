// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE SEAM NOTHING COVERED: palette selection → router → the feature that is already on
// screen. Adapted from the independent review's reproducer for
// `palette-action-authority-20260911` (correction 1), with its defect assertions inverted
// into the required behaviour and its positive control kept as written.
//
// ⛔ WHAT THE REVIEW MEASURED ON 351d7bd, and what each cell now pins instead.
//    The consumer was a mount hook, so a verb selected while its feature was already shown
//    consumed nothing — navigating to the path you are on remounts nothing. The command
//    stayed in the store and opened the form on a LATER, unrelated visit; an observed
//    revocation did not retire it, so a regrant resurrected it; and orchestration's verb
//    landed on the Graph tab, whose sibling holds the form.
//
// Every cell drives the REAL palette, the REAL router and the REAL views. The stand-ins
// are the ones the other suites use: `useAuth` with a set-membership `can`, and the
// feature API seams.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import {
  Outlet,
  RouterProvider,
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
} from '@tanstack/react-router'
import { useEffect } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import '@/features/alerting/i18n'
import '@/features/orchestration/i18n'

const { toastMock } = vi.hoisted(() => ({
  toastMock: {
    success: vi.fn(),
    error: vi.fn(),
    warning: vi.fn(),
    info: vi.fn(),
  },
}))
vi.mock('@/components/ui/toaster', () => ({ toast: toastMock }))

const { notify, orch, recordingApiMock, searchMock, authState } = vi.hoisted(
  () => ({
    notify: {
      listRoutes: vi.fn(),
      getRoute: vi.fn(),
      createRoute: vi.fn(),
      updateRoute: vi.fn(),
      deleteRoute: vi.fn(),
      testRoute: vi.fn(),
      routeRevisions: vi.fn(),
      restoreRoute: vi.fn(),
      listDestinations: vi.fn(),
      listMatchTypes: vi.fn(),
      evaluateRoutes: vi.fn(),
      listDeliveries: vi.fn(),
      listOutbox: vi.fn(),
      redeliverOutbox: vi.fn(),
    },
    orch: {
      graph: vi.fn(),
      flows: vi.fn(),
      schedules: vi.fn(),
      scheduleDecisions: vi.fn(),
      decisions: vi.fn(),
      timeline: vi.fn(),
    },
    recordingApiMock: { notice: vi.fn(), acknowledge: vi.fn() },
    searchMock: vi.fn(),
    authState: {
      can: (_p: string): boolean => false,
      activeTenant: 't1' as string | null,
      logout: vi.fn(),
    },
  }),
)
vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))
vi.mock('@/features/alerting/api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/features/alerting/api')>()),
  notifyApi: notify,
}))
vi.mock('@/features/orchestration/api', async (importOriginal) => {
  const actual =
    await importOriginal<typeof import('@/features/orchestration/api')>()
  return {
    ...actual,
    orchestrationApi: { ...actual.orchestrationApi, ...orch },
  }
})
vi.mock('@/features/recordings/api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/features/recordings/api')>()),
  recordingApi: recordingApiMock,
}))
vi.mock('@/lib/api/search', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/lib/api/search')>()),
  searchConsole: (q: string) => searchMock(q),
}))

import '@/features/_intel'
import { CommandMenu } from '@/components/layout/command-menu'
import { TooltipProvider } from '@/components/ui/tooltip'
import { AlertingView } from '@/features/alerting/alerting-view'
import { OrchestrationView } from '@/features/orchestration/orchestration-view'
import { flowsFixture, graphFixture } from '@/features/orchestration/fixtures'
import { queryKeys } from '@/lib/api/query'
import { liveCapabilityContext } from '@/lib/auth/capabilities'
import { useCommandStore } from '@/stores/command'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import { useWorkspaceStore } from '@/stores/workspace'

const PRINCIPAL = {
  kind: 'user',
  user_id: 'u-1',
  actor: 'u-1',
  display_name: 'Ada',
  superadmin: false,
  grants: [] as unknown[],
}

const ALERTING = ['notify:route:read', 'notify:route:write']
const ORCHESTRATION = [
  'orchestration:graph:read',
  'orchestration:schedule:read',
  'orchestration:schedule:write',
]

/** A principal holding exactly `permissions`, read at render like the real projection. */
function holding(...permissions: string[]) {
  const held = new Set(permissions)
  authState.can = (p: string) => held.has(p)
}

let alertingMounts = 0
function AlertingRoute() {
  useEffect(() => {
    alertingMounts += 1
  }, [])
  return <AlertingView />
}

function mount(initial: string) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  qc.setQueryData(queryKeys.whoami, PRINCIPAL)
  const root = createRootRoute({
    component: () => (
      <>
        <CommandMenu />
        <Outlet />
      </>
    ),
  })
  const router = createRouter({
    routeTree: root.addChildren([
      createRoute({
        getParentRoute: () => root,
        path: '/alerting',
        component: AlertingRoute,
      }),
      createRoute({
        getParentRoute: () => root,
        path: '/orchestration',
        component: OrchestrationView,
      }),
      createRoute({
        getParentRoute: () => root,
        path: '/elsewhere',
        component: () => <p>elsewhere</p>,
      }),
    ]),
    history: createMemoryHistory({ initialEntries: [initial] }),
  })
  render(
    <QueryClientProvider client={qc}>
      <TooltipProvider delayDuration={0}>
        <RouterProvider router={router} />
      </TooltipProvider>
    </QueryClientProvider>,
  )
  return { qc, router }
}

/** Open ⌘K and select a verb by its visible label, exactly as an operator does. */
async function selectVerb(
  user: ReturnType<typeof userEvent.setup>,
  label: string,
) {
  act(() => useCommandStore.getState().setOpen(true))
  await user.type(await screen.findByRole('combobox'), label)
  await user.click(await screen.findByRole('option', { name: label }))
  await waitFor(() => expect(useCommandStore.getState().open).toBe(false))
  await act(async () => {
    await new Promise((r) => setTimeout(r, 150))
  })
}

async function goTo(
  router: ReturnType<typeof mount>['router'],
  to: '/alerting' | '/orchestration' | '/elsewhere',
) {
  await act(async () => {
    // `as never` for the same reason the palette itself uses it (command-menu.tsx): these
    // paths belong to this test's memory route tree, not to the app's registered one.
    await router.navigate({ to: to as never })
  })
}

/** Queue a command the way the palette's handler does, without its navigation. */
function queueWithoutNavigating(
  qc: QueryClient,
  feature: string,
  verb: string,
) {
  act(() => {
    useCommandStore
      .getState()
      .setPendingAction(feature, verb, liveCapabilityContext(qc))
  })
  expect(useCommandStore.getState().pendingAction).not.toBeNull()
}

const EMPTY = { items: [], has_more: false }

beforeEach(() => {
  vi.clearAllMocks()
  alertingMounts = 0
  authState.activeTenant = 't1'
  useTenantStore.setState({ activeTenant: 't1' })
  useSessionStore.setState({ credentialGeneration: 0 })
  useWorkspaceStore.setState({ activeWorkspace: null })
  useCommandStore.setState({ pendingAction: null, open: false, opener: null })
  searchMock.mockResolvedValue({ results: [] })
  notify.listRoutes.mockResolvedValue({
    items: [
      {
        id: 'r1',
        name: 'sec-alerts',
        destination: 'slack-sec',
        enabled: true,
        min_severity: 'high',
      },
    ],
    has_more: false,
  })
  notify.listDestinations.mockResolvedValue({
    destinations: ['slack-sec', 'pagerduty'],
  })
  notify.listMatchTypes.mockResolvedValue({ match_types: [] })
  notify.evaluateRoutes.mockResolvedValue({ items: [], matched_count: 0 })
  notify.listDeliveries.mockResolvedValue(EMPTY)
  notify.listOutbox.mockResolvedValue(EMPTY)
  notify.routeRevisions.mockResolvedValue(EMPTY)
  recordingApiMock.notice.mockResolvedValue({
    recorded_namespaces: [],
    consent_required: false,
  })
  orch.graph.mockResolvedValue(graphFixture)
  orch.flows.mockResolvedValue({ items: flowsFixture, has_more: false })
  orch.timeline.mockResolvedValue(EMPTY)
  orch.schedules.mockResolvedValue(EMPTY)
  orch.scheduleDecisions.mockResolvedValue(EMPTY)
  orch.decisions.mockResolvedValue(EMPTY)
})

describe('a palette verb opens its form on the arrival it was selected for', () => {
  // The review's CONTROL, unchanged: the ordering the old code did handle must keep working.
  it('CONTROL: from another route it navigates, mounts, consumes once and opens the form', async () => {
    holding(...ALERTING)
    const { router } = mount('/elsewhere')
    expect(await screen.findByText('elsewhere')).toBeInTheDocument()
    const user = userEvent.setup()
    await selectVerb(user, 'New alert route')
    expect(router.state.location.pathname).toBe('/alerting')
    expect(
      await screen.findByRole('dialog', { name: /new route/i }),
    ).toBeInTheDocument()
    expect(useCommandStore.getState().pendingAction).toBeNull()
  })

  it('same route: the already-mounted view opens the form on THIS arrival', async () => {
    holding(...ALERTING)
    const { router } = mount('/alerting')
    expect(await screen.findByText('sec-alerts')).toBeInTheDocument()
    expect(alertingMounts).toBe(1)

    const user = userEvent.setup()
    await selectVerb(user, 'New alert route')

    expect(router.state.location.pathname).toBe('/alerting')
    // No remount: this is the ordering the mount-only consumer could not see.
    expect(alertingMounts).toBe(1)
    expect(
      await screen.findByRole('dialog', { name: /new route/i }),
    ).toBeInTheDocument()
    expect(useCommandStore.getState().pendingAction).toBeNull()
  })

  it('same route, another tab showing: the verb selects the tab that holds its form', async () => {
    holding(...ALERTING)
    mount('/alerting')
    const user = userEvent.setup()
    await user.click(await screen.findByRole('tab', { name: /deliveries/i }))
    expect(
      await screen.findByRole('tab', { name: /deliveries/i }),
    ).toHaveAttribute('data-state', 'active')

    await selectVerb(user, 'New alert route')

    expect(
      await screen.findByRole('dialog', { name: /new route/i }),
    ).toBeInTheDocument()
    expect(useCommandStore.getState().pendingAction).toBeNull()
  })

  // The review's R1b ordering, inverted: selection on the page itself, then a revocation
  // the console observes, then the grant back. On 351d7bd the command was never consumed
  // at the selection, survived both, and opened the form on the next plain visit.
  it('one shot: a later visit opens nothing, and a revoke/regrant does not resurrect it', async () => {
    holding(...ALERTING)
    const { qc, router } = mount('/alerting')
    expect(await screen.findByText('sec-alerts')).toBeInTheDocument()
    const user = userEvent.setup()
    await selectVerb(user, 'New alert route')
    await screen.findByRole('dialog', { name: /new route/i })
    await user.keyboard('{Escape}')
    await waitFor(() =>
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument(),
    )

    holding('notify:route:read')
    act(() => {
      qc.setQueryData(queryKeys.whoami, { ...PRINCIPAL, grants: [{ n: 1 }] })
    })
    holding(...ALERTING)
    act(() => {
      qc.setQueryData(queryKeys.whoami, { ...PRINCIPAL, grants: [{ n: 2 }] })
    })

    await goTo(router, '/elsewhere')
    expect(await screen.findByText('elsewhere')).toBeInTheDocument()
    await goTo(router, '/alerting')
    expect(await screen.findByText('sec-alerts')).toBeInTheDocument()
    // FIRES IF: the command survives the arrival it was selected for — the form would
    // open for an operator who selected nothing on this visit.
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(alertingMounts).toBe(2)
  })

  // The other window, and the only one the fix leaves: queued while the feature root is
  // not mounted. The refusal must RETIRE the command where it is observed.
  it('a revocation observed before the arrival retires the command for good', async () => {
    holding(...ALERTING)
    const { qc, router } = mount('/elsewhere')
    expect(await screen.findByText('elsewhere')).toBeInTheDocument()
    // Queued while the feature is not on screen: the one window in which a command waits.
    queueWithoutNavigating(qc, 'alerting', 'createRoute')

    // The revocation reaches the console as a new whoami payload for the same principal —
    // which does NOT move the capability context, so only the authority check can catch it.
    holding('notify:route:read')
    act(() => {
      qc.setQueryData(queryKeys.whoami, { ...PRINCIPAL, grants: [{ n: 1 }] })
    })

    await goTo(router, '/alerting')
    expect(await screen.findByText('sec-alerts')).toBeInTheDocument()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    // RETIRED WHERE IT WAS REFUSED, not merely left unconsumed.
    expect(useCommandStore.getState().pendingAction).toBeNull()

    holding(...ALERTING)
    act(() => {
      qc.setQueryData(queryKeys.whoami, { ...PRINCIPAL, grants: [{ n: 2 }] })
    })
    await goTo(router, '/elsewhere')
    await goTo(router, '/alerting')
    expect(await screen.findByText('sec-alerts')).toBeInTheDocument()
    // FIRES IF: a refused command outlives the refusal — the grant coming back would open
    // a form nobody asked for on that visit.
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('a tenant round trip retires the command before any consumer sees it', async () => {
    holding(...ALERTING)
    const { qc, router } = mount('/elsewhere')
    expect(await screen.findByText('elsewhere')).toBeInTheDocument()
    queueWithoutNavigating(qc, 'alerting', 'createRoute')

    // A → B → A: every value matches again; only the movement counter does not.
    act(() => {
      useTenantStore.setState({ activeTenant: 't2' })
      useTenantStore.setState({ activeTenant: 't1' })
    })

    await goTo(router, '/alerting')
    expect(await screen.findByText('sec-alerts')).toBeInTheDocument()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(useCommandStore.getState().pendingAction).toBeNull()
  })

  it('orchestration: the verb selects the Schedules tab and opens the form there', async () => {
    holding(...ORCHESTRATION)
    const { router } = mount('/elsewhere')
    expect(await screen.findByText('elsewhere')).toBeInTheDocument()
    const user = userEvent.setup()

    await selectVerb(user, 'New schedule')

    expect(router.state.location.pathname).toBe('/orchestration')
    expect(
      await screen.findByRole('dialog', { name: /new schedule/i }),
    ).toBeInTheDocument()
    expect(useCommandStore.getState().pendingAction).toBeNull()
  })

  it('orchestration: ordinary navigation still lands on the default Graph tab', async () => {
    // CONTROL for the cell above: the tab moves for an explicit selection and for nothing
    // else, which is what "preserve the default tab" means.
    holding(...ORCHESTRATION)
    const { router } = mount('/elsewhere')
    expect(await screen.findByText('elsewhere')).toBeInTheDocument()
    await goTo(router, '/orchestration')
    expect(
      await screen.findByRole('tab', { name: 'Communication graph' }),
    ).toHaveAttribute('data-state', 'active')
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('orchestration: a reader of the graph alone is offered nothing and opens nothing', async () => {
    holding('orchestration:graph:read', 'orchestration:schedule:read')
    const { qc, router } = mount('/elsewhere')
    expect(await screen.findByText('elsewhere')).toBeInTheDocument()
    const user = userEvent.setup()
    act(() => useCommandStore.getState().setOpen(true))
    await user.type(await screen.findByRole('combobox'), 'New schedule')
    expect(
      screen.queryByRole('option', { name: 'New schedule' }),
    ).not.toBeInTheDocument()
    await user.keyboard('{Escape}')
    await waitFor(() => expect(useCommandStore.getState().open).toBe(false))

    // And a command forced into the store for that principal is refused and retired.
    queueWithoutNavigating(qc, 'orchestration', 'createSchedule')
    await goTo(router, '/orchestration')
    expect(
      await screen.findByRole('tab', { name: 'Communication graph' }),
    ).toHaveAttribute('data-state', 'active')
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(useCommandStore.getState().pendingAction).toBeNull()
  })
})
