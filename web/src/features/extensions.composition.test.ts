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
  }
  const historicalExtensions = [
    { id: 'finops', path: '/finops', heading: 'FinOps' },
    { id: 'redteam', path: '/red-team', heading: 'Red team' },
    { id: 'team-costs', path: '/team-costs', heading: 'Team costs' },
  ]
  return {
    PANEL_EXTENSIONS: {
      capabilitiesTabs: [],
      complianceTabs: [],
      reportingCards: [],
      licenseCards: [],
      scopesCards: [],
    },
    FEATURE_EXTENSIONS: [
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
import {
  FEATURE_VIEWS,
  PRODUCT_NOUNS,
  ROUTE_ALIASES,
  nounsForView,
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
