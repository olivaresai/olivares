// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE ONE NAVIGATION, AS A MODEL: the 0.7 map drawn from the registry. Sessions, Files and
// Tools, then the places under More, one noun each and no group; every page this principal may
// open sits in exactly one place; a place appears only when one of its pages opens.
import i18n from 'i18next'
import { describe, expect, it, vi } from 'vitest'
import '@/lib/i18n'
import { APPROVALS_SECTION } from '@/components/layout/shell-sections'
import { BUSINESS_BUILD } from '@/features/business-build'
import {
  FEATURE_VIEWS,
  ROUTE_ALIASES,
  type FeatureView,
} from '@/features/registry'
import census from '@/features/route-census.json'
import { settingsSearchEntries } from '@/features/settings/sections'
import {
  authorizedEntries,
  isListed,
  permissionGate,
  rankNavMatches,
  type ViewGate,
} from './model'
import {
  FIRST_PLACES,
  PLACES,
  buildNavSearchIndex,
  businessRows,
  currentPlace,
  placeOfView,
  placeOpenedAt,
  placePages,
  settingsPlace,
  sidebarPlaces,
  type PlaceId,
} from './places'

const everything: ViewGate = () => true

const t = i18n.getFixedT('en', 'nav')

describe('the sidebar follows the 0.7 map', () => {
  it('names Sessions, Files and Tools first, then the places under More, one noun each, with Settings at the foot', () => {
    expect(sidebarPlaces(everything).map((p) => t(`places.${p.id}`))).toEqual([
      'Sessions',
      'Files',
      'Tools',
      'Overview',
      'Rules',
      'Usage',
      'Audit',
      'People',
      'Security',
      'Operations',
    ])
    expect([...FIRST_PLACES]).toEqual(['sessions', 'files', 'tools'])
    expect(t(`places.${settingsPlace().id}`)).toBe('Settings')
    expect(settingsPlace().path).toBe('/settings')
  })

  it('names every place in every shipped language', () => {
    const ids: PlaceId[] = [...PLACES.map((p) => p.id), 'settings']
    const locales = Object.entries(
      import.meta.glob<{ places: Record<string, string> }>(
        '/src/lib/i18n/locales/*/nav.json',
        { eager: true, import: 'default' },
      ),
    )
    expect(locales).toHaveLength(11)
    for (const [file, nav] of locales) {
      const named = ids.map((id) => nav.places?.[id])
      expect(named.every(Boolean), file).toBe(true)
      expect(new Set(named).size, file).toBe(ids.length)
    }
  })

  it('links each place to a page of its own, and none to a Settings section', () => {
    for (const place of sidebarPlaces(everything)) {
      expect(place.path, place.id).not.toBe('/settings')
      const view = FEATURE_VIEWS.find((v) => v.path === place.path)!
      expect(placeOfView(view), place.id).toBe(place.id)
    }
  })
})

describe('the place you click is the place you land on', () => {
  // A fresh install selects no Security or Health module, so its Security and Operations
  // links open the next page the person may open (Kill switch, Backups): that page is the
  // place's, whoever opens it.
  const withoutModules: ViewGate = (view) =>
    !['security', 'health'].includes(view.id)

  it.each([
    ['a person who may open everything', everything],
    ['a person on a fresh install', withoutModules],
  ])(
    'names the page each sidebar link opens by its place: %s',
    (_who, gate) => {
      const places = sidebarPlaces(gate)
      expect(places.length).toBe(10)
      const wrong = [...places, settingsPlace()]
        .map((p) => ({
          place: p.id,
          named: placeOpenedAt(p.path, p.search ?? {}, gate),
        }))
        .filter((p) => p.named !== p.place)
      expect(wrong).toEqual([])
    },
  )

  it('names no other page of a place by the place', () => {
    // Operations opens its own page, which needs no module; with neither it nor Health to
    // open, the next page of the place (Backups) is the one it opens.
    expect(placeOpenedAt('/operations', {}, withoutModules)).toBe('operations')
    expect(
      placeOpenedAt(
        '/backups',
        {},
        (view) => !['operations', 'health'].includes(view.id),
      ),
    ).toBe('operations')
    expect(placeOpenedAt('/backups', {}, everything)).toBeNull()
    expect(
      placeOpenedAt('/settings', { section: 'signIn' }, everything),
    ).toBeNull()
  })

  it('lists the page each place opens under the place in the palette, and keeps its older name as a search word', () => {
    const index = buildNavSearchIndex(t as never)
    const places = sidebarPlaces(everything)
    const rows = places.map((p) => ({
      place: p.id,
      entry: index.find((e) => e.kind === 'view' && e.path === p.path),
      sidebar: t(`places.${p.id}`),
    }))
    expect(rows.filter((r) => r.entry?.label !== r.sidebar)).toEqual([])
    // Operations used to open "Health & SLA": Health keeps its own entry under that name.
    const health = index.find((e) => e.kind === 'view' && e.path === '/health')!
    expect(health.label).toBe(t('nav:items.health'))
  })
})

describe('every page sits in exactly one place', () => {
  it('lists each page the principal may open once across all places, and nothing else', () => {
    const ids = [...PLACES.map((p) => p.id), 'settings' as const]
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
    for (const place of sidebarPlaces(none)) {
      if (place.id === 'now') continue
      expect(placePages(place.id, none).length, place.id).toBeGreaterThan(0)
    }
    const offered = sidebarPlaces(none).map((p) => p.id)
    expect(offered).not.toContain('rules')
    expect(offered).not.toContain('sessions')
    expect(offered).toContain('now')
  })

  it('leads Tools with a door when the room is closed to the principal', () => {
    const roomClosed = permissionGate((p) => p !== 'sessions:account:read')
    const tools = sidebarPlaces(roomClosed).find((p) => p.id === 'tools')
    expect(tools?.path).toBe('/providers')
    // With the room open, the door is not listed beside it.
    expect(placePages('tools', everything).map((p) => p.id)).not.toContain(
      'providers',
    )
  })

  it('opens Rules on the approval queue only with the queue permission', () => {
    const rules = (gate: ViewGate) =>
      sidebarPlaces(gate).find((p) => p.id === 'rules')
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

  it('holds hosts, health, deployment, backups, system updates, webhooks, alerting, inference proxy, residency and tenants in Operations', () => {
    expect(placePages('operations', everything).map((p) => p.id)).toEqual(
      registered([
        'operations',
        'health',
        'backups',
        'updates',
        'eventing',
        'alerting',
        'inferenceProxy',
        'deploy',
        'residency',
        'tenants',
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
    const ids = [...PLACES.map((p) => p.id), 'settings' as const]
    const paths = ids.flatMap((id) =>
      placePages(id, everything).map((p) => p.path),
    )
    expect(paths.filter((p) => census.business_paths.includes(p))).toEqual([])
  })
})

describe('a row shows at most five pages of its place; More holds the rest (DESIGN-0.7 section 4)', () => {
  const rowOf = (id: PlaceId, gate: ViewGate = everything) =>
    placePages(id, gate)
      .filter((p) => !p.more)
      .map((p) => p.id)
  const moreOf = (id: PlaceId) =>
    placePages(id, everything)
      .filter((p) => p.more)
      .map((p) => p.id)

  it('names the row and the More of each place', () => {
    expect(rowOf('sessions')).toEqual([
      'sessions',
      'work',
      'communications',
      'voice',
      'sandbox',
    ])
    expect(moreOf('sessions')).toEqual(
      registered(['gitPublication', 'sourceDiff', 'recordings']),
    )
    expect(rowOf('tools')).toEqual([
      'agent-tools',
      'models',
      'capabilities',
      'knowledge',
    ])
    expect(moreOf('tools')).toEqual([
      'modelOps',
      'platforms',
      'rateLimits',
      'protocolBindings',
      'apiPlayground',
      'catalog',
      'agentArtifacts',
    ])
    expect(moreOf('rules')).toEqual(['agentcoreExport'])
    expect(rowOf('activity')).toEqual(['audit', 'accessMap', 'logs', 'evals'])
    expect(moreOf('activity')).toEqual([
      'observability',
      'dashboards',
      'retention',
      'reach',
    ])
    expect(rowOf('operations')).toEqual([
      'operations',
      'health',
      'backups',
      'updates',
      'eventing',
    ])
    expect(moreOf('operations')).toEqual(
      registered([
        'alerting',
        'inferenceProxy',
        'deploy',
        'residency',
        'tenants',
      ]),
    )
  })

  it('draws no row of more than five pages, even where a closed room lists its doors', () => {
    const roomClosed = permissionGate((p) => p !== 'sessions:account:read')
    for (const gate of [everything, roomClosed])
      for (const id of [...PLACES.map((p) => p.id), 'settings' as const])
        expect(rowOf(id, gate).length, id).toBeLessThanOrEqual(5)
    expect(rowOf('tools', roomClosed)).toHaveLength(5)
  })

  it('counts a page under More as its place for placeOfView', () => {
    const more = PLACES.flatMap((p) =>
      (p.more ?? []).map((id) => [id, p.id] as const),
    )
    expect(more.map(([id]) => id)).toContain('sourceDiff')
    for (const [id, place] of more) {
      const view = FEATURE_VIEWS.find((v) => v.id === id)
      if (view) expect(placeOfView(view), id).toBe(place)
    }
    // An edition page no list names joins the place of its section.
    expect(
      placeOfView(editionView('automations', 'automation', 'workflows')),
    ).toBe('sessions')
  })
})

describe('the current place', () => {
  it.each([
    ['/now', 'now'],
    ['/providers', 'tools'],
    ['/communications/inbox', 'sessions'],
    ['/permissions', 'rules'],
    ['/settings', 'settings'],
    ['/updates', 'operations'],
    ['/attestation', 'security'],
    ['/console', 'people'],
    ['/no-such-page', null],
  ] as const)('%s is %s', (path, place) => {
    expect(currentPlace(path)).toBe(place)
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

describe('"What do I have?" is answered in one place', () => {
  it('holds the workspace, its configured resources and the discovered ones together', () => {
    const files = placePages('files', everything).map((p) => p.id)
    for (const id of registered(['workspaceDashboard', 'estate', 'inventory']))
      expect(files).toContain(id)
  })

  it('names no page after another place, nor after the concept the home is built on', () => {
    const locales = Object.entries(
      import.meta.glob<{
        places: Record<string, string>
        items: Record<string, string>
      }>('/src/lib/i18n/locales/*/nav.json', {
        eager: true,
        import: 'default',
      }),
    )
    for (const [file, nav] of locales)
      for (const view of FEATURE_VIEWS) {
        const label = nav.items?.[view.id]
        if (!label) continue
        const own = placeOfView(view)
        for (const [place, name] of Object.entries(nav.places))
          if (place !== own) expect(label, `${file} ${view.id}`).not.toBe(name)
      }
    // "Estate" is the home's concept (Now): no page or place carries it as its name.
    const en = locales.find(([file]) => file.includes('/en/'))![1]
    for (const name of [
      ...Object.values(en.items),
      ...Object.values(en.places),
    ])
      expect(name).not.toMatch(/^estate$/i)
  })
})

/** The 0.7 map, page by page (DESIGN-0.7 section 4): every page of the sixteen 0.6 places,
 * Business pages included, and the place that holds it now. */
const PLACE_OF: Readonly<Record<string, PlaceId>> = {
  home: 'now',
  ...Object.fromEntries(
    [
      'sessions',
      'agentops',
      'work',
      'communications',
      'communicationsInbox',
      'communicationsNew',
      'communicationsHandoffs',
      'communicationsAdministration',
      'voice',
      'recordings',
      'sandbox',
      'gitPublication',
      'sourceDiff',
      'session-viewer',
    ].map((id) => [id, 'sessions']),
  ),
  ...Object.fromEntries(
    ['workspaceDashboard', 'estate', 'inventory', 'workspace-templates'].map(
      (id) => [id, 'files'],
    ),
  ),
  ...Object.fromEntries(
    [
      'agent-tools',
      'providers',
      'providerProfiles',
      'providerBindings',
      'providerAccounts',
      'models',
      'capabilities',
      'knowledge',
      'modelOps',
      'platforms',
      'rateLimits',
      'protocolBindings',
      'apiPlayground',
      'catalog',
      'agentArtifacts',
    ].map((id) => [id, 'tools']),
  ),
  ...Object.fromEntries(
    ['permissions', 'claudePolicy', 'routinePolicies', 'agentcoreExport'].map(
      (id) => [id, 'rules'],
    ),
  ),
  ...Object.fromEntries(
    ['usage', 'finops', 'storedBudgets', 'adoption'].map((id) => [id, 'usage']),
  ),
  ...Object.fromEntries(
    [
      'audit',
      'accessMap',
      'logs',
      'evals',
      'observability',
      'dashboards',
      'retention',
      'reach',
    ].map((id) => [id, 'activity']),
  ),
  ...Object.fromEntries(
    ['identity', 'organization', 'console'].map((id) => [id, 'people']),
  ),
  ...Object.fromEntries(
    ['security', 'attestation', 'compliance', 'redteam', 'killswitch'].map(
      (id) => [id, 'security'],
    ),
  ),
  ...Object.fromEntries(
    [
      'operations',
      'health',
      'deploy',
      'backups',
      'updates',
      'eventing',
      'alerting',
      'inferenceProxy',
      'residency',
      'tenants',
    ].map((id) => [id, 'operations']),
  ),
  vault: 'settings',
  onboarding: 'settings',
}

describe('the 0.7 map holds every page of the sixteen 0.6 places', () => {
  it('puts every registered page in its new place', () => {
    const wrong = FEATURE_VIEWS.map((v) => [v.id, placeOfView(v)]).filter(
      ([id, place]) => PLACE_OF[id] !== place,
    )
    expect(wrong).toEqual([])
  })

  it('files a Business page no list names by its section, in the place its section moved to', () => {
    expect(
      placeOfView(editionView('automations', 'automation', 'workflows')),
    ).toBe('sessions')
    expect(
      placeOfView(editionView('webhookRoutes', 'automation', 'events')),
    ).toBe('operations')
    expect(
      placeOfView(editionView('skillPacks', 'data-context', 'capabilities')),
    ).toBe('tools')
  })
})

describe('the palette names the place alone and keeps the former names as search words', () => {
  const index = [
    ...buildNavSearchIndex(t),
    ...settingsSearchEntries(i18n.getFixedT('en'), true, everything),
  ]
  const first = (q: string) =>
    rankNavMatches(authorizedEntries(index, everything), q)[0]?.id

  it('says the place without a group', () => {
    expect(index.find((e) => e.id === 'security')?.context).toBe('Security')
    expect(index.find((e) => e.id === 'capabilities')?.context).toBe('Tools')
    expect(index.find((e) => e.id === 'accessMap')?.context).toBe('Audit')
  })

  it.each([
    ['MCP & skills', 'capabilities'],
    ['Activity & audit', 'audit'],
    ['License', 'edition'],
    ['Work & communications', 'work'],
    ['Sources', 'knowledge'],
    ['Files & workspaces', 'workspaceDashboard'],
    ['Rules & approvals', 'permissions'],
    ['Usage & cost', 'usage'],
    ['People & groups', 'identity'],
    ['Now', 'home'],
    // The row names that became one noun (0.7).
    ['Communications', 'communications'],
    ['Configured resources', 'estate'],
    ['Discovered resources', 'inventory'],
    ['Workspace templates', 'workspace-templates'],
    ['Audit ledger', 'audit'],
    ['Identity & access', 'identity'],
    ['Health & SLA', 'health'],
    ['Webhooks & events', 'eventing'],
    ['Setup wizard', 'onboarding'],
    ['Model Operations', 'modelOps'],
    ['API Playground', 'apiPlayground'],
    ['Agent Artifacts', 'agentArtifacts'],
    ['Platforms', 'platforms'],
    // The page headings that became one noun (DESIGN-0.7 section 10 rule 2).
    ['Backup & Restore', 'backups'],
    ['Claude Code governance', 'claudePolicy'],
    ['Webhooks & event subscriptions', 'eventing'],
    ['Data, knowledge & context', 'knowledge'],
    ['Platforms & lifecycle', 'platforms'],
    ['Security & forensics', 'security'],
    ['Claude Code Adoption', 'adoption'],
    ['Log Viewer', 'logs'],
    ['Approvals and permissions', 'permissions'],
    ['Usage and cost, by account', 'usage'],
    ['Get started', 'onboarding'],
    ['Sign-in and security', 'signIn'],
  ])('finds %s first at the page it opened', (former, id) => {
    expect(first(former)).toBe(id)
  })
})

describe('the closed Business rows', () => {
  const rowsByView = (rows: (id: string) => readonly string[]) =>
    Object.fromEntries(
      FEATURE_VIEWS.flatMap((v) =>
        rows(v.id).length ? [[v.id, rows(v.id)]] : [],
      ),
    )

  it('Community shows them on the page of their capability, and nowhere else', () => {
    expect(rowsByView(businessRows)).toEqual(
      BUSINESS_BUILD
        ? {}
        : {
            work: ['sessions'],
            identity: ['people'],
            permissions: ['rules'],
            usage: ['usage'],
            security: ['securityCompliance', 'securityRedTeam'],
            operations: ['operationsDeploy', 'operationsResidency'],
            retention: ['retentionSchedules'],
          },
    )
  })

  it('a Business build draws none', async () => {
    vi.resetModules()
    vi.doMock('@/features/business-build', async (original) => ({
      ...(await original<typeof import('@/features/business-build')>()),
      BUSINESS_BUILD: true,
    }))
    try {
      const business = await import('./places')
      expect(rowsByView(business.businessRows)).toEqual({})
    } finally {
      vi.doUnmock('@/features/business-build')
      vi.resetModules()
    }
  })
})
