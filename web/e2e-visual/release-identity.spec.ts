// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { expect, test } from '@playwright/test'
import { expectReleaseVersion } from '../e2e-release/surface'
import { SESSION_TOOLS } from '../src/features/agentops/tool-names'
import { browserSession, fixtureFor, serverInfo } from './fixtures'

test.use({ screenshot: 'only-on-failure' })

for (const theme of ['light', 'dark']) {
  for (const width of [1280, 390]) {
    test(`release identity stays painted on work pages and New session at ${width} in ${theme}`, async ({
      page,
    }, testInfo) => {
      await page.setViewportSize({ width, height: 900 })
      let version = '26.10.1'
      await page.route('**/v1/**', async (route) => {
        const path = new URL(route.request().url()).pathname
        if (path.endsWith('/stream'))
          return route.fulfill({
            contentType: 'text/event-stream',
            body: ': connected\n\n',
          })
        const json = path.endsWith('/server-info')
          ? { ...serverInfo, version }
          : path.endsWith('/auth/browser-session')
            ? browserSession
            : path === '/v1/m/sessions/provider-profiles/readiness'
              ? {
                  tools: SESSION_TOOLS.map((driver) => ({
                    driver,
                    ready: true,
                    reason: 'own_login',
                  })),
                }
              : path.endsWith('/ui-state')
                ? { stored: true, sidebar: 'rail' }
                : (fixtureFor(path) ?? { items: [], has_more: false })
        await route.fulfill({ json })
      })
      await page.addInitScript(
        ({ theme }) => {
          localStorage.setItem(
            'olivares.tenant',
            JSON.stringify({ state: { activeTenant: 't-demo' }, version: 0 }),
          )
          localStorage.setItem('olivares.lang', 'en')
          localStorage.setItem('olivares.theme', theme)
        },
        { theme },
      )

      for (const release of ['26.10.1', '1.0']) {
        version = release
        for (const path of ['/sessions', '/session-viewer/sess-a11y']) {
          await page.goto(path)
          await expect(page.locator('[data-slot="app-frame"]')).toBeVisible()
          await expectReleaseVersion(page, version)
          if (width === 1280)
            await expect(
              page.locator('aside[aria-label="Primary"]'),
            ).toHaveAttribute('data-sidebar-mode', 'rail')
          await page.screenshot({
            path: testInfo.outputPath(`${path.split('/')[1]}-${release}.png`),
          })
        }
        // Workspaces is a Sessions tab, opened by the release journey's menu.
        await page.goto('/sessions')
        await page.getByTestId('sessions-list-menu').click()
        await page
          .getByRole('menuitem', { name: 'Workspaces', exact: true })
          .click()
        await expect(
          page.getByRole('tabpanel', { name: 'Workspaces', exact: true }),
        ).toBeVisible()
        await page.mouse.move(width - 1, 899)
        await expectReleaseVersion(page, version)
        await page.screenshot({
          path: testInfo.outputPath(`workspaces-${release}.png`),
        })
        await page.goto('/sessions')
        await page
          .getByRole('button', { name: 'New session', exact: true })
          .first()
          .click()
        await expect(
          page.getByRole('dialog', { name: 'New session' }),
        ).toBeVisible()
        await page
          .getByLabel('First message (optional)')
          .fill('Check the release identity')
        await page
          .getByRole('button', { name: 'More options', exact: true })
          .click()
        await expectReleaseVersion(page, version)
        const identity = page
          .locator(
            'aside[aria-label="Primary"], [data-testid="deployment-identity"]',
          )
          .getByText(version, { exact: true })
        expect(
          await identity.evaluate((el) => {
            const box = el.getBoundingClientRect()
            const dialog = document
              .querySelector('[role="dialog"]')
              ?.getBoundingClientRect()
            return (
              el.clientWidth > 0 &&
              el.clientHeight > 0 &&
              el.scrollWidth <= el.clientWidth &&
              el.scrollHeight <= el.clientHeight &&
              box.left >= 0 &&
              box.right <= innerWidth &&
              box.top >= 0 &&
              box.bottom <= innerHeight &&
              (!dialog ||
                box.right <= dialog.left ||
                box.left >= dialog.right ||
                box.bottom <= dialog.top ||
                box.top >= dialog.bottom)
            )
          }),
        ).toBe(true)
        await page.screenshot({
          path: testInfo.outputPath(`identity-${release}.png`),
        })
      }
      // The shell switches at 761, distinct from the page controls' sm breakpoint.
      await page.keyboard.press('Escape')
      for (const edge of [760, 761]) {
        await page.setViewportSize({ width: edge, height: 900 })
        await expectReleaseVersion(page, version)
      }
    })
  }
}
