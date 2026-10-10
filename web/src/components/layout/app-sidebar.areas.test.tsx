// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE FULL SIDEBAR SHOWS WHAT THE PRODUCT HOLDS. Under the pinned destinations it lists the
// areas, grouped and folding, from the first sign-in. A page whose module is off is a calm
// entry (dimmed, tagged Off) and still a link. The rail keeps the pins and "All areas".
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ComponentProps } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
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
    activeOptions: _o,
    activeProps: _p,
    search: _s,
    ...props
  }: ComponentProps<'a'> & {
    to?: string
    activeOptions?: unknown
    activeProps?: unknown
    search?: unknown
  }) => (
    <a href={to} {...props}>
      {children}
    </a>
  ),
}))
const auth = vi.hoisted(() => ({ denied: new Set<string>() }))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    can: (p: string) => !auth.denied.has(p),
    grants: [],
    isSuperadmin: false,
    activeTenant: null,
    setActiveTenant: () => {},
  }),
}))
vi.mock('@/lib/hooks/use-server-info', () => ({
  useServerInfo: () => ({ data: { version: '1.0' } }),
}))
vi.mock('@/features/governance/use-pending-approvals', () => ({
  usePendingApprovals: () => ({ query: { data: undefined } }),
  pendingCount: () => 0,
}))
vi.mock('./use-session-rail', () => ({
  useSessionRail: () => ({
    sessionCounts: { live: 0, needsYou: 0 },
    visible: true,
    status: 'ready',
    allTo: '/sessions',
    groups: [],
  }),
}))
vi.mock('./tenant-switcher', () => ({ TenantSwitcher: () => null }))
vi.mock('./workspace-switcher', () => ({ WorkspaceSwitcher: () => null }))
vi.mock('./personal-navigation', () => ({
  SidebarFavorites: () => null,
  PersonalNavigation: () => null,
}))
vi.mock('./engine-status', () => ({ EngineStatus: () => null }))
vi.mock('./theme-toggle', () => ({ ThemeToggle: () => null }))
vi.mock('./user-menu', () => ({
  UserMenu: () => <button type="button">Account</button>,
}))

import { FEATURE_VIEWS } from '@/features/registry'
import { useModulesStore } from '@/stores/modules'
import { AppSidebar } from './app-sidebar'

beforeEach(() => {
  useModulesStore.setState({ off: new Set() })
})
afterEach(() => {
  auth.denied = new Set()
  useModulesStore.setState({ off: new Set() })
})

const view = (id: string) => {
  const found = FEATURE_VIEWS.find((v) => v.id === id)
  if (!found) throw new Error(`no such view: ${id}`)
  return found
}
const primary = () => screen.getByRole('complementary', { name: 'Primary' })
const linkTo = (href: string) =>
  within(primary())
    .getAllByRole('link')
    .find((a) => a.getAttribute('href') === href)!
const full = (onNavigate?: () => void) =>
  renderIntel(
    <AppSidebar
      mode="full"
      onToggle={() => {}}
      areasOpen={false}
      onOpenAreas={() => {}}
      onNavigate={onNavigate}
    />,
  )

describe('the full sidebar lists the areas under the pinned destinations', () => {
  it('shows the areas and their pages from the first sign-in, after the pins', () => {
    full()
    const pins = within(primary()).getByRole('navigation', { name: 'Journeys' })
    const areas = within(primary()).getByRole('navigation', { name: 'Areas' })
    expect(
      pins.compareDocumentPosition(areas) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy()
    const hrefs = within(areas)
      .getAllByRole('link')
      .map((a) => a.getAttribute('href'))
    expect(hrefs).toContain('/areas/observation')
    expect(hrefs).toContain(view('audit').path)
    expect(hrefs).toContain(view('console').path)
  })

  it('folds and unfolds an area without leaving the page', async () => {
    const user = userEvent.setup()
    full()
    const fold = within(primary()).getByRole('button', {
      name: /^(Show|Hide) Observability & evidence modules$/,
    })
    const open = fold.getAttribute('aria-expanded') === 'true'
    await user.click(fold)
    expect(fold).toHaveAttribute('aria-expanded', String(!open))
  })

  it('keeps a permission refusal hidden', () => {
    auth.denied = new Set([view('audit').permission ?? ''])
    full()
    const hrefs = within(primary())
      .getAllByRole('link')
      .map((a) => a.getAttribute('href'))
    expect(hrefs).not.toContain(view('audit').path)
    expect(hrefs).toContain(view('console').path)
  })

  it('draws a page whose module is off dimmed and tagged Off, still a link', () => {
    useModulesStore.setState({ off: new Set(['deploy']) })
    full()
    const link = linkTo(view('deploy').path)
    expect(within(link).getByText('Off')).toBeInTheDocument()
    expect(link.querySelector('.text-text-3')).not.toBeNull()
    expect(within(linkTo(view('audit').path)).queryByText('Off')).toBeNull()
  })

  it('tells the overlay when a destination was chosen', async () => {
    const user = userEvent.setup()
    const onNavigate = vi.fn()
    full(onNavigate)
    await user.click(linkTo(view('audit').path))
    expect(onNavigate).toHaveBeenCalledTimes(1)
  })

  it('leaves the rail with the pins and All areas, no area tree', () => {
    renderIntel(
      <AppSidebar
        mode="rail"
        onToggle={() => {}}
        areasOpen={false}
        onOpenAreas={() => {}}
      />,
    )
    expect(
      within(primary()).queryByRole('navigation', { name: 'Areas' }),
    ).toBeNull()
    expect(
      within(primary()).getByRole('button', { name: 'All areas' }),
    ).toBeInTheDocument()
  })
})
