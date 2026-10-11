// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Offline, choosing a language whose sign-in dictionary is not loaded yet keeps the console
// running in English; back online, choosing it again shows it, with no reload and no uncaught
// error. A unit test cannot see what this guards: in a browser a module import that failed
// stays failed until the page reloads, and the console's load guard then reloaded the page.
//
// Environment (the spec SKIPS without them): PLAYWRIGHT_BASE_URL, the engine; E2E_EMAIL and
// E2E_PASSWORD, a user on it whose console language is English.
import { expect, test, type Page } from '@playwright/test'
import { signInFixture } from './fixture-sign-in'

const EMAIL = process.env.E2E_EMAIL ?? ''
const PASSWORD = process.env.E2E_PASSWORD ?? ''

async function chooseLanguage(page: Page, name: string) {
  await page.locator('#language').click()
  await page.getByRole('option', { name }).click()
}

test('an offline language keeps the console, and loads once chosen again online', async ({
  page,
  context,
}) => {
  test.skip(!EMAIL || !PASSWORD, 'needs E2E_EMAIL and E2E_PASSWORD')
  await signInFixture(page, { email: EMAIL, password: PASSWORD }, 15_000)
  await page.goto('/settings')
  await expect(page.locator('#language')).toBeVisible({ timeout: 15_000 })
  const navigations: string[] = []
  const errors: string[] = []
  page.on('framenavigated', (frame) => {
    if (frame === page.mainFrame()) navigations.push(frame.url())
  })
  page.on('pageerror', (error) => errors.push(error.message))

  await context.setOffline(true)
  await chooseLanguage(page, '日本語')
  await expect(
    page.getByRole('button', { name: 'Account', exact: true }),
  ).toBeVisible()

  await context.setOffline(false)
  await chooseLanguage(page, 'English')
  await chooseLanguage(page, '日本語')
  await expect(
    page.getByRole('button', { name: 'アカウント', exact: true }),
  ).toBeVisible({
    timeout: 15_000,
  })
  expect(navigations).toEqual([])
  expect(errors).toEqual([])
})
