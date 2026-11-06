// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import AxeBuilder from '@axe-core/playwright'
import { expect, test } from '@playwright/test'
import { fixtureFor } from './fixtures'

for (const width of [1280, 390]) {
  for (const theme of ['light', 'dark']) {
    test(`model installer ${width} ${theme}`, async ({
      page,
      baseURL,
    }, info) => {
      await page.setViewportSize({ width, height: 900 })
      await page.addInitScript((theme) => {
        localStorage.setItem('olivares.theme', theme)
        localStorage.setItem('olivares.lang', 'en')
        localStorage.setItem(
          'olivares.tenant',
          JSON.stringify({ state: { activeTenant: 't-demo' }, version: 0 }),
        )
      }, theme)
      let cancelled = false
      await page.route('**/*', (route) => {
        const url = new URL(route.request().url())
        if (url.origin !== baseURL) return route.abort()
        if (!url.pathname.startsWith('/v1/')) return route.continue()
        if (url.pathname.endsWith('/stream'))
          return route.fulfill({
            contentType: 'text/event-stream',
            body: ': connected\n\n',
          })
        if (url.pathname === '/v1/m/agenttools/ollama')
          return route.fulfill({
            json: {
              installed: true,
              state: 'running',
              models: ['qwen2.5:0.5b'],
              model_sizes: { 'qwen2.5:0.5b': 397319168 },
            },
          })
        if (url.pathname.startsWith('/v1/m/agenttools/ollama/pulls')) {
          if (route.request().method() === 'DELETE') cancelled = true
          return route.fulfill({
            json: {
              id: 'download',
              model: 'llama3.2:1b',
              state: cancelled ? 'failed' : 'running',
              completed: 50,
              total: 100,
              status: 'pulling model',
              error: cancelled ? 'Download cancelled.' : undefined,
            },
          })
        }
        return route.fulfill({
          json: fixtureFor(url.pathname) ?? { items: [], has_more: false },
        })
      })
      await page.goto('/models')
      await expect(
        page.getByRole('heading', { name: 'Install a model' }),
      ).toBeVisible()
      await page
        .getByRole('button', { name: 'llama3.2:1b', exact: true })
        .click()
      await page.getByRole('button', { name: 'Download', exact: true }).click()
      await expect(page.getByRole('progressbar')).toHaveAttribute(
        'aria-valuenow',
        '50',
      )
      const axe = await new AxeBuilder({ page })
        .withTags(['wcag2a', 'wcag2aa', 'wcag21aa'])
        .analyze()
      expect(axe.violations).toEqual([])
      await page.screenshot({
        path: info.outputPath('models-install.png'),
        fullPage: true,
      })
      await page
        .getByRole('button', { name: 'Cancel the download of llama3.2:1b' })
        .click()
      await expect(
        page.getByRole('alert').filter({ hasText: 'Download cancelled.' }),
      ).toBeVisible()
      if (width === 1280 && theme === 'light') {
        await page.route('**/v1/m/agenttools/inventory', (route) =>
          route.fulfill({
            json: {
              ...(fixtureFor('/v1/m/agenttools/inventory') as object),
              inventory: {
                installed: [
                  {
                    driver: 'ollama',
                    state: 'installed',
                    version: '0.12.0',
                    executable: '/models/ollama',
                    is_executable: true,
                  },
                ],
                leftovers: [],
              },
            },
          }),
        )
        await page.goto('/agent-tools')
        await expect(page.getByTestId('ollama-service')).toBeVisible()
        await expect(page.getByLabel('Model name')).toBeVisible()
        await page.screenshot({
          path: info.outputPath('ai-tools-install.png'),
          fullPage: true,
        })
      }
    })
  }
}
