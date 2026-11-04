// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { expect, test } from '@playwright/test'
import { browserSession, fixtureFor } from './fixtures'

// Recording is a Business capability: a Community build never asks the engine about it and
// shows the access map's own notice. The recorded variant lives with the Business overlay.
test('access map has one notice', async ({ page }, testInfo) => {
  await page.route('**/v1/**', async (route) => {
    const path = new URL(route.request().url()).pathname
    if (path === '/v1/auth/browser-session')
      return route.fulfill({ json: browserSession })
    if (path.endsWith('/stream'))
      return route.fulfill({
        contentType: 'text/event-stream',
        body: ': connected\n\n',
      })
    return route.fulfill({
      json: fixtureFor(path) ?? { items: [], has_more: false },
    })
  })
  await page.addInitScript(() => {
    localStorage.setItem('olivares.lang', 'en')
    localStorage.setItem('olivares.theme', 'light')
    localStorage.setItem(
      'olivares.tenant',
      JSON.stringify({ state: { activeTenant: 't-demo' }, version: 0 }),
    )
  })
  await page.goto('/access-map')
  await expect(page.locator('.react-flow__node').first()).toBeVisible()
  await expect(page.getByText(/privileged, audited action/i)).toHaveCount(1)
  await expect(page.getByText(/recorded privileged surface/i)).toHaveCount(0)
  await page.screenshot({
    path: testInfo.outputPath('access-map-notice.png'),
  })
})
