// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import {
  focusManager,
  QueryClient,
  QueryClientProvider,
} from '@tanstack/react-query'
import { act, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { useClientSettings } from '@/features/settings/preferences'
import { useModulesStore } from '@/stores/modules'
import { useSessionStore } from '@/stores/session'
import { useWorkspaceStore } from '@/stores/workspace'
import { setPageTitle } from './document-title'
import { SessionNotifications } from './session-notifications'
import { useSessionRail } from './use-session-rail'
import { toast } from 'sonner'

const auth = vi.hoisted(() => ({
  tenant: 'tenant',
  search: {} as Record<string, string>,
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: auth.tenant,
    principal: { user_id: 'person', superadmin: false },
    can: () => true,
  }),
}))
vi.mock('@tanstack/react-router', async (original) => ({
  ...(await original<typeof import('@tanstack/react-router')>()),
  useRouterState: ({ select }: { select: (state: unknown) => unknown }) =>
    select({ location: { pathname: '/sessions', search: auth.search } }),
}))

let client: QueryClient
let hidden: boolean
let state: string
let activity: string | undefined
let removed: boolean
let calls: string[]
let failedSource: string | null
let extraRuns: Record<string, unknown>[]
let marks: Record<string, unknown>
const notify = vi.fn()
const closed = vi.fn()
const run = {
  run_ref: 'run-one',
  name: 'Review changes',
  transport: 'stream-json',
  created_at: '2026-10-14T06:00:00Z',
}

beforeEach(() => {
  auth.tenant = 'tenant'
  auth.search = {}
  hidden = true
  state = 'running'
  activity = undefined
  removed = false
  calls = []
  extraRuns = []
  marks = {}
  failedSource = null
  setPageTitle('Olivares')
  useModulesStore.setState({ off: new Set() })
  useWorkspaceStore.setState({ activeWorkspace: null })
  useClientSettings.getState().restore()
  useClientSettings.setState({
    sessionNotificationsFor: JSON.stringify(['person', 'tenant']),
  })
  notify.mockReset()
  closed.mockReset()
  vi.stubGlobal(
    'Notification',
    class {
      static permission = 'granted'
      constructor(...args: unknown[]) {
        notify(...args)
      }
      close() {
        closed()
      }
    },
  )
  vi.spyOn(document, 'hidden', 'get').mockImplementation(() => hidden)
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const path = new URL(String(input), 'http://localhost').pathname
      calls.push(path)
      if (path === failedSource)
        return Response.json({ error: 'Unavailable' }, { status: 503 })
      if (path === '/v1/m/sessions/runs')
        return Response.json({
          items: removed
            ? []
            : [
                { ...run, state, last_activity_at: activity, ...marks },
                ...extraRuns,
              ],
          has_more: false,
        })
      if (path === '/v1/m/sessions/live')
        return Response.json({ items: [], has_more: false })
      throw new Error(`Unexpected read: ${path}`)
    }),
  )
  client = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  })
})
afterEach(() => {
  client.clear()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  vi.useRealTimers()
  focusManager.setFocused(undefined)
})

/** Wait for the rail's rendered, complete baseline, not only the fetch promise. */
function RailProbe() {
  const rail = useSessionRail()
  return (
    <output data-testid="rail-probe">
      {rail.attentionReady
        ? rail.attention.map((s) => s.state).join(',')
        : 'loading'}
    </output>
  )
}
function Harness() {
  return (
    <QueryClientProvider client={client}>
      <RailProbe />
      <SessionNotifications />
    </QueryClientProvider>
  )
}

async function mount() {
  const view = render(<Harness />)
  await waitFor(() => expect(client.isFetching()).toBe(0))
  expect(calls).toContain('/v1/m/sessions/runs')
  await waitFor(() =>
    expect(screen.getByTestId('rail-probe')).toHaveTextContent('working'),
  )
  return view
}
async function refresh() {
  await act(async () => {
    await client.invalidateQueries()
  })
  await waitFor(() =>
    expect(screen.getByTestId('rail-probe')).toHaveTextContent(
      failedSource
        ? 'loading'
        : removed
          ? /^$/
          : state === 'waiting_approval'
            ? 'needs-you'
            : 'working',
    ),
  )
}

it('notifies exactly once in a hidden tab and counts the session that needs you', async () => {
  const view = await mount()
  expect(notify).not.toHaveBeenCalled()
  state = 'waiting_approval'
  await refresh()
  expect(notify).toHaveBeenCalledExactlyOnceWith(
    'Review changes',
    expect.objectContaining({
      body: 'This session needs you.',
      silent: true,
    }),
  )
  expect(document.title).toBe('(1) Olivares')
  await refresh()
  expect(notify).toHaveBeenCalledTimes(1)
  hidden = false
  auth.search = { session: 'run:run-one' }
  act(() => document.dispatchEvent(new Event('visibilitychange')))
  expect(document.title).toBe('Olivares')
  view.unmount()
  expect(document.title).toBe('Olivares')
})

async function turnEndsWith(lastActivity: string, probe: string) {
  await mount()
  state = 'idle'
  activity = lastActivity
  await act(async () => {
    await client.invalidateQueries()
  })
  await waitFor(() =>
    expect(screen.getByTestId('rail-probe')).toHaveTextContent(probe),
  )
}

it('notifies once when a turn ends in a hidden tab', async () => {
  await turnEndsWith(new Date(Date.now() - 20_000).toISOString(), 'finished')
  expect(notify).toHaveBeenCalledExactlyOnceWith(
    'Review changes',
    expect.objectContaining({ body: 'The turn finished.', silent: true }),
  )
  expect(document.title).toBe('Olivares')
})

it('keeps finished notifications off when the user disabled that event', async () => {
  useClientSettings.setState({
    notificationEvents: { needsYou: true, finished: false, failed: true },
  })
  await turnEndsWith(new Date(Date.now() - 20_000).toISOString(), 'finished')
  expect(notify).not.toHaveBeenCalled()
})

it('stays quiet for a run that went idle from silence', async () => {
  await turnEndsWith(new Date(Date.now() - 6 * 60_000).toISOString(), 'quiet')
  expect(notify).not.toHaveBeenCalled()
})

it('does not deliver in a visible tab or count its on-screen session', async () => {
  hidden = false
  auth.search = { session: 'run:run-one' }
  await mount()
  state = 'waiting_approval'
  await refresh()
  expect(notify).not.toHaveBeenCalled()
  expect(document.title).toBe('Olivares')
})

it('is off by default and restores the title when disabled or removed', async () => {
  await mount()
  state = 'waiting_approval'
  await refresh()
  removed = true
  await refresh()
  expect(document.title).toBe('Olivares')
  act(() => useClientSettings.getState().restore())
  removed = false
  await refresh()
  expect(notify).toHaveBeenCalledTimes(1)
  expect(document.title).toBe('Olivares')
})

it('drops old attention on tenant changes', async () => {
  const view = await mount()
  state = 'waiting_approval'
  await refresh()
  auth.tenant = 'other'
  view.rerender(<Harness />)
  await waitFor(() => expect(client.isFetching()).toBe(0))
  expect(notify).toHaveBeenCalledTimes(1)
  expect(document.title).toBe('Olivares')
})

it('suppresses desktop delivery for a visible tab showing another screen', async () => {
  hidden = false
  await mount()
  state = 'waiting_approval'
  await refresh()
  expect(notify).not.toHaveBeenCalled()
  expect(document.title).toBe('(1) Olivares')
})

it('counts a selected session whose conversation is hidden by the phone rail pane', async () => {
  hidden = false
  auth.search = { session: 'run:run-one', pane: 'rail' }
  await mount()
  state = 'waiting_approval'
  await refresh()
  expect(document.title).toBe('(1) Olivares')
})

it('keeps a clean title across credential renewal and disablement', async () => {
  await mount()
  state = 'waiting_approval'
  await refresh()
  expect(document.title).toBe('(1) Olivares')
  act(() =>
    useSessionStore.setState((s) => ({
      credentialGeneration: s.credentialGeneration + 1,
    })),
  )
  await waitFor(() => expect(client.isFetching()).toBe(0))
  await waitFor(() => expect(document.title).toBe('(1) Olivares'))
  act(() => useClientSettings.getState().restore())
  expect(document.title).toBe('Olivares')
})

it.each(['/v1/m/sessions/runs', '/v1/m/sessions/live'])(
  'preserves attention transitions across a failed %s read',
  async (source) => {
    await mount()
    failedSource = source
    state = 'waiting_approval'
    await refresh()
    expect(notify).not.toHaveBeenCalled()
    failedSource = null
    await refresh()
    expect(notify).toHaveBeenCalledTimes(1)
    expect(document.title).toBe('(1) Olivares')
  },
)

it('polls a hidden tab automatically while the setting is enabled', async () => {
  vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
  await mount()
  focusManager.setFocused(false)
  state = 'waiting_approval'
  await act(async () => {
    await vi.advanceTimersByTimeAsync(5_000)
  })
  await waitFor(() => expect(notify).toHaveBeenCalledTimes(1))
})

it('counts sessions beyond the sidebar row cap', async () => {
  extraRuns = Array.from({ length: 11 }, (_, i) => ({
    ...run,
    run_ref: `extra-${i}`,
    state: 'waiting_approval',
  }))
  await mount()
  state = 'waiting_approval'
  await refresh()
  expect(document.title).toBe('(12) Olivares')
})

it('keeps the title usable and explains a browser delivery failure once', async () => {
  vi.stubGlobal(
    'Notification',
    class {
      static permission = 'granted'
      constructor() {
        throw new Error('Desktop delivery unavailable')
      }
    },
  )
  const message = vi.spyOn(toast, 'message').mockReturnValue(0)
  await mount()
  state = 'waiting_approval'
  await refresh()
  expect(document.title).toBe('(1) Olivares')
  await refresh()
  // The tab is hidden: nobody can read a toast now, so it waits for the person.
  expect(message).not.toHaveBeenCalled()
  hidden = false
  act(() => document.dispatchEvent(new Event('visibilitychange')))
  expect(message).toHaveBeenCalledExactlyOnceWith(
    'Browser notifications are unavailable here.',
  )
  hidden = true
  act(() => document.dispatchEvent(new Event('visibilitychange')))
  state = 'running'
  await refresh()
  state = 'waiting_approval'
  await refresh()
  hidden = false
  act(() => document.dispatchEvent(new Event('visibilitychange')))
  expect(message).toHaveBeenCalledTimes(1)
})

it('closes the needs-you notification when the session no longer needs the person', async () => {
  await mount()
  state = 'waiting_approval'
  await refresh()
  expect(notify).toHaveBeenCalledTimes(1)
  expect(closed).not.toHaveBeenCalled()
  state = 'running'
  await refresh()
  expect(closed).toHaveBeenCalledTimes(1)
  expect(document.title).toBe('Olivares')
})

it.each([
  { mark: 'snoozed', marks: { snoozed_until: '2030-01-02T09:00:00Z' } },
  { mark: 'done', marks: { done: true } },
])(
  'closes the needs-you notification when the person marks the session $mark (#2480)',
  async ({ marks: set }) => {
    await mount()
    state = 'waiting_approval'
    await refresh()
    expect(notify).toHaveBeenCalledTimes(1)
    expect(document.title).toBe('(1) Olivares')
    marks = set
    await act(async () => {
      await client.invalidateQueries()
    })
    await waitFor(() =>
      expect(screen.getByTestId('rail-probe')).toHaveTextContent(/^$/),
    )
    expect(closed).toHaveBeenCalledTimes(1)
    expect(document.title).toBe('Olivares')
    expect(notify).toHaveBeenCalledTimes(1)
  },
)

it('does not notify the same session twice inside five seconds', async () => {
  await mount()
  state = 'waiting_approval'
  await refresh()
  state = 'running'
  await refresh()
  state = 'waiting_approval'
  await refresh()
  expect(notify).toHaveBeenCalledTimes(1)
  expect(document.title).toBe('(1) Olivares')
})

it.each(['denied', 'default'])(
  'counts the session but shows no notification when permission is %s',
  async (permission) => {
    vi.stubGlobal(
      'Notification',
      Object.assign(vi.fn(), { permission, requestPermission: vi.fn() }),
    )
    const message = vi.spyOn(toast, 'message').mockReturnValue(0)
    await mount()
    state = 'waiting_approval'
    await refresh()
    expect(Notification).not.toHaveBeenCalled()
    expect(message).not.toHaveBeenCalled()
    expect(document.title).toBe('(1) Olivares')
  },
)
