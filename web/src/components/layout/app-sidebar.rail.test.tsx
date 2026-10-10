// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE SIDEBAR IS NAVIGATION, IN TWO WIDTHS. Full: destinations with labels. Rail: the same
// destinations as icons, each named for a screen reader and a tooltip. Neither width lists
// sessions: the Sessions page is the one list of sessions, so the sidebar carries only the
// Sessions count and says, in the warning role, when a session needs the person.
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ComponentProps } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { renderIntel } from '@/test/intel'

vi.mock('@tanstack/react-router', () => ({
  useRouterState: ({
    select,
  }: {
    select?: (s: {
      location: { pathname: string; search: Record<string, unknown> }
    }) => unknown
  } = {}) =>
    select ? select({ location: { pathname: '/', search: {} } }) : '',
  Link: ({
    children,
    to,
    activeOptions: _a,
    search: _s,
    ...props
  }: ComponentProps<'a'> & {
    to?: string
    activeOptions?: unknown
    search?: unknown
  }) => (
    <a href={to} {...props}>
      {children}
    </a>
  ),
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    can: () => true,
    grants: [],
    isSuperadmin: false,
    activeTenant: null,
    setActiveTenant: () => {},
  }),
}))
const identity = vi.hoisted(() => ({ version: '1.0', desktop: true }))
vi.mock('@/lib/hooks/use-server-info', () => ({
  useServerInfo: () => ({ data: { version: identity.version } }),
}))
vi.mock('@/lib/hooks/use-min-width', () => ({
  useMinWidth: () => identity.desktop,
}))
vi.mock('@/features/governance/use-pending-approvals', () => ({
  usePendingApprovals: () => ({ query: { data: undefined } }),
  pendingCount: () => 0,
}))
const rail = vi.hoisted(() => ({
  needs: 0,
  working: 2,
  workingTotal: 0,
  handoffs: 0,
}))
const row = (id: string) => ({ id }) as never
vi.mock('./use-session-rail', () => ({
  useSessionRail: () => ({
    sessionCounts: {
      live: rail.needs + (rail.workingTotal || rail.working),
      needsYou: rail.needs,
    },
    visible: true,
    status: 'ready',
    allTo: '/sessions',
    groups: [
      {
        id: 'needsYou',
        rows: Array.from({ length: rail.needs }, (_, i) => row(`n${i}`)),
        total: rail.needs + rail.handoffs,
      },
      {
        id: 'working',
        rows: Array.from({ length: rail.working }, (_, i) => row(`w${i}`)),
        total: rail.workingTotal || rail.working,
      },
      { id: 'earlier', rows: [row('e0')], total: 1 },
    ],
  }),
}))
// Not under test here: each owns its suite.
vi.mock('./tenant-switcher', () => ({ TenantSwitcher: () => null }))
vi.mock('./workspace-switcher', () => ({ WorkspaceSwitcher: () => null }))
vi.mock('./personal-navigation', () => ({ SidebarFavorites: () => null }))
vi.mock('./engine-status', () => ({ EngineStatus: () => null }))
vi.mock('./theme-toggle', () => ({ ThemeToggle: () => null }))
vi.mock('./user-menu', () => ({
  UserMenu: () => <button type="button">Account</button>,
}))

import { AppSidebar } from './app-sidebar'

afterEach(() => {
  identity.version = '1.0'
  identity.desktop = true
  rail.needs = 0
  rail.handoffs = 0
  rail.working = 2
  rail.workingTotal = 0
})

const primary = () => screen.getByRole('complementary', { name: 'Primary' })
const pins = () =>
  within(primary()).getByRole('navigation', { name: 'Journeys' })

describe('the sidebar in its two widths', () => {
  it.each(['full', 'rail'] as const)(
    'paints the server version in %s without unfolding or hovering',
    (mode) => {
      identity.version = '26.10.1'
      const { rerender } = renderIntel(
        <AppSidebar
          mode={mode}
          onToggle={() => {}}
          areasOpen={false}
          onOpenAreas={() => {}}
        />,
      )
      expect(
        within(primary()).getByText('26.10.1', { exact: true }),
      ).toBeVisible()
      identity.version = '1.0'
      rerender(
        <AppSidebar
          mode={mode}
          onToggle={() => {}}
          areasOpen={false}
          onOpenAreas={() => {}}
        />,
      )
      expect(within(primary()).getByText('1.0', { exact: true })).toBeVisible()
      identity.version = ''
      rerender(
        <AppSidebar
          mode={mode}
          onToggle={() => {}}
          areasOpen={false}
          onOpenAreas={() => {}}
        />,
      )
      expect(within(primary()).queryByText('1.0', { exact: true })).toBeNull()
    },
  )

  it.each(['full', 'rail'] as const)(
    'leaves phone identity to the phone brand in %s',
    (mode) => {
      identity.desktop = false
      renderIntel(
        <AppSidebar
          mode={mode}
          onToggle={() => {}}
          areasOpen={false}
          onOpenAreas={() => {}}
        />,
      )
      expect(within(primary()).queryByText('1.0', { exact: true })).toBeNull()
    },
  )

  it('lists no sessions in either width', () => {
    for (const mode of ['full', 'rail'] as const) {
      const { unmount } = renderIntel(
        <AppSidebar
          mode={mode}
          onToggle={() => {}}
          areasOpen={false}
          onOpenAreas={() => {}}
        />,
      )
      expect(primary()).toHaveAttribute('data-sidebar-mode', mode)
      expect(document.querySelector('[data-slot="session-rail"]')).toBeNull()
      unmount()
    }
  })

  it('shows the live Sessions count, and in the warning role when a session needs the person', () => {
    rail.needs = 1
    renderIntel(
      <AppSidebar
        mode="full"
        onToggle={() => {}}
        areasOpen={false}
        onOpenAreas={() => {}}
      />,
    )
    const sessions = within(pins()).getByRole('link', { name: /Sessions/ })
    expect(sessions).toHaveTextContent('3')
    expect(sessions.querySelector('.text-warning')).not.toBeNull()
  })

  it('counts every live session, not only the rows a group shows', () => {
    rail.working = 8
    rail.workingTotal = 12
    renderIntel(
      <AppSidebar
        mode="full"
        onToggle={() => {}}
        areasOpen={false}
        onOpenAreas={() => {}}
      />,
    )
    expect(
      within(pins()).getByRole('link', { name: /Sessions/ }),
    ).toHaveTextContent('12')
  })

  it('in the rail announces the count and that a session needs the person', () => {
    rail.needs = 1
    renderIntel(
      <AppSidebar
        mode="rail"
        onToggle={() => {}}
        areasOpen={false}
        onOpenAreas={() => {}}
      />,
    )
    expect(
      within(primary()).getByRole('link', { name: 'Sessions, 3, Needs you' }),
    ).toBeInTheDocument()
  })

  it('in the rail names every destination for a screen reader and draws no label text', () => {
    renderIntel(
      <AppSidebar
        mode="rail"
        onToggle={() => {}}
        areasOpen={false}
        onOpenAreas={() => {}}
      />,
    )
    const sessions = within(primary()).getByRole('link', {
      name: 'Sessions, 2',
    })
    expect(sessions).toHaveAttribute('data-journey', 'sessions')
    expect(within(sessions).queryByText('Sessions')).toBeNull()
  })

  it('folds and unfolds from its own button', async () => {
    const user = userEvent.setup()
    const onToggle = vi.fn()
    const { unmount } = renderIntel(
      <AppSidebar
        mode="full"
        onToggle={onToggle}
        areasOpen={false}
        onOpenAreas={() => {}}
      />,
    )
    await user.click(screen.getByRole('button', { name: 'Collapse sidebar' }))
    expect(onToggle).toHaveBeenCalledTimes(1)
    unmount()
    renderIntel(
      <AppSidebar
        mode="rail"
        onToggle={onToggle}
        areasOpen={false}
        onOpenAreas={() => {}}
      />,
    )
    await user.click(screen.getByRole('button', { name: 'Expand sidebar' }))
    expect(onToggle).toHaveBeenCalledTimes(2)
  })
})

it.each(['full', 'rail'] as const)(
  'offered handoffs do not change the Sessions count or attention in %s',
  (mode) => {
    rail.handoffs = 1
    rail.working = 31
    const view = renderIntel(
      <AppSidebar
        mode={mode}
        onToggle={() => {}}
        areasOpen={false}
        onOpenAreas={() => {}}
      />,
    )
    const link = within(pins()).getByRole('link', { name: /Sessions/ })
    expect(link).toHaveTextContent('31')
    expect(link).not.toHaveAttribute(
      'aria-label',
      expect.stringContaining('Needs you'),
    )
    expect(link.querySelector('.text-warning')).toBeNull()
    expect(link.querySelector('[data-slot="session-attention"]')).toBeNull()
    view.unmount()
  },
)

it.each(['full', 'rail'] as const)(
  'shows the needs-you dot for sessions in %s',
  (mode) => {
    rail.needs = 1
    const view = renderIntel(
      <AppSidebar
        mode={mode}
        onToggle={() => {}}
        areasOpen={false}
        onOpenAreas={() => {}}
      />,
    )
    const link = within(pins()).getByRole('link', { name: /Sessions/ })
    expect(
      link.querySelector('[data-slot="session-attention"] .bg-warning'),
    ).not.toBeNull()
    expect(link).toHaveAccessibleName(expect.stringContaining('Needs you'))
    view.unmount()
  },
)
