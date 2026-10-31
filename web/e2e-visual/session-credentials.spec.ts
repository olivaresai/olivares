// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
// Visual fixtures exercise the production picker/manager without storing real credentials.
import AxeBuilder from '@axe-core/playwright'
import { expect, test } from '@playwright/test'
import { fixtureFor, serverInfo } from './fixtures'

for (const [width, theme] of [
  [1280, 'light'],
  [390, 'dark'],
] as const) {
  test(`credential picker and manager at ${width} ${theme}`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height: 900 })
    await page.addInitScript((theme) => {
      localStorage.setItem(
        'olivares.tenant',
        JSON.stringify({ state: { activeTenant: 't-demo' }, version: 0 }),
      )
      localStorage.setItem('olivares.lang', 'en')
      localStorage.setItem('olivares.theme', theme)
    }, theme)
    await page.route('**/v1/**', async (route) => {
      const path = new URL(route.request().url()).pathname
      if (path.endsWith('/stream'))
        return route.fulfill({
          contentType: 'text/event-stream',
          body: ': connected\n\n',
        })
      const json =
        path === '/v1/m/sessions/provider-profiles/readiness'
          ? { tools: [{ driver: 'claude', ready: true, reason: 'own_login' }] }
          : path === '/v1/server-info'
            ? { ...serverInfo, edition: 'business' }
            : path === '/v1/console/secrets'
              ? {
                  sealer_available: true,
                  secrets: [
                    { name: 'credential/build-host', hint: 'synthetic' },
                  ],
                }
              : (fixtureFor(path) ?? { items: [], has_more: false })
      await route.fulfill({ json })
    })
    await page.goto('/sessions')
    await page
      .locator('#work-pane-rail')
      .getByRole('button', { name: 'New session', exact: true })
      .click()
    await page
      .getByRole('dialog')
      .getByRole('button', { name: 'More options', exact: true })
      .click()
    const checkbox = page.getByRole('checkbox', { name: /build-host/ })
    await expect(checkbox).toBeVisible()
    await checkbox.check()
    await page.screenshot({
      path: process.env.TMPDIR + `/2524-picker-${width}-${theme}.png`,
    })
    await page
      .getByRole('button', { name: /Manage managed credentials/i })
      .click()
    await expect(page.getByRole('dialog').last()).toContainText('build-host')
    const manager = page.getByRole('dialog').last()
    await page.screenshot({
      path: process.env.TMPDIR + `/2524-manager-${width}-${theme}.png`,
    })
    await expect
      .poll(
        () => manager.evaluate((node) => node.scrollWidth - node.clientWidth),
        { message: 'manager must not overflow horizontally' },
      )
      .toBeLessThanOrEqual(1)
    const report = await new AxeBuilder({ page })
      .include('[role="dialog"]:not([aria-hidden="true"])')
      .analyze()
    expect(report.violations).toEqual([])
  })
}
