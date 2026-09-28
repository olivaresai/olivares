// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { writeFile } from 'node:fs/promises'
import AxeBuilder from '@axe-core/playwright'
import { expect, test, type Page } from '@playwright/test'
import { fixtureFor, modelsCatalog } from './fixtures'
import { platformsReferenceFixture } from '../src/features/platforms/fixtures'
import type { CatalogResponse } from '../src/features/models/types'

// The retained at-run.ts catalog has no rows, but its pricing headers overflow.
// VISUAL_PORT=5217 pnpm exec playwright test -c playwright.visual.config.ts models-scroll-focus.spec.ts
const populatedCatalog: CatalogResponse = {
  ...modelsCatalog,
  capabilities: [
    'text',
    'vision',
    'tools',
    'streaming',
    'json_mode',
    'batch',
    'extended_thinking',
  ],
  models: [
    {
      family: 'keyboard-test-model',
      provider_ref: 'fixture-provider',
      capabilities: ['text', 'vision'],
      context_window: 1000,
      max_output_tokens: 100,
      modality: 'text',
      pricing: null,
    },
  ],
}

async function prepare(
  page: Page,
  baseURL: string | undefined,
  theme: string,
  populated = false,
) {
  await page.route('**/*', (route) => {
    const url = new URL(route.request().url())
    if (url.origin !== baseURL) return route.abort()
    if (!url.pathname.startsWith('/v1/')) return route.continue()
    if (url.pathname.endsWith('/stream')) {
      return route.fulfill({
        contentType: 'text/event-stream',
        body: ': connected\n\n',
      })
    }
    return route.fulfill({
      json:
        url.pathname === '/v1/m/models/catalog' && populated
          ? populatedCatalog
          : (fixtureFor(url.pathname) ?? { items: [], has_more: false }),
    })
  })
  await page.addInitScript((selectedTheme) => {
    localStorage.setItem('olivares.theme', selectedTheme)
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
      JSON.stringify({
        state: { activeTenant: 't-demo' },
        version: 0,
      }),
    )
  }, theme)
}

for (const { theme, width, populated } of [
  { theme: 'dark', width: 1440, populated: false },
  { theme: 'light', width: 1440, populated: false },
  { theme: 'dark', width: 390, populated: false },
  { theme: 'light', width: 390, populated: false },
  { theme: 'dark', width: 390, populated: true },
]) {
  test.describe(`${theme} ${width} ${populated ? 'populated' : 'empty'}`, () => {
    test.use({
      viewport: { width, height: 900 },
      contextOptions: { reducedMotion: 'reduce' },
    })
    test('models tables support keyboard scrolling', async ({
      page,
      baseURL,
    }, testInfo) => {
      await prepare(page, baseURL, theme, populated)
      await page.goto('/models')
      const tables = page.getByRole('main').getByRole('table')
      await expect(tables).toHaveCount(2)
      await expect(
        tables
          .nth(1)
          .getByRole('columnheader', { name: 'Service tiers', exact: true }),
      ).toBeVisible()
      if (populated)
        await expect(tables.nth(1).locator('tbody tr')).toHaveCount(1)
      // Match the original gate's tags and critical/serious threshold.
      const axe = await new AxeBuilder({ page })
        .withTags([
          'wcag2a',
          'wcag2aa',
          'wcag21a',
          'wcag21aa',
          'wcag22aa',
          'best-practice',
        ])
        .analyze()
      await writeFile(
        testInfo.outputPath('axe.json'),
        JSON.stringify(axe.violations, null, 2),
      )
      await writeFile(
        testInfo.outputPath('models.html'),
        await page.getByRole('main').evaluate((el) => el.outerHTML),
      )
      await page.screenshot({
        path: testInfo.outputPath('models.png'),
        fullPage: true,
      })
      expect(
        axe.violations.filter(
          (v) => v.impact === 'serious' || v.impact === 'critical',
        ),
      ).toEqual([])

      const dimensions = []
      let overflowingTables = 0
      for (const [index, name] of [
        'Capability matrix',
        'Declared pricing',
      ].entries()) {
        const scroller = tables.nth(index).locator('..')
        const overflowing = await scroller.evaluate(
          (el) => el.scrollWidth > el.clientWidth + 1,
        )
        dimensions.push(
          await scroller.evaluate((el) => ({
            clientWidth: el.clientWidth,
            scrollWidth: el.scrollWidth,
            tabIndex: el.getAttribute('tabindex'),
          })),
        )
        if (!overflowing) {
          await expect(scroller).not.toHaveAttribute('tabindex')
          continue
        }
        overflowingTables++
        await expect(scroller).toHaveRole('region')
        await expect(scroller).toHaveAccessibleName(name)
        // Enter through normal tab order, not programmatic focus on the scroller.
        await page.getByRole('tab', { name: 'Catalog', exact: true }).focus()
        for (let tabs = 0; tabs < 12; tabs++) {
          await page.keyboard.press('Tab')
          if (await scroller.evaluate((el) => el === document.activeElement))
            break
        }
        await expect(scroller).toBeFocused()
        await expect
          .poll(() =>
            scroller.evaluate((el) => getComputedStyle(el).outlineStyle),
          )
          .not.toBe('none')
        const before = await scroller.evaluate((el) => el.scrollLeft)
        await page.keyboard.press('ArrowRight')
        await expect
          .poll(() => scroller.evaluate((el) => el.scrollLeft))
          .toBeGreaterThan(before)
        await page.screenshot({
          path: testInfo.outputPath(`focused-table-${index}.png`),
        })
        await page.keyboard.press('Tab')
        await expect(scroller).not.toBeFocused()
      }
      expect(overflowingTables).toBeGreaterThan(0)
      await writeFile(
        testInfo.outputPath('dimensions.json'),
        JSON.stringify(dimensions, null, 2),
      )
      // ResizeObserver removes the extra stop when the static table fits.
      await page.setViewportSize({ width: 2400, height: 900 })
      await expect(tables.nth(1).locator('..')).not.toHaveAttribute('tabindex')
    })
  })
}

test('platforms keeps the existing unlabelled XScroll behavior', async ({
  page,
  baseURL,
}, testInfo) => {
  await prepare(page, baseURL, 'dark')
  await page.route('**/v1/m/models/platforms', (route) =>
    route.fulfill({ json: platformsReferenceFixture }),
  )
  await page.goto('/platforms')
  const table = page.getByRole('table', {
    name: 'Anthropic API support by surface',
  })
  await expect(table).toBeVisible()
  const scroller = table.locator('..')
  await expect(scroller).not.toHaveAttribute('tabindex')
  await expect(scroller).not.toHaveAttribute('role')
  await expect(scroller).not.toHaveAttribute('aria-label')
  await expect(scroller).toHaveCSS('overflow-x', 'auto')
  await writeFile(
    testInfo.outputPath('platforms.html'),
    await scroller.evaluate((el) => el.outerHTML),
  )
  await table.scrollIntoViewIfNeeded()
  await page.screenshot({ path: testInfo.outputPath('platforms.png') })
})
