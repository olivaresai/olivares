// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE ROUTE MAP PIN AND THE MODEL'S CONTRACT (N1).
//
// Root ratified one placement per route (an internal design note (not shipped)
// ROUTE-MAP.md), extended by k3-i3-console-construction/CONSTRUCTION-3.md §4 for Handoffs.
// The table below pins those decisions so the registry cannot drift
// from it silently: moving a view to another area, renaming a section or forgetting a new
// entry's place turns this file red with the id in the message. Structural facts the shell
// relies on — every leaf placed, every area non-empty, one detail per parent, the utility's
// place — and the behaviours the proposal's §3 and §9 name (breadcrumbs, union visibility,
// ranking, the fold) are pinned beside it.
import { readFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import i18n from 'i18next'
import { describe, expect, it } from 'vitest'
import { LANGUAGE_CODES } from '@/lib/i18n'
import {
  FEATURE_VIEWS,
  NAV_AREAS,
  viewsByArea,
  type AreaId,
} from '@/features/registry'
import {
  SETTINGS_UTILITY,
  activeAreaId,
  authorizedSections,
  breadcrumbTrail,
  buildNavSearchIndex,
  currentViewId,
  fold,
  isAreaVisible,
  permissionGate,
  rankNavMatches,
  resolveLocation,
  visibleAreas,
} from './model'

/** Ratified route placements, as `id: [area, section]` (`root` for `/`). */
const RATIFIED: Record<string, [AreaId, string] | 'root'> = {
  home: 'root',
  workspaceDashboard: ['infrastructure', 'estate'],
  inventory: ['infrastructure', 'estate'],
  sessions: ['ai', 'sessions'],
  agentops: ['ai', 'sessions'],
  providerProfiles: ['ai', 'environments'],
  providerBindings: ['ai', 'environments'],
  'workspace-templates': ['ai', 'environments'],
  models: ['ai', 'models'],
  modelOps: ['ai', 'models'],
  voice: ['ai', 'execution'],
  sandbox: ['ai', 'execution'],
  platforms: ['ai', 'provider-reference'],
  rateLimits: ['ai', 'provider-reference'],
  capabilities: ['data-context', 'capabilities'],
  catalog: ['data-context', 'capabilities'],
  knowledge: ['data-context', 'knowledge'],
  agentArtifacts: ['data-context', 'artifacts'],
  work: ['work-communications', 'work'],
  protocolBindings: ['work-communications', 'communications'],
  communications: ['work-communications', 'communications'],
  communicationsInbox: ['work-communications', 'communications'],
  communicationsNew: ['work-communications', 'communications'],
  communicationsAdministration: ['work-communications', 'communications'],
  communicationsHandoffs: ['work-communications', 'communications'],
  automations: ['automation', 'workflows'],
  orchestration: ['automation', 'workflows'],
  eventing: ['automation', 'events'],
  alerting: ['automation', 'events'],
  accessMap: ['security-identity', 'access'],
  permissions: ['security-identity', 'access'],
  identity: ['security-identity', 'access'],
  claudePolicy: ['security-identity', 'policy'],
  routinePolicies: ['security-identity', 'policy'],
  inferenceProxy: ['security-identity', 'policy'],
  security: ['security-identity', 'defense'],
  redteam: ['security-identity', 'defense'],
  killswitch: ['security-identity', 'defense'],
  agentcoreExport: ['security-identity', 'boundaries'],
  residency: ['security-identity', 'boundaries'],
  deploy: ['deployment', 'deployments'],
  health: ['observation', 'operations'],
  observability: ['observation', 'operations'],
  dashboards: ['observation', 'operations'],
  finops: ['observation', 'cost-adoption'],
  'team-costs': ['observation', 'cost-adoption'],
  adoption: ['observation', 'cost-adoption'],
  audit: ['observation', 'audit-recordings'],
  recordings: ['observation', 'audit-recordings'],
  'session-viewer': ['observation', 'audit-recordings'],
  evals: ['observation', 'evaluation-evidence'],
  compliance: ['observation', 'evaluation-evidence'],
  postureExport: ['observation', 'evaluation-evidence'],
  reporting: ['observation', 'evaluation-evidence'],
  attestation: ['observation', 'evaluation-evidence'],
  console: ['system', 'administration'],
  tenants: ['system', 'administration'],
  onboarding: ['system', 'maintenance'],
  backups: ['system', 'maintenance'],
  logs: ['system', 'maintenance'],
  apiPlayground: ['system', 'development'],
}

const t = i18n.getFixedT(null, 'nav') as never
/** The two extreme permission sets, as view gates. They go through `permissionGate` on
 *  purpose: the rule under test is that a view with NO permission is open to every
 *  signed-in principal, and a bare `() => false` would deny those too — measuring the
 *  gate instead of the area algebra. */
const allow = permissionGate(() => true)
const deny = permissionGate(() => false)

describe('route map — every route sits where root ratified it', () => {
  it('places every registered view exactly as ratified, and knows no other', () => {
    const actual: Record<string, [string, string] | 'root'> = {}
    for (const v of FEATURE_VIEWS) {
      actual[v.id] =
        v.navigation.kind === 'root'
          ? 'root'
          : [v.navigation.areaId, v.navigation.sectionId]
    }
    expect(actual).toEqual(RATIFIED)
  })

  it('names only sections the area declares, in a declared area', () => {
    const bad = FEATURE_VIEWS.filter((v) => {
      const nav = v.navigation
      if (nav.kind === 'root') return false
      const area = NAV_AREAS.find((a) => a.id === nav.areaId)
      return !area || !area.sections.includes(nav.sectionId)
    }).map((v) => v.id)
    expect(bad).toEqual([])
  })

  it('gives every area at least one listed leaf, and every declared section a member (Preferences holds the utility)', () => {
    for (const area of NAV_AREAS) {
      const sections = viewsByArea(area.id)
      expect(sections.length, area.id).toBeGreaterThan(0)
      const used = new Set(sections.map((s) => s.sectionId))
      for (const s of area.sections) {
        if (
          area.id === SETTINGS_UTILITY.areaId &&
          s === SETTINGS_UTILITY.sectionId
        )
          continue
        expect(used.has(s), `${area.id}/${s} has no view`).toBe(true)
      }
    }
  })

  it('keeps the recording viewer a detail of Recordings, hidden from every list', () => {
    const viewer = FEATURE_VIEWS.find((v) => v.id === 'session-viewer')!
    expect(viewer.hideInNav).toBe(true)
    expect(viewer.navigation).toEqual({
      kind: 'detail',
      areaId: 'observation',
      sectionId: 'audit-recordings',
      parentViewId: 'recordings',
    })
    const listed = viewsByArea('observation').flatMap((s) =>
      s.views.map((v) => v.id),
    )
    expect(listed).toContain('recordings')
    expect(listed).not.toContain('session-viewer')
    expect(buildNavSearchIndex(t).some((e) => e.id === 'session-viewer')).toBe(
      false,
    )
  })

  it('mounts exactly nine areas with unique paths under /areas/', () => {
    expect(NAV_AREAS).toHaveLength(9)
    const paths = NAV_AREAS.map((a) => a.path)
    expect(new Set(paths).size).toBe(9)
    for (const p of paths) expect(p).toMatch(/^\/areas\/[a-z-]+$/)
    expect(NAV_AREAS.map((a) => a.id)).toEqual([
      'infrastructure',
      'ai',
      'data-context',
      'work-communications',
      'automation',
      'security-identity',
      'deployment',
      'observation',
      'system',
    ])
  })
})

describe('directory descriptions (review F3)', () => {
  // Every listed view has a description in EVERY published language; for the seven the
  // review found empty, the translation must also differ from the English sentence. That
  // is a presence-and-difference check — it does not certify the translation's quality.
  const LOCALES_ROOT = resolve(__dirname, '../../lib/i18n/locales')
  const nav = (lng: string) =>
    JSON.parse(readFileSync(join(LOCALES_ROOT, lng, 'nav.json'), 'utf8')) as {
      descriptions: Record<string, string>
    }
  const SEVEN = [
    'routinePolicies',
    'agentcoreExport',
    'workspace-templates',
    'eventing',
    'team-costs',
    'backups',
    'logs',
  ]

  it('describes every listed view in every language', () => {
    for (const lng of LANGUAGE_CODES) {
      const d = nav(lng).descriptions
      const missing = FEATURE_VIEWS.filter(
        (v) => !v.hideInNav && !(d[v.id] ?? '').trim(),
      ).map((v) => v.id)
      expect(missing, lng).toEqual([])
    }
  })

  it('translates the seven completed descriptions rather than copying the English sentence', () => {
    const en = nav('en').descriptions
    for (const id of SEVEN) expect(en[id].length, id).toBeGreaterThan(40)
    for (const lng of LANGUAGE_CODES) {
      if (lng === 'en') continue
      const d = nav(lng).descriptions
      for (const id of SEVEN) {
        expect((d[id] ?? '').trim().length, `${lng}:${id}`).toBeGreaterThan(20)
        expect(d[id], `${lng}:${id}`).not.toBe(en[id])
      }
    }
  })
})

describe('resolveLocation and the trail', () => {
  it('resolves every kind of location', () => {
    expect(resolveLocation('/').kind).toBe('home')
    expect(resolveLocation('/areas/ai')).toMatchObject({
      kind: 'area',
      area: { id: 'ai' },
    })
    expect(resolveLocation('/agentops')).toMatchObject({
      kind: 'view',
      view: { id: 'agentops' },
      area: { id: 'ai' },
      parent: null,
    })
    expect(resolveLocation('/session-viewer/sess-a11y')).toMatchObject({
      kind: 'view',
      view: { id: 'session-viewer' },
      parent: { id: 'recordings' },
    })
    expect(resolveLocation('/settings').kind).toBe('settings')
    expect(resolveLocation('/areas/nope').kind).toBe('unknown')
    expect(resolveLocation('/areas/ai/extra').kind).toBe('unknown')
    expect(resolveLocation('/does-not-exist').kind).toBe('unknown')
  })

  it('prefers the longest registry prefix (no shorter-path shadowing)', () => {
    expect(currentViewId('/model-operations')).toBe('modelOps')
    expect(currentViewId('/models')).toBe('models')
    expect(currentViewId('/audit/some/detail')).toBe('audit')
  })

  it('builds the trails the proposal names, ancestors linked and the page not', () => {
    const labels = (path: string) =>
      breadcrumbTrail(t, resolveLocation(path)).map((c) => [c.label, c.to])
    expect(labels('/')).toEqual([['Overview', undefined]])
    expect(labels('/areas/ai')).toEqual([
      ['Overview', '/'],
      ['AI', undefined],
    ])
    expect(labels('/agentops')).toEqual([
      ['AI', '/areas/ai'],
      ['Operate sessions', undefined],
    ])
    expect(labels('/session-viewer/sess-a11y')).toEqual([
      ['Observability & evidence', '/areas/observation'],
      ['Recordings', '/recordings'],
      ['Session viewer', undefined],
    ])
    expect(labels('/settings')).toEqual([
      ['System & settings', '/areas/system'],
      ['Settings', undefined],
    ])
    expect(labels('/nowhere')).toEqual([])
  })

  it('derives the active area from the url, Settings included', () => {
    expect(activeAreaId(resolveLocation('/agentops'))).toBe('ai')
    expect(activeAreaId(resolveLocation('/areas/system'))).toBe('system')
    expect(activeAreaId(resolveLocation('/settings'))).toBe('system')
    expect(activeAreaId(resolveLocation('/'))).toBeNull()
  })
})

describe('visibility is the union of authorized leaves', () => {
  it('shows an area iff one of its leaves is authorized, with no parent permission', () => {
    expect(visibleAreas(allow).map((a) => a.id)).toEqual(
      NAV_AREAS.map((a) => a.id),
    )
    // /dashboards carries no permission, so Observability alone survives an empty set.
    expect(visibleAreas(deny).map((a) => a.id)).toEqual(['observation'])
    // A principal holding ONLY the binding read: the AI area appears, with that one door.
    // The permission-only adapter: these cases are about the AREA algebra, not about
    // where a view's answer comes from. Production builds its gate in
    // features/navigation/authorization.ts.
    const bindingOnly = permissionGate(
      (p: string) => p === 'sessions:profile-binding:read',
    )
    // AI through that one door, Observability through the unpermissioned /dashboards.
    expect(visibleAreas(bindingOnly).map((a) => a.id)).toEqual([
      'ai',
      'observation',
    ])
    expect(authorizedSections('ai', bindingOnly)).toEqual([
      {
        sectionId: 'environments',
        views: [expect.objectContaining({ id: 'providerBindings' })],
      },
    ])
    // The sibling door needs its OWN permission; the union never lends it.
    expect(
      authorizedSections('ai', bindingOnly)
        .flatMap((s) => s.views)
        .some((v) => v.id === 'providerProfiles'),
    ).toBe(false)
  })

  it('treats an unpermissioned leaf as open to every signed-in principal', () => {
    // /dashboards has no permission: Observability is visible even to a principal
    // whose set is otherwise empty.
    expect(isAreaVisible('observation', deny)).toBe(true)
    expect(
      authorizedSections('observation', deny).flatMap((s) =>
        s.views.map((v) => v.id),
      ),
    ).toEqual(['dashboards'])
  })
})

describe('search ranking — one index for the sidebar and the palette', () => {
  const index = buildNavSearchIndex(t)
  const ids = (q: string) => rankNavMatches(index, q).map((e) => e.id)

  it('indexes every area, every listed view and the Settings utility, in registry order', () => {
    expect(index.filter((e) => e.kind === 'area')).toHaveLength(9)
    const listed = FEATURE_VIEWS.filter((v) => !v.hideInNav).length
    expect(index.filter((e) => e.kind === 'view')).toHaveLength(listed)
    expect(index.filter((e) => e.kind === 'settings')).toHaveLength(1)
    expect(index.map((e) => e.order)).toEqual(index.map((_, i) => i))
    expect(rankNavMatches(index, '   ')).toEqual(index)
  })

  it('finds "sessions" and "sesion" (folded) with both session doors first, and "/agentops" by path', () => {
    const hits = ids('sessions')
    expect(hits.slice(0, 2)).toEqual(['sessions', 'agentops'])
    expect(hits).toContain('providerProfiles')
    expect(ids('/agentops')[0]).toBe('agentops')
    // Spanish, with and without the accent, folds to the same needle.
    expect(fold('Sesión')).toBe(fold('sesion'))
  })

  it('finds the sessions doors by their former name "Claude Code" without eclipsing governance and adoption', () => {
    const hits = ids('claude code')
    expect(hits).toContain('agentops')
    expect(hits).toContain('claudePolicy')
    expect(hits).toContain('adoption')
    // Label hits rank above the former-name hit: governance and adoption carry the words.
    expect(hits.indexOf('claudePolicy')).toBeLessThan(hits.indexOf('agentops'))
  })

  it('finds Administration by its former name "Control console"', () => {
    expect(ids('control console')[0]).toBe('console')
  })

  it('finds profiles and identity surfaces by label, English fallback and noun', () => {
    expect(ids('profiles')[0]).toBe('providerProfiles')
    expect(ids('identity')[0]).toBe('identity')
    // The NOUN "identities" reaches every surface that manages identities, the
    // administration console among them, exactly as the sidebar filter always did.
    expect(ids('identities')).toEqual(
      expect.arrayContaining([
        'identity',
        'permissions',
        'accessMap',
        'console',
      ]),
    )
  })

  it('ranks an exact label above a prefix, a prefix above a word, and a description last', () => {
    // "models" is the exact label of /models; "Model Operations" is a word-prefix hit;
    // several descriptions mention models.
    const hits = ids('models')
    expect(hits[0]).toBe('models')
    expect(hits.indexOf('modelOps')).toBeLessThan(hits.indexOf('finops'))
    // A description-only hit still appears, after every label hit.
    expect(hits).toContain('finops')
  })

  it('finds an area by its own name and by a section name', () => {
    // The module labelled exactly "Security" outranks the area whose label merely starts
    // with the word; the area is the very next hit.
    const security = rankNavMatches(index, 'security')
    expect(security[0]).toMatchObject({ kind: 'view', id: 'security' })
    expect(security[1]).toMatchObject({ kind: 'area', id: 'security-identity' })
    expect(
      rankNavMatches(index, 'provider reference').some(
        (e) => e.kind === 'area' && e.id === 'ai',
      ),
    ).toBe(true)
  })

  it('keeps the tie-break stable: equal scores stay in registry order', () => {
    const a = ids('sessions')
    const b = ids('sessions')
    expect(a).toEqual(b)
    const observe = index.find((e) => e.id === 'sessions')!
    const operate = index.find((e) => e.id === 'agentops')!
    expect(observe.order).toBeLessThan(operate.order)
  })

  it('folds Latin accents and leaves Russian and Japanese letters untouched', () => {
    expect(fold('Prüfung')).toBe('prufung')
    expect(fold('Задачи')).toBe('задачи')
    expect(fold('が')).toBe('が')
  })

  it('shows Area › Section beside every module result', () => {
    const agentops = index.find((e) => e.id === 'agentops')!
    expect(agentops.context).toBe('AI › Sessions')
    const settings = index.find((e) => e.kind === 'settings')!
    expect(settings.context).toBe('System & settings › Personal preferences')
  })
})
