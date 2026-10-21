// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import AxeBuilder from '@axe-core/playwright'
import { expect, test } from '@playwright/test'
import { fixtureFor } from './fixtures'

for (const theme of ['light', 'dark']) {
  for (const width of [320, 1440]) {
    test.describe(`${theme} ${width}`, () => {
      test.use({ viewport: { width, height: 900 } })
      test('commercial badges share one saved preference and retire with the licence', async ({
        page,
        baseURL,
      }) => {
        let shown = true
        let status = 'valid'
        let edition = 'enterprise'
        let tier = 'enterprise'
        await page.route('**/*', async (route) => {
          const url = new URL(route.request().url())
          if (url.origin !== baseURL) return route.abort()
          if (!url.pathname.startsWith('/v1/')) return route.continue()
          if (url.pathname.endsWith('/stream'))
            return route.fulfill({
              contentType: 'text/event-stream',
              body: ': connected\n\n',
            })
          let body: unknown = fixtureFor(url.pathname) ?? {
            items: [],
            has_more: false,
          }
          if (url.pathname.endsWith('/server-info'))
            body = {
              edition,
              version: '1.0',
              engine: 'sqlite',
              setup_required: false,
              license: { status, licensee: 'Fixture', support_profile: tier },
            }
          if (url.pathname.endsWith('/ui-state')) {
            if (route.request().method() === 'PUT') {
              const update = route.request().postDataJSON()
              if (typeof update.show_edition_badge === 'boolean')
                shown = update.show_edition_badge
            }
            body = { stored: true, sidebar: 'full', show_edition_badge: shown }
          }
          await route.fulfill({ json: body })
        })
        await page.addInitScript((theme) => {
          localStorage.setItem('olivares.theme', theme)
          localStorage.setItem('olivares.lang', 'en')
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
        }, theme)
        await page.clock.install()
        await page.goto('/settings?section=edition')
        const toggle = page.getByRole('switch', { name: 'Show edition badge' })
        await expect(toggle).toBeEnabled()
        await expect(
          page.getByRole('img', { name: 'Enterprise edition' }).first(),
        ).toBeVisible()
        const badges = page.getByRole('img', { name: 'Enterprise edition' })
        for (const badge of await badges.all()) {
          const result = await new AxeBuilder({ page })
            .include(`[aria-label="Enterprise edition"]`)
            .withRules(['color-contrast'])
            .analyze()
          expect(result.violations).toEqual([])
          const box = await badge.boundingBox()
          if (box) expect(box.x + box.width).toBeLessThanOrEqual(width)
        }
        await page.keyboard.press('Control+k')
        const palette = page.getByRole('dialog')
        await expect(
          palette.getByRole('img', { name: 'Enterprise edition' }),
        ).toBeVisible()
        await page.keyboard.press('Escape')
        await toggle.focus()
        await page.keyboard.press('Space')
        await expect(toggle).not.toBeChecked()
        await expect(badges).toHaveCount(0)
        await page.reload()
        await expect(toggle).toBeEnabled()
        await expect(toggle).not.toBeChecked()
        await expect(badges).toHaveCount(0)
        await toggle.click()
        await expect(badges.first()).toBeVisible()
        // Exercise background lifecycle refresh without waiting thirty seconds in the test.
        status = 'expired'
        await page.clock.fastForward(31_000)
        await expect(badges).toHaveCount(0)
        status = 'none'
        await page.reload()
        await expect(badges).toHaveCount(0)
        status = 'valid'
        tier = 'business'
        await page.reload()
        await expect(
          page.getByRole('img', { name: 'Business edition' }).first(),
        ).toBeVisible()
        edition = 'community'
        await page.reload()
        await expect(page.getByRole('img', { name: /edition$/ })).toHaveCount(0)
      })
    })
  }
}
