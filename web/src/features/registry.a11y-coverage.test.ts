// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The AT gate (`pnpm -C web at:gate` → e2e-visual/at-run.ts) scans AUTH_ROUTES ∪
// PUBLIC_ROUTES. AUTH_ROUTES is derived from route-census.json; this file proves the
// derivation scans exactly what the authenticated shell mounts, and nothing else.
import { createRouter } from '@tanstack/react-router'
import { describe, expect, it } from 'vitest'
import { routeTree } from '@/app/routes'
import {
  AUTH_ROUTES,
  PUBLIC_ROUTES,
  ROUTES,
  SESSION_VIEWER_ROUTE,
} from '../../e2e-visual/routes'
import { ANONYMOUS_VIEWS } from './anonymous-registry'

const authRoutes = new Set<string>(AUTH_ROUTES)

/** Every path the authenticated shell mounts, with the session viewer's fixture id. */
function mountedAuthenticatedPaths(): string[] {
  const router = createRouter({ routeTree })
  return Object.keys(router.routesById)
    .filter((id) => id === '/app/' || id.startsWith('/app/'))
    .map((id) => (id === '/app/' ? '/' : id.slice(4)))
    .map((path) =>
      path === '/session-viewer/$id' ? SESSION_VIEWER_ROUTE : path,
    )
    .sort()
}

describe('authenticated accessibility route coverage', () => {
  it('scans exactly the routes the authenticated shell mounts', () => {
    const mounted = mountedAuthenticatedPaths()
    const unscanned = mounted.filter((route) => !authRoutes.has(route))
    const unmounted = AUTH_ROUTES.filter((route) => !mounted.includes(route))
    expect(
      [...AUTH_ROUTES].sort(),
      'AUTH_ROUTES (the AT gate inventory) differs from the routes the authenticated shell mounts' +
        `\n  not scanned: ${unscanned.join(', ')}\n  not mounted: ${unmounted.join(', ')}`,
    ).toEqual(mounted)
  })

  it('opens only concrete paths: a parametric route needs a fixture id', () => {
    expect(AUTH_ROUTES.filter((route) => route.includes('$'))).toEqual([])
  })

  it('keeps ROUTES a real subset: every Playwright ROUTES path is also in AUTH_ROUTES', () => {
    const orphaned = ROUTES.filter((route) => !authRoutes.has(route))

    expect(
      orphaned,
      `ROUTES (Playwright deep-interaction subset) references paths absent from AUTH_ROUTES:\n${orphaned.join('\n')}`,
    ).toEqual([])
  })
})

describe('anonymous accessibility route coverage', () => {
  const mounted = new Set(
    Object.keys(createRouter({ routeTree }).routesById).filter(
      (id) => id !== '__root__' && id !== '/app' && !id.startsWith('/app/'),
    ),
  )

  it('scans every mounted public route in an anonymous browser context', () => {
    expect(
      [...mounted].filter((path) => !PUBLIC_ROUTES.includes(path)),
    ).toEqual([])
    expect(PUBLIC_ROUTES.filter((path) => !mounted.has(path))).toEqual([])
  })

  it('keeps anonymous views out of the authenticated gate inventory', () => {
    expect(
      ANONYMOUS_VIEWS.filter(({ path }) => AUTH_ROUTES.includes(path)),
    ).toEqual([])
    expect(PUBLIC_ROUTES.filter((path) => AUTH_ROUTES.includes(path))).toEqual(
      [],
    )
  })
})
