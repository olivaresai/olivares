// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE AREA ROUTES ARE LITERAL AND THIS PINS THEM TO NAV_AREAS (N1).
//
// app/routes.tsx writes the nine `/areas/*` routes out by hand so the census, the guide
// generator and the capture-coverage guard can read their paths as literals. The cost of a
// hand-written list is drift, and this file is the price paid once: the BUILT router's
// `/areas/*` set must equal NAV_AREAS in both directions. Delete one createRoute and the
// area is named here; add an area to the registry without a route and it is named here.
// Measured by mutation while writing it: removing `areaDeploymentRoute` from `areaRoutes`
// reddened this file and registry.route-conservation.test.ts (the census still lists it).
import { createRouter } from '@tanstack/react-router'
import { describe, expect, it } from 'vitest'
import { routeTree } from '@/app/routes'
import { NAV_AREAS } from '@/features/registry'

function mountedAreaPaths(): string[] {
  const router = createRouter({ routeTree })
  return Object.keys(router.routesById)
    .filter((id) => id.startsWith('/app/areas/'))
    .map((id) => id.slice(4))
    .sort()
}

describe('area directory routes', () => {
  it('mounts exactly the NAV_AREAS paths, under the authenticated shell', () => {
    expect(mountedAreaPaths()).toEqual([...NAV_AREAS.map((a) => a.path)].sort())
  })

  it('mounts no area route outside the shell', () => {
    const router = createRouter({ routeTree })
    const outside = Object.keys(router.routesById).filter(
      (id) => id.startsWith('/areas/') && !id.startsWith('/app/'),
    )
    expect(outside).toEqual([])
  })
})
