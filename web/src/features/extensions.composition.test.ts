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
  }
  return {
    FEATURE_EXTENSIONS: [
      {
        id: 'extension-fixture',
        path: '/extension-fixture',
        hub: 'operate',
        icon: Terminal,
        helpHref: '/',
        navigation: { kind: 'feature', areaId: 'ai', sectionId: 'sessions' },
        permission: 'fixture:read',
        element: () => null,
      },
    ],
    EXTENSION_ROUTES: [
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
import { FEATURE_VIEWS } from './registry'
import { ANONYMOUS_VIEWS } from './anonymous-registry'
import { AUTH_ROUTES, PUBLIC_ROUTES } from '../../e2e-visual/routes'
// Run the existing guards against the non-empty composition as well.
import './registry.route-conservation.test'
import './registry.immutability.test'
import './registry.capture-coverage.test'
import './registry.a11y-coverage.test'
import './registry.nav-labels.test'
import './anonymous-registry.test'

describe('composed console extensions', () => {
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
})
