// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ONE NAVIGATION MODEL, AS THE PERSON SEES IT. Each view sits in one place, its area and
// section, and every surface that shows where a page is says the same thing: the row of
// pages above it, the sidebar row it marks, and All areas. The first job (the four
// journeys) is reachable from the first screen: a sidebar row, or a page in that row's
// strip, or the footer's Settings and its strip. The sidebar pins the first job; the areas
// hold the rest.
import { cleanup } from '@testing-library/react'
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
    const active =
      routerState.pathname === to &&
      Object.entries(search ?? {}).every(
        ([k, v]) => routerState.search[k] === v,
      )
    const href = search ? `${to}?${new URLSearchParams(search)}` : to
    return (
      <a href={href} {...props} {...(active ? activeProps : {})}>
        {children}
      </a>
    )
  },
}))

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    can: () => true,
    grants: [],
    isSuperadmin: true,
    principal: { superadmin: true, grants: [] },
    activeTenant: null,
    setActiveTenant: () => {},
  }),
}))

import { FEATURE_VIEWS, type FeatureView } from '@/features/registry'
import { SETTINGS_UTILITY, isListed, permissionGate } from './model'
import { JOURNEYS, stepHref } from './journeys'
import { AreasEntry } from '@/components/layout/areas-entry'
import { DestinationSections } from '@/components/layout/destination-sections'
import { JourneyNav } from '@/components/layout/journey-nav'

afterEach(() => {
  cleanup()
  routerState.pathname = '/'
  routerState.search = {}
})

const allowAll = permissionGate(() => true)

/** What the sidebar and the strip show at an address, for a principal who may open all. */
function shownAt(href: string) {
  const url = new URL(href, 'http://console.invalid')
  routerState.pathname = url.pathname
  routerState.search = Object.fromEntries(url.searchParams)
  const { container } = renderIntel(
    <>
      <JourneyNav />
      <DestinationSections />
      <AreasEntry open={false} onOpen={() => {}} />
    </>,
  )
  const strip = [
    ...container.querySelectorAll<HTMLAnchorElement>(
      '[data-slot="destination-sections"] a',
    ),
  ].map((a) => a.getAttribute('href') ?? '')
  const rows = [
    ...container.querySelectorAll<HTMLAnchorElement>('a[data-journey]'),
  ]
  const marked = rows
    .filter((a) => a.getAttribute('aria-current') === 'page')
    .map((a) => a.getAttribute('href') ?? '')
  const branch = container.querySelector('button[data-branch="active"]')
    ? 'active'
    : ''
  const result = {
    rows: rows.map((a) => a.getAttribute('href') ?? ''),
    strip,
    marked,
    branch,
  }
  cleanup()
  return result
}

const placeOf = (v: FeatureView) =>
  v.navigation.kind === 'root'
    ? 'root'
    : `${v.navigation.areaId}/${v.navigation.sectionId}`
const viewAt = (path: string) => FEATURE_VIEWS.find((v) => v.path === path)
const inSettingsArea = (v: FeatureView) =>
  v.navigation.kind !== 'root' &&
  v.navigation.areaId === SETTINGS_UTILITY.areaId

/** Every address the first screen offers: its sidebar rows, the footer's Settings, and the
 *  strip each of those opens on. */
function offeredFromFirstScreen(): Set<string> {
  const { rows } = shownAt('/')
  const offered = new Set(rows)
  for (const start of [...rows, SETTINGS_UTILITY.path]) {
    offered.add(start)
    for (const href of shownAt(start).strip) offered.add(href)
  }
  return offered
}

describe('the first screen', () => {
  it('offers every step of the four journeys: a sidebar row, its strip, or Settings', () => {
    const offered = offeredFromFirstScreen()
    const missing = Object.entries(JOURNEYS).flatMap(([journey, steps]) =>
      steps
        .map((step) => stepHref(step))
        .filter((href) => !offered.has(href))
        .map((href) => `${journey}: ${href}`),
    )
    expect(missing).toEqual([])
  })

  it('pins the first job and only the first job, on a new installation and any other', () => {
    expect(shownAt('/').rows).toEqual([
      '/',
      '/sessions',
      '/agent-tools',
      '/permissions?tab=approvals',
    ])
    // A tool, a key or a local model, a session, its approval, stop and resume, history.
    const offered = offeredFromFirstScreen()
    for (const step of [
      ...JOURNEYS.firstHour,
      ...JOURNEYS.sessions,
      ...JOURNEYS.approvals,
    ])
      expect(offered).toContain(stepHref(step))
    // The rest of the product is in the areas, not in a second list of pins.
    expect(shownAt('/').rows).not.toContain('/audit')
  })
})

describe('every page sits in one place', () => {
  const listed = FEATURE_VIEWS.filter(
    (v) => v.navigation.kind === 'feature' && isListed(v, allowAll),
  )

  it('lists only pages of its own section in the strip above it', () => {
    const strays = listed.flatMap((v) =>
      shownAt(v.path)
        .strip.filter((href) => href !== SETTINGS_UTILITY.path)
        .map((href) => viewAt(href))
        .filter(
          (member): member is FeatureView =>
            !!member &&
            placeOf(member) !== placeOf(v) &&
            // The footer's Settings stands for its whole area.
            !(inSettingsArea(v) && inSettingsArea(member)),
        )
        .map(
          (member) =>
            `${v.path} (${placeOf(v)}) lists ${member.path} (${placeOf(member)})`,
        ),
    )
    expect(strays).toEqual([])
  })

  it('marks one sidebar row of its own section (itself when it is one), or names its area in All areas', () => {
    const wrong = listed.flatMap((v) => {
      const { rows, marked, branch } = shownAt(v.path)
      const pinned = rows.some((href) => href === v.path)
      const row = marked.length === 1 ? viewAt(marked[0].split('?')[0]) : null
      if (row && placeOf(row) === placeOf(v) && (!pinned || row.id === v.id))
        return []
      if (marked.length === 0 && (branch !== '' || inSettingsArea(v))) return []
      return [
        `${v.path} (${placeOf(v)}) marks ${marked.join(', ') || 'nothing'}`,
      ]
    })
    expect(listed.length).toBeGreaterThan(50)
    expect(wrong).toEqual([])
  })
})
