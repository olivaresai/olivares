// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Visual e2e for the executive dashboard (module XXI). Like the visibility
// spec, it runs against `vite dev` with every /v1 call mocked by route fixtures, so
// the leadership dashboard — KPIs, trends, compliance bars, the risk gauge — renders
// deterministically (a marketing asset). Captures light + dark on desktop AND a
// mobile viewport, so the responsive degradation is part of the regression set.
import { expect, test, type Page } from '@playwright/test'
import { fixtureFor } from './fixtures'
import { PANEL_EXTENSIONS } from '../src/features/extensions'

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

async function openDashboard(page: Page) {
  const requests: string[] = []
  page.on('request', (request) =>
    requests.push(new URL(request.url()).pathname),
  )
  await page.goto('/dashboards')
  await page
    .getByRole('heading', {
      level: 1,
      name: 'Executive overview',
      exact: true,
    })
    .waitFor({ state: 'visible' })
  await expect(page.getByText('Active agents', { exact: true })).toBeVisible()
  await expect(
    page.getByRole('heading', { name: 'Reliability', exact: true }),
  ).toBeVisible()
  // The composition, not a synthetic license string, decides which panels exist.
  if (PANEL_EXTENSIONS.useDashboardCost) {
    await page.getByText('Spend trend').first().waitFor({ state: 'visible' })
  } else {
    await expect(page.getByText('Spend trend', { exact: true })).toHaveCount(0)
    expect(
      requests.filter((path) => path.startsWith('/v1/m/finops/spend')),
    ).toEqual([])
  }
  if (PANEL_EXTENSIONS.complianceView) {
    await page
      .getByText('Control coverage')
      .first()
      .waitFor({ state: 'visible' })
  } else {
    await expect(
      page.getByText('Control coverage', { exact: true }),
    ).toHaveCount(0)
    expect(requests).not.toContain('/v1/m/compliance/summary')
    expect(requests).not.toContain('/v1/m/compliance/risk')
  }
}

test('executive dashboard — shared KPIs and composed panels, light + dark (marketing asset)', async ({
  page,
}) => {
  await openDashboard(page)
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
  await openDashboard(page)
  await page.waitForTimeout(900)
  await page.screenshot({
    path: `${SHOTS}/dashboards-mobile.png`,
    fullPage: true,
  })
})
