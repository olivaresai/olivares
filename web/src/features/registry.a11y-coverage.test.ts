// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Keep the AT gate's route inventory in lockstep with the registry. The gate that
// actually runs in this environment (`pnpm -C web at:gate` → e2e-visual/at-run.ts)
// scans AUTH_ROUTES ∪ PUBLIC_ROUTES — it never consumes ROUTES (a Playwright-only
// subset) nor FEATURE_VIEWS. So the ONLY inventory that guarantees a view is axe-scanned
// is AUTH_ROUTES. This guard therefore validates AUTH_ROUTES directly (not a union with
// ROUTES): a union would let a view live solely in ROUTES and silently escape the gate
// while the guard still reported green — exactly the drift this test now forbids.
import { createRouter } from '@tanstack/react-router'
import { describe, expect, it } from 'vitest'
import { routeTree } from '@/app/routes'
import {
  AUTH_ROUTES,
  ROUTES,
  SESSION_VIEWER_ROUTE,
} from '../../e2e-visual/routes'
import { FEATURE_VIEWS, ROUTE_ALIASES } from './registry'

function concretePath(path: string): string {
  if (path === '/session-viewer/$id') return SESSION_VIEWER_ROUTE
  return path.replace(/\/\$[^/]+/g, '') || '/'
}

const authRoutes = new Set<string>(AUTH_ROUTES)

/**
 * Every route the authenticated shell ACTUALLY mounts, including the pinned
 * `/settings` utility that deliberately lives outside FEATURE_VIEWS. Deriving this
 * side from the built router makes an allowlist unnecessary: deleting a real route
 * or adding an unscanned one changes one side of the comparison, never both.
 *
 * Retired aliases are redirects, not screens for axe to exercise. Their live target
 * remains in this set and route-conservation separately proves the redirect resolves.
 */
function mountedAuthenticatedPaths(): string[] {
  const aliases = new Set(ROUTE_ALIASES.map(({ from }) => from))
  const router = createRouter({ routeTree })
  return Object.keys(router.routesById)
    .filter((id) => id === '/app/' || id.startsWith('/app/'))
    .map((id) => (id === '/app/' ? '/' : id.slice(4)))
    .filter((path) => !aliases.has(path))
    .map(concretePath)
    .sort()
}

const mountedAuthPaths = mountedAuthenticatedPaths()
const mountedAuth = new Set(mountedAuthPaths)

describe('FEATURE_VIEWS accessibility route coverage', () => {
  it('scans every registered view: each FEATURE_VIEW has a concrete route in AUTH_ROUTES', () => {
    const uncovered = FEATURE_VIEWS.map(({ id, path }) => ({
      id,
      path: concretePath(path),
    }))
      .filter(({ path }) => !authRoutes.has(path))
      .map(({ id, path }) => `${id}: ${path}`)

    expect(
      uncovered,
      `Registered views absent from the AT gate inventory (AUTH_ROUTES) — they would NOT be axe-scanned:\n${uncovered.join('\n')}`,
    ).toEqual([])
  })

  it('has no dead gate routes: every AUTH_ROUTES entry is mounted by the authenticated router', () => {
    const stale = AUTH_ROUTES.filter((route) => !mountedAuth.has(route))

    expect(
      stale,
      `AUTH_ROUTES entries that the authenticated router does not mount:\n${stale.join('\n')}`,
    ).toEqual([])
  })

  it('scans every mounted authenticated route, including static utilities outside FEATURE_VIEWS', () => {
    const unscanned = mountedAuthPaths.filter((route) => !authRoutes.has(route))

    expect(
      unscanned,
      `Mounted authenticated routes absent from the AT gate inventory (AUTH_ROUTES) — they would NOT be axe-scanned:\n${unscanned.join('\n')}`,
    ).toEqual([])
  })

  it('keeps ROUTES a real subset: every Playwright ROUTES path is also in AUTH_ROUTES', () => {
    const orphaned = ROUTES.filter((route) => !authRoutes.has(route))

    expect(
      orphaned,
      `ROUTES (Playwright deep-interaction subset) references paths absent from AUTH_ROUTES:\n${orphaned.join('\n')}`,
    ).toEqual([])
  })
})
