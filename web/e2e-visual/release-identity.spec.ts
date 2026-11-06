// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { expect, type Page, test } from '@playwright/test'
import { expectReleaseVersion } from '../e2e-release/surface'
import settings from '../src/lib/i18n/locales/en/settings.json' with { type: 'json' }
import { browserSession, fixtureFor, serverInfo } from './fixtures'

test.use({ screenshot: 'only-on-failure' })

/** A signed-in console on a Community engine that reports `version`. */
async function engine(page: Page, version: string) {
  await page.route('**/v1/**', async (route) => {
    const path = new URL(route.request().url()).pathname
    if (path.endsWith('/stream'))
      return route.fulfill({
        contentType: 'text/event-stream',
        body: ': connected\n\n',
      })
    const json = path.endsWith('/server-info')
      ? {
          ...serverInfo,
          version,
          edition: 'community',
          license: { status: 'none' },
        }
      : path.endsWith('/auth/browser-session')
        ? browserSession
        : path.endsWith('/ui-state')
          ? { stored: true, sidebar: 'rail' }
          : (fixtureFor(path) ?? { items: [], has_more: false })
    await route.fulfill({ json })
  })
  await page.addInitScript(() => {
    localStorage.setItem(
      'olivares.tenant',
      JSON.stringify({ state: { activeTenant: 't-demo' }, version: 0 }),
    )
    localStorage.setItem('olivares.lang', 'en')
    localStorage.setItem('olivares.theme', 'light')
  })
}

/** Where a person reads the release: Settings › About names the version and the edition
 * /v1/server-info reports, and that answer is the release the gate expects. */
async function expectAbout(page: Page, release: string) {
  await page.goto('/settings?section=about')
  const fact = (label: string) =>
    page
      .getByRole('main')
      .locator('dl > div')
      .filter({ has: page.getByText(label, { exact: true }) })
      .locator('dd')
  await expect(fact(settings.about.version)).toHaveText(release)
  await expect(fact(settings.about.edition)).toHaveText(
    settings.about.community,
  )
  await expectReleaseVersion(page, release)
}

for (const release of ['0.1', '1.0', '10.12']) {
  test(`Settings › About shows release ${release}, the version and edition server-info reports`, async ({
    page,
  }, testInfo) => {
    await page.setViewportSize({ width: 1280, height: 900 })
    await engine(page, release)
    await expectAbout(page, release)
    await page.screenshot({ path: testInfo.outputPath(`about-${release}.png`) })
  })
}

test('a console on another version fails the release identity', async ({
  page,
}) => {
  await engine(page, '0.2')
  await expect(expectAbout(page, '0.1')).rejects.toThrow()
})
