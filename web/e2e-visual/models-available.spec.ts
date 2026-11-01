// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import AxeBuilder from '@axe-core/playwright'
import { expect, test, type Page } from '@playwright/test'
import { fixtureFor, modelsAvailability } from './fixtures'

async function prepare(page: Page, baseURL: string | undefined, theme: string) {
  await page.route('**/*', (route) => {
    const url = new URL(route.request().url())
    if (url.origin !== baseURL) return route.abort()
    if (!url.pathname.startsWith('/v1/')) return route.continue()
    if (url.pathname.endsWith('/stream'))
      return route.fulfill({
        contentType: 'text/event-stream',
        body: ': connected\n\n',
      })
    return route.fulfill({
      json: fixtureFor(url.pathname) ?? { items: [], has_more: false },
    })
  })
  await page.addInitScript((selectedTheme) => {
    localStorage.setItem('olivares.theme', selectedTheme)
    localStorage.setItem('olivares.lang', 'en')
    localStorage.setItem(
      'olivares.tenant',
      JSON.stringify({ state: { activeTenant: 't-demo' }, version: 0 }),
    )
  }, theme)
}

async function liveModels(page: Page, model = 'gpt-5') {
  await page.route('**/v1/m/sessions/provider-profiles?*', (route) =>
    route.fulfill({
      json: {
        items: [{ profile_ref: 'p-oc', driver: 'opencode', state: 'active' }],
        has_more: false,
      },
    }),
  )
  await page.route('**/v1/m/sessions/provider-profiles/p-oc/models', (route) =>
    route.fulfill({
      json: {
        profile_ref: 'p-oc',
        driver: 'opencode',
        state: 'fresh',
        models: [{ id: model }],
        default_model: '',
        free_text: false,
        checked_at: '2026-10-24T10:00:00Z',
        refresh_interval_seconds: 120,
      },
    }),
  )
  await page.route('**/v1/m/sessions/runs?*', (route) => {
    const state = new URL(route.request().url()).searchParams.get('state')
    return route.fulfill({
      json: {
        items:
          state === 'running'
            ? [
                {
                  run_ref: 'r-a',
                  name: 'Session r-a',
                  state: 'running',
                  provider_driver: 'opencode',
                  provider_profile_ref: 'p-oc',
                  model_ref: model,
                },
              ]
            : [],
        has_more: false,
      },
    })
  })
  await page.route('**/v1/m/models/availability', (route) =>
    route.fulfill({ json: modelsAvailability }),
  )
  await page.route('**/v1/m/agenttools/ollama', (route) =>
    route.fulfill({ json: { installed: false, state: 'stopped', models: [] } }),
  )
}

for (const { theme, width } of [
  { theme: 'light', width: 1280 },
  { theme: 'dark', width: 1280 },
  { theme: 'light', width: 390 },
  { theme: 'dark', width: 390 },
]) {
  test.describe(`${theme} ${width}`, () => {
    test.use({
      viewport: { width, height: 900 },
      contextOptions: { reducedMotion: 'reduce' },
    })
    test('Available is the default tab and a model opens its live session', async ({
      page,
      baseURL,
    }, testInfo) => {
      await prepare(page, baseURL, theme)
      await liveModels(page)
      await page.goto('/models')
      await expect(
        page.getByRole('tab', { name: 'Available', exact: true }),
      ).toHaveAttribute('aria-selected', 'true')
      const row = page.getByRole('row').filter({ hasText: 'gpt-5' }).first()
      await expect(row).toContainText('Live list of the tool')
      const provider = page
        .getByRole('row')
        .filter({ hasText: 'api-stub-model' })
      await expect(provider).toContainText('API provider')
      await expect(provider).toContainText('provider:1')
      await row.getByRole('button', { name: /1 session/i }).click()
      const link = page.getByRole('link', { name: 'Session r-a', exact: true })
      await expect(link).toBeVisible()
      await expect(link).toHaveAttribute('href', '/sessions?session=run%3Ar-a')
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
      await testInfo.attach('axe', {
        body: JSON.stringify(axe.violations, null, 2),
        contentType: 'application/json',
      })
      expect(axe.violations).toEqual([])
      await page.screenshot({
        path: testInfo.outputPath('available.png'),
        fullPage: true,
      })
      if (theme === 'light' && width === 1280) {
        await link.click()
        await expect(page).toHaveURL(/\/sessions\?session=run(%3A|:)r-a/)
      }
    })
  })
}

test('a longer model makes the Available table keyboard-scrollable without a row-count change', async ({
  page,
  baseURL,
}) => {
  test.setTimeout(45_000)
  await page.setViewportSize({ width: 1280, height: 900 })
  await prepare(page, baseURL, 'light')
  let model = 'm'
  await liveModels(page, model)
  await page.route('**/v1/m/sessions/provider-profiles/p-oc/models', (route) =>
    route.fulfill({
      json: {
        profile_ref: 'p-oc',
        driver: 'opencode',
        state: 'fresh',
        models: [{ id: model }],
        default_model: '',
        free_text: false,
        checked_at: '2026-10-24T10:00:00Z',
        refresh_interval_seconds: 120,
      },
    }),
  )
  await page.route('**/v1/m/sessions/runs?*', (route) => {
    const state = new URL(route.request().url()).searchParams.get('state')
    return route.fulfill({
      json: {
        items:
          state === 'running'
            ? [
                {
                  run_ref: 'r-a',
                  name: 'Session r-a',
                  state: 'running',
                  provider_driver: 'opencode',
                  provider_profile_ref: 'p-oc',
                  model_ref: model,
                },
              ]
            : [],
        has_more: false,
      },
    })
  })
  await page.goto('/models')
  const table = page.getByRole('table')
  const scroller = table.locator('..')
  await expect(table).toBeVisible()
  await expect(scroller).not.toHaveAttribute('role')
  model = `model-${'x'.repeat(120)}`
  await expect(page.getByText(model, { exact: true })).toBeVisible({
    timeout: 25_000,
  })
  await expect(scroller).toHaveRole('region')
  await expect(scroller).toHaveAccessibleName('Available now')
})
