// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Structure of the top bar (0.5, after and). jsdom does not lay out, so the pixel
// facts live in the browser captures; what is pinned here is what those pixels depend on: ONE
// 52 px ROW with the page title and its controls (search, notifications and the account; below
// 761 px the mark opens the bar and the phone bar's More opens the navigation) and no places, and
// the Tab order equal to the reading order.
// The version, Stop all and the stops live in the status bar (status-bar.test.tsx).
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import type { ComponentProps, ReactNode } from 'react'
import { createTestQueryClient, renderIntel } from '@/test/intel'

const routerState = vi.hoisted(() => ({
  pathname: '/console',
  search: {} as Record<string, unknown>,
}))
const identity = vi.hoisted(() => ({ version: '1.0' }))
const navigate = vi.hoisted(() => vi.fn())
// How many organizations the person belongs to and how many workspaces the list holds.
const scope = vi.hoisted(() => ({ orgs: 2, workspaces: 2 }))
const authz = vi.hoisted(() => ({
  denied: new Set<string>(),
  estateStopped: false,
  established: true,
}))
vi.mock('@/lib/hooks/use-server-info', () => ({
  useServerInfo: () => ({ data: { version: identity.version } }),
}))
vi.mock('@tanstack/react-router', () => ({
  useRouterState: ({
    select,
  }: {
    select: (s: {
      location: { pathname: string; search: Record<string, unknown> }
    }) => unknown
  }) =>
    select({
      location: { pathname: routerState.pathname, search: routerState.search },
    }),
  useNavigate: () => navigate,
  Link: ({
    children,
    to,
    ...props
  }: ComponentProps<'a'> & { to?: string; children?: ReactNode }) => (
    <a href={to} {...props}>
      {children}
    </a>
  ),
}))

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    status: 'authenticated',
    principal: {
      email: 'operator@example.invalid',
      display_name: 'Operator Example',
      superadmin: false,
    },
    grants: [
      { tenant: '01a0776d-4e2c-4c1b-9f3a-000000000001', role: 'owner' },
      { tenant: '7b3c9e10-1111-4c1b-9f3a-000000000002', role: 'viewer' },
    ].slice(0, scope.orgs),
    activeTenant: '01a0776d-4e2c-4c1b-9f3a-000000000001',
    activeRole: 'owner',
    isSuperadmin: false,
    isAuthenticated: true,
    can: (p: string) => !authz.denied.has(p),
    login: async () => {},
    logout: async () => {},
    setActiveTenant: () => {},
  }),
}))

// Two workspaces so the workspace switcher renders its trigger (one = no control).
vi.mock('@/features/console/api', () => ({
  consoleKeys: { workspaces: (...args: unknown[]) => ['workspaces', ...args] },
  consoleApi: {
    listWorkspaces: async () => ({
      items: [
        {
          id: 'w1',
          name: 'Billing operations',
          slug: 'billing',
          status: 'active',
          is_default: true,
        },
        {
          id: 'w2',
          name: 'Research',
          slug: 'research',
          status: 'active',
          is_default: false,
        },
      ].slice(0, scope.workspaces),
      has_more: false,
    }),
  },
}))

// The bell owns a query + live feed of its own; a stand-in with the same accessible
// name keeps this test about the bar's structure.
vi.mock('./notification-bell', () => ({
  NotificationBell: () => (
    <button type="button" aria-label="Notifications">
      bell
    </button>
  ),
}))

// The counts and the stop posture own live reads of their own; the bar only draws them.
vi.mock('./use-session-rail', () => ({
  // Working and Needs you are 5; with the idle ones, 7 sessions have not ended.
  useSessionRail: () => ({
    sessionCounts: { needsYou: 1, live: 5, running: 7, open: 7 },
  }),
}))
vi.mock('@/features/governance/use-pending-approvals', () => ({
  usePendingApprovals: () => ({ query: { data: undefined } }),
  pendingCount: () => 3,
}))
vi.mock('./use-killswitch-state', () => ({
  useKillSwitchState: () => ({
    permitted: true,
    posture: { estate: authz.estateStopped, active: 0, agentStops: 0 },
  }),
}))
// The identity the kill switch verb is queued under (the whoami reflection, in the app).
vi.mock('@/lib/auth/capabilities', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/lib/auth/capabilities')>()),
  liveCapabilityContext: () =>
    authz.established
      ? {
          principalKind: 'user',
          actor: 'user:operator',
          tenant: '01a0776d-4e2c-4c1b-9f3a-000000000001',
          credentialGeneration: 1,
          workspace: null,
          lifetime: 1,
        }
      : null,
}))

import { afterEach } from 'vitest'
import { consoleKeys } from '@/features/console/api'
import { useCommandStore } from '@/stores/command'
import { useWorkspaceStore } from '@/stores/workspace'
import { ScopeSwitcher } from './scope-switcher'
import { Topbar } from './topbar'

afterEach(() => {
  identity.version = '1.0'
  authz.denied.clear()
  authz.estateStopped = false
  authz.established = true
  navigate.mockReset()
  useCommandStore.setState({ pendingAction: null })
  routerState.pathname = '/console'
  routerState.search = {}
  useWorkspaceStore.setState({
    activeWorkspace: null,
    activeWorkspaceName: null,
  })
})

describe('the top bar holds the title and four controls, nothing else (0.5)', () => {
  it('is one 52 px row: the page title, search, notifications and the account', async () => {
    const { container } = renderIntel(<Topbar />)
    const header = container.querySelector('header') as HTMLElement
    expect(header.getAttribute('data-slot')).toBe('topbar')
    expect(header.className).toContain('h-[52px]')
    expect(header.className).toContain('flex-nowrap')
    // No places: the left navigation is the only one.
    expect(
      within(header).getByRole('navigation', { name: 'People' }),
    ).toBeInTheDocument()
    expect(
      within(header).queryByRole('button', { name: /, Account$/ }),
    ).toBeNull()
    // No shortcut legend: the keys live in the command palette and the `?` keymap.
    expect(header.querySelector('[data-testid="hint-bar"]')).toBeNull()
    expect(header).not.toHaveTextContent(/approve|deny|launcher/)
    expect(
      header.querySelector('[data-slot="topbar-title"]'),
    ).toHaveTextContent('People')
    // The title is the bar's one bold line: 15 px, 17 px heavier on a phone.
    expect(header.querySelector('[data-slot="topbar-title"]')).toHaveClass(
      'text-body-l',
      'font-bold',
      '[&_h1]:text-body-l',
      '[&_h1]:font-bold',
      'max-[760px]:text-heading',
      'max-[760px]:font-extrabold',
    )
    await waitFor(() =>
      expect(within(header).getAllByRole('button')).toHaveLength(2),
    )
    const [search, bell] = within(header).getAllByRole('button')
    expect(search).toHaveAccessibleName('Search and commands')
    expect(bell).toHaveAccessibleName('Notifications')
  })

  it("on a phone the place's pages take a second row, never the gap beside search", () => {
    // At 390 px the row squeezed into the 20 px left between the title and the controls:
    // its current member ran under the search button, cut to «Se». Below 761 px the bar
    // wraps (title and controls, then the pages on a full-width scrolling row).
    const { container } = renderIntel(<Topbar />)
    const header = container.querySelector('header') as HTMLElement
    expect(header.className).toContain('max-[760px]:flex-wrap')
    expect(header.className).toContain('max-[760px]:h-auto')
    const pages = within(header).getByRole('navigation', { name: 'People' })
    expect(pages.className).toContain('max-[760px]:order-last')
    expect(pages.className).toContain('max-[760px]:basis-full')
  })

  it('opens with the mark on a phone, a decoration and no menu button: the phone bar opens the navigation', () => {
    const { container } = renderIntel(<Topbar />)
    const header = container.querySelector('header') as HTMLElement
    const mark = header.querySelector('svg[viewBox="3.5 2 28 28"]')
    expect(mark).toHaveAttribute('aria-hidden', 'true')
    expect(mark).toHaveClass('size-6', 'min-[761px]:hidden')
    expect(mark?.closest('a, button')).toBeNull()
    expect(
      within(header).queryByRole('button', { name: 'Open navigation' }),
    ).toBeNull()
  })

  it('names the page the address is: a place of its own by its place name', () => {
    routerState.pathname = '/permissions'
    routerState.search = { tab: 'approvals' }
    const { container } = renderIntel(<Topbar />)
    expect(
      container.querySelector('[data-slot="topbar-title"]'),
    ).toHaveTextContent('Rules')
  })

  it('moves the version, theme, favorites, the mark and Stop all out of the bar', () => {
    const { container } = renderIntel(<Topbar />)
    const header = container.querySelector('header') as HTMLElement
    expect(within(header).queryByTestId('deployment-identity')).toBeNull()
    expect(within(header).queryByText('1.0', { exact: true })).toBeNull()
    for (const name of [
      'Olivares AI, home',
      'Toggle theme',
      'Stop all',
      /favorite/i,
      /sidebar/i,
    ])
      expect(
        within(header).queryByRole('button', { name }) ??
          within(header).queryByRole('link', { name }),
        String(name),
      ).toBeNull()
    expect(
      within(header).queryByRole('navigation', { name: 'Breadcrumb' }),
    ).toBeNull()
    expect(header.querySelector('[data-slot="scope-switcher"]')).toBeNull()
  })

  it('names search by its visible word, and folds it to its icon below 1101 px', () => {
    const { container } = renderIntel(<Topbar />)
    const search = within(container).getByRole('button', {
      name: 'Search and commands',
    })
    expect(search).toHaveTextContent(/^Search/)
    expect(search).toHaveAttribute('aria-keyshortcuts', 'Control+K Meta+K')
    expect(search.className).toContain('max-[1100px]:w-9')
  })

  it("Tab order is the reading order: the place's pages, search, notifications, account", async () => {
    const user = userEvent.setup()
    const { container } = renderIntel(<Topbar />)
    const header = container.querySelector('header') as HTMLElement
    await waitFor(() =>
      expect(within(header).getAllByRole('button')).toHaveLength(2),
    )
    // The place's pages follow the title, before the controls, as A2 draws it.
    const nav = within(header).getByRole('navigation', { name: 'People' })
    const pages = within(nav)
      .getAllByRole('link')
      .map((link) => link.textContent ?? '')
    expect(pages.length).toBeGreaterThan(1)
    const order: string[] = []
    await user.tab()
    while (header.contains(document.activeElement)) {
      const el = document.activeElement as HTMLElement
      order.push(el.getAttribute('aria-label') ?? el.textContent ?? '')
      await user.tab()
    }
    expect(order.slice(0, pages.length)).toEqual(pages)
    expect(order[pages.length]).toBe('Search and commands')
    expect(order.at(-1)).toBe('Notifications')
    expect(order.some((label) => /, Account$/.test(label))).toBe(false)
  })
})

// The scope moved from the bar to the left navigation (sidebar.tsx) with the same control.
describe('the scope control', () => {
  afterEach(() => Object.assign(scope, { orgs: 2, workspaces: 2 }))

  it('draws nothing with one organization and one workspace, and the control once a second workspace is made', async () => {
    Object.assign(scope, { orgs: 1, workspaces: 1 })
    useWorkspaceStore.setState({
      activeWorkspace: null,
      activeWorkspaceName: null,
    })
    const queryClient = createTestQueryClient()
    const { container } = renderIntel(<ScopeSwitcher />, { queryClient })
    await waitFor(() =>
      expect(
        queryClient
          .getQueryCache()
          .find({ queryKey: ['workspaces'], exact: false })?.state.status,
      ).toBe('success'),
    )
    // One choice is no choice: the slot stays empty.
    expect(container).toBeEmptyDOMElement()

    // A second workspace, invalidated the way the create dialog does it (scopes-tab.tsx).
    scope.workspaces = 2
    await queryClient.invalidateQueries({
      queryKey: consoleKeys.workspaces('01a0776d-4e2c-4c1b-9f3a-000000000001'),
    })
    expect(await within(container).findByRole('button')).toHaveAttribute(
      'title',
      expect.stringContaining('All workspaces'),
    )
  })

  it('names the scope once: the workspace in force, or the organization when none is', async () => {
    useWorkspaceStore.setState({
      activeWorkspace: 'w2',
      activeWorkspaceName: 'Research',
    })
    const user = userEvent.setup()
    const { container } = renderIntel(<ScopeSwitcher />)
    const scope = container.querySelector(
      '[data-slot="scope-switcher"]',
    ) as HTMLElement
    await within(scope).findByText('Research')
    // The text it shows is inside the name a speech user says (WCAG 2.5.3): the
    // initial is drawn, not text.
    expect(scope.textContent).toBe('Research')
    // The organization is named in the menu and the tooltip, not a second time here.
    expect(scope.textContent).not.toContain('01a0776d')
    expect(scope).toHaveAttribute('title', expect.stringContaining('01a0776d'))
    // One control, both levels: the organizations and the workspaces in one menu.
    await user.click(scope)
    expect(await screen.findByText('Switch organization')).toBeVisible()
    expect(screen.getByText('Switch workspace')).toBeVisible()
    expect(
      screen.getByRole('menuitem', { name: /Billing operations/ }),
    ).toBeVisible()
    expect(
      screen.getByRole('menuitem', { name: /All workspaces/ }),
    ).toBeVisible()
  })

  it('says a workspace in force that the list does not resolve, never the organization in its place', async () => {
    useWorkspaceStore.setState({
      activeWorkspace: 'gone',
      activeWorkspaceName: 'Archived',
    })
    const { container } = renderIntel(<ScopeSwitcher />)
    const scope = container.querySelector(
      '[data-slot="scope-switcher"]',
    ) as HTMLElement
    await within(scope).findByText('Workspace not in list')
    expect(scope).toHaveAccessibleName(/^Workspace not in list, /)
  })
})

describe('the bar speaks the platform and names its icons', () => {
  /** The platform the browser reports, as `navigator.platform` answers it. */
  const onPlatform = (platform: string) =>
    Object.defineProperty(window.navigator, 'platform', {
      value: platform,
      configurable: true,
    })
  afterEach(() => {
    delete (window.navigator as { platform?: string }).platform
  })

  it.each([
    ['Linux x86_64', 'Ctrl+K'],
    ['Win32', 'Ctrl+K'],
    ['MacIntel', '⌘K'],
  ])('on %s the search hint reads %s', (platform, hint) => {
    onPlatform(platform)
    const { container } = renderIntel(<Topbar />)
    const search = within(container).getByRole('button', {
      name: 'Search and commands',
    })
    // The hint is drawn, not text: what the button says is its label, inside its
    // name (WCAG 2.5.3); the chord itself is aria-keyshortcuts.
    expect(search.querySelector('kbd')).toHaveAttribute('data-keys', hint)
    expect(search.textContent).toBe('Search')
  })

  it('gives every control that shows only an icon a tooltip with its name', async () => {
    const { container } = renderIntel(<Topbar />)
    const header = container.querySelector('header') as HTMLElement
    /** The text a sighted person reads on the control: screen-reader-only text aside. */
    const visibleText = (el: Element) => {
      const copy = el.cloneNode(true) as Element
      copy
        .querySelectorAll('.sr-only, [aria-hidden="true"]')
        .forEach((n) => n.remove())
      return (copy.textContent ?? '').trim()
    }
    const silent = [...header.querySelectorAll('a, button')]
      .filter((el) => visibleText(el) === '')
      .filter((el) => !el.getAttribute('title'))
      .map((el) => el.getAttribute('aria-label') ?? el.outerHTML.slice(0, 80))
    expect(silent).toEqual([])
    // The controls whose visible text is not their name say it on hover too.
    const search = within(header).getByRole('button', {
      name: 'Search and commands',
    })
    expect(search).toHaveAttribute('title', 'Search and commands')
    // The phone's search is a 44 px target.
    expect(search).toHaveClass('max-[760px]:size-11')
    expect(
      within(header).queryByRole('button', { name: /, Account$/ }),
    ).toBeNull()
  })
})
