// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import AxeBuilder from '@axe-core/playwright'
import { expect, test } from '@playwright/test'
import { fixtureFor } from './fixtures'

test.beforeEach(async ({ page }) => {
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
      : path.endsWith('/ui-state')
        ? { stored: true, sidebar: 'full' }
        : path.endsWith('/favorites')
          ? { stored: true, favorites: [{ kind: 'feature', id: 'audit' }] }
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
  })
})

test('real settings, favorite, account, brand and All areas links retire the work overlay, including back', async ({
  page,
}) => {
  for (const target of ['Settings', 'Audit', 'Account', 'brand', 'All areas']) {
    await page.goto('/sessions')
    const primary = page.getByRole('complementary', { name: 'Primary' })
    await primary.getByRole('button', { name: 'Expand sidebar' }).click()
    const overlay = page.locator('[data-slot="nav-overlay"]')
    await expect(overlay).toBeVisible()
    if (target === 'Account') {
      await overlay
        .getByRole('button', { name: 'Account', exact: true })
        .click()
      await page
        .getByRole('menuitem', { name: 'Settings', exact: true })
        .click()
    } else if (target === 'brand')
      await overlay.locator('a[href="/"]').first().click()
    else if (target === 'All areas') {
      await overlay.getByRole('button', { name: 'All areas' }).click()
      await page
        .getByRole('dialog', { name: 'All areas' })
        .getByRole('link', { name: /Audit/ })
        .first()
        .click()
    } else
      await overlay
        .getByRole('link', { name: target, exact: true })
        .first()
        .click()
    await expect(overlay).toHaveCount(0)
    await page.goBack()
    await expect(page).toHaveURL(/\/sessions$/)
    await expect(overlay).toHaveCount(0)
  }
})

test('Escape restores the actual opener and the overlay has no axe violations', async ({
  page,
}) => {
  await page.goto('/sessions')
  const opener = page
    .getByRole('complementary', { name: 'Primary' })
    .getByRole('button', { name: 'Expand sidebar' })
  await opener.focus()
  await page.keyboard.press('Enter')
  await expect(page.locator('[data-slot="nav-overlay"]')).toBeVisible()
  const result = await new AxeBuilder({ page })
    .include('[data-slot="nav-overlay"]')
    .analyze()
  expect(result.violations).toEqual([])
  await page.keyboard.press('Escape')
  await expect(page.locator('[data-slot="nav-overlay"]')).toHaveCount(0)
  await expect(opener).toBeFocused()
})

test('palette unfold returns Escape focus to the control that opened the palette', async ({
  page,
}) => {
  await page.goto('/sessions')
  const opener = page
    .getByRole('complementary', { name: 'Primary' })
    .getByRole('button', { name: 'Expand sidebar' })
  await opener.focus()
  await page.keyboard.press('Control+k')
  await page.getByRole('combobox').fill('Expand sidebar')
  await page
    .getByRole('option', { name: 'Expand sidebar', exact: true })
    .click()
  const overlay = page.locator('[data-slot="nav-overlay"]')
  await expect(overlay).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(overlay).toHaveCount(0)
  await expect(opener).toBeFocused()
})
