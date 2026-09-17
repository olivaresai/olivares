// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Handoff selection through the REAL routing boundary.
//
// The room reads its URL keys with `useUrlState`, which subscribes to the router's
// own location. Neither that hook nor the router is replaced here: the navigation
// this file claims to prove is performed by `router.history.back()` and
// `.forward()` on a real memory history, so a regression in the propagation path
// fails the test rather than the mock. Only the HTTP boundary is replaced.
import { act, render, screen, waitFor } from '@testing-library/react'
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from '@tanstack/react-router'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { queryKeys } from '@/lib/api/query'
import { useWorkspaceStore } from '@/stores/workspace'
import { useSessionStore } from '@/stores/session'

const auth = vi.hoisted(() => ({ perms: new Set<string>() }))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 't1',
    can: (p: string) => auth.perms.has(p),
    isSuperadmin: false,
    principal: {
      kind: 'user',
      user_id: '0192f2c0-eeee-7000-8000-00000000000a',
      actor: 'user:a',
      display_name: 'Ada',
      superadmin: false,
      grants: [],
    },
  }),
}))
vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
  Toaster: () => null,
}))
const api = vi.hoisted(() => ({
  listHandoffInbox: vi.fn(),
  getHandoffDetail: vi.fn(),
  listChannels: vi.fn(),
  listInbox: vi.fn(),
  listAdministrableChannels: vi.fn(),
}))
vi.mock('./api', async (orig) => {
  const real = (await orig()) as Record<string, unknown>
  return { ...real, ...api }
})
const caps = vi.hoisted(() => ({ state: null as unknown }))
vi.mock('@/lib/auth/capabilities', async (orig) => {
  const real = (await orig()) as Record<string, unknown>
  const harness = await import('./test-harness')
  caps.state = harness.capabilityState()
  return {
    ...real,
    ...harness.capabilityDoubles(
      caps.state as ReturnType<typeof capabilityState>,
    ),
  }
})

import { CommunicationsView } from './communications-view'
import {
  capabilityState,
  deferred,
  handoffDetailOf,
  HANDOFF_DELIVERY_ID,
  USER_A,
  WS,
  WS2,
} from './test-harness'
import './i18n'

const DR = 'sessions:delivery:read'
const SECOND_DELIVERY = '0192f2c0-cccc-7000-8000-0000000000ee'
const HANDOFFS = '/communications/handoffs'

/** The room mounted under a real router at a real location. */
function mountAt(initial: string) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  qc.setQueryData(queryKeys.whoami, {
    kind: 'user',
    user_id: USER_A,
    actor: `user:${USER_A}`,
    display_name: 'Ada',
    superadmin: true,
    grants: [],
  })
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const handoffsRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: HANDOFFS,
    validateSearch: (raw: Record<string, unknown>) => ({
      handoff: typeof raw.handoff === 'string' ? raw.handoff : undefined,
    }),
    component: () => <CommunicationsView entrance="handoffs" />,
  })
  const router = createRouter({
    routeTree: rootRoute.addChildren([handoffsRoute]),
    history: createMemoryHistory({ initialEntries: [initial] }),
  })
  render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
  return router
}

beforeEach(() => {
  vi.clearAllMocks()
  auth.perms = new Set([DR])
  Object.assign(caps.state as object, capabilityState(), { access: 'negative' })
  api.listHandoffInbox.mockResolvedValue({ items: [], has_more: false })
  api.listChannels.mockResolvedValue({ items: [], has_more: false })
  api.listInbox.mockResolvedValue({ items: [], has_more: false })
  api.listAdministrableChannels.mockResolvedValue({
    items: [],
    has_more: false,
  })
  useWorkspaceStore.setState({
    activeWorkspace: WS,
    activeWorkspaceName: 'Billing',
  })
  useSessionStore.setState({
    token: 'test-session',
    sessionId: 'sid',
    expiresAt: '2030-01-01T00:00:00Z',
  })
})

describe('handoff selection through the real router', () => {
  it('back and forward between two Deliveries each start their own fresh detail read', async () => {
    api.getHandoffDetail.mockImplementation(async (deliveryId: string) =>
      handoffDetailOf({
        deliveryId,
        summary:
          deliveryId === HANDOFF_DELIVERY_ID ? 'First body' : 'Second body',
      }),
    )
    const router = mountAt(`${HANDOFFS}?handoff=${HANDOFF_DELIVERY_ID}`)
    expect(await screen.findByText('First body')).toBeVisible()
    expect(api.getHandoffDetail).toHaveBeenCalledTimes(1)

    // A real navigation to the second Delivery, pushed onto the same history the
    // router subscribes to.
    await act(async () => {
      router.history.push(`${HANDOFFS}?handoff=${SECOND_DELIVERY}`)
    })
    expect(await screen.findByText('Second body')).toBeVisible()
    await waitFor(() => expect(api.getHandoffDetail).toHaveBeenCalledTimes(2))
    expect(api.getHandoffDetail.mock.calls[1][0]).toBe(SECOND_DELIVERY)
    // The previous protected body is gone, not layered underneath.
    expect(document.body.textContent).not.toContain('First body')

    // BACK through the router's own history.
    await act(async () => {
      router.history.back()
    })
    expect(await screen.findByText('First body')).toBeVisible()
    await waitFor(() => expect(api.getHandoffDetail).toHaveBeenCalledTimes(3))
    expect(api.getHandoffDetail.mock.calls[2][0]).toBe(HANDOFF_DELIVERY_ID)
    expect(document.body.textContent).not.toContain('Second body')

    // FORWARD again.
    await act(async () => {
      router.history.forward()
    })
    expect(await screen.findByText('Second body')).toBeVisible()
    await waitFor(() => expect(api.getHandoffDetail).toHaveBeenCalledTimes(4))
    expect(api.getHandoffDetail.mock.calls[3][0]).toBe(SECOND_DELIVERY)
    expect(document.body.textContent).not.toContain('First body')
  })

  it('a late response for the previous selection is rejected after a real back navigation', async () => {
    const slowSecond = deferred<ReturnType<typeof handoffDetailOf>>()
    api.getHandoffDetail
      .mockResolvedValueOnce(handoffDetailOf({ summary: 'First body' }))
      .mockReturnValueOnce(slowSecond.promise)
      .mockResolvedValueOnce(handoffDetailOf({ summary: 'First body again' }))
    const router = mountAt(`${HANDOFFS}?handoff=${HANDOFF_DELIVERY_ID}`)
    expect(await screen.findByText('First body')).toBeVisible()

    await act(async () => {
      router.history.push(`${HANDOFFS}?handoff=${SECOND_DELIVERY}`)
    })
    await waitFor(() => expect(api.getHandoffDetail).toHaveBeenCalledTimes(2))

    // Go back before the second read answers.
    await act(async () => {
      router.history.back()
    })
    expect(await screen.findByText('First body again')).toBeVisible()

    // The abandoned read answers last and must not paint.
    await act(async () => {
      slowSecond.resolve(handoffDetailOf({ summary: 'Stale second body' }))
      await Promise.resolve()
    })
    expect(document.body.textContent).toContain('First body again')
    expect(document.body.textContent).not.toContain('Stale second body')
  })

  it('navigating to a malformed handoff value selects nothing and reads nothing more', async () => {
    api.getHandoffDetail.mockResolvedValue(handoffDetailOf())
    const router = mountAt(`${HANDOFFS}?handoff=${HANDOFF_DELIVERY_ID}`)
    await waitFor(() => expect(api.getHandoffDetail).toHaveBeenCalledTimes(1))

    await act(async () => {
      router.history.push(`${HANDOFFS}?handoff=not-a-uuid`)
    })
    await waitFor(() =>
      expect(document.body.textContent).not.toContain(
        'Deploy freeze needs an owner',
      ),
    )
    expect(api.getHandoffDetail).toHaveBeenCalledTimes(1)
  })

  it('the actual scope boundary still rejects a late A→B→A read under real routing', async () => {
    const slowA = deferred<ReturnType<typeof handoffDetailOf>>()
    api.getHandoffDetail
      .mockReturnValueOnce(slowA.promise)
      .mockResolvedValueOnce(handoffDetailOf({ summary: 'B current' }))
      .mockResolvedValueOnce(handoffDetailOf({ summary: 'New A current' }))
    mountAt(`${HANDOFFS}?handoff=${HANDOFF_DELIVERY_ID}`)
    await waitFor(() => expect(api.getHandoffDetail).toHaveBeenCalledTimes(1))

    await act(async () => {
      useWorkspaceStore.setState({ activeWorkspace: WS2 })
    })
    expect(await screen.findByText('B current')).toBeVisible()
    await act(async () => {
      useWorkspaceStore.setState({ activeWorkspace: WS })
    })
    expect(await screen.findByText('New A current')).toBeVisible()

    await act(async () => {
      slowA.resolve(handoffDetailOf({ summary: 'Old A protected' }))
      await Promise.resolve()
    })
    expect(document.body.textContent).toContain('New A current')
    expect(document.body.textContent).not.toContain('Old A protected')
  })
})
