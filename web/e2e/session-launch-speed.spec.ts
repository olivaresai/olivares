// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { expect, test } from '@playwright/test'
import { openAdvancedSession } from './session-launch-form'
import { sessionToken } from './live-session'

for (const theme of ['dark', 'light'] as const) {
  for (const width of [1280, 390]) {
    test(`launch speed: explicit Fast mode off reaches the live engine (${theme}, ${width})`, async ({
      page,
    }, info) => {
      test.skip(
        !process.env.DEMO_TENANT || process.env.CONVERSATION_FIXTURE !== '1',
        'requires the live conversation fixture runner',
      )
      test.setTimeout(120_000)
      await page.setViewportSize({ width, height: 900 })
      await page.addInitScript((selectedTheme) => {
        localStorage.setItem('olivares.theme', selectedTheme)
        localStorage.setItem('olivares.lang', 'en')
      }, theme)
      // Discovery is a deterministic upstream catalog. Launch and readback use the live engine.
      await page.route(
        '**/v1/m/sessions/provider-profiles/*/models',
        async (route) => {
          const profileRef = new URL(route.request().url()).pathname
            .split('/')
            .at(-2)
          await route.fulfill({
            json: {
              profile_ref: profileRef,
              driver: 'claude',
              state: 'fresh',
              free_text: true,
              default_model: 'fixture-claude',
              checked_at: new Date().toISOString(),
              refresh_interval_seconds: 120,
              models: [
                {
                  id: 'fixture-claude',
                  is_default: true,
                  supports_fast_mode: true,
                },
              ],
            },
          })
        },
      )
      await page.goto('/login')
      await page
        .locator('input[type=email]')
        .first()
        .fill('demo@olivares.local')
      await page
        .locator('input[type=password]')
        .first()
        .fill('olivares-demo-estate')
      await page.locator('button[type=submit]').first().click()
      await page.waitForURL((url) => !url.pathname.startsWith('/login'))
      const dialog = await openAdvancedSession(page)
      await dialog
        .getByRole('combobox', { name: 'Provider profile', exact: true })
        .click()
      await page.getByRole('option', { name: /Fixture Claude/ }).click()
      await dialog
        .getByRole('combobox', { name: 'Fast mode', exact: true })
        .click()
      await page.getByRole('option', { name: 'Off', exact: true }).click()
      await page.screenshot({
        path: info.outputPath(`launch-speed-${theme}-${width}.png`),
      })
      const response = page.waitForResponse(
        (res) =>
          res.url().endsWith('/v1/m/sessions/runs') &&
          res.request().method() === 'POST',
      )
      await dialog.getByRole('button', { name: 'Start', exact: true }).click()
      const launched = await response
      expect(launched.status()).toBe(201)
      expect(launched.request().postDataJSON().fast_mode).toBe(false)
      const run = await launched.json()
      expect(run.fast_mode).toBe(false)
      const saved = await page.request.get(
        `/v1/m/sessions/runs/${run.run_ref}`,
        {
          headers: {
            Authorization: `Bearer ${await sessionToken(page)}`,
            'X-Olivares-Tenant': process.env.DEMO_TENANT ?? '',
          },
        },
      )
      expect(saved.status()).toBe(200)
      expect((await saved.json()).fast_mode).toBe(false)
    })
  }
}
