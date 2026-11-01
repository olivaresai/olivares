// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The status bar (#3161): one thin row under the work that says how things stand. The
// engine with a dot and a word, the sessions open (not yet ended), each tool's usage windows when the
// tool reported them, agent stops only while one is active, and the version the release
// gate reads at every width. Stop all leaves the permanent chrome: it shows here only while
// a session runs, as a quiet control whose confirmation says how many sessions stop.
import { act, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ComponentProps } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createTestQueryClient, renderIntel, screen } from '@/test/intel'

const state = vi.hoisted(() => ({
  version: '1.0' as string | undefined,
  engineError: false,
  usagePercent: 58,
  refreshing: false,
  live: 0,
  /** What the list shows; null: the same sessions as `live`. */
  open: null as number | null,
  estate: false,
  agentStops: 0,
  denied: new Set<string>(),
  superadmin: false,
}))
const engage = vi.hoisted(() => vi.fn())
const providersRead = vi.hoisted(() => vi.fn())

vi.mock('@/lib/hooks/use-server-info', () => ({
  useServerInfo: () => ({
    data: state.engineError ? undefined : { version: state.version },
    isError: state.engineError,
  }),
}))
vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => vi.fn(),
  useRouterState: ({ select }: { select: (s: unknown) => unknown }) =>
    select({ location: { pathname: '/console', search: {} } }),
  Link: ({ children, to, ...props }: ComponentProps<'a'> & { to?: string }) => (
    <a href={to} {...props}>
      {children}
    </a>
  ),
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    status: 'authenticated',
    principal: {
      actor: 'user:operator',
      superadmin: state.superadmin,
      grants: [{ tenant: 't1', role: 'owner' }],
    },
    grants: [{ tenant: 't1', role: 'owner' }],
    activeTenant: 't1',
    activeRole: 'owner',
    isSuperadmin: state.superadmin,
    isAuthenticated: true,
    can: (p: string) => !state.denied.has(p),
    logout: async () => {},
    setActiveTenant: () => {},
  }),
}))
vi.mock('./use-session-rail', () => ({
  useSessionRail: () => ({
    sessionCounts: {
      needsYou: 0,
      live: state.live,
      running: state.live,
      open: state.open ?? state.live,
    },
  }),
}))
vi.mock('./use-killswitch-state', () => ({
  useKillSwitchState: () => ({
    permitted: true,
    posture: {
      estate: state.estate,
      active: state.agentStops + (state.estate ? 1 : 0),
      agentStops: state.agentStops,
    },
  }),
}))
vi.mock('@/features/killswitch/api', () => ({
  killswitchKeys: {
    all: (t: string | null) => ['killswitch', t],
    state: (t: string | null) => ['killswitch', t, 'state'],
  },
  killswitchApi: { engage },
}))
vi.mock('@/features/console/api', () => ({
  consoleKeys: { members: (t: string | null) => ['members', t] },
  consoleApi: { listMembers: async () => ({ items: [], has_more: false }) },
}))
vi.mock('@/features/agent-tools/api', () => ({
  agentToolsKeys: { all: ['agent-tools'] },
  agentToolsApi: {
    providers: async (...args: unknown[]) => {
      providersRead(...args)
      return {
        refreshing: state.refreshing,
        providers: [
          {
            instance: 'claude-default',
            driver: 'claude',
            default: true,
            state: 'ready',
            limits: [
              { label: '5-hour', percent: state.usagePercent },
              { label: 'Weekly', percent: 41 },
              { label: 'Weekly (Opus)', percent: 12 },
            ],
          },
          {
            instance: 'codex-default',
            driver: 'codex',
            default: true,
            state: 'not_signed_in',
            limits: [],
          },
        ],
      }
    },
  },
}))

import { toast } from '@/components/ui/toaster'
import engineTools from '@/features/agentops/engine-tools.fixture.json'
import { http } from '@/lib/api'
import { ApiError } from '@/lib/api/errors'
import { useTenantStore } from '@/stores/tenant'
import { StatusBar } from './status-bar'

// The engine's tool table, as the shell reads it (GET /v1/m/sessions/tools).
const realGet = http.get.bind(http)
const toolsRead = vi.hoisted(() => ({
  answer: null as null | (() => Promise<unknown>),
}))
beforeEach(() => {
  toolsRead.answer = () => Promise.resolve(engineTools)
  vi.spyOn(http, 'get').mockImplementation(((
    path: string,
    ...rest: unknown[]
  ) =>
    path === '/v1/m/sessions/tools'
      ? toolsRead.answer!()
      : (realGet as (...a: unknown[]) => unknown)(path, ...rest)) as never)
})

afterEach(() => {
  Object.assign(state, {
    version: '1.0',
    engineError: false,
    usagePercent: 58,
    refreshing: false,
    live: 0,
    open: null,
    estate: false,
    agentStops: 0,
    superadmin: false,
  })
  state.denied.clear()
  engage.mockReset()
  providersRead.mockReset()
  useTenantStore.setState({ activeTenant: null })
  vi.restoreAllMocks()
})

const bar = () => screen.getByRole('contentinfo', { name: 'Status' })

describe('the status bar says how things stand', () => {
  it('paints the server version at every width, and never an invented one', () => {
    const { rerender } = renderIntel(<StatusBar />)
    const id = () => within(bar()).getByTestId('deployment-identity')
    expect(within(id()).getByText('1.0', { exact: true })).toBeVisible()
    // No width class hides it: the release gate reads it on desktop and phone alike.
    expect(id().className).not.toMatch(/hidden/)
    state.version = '26.10.1'
    rerender(<StatusBar />)
    expect(within(id()).getByText('26.10.1', { exact: true })).toBeVisible()
    state.version = ''
    rerender(<StatusBar />)
    expect(within(bar()).queryByTestId('deployment-identity')).toBeNull()
  })

  it('names the engine state with a dot and a word', () => {
    const { rerender } = renderIntel(<StatusBar />)
    expect(within(bar()).getByRole('status')).toHaveTextContent('Engine ready')
    state.engineError = true
    rerender(<StatusBar />)
    expect(within(bar()).getByRole('status')).toHaveTextContent(
      'Engine not reachable',
    )
  })

  it('counts the open sessions, and says nothing of them when none is open', () => {
    const { rerender } = renderIntel(<StatusBar />)
    expect(within(bar()).queryByText(/open/)).toBeNull()
    state.live = 3
    rerender(<StatusBar />)
    // Open, not running: the count includes idle sessions, which are not working.
    expect(within(bar()).getByText('3 sessions open')).toBeVisible()
    expect(bar()).not.toHaveTextContent(/running/)
  })

  it('counts the sessions the list shows, while Stop all still stops an archived one that runs', () => {
    // One session runs and the person archived it: the list and the total leave it out,
    // and Stop all, which would end it, stays.
    state.live = 1
    state.open = 0
    renderIntel(<StatusBar />)
    expect(within(bar()).queryByText(/open/)).toBeNull()
    expect(
      within(bar()).getByRole('button', { name: 'Stop all' }),
    ).toBeVisible()
  })

  it('shows agent stops only while there are any, and an estate stop above all', () => {
    const { rerender } = renderIntel(<StatusBar />)
    expect(within(bar()).queryByTestId('killswitch-status')).toBeNull()
    state.agentStops = 2
    rerender(<StatusBar />)
    expect(within(bar()).getByTestId('killswitch-status')).toHaveTextContent(
      '2 agent stops active',
    )
    state.estate = true
    rerender(<StatusBar />)
    expect(within(bar()).getByTestId('killswitch-status')).toHaveTextContent(
      'ALL AGENTS STOPPED',
    )
  })

  it("shows each tool's 5-hour and weekly use as the tool reported it, and no guess", async () => {
    state.superadmin = true
    useTenantStore.setState({ activeTenant: 't1' })
    renderIntel(<StatusBar />)
    const usage = await within(bar()).findByRole('group', {
      name: 'Claude Code usage',
    })
    expect(usage).toHaveTextContent('58% 5h')
    expect(usage).toHaveTextContent('41% wk')
    // A model-scoped window is the tool's detail, not the bar's.
    expect(usage).not.toHaveTextContent('12%')
    // A tool that reported nothing has no usage: never 0%.
    expect(
      within(bar()).queryByRole('group', { name: 'Codex usage' }),
    ).toBeNull()
  })
})

describe('the status bar asks nothing tenant-scoped before an organization is chosen', () => {
  it("reads the tools' usage only once an organization is active", async () => {
    state.superadmin = true
    const { rerender } = renderIntel(<StatusBar />)
    // A first-run estate with no organization: the engine would answer "tenant required".
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(providersRead).not.toHaveBeenCalled()
    useTenantStore.setState({ activeTenant: 't1' })
    rerender(<StatusBar />)
    await vi.waitFor(() => expect(providersRead).toHaveBeenCalled())
  })
})

it('refreshes usage every minute while visible and stops polling after unmount', async () => {
  vi.useFakeTimers()
  const visible = vi
    .spyOn(document, 'visibilityState', 'get')
    .mockReturnValue('visible')
  try {
    state.superadmin = true
    useTenantStore.setState({ activeTenant: 't1' })
    const { unmount } = renderIntel(<StatusBar />)
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1)
    })
    expect(providersRead).toHaveBeenCalledTimes(1)
    state.usagePercent = 73
    await act(async () => {
      await vi.advanceTimersByTimeAsync(60_000)
    })
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1)
    })
    expect(
      within(bar()).getByRole('group', { name: 'Claude Code usage' }),
    ).toHaveTextContent('73% 5h')
    expect(providersRead).toHaveBeenCalledTimes(2)
    expect(providersRead).toHaveBeenLastCalledWith(
      't1',
      expect.any(AbortSignal),
      true,
    )
    visible.mockReturnValue('hidden')
    await act(async () => {
      await vi.advanceTimersByTimeAsync(60_000)
    })
    expect(providersRead).toHaveBeenCalledTimes(2)
    visible.mockReturnValue('visible')
    await act(async () => {
      await vi.advanceTimersByTimeAsync(60_000)
    })
    expect(providersRead).toHaveBeenCalledTimes(3)
    unmount()
    await vi.advanceTimersByTimeAsync(60_000)
    expect(providersRead).toHaveBeenCalledTimes(3)
  } finally {
    visible.mockRestore()
    vi.useRealTimers()
  }
})

it('polls pending usage after ten seconds, then returns to the minute interval', async () => {
  vi.useFakeTimers()
  try {
    state.superadmin = true
    state.refreshing = true
    useTenantStore.setState({ activeTenant: 't1' })
    renderIntel(<StatusBar />)
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1)
    })
    expect(providersRead).toHaveBeenCalledTimes(1)
    state.refreshing = false
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_000)
    })
    expect(providersRead).toHaveBeenCalledTimes(2)
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_000)
    })
    expect(providersRead).toHaveBeenCalledTimes(2)
    await act(async () => {
      await vi.advanceTimersByTimeAsync(50_000)
    })
    expect(providersRead).toHaveBeenCalledTimes(3)
  } finally {
    vi.useRealTimers()
  }
})

describe('Stop all lives in the status bar while a session runs', () => {
  it('is absent while no session runs', () => {
    renderIntel(<StatusBar />)
    expect(within(bar()).queryByRole('button', { name: 'Stop all' })).toBeNull()
  })

  it('is present while one runs, as a quiet control', () => {
    state.live = 1
    renderIntel(<StatusBar />)
    const stop = within(bar()).getByRole('button', { name: 'Stop all' })
    // No fill and no outline in the danger or accent colour: red only on hover.
    for (const loud of ['bg-bad', 'bg-danger', 'bg-accent', 'border-bad'])
      expect(stop.classList.contains(loud), loud).toBe(false)
  })

  it('opens a confirmation that says how many sessions stop, and stops on confirm', async () => {
    state.live = 3
    engage.mockResolvedValue({ id: 'ks-1', revoked_approvals: 3 })
    const done = vi.spyOn(toast, 'success')
    const queryClient = createTestQueryClient()
    const invalidate = vi.spyOn(queryClient, 'invalidateQueries')
    const user = userEvent.setup()
    renderIntel(<StatusBar />, { queryClient })
    await user.click(within(bar()).getByRole('button', { name: 'Stop all' }))
    const sheet = await screen.findByRole('dialog', {
      name: 'Stop all agents?',
    })
    expect(sheet).toHaveTextContent('3 open sessions stop.')
    // What the stop cancels, and what it keeps, said before it happens (#3207).
    expect(sheet).toHaveTextContent(
      'the pending approval requests of those sessions and of agent actions are canceled',
    )
    expect(sheet).toHaveTextContent(
      'requests a person filed outside a session stay pending',
    )
    expect(engage).not.toHaveBeenCalled()
    await user.click(
      within(sheet).getByRole('button', { name: 'Stop all agents' }),
    )
    expect(engage).toHaveBeenCalledWith(
      expect.objectContaining({ scope_kind: 'estate' }),
    )
    // The stop answers how many requests it canceled, and the console says it and drops
    // them at once: Now and Rules read the approvals again instead of 30 s later (#3207).
    await vi.waitFor(() =>
      expect(done).toHaveBeenCalledWith(
        'Kill switch engaged',
        expect.objectContaining({
          description: expect.stringContaining(
            '3 pending approval requests canceled',
          ),
        }),
      ),
    )
    expect(invalidate).toHaveBeenCalledWith({
      queryKey: ['governance', 't1', 'approvals'],
    })
  })

  it('is not offered to a principal who may not engage the stop', () => {
    state.live = 1
    state.denied.add('governance:killswitch:admin')
    renderIntel(<StatusBar />)
    expect(within(bar()).queryByRole('button', { name: 'Stop all' })).toBeNull()
  })

  it('is not offered while every agent is already stopped', () => {
    state.live = 1
    state.estate = true
    renderIntel(<StatusBar />)
    expect(within(bar()).queryByRole('button', { name: 'Stop all' })).toBeNull()
  })
})

describe('the status bar says when the AI tools could not be read', () => {
  it('names the failure with the engine reason, and Retry reads them again', async () => {
    toolsRead.answer = () =>
      Promise.reject(new ApiError(503, 'unavailable', 'The engine is busy.'))
    const user = userEvent.setup()
    renderIntel(<StatusBar />)
    const retry = await within(bar()).findByRole('button', {
      name: 'AI tools not read · Retry',
    })
    expect(retry).toHaveAttribute('title', 'The engine is busy.')
    toolsRead.answer = () => Promise.resolve(engineTools)
    await user.click(retry)
    await vi.waitFor(() =>
      expect(
        within(bar()).queryByRole('button', {
          name: 'AI tools not read · Retry',
        }),
      ).toBeNull(),
    )
  })

  it('says nothing while the tools were read', async () => {
    renderIntel(<StatusBar />)
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(within(bar()).queryByText(/AI tools not read/)).toBeNull()
  })
})
