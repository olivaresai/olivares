// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE ONE NAVIGATION, AS A MODEL: the ratified map (IA.md of the 0.5 design gate) drawn from
// the registry. Every area appears once, in its group and order; every page this principal may
// open sits in exactly one place; a place appears only when one of its pages opens.
import i18n from 'i18next'
import { describe, expect, it } from 'vitest'
import '@/lib/i18n'
import { APPROVALS_SECTION } from '@/components/layout/shell-sections'
import { BUSINESS_BUILD } from '@/features/business-build'
import {
  FEATURE_VIEWS,
  ROUTE_ALIASES,
  type FeatureView,
} from '@/features/registry'
import census from '@/features/route-census.json'
import { isListed, permissionGate, type ViewGate } from './model'
import {
  PLACE_GROUPS,
  currentPlace,
  placeOfView,
  placePages,
  settingsPlace,
  sidebarGroups,
  type PlaceId,
} from './places'

const everything: ViewGate = () => true

/** IA.md's table, in its order: group, then the place's name. */
const IA: readonly [string, string][] = [
  ['work', 'Now'],
  ['work', 'Sessions'],
  ['work', 'Files & workspaces'],
  ['work', 'Work & communications'],
  ['tools', 'AI tools'],
  ['tools', 'Models'],
  ['tools', 'MCP & skills'],
  ['tools', 'Sources'],
  ['control', 'Rules & approvals'],
  ['control', 'Usage & cost'],
  ['control', 'Activity & audit'],
  ['control', 'Access map'],
  ['admin', 'People & groups'],
  ['admin', 'Security'],
  ['admin', 'Operations'],
  ['admin', 'License'],
]

const t = i18n.getFixedT('en', 'nav')

describe('the sidebar follows the ratified map', () => {
  it('names every area of IA.md once, in its group and order, with Settings at the foot', () => {
    const groups = sidebarGroups(everything)
    expect(groups.map((g) => g.id)).toEqual([
      'work',
      'tools',
      'control',
      'admin',
    ])
    expect(
      groups.flatMap((g) => g.places.map((p) => [g.id, t(`places.${p.id}`)])),
    ).toEqual(IA)
    expect(t(`places.${settingsPlace().id}`)).toBe('Settings')
    expect(settingsPlace().path).toBe('/settings')
  })

  it('names every place in every shipped language', () => {
    const ids: PlaceId[] = [
      ...PLACE_GROUPS.flatMap((g) => g.places.map((p) => p.id)),
      'settings',
    ]
    const locales = Object.entries(
      import.meta.glob<{ places: Record<string, string> }>(
        '/src/lib/i18n/locales/*/nav.json',
        { eager: true, import: 'default' },
      ),
    )
    expect(locales).toHaveLength(9)
    for (const [file, nav] of locales) {
      const named = ids.map((id) => nav.places?.[id])
      expect(named.every(Boolean), file).toBe(true)
      expect(new Set(named).size, file).toBe(ids.length)
    }
  })

  it('links each place to a page of its own', () => {
    for (const place of sidebarGroups(everything).flatMap((g) => g.places)) {
      if (place.id === 'license') {
        expect(place).toMatchObject({
          path: '/settings',
          search: { section: 'edition' },
        })
        continue
      }
      const view = FEATURE_VIEWS.find((v) => v.path === place.path)!
      expect(placeOfView(view), place.id).toBe(place.id)
    }
  })
})

describe('every page sits in exactly one place', () => {
  it('lists each page the principal may open once across all places, and nothing else', () => {
    const ids = [
      ...PLACE_GROUPS.flatMap((g) => g.places.map((p) => p.id)),
      'settings' as const,
    ]
    const listed = ids.flatMap((id) =>
      placePages(id, everything).map((p) => p.id),
    )
    expect(new Set(listed).size).toBe(listed.length)
    const expected = FEATURE_VIEWS.filter(
      (v) =>
        !v.hideInNav &&
        v.navigation.kind === 'feature' &&
        isListed(v, everything),
    ).map((v) => v.id)
    expect(listed.filter((id) => id !== 'settings').sort()).toEqual(
      [...expected].sort(),
    )
  })

  it('places an edition page no list names by its registry section', () => {
    const edition = {
      id: 'teamCosts',
      path: '/team-costs',
      navigation: {
        kind: 'feature',
        areaId: 'observation',
        sectionId: 'cost-adoption',
      },
    } as unknown as FeatureView
    expect(placeOfView(edition)).toBe('usage')
  })
})

describe('a place appears only where one of its pages opens', () => {
  it('offers a principal with no grant only the places it may open', () => {
    const none = permissionGate(() => false)
    for (const place of sidebarGroups(none).flatMap((g) => g.places)) {
      if (place.id === 'now' || place.id === 'license') continue
      expect(placePages(place.id, none).length, place.id).toBeGreaterThan(0)
    }
    const offered = sidebarGroups(none).flatMap((g) =>
      g.places.map((p) => p.id),
    )
    expect(offered).not.toContain('rules')
    expect(offered).not.toContain('sessions')
    expect(offered).toContain('now')
  })

  it('leads AI tools with a door when the room is closed to the principal', () => {
    const roomClosed = permissionGate((p) => p !== 'sessions:account:read')
    const tools = sidebarGroups(roomClosed)
      .flatMap((g) => g.places)
      .find((p) => p.id === 'ai-tools')
    expect(tools?.path).toBe('/providers')
    // With the room open, the door is not listed beside it.
    expect(placePages('ai-tools', everything).map((p) => p.id)).not.toContain(
      'providers',
    )
  })

  it('opens Rules on the approval queue only with the queue permission', () => {
    const rules = (gate: ViewGate) =>
      sidebarGroups(gate)
        .flatMap((g) => g.places)
        .find((p) => p.id === 'rules')
    expect(rules(everything)).toMatchObject({
      path: '/permissions',
      search: APPROVALS_SECTION.search,
    })
    const noQueue = permissionGate((p) => p !== APPROVALS_SECTION.requires)
    expect(rules(noQueue)?.path).toBe('/permissions')
    expect(rules(noQueue)?.search).toBeUndefined()
  })

  it('offers Business places only where the edition registers their pages', () => {
    const ids = FEATURE_VIEWS.map((v) => v.id)
    expect(ids.includes('organization')).toBe(BUSINESS_BUILD)
  })
})

/** An edition page, as the Business extension registers it: only its id and section matter here. */
const editionView = (id: string, areaId: string, sectionId: string) =>
  ({
    id,
    path: `/${id}`,
    navigation: { kind: 'feature', areaId, sectionId },
  }) as unknown as FeatureView

/** The registered subset of a list of ids, in the list's order. */
const registered = (ids: readonly string[]) =>
  ids.filter((id) => FEATURE_VIEWS.some((v) => v.id === id))

describe('Admin and Settings hold what IA.md names (0.5 fold)', () => {
  it('holds posture, supply chain, compliance, Red team and the kill switch in Security', () => {
    expect(placePages('security', everything).map((p) => p.id)).toEqual(
      registered([
        'security',
        'attestation',
        'compliance',
        'redteam',
        'killswitch',
      ]),
    )
    expect(placePages('activity', everything).map((p) => p.id)).not.toContain(
      'attestation',
    )
    expect(
      placeOfView(editionView('redteam', 'security-identity', 'defense')),
    ).toBe('security')
  })

  it('holds hosts, deployment, backups, residency, inference proxy, tenants, health and system updates in Operations', () => {
    expect(placePages('operations', everything).map((p) => p.id)).toEqual(
      registered([
        'estate',
        'health',
        'deploy',
        'backups',
        'residency',
        'inferenceProxy',
        'tenants',
        'updates',
      ]),
    )
    // System updates: the engine checks system:admin, and so does the page's route.
    expect(FEATURE_VIEWS.find((v) => v.id === 'updates')).toMatchObject({
      path: '/updates',
      permission: 'system:admin',
    })
    const noSystem = permissionGate((p) => p !== 'system:admin')
    expect(placePages('operations', noSystem).map((p) => p.id)).not.toContain(
      'updates',
    )
    // The Business extension files these under sections the other places own.
    expect(
      placeOfView(editionView('residency', 'security-identity', 'policy')),
    ).toBe('operations')
    expect(
      placeOfView(editionView('tenants', 'system', 'administration')),
    ).toBe('operations')
  })

  it('keeps system updates out of Settings: its pages are personal ones', () => {
    expect(placePages('settings', everything).map((p) => p.id)).toEqual([
      'settings',
      'vault',
      'onboarding',
    ])
  })

  it('offers a Community principal no Business destination in any place', () => {
    if (BUSINESS_BUILD) return
    const ids = [
      ...PLACE_GROUPS.flatMap((g) => g.places.map((p) => p.id)),
      'settings' as const,
    ]
    const paths = ids.flatMap((id) =>
      placePages(id, everything).map((p) => p.path),
    )
    expect(paths.filter((p) => census.business_paths.includes(p))).toEqual([])
  })
})

describe('the current place', () => {
  it.each([
    ['/', {}, 'now'],
    ['/providers', {}, 'ai-tools'],
    ['/communications/inbox', {}, 'work'],
    ['/permissions', { tab: 'approvals' }, 'rules'],
    ['/settings', {}, 'settings'],
    ['/settings', { section: 'edition' }, 'license'],
    ['/settings', { section: 'keyboard' }, 'settings'],
    ['/updates', {}, 'operations'],
    ['/attestation', {}, 'security'],
    ['/console', { tab: 'agents' }, 'people'],
    ['/no-such-page', {}, null],
  ] as const)('%s %o is %s', (path, search, place) => {
    expect(currentPlace(path, search)).toBe(place)
  })
})

describe('the retired area directories', () => {
  it('land on a page of the place that holds their pages', () => {
    const areas = ROUTE_ALIASES.filter((a) => a.from.startsWith('/areas/'))
    expect(areas).toHaveLength(9)
    for (const a of areas) {
      if (a.to === '/settings') continue
      expect(
        FEATURE_VIEWS.some((v) => v.path === a.to),
        a.from,
      ).toBe(true)
    }
  })
})
