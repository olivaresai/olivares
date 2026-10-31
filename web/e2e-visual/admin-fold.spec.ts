// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ADMIN AND SETTINGS, FOLDED INTO THEIR PLACES (0.5 one navigation), in Chromium against the
// mocked API. Administration repeats no tab People & groups, License or MCP & skills holds;
// Settings lists personal sections only; system updates are a page of Operations; every old
// address lands on its new page with its query and hash. The two measured tasks of the 0.5
// table that this fold owns are counted here in clicks from Now.
import { writeFileSync } from 'node:fs'
import AxeBuilder from '@axe-core/playwright'
import { expect, test, type Page } from '@playwright/test'
import { fixtureFor } from './fixtures'

const SHOTS = process.env.ADMIN_FOLD_SHOTS

async function mockApi(page: Page) {
  await page.route('**/v1/**', async (route) => {
    const path = new URL(route.request().url()).pathname
    if (path.endsWith('/stream'))
      return route.fulfill({
        contentType: 'text/event-stream',
        body: ': connected\n\n',
      })
    const value = path.endsWith('/auth/browser-session')
      ? {
          csrf_token: 'fixture-csrf',
          session_id: 'fixture-session',
          expires_at: '2030-01-01T00:00:00Z',
        }
      : (fixtureFor(path) ?? { items: [], has_more: false })
    await route.fulfill({
      contentType: 'application/json',
      body: JSON.stringify(value),
    })
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

const sidebar = (page: Page) =>
  page.getByRole('navigation', { name: 'Main navigation' })

async function capture(page: Page, name: string, variant = '1280-light') {
  await page.evaluate(() => document.fonts.ready)
  await page.waitForTimeout(800)
  if (SHOTS) await page.screenshot({ path: `${SHOTS}/${name}-${variant}.png` })
  const axe = await new AxeBuilder({ page })
    .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa'])
    .analyze()
  expect(axe.violations, `axe on ${name}`).toEqual([])
}

test.beforeEach(async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 })
  await mockApi(page)
})

test('Administration repeats no tab another place holds', async ({ page }) => {
  await page.goto('/console')
  const tabs = page.getByRole('tablist')
  await expect(tabs.getByRole('tab', { name: 'Agents' })).toBeVisible()
  for (const name of [
    'Users & groups',
    'SSO / IdP',
    'Roles & delegation',
    'API keys',
    'Edition & license',
    'MCP gateway',
  ])
    await expect(tabs.getByRole('tab', { name, exact: true })).toHaveCount(0)
  await capture(page, 'administration')
})

test('an old Administration tab lands on its place with its query and hash', async ({
  page,
}) => {
  await page.goto('/console?tab=people&focus=svc_pool#row-7')
  await expect(page).toHaveURL(
    /\/identity\?(?=.*tab=people)(?=.*focus=svc_pool).*#row-7$/,
  )
  await page.goto('/console?tab=license')
  await expect(page).toHaveURL(/\/settings\?section=edition$/)
  await page.goto('/console?tab=mcpGateway')
  await expect(page).toHaveURL(/\/capabilities\?tab=servers$/)
})

test('Settings lists personal sections only, and License is its own page', async ({
  page,
}) => {
  await page.goto('/settings')
  const list = page.getByRole('navigation', { name: 'Settings sections' })
  await expect(list.getByRole('button', { name: 'General' })).toBeVisible()
  for (const name of [
    'AI tools',
    'Workspaces',
    'Deploy and updates',
    'Updates',
    'Edition & modules',
  ])
    await expect(list.getByRole('button', { name, exact: true })).toHaveCount(0)
  await capture(page, 'settings')

  await sidebar(page)
    .getByRole('link', { name: 'License', exact: true })
    .click()
  await expect(page).toHaveURL(/\/settings\?section=edition$/)
  await expect(
    page.getByRole('navigation', { name: 'Settings sections' }),
  ).toHaveCount(0)
  await expect(
    sidebar(page).getByRole('link', { name: 'License', exact: true }),
  ).toHaveAttribute('aria-current', 'page')
  await capture(page, 'license')
})

test('system updates are a page of Operations, and their old address lands there', async ({
  page,
}) => {
  await page.goto('/settings?section=updates&from=wizard#latest')
  await expect(page).toHaveURL(/\/updates\?from=wizard#latest$/)
  await expect(
    page.getByRole('heading', { level: 1, name: 'Updates' }),
  ).toBeVisible()
  await expect(
    sidebar(page).getByRole('link', { name: 'Operations', exact: true }),
  ).toHaveAttribute('aria-current', 'page')
  const row = page.locator('[data-slot="destination-sections"]')
  await expect(row.getByRole('link', { name: 'Updates' })).toHaveAttribute(
    'aria-current',
    'page',
  )
  await capture(page, 'operations-updates')

  await page.goto('/attestation')
  await expect(
    sidebar(page).getByRole('link', { name: 'Security', exact: true }),
  ).toHaveAttribute('aria-current', 'page')
})

// The two screens this fold adds or lays out anew, at 390 px and in dark, axe on each.
for (const { width, theme } of [
  { width: 390, theme: 'light' },
  { width: 1280, theme: 'dark' },
])
  test(`Operations › Updates and License at ${width} px ${theme}`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height: 900 })
    await page.addInitScript(
      (value) => localStorage.setItem('olivares.theme', value),
      theme,
    )
    const variant = `${width}-${theme}`

    await page.goto('/updates')
    await expect(
      page.getByRole('heading', { level: 1, name: 'Updates' }),
    ).toBeVisible()
    if (theme === 'dark') await expect(page.locator('html')).toHaveClass(/dark/)
    await capture(page, 'operations-updates', variant)

    await page.goto('/settings?section=edition')
    await expect(
      page.getByRole('heading', { level: 1, name: 'Edition & modules' }),
    ).toBeVisible()
    await expect(
      page.getByRole('navigation', { name: 'Settings sections' }),
    ).toHaveCount(0)
    await capture(page, 'license', variant)
  })

test('measures the two Admin and Settings tasks of the 0.5 table in clicks from Now', async ({
  page,
}) => {
  const counted: Record<string, number> = {}

  await page.goto('/')
  let clicks = 0
  await sidebar(page)
    .getByRole('link', { name: 'Settings', exact: true })
    .click()
  clicks++
  await page.getByRole('radio', { name: 'Dark', exact: true }).click()
  clicks++
  await expect(page.locator('html')).toHaveClass(/dark/)
  counted['Change a setting'] = clicks

  await page.goto('/')
  clicks = 0
  await sidebar(page)
    .getByRole('link', { name: 'People & groups', exact: true })
    .click()
  clicks++
  await expect(page).toHaveURL(/\/identity/)
  await expect(
    page.getByRole('tab', { name: 'Users & groups', selected: true }),
  ).toBeVisible()
  counted['Administer users and groups'] = clicks
  await capture(page, 'people-and-groups')

  if (SHOTS)
    writeFileSync(
      `${SHOTS}/clicks.json`,
      JSON.stringify(counted, null, 2) + '\n',
    )
  // 0.4 measured 3 and 5; the ratified design measured 2 and 2.
  expect(counted['Change a setting']).toBeLessThanOrEqual(2)
  expect(counted['Administer users and groups']).toBeLessThanOrEqual(2)
})
