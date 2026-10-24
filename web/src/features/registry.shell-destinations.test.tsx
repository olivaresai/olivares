// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Every place of the top bar and of the phone bar comes from the registry: its id, its
// path, its icon and its permission. A place written by hand in the shell would be a door
// that no census, no permission check and no route test sees. Where a page sits is its own
// entry's area and section: the top bar marks the place of that section, and the row
// above the page lists that section.
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
    search,
    activeProps,
    activeOptions,
    ...props
  }: ComponentProps<'a'> & {
    to?: string
    search?: Record<string, string>
    activeProps?: Record<string, string>
    activeOptions?: { exact?: boolean }
  }) => {
    void activeOptions
    const active = routerState.pathname === to
    // The real Link spreads the plain props first and the active props last
    // (@tanstack/react-router link.js), so an active link keeps its aria-current.
    // shell-current.router.test.tsx covers the real router's own marking.
    const href = search ? `${to}?${new URLSearchParams(search)}` : to
    return (
      <a href={href} {...props} {...(active ? activeProps : {})}>
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
import { BUSINESS_BUILD } from '@/features/business-build'
import {
  authorizedSections,
  permissionGate,
  resolveLocation,
} from './navigation/model'
import {
  APPROVALS_SECTION,
  DESTINATION_VIEW_IDS,
  SHELL_DESTINATIONS,
  authorizedAreas,
  currentDestination,
  journeyDestinations,
  phoneBarDestinations,
  sectionAt,
} from '@/components/layout/shell-destinations'
import { DestinationSections } from '@/components/layout/destination-sections'
import { PlacesNav } from '@/components/layout/places-nav'
import { PhoneBar } from '@/components/layout/phone-bar'
import { AreasTree } from '@/components/layout/sidebar'

afterEach(() => {
  routerState.pathname = '/'
  routerState.search = {}
  canMock.mockReset()
  canMock.mockReturnValue(true)
})

const viewOf = (id: string) => FEATURE_VIEWS.find((v) => v.id === id)
/** A place names a view by its id, or a section of a view. */
const viewIdOf = (e: string | { view: string }) =>
  typeof e === 'string' ? e : e.view
/** The places whose view this build registers: Organization is a Business page, which a
 * Community build does not hold (shellDestinations offers only registered views). */
const DESTINATION_IDS = SHELL_DESTINATIONS.map(viewIdOf).filter((id) =>
  viewOf(id),
)
const REGISTERED = SHELL_DESTINATIONS.filter((e) => viewOf(viewIdOf(e)))
/** The address a place opens: its view's path, and a section's search. */
const hrefOf = (
  e: string | { view: string; search?: Record<string, string> },
) =>
  typeof e === 'string'
    ? viewOf(e)?.path
    : `${viewOf(e.view)?.path}?${new URLSearchParams(e.search)}`
const allowAll = permissionGate(() => true)
const currentAt = (
  path: string,
  search: Record<string, unknown> = {},
  gate = allowAll,
) => currentDestination(resolveLocation(path), search, gate)
/** A principal who holds every permission but these. */
const allBut = (...denied: (string | undefined)[]) =>
  permissionGate((p) => !denied.includes(p))

describe('the shell destinations come from the registry', () => {
  it('names only registry views that navigation may list, and Business its own place', () => {
    // Every place but Organization is a Community view; Organization ships with the
    // Business build and is never invented by the shell.
    expect(
      SHELL_DESTINATIONS.map(viewIdOf).filter((id) => !viewOf(id)),
    ).toEqual(viewOf('organization') ? [] : ['organization'])
    for (const id of DESTINATION_IDS) {
      const view = viewOf(id)
      expect(view, id).toBeDefined()
      expect(view?.hideInNav, id).not.toBe(true)
      expect(view?.navigation.kind, id).not.toBe('detail')
    }
  })

  it('takes each path and icon from the registry entry, in the pinned order', () => {
    const journeys = journeyDestinations(allowAll)
    expect(journeys.map((d) => d.id)).toEqual(DESTINATION_IDS)
    for (const d of [...journeys, ...phoneBarDestinations(allowAll)]) {
      const view = viewOf(d.id)
      expect(d.path).toBe(view?.path)
      expect(d.icon).toBe(view?.icon)
    }
  })

  it('renders the places as links to registry paths and nothing else', () => {
    renderIntel(<PlacesNav />)
    const nav = screen.getByRole('navigation', { name: 'Journeys' })
    const hrefs = within(nav)
      .getAllByRole('link')
      .map((a) => a.getAttribute('href'))
    expect(hrefs).toEqual(REGISTERED.map(hrefOf))
  })

  it('renders the first three destinations in the phone bar, beside New and More', () => {
    renderIntel(<PhoneBar />)
    const bar = screen.getByRole('navigation', { name: 'Main' })
    const hrefs = within(bar)
      .getAllByRole('link')
      .map((a) => a.getAttribute('href'))
    expect(hrefs).toEqual(['/', '/sessions', '/agent-tools'])
    expect(
      within(bar).getByRole('button', { name: 'New session' }),
    ).toBeInTheDocument()
    expect(
      within(bar).getByRole('button', { name: 'More: all areas' }),
    ).toBeInTheDocument()
  })

  it('gives the phone bar the first three places this principal may open', () => {
    // Without system:admin there is no Tools: the third link is the next place.
    const denied = viewOf('agent-tools')!.permission
    canMock.mockImplementation((p) => p !== denied)
    renderIntel(<PhoneBar />)
    const bar = screen.getByRole('navigation', { name: 'Main' })
    expect(
      within(bar)
        .getAllByRole('link')
        .map((a) => a.getAttribute('href')),
    ).toEqual(['/', '/sessions', '/permissions?tab=approvals'])
    expect(
      journeyDestinations(allBut(denied))
        .slice(0, 3)
        .map((d) => d.key),
    ).toEqual(['home', 'sessions', 'rules'])
  })

  it('marks the phone link of the destination current on the page', () => {
    routerState.pathname = '/providers'
    renderIntel(<PhoneBar />)
    const bar = screen.getByRole('navigation', { name: 'Main' })
    expect(
      [...bar.querySelectorAll('[aria-current="page"]')].map((a) =>
        a.getAttribute('href'),
      ),
    ).toEqual(['/agent-tools'])
  })

  it('hides a destination the principal may not open, and never shows a hand-made one', () => {
    canMock.mockImplementation(
      (p) => p !== viewOf('gitPublication')?.permission,
    )
    renderIntel(<PlacesNav />)
    const nav = screen.getByRole('navigation', { name: 'Journeys' })
    const hrefs = within(nav)
      .getAllByRole('link')
      .map((a) => a.getAttribute('href'))
    expect(hrefs).not.toContain('/git-publication')
    expect(hrefs).toContain('/')
  })
})

describe('a permitted leaf opens by direct link with no parent permission', () => {
  const work = viewOf('work')!

  it('offers the area of the one permitted leaf, and the leaf itself', () => {
    // Only the leaf's own permission: nothing about its area or its section.
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
    // No other area is offered without a leaf of its own. A leaf that asks the engine stays
    // offered while the answer is unknown (G1-B): a Business console has one in System.
    for (const a of NAV_AREAS.filter(
      (x) =>
        x.id !== 'work-communications' &&
        !open.has(x.id) &&
        !(BUSINESS_BUILD && x.id === 'system'),
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
    renderIntel(<PlacesNav />)
    const nav = screen.getByRole('navigation', { name: 'Journeys' })
    const hrefs = within(nav)
      .getAllByRole('link')
      .map((a) => a.getAttribute('href'))
    // The leaf's own grant opens no place but those that need no permission.
    expect(hrefs).toEqual(
      DESTINATION_IDS.map((id) => viewOf(id)!)
        .filter((v) => !v.permission || v.permission === work.permission)
        .map((v) => v.path),
    )
  })
})

describe('the top bar names the places, with no grouping of its own', () => {
  it('reads Now, Sessions, Tools, Rules and Ledger', () => {
    renderIntel(<PlacesNav />)
    const nav = screen.getByRole('navigation', { name: 'Journeys' })
    expect(
      within(nav)
        .getAllByRole('link')
        .map((a) => a.getAttribute('title')),
    ).toEqual(['Now', 'Sessions', 'Tools', 'Rules', 'Ledger'])
    // One list: the place of each page is its area, named by All areas, not a heading here.
    expect(within(nav).queryAllByRole('group')).toEqual([])
  })

  it('marks exactly one place on each page, Estate on the overview only', () => {
    for (const [path, key] of [
      ['/', 'home'],
      ['/sessions', 'sessions'],
      ['/providers', 'agent-tools'],
      ['/audit', 'audit'],
      ['/evals', 'audit'],
    ] as const) {
      routerState.pathname = path
      const { unmount } = renderIntel(<PlacesNav />)
      expect(
        [...document.querySelectorAll('[data-place][aria-current="page"]')].map(
          (a) => a.getAttribute('data-place'),
        ),
        path,
      ).toEqual([key])
      unmount()
    }
  })

  it('draws the current place as the neutral tone step, never orange', () => {
    routerState.pathname = '/sessions'
    renderIntel(<PlacesNav />)
    const here = document.querySelector('[data-place="sessions"]')!
    expect(here).toHaveAttribute('aria-current', 'page')
    expect(here.className).toContain('aria-[current=page]:bg-active')
    expect(here.className).not.toMatch(/accent|orange|shadow|glow|ring-accent/)
  })

  it('folds the places to icons on the room the bar gives them, the current one last', () => {
    routerState.pathname = '/sessions'
    renderIntel(<PlacesNav />)
    const label = (key: string) =>
      document.querySelector(`[data-place="${key}"] > span`)!
    // The places fold on their measured width (#2764), not on a fixed threshold: the fold
    // states are pinned in components/layout/places-nav.fold.test.tsx. Unmeasured (no
    // layout here), every label shows. A visible label never carries the sr-only token: the
    // page's overlap scan must keep seeing it (the token would exempt it from the scan);
    // only a folded, hidden label gets it.
    for (const key of ['home', 'sessions', 'agent-tools', 'rules', 'audit'])
      expect(label(key), key).not.toHaveClass('sr-only')
    // Folded, a place keeps its name for a screen reader and in its tooltip.
    expect(screen.getByRole('link', { name: 'Tools' })).toHaveAttribute(
      'title',
      'Tools',
    )
  })
})

describe('a page in an area without a place', () => {
  it('marks no place', () => {
    routerState.pathname = '/models'
    renderIntel(<PlacesNav />)
    expect(
      document.querySelector('[data-place][aria-current="page"]'),
    ).toBeNull()
  })
})

describe('the Rules place: the approval queue (console remake slice 4)', () => {
  it('is the approval queue section of Permissions, offered only with the queue permission', () => {
    renderIntel(<PlacesNav />)
    const entry = document.querySelector('[data-place="rules"]')
    expect(entry).not.toBeNull()
    expect(entry?.getAttribute('href')).toBe('/permissions?tab=approvals')
    expect(entry).toHaveTextContent('Rules')
  })

  it('is not offered without governance:approval:read, even with the view', () => {
    canMock.mockImplementation((p) => p !== APPROVALS_SECTION.requires)
    renderIntel(<PlacesNav />)
    expect(document.querySelector('[data-place="rules"]')).toBeNull()
  })

  it('draws a count that asks for a person in the warning role', () => {
    renderIntel(
      <PlacesNav counts={{ rules: '3' }} attention={new Set(['rules'])} />,
    )
    const entry = document.querySelector('[data-place="rules"]')!
    const badge = entry.querySelector('[data-slot="count-badge"]')
    expect(badge).not.toBeNull()
    expect(badge).toHaveClass('bg-warn-soft', 'text-warn')
    expect(badge).toHaveTextContent('3')
  })
})

describe('the top bar names a place by its own label', () => {
  it('gives every view that is itself a place a label, and leaves section views out', () => {
    const journeys = enNav.shell.journeys as Record<string, string>
    for (const id of DESTINATION_VIEW_IDS)
      expect(journeys[id], `nav:shell.journeys.${id}`).toBeTruthy()
    // Permissions is reached through its approval queue section: Rules is that section's
    // label, not the view's.
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
    expect(currentAt('/permissions', { tab: 'approvals' })).toBe('rules')
    // Permissions on another tab is a page of Identity and access, which no place holds.
    expect(currentAt('/permissions', { tab: 'policies' })).toBeNull()
  })
})

describe('a place stays current on the pages of its section', () => {
  it('marks no place on a page whose section has none, and lists its section above it', () => {
    routerState.pathname = '/routine-policies'
    renderIntel(
      <>
        <PlacesNav />
        <DestinationSections />
      </>,
    )
    expect(
      document.querySelector('[data-place][aria-current="page"]'),
    ).toBeNull()
    expect(
      document.querySelector('[data-slot="destination-sections"]'),
    ).not.toBeNull()
  })

  it('offers the pages of the section this principal may open above the page, marking it', () => {
    routerState.pathname = '/routine-policies'
    const hidden = viewOf('inferenceProxy')!.permission
    canMock.mockImplementation((p) => p !== hidden)
    renderIntel(<DestinationSections />)
    const strip = screen.getByRole('navigation', {
      name: 'Policies and governance boundaries',
    })
    const links = within(strip).getAllByRole('link')
    expect(links.map((a) => a.getAttribute('href'))).toEqual(
      authorizedSections('security-identity', allowAll)
        .find((s) => s.sectionId === 'policy')!
        .views.filter((v) => v.permission !== hidden)
        .map((v) => v.path),
    )
    expect(
      within(strip).getByRole('link', { name: 'Routine policies' }),
    ).toHaveAttribute('aria-current', 'page')
  })

  it('draws no row for a section with one permitted page', () => {
    routerState.pathname = '/routine-policies'
    const only = viewOf('routinePolicies')!.permission
    canMock.mockImplementation((p) => p === only)
    renderIntel(<DestinationSections />)
    expect(
      document.querySelector('[data-slot="destination-sections"]'),
    ).toBeNull()
  })

  it('pins no second MCP page: the discovered catalog is not a destination (HU 025)', () => {
    expect(DESTINATION_IDS).not.toContain('capabilities')
    expect(DESTINATION_IDS).not.toContain('catalog')
  })

  it('places privileged session recordings with the evidence, under the Ledger', () => {
    // Recordings and its viewer are Business pages: Community resolves neither.
    expect(currentAt('/recordings')).toBe(viewOf('recordings') ? 'audit' : null)
    expect(currentAt('/session-viewer/x')).toBe(
      viewOf('recordings') ? 'audit' : null,
    )
  })

  it('marks no place on a page no place shares a section with', () => {
    expect(currentAt('/models')).toBeNull()
    expect(currentAt('/settings')).toBeNull()
  })

  it('marks only a place the principal is offered', () => {
    // Tools is the only place of its section: without it the page marks nothing.
    expect(
      currentAt('/providers', {}, allBut(viewOf('agent-tools')!.permission)),
    ).toBeNull()
    // The queue address without the queue permission is a page no pin holds.
    expect(
      currentAt(
        '/permissions',
        { tab: 'approvals' },
        allBut(APPROVALS_SECTION.requires),
      ),
    ).toBeNull()
  })

  it('marks no place when the principal may not open the one holding the page', () => {
    expect(currentAt('/evals')).toBe('audit')
    expect(
      currentAt('/evals', {}, allBut(viewOf('audit')!.permission)),
    ).toBeNull()
  })

  it('resolves every door to a registered view', () => {
    const ids = new Set(FEATURE_VIEWS.map((v) => v.id))
    for (const v of FEATURE_VIEWS)
      if (v.doorTo)
        expect(ids.has(v.doorTo), `${v.id} → ${v.doorTo}`).toBe(true)
  })
})

describe('slice 8: a section wins over its view, and Settings holds its area', () => {
  it('marks Rules alone on the queue address, with no row of pages', () => {
    routerState.pathname = '/permissions'
    routerState.search = { tab: 'approvals' }
    renderIntel(<PlacesNav />)
    expect(
      [...document.querySelectorAll('[data-place][aria-current="page"]')].map(
        (a) => a.getAttribute('data-place'),
      ),
    ).toEqual(['rules'])
    renderIntel(<DestinationSections />)
    expect(
      document.querySelector('[data-slot="destination-sections"]'),
    ).toBeNull()
  })

  it('marks no place on Permissions itself, and gives it the row of its section', () => {
    routerState.pathname = '/permissions'
    routerState.search = { tab: 'policies' }
    renderIntel(<PlacesNav />)
    expect(
      document.querySelector('[data-place][aria-current="page"]'),
    ).toBeNull()
    renderIntel(<DestinationSections />)
    const strip = screen.getByRole('navigation', {
      name: 'Identity and access',
    })
    expect(
      within(strip).getByRole('link', { name: 'Permissions' }),
    ).toHaveAttribute('aria-current', 'page')
  })

  it("leads Settings' row with Settings itself, across its area", () => {
    routerState.pathname = '/backups'
    renderIntel(<DestinationSections />)
    const strip = screen.getByRole('navigation', { name: 'System & settings' })
    const links = within(strip).getAllByRole('link')
    expect(links[0]).toHaveAttribute('href', '/settings')
    expect(links.map((a) => a.getAttribute('href'))).toEqual([
      '/settings',
      ...authorizedSections('system', allowAll).flatMap((s) =>
        s.views.map((v) => v.path),
      ),
    ])
    expect(
      within(strip).getByRole('link', { name: 'Backups' }),
    ).toHaveAttribute('aria-current', 'page')
  })

  it('draws no row over a work surface, and keeps Sessions current on the /agentops door', () => {
    routerState.pathname = '/agentops'
    renderIntel(<DestinationSections />)
    expect(
      document.querySelector('[data-slot="destination-sections"]'),
    ).toBeNull()
    renderIntel(<PlacesNav />)
    expect(
      document
        .querySelector('[data-place="sessions"]')!
        .getAttribute('aria-current'),
    ).toBe('page')
  })
})

describe('the top bar names the places only; every other page is in an area', () => {
  it('leaves the rest of the product to the areas, not to a second list of places', () => {
    const placed = new Set(journeyDestinations(allowAll).map((d) => d.id))
    expect([...placed].sort()).toEqual(
      ['agent-tools', 'audit', 'home', 'permissions', 'sessions'].sort(),
    )
  })
})

describe('the row of pages beside this one is drawn at every width', () => {
  it('moves between the pages All areas lists for the section, with no sidebar to repeat them', () => {
    routerState.pathname = '/routine-policies'
    renderIntel(
      <>
        <DestinationSections />
        <AreasTree />
      </>,
    )
    const row = document.querySelector('[data-slot="destination-sections"]')
    expect(row).not.toBeNull()
    expect(row!.className).not.toMatch(/hidden/)
    const inRow = [...row!.querySelectorAll('a')].map((a) =>
      a.getAttribute('href'),
    )
    const inAreas = [
      ...document.querySelectorAll('[data-slot="areas-tree"] a'),
    ].map((a) => a.getAttribute('href'))
    expect(inRow.length).toBeGreaterThan(1)
    for (const href of inRow) expect(inAreas).toContain(href)
  })
})
