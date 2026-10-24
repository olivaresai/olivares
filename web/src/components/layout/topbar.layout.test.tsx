// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Structure of the top bar (console Estate #2544, slimmed in #3161). jsdom does not lay
// out, so the pixel facts (no overlap at 1280 and 1920, light, dark and black) live in the
// browser captures; what is pinned here is what those pixels depend on: ONE 56 px ROW and
// no sidebar, the five places and four controls (the scope, search, notifications and the
// account) and nothing else, the scope named once, and the Tab order equal to the reading
// order. The version, Stop all and the stops live in the status bar (status-bar.test.tsx).
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
    sessionCounts: { needsYou: 1, live: 5, running: 7 },
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

/** Every control a person can reach in the bar that is not one of the places. */
function controlsOutsidePlaces(header: HTMLElement): HTMLElement[] {
  const places = within(header).getByRole('navigation', { name: 'Journeys' })
  return [...header.querySelectorAll<HTMLElement>('a, button')].filter(
    (el) => !places.contains(el),
  )
}

describe('the top bar holds the places and four controls, nothing else (#3161)', () => {
  it('is one 56 px row: the scope, the five places, search, notifications and the account', async () => {
    const { container } = renderIntel(<Topbar />)
    const header = container.querySelector('header') as HTMLElement
    expect(header.getAttribute('data-slot')).toBe('topbar')
    expect(header.className).toContain('h-14')
    expect(header.className).toContain('flex-nowrap')
    const places = within(header).getByRole('navigation', { name: 'Journeys' })
    expect(
      within(places)
        .getAllByRole('link')
        .map((a) => a.getAttribute('title')),
    ).toEqual(['Now', 'Sessions', 'Tools', 'Rules', 'Ledger'])
    // The counts: the sessions open (the status bar's number, idle ones too), and
    // the requests that wait for a person.
    expect(
      within(places)
        .getAllByRole('link')
        .map((a) => a.textContent),
    ).toEqual(['Now', 'Sessions, 7, Needs you', 'Tools', 'Rules, 3', 'Ledger'])
    // Four controls, and still four once the workspaces answered after the first paint.
    await waitFor(() => expect(controlsOutsidePlaces(header)).toHaveLength(4))
    const controls = controlsOutsidePlaces(header)
    expect(controls[0]).toHaveAttribute('data-slot', 'scope-switcher')
    expect(controls[1]).toHaveAccessibleName('Search and commands')
    expect(controls[2]).toHaveAccessibleName('Notifications')
    expect(controls[3]).toHaveAccessibleName(/, Account$/)
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
  })

  it('names the scope once: the workspace in force, or the organization when none is', async () => {
    useWorkspaceStore.setState({
      activeWorkspace: 'w2',
      activeWorkspaceName: 'Research',
    })
    const user = userEvent.setup()
    const { container } = renderIntel(<Topbar />)
    const header = container.querySelector('header') as HTMLElement
    const scope = header.querySelector(
      '[data-slot="scope-switcher"]',
    ) as HTMLElement
    await within(scope).findByText('Research')
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
    const { container } = renderIntel(<Topbar />)
    const scope = container.querySelector(
      '[data-slot="scope-switcher"]',
    ) as HTMLElement
    await within(scope).findByText('Workspace not in list')
    expect(scope).toHaveAccessibleName(/^Workspace not in list, /)
  })

  it('gives the scope to All areas below 1024 px, where All areas shows it', () => {
    const { container } = renderIntel(<Topbar />)
    expect(
      container.querySelector('[data-slot="topbar-scope"]')!.className,
    ).toContain('max-[1023px]:hidden')
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

  it('Tab order is the reading order: the scope, the places, then the actions', async () => {
    const user = userEvent.setup()
    const { container } = renderIntel(<Topbar />)
    const header = container.querySelector('header') as HTMLElement
    await waitFor(() => expect(controlsOutsidePlaces(header)).toHaveLength(4))
    const order: string[] = []
    await user.tab()
    while (header.contains(document.activeElement)) {
      const el = document.activeElement as HTMLElement
      order.push(
        el.getAttribute('data-slot') === 'scope-switcher'
          ? 'scope'
          : (el.getAttribute('aria-label') ?? el.textContent ?? ''),
      )
      await user.tab()
    }
    const at = (name: string) => order.findIndex((n) => n.startsWith(name))
    expect(at('scope')).toBe(0)
    expect(at('scope')).toBeLessThan(at('Now'))
    expect(at('Now')).toBeLessThan(at('Ledger'))
    expect(at('Ledger')).toBeLessThan(at('Search and commands'))
    expect(at('Search and commands')).toBeLessThan(at('Notifications'))
    expect(at('Notifications')).toBeLessThan(
      order.findIndex((n) => n.endsWith(', Account')),
    )
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
    const { container } = renderIntel(<Topbar />)
    const search = within(container).getByRole('button', {
      name: 'Search and commands',
    })
    expect(search).toHaveTextContent(hint)
    if (hint !== '⌘K') expect(search.textContent).not.toContain('⌘')
  })

  it('gives every control that shows only an icon a tooltip with its name', () => {
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
    expect(
      within(header).getByRole('button', { name: 'Search and commands' }),
    ).toHaveAttribute('title', 'Search and commands')
    expect(
      within(header).getByRole('button', { name: /, Account$/ }),
    ).toHaveAttribute('title', 'Account')
  })
})
