// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Visual e2e for the executive dashboard (module XXI). Like the visibility
// spec, it runs against `vite dev` with every /v1 call mocked by route fixtures, so
// the leadership dashboard — KPIs, trends, compliance bars, the risk gauge — renders
// deterministically (a marketing asset). Captures light + dark on desktop AND a
// mobile viewport, so the responsive degradation is part of the regression set.
import { test, type Page } from '@playwright/test'
import { fixtureFor } from './fixtures'

const SHOTS = 'e2e-visual/__shots__'

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

async function toDark(page: Page) {
  await page.getByRole('button', { name: /toggle theme/i }).click()
  await page.getByRole('menuitem', { name: /^dark$/i }).click()
  await page.waitForFunction(() =>
    document.documentElement.classList.contains('dark'),
  )
}

test.beforeEach(async ({ page }) => {
  await mockApi(page)
})

test('executive dashboard — KPIs + trends + compliance, light + dark (marketing asset)', async ({
  page,
}) => {
  await page.goto('/dashboards')
  // Wait for the rollups to resolve and the headline + a chart to render.
  await page.getByText('Spend trend').first().waitFor({ state: 'visible' })
  await page.getByText('Control coverage').first().waitFor({ state: 'visible' })
  await page.waitForTimeout(900)
  await page.screenshot({
    path: `${SHOTS}/dashboards-light.png`,
    fullPage: true,
  })

  await toDark(page)
  await page.waitForTimeout(700)
  await page.screenshot({
    path: `${SHOTS}/dashboards-dark.png`,
    fullPage: true,
  })
})

test('executive dashboard — mobile (responsive degradation)', async ({
  page,
}) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await page.goto('/dashboards')
  await page.getByText('Spend trend').first().waitFor({ state: 'visible' })
  await page.waitForTimeout(900)
  await page.screenshot({
    path: `${SHOTS}/dashboards-mobile.png`,
    fullPage: true,
  })
})
