// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { readdirSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import census from '@/features/route-census.json'

const GATE = join(__dirname, '..', 'e2e-release')

/** The Business pages as app/pages/not-found.tsx reads the census: a `$param` segment stands for
 * any one segment, and the case does not matter. */
const BUSINESS = census.business_paths.map(
  (path) => new RegExp(`^${path.replace(/\$\w+/g, '[^/]+')}$`, 'i'),
)

/** Every address a spec writes as a literal, in any quote: visit('/x'), goto(`/x?y=1`),
 * ['08-sessions', "/x"]. The query and the trailing slash are dropped, as the notice does.
 * ponytail: an address built at run time (stepHref(...), goto(`/x/${id}`)) is not seen;
 * walk the router instead if a gate stage ever reaches a page that way. */
function addressesIn(src: string): string[] {
  return [
    ...src.matchAll(/(['"`])(\/[a-z][a-z0-9/-]*)(?:[?#][^'"`]*)?\1/gi),
  ].map((match) => match[2].replace(/\/+$/, ''))
}

/** The addresses of `src` that a Community console answers with the Business notice, which
 * paints no top bar: the release gate reads the version from the top bar's identity, so a
 * stage that opens one fails with "release version must be visible in the console identity". */
function businessAddressesIn(src: string): string[] {
  return addressesIn(src).filter((address) =>
    BUSINESS.some((pattern) => pattern.test(address)),
  )
}

const gateSources = readdirSync(GATE)
  .filter((name) => name.endsWith('.ts'))
  .map((name) => ({ name, src: readFileSync(join(GATE, name), 'utf8') }))

describe('the Community release gate', () => {
  it('reads the gate and the census, so the checks below are not vacuous', () => {
    expect(BUSINESS.length).toBeGreaterThan(0)
    const opened = gateSources.flatMap(({ src }) => addressesIn(src))
    expect(opened).toContain('/sessions')
    expect(opened).toContain('/onboarding')
  })

  it.each(gateSources)(
    '$name opens only pages a Community console serves',
    ({ src }) => {
      expect(businessAddressesIn(src)).toEqual([])
    },
  )

  it.each([
    ['a Business page', "await stage('06-deploy', () => visit('/deploy'))", 1],
    ['a Community page', "['08-sessions', '/sessions']", 0],
    ['a page with a parameter', "page.goto('/session-viewer/abc')", 1],
    [
      'the parameter page without its parameter',
      "page.goto('/session-viewer')",
      0,
    ],
    [
      'double quotes, a query and a trailing slash',
      'page.goto("/deploy/?tab=1")',
      1,
    ],
    ['a template literal', 'page.goto(`/recordings`)', 1],
    ['an address built at run time', 'page.goto(`/deploy/${id}`)', 0],
  ])('classifies %s', (_name, src, business) => {
    expect(businessAddressesIn(src)).toHaveLength(business)
  })
})
