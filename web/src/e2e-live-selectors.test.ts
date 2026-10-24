// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { readdirSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import sessions from '@/features/sessions/i18n/en.json'
import nav from '@/lib/i18n/locales/en/nav.json'

const WEB = join(__dirname, '..')
const E2E = join(WEB, 'e2e')

/** A spec with its whitespace collapsed: the formatter wraps a long selector across lines, and a
 * selector is the same selector however it is wrapped. */
function spec(name: string, dir = E2E): string {
  return readFileSync(join(dir, name), 'utf8').replace(/\s+/g, ' ')
}

/** Every browser spec directory (e2e, e2e-release, e2e-visual...), not only the demo one:
 * a spec outside `e2e/` signs in to the same console (#1520). */
const SPEC_DIRS = readdirSync(WEB, { withFileTypes: true })
  .filter((entry) => entry.isDirectory() && /^e2e(-|$)/.test(entry.name))
  .map((entry) => entry.name)

/** The nav catalogue read the other way round: the address a name is reached at. */
const NAV_NAME_BY_SEGMENT = nav.items as Record<string, string>
/** The overview's place in the top bar: the first link a sign-in paints. */
const ESTATE = nav.shell.journeys.home

/**
 * THE FIRST LINK A SPEC WAITS FOR AFTER SIGNING IN, AND WHETHER IT CAN BE PAINTED.
 *
 * ⛔ AND WHAT THIS DOES NOT SEE, SAID PLAINLY, because a guard read as more than it is
 *    becomes the reason nobody checks the real thing. It reads SOURCE TEXT — the spec
 *    files as characters — and it knows two facts: the English nav catalogue, and that
 *    a sign-in leaves the console on the root, which belongs to no area, so the top bar
 *    paints the places, the overview (Now) first, and no page of an area.
 *    It does not render the top bar, does not read the feature registry, and cannot say
 *    which area any given leaf belongs to. So the exemption is narrow ON PURPOSE: a
 *    navigation exempts the wait that the navigation's own address names in the nav
 *    catalogue, and nothing else. A spec that goes to one address and waits for a
 *    sibling of that area is held here too — deliberately, because this file cannot
 *    tell that sibling from a leaf of a third area, which is the defect it exists to
 *    stop. Widening it means teaching it the registry, not loosening the match.
 */
function waitsASignInCannotPaint(file: string, src: string): string[] {
  const overview = `name: '${ESTATE}', exact: true`
  const found: string[] = []
  for (const signIn of src.matchAll(/name: \/\^sign in\$\/i \}\)/g)) {
    const after = src.slice((signIn.index ?? 0) + signIn[0].length)
    const link = /getByRole\(\s*'link',\s*\{([^}]*)\}/.exec(after)
    // The shared locator is the same scoped overview wait, not a later journey link.
    const sharedOverview =
      /await\s+expect\(\s*journeyNowLink\(\s*page\s*\)\s*\)\s*\.toBeVisible\s*\(/.exec(
        after,
      )
    if (sharedOverview && (!link || sharedOverview.index < link.index)) continue
    if (!link) continue
    const body = link[1].replace(/\s+/g, ' ').trim()
    if (body === overview) continue
    // A navigation between the sign-in and the wait proves the area is open — but only
    // the area of the address it went to. The address's first segment is the nav key.
    const navigated = [
      ...after.slice(0, link.index).matchAll(/goto\(\s*'\/([^'/?#]*)/g),
    ].at(-1)
    const painted = navigated ? NAV_NAME_BY_SEGMENT[navigated[1]] : undefined
    if (painted && body.startsWith(`name: '${painted}'`)) continue
    found.push(`${file}: ${body}`)
  }
  return found
}

/**
 * THE OVERVIEW LINK READ OUTSIDE THE JOURNEYS NAVIGATION.
 *
 * ⛔ THE TOP BAR'S CURRENT-PAGE CRUMB WAS A LINK TOO (`BreadcrumbPage` is `role="link"`
 *    with `aria-current`), named like the page it marked, so a page-level
 *    `getByRole('link', { name, exact: true })` matched the place AND the crumb, and
 *    Playwright's strict mode rejected it before the assertion it was written for. The
 *    crumb went with the sidebar (#2544); a page's own links still share names with the
 *    places, so the wait stays scoped. Measured on the supported demo run
 *    (#1181): nine cases failed at their sign-in helper. The wait is read inside the
 *    Journeys navigation, as the specs that already passed read it.
 */
function overviewLinksOutsideJourneys(file: string, src: string): string[] {
  const label = nav.shell.journeysLabel
  const link = `getByRole('link', { name: '${ESTATE}', exact: true })`
  const inJourneys = [
    `getByRole('navigation', { name: '${label}', exact: true }).`,
    `getByLabel('${label}').`,
  ]
  // A chain the formatter wrapped over several lines is the same chain.
  const flat = src.replace(/\s+\./g, '.')
  const found: string[] = []
  for (let at = flat.indexOf(link); at >= 0; at = flat.indexOf(link, at + 1)) {
    const before = flat.slice(0, at)
    if (!inJourneys.some((scope) => before.endsWith(scope)))
      found.push(
        `${file}: ${flat.slice(Math.max(0, at - 60), at + link.length)}`,
      )
  }
  return found
}

describe('live end-to-end specs name what the console paints', () => {
  it('the operate portal is reached by its nav name and its heading', () => {
    // Read as one line: the formatter may wrap a locator's options over several lines.
    const src = spec('sessions-unified.spec.ts').replace(/\s+/g, ' ')
    // The top bar has one Sessions place, and /agentops is its second door: the spec
    // reads the place, which is what the top bar paints for either door.
    const destination = nav.shell.journeys.sessions
    expect(src).toContain(`getByRole('link', { name: '${destination}'`)
    // The entry lives inside a nav AREA, and a fresh sign-in leaves the console at
    // the root with every area but the active one closed. The spec must reach the
    // portal by its own address before it can assert the entry that opens it.
    expect(src.indexOf(`goto('/agentops')`)).toBeGreaterThan(-1)
    expect(src.indexOf(`goto('/agentops')`)).toBeLessThan(
      src.indexOf(`getByRole('link', { name: '${destination}'`),
    )
    // The destination opens the Sessions screen, titled as the destination reads.
    expect(destination).toBe(sessions.title)
    expect(src).toContain(`getByRole('heading', { name: '${sessions.title}'`)
    expect(src).not.toContain(`name: 'Claude Code'`)
  })

  it('signed-in waits use the overview link and the Playwright base URL', () => {
    const four = spec('four-tabs-live.spec.ts')
    const rid = spec('request-id-live.spec.ts')
    const overview = `getByRole('link', { name: '${ESTATE}'`
    expect(four).toContain(overview)
    expect(four).not.toContain("name: 'Inventory'")
    expect(rid).toContain(overview)
    expect(rid).not.toContain("name: 'Inventory'")
    expect(rid).not.toContain('127.0.0.1:8489')
    expect(rid).not.toMatch(/PLAYWRIGHT_BASE_URL \?\? 'http/)
  })

  // ⛔ WHAT A FRESH SIGN-IN PAINTS IS ONE LINK, AND EIGHT SPECS WAITED FOR ANOTHER.
  //    Sign-in lands on the root, which belongs to no navigation area; the top bar
  //    paints the places and no page of an area (the sidebar that preceded it painted
  //    the areas with every panel but the active one hidden). A wait
  //    on a leaf of a closed area — "Inventory" was the one they all used — can only
  //    pass by a substring accident, and three of them did pass that way.
  it('every spec file waits first for the overview link, or for the one its navigation names', () => {
    const waits: string[] = []
    for (const file of readdirSync(E2E)) {
      if (!file.endsWith('.spec.ts')) continue
      waits.push(...waitsASignInCannotPaint(file, spec(file)))
    }
    expect(waits).toEqual([])
  })

  it('the shared overview locator names the exact link inside Journeys', () => {
    const src = spec('browser-session.ts').replace(/\s+\./g, '.')
    expect(src).toContain(
      `getByRole('navigation', { name: '${nav.shell.journeysLabel}', exact: true }).getByRole('link', { name: '${ESTATE}', exact: true })`,
    )
  })

  it.each(['console-func-l4.spec.ts', 'demo-graph.spec.ts'])(
    '%s stops at the shared overview assertion before later journey links',
    (file) => {
      expect(waitsASignInCannotPaint(file, spec(file))).toEqual([])
    },
  )

  it('recognizes a wrapped shared overview assertion before a later leaf', () => {
    const src = [
      "await page.getByRole('button', { name: /^sign in$/i }).click()",
      'await expect(\n journeyNowLink(page)\n ).toBeVisible({ timeout: 15_000 })',
      `await page.getByRole('link', { name: '${nav.items.inventory}', exact: true }).click()`,
    ].join(' ')
    expect(waitsASignInCannotPaint('shared.spec.ts', src)).toEqual([])
  })

  it.each([
    'await expect(journeyNowLink(page)).toBeVisible()',
    'journeyNowLink(page)',
  ])('still rejects a leaf wait before %s', (later) => {
    const body = `name: '${nav.items.inventory}', exact: true`
    const src = [
      "await page.getByRole('button', { name: /^sign in$/i }).click()",
      `await expect(page.getByRole('link', { ${body} })).toBeVisible()`,
      later,
    ].join(' ')
    expect(waitsASignInCannotPaint('unsafe.spec.ts', src)).toEqual([
      `unsafe.spec.ts: ${body}`,
    ])
  })

  it('a bare shared locator does not exempt a later leaf wait', () => {
    const body = `name: '${nav.items.inventory}', exact: true`
    const src = [
      "await page.getByRole('button', { name: /^sign in$/i }).click()",
      'journeyNowLink(page)',
      `await expect(page.getByRole('link', { ${body} })).toBeVisible()`,
    ].join(' ')
    expect(waitsASignInCannotPaint('unsafe.spec.ts', src)).toEqual([
      `unsafe.spec.ts: ${body}`,
    ])
  })

  it('the overview link is read inside the Journeys navigation, never at page level', () => {
    expect(SPEC_DIRS).toEqual(expect.arrayContaining(['e2e', 'e2e-release']))
    const loose: string[] = []
    for (const dir of SPEC_DIRS) {
      for (const file of readdirSync(join(WEB, dir))) {
        if (!file.endsWith('.spec.ts')) continue
        loose.push(
          ...overviewLinksOutsideJourneys(
            `${dir}/${file}`,
            spec(file, join(WEB, dir)),
          ),
        )
      }
    }
    expect(loose).toEqual([])
  })

  it.each([
    [
      'a page-level link',
      "await expect(page.getByRole('link', { name: 'Now', exact: true })).toBeVisible()",
      1,
    ],
    [
      'a link inside the Journeys navigation',
      "await expect(page.getByRole('navigation', { name: 'Journeys', exact: true }).getByRole('link', { name: 'Now', exact: true })).toBeVisible()",
      0,
    ],
    [
      'a wrapped chain inside the Journeys label',
      "page\n  .getByLabel('Journeys')\n  .getByRole('link', { name: 'Now', exact: true })",
      0,
    ],
    [
      'a link scoped to something else',
      "page.getByRole('main').getByRole('link', { name: 'Now', exact: true })",
      1,
    ],
  ])('classifies %s', (_name, src, loose) => {
    expect(overviewLinksOutsideJourneys('probe.spec.ts', src)).toHaveLength(
      loose,
    )
  })

  it('Deploy first boot waits for the shell before its page helper and help link', () => {
    const src = spec('deploy-executor.spec.ts')
    const after = src.slice(src.indexOf('name: /^sign in$/i'))
    const link = /getByRole\(\s*'link',\s*\{([^}]*)\}/.exec(after)
    expect(link?.[1].trim()).toBe(`name: '${ESTATE}', exact: true`)
    expect(link?.index).toBeLessThan(after.indexOf('async function openDeploy'))
  })

  it.each([
    ['a closed leaf', '', `name: '${nav.items.inventory}', exact: true`],
    ['an inexact overview', '', `name: '${ESTATE}'`],
    [
      'an arbitrary help link',
      "await page.goto('/deploy')",
      "name: 'How to connect an executor'",
    ],
  ])('rejects %s as the first sign-in wait', (_name, navigation, body) => {
    const src = [
      "await page.getByRole('button', { name: /^sign in$/i }).click()",
      navigation,
      `await expect(page.getByRole('link', { ${body} })).toBeVisible()`,
    ].join(' ')
    expect(waitsASignInCannotPaint('unsafe.spec.ts', src)).toEqual([
      `unsafe.spec.ts: ${body}`,
    ])
  })

  // The exemption is a match, not a hole: going somewhere does not license waiting for
  // anything. The spec that would prove it is written here as source, so the guard is
  // held to it whether or not a file in this repository happens to be written that way.
  it('a navigation to another area does not license the wait', () => {
    const elsewhere = [
      `await page.getByRole('button', { name: /^sign in$/i }).click()`,
      `await page.goto('/audit')`,
      `await expect(`,
      `  page.getByRole('link', { name: '${nav.items.agentops}', exact: true }),`,
      `).toBeVisible()`,
    ].join('\n')
    expect(
      waitsASignInCannotPaint('elsewhere.spec.ts', elsewhere),
    ).toHaveLength(1)
    const matching = elsewhere.replace(`goto('/audit')`, `goto('/agentops')`)
    expect(waitsASignInCannotPaint('matching.spec.ts', matching)).toEqual([])
  })
})
