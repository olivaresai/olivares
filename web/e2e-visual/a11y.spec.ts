// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Automated accessibility pass (axe-core) over the executive dashboard and a
// representative slice of every product layer (visibility / management /
// intelligence). It asserts ZERO critical violations (the first
// impression must hold up to a screen reader) and logs any serious ones for
// follow-up. Runs against `vite dev` with all /v1 calls mocked, like the visual set.
import AxeBuilder from '@axe-core/playwright'
import { expect, test, type Page } from '@playwright/test'
import { fixtureFor } from './fixtures'
import { ROUTES } from './routes'

async function mockApi(page: Page) {
  await page.route('**/v1/**', async (route) => {
    const p = new URL(route.request().url()).pathname
    if (p.endsWith('/stream')) {
      return route.fulfill({
        status: 200,
        contentType: 'text/event-stream',
        body: ': connected\n\n',
      })
    }
    const fx = fixtureFor(p)
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(fx ?? { items: [], has_more: false }),
    })
  })
  await page.addInitScript(() => {
    localStorage.setItem(
      'olivares.session',
      JSON.stringify({
        state: {
          token: 'olvs_demo',
          sessionId: 's1',
          expiresAt: '2030-01-01T00:00:00Z',
        },
        version: 0,
      }),
    )
    localStorage.setItem(
      'olivares.tenant',
      JSON.stringify({ state: { activeTenant: 't-demo' }, version: 0 }),
    )
    localStorage.setItem('olivares.lang', 'en')
  })
}

test.beforeEach(async ({ page }) => {
  await mockApi(page)
})

for (const route of ROUTES) {
  test(`a11y — no critical axe violations on ${route}`, async ({ page }) => {
    await page.goto(route)
    await page.waitForTimeout(1200)
    // ADM-CORE-01: anchor to WCAG 2.2 AA explicitly (incl. 2.1) so the new-in-2.2
    // rules — target-size (2.5.8) etc. — actually run, in a real browser.
    const results = await new AxeBuilder({ page })
      .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa'])
      .analyze()

    const byImpact = (impact: string) =>
      results.violations.filter((v) => v.impact === impact)
    const critical = byImpact('critical')
    const serious = byImpact('serious')

    // Surface detail in the test output for any non-clean result.
    for (const v of [...critical, ...serious]) {
      // eslint-disable-next-line no-console
      console.log(`[axe ${route}] ${v.impact}: ${v.id} — ${v.help}`)
      for (const n of v.nodes) {
        // eslint-disable-next-line no-console
        console.log(`    ${n.target.join(' ')} :: ${n.html.slice(0, 140)}`)
      }
    }

    expect(critical, `critical a11y violations on ${route}`).toEqual([])

    // Verify the NEW-in-2.2 success criteria specifically (not just "no critical").
    const new22 = results.violations.filter((v) =>
      ['target-size'].includes(v.id),
    )
    expect(
      new22.map((v) => v.id),
      `WCAG 2.2 new-SC violations on ${route}`,
    ).toEqual([])
  })
}
