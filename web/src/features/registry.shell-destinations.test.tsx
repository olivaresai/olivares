// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Every destination of the sidebar's journeys and of the phone bar comes from the
// registry: its id, its path, its icon and its permission. A destination written by hand
// in the shell would be a door that no census, no permission check and no route test sees.
//
// And a permitted leaf opens by direct link with no parent permission: an area is the
// union of the leaves the principal may open, never a gate of its own.
import { screen, within } from '@testing-library/react'
import type { ComponentProps } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { renderIntel } from '@/test/intel'

const routerState = vi.hoisted(() => ({
  pathname: '/',
  search: {} as Record<string, unknown>,
}))
vi.mock('@tanstack/react-router', () => ({
  useRouterState: ({
    select,
  }: {
    select?: (s: {
      location: { pathname: string; search: Record<string, unknown> }
    }) => unknown
  } = {}) =>
    select
      ? select({
          location: {
            pathname: routerState.pathname,
            search: routerState.search,
          },
        })
      : '',
  useNavigate: () => () => undefined,
  Link: ({
    children,
    to,
    activeProps,
    activeOptions,
    ...props
  }: ComponentProps<'a'> & {
    to?: string
    activeProps?: Record<string, string>
    activeOptions?: { exact?: boolean }
  }) => {
    void activeOptions
    const active = routerState.pathname === to
    // The real Link spreads the plain props first and the active props last
    // (@tanstack/react-router link.js), so an active link keeps its aria-current.
    return (
      <a href={to} {...props} {...(active ? activeProps : {})}>
        {children}
      </a>
    )
  },
}))

const canMock = vi.fn((_permission: string) => true)
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    can: (p: string) => canMock(p),
    grants: [],
    isSuperadmin: false,
    activeTenant: null,
    setActiveTenant: () => {},
  }),
}))

import { FEATURE_VIEWS, NAV_AREAS } from './registry'
import enNav from '@/lib/i18n/locales/en/nav.json'
import { authorizedSections, permissionGate } from './navigation/model'
import {
  JOURNEY_IDS,
  PHONE_BAR_IDS,
  SHELL_GROUPS,
  APPROVALS_SECTION,
  DESTINATION_VIEW_IDS,
  coveredByDestination,
  DESTINATION_MEMBERS,
  SETTINGS_DESTINATION,
  destinationOf,
  sectionAt,
  authorizedAreas,
  groupedDestinations,
  journeyDestinations,
  phoneBarDestinations,
} from '@/components/layout/shell-destinations'
import { DestinationSections } from '@/components/layout/destination-sections'
import { JourneyNav } from '@/components/layout/journey-nav'
import { PhoneBar } from '@/components/layout/phone-bar'
import { AreasTree } from '@/components/layout/sidebar'

afterEach(() => {
  routerState.pathname = '/'
  canMock.mockReset()
  canMock.mockReturnValue(true)
})

const viewOf = (id: string) => FEATURE_VIEWS.find((v) => v.id === id)
/** A sidebar entry names a view by its id, or a section of a view. */
const viewIdOf = (e: string | { view: string }) =>
  typeof e === 'string' ? e : e.view
const allowAll = permissionGate(() => true)

describe('the shell destinations come from the registry', () => {
  it('names only registry views that navigation may list', () => {
    for (const id of [...JOURNEY_IDS, ...PHONE_BAR_IDS]) {
      const view = viewOf(id)
      expect(view, id).toBeDefined()
      expect(view?.hideInNav, id).not.toBe(true)
      expect(view?.navigation.kind, id).not.toBe('detail')
    }
  })

  it('takes each path and icon from the registry entry, in the pinned order', () => {
    const journeys = journeyDestinations(allowAll)
    expect(journeys.map((d) => d.id)).toEqual([...JOURNEY_IDS])
    for (const d of [...journeys, ...phoneBarDestinations(allowAll)]) {
      const view = viewOf(d.id)
      expect(d.path).toBe(view?.path)
      expect(d.icon).toBe(view?.icon)
    }
  })

  it('renders the sidebar journeys as links to registry paths and nothing else', () => {
    renderIntel(<JourneyNav />)
    const nav = screen.getByRole('navigation', { name: 'Journeys' })
    const hrefs = within(nav)
      .getAllByRole('link')
      .map((a) => a.getAttribute('href'))
    expect(hrefs).toEqual(JOURNEY_IDS.map((id) => viewOf(id)?.path))
  })

  it('renders the phone bar links from registry paths, beside New and More', () => {
    renderIntel(<PhoneBar />)
    const bar = screen.getByRole('navigation', { name: 'Main' })
    const hrefs = within(bar)
      .getAllByRole('link')
      .map((a) => a.getAttribute('href'))
    expect(hrefs).toEqual(PHONE_BAR_IDS.map((id) => viewOf(id)?.path))
    expect(
      within(bar).getByRole('button', { name: 'New session' }),
    ).toBeInTheDocument()
    expect(
      within(bar).getByRole('button', { name: 'More: all areas' }),
    ).toBeInTheDocument()
  })

  it('hides a journey the principal may not open, and never shows a hand-made one', () => {
    canMock.mockImplementation((p) => p !== viewOf('deploy')?.permission)
    renderIntel(<JourneyNav />)
    const nav = screen.getByRole('navigation', { name: 'Journeys' })
    const hrefs = within(nav)
      .getAllByRole('link')
      .map((a) => a.getAttribute('href'))
    expect(hrefs).not.toContain('/deploy')
    expect(hrefs).toContain('/')
  })
})

describe('a permitted leaf opens by direct link with no parent permission', () => {
  const work = viewOf('work')!

  it('offers the area of the one permitted leaf, and the leaf itself', () => {
    // Only the leaf's own permission: nothing about its area, its section or a hub.
    canMock.mockImplementation((p) => p === work.permission)
    const onlyWork = permissionGate((p) => p === work.permission)
    // The areas open to anyone (a leaf that needs no permission), and the leaf's own.
    const open = new Set(
      NAV_AREAS.filter(
        (a) =>
          authorizedSections(
            a.id,
            permissionGate(() => false),
          ).length > 0,
      ).map((a) => a.id),
    )
    expect(open.has('work-communications')).toBe(false)
    expect(authorizedAreas(onlyWork).map((a) => a.id)).toEqual(
      NAV_AREAS.filter(
        (a) => a.id === 'work-communications' || open.has(a.id),
      ).map((a) => a.id),
    )
    // Inside its area, the leaf is the only door: nothing about the area opened it.
    expect(
      authorizedSections('work-communications', onlyWork).flatMap((s) =>
        s.views.map((v) => v.id),
      ),
    ).toEqual(['work'])

    routerState.pathname = work.path
    renderIntel(<AreasTree />)
    const links = screen.getAllByRole('link').map((a) => a.getAttribute('href'))
    expect(links).toContain(work.path)
    expect(links).toContain('/areas/work-communications')
    // No other area is offered without a leaf of its own.
    for (const a of NAV_AREAS.filter(
      (x) => x.id !== 'work-communications' && !open.has(x.id),
    ))
      expect(links).not.toContain(a.path)
    // The direct link is the current page.
    expect(
      screen
        .getAllByRole('link')
        .find((a) => a.getAttribute('href') === work.path),
    ).toHaveAttribute('aria-current', 'page')
  })

  it('keeps the destinations that need no permission, and the one the grant opens', () => {
    canMock.mockImplementation((p) => p === work.permission)
    renderIntel(<JourneyNav />)
    const nav = screen.getByRole('navigation', { name: 'Journeys' })
    const hrefs = within(nav)
      .getAllByRole('link')
      .map((a) => a.getAttribute('href'))
    // Work is itself a sidebar destination since the console remake: the leaf's own
    // grant opens it there too, and nothing else.
    expect(hrefs).toEqual(
      JOURNEY_IDS.map((id) => viewOf(id)!)
        .filter((v) => !v.permission || v.permission === work.permission)
        .map((v) => v.path),
    )
  })
})

describe('the sidebar groups (console remake 26.10)', () => {
  it('draws the work section, then Manage, Integrate and Secure, each labelled, in order', () => {
    renderIntel(<JourneyNav />)
    const nav = screen.getByRole('navigation', { name: 'Journeys' })
    const groups = within(nav).getAllByRole('group')
    expect(groups.map((g) => g.getAttribute('data-shell-group'))).toEqual(
      SHELL_GROUPS.map((g) => g.id),
    )
    expect(groups.map((g) => g.getAttribute('aria-label') ?? '')).toEqual([
      'Work',
      '',
      '',
      '',
    ])
    for (const [i, name] of ['Manage', 'Integrate', 'Secure'].entries())
      expect(within(groups[i + 1]).getByText(name)).toBeInTheDocument()
    // Each group links exactly its own registry views, in their pinned order.
    for (const [i, g] of SHELL_GROUPS.entries())
      expect(
        within(groups[i])
          .getAllByRole('link')
          .map((a) => a.getAttribute('href')),
      ).toEqual(
        (g.ids as readonly (string | { view: string })[]).map(
          (e) => viewOf(viewIdOf(e))?.path,
        ),
      )
  })

  it('does not draw a group whose destinations the principal may not open', () => {
    const integrate = SHELL_GROUPS.find((g) => g.id === 'integrate')!
    const denied = new Set(
      (integrate.ids as readonly (string | { view: string })[]).map(
        (e) => viewOf(viewIdOf(e))?.permission,
      ),
    )
    canMock.mockImplementation((p) => !denied.has(p))
    renderIntel(<JourneyNav />)
    const nav = screen.getByRole('navigation', { name: 'Journeys' })
    expect(
      within(nav)
        .getAllByRole('group')
        .map((g) => g.getAttribute('data-shell-group')),
    ).toEqual(['work', 'manage', 'secure'])
    expect(within(nav).queryByText('Integrate')).toBeNull()
  })
})

describe('the Approvals entry (console remake slice 4)', () => {
  it('is the approval queue section of Permissions, offered only with the queue permission', () => {
    renderIntel(<JourneyNav />)
    const entry = document.querySelector('[data-journey="approvals"]')
    expect(entry).not.toBeNull()
    expect(entry?.getAttribute('href')).toBe(viewOf('permissions')?.path)
    expect(entry).toHaveTextContent('Approvals')
  })

  it('is not offered without governance:approval:read, even with the view', () => {
    canMock.mockImplementation((p) => p !== APPROVALS_SECTION.requires)
    renderIntel(<JourneyNav />)
    expect(document.querySelector('[data-journey="approvals"]')).toBeNull()
  })

  it('draws a count that asks for a person in the warning role', () => {
    renderIntel(
      <JourneyNav
        counts={{ approvals: '3' }}
        attention={new Set(['approvals'])}
      />,
    )
    const entry = document.querySelector('[data-journey="approvals"]')!
    const badge = entry.querySelector('span.bg-warning-soft')
    expect(badge).not.toBeNull()
    expect(badge).toHaveTextContent('3')
  })
})

describe('the top bar names a destination by its own label', () => {
  it('gives every view that is itself a destination a label, and leaves section views out', () => {
    const journeys = enNav.shell.journeys as Record<string, string>
    for (const id of DESTINATION_VIEW_IDS)
      expect(journeys[id], `nav:shell.journeys.${id}`).toBeTruthy()
    // Permissions is reached through its Approvals section: it keeps its registry trail
    // instead of asking for a journey label it does not have.
    expect(DESTINATION_VIEW_IDS).not.toContain(APPROVALS_SECTION.view)
    expect(journeys[APPROVALS_SECTION.key]).toBeTruthy()
  })
})

describe('a section destination covers its own address only', () => {
  it('is the view with the section search, and not the view on another tab', () => {
    expect(sectionAt('permissions', { tab: 'approvals' })).toBe(
      APPROVALS_SECTION,
    )
    expect(sectionAt('permissions', { tab: 'policies' })).toBeNull()
    expect(sectionAt('permissions', {})).toBeNull()
    expect(sectionAt('killswitch', { tab: 'approvals' })).toBeNull()
    // Covered either way: the section, or Permissions as a member of Identity & access.
    expect(coveredByDestination('permissions', { tab: 'approvals' })).toBe(true)
    expect(coveredByDestination('killswitch', { tab: 'approvals' })).toBe(false)
  })
})

describe('Policies spans several views (console remake slice 6)', () => {
  afterEach(() => {
    routerState.pathname = '/'
    canMock.mockImplementation(() => true)
  })

  it('names registry views the sidebar may list, each with its own label, led by a sidebar destination', () => {
    const items = enNav.items as Record<string, string>
    for (const [destination, members] of Object.entries(DESTINATION_MEMBERS)) {
      // The footer's Settings is not a registry view and not among its own members.
      if (destination !== SETTINGS_DESTINATION) {
        expect(members[0]).toBe(destination)
        expect(DESTINATION_VIEW_IDS).toContain(destination)
      }
      for (const id of members) {
        const view = viewOf(id)
        expect(view, id).toBeDefined()
        expect(view!.navigation.kind, id).not.toBe('detail')
        expect(items[id], `nav:items.${id}`).toBeTruthy()
        expect(destinationOf(id)).toBe(destination)
      }
    }
    expect(destinationOf('home')).toBeNull()
    expect(coveredByDestination('routinePolicies', {})).toBe(true)
  })

  it('opens MCP servers on the page that adds, tests and enables them; the discovered catalog is not a second MCP page (HU 025)', () => {
    const integrate = groupedDestinations(allowAll).find(
      (g) => g.id === 'integrate',
    )!
    const mcp = integrate.destinations.find((d) => d.key === 'mcpServers')!
    expect(mcp.path).toBe(viewOf('mcpServers')?.path)
    expect(integrate.destinations.map((d) => d.key)).not.toContain(
      'capabilities',
    )
    expect(destinationOf('capabilities')).toBeNull()
  })

  it('puts a view in at most one destination, and never another sidebar destination inside one', () => {
    const seen = new Map<string, string>()
    for (const [destination, members] of Object.entries(DESTINATION_MEMBERS)) {
      for (const id of members) {
        expect(seen.get(id), `${id} in ${destination}`).toBeUndefined()
        seen.set(id, destination)
        if (id !== destination)
          expect(
            DESTINATION_VIEW_IDS,
            `${id} is its own destination`,
          ).not.toContain(id)
      }
    }
  })

  it('keeps Policies current on a member page whose path is not under its own', () => {
    routerState.pathname = '/routine-policies'
    renderIntel(<JourneyNav />)
    const policies = document.querySelector('[data-journey="claudePolicy"]')!
    expect(policies.getAttribute('aria-current')).toBe('page')
    const audit = document.querySelector('[data-journey="audit"]')!
    expect(audit.getAttribute('aria-current')).toBeNull()
  })

  it('offers the members this principal may open above a member page, marking the current one', () => {
    routerState.pathname = '/routine-policies'
    const hidden = viewOf('redteam')!.permission
    canMock.mockImplementation((p) => p !== hidden)
    renderIntel(<DestinationSections />)
    const strip = screen.getByRole('navigation', { name: 'Policies' })
    const links = within(strip).getAllByRole('link')
    expect(links.map((a) => a.getAttribute('href'))).toEqual(
      DESTINATION_MEMBERS.claudePolicy
        .map((id) => viewOf(id)!)
        .filter((v) => v.permission !== hidden)
        .map((v) => v.path),
    )
    expect(links).toHaveLength(DESTINATION_MEMBERS.claudePolicy.length - 1)
    expect(
      within(strip).getByRole('link', { name: 'Routine policies' }),
    ).toHaveAttribute('aria-current', 'page')
  })

  it('draws no strip outside a multi-view destination, or with one permitted member', () => {
    routerState.pathname = '/audit'
    const { unmount } = renderIntel(<DestinationSections />)
    expect(screen.queryByRole('navigation', { name: 'Policies' })).toBeNull()
    unmount()
    routerState.pathname = '/routine-policies'
    const only = viewOf('routinePolicies')!.permission
    canMock.mockImplementation((p) => p === only)
    renderIntel(<DestinationSections />)
    expect(screen.queryByRole('navigation', { name: 'Policies' })).toBeNull()
  })
})

describe('slice 8: every destination owns its pages, and a section wins over membership', () => {
  afterEach(() => {
    routerState.pathname = '/'
    routerState.search = {}
    canMock.mockImplementation(() => true)
  })

  it('marks Approvals alone on the queue address, with no member row', () => {
    routerState.pathname = '/permissions'
    routerState.search = { tab: 'approvals' }
    renderIntel(<JourneyNav />)
    expect(
      document
        .querySelector('[data-journey="identity"]')!
        .getAttribute('aria-current'),
    ).toBeNull()
    renderIntel(<DestinationSections />)
    expect(
      document.querySelector('[data-slot="destination-sections"]'),
    ).toBeNull()
  })

  it('marks Identity & access on Permissions itself, with its member row', () => {
    routerState.pathname = '/permissions'
    routerState.search = { tab: 'policies' }
    renderIntel(<JourneyNav />)
    expect(
      document
        .querySelector('[data-journey="identity"]')!
        .getAttribute('aria-current'),
    ).toBe('page')
    renderIntel(<DestinationSections />)
    const strip = screen.getByRole('navigation', { name: 'Identity & access' })
    expect(
      within(strip).getByRole('link', { name: 'Permissions' }),
    ).toHaveAttribute('aria-current', 'page')
  })

  it("leads the footer Settings' row with Settings itself", () => {
    routerState.pathname = '/backups'
    renderIntel(<DestinationSections />)
    const strip = screen.getByRole('navigation', { name: 'Settings' })
    const links = within(strip).getAllByRole('link')
    expect(links[0]).toHaveAttribute('href', '/settings')
    expect(
      within(strip).getByRole('link', { name: 'Backups' }),
    ).toHaveAttribute('aria-current', 'page')
  })

  it("names a destination's own view in English as the sidebar names the destination", () => {
    // The member row, the command menu and the sidebar must not call one page by two names.
    const items = enNav.items as Record<string, string>
    const journeys = enNav.shell.journeys as Record<string, string>
    expect(items.identity).toBe(journeys.identity)
  })

  it('places privileged session recordings with the evidence, under Audit', () => {
    expect(destinationOf('recordings')).toBe('audit')
  })

  it('draws no row over a work surface, and keeps Sessions current on Operate sessions', () => {
    routerState.pathname = '/agentops'
    renderIntel(<DestinationSections />)
    expect(
      document.querySelector('[data-slot="destination-sections"]'),
    ).toBeNull()
    renderIntel(<JourneyNav />)
    expect(
      document
        .querySelector('[data-journey="sessions"]')!
        .getAttribute('aria-current'),
    ).toBe('page')
  })
})
