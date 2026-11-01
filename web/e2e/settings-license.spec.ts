// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { randomUUID } from 'node:crypto'
import { expect, test } from '@playwright/test'

test.use({ screenshot: 'off', trace: 'off', video: 'off' })

test('License is one page: its old Administration address and the Settings search land on it', async ({
  page,
}) => {
  test.setTimeout(180_000)
  const token = process.env.PLAYWRIGHT_SETUP_TOKEN
  test.skip(!token, 'Requires a fresh engine and its setup token')
  await page.goto('/setup')
  await page.locator('#token').fill(token!)
  await page.locator('#setup-email').fill('settings-license@example.com')
  await page.locator('#setup-password').fill(`${randomUUID()}-Aa1!`)
  await page.getByRole('button', { name: /create administrator/i }).click()
  await page.waitForURL('**/onboarding')

  for (const theme of ['light', 'dark']) {
    await page.goto('/settings')
    await page
      .getByRole('radio', {
        name: theme === 'light' ? 'Light' : 'Dark',
        exact: true,
      })
      .click()
    for (const width of [1280, 390]) {
      await page.setViewportSize({ width, height: 900 })
      for (const path of [
        '/settings?section=edition',
        // 0.5 one navigation: Administration's copy of the panel left; its address lands here.
        '/console?tab=license',
      ]) {
        await page.goto(path)
        await expect(page).toHaveURL(/\/settings\?section=edition/)
        await expect(
          page.getByRole('heading', { name: 'Edition & license', exact: true }),
        ).toBeVisible()
        await expect(
          page.getByText('No license', { exact: true }),
        ).toBeVisible()
        await page
          .getByRole('button', { name: 'Install / update', exact: true })
          .click()
        await expect(page.getByRole('dialog')).toBeVisible()
        await page.keyboard.press('Escape')
      }
      await page.goto('/settings')
      // License is a place of the sidebar (a drawer item at 390 px), not a row of Settings.
      if (width < 761)
        await page.getByRole('button', { name: /open navigation/i }).click()
      await page
        .getByRole('navigation', { name: 'Main navigation' })
        .getByRole('link', { name: 'License', exact: true })
        .click()
      await expect(page.getByText('No license', { exact: true })).toBeVisible()
      await expect(page).toHaveURL(/section=edition/)
      await page.screenshot({
        path: `playwright-report/settings-license-${theme}-${width}.png`,
        fullPage: true,
      })
    }
  }

  for (const term of ['License', 'Edition']) {
    await page.goto('/settings')
    await page.getByRole('searchbox').fill(term)
    await page
      .getByRole('link', { name: 'Edition & license', exact: true })
      .click()
    await expect(page.getByText('No license', { exact: true })).toBeVisible()
    await page.keyboard.press('Control+k')
    const palette = page.getByRole('dialog')
    await palette.getByRole('combobox').fill(term)
    await palette
      .getByRole('option')
      .filter({ hasText: 'Edition & modules' })
      .click()
    await expect(page).toHaveURL(/\/settings\?section=edition/)
    await expect(page.getByText('No license', { exact: true })).toBeVisible()
  }
})
