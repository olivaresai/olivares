// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE ONE LEFT NAVIGATION AS A PERSON USES IT: the person's own sessions under Sessions, then
// Files and Tools, and Settings at the foot with the administrative places one level under it
// (its More toggle), no group headers; one current place, counts read from current state, a
// fold that works from the mouse and the keyboard, and the same items in the same order in
// the narrow drawer.
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ComponentProps } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel } from '@/test/intel'

const state = vi.hoisted(() => ({
  pathname: '/now',
  search: {} as Record<string, unknown>,
  needsYou: 0,
  running: 0,
  open: 0,
  approvals: undefined as { items: unknown[] } | undefined,
  groups: [] as { id: string; rows: unknown[] }[],
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
    groups: state.groups,
  }),
}))
vi.mock('@/features/governance/use-pending-approvals', async (actual) => ({
  ...(await actual<object>()),
  usePendingApprovals: () => ({ query: { data: state.approvals } }),
}))

import { usePreferencesStore } from '@/stores/preferences'
import { NavigationDrawer, Sidebar } from './sidebar'

beforeEach(() => usePreferencesStore.setState({ navAll: true }))
afterEach(() => {
  state.pathname = '/now'
  state.search = {}
  state.needsYou = 0
  state.running = 0
  state.open = 0
  state.approvals = undefined
  state.groups = []
  can.mockReset()
  can.mockReturnValue(true)
  usePreferencesStore.setState({ navCollapsed: false })
})

const PLACES = [
  'Sessions',
  'Files',
  'Tools',
  'Settings',
  'Overview',
  'Rules',
  'Usage',
  'Audit',
  'People',
  'Security',
  'Operations',
]

const column = () => screen.getByRole('navigation', { name: 'Main navigation' })
/** Document order: `a` comes before `b` in the navigation. */
const isBefore = (a: Element, b: Element) =>
  !!(a.compareDocumentPosition(b) & Node.DOCUMENT_POSITION_FOLLOWING)
const names = (root: HTMLElement) =>
  within(root)
    .getAllByRole('link')
    .map((a) => a.textContent)

describe('the left navigation', () => {
  it('a new person sees Sessions, Files, Tools and Settings; its More toggle opens the administrative places under Settings, and what needs them stays', async () => {
    const user = userEvent.setup()
    usePreferencesStore.setState({ navAll: false })
    state.pathname = '/workspace'
    state.approvals = { items: [{}] }
    const { unmount } = renderIntel(<Sidebar />)
    // Rules waits for a decision, so it stays shown under Settings while folded.
    expect(names(column()).map((n) => n?.replace(/\s*,.*$|\d+$/, ''))).toEqual([
      'Sessions',
      'Files',
      'Tools',
      'Settings',
      'Rules',
    ])
    const more = within(column()).getByRole('button', { name: 'More' })
    expect(more).toHaveAttribute('aria-expanded', 'false')
    const settings = within(column()).getByRole('link', { name: 'Settings' })
    const rules = within(column()).getByRole('link', { name: /^Rules/ })
    expect(isBefore(settings, rules)).toBe(true)
    await user.click(more)
    expect(more).toHaveAttribute('aria-expanded', 'true')
    expect(usePreferencesStore.getState().navAll).toBe(true)
    const usage = within(column()).getByRole('link', { name: 'Usage' })
    expect(usage).toBeVisible()
    expect(isBefore(settings, usage)).toBe(true)
    expect(
      isBefore(within(column()).getByRole('link', { name: 'Tools' }), settings),
    ).toBe(true)
    unmount()
    // A place opened by its address is marked, not hidden.
    usePreferencesStore.setState({ navAll: false })
    state.approvals = undefined
    state.pathname = '/audit'
    renderIntel(<Sidebar />)
    expect(
      within(column()).getByRole('link', { current: 'page' }),
    ).toHaveTextContent('Audit')
  })

  it("lists the person's sessions under Sessions, the ones that need them first, and opens each by its address", () => {
    const row = (
      key: string,
      title: string | null,
      status: string,
      kind = 'session',
    ) => ({
      key,
      kind,
      status,
      title,
      reference: key,
      to: '/sessions',
      search: { session: key },
    })
    state.groups = [
      { id: 'needsYou', rows: [row('s1', 'Fix login', 'needs-you')] },
      {
        id: 'working',
        rows: [row('s2', 'Refactor billing', 'working')],
      },
      { id: 'earlier', rows: [row('s3', null, 'done')] },
      // A handoff is Now's, a snoozed or archived session is out of the list.
      { id: 'snoozed', rows: [row('s4', 'Snoozed one', 'idle')] },
      { id: 'archived', rows: [row('s5', 'Archived one', 'done')] },
    ]
    state.groups[0].rows.push(
      row('h1', 'Handoff from Ana', 'needs-you', 'handoff'),
    )
    renderIntel(<Sidebar />)
    const nav = column()
    const sessionsLink = within(nav).getByRole('link', { name: 'Sessions' })
    const login = within(nav).getByRole('link', { name: /^Fix login/ })
    expect(login).toHaveAttribute('href', '/sessions?session=s1')
    const billing = within(nav).getByRole('link', { name: /^Refactor billing/ })
    const untitled = within(nav).getByRole('link', { name: /^Session s3/ })
    const files = within(nav).getByRole('link', { name: 'Files' })
    expect(isBefore(sessionsLink, login)).toBe(true)
    expect(isBefore(login, billing)).toBe(true)
    expect(isBefore(billing, untitled)).toBe(true)
    expect(isBefore(untitled, files)).toBe(true)
    expect(within(nav).queryByText('Handoff from Ana')).toBeNull()
    expect(within(nav).queryByText('Snoozed one')).toBeNull()
    expect(within(nav).queryByText('Archived one')).toBeNull()
  })

  it('shows at most eight sessions, however many the rail holds', () => {
    // The rail keeps up to eight rows in each of its groups; the sidebar is one short list.
    const rows = (group: string) =>
      Array.from({ length: 8 }, (_, i) => ({
        key: `${group}-${i}`,
        kind: 'session',
        status: 'working',
        title: `${group} ${i}`,
        reference: `${group}-${i}`,
        to: '/sessions',
        search: { session: `${group}-${i}` },
      }))
    state.groups = ['needsYou', 'working', 'earlier'].map((id) => ({
      id,
      rows: rows(id),
    }))
    renderIntel(<Sidebar />)
    const shown = within(column())
      .getAllByRole('link')
      .filter((a) => /^(needsYou|working|earlier) \d/.test(a.textContent ?? ''))
    expect(shown).toHaveLength(8)
    // The ones that need the person come first when the list is cut.
    expect(shown.every((a) => a.textContent?.startsWith('needsYou'))).toBe(true)
  })

  it('draws no session rows when the navigation is folded to its icons', () => {
    usePreferencesStore.setState({ navCollapsed: true })
    state.groups = [
      {
        id: 'working',
        rows: [
          {
            key: 's2',
            kind: 'session',
            status: 'working',
            title: 'Refactor billing',
            reference: 's2',
            to: '/sessions',
            search: { session: 's2' },
          },
        ],
      },
    ]
    renderIntel(<Sidebar />)
    expect(within(column()).queryByText('Refactor billing')).toBeNull()
  })

  it('lists every place once, in order, Settings before the administrative places and no group headers', () => {
    renderIntel(<Sidebar />)
    const nav = column()
    expect(within(nav).queryAllByRole('region')).toEqual([])
    expect(within(nav).queryAllByRole('heading')).toEqual([])
    expect(nav.querySelectorAll('h2')).toHaveLength(0)
    expect(names(nav)).toEqual(PLACES)
    expect(within(nav).queryByText('All areas')).toBeNull()
  })

  it('marks exactly the place the address belongs to', () => {
    state.pathname = '/providers'
    renderIntel(<Sidebar />)
    const current = within(column()).getAllByRole('link', { current: 'page' })
    expect(current.map((a) => a.textContent)).toEqual(['Tools'])
  })

  it('marks Settings on the edition section, and no link opens a Settings section', () => {
    state.pathname = '/settings'
    state.search = { section: 'edition' }
    renderIntel(<Sidebar />)
    expect(
      within(column()).getByRole('link', { current: 'page' }),
    ).toHaveTextContent('Settings')
    expect(column().querySelector('a[href*="section="]')).toBeNull()
  })

  it('opens Rules on the approval queue', () => {
    renderIntel(<Sidebar />)
    expect(
      within(column()).getByRole('link', { name: 'Rules' }),
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
      within(nav).getByRole('link', { name: /^Rules\s*,\s*3$/ }),
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
    expect(offered).toContain('Overview')
    expect(offered).toContain('Settings')
    // Sessions stays offered here through Channel administration, whose capability is not
    // known without a workspace (navigation keeps an unknown visible).
    expect(offered).not.toContain('Files')
    expect(offered).not.toContain('Usage')
    expect(offered).not.toContain('Rules')
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
      within(column()).getByRole('link', { name: 'Tools' }),
    ).toHaveAttribute('title', 'Tools')

    const expand = screen.getByRole('button', { name: 'Expand navigation' })
    expand.focus()
    await user.keyboard('{Enter}')
    expect(column()).not.toHaveAttribute('data-folded')
    expect(usePreferencesStore.getState().navCollapsed).toBe(false)
  })

  // The column is the drawer's list at any width: with no fold chosen it names its places on
  // every page, the work surface included; folding to icons is the person's own choice.
  it.each(['/sessions', '/work', '/communications'])(
    'with no fold chosen, names its places at %s',
    (path) => {
      usePreferencesStore.setState({ navCollapsed: null })
      state.pathname = path
      renderIntel(<Sidebar />)
      expect(column()).not.toHaveAttribute('data-folded')
    },
  )
})

describe('the narrow drawer', () => {
  it('holds the same places in the same order and closes on a choice', async () => {
    const user = userEvent.setup()
    const onOpenChange = vi.fn()
    renderIntel(<NavigationDrawer open onOpenChange={onOpenChange} />)
    const drawer = screen.getByRole('dialog', { name: 'Main navigation' })
    expect(names(drawer)).toEqual(PLACES)
    await user.click(within(drawer).getByRole('link', { name: 'Tools' }))
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
