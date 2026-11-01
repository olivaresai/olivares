// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE ONE LEFT NAVIGATION AS A PERSON USES IT: four named groups and Settings at the foot,
// one current place, counts read from current state, a fold that works from the mouse and
// the keyboard, and the same items in the same order in the narrow drawer.
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ComponentProps } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { renderIntel } from '@/test/intel'

const state = vi.hoisted(() => ({
  pathname: '/',
  search: {} as Record<string, unknown>,
  needsYou: 0,
  running: 0,
  open: 0,
  approvals: undefined as { items: unknown[] } | undefined,
}))
vi.mock('@tanstack/react-router', () => ({
  useRouterState: ({
    select,
  }: {
    select: (s: {
      location: { pathname: string; search: Record<string, unknown> }
    }) => unknown
  }) =>
    select({ location: { pathname: state.pathname, search: state.search } }),
  Link: ({
    children,
    to,
    search,
    ...props
  }: ComponentProps<'a'> & {
    to?: string
    search?: Record<string, string>
  }) => (
    <a href={search ? `${to}?${new URLSearchParams(search)}` : to} {...props}>
      {children}
    </a>
  ),
}))

const can = vi.fn((_permission: string) => true)
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    can: (p: string) => can(p),
    grants: [],
    isSuperadmin: false,
    activeTenant: null,
    principal: null,
  }),
}))
vi.mock('./scope-switcher', () => ({ ScopeSwitcher: () => null }))
vi.mock('./use-session-rail', () => ({
  useSessionRail: () => ({
    sessionCounts: {
      needsYou: state.needsYou,
      live: 0,
      running: state.running,
      open: state.open,
    },
  }),
}))
vi.mock('@/features/governance/use-pending-approvals', async (actual) => ({
  ...(await actual<object>()),
  usePendingApprovals: () => ({ query: { data: state.approvals } }),
}))

import { usePreferencesStore } from '@/stores/preferences'
import { NavigationDrawer, Sidebar } from './sidebar'

afterEach(() => {
  state.pathname = '/'
  state.search = {}
  state.needsYou = 0
  state.running = 0
  state.open = 0
  state.approvals = undefined
  can.mockReset()
  can.mockReturnValue(true)
  usePreferencesStore.setState({ navCollapsed: false })
})

const PLACES = [
  'Now',
  'Sessions',
  'Files & workspaces',
  'Work & communications',
  'AI tools',
  'Models',
  'MCP & skills',
  'Sources',
  'Rules & approvals',
  'Usage & cost',
  'Activity & audit',
  'Access map',
  'People & groups',
  'Security',
  'Operations',
  'License',
  'Settings',
]

const column = () => screen.getByRole('navigation', { name: 'Main navigation' })
const names = (root: HTMLElement) =>
  within(root)
    .getAllByRole('link')
    .map((a) => a.textContent)

describe('the left navigation', () => {
  it('lists the four groups and every place once, in order, with Settings last', () => {
    renderIntel(<Sidebar />)
    const nav = column()
    expect(
      within(nav)
        .getAllByRole('region')
        .map((g) => g.getAttribute('aria-label')),
    ).toEqual(['Work', 'Tools', 'Control', 'Admin'])
    expect(names(nav)).toEqual(PLACES)
    expect(within(nav).queryByText('All areas')).toBeNull()
  })

  it('marks exactly the place the address belongs to', () => {
    state.pathname = '/providers'
    renderIntel(<Sidebar />)
    const current = within(column()).getAllByRole('link', { current: 'page' })
    expect(current.map((a) => a.textContent)).toEqual(['AI tools'])
  })

  it('marks License, not Settings, on the edition section', () => {
    state.pathname = '/settings'
    state.search = { section: 'edition' }
    renderIntel(<Sidebar />)
    expect(
      within(column()).getByRole('link', { current: 'page' }),
    ).toHaveTextContent('License')
  })

  it('opens Rules on the approval queue', () => {
    renderIntel(<Sidebar />)
    expect(
      within(column()).getByRole('link', { name: 'Rules & approvals' }),
    ).toHaveAttribute('href', '/permissions?tab=approvals')
  })

  it('counts the open sessions, says when one needs the person, and counts pending approvals', () => {
    // Stop all would also end an archived one; the badge counts what the list shows.
    state.running = 8
    state.open = 7
    state.needsYou = 2
    state.approvals = { items: [{}, {}, {}] }
    renderIntel(<Sidebar />)
    const nav = column()
    expect(
      within(nav).getByRole('link', {
        name: /^Sessions\s*,\s*7\s*,\s*Needs you$/,
      }),
    ).toBeInTheDocument()
    expect(
      within(nav).getByRole('link', { name: /^Rules & approvals\s*,\s*3$/ }),
    ).toBeInTheDocument()
  })

  it('draws no count when nothing waits', () => {
    renderIntel(<Sidebar />)
    expect(
      within(column()).getByRole('link', { name: 'Sessions' }),
    ).toBeInTheDocument()
    expect(column().querySelector('[data-slot="count-badge"]')).toBeNull()
  })

  it('offers only the places the principal may open', () => {
    can.mockReturnValue(false)
    renderIntel(<Sidebar />)
    const offered = names(column())
    expect(offered).toContain('Now')
    expect(offered).toContain('Settings')
    expect(offered).not.toContain('Sessions')
    expect(offered).not.toContain('Rules & approvals')
  })

  it('folds to its icons and opens again, with the mouse and the keyboard', async () => {
    const user = userEvent.setup()
    renderIntel(<Sidebar />)
    await user.click(
      screen.getByRole('button', { name: 'Collapse navigation' }),
    )
    expect(column()).toHaveAttribute('data-folded', 'true')
    expect(usePreferencesStore.getState().navCollapsed).toBe(true)
    // Folded, every place still has its name.
    expect(
      within(column()).getByRole('link', { name: 'AI tools' }),
    ).toHaveAttribute('title', 'AI tools')

    const expand = screen.getByRole('button', { name: 'Expand navigation' })
    expand.focus()
    await user.keyboard('{Enter}')
    expect(column()).not.toHaveAttribute('data-folded')
    expect(usePreferencesStore.getState().navCollapsed).toBe(false)
  })
})

describe('the narrow drawer', () => {
  it('holds the same places in the same order and closes on a choice', async () => {
    const user = userEvent.setup()
    const onOpenChange = vi.fn()
    renderIntel(<NavigationDrawer open onOpenChange={onOpenChange} />)
    const drawer = screen.getByRole('dialog', { name: 'Main navigation' })
    expect(names(drawer)).toEqual(PLACES)
    await user.click(within(drawer).getByRole('link', { name: 'Models' }))
    expect(onOpenChange).toHaveBeenCalledWith(false)
  })

  it('closes from its own button', async () => {
    const user = userEvent.setup()
    const onOpenChange = vi.fn()
    renderIntel(<NavigationDrawer open onOpenChange={onOpenChange} />)
    await user.click(screen.getByRole('button', { name: 'Close navigation' }))
    expect(onOpenChange).toHaveBeenCalledWith(false)
  })
})
