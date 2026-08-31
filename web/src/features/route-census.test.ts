// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The checker's own battery, on SYNTHETIC data.
//
// registry.route-conservation.test.ts runs `auditRouteCensus` against the real registry,
// where it must always be green — so it can only ever prove the "nothing is wrong"
// branch. Every branch that FIRES on a defect is exercised here instead, and each case
// carries its non-firing twin: a checker that reports `vanished` for everything passes
// "reports a deleted route" just as well as a correct one, and only the negative case
// tells the two apart.
//
// The alias branches especially. ROUTE_ALIASES is empty today (moved zero paths),
// so against real data `resolve()` is never once entered — its bugs would be invisible
// until the first session that moves a path, which is the worst possible moment to find
// them. These fixtures are the only thing standing under that code.
import { describe, expect, it } from 'vitest'
import { auditRouteCensus, type RouteAlias } from './route-census'

const alias = (from: string, to: string): RouteAlias => ({ from, to, note: 'fixture' })

describe('auditRouteCensus', () => {
  const CLEAN = {
    vanished: [],
    dangling: [],
    unrecorded: [],
    shadowed: [],
    duplicated: [],
    paramMismatch: [],
  }

  it('is silent when the router still mounts every censused path', () => {
    expect(
      auditRouteCensus({ census: ['/a', '/b'], live: ['/a', '/b'], aliases: [] }),
    ).toEqual(CLEAN)
  })

  it('is silent for a HEALTHY alias — the non-firing twin of every alias check', () => {
    // Without this, a checker that flagged `shadowed`/`duplicated`/`paramMismatch` on
    // every alias would pass all the firing cases below and still be useless.
    expect(
      auditRouteCensus({
        census: ['/old/$id', '/new/$id'],
        live: ['/new/$id'],
        aliases: [alias('/old/$id', '/new/$id')],
      }),
    ).toEqual(CLEAN)
  })

  it('REPORTS two aliases claiming the same source', () => {
    // `new Map(aliases)` keeps only one of them, so a string-graph check reports a clean
    // tree for a pair the router refuses to build ("Duplicate routes found with id").
    const r = auditRouteCensus({
      census: ['/old'],
      live: ['/a', '/b'],
      aliases: [alias('/old', '/a'), alias('/old', '/b')],
    })
    expect(r.duplicated).toEqual(['/old'])
  })

  it('REPORTS a redirect whose target param the source cannot supply', () => {
    // `/old/$oldId → /new/$newId` resolves fine as strings and builds `/new/undefined`.
    const r = auditRouteCensus({
      census: ['/old/$oldId'],
      live: ['/new/$newId'],
      aliases: [alias('/old/$oldId', '/new/$newId')],
    })
    expect(r.paramMismatch).toEqual(['/old/$oldId → /new/$newId'])
    // Renaming nothing is fine — the same shape with a shared param name is silent.
    expect(
      auditRouteCensus({
        census: ['/old/$id'],
        live: ['/new/$id'],
        aliases: [alias('/old/$id', '/new/$id')],
      }).paramMismatch,
    ).toEqual([])
  })

  it('REPORTS an alias shadowing a route outside the feature registry', () => {
    // `/settings` and the public legs are mounted too. When `live` came from
    // FEATURE_VIEWS this returned a clean report while the router aborted on a
    // duplicate id — which is why `live` must be walked off the built router.
    const r = auditRouteCensus({
      census: ['/settings'],
      live: ['/settings', '/login', '/'],
      aliases: [alias('/settings', '/')],
    })
    expect(r.shadowed).toEqual(['/settings'])
  })

  it('REPORTS a censused path the registry no longer serves', () => {
    const r = auditRouteCensus({ census: ['/a', '/b'], live: ['/a'], aliases: [] })
    expect(r.vanished).toEqual(['/b'])
  })

  it('reports the SAME loss when the route is deleted from every other file too', () => {
    // The defect the two existing cross-file guards cannot see: a coordinated delete.
    // `live` here is what the registry, routes.ts and nav.json all agree on after a
    // tidy-up commit — they are consistent with each other and wrong together.
    const r = auditRouteCensus({ census: ['/a', '/b', '/c'], live: ['/a'], aliases: [] })
    expect(r.vanished).toEqual(['/b', '/c'])
  })

  it('accepts a retired path that is ALIASED to a live one', () => {
    const r = auditRouteCensus({
      census: ['/old'],
      live: ['/new'],
      aliases: [alias('/old', '/new')],
    })
    expect(r.vanished).toEqual([])
    expect(r.dangling).toEqual([])
    // The non-firing twin: /new is live but absent from the census, so the census is no
    // longer total. Silence here would let the list rot into a stale subset.
    expect(r.unrecorded).toEqual(['/new'])
  })

  it('follows a CHAIN of aliases to a live path', () => {
    const r = auditRouteCensus({
      census: ['/v1'],
      live: ['/v3'],
      aliases: [alias('/v1', '/v2'), alias('/v2', '/v3')],
    })
    expect(r.vanished).toEqual([])
    expect(r.dangling).toEqual([])
  })

  it('REPORTS an alias that lands on nothing', () => {
    const r = auditRouteCensus({
      census: ['/old'],
      live: ['/other'],
      aliases: [alias('/old', '/gone')],
    })
    // Both faces of one defect: the redirect is dead, and the path it was meant to
    // rescue is therefore still lost. A checker reporting only `dangling` would let a
    // reviewer read "one broken redirect" instead of "one lost function".
    expect(r.dangling).toEqual(['/old → /gone'])
    expect(r.vanished).toEqual(['/old'])
  })

  it('REPORTS a cycle instead of hanging on it', () => {
    const r = auditRouteCensus({
      census: ['/a'],
      live: ['/live'],
      aliases: [alias('/a', '/b'), alias('/b', '/a')],
    })
    expect(r.vanished).toEqual(['/a'])
    expect(r.dangling).toEqual(['/a → /b', '/b → /a'])
  })

  it('REPORTS a live path that nobody recorded in the census', () => {
    const r = auditRouteCensus({ census: ['/a'], live: ['/a', '/new'], aliases: [] })
    expect(r.unrecorded).toEqual(['/new'])
    expect(r.vanished).toEqual([])
  })

  it('REPORTS an alias shadowed by a live route of the same path', () => {
    // `/a` is both served and declared retired: the redirect can never fire, and which
    // one the author meant is unknowable from the files.
    const r = auditRouteCensus({
      census: ['/a'],
      live: ['/a', '/b'],
      aliases: [alias('/a', '/b')],
    })
    expect(r.shadowed).toEqual(['/a'])
  })

  it('does not confuse a path with a prefix of another', () => {
    // `/work` and `/workspace` differ by a suffix; a `startsWith` implementation would
    // call the census satisfied when /work had actually gone.
    const r = auditRouteCensus({
      census: ['/work', '/workspace'],
      live: ['/workspace'],
      aliases: [],
    })
    expect(r.vanished).toEqual(['/work'])
  })
})
