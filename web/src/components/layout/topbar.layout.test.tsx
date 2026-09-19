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
//    390×844 phone spent on chrome before any content. The switchers
//    moved to the rail (`sidebar.tsx`), where the scope belongs, and the checks below
//    now say the opposite of what they used to say: there is NO `topbar-context`
//    wrapper, and no switcher in this bar. A test that had merely been deleted would
//    have left nothing saying which way it must be.
import { within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import type { ComponentProps, ReactNode } from 'react'
import { renderIntel } from '@/test/intel'

vi.mock('@tanstack/react-router', () => ({
  useRouterState: ({
    select,
  }: {
    select: (s: { location: { pathname: string } }) => unknown
  }) => select({ location: { pathname: '/console' } }),
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

import { Topbar } from './topbar'

const bar = () => document.querySelector('[data-slot="topbar"]') as HTMLElement

describe('Topbar — responsive structure', () => {
  it('renders the trail, every action and the context switchers once each', async () => {
    renderIntel(<Topbar onMenuClick={() => {}} />)
    const header = bar()
    expect(header.tagName).toBe('HEADER')
    // ONE ROW AT EVERY WIDTH, and its height is the token — not an emergent property
    // of whatever happens to be in it.
    expect(header).toHaveClass('flex-nowrap', 'shrink-0')
    expect(header.className).toContain('h-[var(--console-header-height)]')
    expect(header.className).not.toContain('flex-wrap')

    const trail = within(header).getByRole('navigation', { name: 'Breadcrumb' })
    // N1: /console is «Administration» under System & settings; the trail is the
    // area (linked) then the page — not «Overview › …».
    const page = within(trail).getByRole('link', { name: 'Administration' })
    expect(page).toHaveAttribute('aria-current', 'page')
    expect(page).toHaveAttribute('title', 'Administration')
    // Two renderings of the parent crumb (icon below sm, text from sm), the SAME
    // accessible name and href in both — condensed, not hidden — and each a 24 px
    // pointer target.
    const parents = within(trail).getAllByRole('link', {
      name: 'System & settings',
    })
    expect(parents).toHaveLength(2)
    for (const parent of parents)
      expect(parent).toHaveAttribute('href', '/areas/system')
    expect(parents[0]).toHaveClass('sm:hidden', 'size-6')
    // A 24 px minimum width: the area name may be two letters ("AI"), and the text link
    // must still be a WCAG 2.5.8 target — axe measured 14 px on the built console.
    expect(parents[1]).toHaveClass(
      'hidden',
      'sm:inline-block',
      'leading-6',
      'min-w-6',
    )
    // The old «Overview» parent crumb is gone from a module page.
    expect(within(trail).queryByRole('link', { name: 'Overview' })).toBeNull()

    expect(
      within(header).getByRole('button', { name: 'Open menu' }),
    ).toHaveClass('lg:hidden')
    expect(
      within(header).getByRole('button', { name: 'Search' }),
    ).toBeInTheDocument()
    expect(
      within(header).getByRole('link', { name: /documentation/i }),
    ).toBeInTheDocument()
    expect(
      within(header).getByRole('button', { name: 'Notifications' }),
    ).toBeInTheDocument()

    // NO SECOND ROW AND NO SCOPE CONTROL IN THIS BAR. Both switchers live in the rail
    // now; the bar must not grow a second copy of either.
    expect(header.querySelector('[data-slot="topbar-context"]')).toBeNull()
    expect(
      within(header).queryByRole('button', { name: /01a0776d/ }),
    ).toBeNull()
    expect(
      within(header).queryByRole('button', { name: /All workspaces/ }),
    ).toBeNull()
    // Theme and account are still the last two controls of the one row.
    const theme = within(header).getByRole('button', { name: /theme/i })
    const account = within(header).getByRole('button', { name: 'Account' })
    expect(
      theme.compareDocumentPosition(account) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy()
    // Exactly one of each: no duplicated control for the narrow layout.
    expect(
      within(header).getAllByRole('button', { name: 'Account' }),
    ).toHaveLength(1)
  })

  it('Tab order is the reading order, and it no longer detours through a second row', async () => {
    const user = userEvent.setup()
    renderIntel(<Topbar onMenuClick={() => {}} />)
    await within(bar()).findByRole('button', { name: 'Search' })
    const names: string[] = []
    for (let i = 0; i < 8; i++) {
      await user.tab()
      const el = document.activeElement as HTMLElement
      names.push(el.getAttribute('aria-label') ?? el.textContent?.trim() ?? '')
    }
    // The old sequence visited the context row between Notifications and the theme
    // toggle — an irregularity the retired comment documented as a stated trade. With
    // one row there is nothing to detour through: the order is simply the DOM.
    // `FavoriteButton` is absent here: it renders nothing without a personal-navigation
    // partition, and this test mounts the bar alone. That is its real behaviour, not a
    // gap in the mock.
    expect(names.slice(0, 6)).toEqual([
      'Open menu',
      'System & settings', // the icon variant (first in DOM; jsdom does not apply sm:hidden)
      'System & settings',
      'Search',
      'Open documentation for this view',
      'Notifications',
    ])
    expect(names[6]).toMatch(/theme/i)
    expect(names[7]).toBe('Account')
  })

  it('the search text label and shortcut hint show only from xl; the button keeps its name', () => {
    renderIntel(<Topbar onMenuClick={() => {}} />)
    const search = within(bar()).getByRole('button', { name: 'Search' })
    const label = within(search).getByText(/Search/)
    expect(label).toHaveClass('hidden', 'xl:inline')
    expect(within(search).getByText('⌘K')).toHaveClass(
      'hidden',
      'xl:inline-flex',
    )
  })
})
