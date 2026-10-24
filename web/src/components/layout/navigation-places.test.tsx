// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE NAVIGATION AS A PERSON READS IT (issue #3121): the places of the top bar, then All
// areas. Each place appears once, by one name, at most two levels deep (an area, then its
// pages), and no entry is named after a vendor. The registry feeds both surfaces, so this
// walks what they render for a principal who may open everything.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ComponentProps } from 'react'
import i18n from 'i18next'
import { renderIntel } from '@/test/intel'

vi.mock('@tanstack/react-router', () => ({
  useRouterState: ({
    select,
  }: { select?: (s: { location: { pathname: string } }) => unknown } = {}) =>
    select ? select({ location: { pathname: '/' } }) : '',
  Link: ({
    children,
    to,
    activeProps: _activeProps,
    activeOptions: _activeOptions,
    ...props
  }: ComponentProps<'a'> & {
    to?: string
    activeProps?: Record<string, string>
    activeOptions?: { exact?: boolean }
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

/** Favorites and Recent, off unless a test opens a personal partition. */
const personal = vi.hoisted(() => ({
  available: false,
  favorites: [] as { kind: 'feature'; id: string }[],
  recents: [] as { kind: 'feature'; id: string }[],
}))
vi.mock('@/features/navigation/personal-navigation', () => ({
  usePersonalNavigation: () => ({
    ...personal,
    temporary: false,
    visible: () => true,
    setFavorite: () => {},
    removeRecent: () => {},
    clearRecents: () => {},
    clearFavorites: () => {},
    recordVisit: () => {},
  }),
}))

import { AreasTree } from './sidebar'
import {
  directoryAreas,
  journeyDestinations,
  phoneBarDestinations,
} from './shell-destinations'
import {
  AREA_EXPANSION_VERSION,
  DEFAULT_AREA_EXPANSION,
  usePreferencesStore,
} from '@/stores/preferences'
import { FEATURE_VIEWS, NAV_AREAS } from '@/features/registry'
import { isListed, type ViewGate } from '@/features/navigation/model'

const jsdomMatchMedia = window.matchMedia
/** A viewport this wide, as `matchMedia` answers the console's width queries. */
function viewportWidth(px: number) {
  window.matchMedia = (query: string) =>
    ({
      matches: px >= Number(/min-width: (\d+)px/.exec(query)?.[1] ?? Infinity),
      media: query,
      onchange: null,
      addEventListener: () => {},
      removeEventListener: () => {},
      addListener: () => {},
      removeListener: () => {},
      dispatchEvent: () => false,
    }) as MediaQueryList
}

beforeEach(() => viewportWidth(1280))
afterEach(() => {
  personal.available = false
  personal.favorites = []
  personal.recents = []
  window.matchMedia = jsdomMatchMedia
  usePreferencesStore.setState({ navAreas: DEFAULT_AREA_EXPANSION })
})

/** Vendors and their products: a navigation entry names what a person does, not whose
 * tool it is (adoption is a filter of Costs, not a page per vendor). */
const VENDOR =
  /\b(anthropic|claude|openai|codex|chatgpt|gemini|google|aws|amazon|agentcore|bedrock|azure|microsoft|github|gitlab|opencode|grok|xai|cursor|copilot|mistral|kimi|glm)\b/i

const allowAll = () => true

interface Entry {
  label: string
  route: string
}

/** The top bar's places and every row of All areas with every area open. */
function navigationEntries(): Entry[] {
  usePreferencesStore.setState({
    navAreas: {
      v: AREA_EXPANSION_VERSION,
      open: NAV_AREAS.map((a) => a.id),
      closed: [],
    },
  })
  const { container, unmount } = renderIntel(<AreasTree />)
  const places = journeyDestinations(allowAll).map((p) => ({
    label: i18n.t(`nav:shell.journeys.${p.key}`),
    route: p.path,
  }))
  const rows = [
    ...container.querySelectorAll<HTMLAnchorElement>('a[href]'),
  ].map((a) => ({
    label: (a.textContent ?? '').trim(),
    route: a.getAttribute('href') ?? '',
  }))
  unmount()
  // A tree that rendered nothing would pass every check below.
  expect(rows.length).toBeGreaterThan(NAV_AREAS.length * 2)
  expect(rows.map((r) => r.route)).toContain('/mcp-servers')
  return [...places, ...rows]
}

const repeats = (values: string[]) =>
  values.filter((v, i) => values.indexOf(v) !== i)

describe('the navigation: one entry per place, two levels at most (#3121)', () => {
  it('offers each route once across the top bar and All areas', () => {
    expect(repeats(navigationEntries().map((e) => e.route))).toEqual([])
  })

  it('names each entry once, so no two entries read alike', () => {
    expect(
      repeats(navigationEntries().map((e) => e.label.toLowerCase())),
    ).toEqual([])
  })

  it('names no vendor', () => {
    expect(
      navigationEntries()
        .map((e) => e.label)
        .filter((l) => VENDOR.test(l)),
    ).toEqual([])
  })

  it('is two levels deep: an area, then its pages, with no heading between them', () => {
    const { container } = renderIntel(<AreasTree />)
    // A section heading inside an area is a third level, and so is a list in a list.
    expect(
      container.querySelectorAll('[data-nav-area] [role="group"]'),
    ).toHaveLength(0)
    expect(container.querySelectorAll('[data-nav-area] ul ul')).toHaveLength(0)
    // The tree opens on its areas, not on a caption above them.
    expect(container.querySelector('[data-nav-area]')).not.toBeNull()
    expect(
      [...container.querySelectorAll('span, p')].some(
        (e) => e.textContent?.trim() === i18n.t('nav:directory.areas'),
      ),
    ).toBe(false)
  })

  it('calls the home Now wherever it is named, in every language', () => {
    for (const lng of ['en', 'de', 'es', 'fr', 'it', 'ja', 'ko', 'ru', 'zh'])
      expect(i18n.t('nav:shell.journeys.home', { lng }), lng).toBe(
        i18n.t('nav:items.home', { lng }),
      )
    expect(i18n.t('nav:shell.journeys.home')).toBe('Now')
  })
})

describe('All areas keeps every page reachable (#3121)', () => {
  /** Every view navigation offers this principal: listed, not a detail, not the root. */
  const offered = (gate: ViewGate) =>
    FEATURE_VIEWS.filter(
      (v) =>
        !v.hideInNav && v.navigation.kind === 'feature' && isListed(v, gate),
    ).map((v) => v.id)

  const GATES: Record<string, ViewGate> = {
    'may open everything': () => true,
    'without the approval queue': (_view, section) => !section,
    'without live sessions, through the operate door': (view) =>
      view.id !== 'sessions',
    'without the ledger': (view) => view.id !== 'audit',
  }

  for (const [name, gate] of Object.entries(GATES))
    for (const [bar, places] of [
      ['top bar', journeyDestinations(gate)],
      ['phone bar', phoneBarDestinations(gate)],
    ] as const)
      it(`a principal ${name}, on the ${bar}, finds each page once`, () => {
        const listed = directoryAreas(gate, places).flatMap((a) =>
          a.views.map((v) => v.id),
        )
        const shown = places.map((p) => p.id)
        // Nothing doubled, nothing both a place and a row, nothing lost.
        expect(listed.filter((id, i) => listed.indexOf(id) !== i)).toEqual([])
        expect(listed.filter((id) => shown.includes(id))).toEqual([])
        expect(
          offered(gate).filter(
            (id) => !listed.includes(id) && !shown.includes(id),
          ),
        ).toEqual([])
      })

  it('Rules is the Permissions page, so it is listed only for whom Rules is not offered', () => {
    const ids = (gate: ViewGate) =>
      directoryAreas(gate).flatMap((a) => a.views.map((v) => v.id))
    expect(ids(() => true)).not.toContain('permissions')
    expect(ids((_view, section) => !section)).toContain('permissions')
  })

  it('below 761 px All areas lists the places the phone bar leaves out', () => {
    viewportWidth(390)
    usePreferencesStore.setState({
      navAreas: {
        v: AREA_EXPANSION_VERSION,
        open: NAV_AREAS.map((a) => a.id),
        closed: [],
      },
    })
    const { container } = renderIntel(<AreasTree />)
    const routes = [...container.querySelectorAll('a[href]')].map((a) =>
      a.getAttribute('href'),
    )
    // Rules and Ledger have no room in the phone bar: their pages stay in their areas.
    expect(routes).toEqual(expect.arrayContaining(['/permissions', '/audit']))
    // Now, Sessions and Tools are in the phone bar: not listed again.
    expect(routes).not.toContain('/')
    expect(routes).not.toContain('/sessions')
    expect(routes).not.toContain('/agent-tools')
  })
})

describe('Favorites and Recent in All areas (#3121)', () => {
  it('list no page the bar shows', () => {
    personal.available = true
    personal.recents = [
      { kind: 'feature', id: 'home' },
      { kind: 'feature', id: 'mcpServers' },
    ]
    const { container } = renderIntel(<AreasTree />)
    const routes = [...container.querySelectorAll('a[href]')].map((a) =>
      a.getAttribute('href'),
    )
    // Now is a place of the bar: not listed again under Recent; MCP servers is.
    expect(routes).not.toContain('/')
    expect(routes).toContain('/mcp-servers')
  })
})
