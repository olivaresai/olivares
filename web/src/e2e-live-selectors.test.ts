// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { readdirSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import sessions from '@/features/sessions/i18n/en.json'
import nav from '@/lib/i18n/locales/en/nav.json'

const E2E = join(__dirname, '..', 'e2e')

function spec(name: string): string {
  return readFileSync(join(E2E, name), 'utf8')
}

/** The nav catalogue read the other way round: the address a name is reached at. */
const NAV_NAME_BY_SEGMENT = nav.items as Record<string, string>

/**
 * THE FIRST LINK A SPEC WAITS FOR AFTER SIGNING IN, AND WHETHER IT CAN BE PAINTED.
 *
 * ⛔ AND WHAT THIS DOES NOT SEE, SAID PLAINLY, because a guard read as more than it is
 *    becomes the reason nobody checks the real thing. It reads SOURCE TEXT — the spec
 *    files as characters — and it knows two facts: the English nav catalogue, and that
 *    a sign-in leaves the console on the root, which belongs to no area, so the sidebar
 *    paints the areas and the overview above them and nothing inside a closed area.
 *    It does not render the sidebar, does not read the feature registry, and cannot say
 *    which area any given leaf belongs to. So the exemption is narrow ON PURPOSE: a
 *    navigation exempts the wait that the navigation's own address names in the nav
 *    catalogue, and nothing else. A spec that goes to one address and waits for a
 *    sibling of that area is held here too — deliberately, because this file cannot
 *    tell that sibling from a leaf of a third area, which is the defect it exists to
 *    stop. Widening it means teaching it the registry, not loosening the match.
 */
function waitsASignInCannotPaint(file: string, src: string): string[] {
  const overview = `name: '${nav.items.home}', exact: true`
  const found: string[] = []
  for (const signIn of src.matchAll(/name: \/\^sign in\$\/i \}\)/g)) {
    const after = src.slice((signIn.index ?? 0) + signIn[0].length)
    const link = /getByRole\(\s*'link',\s*\{([^}]*)\}/.exec(after)
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

describe('live end-to-end specs name what the console paints', () => {
  it('the operate portal is reached by its nav name and its heading', () => {
    const src = spec('sessions-unified.spec.ts')
    expect(src).toContain(`getByRole('link', { name: '${nav.items.agentops}'`)
    // The entry lives inside a nav AREA, and a fresh sign-in leaves the console at
    // the root with every area but the active one closed. The spec must reach the
    // portal by its own address before it can assert the entry that opens it.
    expect(src.indexOf(`goto('/agentops')`)).toBeGreaterThan(-1)
    expect(src.indexOf(`goto('/agentops')`)).toBeLessThan(
      src.indexOf(`getByRole('link', { name: '${nav.items.agentops}'`),
    )
    expect(src).toContain(
      `getByRole('heading', { name: '${sessions.operateTitle}'`,
    )
    expect(src).not.toContain(`getByRole('link', { name: 'Claude Code'`)
  })

  it('signed-in waits use the overview link and the Playwright base URL', () => {
    const four = spec('four-tabs-live.spec.ts')
    const rid = spec('request-id-live.spec.ts')
    const overview = `getByRole('link', { name: '${nav.items.home}'`
    expect(four).toContain(overview)
    expect(four).not.toContain("name: 'Inventory'")
    expect(rid).toContain(overview)
    expect(rid).not.toContain("name: 'Inventory'")
    expect(rid).not.toContain('127.0.0.1:8489')
    expect(rid).not.toMatch(/PLAYWRIGHT_BASE_URL \?\? 'http/)
  })

  // ⛔ WHAT A FRESH SIGN-IN PAINTS IS ONE LINK, AND EIGHT SPECS WAITED FOR ANOTHER.
  //    Sign-in lands on the root, which belongs to no navigation area; the sidebar
  //    renders every area but the active one with its panel hidden, so the only
  //    entries painted are the areas themselves and the overview above them. A wait
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
