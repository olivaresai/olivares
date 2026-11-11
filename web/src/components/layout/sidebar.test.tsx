// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE ONE LEFT NAVIGATION AS A PERSON USES IT: the person's own sessions under Sessions, then
// Files and Tools, and Settings at the foot with the administrative places one level under it
// (its More toggle), no group headers; one current place, counts read from current state, a
// fold that works from the mouse and the keyboard, and the same items in the same order in
// the narrow drawer.
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useRef, useState, type ComponentProps } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel } from '@/test/intel'
import { stubViewportWidth } from '@/test/viewport'

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
vi.mock('./settings-section-navigation', () => ({
  SettingsSectionNavigation: () => <div>Settings sections</div>,
}))
vi.mock('./user-menu', () => ({ UserMenu: () => null }))
vi.mock('./scope-switcher', () => ({ ScopeSwitcher: () => null }))
vi.mock('./new-session', () => ({ useNewSession: () => () => {} }))
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
import { NavigationDrawer, PhoneNavigation, Sidebar } from './sidebar'

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
  'Overview',
  'Sessions',
  'Files',
  'Tools',
  'Rules',
  'Usage',
  'Audit',
  'People',
  'Security',
  'Operations',
  'Settings',
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
  it('shows the approved places in order without hiding administration behind More', () => {
    usePreferencesStore.setState({ navAll: false })
    renderIntel(<Sidebar />)
    expect(names(column())).toEqual([
      'Overview',
      'Sessions',
      'Files',
      'Tools',
      'Rules',
      'Usage',
      'Audit',
      'People',
      'Security',
      'Operations',
      'Settings',
    ])
    expect(within(column()).queryByRole('button', { name: 'More' })).toBeNull()
  })

  it("lists the person's sessions under Sessions, the ones that need them first, then the working ones, and opens each by its address", () => {
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
        rows: [
          row('s2', 'Refactor billing', 'working'),
          row('s3', null, 'working'),
        ],
      },
      // An idle session from earlier is the list's, not the navigation's.
      { id: 'earlier', rows: [row('s6', 'Old draft', 'done')] },
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
    expect(within(nav).queryByText('Old draft')).toBeNull()
    expect(within(nav).queryByText('Handoff from Ana')).toBeNull()
    expect(within(nav).queryByText('Snoozed one')).toBeNull()
    expect(within(nav).queryByText('Archived one')).toBeNull()
  })

  it('marks the open session as the current page, by its weight as well as its fill', () => {
    const row = (key: string, title: string) => ({
      key,
      kind: 'session',
      status: 'working',
      title,
      reference: key,
      to: '/sessions',
      search: { session: key },
    })
    state.pathname = '/sessions'
    state.search = { session: 's2' }
    state.groups = [
      {
        id: 'working',
        rows: [row('s1', 'Fix login'), row('s2', 'Refactor billing')],
      },
    ]
    renderIntel(<Sidebar />)
    const nav = column()
    const open = within(nav).getByRole('link', { name: /^Refactor billing/ })
    expect(open).toHaveAttribute('aria-current', 'page')
    expect(open).toHaveClass('aria-[current=page]:font-semibold')
    expect(
      within(nav).getByRole('link', { name: /^Fix login/ }),
    ).not.toHaveAttribute('aria-current')
  })

  it('shows at most five sessions, however many the rail holds', () => {
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
    expect(shown).toHaveLength(5)
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

  it('lists every place once, in order, with Settings at the foot and governance and administration groups', () => {
    renderIntel(<Sidebar />)
    const nav = column()
    expect(within(nav).queryAllByRole('region')).toEqual([])
    expect(within(nav).getAllByRole('heading')).toHaveLength(2)
    expect(nav.querySelectorAll('h2')).toHaveLength(2)
    expect(names(nav)).toEqual(PLACES)
    expect(within(nav).queryByText('All areas')).toBeNull()
  })

  it('marks exactly the place the address belongs to', () => {
    state.pathname = '/providers'
    renderIntel(<Sidebar />)
    const current = within(column()).getAllByRole('link', { current: 'page' })
    expect(current.map((a) => a.textContent)).toEqual(['Tools'])
    // The fill is light on purpose: the current place is also bold, in full ink.
    expect(current[0]).toHaveClass(
      'aria-[current=page]:bg-active',
      'aria-[current=page]:font-bold',
      'aria-[current=page]:text-text',
    )
    // Pressing paints the pressed layer; nothing scales.
    expect(current[0].className).toContain('active:bg-[var(--pressed)]')
    expect(current[0].className).not.toMatch(/scale/)
  })

  it('signs its head with the mark and the traced name, never live text, on the frame with no rule', () => {
    renderIntel(<Sidebar />)
    const nav = column()
    expect(nav).not.toHaveClass('border-r')
    const head = nav.querySelector('[data-slot="sidebar-head"]') as HTMLElement
    expect(head).toHaveClass('h-10')
    const name = within(head).getByRole('img', { name: 'Olivares AI' })
    expect(name.tagName).toBe('svg')
    expect(within(nav).queryByText('Olivares AI')).toBeNull()
    expect(head.querySelector('svg[aria-hidden="true"]')).toHaveClass('size-6')
  })

  it('names its group labels in the 12 px bold step', () => {
    renderIntel(<Sidebar />)
    for (const h2 of column().querySelectorAll('h2'))
      expect(h2).toHaveClass('text-overline', 'font-bold', 'text-text-3')
  })

  it('replaces the places with Settings sections on the edition section', () => {
    state.pathname = '/settings'
    state.search = { section: 'edition' }
    renderIntel(<Sidebar />)
    expect(within(column()).getByText('Settings sections')).toBeVisible()
    expect(
      within(column()).queryByRole('link', { name: 'Sessions' }),
    ).toBeNull()
    const back = within(column()).getByRole('link', { name: 'Settings' })
    expect(back).toHaveAttribute('href', '/now')
    expect(back).toHaveClass('h-10', 'text-body-l', 'font-bold')
    // The settings list has its own search: no palette search and no scope here.
    expect(
      within(column()).queryByRole('button', { name: /Search and commands/ }),
    ).toBeNull()
    expect(column().querySelector('[data-slot="sidebar-scope"]')).toBeNull()
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
    // Folded by choice, the rail still opens with the mark, at 26 px.
    expect(
      column().querySelector(
        '[data-slot="sidebar-head"] svg[aria-hidden="true"]',
      ),
    ).toHaveClass('size-[26px]')
    // Folded, every place still has its name, and so does the search icon.
    expect(
      within(column()).getByRole('link', { name: 'Tools' }),
    ).toHaveAttribute('title', 'Tools')
    expect(
      within(column()).getByRole('button', { name: /Search and commands/ }),
    ).toHaveAttribute('title', 'Search and commands')

    const expand = screen.getByRole('button', { name: 'Expand navigation' })
    expand.focus()
    await user.keyboard('{Enter}')
    expect(column()).not.toHaveAttribute('data-folded')
    expect(usePreferencesStore.getState().navCollapsed).toBe(false)
  })

  it('offers all the sessions by their number under the ones it lists', () => {
    state.open = 6
    state.groups = [
      {
        id: 'needsYou',
        rows: [
          {
            key: 'a',
            kind: 'session',
            status: 'needs_you',
            title: 'Review the login fix',
            reference: 'a',
            to: '/sessions',
            search: { session: 'a' },
          },
        ],
      },
    ]
    renderIntel(<Sidebar />)
    expect(
      within(column()).getByRole('link', { name: 'See all 6 sessions' }),
    ).toHaveAttribute('href', '/sessions')
  })

  it('is the rail between 761 and 960 px, with each place named by its tooltip', () => {
    const original = window.matchMedia
    stubViewportWidth(900)
    try {
      usePreferencesStore.setState({ navCollapsed: null })
      renderIntel(<Sidebar />)
      expect(column()).toHaveAttribute('data-folded', 'true')
      expect(
        within(column()).getByRole('link', { name: 'Tools' }),
      ).toHaveAttribute('title', 'Tools')
      // No fold to offer at a width that is always the rail.
      expect(
        screen.queryByRole('button', { name: 'Expand navigation' }),
      ).toBeNull()
    } finally {
      window.matchMedia = original
    }
  })

  it('hints the palette with the chord of the platform', () => {
    Object.defineProperty(window.navigator, 'platform', {
      value: 'MacIntel',
      configurable: true,
    })
    try {
      renderIntel(<Sidebar />)
      // The name starts with what the button shows (WCAG 2.5.3), then the name it had.
      const search = within(column()).getByRole('button', {
        name: 'Search or go to…, Search and commands',
      })
      expect(search).toHaveTextContent(/^Search or go to…$/)
      // Its words are on it: no tooltip says the end of its name a second time.
      expect(search).not.toHaveAttribute('title')
      expect(search).toHaveClass('border-0', 'text-body', 'text-text-2')
      expect(search.querySelector('kbd')).toHaveAttribute('data-keys', '⌘K')
    } finally {
      delete (window.navigator as { platform?: string }).platform
    }
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

  // WCAG 2.4.3: the phone bar's More opens the drawer, so closing it puts the keyboard
  // back on More, not on the page body. Wired as app-layout.tsx wires the two.
  it('returns focus to More when it closes', async () => {
    function Phone() {
      const [open, setOpen] = useState(false)
      const more = useRef<HTMLButtonElement>(null)
      return (
        <>
          <NavigationDrawer
            open={open}
            onOpenChange={setOpen}
            returnFocus={more}
          />
          <PhoneNavigation onMore={() => setOpen(true)} moreRef={more} />
        </>
      )
    }
    const user = userEvent.setup()
    renderIntel(<Phone />)
    const more = screen.getByRole('button', { name: 'More' })
    more.focus()
    await user.keyboard('{Enter}')
    expect(
      screen.getByRole('dialog', { name: 'Main navigation' }),
    ).toBeVisible()
    await user.keyboard('{Escape}')
    expect(screen.queryByRole('dialog')).toBeNull()
    await waitFor(() => expect(more).toHaveFocus())
  })
})

describe('the phone bar', () => {
  const bar = () =>
    document.querySelector('[data-slot="phone-navigation"]') as HTMLElement

  it('is five equal columns on the frame, the current place marked and bold, More opening the drawer', async () => {
    const user = userEvent.setup()
    const onMore = vi.fn()
    state.pathname = '/sessions'
    renderIntel(<PhoneNavigation onMore={onMore} />)
    expect(bar()).toHaveClass('grid', 'grid-cols-5', 'bg-frame')
    const sessions = within(bar()).getByRole('link', { name: 'Sessions' })
    expect(sessions).toHaveAttribute('aria-current', 'page')
    expect(sessions).toHaveClass(
      'text-metadata',
      'aria-[current=page]:font-bold',
      'aria-[current=page]:text-text',
    )
    expect(
      within(bar()).getByRole('link', { name: 'Overview' }),
    ).not.toHaveAttribute('aria-current')
    // 44 px targets.
    for (const target of within(bar()).getAllByRole('link'))
      expect(target).toHaveClass('min-h-11')
    await user.click(within(bar()).getByRole('button', { name: 'More' }))
    expect(onMore).toHaveBeenCalledOnce()
  })
})
