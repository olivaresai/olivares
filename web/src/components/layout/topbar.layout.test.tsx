// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Structure of the top bar (0.5, after #2544 and #3161). jsdom does not lay out, so the pixel
// facts live in the browser captures; what is pinned here is what those pixels depend on: ONE
// 56 px ROW with the page title and four controls (the menu button below 761 px, search,
// notifications and the account) and no places, and the Tab order equal to the reading order.
// The version, Stop all and the stops live in the status bar (status-bar.test.tsx).
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import type { ComponentProps, ReactNode } from 'react'
import { renderIntel } from '@/test/intel'

const routerState = vi.hoisted(() => ({
  pathname: '/console',
  search: {} as Record<string, unknown>,
}))
const identity = vi.hoisted(() => ({ version: '1.0' }))
const navigate = vi.hoisted(() => vi.fn())
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
    ],
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
      ],
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

/** Every control a person can reach in the bar. */
const controls = (header: HTMLElement) => [
  ...header.querySelectorAll<HTMLElement>('a, button'),
]

describe('the top bar holds the title and four controls, nothing else (0.5)', () => {
  it('is one 56 px row: the menu button, the page title, search, notifications and the account', async () => {
    const { container } = renderIntel(<Topbar onMenu={() => {}} />)
    const header = container.querySelector('header') as HTMLElement
    expect(header.getAttribute('data-slot')).toBe('topbar')
    expect(header.className).toContain('h-14')
    expect(header.className).toContain('flex-nowrap')
    // No places: the left navigation is the only one.
    expect(within(header).queryByRole('navigation')).toBeNull()
    expect(within(header).queryAllByRole('link')).toEqual([])
    expect(
      header.querySelector('[data-slot="topbar-title"]'),
    ).toHaveTextContent('Administration')
    await waitFor(() => expect(controls(header)).toHaveLength(4))
    const [menu, search, bell, account] = controls(header)
    expect(menu).toHaveAccessibleName('Open navigation')
    expect(menu.className).toContain('min-[761px]:hidden')
    expect(search).toHaveAccessibleName('Search and commands')
    expect(bell).toHaveAccessibleName('Notifications')
    expect(account).toHaveAccessibleName(/, Account$/)
  })

  it('opens the navigation drawer from its menu button', async () => {
    const user = userEvent.setup()
    const onMenu = vi.fn()
    renderIntel(<Topbar onMenu={onMenu} />)
    await user.click(screen.getByRole('button', { name: 'Open navigation' }))
    expect(onMenu).toHaveBeenCalledOnce()
  })

  it('names the page the address is: a place of its own by its place name', () => {
    routerState.pathname = '/permissions'
    routerState.search = { tab: 'approvals' }
    const { container } = renderIntel(<Topbar onMenu={() => {}} />)
    expect(
      container.querySelector('[data-slot="topbar-title"]'),
    ).toHaveTextContent('Rules & approvals')
  })

  it('moves the version, theme, favorites, the mark and Stop all out of the bar', () => {
    const { container } = renderIntel(<Topbar onMenu={() => {}} />)
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
    const { container } = renderIntel(<Topbar onMenu={() => {}} />)
    const search = within(container).getByRole('button', {
      name: 'Search and commands',
    })
    expect(search).toHaveTextContent(/^Search/)
    expect(search).toHaveAttribute('aria-keyshortcuts', 'Control+K Meta+K')
    expect(search.className).toContain('max-[1100px]:w-9')
  })

  it('Tab order is the reading order: menu, search, notifications, account', async () => {
    const user = userEvent.setup()
    const { container } = renderIntel(<Topbar onMenu={() => {}} />)
    const header = container.querySelector('header') as HTMLElement
    await waitFor(() => expect(controls(header)).toHaveLength(4))
    const order: string[] = []
    await user.tab()
    while (header.contains(document.activeElement)) {
      const el = document.activeElement as HTMLElement
      order.push(el.getAttribute('aria-label') ?? el.textContent ?? '')
      await user.tab()
    }
    expect(order[0]).toBe('Open navigation')
    expect(order[1]).toBe('Search and commands')
    expect(order[2]).toBe('Notifications')
    expect(order[3]).toMatch(/, Account$/)
  })
})

// The scope moved from the bar to the left navigation (sidebar.tsx) with the same control.
describe('the scope control', () => {
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

describe('the bar speaks the platform and names its icons (#3121)', () => {
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
    const { container } = renderIntel(<Topbar onMenu={() => {}} />)
    const search = within(container).getByRole('button', {
      name: 'Search and commands',
    })
    // The hint is drawn, not text: what the button says is its label, inside its
    // name (WCAG 2.5.3); the chord itself is aria-keyshortcuts.
    expect(search.querySelector('kbd')).toHaveAttribute('data-keys', hint)
    expect(search.textContent).toBe('Search')
  })

  it('gives every control that shows only an icon a tooltip with its name', () => {
    const { container } = renderIntel(<Topbar onMenu={() => {}} />)
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
    expect(
      within(header).getByRole('button', { name: 'Search and commands' }),
    ).toHaveAttribute('title', 'Search and commands')
    expect(
      within(header).getByRole('button', { name: /, Account$/ }),
    ).toHaveAttribute('title', 'Account')
  })
})
