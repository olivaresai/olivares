// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Structure of the responsive topbar. jsdom does not lay out, so the pixel facts (no
// overlap at 1440/1024/390, the ellipsis) live in the browser evidence; what is pinned
// here is what those pixels depend on: ONE ROW at every width, one DOM instance of
// every control, the Tab order equal to the reading order, the parent crumb keeping its
// accessible name in its condensed form, and the page crumb carrying its full title.
//
// ⛔ THE CONTEXT ROW IS GONE, AND THE ASSERTIONS THAT PINNED IT ARE REPLACED RATHER
//    THAN DELETED. The bar carried a second 40 px row below `lg` holding the
//    organisation and workspace switchers, and the consequence measured 52 % of a
//    390×844 phone spent on chrome before any content. The switchers live in the
//    sidebar (`app-sidebar.tsx`), and the checks below say so: there is NO
//    `topbar-context` wrapper, and no switcher in this bar.
//
// THE v26.10 BAR (redesign §3.2) is breadcrumb · page state · page actions · panel
// toggles, 52 px. Search sits in the sidebar beside New session, and on the phone, where
// the sidebar gives way to the phone bar, in this bar; theme, settings and the account sit
// in the sidebar footer (the account also here on the phone).
import { within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import type { ComponentProps, ReactNode } from 'react'
import { renderIntel } from '@/test/intel'

const routerState = vi.hoisted(() => ({ pathname: '/console' }))
vi.mock('@tanstack/react-router', () => ({
  useRouterState: ({
    select,
  }: {
    select: (s: { location: { pathname: string } }) => unknown
  }) => select({ location: { pathname: routerState.pathname } }),
  useNavigate: () => vi.fn(),
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
    can: () => true,
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

import { afterEach } from 'vitest'
import { useWorkspaceStore } from '@/stores/workspace'
import { Topbar } from './topbar'

afterEach(() => {
  routerState.pathname = '/console'
  useWorkspaceStore.setState({
    activeWorkspace: null,
    activeWorkspaceName: null,
  })
})

describe('Topbar — responsive structure', () => {
  it('is one 52 px row with the trail, the reserved page slots and each action once', async () => {
    const { container } = renderIntel(<Topbar />)
    const header = container.querySelector('header') as HTMLElement
    expect(header.getAttribute('data-slot')).toBe('topbar')
    expect(header.className).toContain('h-13')
    expect(header.className).toContain('flex-nowrap')
    // No second row and no switcher in the bar.
    expect(header.querySelector('[data-slot="topbar-context"]')).toBeNull()
    expect(
      within(header).queryByRole('button', { name: /workspace/i }),
    ).toBeNull()
    // The trail: the area links, the page does not.
    const nav = within(header).getByRole('navigation', { name: 'Breadcrumb' })
    expect(
      within(nav).getAllByRole('link', { name: /System/ })[0],
    ).toHaveAttribute('href', '/areas/system')
    // The places a screen fills are reserved, and empty until it does.
    expect(header.querySelector('[data-slot="page-state"]')).not.toBeNull()
    expect(header.querySelector('[data-slot="page-actions"]')).not.toBeNull()
    // One instance of each action.
    for (const name of [
      'Search and commands',
      'Notifications',
      'Open documentation for this view',
    ])
      expect(header.querySelectorAll(`[aria-label="${name}"]`)).toHaveLength(1)
    // The phone-only controls say so in their classes: jsdom does not lay out.
    expect(
      within(header).getByRole('button', { name: 'Search and commands' })
        .className,
    ).toContain('min-[761px]:hidden')
  })

  it('Tab order is the reading order: the trail, then the actions', async () => {
    const user = userEvent.setup()
    const { container } = renderIntel(<Topbar />)
    const header = container.querySelector('header') as HTMLElement
    const order: string[] = []
    await user.tab()
    while (header.contains(document.activeElement)) {
      const el = document.activeElement as HTMLElement
      order.push(el.getAttribute('aria-label') ?? el.textContent ?? '')
      await user.tab()
    }
    const at = (name: string) => order.findIndex((n) => n.includes(name))
    expect(at('System')).toBeGreaterThanOrEqual(0)
    expect(at('System')).toBeLessThan(at('Search and commands'))
    expect(at('Search and commands')).toBeLessThan(at('Notifications'))
    expect(at('Notifications')).toBeLessThan(at('Open documentation'))
  })

  it('reads a journey the way the sidebar names it, after the workspace it operates on', () => {
    routerState.pathname = '/providers'
    useWorkspaceStore.setState({
      activeWorkspace: 'w1',
      activeWorkspaceName: 'Billing operations',
    })
    const { container } = renderIntel(<Topbar />)
    const nav = within(
      container.querySelector('header') as HTMLElement,
    ).getByRole('navigation', { name: 'Breadcrumb' })
    expect(nav.textContent?.replace(/\s+/g, ' ')).toMatch(
      /Billing operations.*AI tools/,
    )
    // The journey is the page (the breadcrumb's current page), and the workspace is a
    // caption, not a door: no anchor in the trail.
    expect(nav.querySelectorAll('a')).toHaveLength(0)
    expect(nav.querySelector('[aria-current="page"]')?.textContent).toBe(
      'AI tools',
    )
  })
})
