// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { test, type Page } from '@playwright/test'
import { fixtureFor } from './fixtures'

const SHOTS = 'e2e-visual/__shots__'

async function mockApi(page: Page) {
  await page.route('**/v1/**', async (route) => {
    const p = new URL(route.request().url()).pathname
    if (p.endsWith('/stream')) {
      // Keep the SSE socket trivially alive with a comment frame (no live data needed
      // for the shot); the view shows its honest connection state.
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

test('access map — R/RW graph, light + dark + drift overlay (marketing asset)', async ({
  page,
}) => {
  await page.goto('/access-map')
  // Wait for React Flow to actually lay the graph out. Nodes are <div>s (visible);
  // edges are SVG <g> elements (Playwright reports those as "hidden" — they have no
  // layout box — so wait for them ATTACHED, not visible).
  await page.locator('.react-flow__node').first().waitFor({ state: 'visible' })
  await page.locator('.react-flow__edge').first().waitFor({ state: 'attached' })
  await page.waitForTimeout(900)
  await page.screenshot({ path: `${SHOTS}/access-map-light.png` })

  // The killer feature: permitted-vs-observed overlay with unexpected access.
  await page.getByRole('button', { name: /permitted vs observed/i }).click()
  await page.waitForTimeout(900)
  await page.screenshot({ path: `${SHOTS}/access-map-drift.png` })

  await toDark(page)
  await page.waitForTimeout(700)
  await page.screenshot({ path: `${SHOTS}/access-map-dark.png` })
})

test('inventory — catalog, light + dark', async ({ page }) => {
  await page.goto('/inventory')
  await page.getByText('orchestrator').first().waitFor({ state: 'visible' })
  await page.waitForTimeout(400)
  await page.screenshot({
    path: `${SHOTS}/inventory-light.png`,
    fullPage: true,
  })
  await toDark(page)
  await page.waitForTimeout(400)
  await page.screenshot({ path: `${SHOTS}/inventory-dark.png`, fullPage: true })
})

test('sessions — live operation, light + dark', async ({ page }) => {
  await page.goto('/sessions')
  await page.waitForTimeout(900)
  await page.screenshot({ path: `${SHOTS}/sessions-light.png`, fullPage: true })
  await toDark(page)
  await page.waitForTimeout(400)
  await page.screenshot({ path: `${SHOTS}/sessions-dark.png`, fullPage: true })
})

test('health — status + SLA + dependency map, light + dark', async ({
  page,
}) => {
  await page.goto('/health')
  await page.waitForTimeout(900)
  await page.screenshot({ path: `${SHOTS}/health-light.png`, fullPage: true })
  await toDark(page)
  await page.waitForTimeout(400)
  await page.screenshot({ path: `${SHOTS}/health-dark.png`, fullPage: true })
})
