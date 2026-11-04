// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { createRouter } from '@tanstack/react-router'
import { Terminal } from 'lucide-react'
import { describe, expect, it, vi } from 'vitest'

vi.mock('./extensions', async () => {
  const { default: i18n, LANGUAGE_CODES } = await import('@/lib/i18n')
  for (const lng of LANGUAGE_CODES) {
    i18n.addResource(lng, 'nav', 'items.extension-fixture', 'Extension fixture')
    i18n.addResource(lng, 'nav', 'items.finops', 'FinOps')
    i18n.addResource(lng, 'nav', 'items.redteam', 'Red team')
    i18n.addResource(lng, 'nav', 'items.team-costs', 'Team costs')
    i18n.addResource(lng, 'nav', 'items.recordings', 'Recordings')
    i18n.addResource(lng, 'nav', 'items.session-viewer', 'Session viewer')
  }
  const historicalExtensions = [
    { id: 'finops', path: '/finops', heading: 'FinOps' },
    { id: 'redteam', path: '/red-team', heading: 'Red team' },
    { id: 'team-costs', path: '/team-costs', heading: 'Team costs' },
    { id: 'recordings', path: '/recordings', heading: 'Recordings' },
    {
      id: 'session-viewer',
      path: '/session-viewer/$id',
      heading: 'Session viewer',
    },
  ]
  // The Business extension registers the pages of shared source itself, from the same
  // business-views.tsx files: the registry serves none of them for it.
  const sharedBusinessViews = Object.values(
    import.meta.glob<readonly object[]>('./*/business-views.tsx', {
      eager: true,
      import: 'VIEWS',
    }),
  ).flat()
  return {
    LAUNCH_IDENTITY_EXTENSION: undefined,
    PANEL_EXTENSIONS: {
      identityTabs: [],
      governanceTabs: [],
      executiveRiskCards: [],
      capabilitiesTabs: [],
      complianceTabs: [],
      reportingCards: [],
      licenseCards: [],
      scopesCards: [],
    },
    FEATURE_EXTENSIONS: [
      ...sharedBusinessViews,
      ...historicalExtensions.map(({ id, path }) => ({
        id,
        path,
        hub: 'operate',
        icon: Terminal,
        helpHref: '/',
        navigation: { kind: 'feature', areaId: 'ai', sectionId: 'sessions' },
        permission: 'fixture:read',
        element: () => null,
      })),
      {
        id: 'extension-fixture',
        path: '/extension-fixture',
        hub: 'operate',
        icon: Terminal,
        helpHref: '/',
        navigation: { kind: 'feature', areaId: 'ai', sectionId: 'sessions' },
        permission: 'fixture:read',
        // Deliberately out of product order, with a repeat.
        nouns: ['identities', 'sessions', 'sessions'],
        element: () => null,
      },
    ],
    EXTENSION_ROUTES: [
      ...historicalExtensions,
      // A Business build serves the pages whose source is shared (business-views.tsx).
      { id: 'compliance', path: '/compliance', heading: 'Compliance' },
      {
        id: 'extension-fixture',
        path: '/extension-fixture',
        heading: 'Extension fixture',
      },
    ],
    ANONYMOUS_FEATURE_EXTENSIONS: [
      {
        id: 'anonymous-extension-fixture',
        path: '/sign-in-fixture',
        element: () => null,
        loginLabel: () => 'Sign in with fixture',
      },
    ],
    ANONYMOUS_EXTENSION_ROUTES: [
      {
        id: 'anonymous-extension-fixture',
        path: '/sign-in-fixture',
        heading: 'Fixture sign-in',
      },
    ],
  }
})

import { routeTree } from '@/app/routes'
import { BUSINESS_BUILD } from './business-build'
import { FEATURE_EXTENSIONS } from './extensions'
import {
  BUSINESS_ONLY_VIEW_IDS,
  FEATURE_VIEWS,
  PRODUCT_NOUNS,
  ROUTE_ALIASES,
  nounsForView,
  type ViewEntry,
} from './registry'
import { ANONYMOUS_VIEWS } from './anonymous-registry'
import { AUTH_ROUTES, PUBLIC_ROUTES, ROUTES } from '../../e2e-visual/routes'
// Run the existing guards against the non-empty composition as well.
import './registry.route-conservation.test'
import './registry.immutability.test'
import './registry.capture-coverage.test'
import './registry.a11y-coverage.test'
import './registry.nav-labels.test'
import './anonymous-registry.test'

describe('composed console extensions', () => {
  it('registers the Business pages of shared source exactly once, after every built-in view, in the extension order', () => {
    expect(BUSINESS_BUILD).toBe(true)
    const builtIns = FEATURE_VIEWS.slice(
      0,
      FEATURE_VIEWS.length - FEATURE_EXTENSIONS.length,
    ).map((view) => (view as ViewEntry).order)
    expect(builtIns).toEqual([...builtIns].sort((a, b) => a - b))
    expect(FEATURE_VIEWS.slice(builtIns.length).map((view) => view.id)).toEqual(
      FEATURE_EXTENSIONS.map((view) => view.id),
    )
    for (const id of ['compliance']) {
      expect(
        FEATURE_VIEWS.filter((view) => view.id === id),
        id,
      ).toHaveLength(1)
    }
    const orders = FEATURE_VIEWS.filter((view) =>
      BUSINESS_ONLY_VIEW_IDS.includes(view.id),
    ).map((view) => view.path)
    expect(orders.sort()).toEqual(['/compliance'])
  })
  it('mounts historical extension destinations once instead of their Community aliases', () => {
    const router = createRouter({ routeTree })
    for (const path of ['/finops', '/red-team', '/team-costs']) {
      expect(
        Object.values(router.routesById).filter(
          (route) => route.fullPath === path,
        ),
      ).toHaveLength(1)
      expect(ROUTE_ALIASES.some((alias) => alias.from === path)).toBe(false)
    }
  })
  it('composes extension objects into the existing thirteen nouns once', () => {
    expect(PRODUCT_NOUNS).toHaveLength(13)
    expect(nounsForView('extension-fixture')).toEqual([
      'sessions',
      'identities',
    ])
    for (const noun of PRODUCT_NOUNS) {
      expect(
        noun.views.filter((id) => id === 'extension-fixture'),
      ).toHaveLength(['sessions', 'identities'].includes(noun.id) ? 1 : 0)
    }
    expect(nounsForView('sessions')).toEqual(['sessions'])
    expect(nounsForView('unknown-view')).toEqual([])
  })
  it('mounts, seals and qualifies the extra route', () => {
    const view = FEATURE_VIEWS.find(({ id }) => id === 'extension-fixture')!
    expect(Object.isFrozen(view)).toBe(true)
    expect(view.permission).toBe('fixture:read')
    expect(Object.keys(createRouter({ routeTree }).routesById)).toContain(
      '/app/extension-fixture',
    )
    expect(AUTH_ROUTES).toContain('/extension-fixture')
  })

  it('mounts the anonymous route outside the authenticated shell', () => {
    const ids = Object.keys(createRouter({ routeTree }).routesById)
    expect(ids).toContain('/sign-in-fixture')
    expect(ids).not.toContain('/app/sign-in-fixture')
    expect(PUBLIC_ROUTES).toContain('/sign-in-fixture')
    expect(AUTH_ROUTES).not.toContain('/sign-in-fixture')
    expect(
      FEATURE_VIEWS.some(({ id }) => id === 'anonymous-extension-fixture'),
    ).toBe(false)
    expect(ANONYMOUS_VIEWS).toHaveLength(1)
    expect(Object.isFrozen(ANONYMOUS_VIEWS[0])).toBe(true)
  })

  it('qualifies authenticated extensions in Playwright without scanning anonymous routes', () => {
    expect(ROUTES).toContain('/extension-fixture')
    expect(ROUTES).not.toContain('/sign-in-fixture')
  })
})
