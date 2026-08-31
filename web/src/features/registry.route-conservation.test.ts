// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE CONSERVATION GATE: this file goes RED if a published route stops resolving.
//
// It is the one artefact of the five-hub change meant to outlive it. That change moved
// zero paths, but the NEXT navigation change will not know it unless something enforces
// it, and "I counted my own diff" — how #680 established its 0-routes-deleted claim — is
// not something the next session inherits.
//
// WHAT IT MEASURES AGAINST, and this is the whole design. The live set is walked off the
// BUILT ROUTER (`routesById`), never off FEATURE_VIEWS. An earlier draft of this file
// used the registry, and the adversarial contrast killed it in one line: the registry
// cannot be both the implementation and the only oracle that the router implemented it.
// Filter `featureRoutes`, or drop a child from `addChildren` (app/routes.tsx), and a
// registry-based check keeps every assertion's value while the url 404s. Walking the
// router closes that, and it also brings in the five routes FEATURE_VIEWS never had:
// /login, /setup, /accept-invite, /status-page and /settings.
//
// What it can see that the older guards cannot. registry.a11y-coverage.test.ts and
// registry.nav-labels.test.ts are both RELATIVE — registry against AUTH_ROUTES, registry
// against the seven nav.json — and a commit deleting a view from all of them leaves every
// comparison true. route-census.json is the fixed point: it records what was published,
// so the comparison survives the deletion of everything it compares against. Measured by
// mutation: a coordinated delete compiles clean (tsc rc=0), leaves both older guards
// green, and reddens only this file.
import { createRouter } from '@tanstack/react-router'
import { describe, expect, it } from 'vitest'
import { routeTree } from '@/app/routes'
import census from './route-census.json'
import { auditRouteCensus } from './route-census'
import { ROUTE_ALIASES } from './registry'

/**
 * Every path the router actually serves. `routesById` keys carry the pathless shell's
 * `/app` prefix (`/app/audit`), and the layout itself and `__root__` are not routes an
 * operator can reach.
 */
function mountedPaths(): string[] {
  const router = createRouter({ routeTree })
  return Object.keys(router.routesById)
    .filter((id) => id !== '__root__' && id !== '/app')
    .map((id) => (id === '/app/' ? '/' : id.startsWith('/app/') ? id.slice(4) : id))
    .sort()
}

const live = mountedPaths()
const report = auditRouteCensus({
  census: census.paths,
  live,
  aliases: ROUTE_ALIASES,
})

describe('route conservation', () => {
  it('mounts a non-trivial number of routes, so the comparison is never vacuous', () => {
    // Both sides empty would satisfy every assertion below. If the router failed to build
    // or the JSON came back without `paths`, that must read as broken, not as clean.
    expect(census.paths.length).toBeGreaterThanOrEqual(50)
    expect(live.length).toBeGreaterThanOrEqual(50)
  })

  it('still resolves every path this console has ever published', () => {
    expect(
      report.vanished,
      `These paths are in route-census.json and the ROUTER no longer mounts them.\n` +
        `A screen surviving under a new url is NOT conservation: a bookmark, a runbook\n` +
        `deep link and a docs cross-reference all point at the OLD string.\n\n` +
        `  ${report.vanished.join('\n  ')}\n\n` +
        `Fix by restoring the route, or — if it genuinely moved — add a ROUTE_ALIASES\n` +
        `entry in registry.tsx so the old path redirects to the new one. Do NOT delete\n` +
        `the census line: that is the record that it was ever promised.`,
    ).toEqual([])
  })

  it('has no redirect that lands on nothing', () => {
    expect(
      report.dangling,
      `Declared redirects whose target no route mounts (dead end or cycle):\n` +
        `  ${report.dangling.join('\n  ')}\n` +
        `An alias to a route that does not exist is a 404 with extra steps.`,
    ).toEqual([])
  })

  it('has no redirect shadowed by a mounted route', () => {
    expect(
      report.shadowed,
      `These paths are BOTH mounted and declared retired in ROUTE_ALIASES:\n` +
        `  ${report.shadowed.join(', ')}\n` +
        `The redirect can never fire, and the router aborts the whole tree with\n` +
        `"Duplicate routes found with id". Remove whichever of the two is stale.`,
    ).toEqual([])
  })

  it('has no two redirects claiming the same source', () => {
    expect(
      report.duplicated,
      `Two ROUTE_ALIASES entries share a "from": ${report.duplicated.join(', ')}\n` +
        `One silently wins, and the router refuses to build the tree.`,
    ).toEqual([])
  })

  it('has no redirect that cannot supply its target params', () => {
    expect(
      report.paramMismatch,
      `These redirects need a path param the source does not carry:\n` +
        `  ${report.paramMismatch.join('\n  ')}\n` +
        `The redirect would build a url with "undefined" in it. Keep the param NAME\n` +
        `identical across the move, or add an explicit params mapping.`,
    ).toEqual([])
  })

  it('records every route it mounts, so the census stays total', () => {
    expect(
      report.unrecorded,
      `Routes the router mounts but route-census.json does not list:\n` +
        `  ${report.unrecorded.join('\n  ')}\n` +
        `Append them (sorted). An unrecorded route is unprotected: delete it tomorrow\n` +
        `and this gate stays green, which is the exact hole the census closes.`,
    ).toEqual([])
  })

  it('keeps the census free of duplicates', () => {
    const dupes = census.paths.filter((p, i) => census.paths.indexOf(p) !== i)
    expect(dupes, `route-census.json lists these twice: ${dupes.join(', ')}`).toEqual([])
  })
})
