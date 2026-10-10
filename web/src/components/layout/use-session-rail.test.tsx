// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  act,
  render,
  renderHook,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import type { ComponentProps, ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useWorkspaceStore } from '@/stores/workspace'
import { useModulesStore } from '@/stores/modules'

const auth = vi.hoisted(() => ({
  permissions: new Set<string>(),
  search: {} as Record<string, string>,
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 'rail-tenant',
    principal: { user_id: 'rail-user', superadmin: false },
    isSuperadmin: false,
    can: (permission: string) => auth.permissions.has(permission),
  }),
}))
vi.mock('@tanstack/react-router', async (original) => ({
  ...(await original<typeof import('@tanstack/react-router')>()),
  useRouterState: ({ select }: { select: (state: unknown) => unknown }) =>
    select({ location: { pathname: '/agentops', search: auth.search } }),
  Link: ({
    children,
    to,
    search,
    ...props
  }: ComponentProps<'a'> & {
    to: string
    search?: Record<string, string>
  }) => (
    <a href={search ? `${to}?${new URLSearchParams(search)}` : to} {...props}>
      {children}
    </a>
  ),
}))

import { useSessionRail, type SessionRailData } from './use-session-rail'

const RUN = {
  run_ref: 'rail-run',
  tenant_id: 'rail-tenant',
  state: 'running',
  name: 'Operated rail session',
  created_at: '2026-10-03T08:00:00Z',
  started_at: '2026-10-03T08:00:00Z',
}
const LIVE = {
  session_ref: 'rail-live',
  live_ref: 'rail-live-ref',
  attribution: 'legacy',
  cc_state: 'active',
  goal: 'Observed rail session',
  first_event_at: '2026-10-03T08:00:00Z',
  last_event_at: '2026-10-03T08:00:00Z',
}

let client: QueryClient
let requests: string[]
let pendingApproval: string | undefined
let replies: Map<string, () => Response | Promise<Response>>
const RUNS = '/v1/m/sessions/runs'
const OBSERVED = '/v1/m/sessions/live'
const HANDOFFS = '/v1/m/sessions/inbox/handoffs'

const HANDOFF = {
  carrier: {
    channel_id: 'rail-channel',
    message_id: 'rail-message',
    delivery_id: 'rail-delivery',
    delivery_version: 1,
  },
  deadline_elapsed: false,
  handoff: {
    id: 'rail-handoff',
    state: 'offered',
    created_at: '2026-10-03T08:00:00Z',
    from: { kind: 'agent', ref: 'rail-agent' },
    to: { kind: 'user', ref: 'user:rail-user' },
  },
  work_item: { id: 'rail-work-item' },
}

function failed(status = 500) {
  return Response.json({ error: 'rail read unavailable' }, { status })
}
function deferredResponse() {
  let finish: (response: Response) => void = () => {
    throw new Error('response gate not initialized')
  }
  const response = new Promise<Response>((resolve) => {
    finish = resolve
  })
  return { response, finish }
}
function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>
}
function mount() {
  return renderHook(() => useSessionRail(), { wrapper })
}
/** The rows the rail holds, each with the group it sits in. */
function rowsOf(rail: SessionRailData) {
  return rail.groups.flatMap((group) =>
    group.rows.map((row) => ({ group: group.id, ...row })),
  )
}
function titlesOf(rail: SessionRailData) {
  return rowsOf(rail).map((row) => row.title)
}

beforeEach(() => {
  requests = []
  auth.permissions = new Set()
  auth.search = {}
  pendingApproval = undefined
  replies = new Map([
    [
      RUNS,
      () =>
        Response.json({
          items: [{ ...RUN, pending_approval_ref: pendingApproval }],
          has_more: false,
        }),
    ],
    [OBSERVED, () => Response.json({ items: [LIVE], has_more: false })],
    [HANDOFFS, () => Response.json({ items: [HANDOFF], has_more: false })],
  ])
  useWorkspaceStore.setState({ activeWorkspace: null })
  useModulesStore.setState({ off: new Set() })
  client = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  })
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const path = new URL(String(input), 'http://localhost').pathname
      requests.push(path)
      const reply = replies.get(path)
      if (reply) return reply()
      if (path === '/v1/auth/capabilities') return failed(503)
      throw new Error(`unexpected rail request: ${path}`)
    }),
  )
})

describe('session rail retrieval state', () => {
  it.each([
    { permission: 'sessions:run:read', path: RUNS },
    { permission: 'sessions:live:read', path: OBSERVED },
    { permission: 'sessions:delivery:read', path: HANDOFFS },
  ])(
    'reports an enabled source failure without claiming an empty queue ($permission)',
    async ({ permission, path }) => {
      auth.permissions.add(permission)
      if (path === HANDOFFS)
        useWorkspaceStore.setState({ activeWorkspace: 'rail-workspace' })
      replies.set(path, () => failed())
      const { result } = mount()
      await waitFor(() => expect(result.current.status.error).toBe(true))
      expect(result.current.status.loading).toBe(false)
      expect(result.current.visible).toBe(true)
      expect(rowsOf(result.current)).toEqual([])
      expect(requests.filter((p) => p.startsWith('/v1/m/sessions/'))).toEqual([
        path,
      ])
    },
  )

  it('offers a handoff-only user their inbox and readable All destination', async () => {
    auth.permissions.add('sessions:delivery:read')
    useWorkspaceStore.setState({ activeWorkspace: 'rail-workspace' })
    const { result } = mount()
    await waitFor(() => expect(rowsOf(result.current)).toHaveLength(1))
    expect(rowsOf(result.current)[0]).toMatchObject({
      group: 'needsYou',
      kind: 'handoff',
      state: 'need',
      from: 'rail-agent',
      href: '/communications/handoffs?handoff=rail-delivery',
    })
    expect(result.current.allTo).toBe('/communications/handoffs')
    expect(requests.filter((p) => p.startsWith('/v1/m/sessions/'))).toEqual([
      HANDOFFS,
    ])
  })

  it.each([
    { failedPath: OBSERVED, kept: 'Operated rail session' },
    { failedPath: RUNS, kept: 'Observed rail session' },
  ])(
    'keeps the other successful source visible alongside a read failure ($failedPath)',
    async ({ failedPath, kept }) => {
      auth.permissions = new Set(['sessions:live:read', 'sessions:run:read'])
      replies.set(failedPath, () => failed())
      const { result } = mount()
      await waitFor(() => {
        expect(result.current.status.error).toBe(true)
        expect(result.current.status.loading).toBe(false)
      })
      expect(titlesOf(result.current)).toEqual([kept])
    },
  )

  it('keeps observed sessions visible when the offered handoff inbox fails', async () => {
    auth.permissions = new Set(['sessions:live:read', 'sessions:delivery:read'])
    useWorkspaceStore.setState({ activeWorkspace: 'rail-workspace' })
    replies.set(HANDOFFS, () => failed())
    const { result } = mount()
    await waitFor(() => {
      expect(result.current.status.error).toBe(true)
      expect(result.current.status.loading).toBe(false)
    })
    expect(titlesOf(result.current)).toEqual(['Observed rail session'])
  })

  it('shows pending operated-run retrieval instead of an empty rail', async () => {
    auth.permissions.add('sessions:run:read')
    const gate = deferredResponse()
    replies.set(RUNS, () => gate.response)
    const { result } = mount()
    await waitFor(() => expect(result.current.status.loading).toBe(true))
    expect(result.current.visible).toBe(true)
    expect(result.current.status.error).toBe(false)
    expect(rowsOf(result.current)).toEqual([])
    await act(async () =>
      gate.finish(Response.json({ items: [RUN], has_more: false })),
    )
    await waitFor(() =>
      expect(titlesOf(result.current)).toEqual(['Operated rail session']),
    )
    expect(result.current.status.loading).toBe(false)
  })

  it('keeps successful operated runs visible while observed sessions are pending', async () => {
    auth.permissions = new Set(['sessions:live:read', 'sessions:run:read'])
    const gate = deferredResponse()
    replies.set(OBSERVED, () => gate.response)
    const { result } = mount()
    await waitFor(() =>
      expect(titlesOf(result.current)).toEqual(['Operated rail session']),
    )
    expect(result.current.status.loading).toBe(true)
    expect(result.current.status.error).toBe(false)
    await act(async () =>
      gate.finish(Response.json({ items: [LIVE], has_more: false })),
    )
    await waitFor(() =>
      expect(titlesOf(result.current).sort()).toEqual([
        'Observed rail session',
        'Operated rail session',
      ]),
    )
    expect(result.current.status.loading).toBe(false)
  })

  it('reports a failed source while another read is still pending', async () => {
    auth.permissions = new Set(['sessions:live:read', 'sessions:run:read'])
    const gate = deferredResponse()
    replies.set(OBSERVED, () => failed())
    replies.set(RUNS, () => gate.response)
    const { result } = mount()
    await waitFor(() => expect(result.current.status.error).toBe(true))
    expect(result.current.status.loading).toBe(true)
    await act(async () =>
      gate.finish(Response.json({ items: [RUN], has_more: false })),
    )
    await waitFor(() =>
      expect(titlesOf(result.current)).toEqual(['Operated rail session']),
    )
    expect(result.current.status).toEqual({ loading: false, error: true })
  })

  it('removes a cached source after a refused refresh and restores it only after a fresh successful read', async () => {
    auth.permissions = new Set(['sessions:live:read', 'sessions:run:read'])
    const { result } = mount()
    await waitFor(() => expect(titlesOf(result.current)).toHaveLength(2))
    replies.set(RUNS, () => failed(403))
    await act(async () => {
      await client.invalidateQueries({
        predicate: (query) => query.queryKey.includes('shell-rail'),
      })
    })
    await waitFor(() => expect(result.current.status.error).toBe(true))
    expect(titlesOf(result.current)).toEqual(['Observed rail session'])
    replies.set(RUNS, () => Response.json({ items: [RUN], has_more: false }))
    await act(async () => {
      await client.invalidateQueries({
        predicate: (query) => query.queryKey.includes('shell-rail'),
      })
    })
    await waitFor(() => expect(titlesOf(result.current)).toHaveLength(2))
    expect(result.current.status.error).toBe(false)
  })
})
afterEach(() => {
  client.clear()
  vi.unstubAllGlobals()
})

describe('session rail read authority', () => {
  it('offers no rail and makes no protected read without source authority', async () => {
    const { result } = mount()
    await waitFor(() => expect(client.isFetching()).toBe(0))
    expect(result.current.visible).toBe(false)
    expect(rowsOf(result.current)).toEqual([])
    expect(requests).toEqual([])
  })

  it('offers run-only users their operated session and an allowed All destination without asking for live sessions', async () => {
    auth.permissions.add('sessions:run:read')
    const { result } = mount()
    await waitFor(() => expect(rowsOf(result.current)).toHaveLength(1))
    expect(rowsOf(result.current)[0]).toMatchObject({
      group: 'working',
      title: 'Operated rail session',
      href: '/agentops?session=run%3Arail-run',
    })
    expect(result.current.allTo).toBe('/agentops')
    expect(result.current.sessionCounts).toEqual({ live: 1, needsYou: 0 })
    expect(requests).toEqual(['/v1/m/sessions/runs'])
  })

  it('offers live-only users their observed session without asking for operated runs', async () => {
    auth.permissions.add('sessions:live:read')
    const { result } = mount()
    await waitFor(() => expect(rowsOf(result.current)).toHaveLength(1))
    expect(rowsOf(result.current)[0]).toMatchObject({
      group: 'working',
      title: 'Observed rail session',
      href: '/sessions?session=sess%3Arail-live',
    })
    expect(result.current.allTo).toBe('/sessions')
    expect(requests).toEqual(['/v1/m/sessions/live'])
  })

  it('removes cached run rows on permission withdrawal and does not refetch a run opened afterward', async () => {
    auth.permissions = new Set(['sessions:live:read', 'sessions:run:read'])
    const { result, rerender } = mount()
    await waitFor(() => expect(titlesOf(result.current)).toHaveLength(2))
    expect(rowsOf(result.current).map((row) => row.group)).toEqual([
      'working',
      'working',
    ])
    const reads = requests.filter((p) => p.endsWith('/runs')).length
    act(() => {
      auth.permissions.delete('sessions:run:read')
      auth.search = { session: 'run:unreadable' }
    })
    rerender()
    // A refetch of the withdrawn source would be in flight here: let it settle first.
    await waitFor(() => expect(client.isFetching()).toBe(0))
    expect(titlesOf(result.current)).toEqual(['Observed rail session'])
    expect(requests.filter((p) => p.endsWith('/runs'))).toHaveLength(reads)
  })

  it.each([
    { permissions: [], href: '/agentops?session=run%3Arail-run' },
    {
      permissions: ['governance:identity:read'],
      href: '/agentops?session=run%3Arail-run',
    },
    {
      permissions: ['governance:approval:read'],
      href: '/agentops?session=run%3Arail-run',
    },
    {
      permissions: ['governance:identity:read', 'governance:approval:read'],
      href: '/permissions?tab=approvals&approval=approval-needed',
    },
  ])(
    'requires page and approval authority before opening a pending approval ($permissions)',
    async ({ permissions, href }) => {
      auth.permissions.add('sessions:run:read')
      for (const permission of permissions) auth.permissions.add(permission)
      pendingApproval = 'approval-needed'
      const { result } = mount()
      await waitFor(() => expect(rowsOf(result.current)).toHaveLength(1))
      expect(rowsOf(result.current)[0]).toMatchObject({
        group: 'needsYou',
        title: 'Operated rail session',
        href,
      })
      expect(result.current.sessionCounts).toEqual({ live: 1, needsYou: 1 })
      expect(requests).toEqual(['/v1/m/sessions/runs'])
    },
  )
})

// Keep the real sidebar + query hook + HTTP client together for the count oracle.
vi.mock('./tenant-switcher', () => ({ TenantSwitcher: () => null }))
vi.mock('./workspace-switcher', () => ({ WorkspaceSwitcher: () => null }))
vi.mock('./user-menu', () => ({ UserMenu: () => null }))
vi.mock('@/lib/hooks/use-server-info', () => ({
  useServerInfo: () => ({ data: undefined }),
}))
import { TooltipProvider } from '@/components/ui/tooltip'
import { AppSidebar } from './app-sidebar'
/** The Sessions destination among the pinned ones: the full sidebar also lists Sessions in
 * its areas, and these cases are about the pin's count and attention. */
function sessionsPin() {
  return within(screen.getByRole('navigation', { name: 'Journeys' })).getByRole(
    'link',
    { name: /Sessions/ },
  )
}
function counts(mode: 'full' | 'rail') {
  return render(
    <QueryClientProvider client={client}>
      <TooltipProvider>
        <AppSidebar
          mode={mode}
          onToggle={() => {}}
          areasOpen={false}
          onOpenAreas={() => {}}
        />
      </TooltipProvider>
    </QueryClientProvider>,
  )
}
it.each(['full', 'rail'] as const)(
  'counts sessions separately from offered handoffs in %s',
  async (mode) => {
    auth.permissions = new Set([
      'sessions:run:read',
      'sessions:live:read',
      'sessions:delivery:read',
    ])
    useWorkspaceStore.setState({ activeWorkspace: 'rail-workspace' })
    replies.set(OBSERVED, () => Response.json({ items: [], has_more: false }))
    counts(mode)
    await waitFor(() => expect(sessionsPin()).toHaveTextContent('1'))
    expect(
      sessionsPin().querySelector(
        mode === 'rail' ? '.bg-warning' : '.text-warning',
      ),
    ).toBeNull()
  },
)
it.each([
  ['full', RUNS],
  ['rail', RUNS],
  ['full', OBSERVED],
  ['rail', OBSERVED],
] as const)(
  'reads later pages in %s from %s, including attention beyond the first thirty',
  async (mode, path) => {
    auth.permissions.add(
      path === RUNS ? 'sessions:run:read' : 'sessions:live:read',
    )
    auth.permissions.add('sessions:live:read')
    const pages: string[] = []
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        const url = new URL(String(input), 'http://localhost')
        if (url.pathname !== path)
          return Response.json({ items: [], has_more: false })
        pages.push(url.searchParams.get('cursor') ?? '')
        const later = url.searchParams.has('cursor')
        const items = Array.from({ length: later ? 5 : 30 }, (_, i) =>
          path === RUNS
            ? {
                ...RUN,
                run_ref: `run-${later ? 30 + i : i}`,
                state: later && i === 4 ? 'waiting_approval' : 'running',
              }
            : {
                ...LIVE,
                session_ref: `live-${later ? 30 + i : i}`,
                live_ref: `ref-${later ? 30 + i : i}`,
                cc_state: later && i === 4 ? 'silent_evasion' : 'active',
              },
        )
        return Response.json({
          items,
          has_more: !later,
          cursor: later ? undefined : 'next-page',
        })
      }),
    )
    counts(mode)
    await waitFor(() => expect(sessionsPin()).toHaveTextContent('35'))
    expect(
      sessionsPin().querySelector(
        mode === 'rail' ? '.bg-warning' : '.text-warning',
      ),
    ).not.toBeNull()
    expect(pages).toEqual(['', 'next-page'])
  },
)
