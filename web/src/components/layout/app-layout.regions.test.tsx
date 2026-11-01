// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE SHELL'S REGIONS: one header row · the work · one status row, and NOTHING ELSE BELOW
// THE WORK. The left navigation sits beside the sheet (0.5); the top bar holds no places. The work sits
// in the sheet's body, a ROW that may also hold the side panel a page declares — beside the
// work, never below it. The status bar (#3161) is the sheet's last row: 24 px that say how
// things stand, under the work and the panel alike, and the only row allowed there.
//
// ⛔ THIS FILE EXISTS BECAUSE ITS ABSENCE WAS MEASURED. An independent review of this
//    lane ran the obvious mutation — a fixed 90 px bar reinstated after `</main>` in
//    app-layout.tsx — and the whole `layout/` suite plus the home suite stayed green:
//    13 files, 128 tests, none of which could see a second region appear under the work.
//    The lane's own design claimed an oracle for it. The claim was the defect: the
//    shell's most expensive decision — that the viewport IS the work — was resting on
//    nobody re-adding ninety pixels.
//
// So the assertions below are shaped by that mutation rather than by the source: they
// are about WHAT IS UNDER `main` and HOW MANY HEADER ROWS there are, which is what an
// action bar changes, and not about the classes the shell happens to carry today.
//
// The children are stood in for on purpose. The bar, the navigation, the palette and the
// tenant gate each own a suite that pins THEIR structure (topbar.layout.test.tsx,
// sidebar.test.tsx, command-menu.test.tsx, tenant-gate.test.tsx); mounting the
// real ones here would make this file fail for their reasons and say nothing new about
// the regions.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { fireEvent, render, screen } from '@testing-library/react'
import type { ComponentProps, ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const auth = vi.hoisted(() => ({
  status: 'authenticated',
  activeTenant: null as string | null,
  grants: [] as { tenant: string }[],
}))
// The router's LIVE location and its last COMMITTED one. A pending redirect moves
// the first; the second decides where sign-in returns to, and the first only before the
// router has committed any location.
const router = vi.hoisted(() => ({
  live: '/audit?from=2026-09-30#entry-7',
  // undefined until the router commits its first location.
  committed: '/audit?from=2026-09-30#entry-7' as string | undefined,
  navigate: vi.fn(),
}))
const routerInstance = vi.hoisted(() => ({
  get state() {
    return {
      location: { href: router.live },
      resolvedLocation:
        router.committed === undefined ? undefined : { href: router.committed },
    }
  },
}))
const server = vi.hoisted(() => ({ setupRequired: false, pending: false }))
// The app root provides one QueryClient and the shell reads it (NewSessionDialog's
// useKeyBeingStored), so the layout renders under a fresh one per test.
let client: QueryClient
function Providers({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>
}
const renderLayout = () => render(<AppLayout />, { wrapper: Providers })
beforeEach(() => {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  auth.status = 'authenticated'
  auth.activeTenant = null
  auth.grants = []
  router.live = router.committed = '/audit?from=2026-09-30#entry-7'
  router.navigate.mockReset()
  server.setupRequired = false
  server.pending = false
})

vi.mock('@tanstack/react-router', () => ({
  useRouterState: ({
    select,
  }: {
    select: (s: {
      location: { pathname: string; href: string }
      resolvedLocation: { href: string } | undefined
    }) => unknown
  }) =>
    select({
      location: { pathname: router.live.split(/[?#]/)[0], href: router.live },
      resolvedLocation:
        router.committed === undefined ? undefined : { href: router.committed },
    }),
  useNavigate: () => router.navigate,
  // The guard reads the requested page from the router's state in its effect. One
  // router instance, as the real hook returns.
  useRouter: () => routerInstance,
  Outlet: () => <div data-testid="routed-content">routed content</div>,
  Link: ({ children, to, ...props }: ComponentProps<'a'> & { to?: string }) => (
    <a href={to} {...props}>
      {children}
    </a>
  ),
}))

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => auth,
}))
vi.mock('@/lib/hooks/use-server-info', () => ({
  useServerInfo: () =>
    server.pending
      ? { isPending: true, data: undefined }
      : { isPending: false, data: { setup_required: server.setupRequired } },
}))

vi.mock('./sidebar', () => ({
  Sidebar: () => <nav data-slot="sidebar" />,
  NavigationDrawer: ({ open }: { open: boolean }) => (
    <div data-testid="drawer" data-open={String(open)} />
  ),
}))
vi.mock('./topbar', () => ({
  Topbar: ({ onMenu }: { onMenu: () => void }) => (
    <header data-slot="topbar">
      <button type="button" onClick={onMenu}>
        Open navigation
      </button>
    </header>
  ),
}))
vi.mock('./status-bar', () => ({
  StatusBar: () => <footer data-slot="status-bar" />,
}))
vi.mock('./command-menu', () => ({
  CommandMenu: () => <div data-testid="palette" />,
}))
vi.mock('./shortcuts', () => ({ GlobalShortcuts: () => null }))
// Session notifications are their own unit (session-notifications.test.tsx).
vi.mock('./session-notifications', () => ({ SessionNotifications: () => null }))
// The row of a place's pages is its own unit (shell-current.router.test.tsx).
vi.mock('./destination-sections', () => ({ DestinationSections: () => null }))
vi.mock('./tenant-gate', () => ({
  TenantGate: ({ children }: { children: ReactNode }) => <>{children}</>,
}))
vi.mock('@/features/navigation/permitted-visit', () => ({
  SettingsVisit: () => null,
}))
vi.mock('@/features/navigation/personal-navigation', () => ({
  usePersonalNavigation: () => null,
  PersonalNavigationProvider: ({ children }: { children: ReactNode }) => (
    <>{children}</>
  ),
}))

import engineTools from '@/features/agentops/engine-tools.fixture.json'
import { toolName } from '@/features/agentops/engine-tools'
import { http } from '@/lib/api'
import { ApiError } from '@/lib/api/errors'
import { AppLayout } from './app-layout'

const work = () => document.querySelector('main#main-content') as HTMLElement

describe('the shell has three regions and nothing else', () => {
  it('paints NOTHING under the work region', () => {
    renderLayout()
    const main = work()
    expect(main).not.toBeNull()

    // ⛔ THE ASSERTION THE MUTATION FAILS. An action bar is, structurally, an element
    //    after `main` in the column that holds it — that is what the 90 px bar was for
    //    77 routes, and what a "quick launcher" would be again. `main` is `flex-1`, so
    //    anything after it takes height from the work and gives it to chrome.
    expect(main.parentElement?.lastElementChild).toBe(main)

    // And the other shape the same region takes: anchored to the viewport instead of
    // stacked. Nothing in the shell may be `fixed` to an edge — a floating bar occludes
    // the last row of every table and form, which is the defect this pass was given.
    const shell = main.closest('[data-slot="app-frame"]') as HTMLElement
    const anchored = [...shell.querySelectorAll('[class*="fixed"]')].filter(
      (el) => /\bfixed\b/.test(el.className),
    )
    expect(anchored.map((el) => el.className)).toEqual([])
  })

  it('has ONE header row, and it is the only thing above the work', () => {
    renderLayout()
    const main = work()
    const body = main.parentElement as HTMLElement
    const sheet = body.parentElement as HTMLElement
    expect(document.querySelectorAll('[data-slot="topbar"]')).toHaveLength(1)
    // The sheet is exactly: the bar, the body, the status bar. Another row — the context
    // row an earlier pass removed, a notice strip, a tab bar hoisted out of a route, an
    // action bar under the work — would add a fourth child here and is what this refuses.
    expect(sheet.getAttribute('data-slot')).toBe('sheet')
    expect(
      [...sheet.children].map((el) => el.getAttribute('data-slot')),
    ).toEqual(['topbar', 'sheet-body', 'status-bar'])
    expect(sheet.firstElementChild?.getAttribute('data-slot')).toBe('topbar')
    expect(body.previousElementSibling).toBe(sheet.firstElementChild)
    // And the body opens with the work: nothing is stacked above it inside the sheet.
    expect(body.firstElementChild).toBe(main)
  })

  it('mounts no composer of its own: starting work belongs to the routes and the palette', () => {
    renderLayout()
    // The requirement did not go away — the palette carries `Start a session` and the
    // two screens where starting work IS the work mount `WorkComposer` inside their own
    // work region. What the SHELL may not do is spend every route's pixels on it.
    expect(document.querySelector('[data-testid="work-composer"]')).toBeNull()
    expect(screen.getByTestId('routed-content')).toBeInTheDocument()
  })

  it('gives the work region the geometry that lets a route own the viewport', () => {
    renderLayout()
    const main = work()
    // `min-h-0` is the load-bearing one: without it a flex child refuses to shrink
    // below its content, so a route that scrolls inside itself grows the shell instead
    // and the page gets a second scrollbar. `overflow-hidden` is what makes a pane that
    // forgets its own `min-h-0` fail loudly rather than quietly.
    expect(main).toHaveClass(
      'flex',
      'min-h-0',
      'flex-1',
      'flex-col',
      'overflow-hidden',
    )
    // No padding and no max width: the frame is the ROUTE's choice (page-frames.tsx),
    // and a shell that imposed one made every route a document.
    expect(main.className).not.toMatch(/\b(p|px|py|max-w)-/)
  })
})

it('carries the requested page to sign-in before mounting private content', () => {
  auth.status = 'anonymous'
  renderLayout()
  expect(router.navigate).toHaveBeenCalledTimes(1)
  expect(router.navigate).toHaveBeenCalledWith({
    to: '/login',
    search: { returnTo: '/audit?from=2026-09-30#entry-7' },
    replace: true,
  })
  expect(screen.queryByTestId('routed-content')).not.toBeInTheDocument()
})

// Refresh 02 froze every signed-out browser on its first page: the guard read
// the LIVE location, its own pending redirect moved it, and each re-render redirected
// again with a longer returnTo. It now navigates once per signed-out state.
it('redirects once, whatever the pending navigation does to the live location', () => {
  auth.status = 'anonymous'
  const { rerender } = renderLayout()
  for (let i = 0; i < 5; i++) {
    router.live = `/login?returnTo=${encodeURIComponent(router.live)}`
    rerender(<AppLayout />)
  }
  expect(router.navigate).toHaveBeenCalledTimes(1)
})

// RC10 (SC on 6e97de81): a signed-out deep link reached /login with no returnTo and the
// person landed on Home after signing in. The signed-out answer came before the router
// committed its first location, and only the committed one was read.
it('returns a deep link to its page when the router has not committed a location yet', () => {
  auth.status = 'anonymous'
  router.committed = undefined
  router.live = '/settings?tab=appearance#appearance'
  renderLayout()
  expect(router.navigate).toHaveBeenCalledTimes(1)
  expect(router.navigate).toHaveBeenCalledWith({
    to: '/login',
    search: { returnTo: '/settings?tab=appearance#appearance' },
    replace: true,
  })
})

it('still redirects once when the live location moves before the first commit', () => {
  auth.status = 'anonymous'
  router.committed = undefined
  router.live = '/settings?tab=appearance#appearance'
  const { rerender } = renderLayout()
  for (let i = 0; i < 5; i++) {
    router.live = `/login?returnTo=${encodeURIComponent(router.live)}`
    rerender(<AppLayout />)
  }
  expect(router.navigate).toHaveBeenCalledTimes(1)
  expect(router.navigate).toHaveBeenCalledWith({
    to: '/login',
    search: { returnTo: '/settings?tab=appearance#appearance' },
    replace: true,
  })
})

it('sends a first boot straight to the setup wizard, once', () => {
  auth.status = 'anonymous'
  server.setupRequired = true
  const { rerender } = renderLayout()
  rerender(<AppLayout />)
  expect(router.navigate).toHaveBeenCalledTimes(1)
  expect(router.navigate).toHaveBeenCalledWith({ to: '/setup', replace: true })
  expect(screen.queryByTestId('routed-content')).not.toBeInTheDocument()
})

it('waits for the server to say whether setup is needed before deciding', () => {
  auth.status = 'anonymous'
  server.pending = true
  renderLayout()
  expect(router.navigate).not.toHaveBeenCalled()
})

it('never sends sign-in back to a sign-in page', () => {
  auth.status = 'anonymous'
  router.live = router.committed = '/login?returnTo=%2Faudit'
  renderLayout()
  expect(router.navigate).toHaveBeenCalledWith({
    to: '/login',
    search: {},
    replace: true,
  })
})

describe('one left navigation', () => {
  it('draws the sidebar beside the sheet and nothing else that navigates, on a document page or a work page', () => {
    for (const path of ['/audit', '/sessions']) {
      router.live = router.committed = path
      const { unmount } = renderLayout()
      const sidebar = document.querySelector('[data-slot="sidebar"]')
      expect(sidebar, path).not.toBeNull()
      expect(sidebar?.nextElementSibling?.getAttribute('data-slot'), path).toBe(
        'sheet',
      )
      expect(screen.queryByTestId('phone-bar'), path).toBeNull()
      expect(screen.queryByTestId('areas-sheet'), path).toBeNull()
      unmount()
    }
  })

  it("opens the drawer from the top bar's menu button", () => {
    // Below 761 px: the column is hidden and the drawer is the navigation.
    vi.stubGlobal(
      'matchMedia',
      (query: string) =>
        ({
          matches: false,
          media: query,
          addEventListener: () => {},
          removeEventListener: () => {},
        }) as unknown as MediaQueryList,
    )
    try {
      renderLayout()
      expect(screen.getByTestId('drawer')).toHaveAttribute('data-open', 'false')
      fireEvent.click(screen.getByRole('button', { name: 'Open navigation' }))
      expect(screen.getByTestId('drawer')).toHaveAttribute('data-open', 'true')
    } finally {
      vi.unstubAllGlobals()
    }
  })
})

// The engine's tool table (GET /v1/m/sessions/tools) is read before the shell paints:
// every tool name and list under it answers from that read (engine-tools.ts).
describe("the shell waits for the engine's tool table", () => {
  const NEW_TOOL = {
    key: 'hermes',
    name: 'Hermes Agent',
    name_stem: 'hermes',
    session: true,
    bindings: [],
  }
  const asksForTools = (get: { mock: { calls: unknown[][] } }) =>
    get.mock.calls.some((call) => call[0] === '/v1/m/sessions/tools')

  it('paints once the engine has answered, and names tools from that answer', async () => {
    auth.activeTenant = 'tnt-a'
    auth.grants = [{ tenant: 'tnt-a' }]
    let answer!: (value: unknown) => void
    const get = vi
      .spyOn(http, 'get')
      .mockImplementation(
        (path: string) =>
          new Promise((resolve) =>
            path === '/v1/m/sessions/tools' ? (answer = resolve) : undefined,
          ) as never,
      )
    renderLayout()
    expect(work()).toBeNull()
    expect(asksForTools(get)).toBe(true)
    answer({ tools: [...engineTools.tools, NEW_TOOL] })
    expect(await screen.findByTestId('routed-content')).toBeInTheDocument()
    expect(toolName('hermes')).toBe('Hermes Agent')
    get.mockRestore()
  })

  it('paints when the read is refused, for the status bar to say so', async () => {
    auth.activeTenant = 'tnt-a'
    auth.grants = [{ tenant: 'tnt-a' }]
    const get = vi
      .spyOn(http, 'get')
      .mockRejectedValue(
        new ApiError(403, 'forbidden', 'Not allowed.', undefined),
      )
    renderLayout()
    expect(await screen.findByTestId('routed-content')).toBeInTheDocument()
    get.mockRestore()
  })

  it('waits while its one organization is being chosen', () => {
    auth.grants = [{ tenant: 'tnt-a' }]
    const get = vi.spyOn(http, 'get')
    renderLayout()
    expect(work()).toBeNull()
    expect(asksForTools(get)).toBe(false)
    get.mockRestore()
  })

  it('asks nothing before an organization is picked among several', () => {
    auth.grants = [{ tenant: 'tnt-a' }, { tenant: 'tnt-b' }]
    const get = vi.spyOn(http, 'get')
    renderLayout()
    expect(work()).not.toBeNull()
    expect(asksForTools(get)).toBe(false)
    get.mockRestore()
  })
})
