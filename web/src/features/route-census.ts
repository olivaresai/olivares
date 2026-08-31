// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ROUTE CONSERVATION — the check that makes "no route was lost" a MEASUREMENT.
//
// The five-hub reorganisation is the exact shape of change where functions disappear
// without anyone noticing: a view loses its heading, then its entry, then its file, and
// each step looks like tidying. Canon §1.1 — "la solución a un problema NUNCA es
// eliminar una función" — forbids the outcome; this module is how we can TELL.
//
// Why a committed census and not a cross-file guard. Two guards already compare the
// registry against another file: registry.a11y-coverage.test.ts (FEATURE_VIEWS ↔
// AUTH_ROUTES, both directions) and registry.nav-labels.test.ts (FEATURE_VIEWS ↔ the
// seven nav.json). Both are RELATIVE — they prove two files agree. Delete a view from
// both in one commit and both stay green, because the thing they measure each other
// against moved too. #680 hit precisely this and had to verify "0 routes deleted" BY
// HAND, counting its own diff; a hand-count does not survive the session that made it.
//
// The census is ABSOLUTE: a frozen list of every path this console has ever published,
// checked against the registry of today. Deleting a route from every file in the tree
// still fails, because the census is the one file the deletion has no reason to touch —
// and if a future session deletes the census entry too, that edit is the confession, in
// the diff, in a file whose only purpose is to be hard to edit innocently.
//
// A retired path is retired through ROUTE_ALIASES, never by dropping its census line:
// the old URL keeps resolving (app/routes.tsx mounts a redirect for each alias), so a
// bookmark, a runbook deep link or a docs cross-reference still lands. That is the
// difference between moving a door and bricking it up.

/** A path this console used to serve, and the live path it now lands on. */
export interface RouteAlias {
  /** The retired path. Still served — it redirects, it does not 404. */
  from: string
  /** Where it lands. Must resolve (directly or through further aliases) to a live path. */
  to: string
  /** Which session retired it and why — the diff should not need archaeology. */
  note: string
}

export interface CensusInput {
  /** Every path ever published (route-census.json). */
  census: readonly string[]
  /**
   * Every path the application ACTUALLY MOUNTS today.
   *
   * ⚠ This must come from the built router, never from FEATURE_VIEWS. Deriving it from
   * the registry would make the registry both the implementation and the only oracle
   * that the router implemented it — and then `featureRoutes` could be filtered, or a
   * child dropped from `addChildren`, and every assertion here would keep its value
   * while the route 404-ed. registry.route-conservation.test.ts walks `routesById`.
   */
  live: readonly string[]
  /** Declared redirects from retired paths to live ones. */
  aliases: readonly RouteAlias[]
}

export interface CensusReport {
  /** Census paths that are neither live nor aliased to something live. FUNCTION LOST. */
  vanished: string[]
  /** Aliases whose target does not resolve to a live path (dead redirect / cycle). */
  dangling: string[]
  /** Live paths missing from the census — append them, or the census stops being total. */
  unrecorded: string[]
  /** Aliases whose `from` is ALSO mounted: the redirect is unreachable, and the router
   *  aborts the whole tree with "Duplicate routes found with id". */
  shadowed: string[]
  /** Two aliases claiming the same `from`: one silently wins, and the tree may not build. */
  duplicated: string[]
  /** Aliases whose target needs a path param the source cannot supply — the redirect
   *  builds a url with `undefined` in it. */
  paramMismatch: string[]
}

/** Path params in a TanStack pattern: `/session-viewer/$id` → ['id']. */
function params(path: string): string[] {
  return [...path.matchAll(/\$([A-Za-z0-9_]+)/g)].map((m) => m[1])
}

/** Follow `from → to` hops to the live path they end at, or null (dead end / cycle). */
function resolve(
  path: string,
  live: ReadonlySet<string>,
  byFrom: ReadonlyMap<string, string>,
): string | null {
  const walked = new Set<string>()
  let cur = path
  // Bounded by the alias count: every hop consumes one unvisited `from`.
  while (!live.has(cur)) {
    if (walked.has(cur)) return null // cycle
    walked.add(cur)
    const next = byFrom.get(cur)
    if (next === undefined) return null // dead end
    cur = next
  }
  return cur
}

/**
 * Audit one census against one registry. Pure — no imports of the real data — so the
 * test can drive every branch with synthetic fixtures. That matters here: the real
 * alias list is EMPTY today (moved zero paths), so a check written only against
 * real data would never once execute the alias branch, and an alias-handling bug would
 * ship untested behind a permanently-true condition.
 */
export function auditRouteCensus(input: CensusInput): CensusReport {
  const live = new Set(input.live)
  const byFrom = new Map(input.aliases.map((a) => [a.from, a.to]))

  const vanished = input.census
    .filter((p) => !live.has(p))
    .filter((p) => resolve(p, live, byFrom) === null)

  const dangling = input.aliases
    .filter((a) => resolve(a.to, live, byFrom) === null)
    .map((a) => `${a.from} → ${a.to}`)

  const censusSet = new Set(input.census)
  const unrecorded = input.live.filter((p) => !censusSet.has(p))

  const shadowed = input.aliases.filter((a) => live.has(a.from)).map((a) => a.from)

  // `new Map` silently keeps ONE of two entries sharing a `from`, so the checker would
  // report a clean tree for a pair the router refuses to build.
  const duplicated = input.aliases
    .map((a) => a.from)
    .filter((f, i, all) => all.indexOf(f) !== i)

  // `/old/$oldId → /new/$newId` type-checks and resolves as a string graph, but the
  // redirect has no `oldId` to hand to `newId` and builds `/new/undefined`.
  const paramMismatch = input.aliases
    .filter((a) => params(a.to).some((p) => !params(a.from).includes(p)))
    .map((a) => `${a.from} → ${a.to}`)

  return { vanished, dangling, unrecorded, shadowed, duplicated, paramMismatch }
}
