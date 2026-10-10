// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { expect, test } from '@playwright/test'

const setupToken = process.env.PLAYWRIGHT_SETUP_TOKEN ?? ''
const email = 'admin@example.com'
const password = 'correct-horse-battery-staple-42'

// Setup consumes a one-time token: scripts/web-e2e.sh gives this spec its own engine.
test('Administration renders within five seconds on a fresh installation', async ({
  page,
}) => {
  test.skip(!setupToken, 'Run via scripts/web-e2e.sh with a fresh engine')
  await page.goto('/setup')
  await page.locator('#token').fill(setupToken)
  await page.locator('#setup-email').fill(email)
  await page.locator('#setup-password').fill(password)
  await page.getByRole('button', { name: /create administrator/i }).click()
  await page.waitForURL('**/onboarding')
  await expect(
    page.getByRole('link', { name: 'Now', exact: true }),
  ).toBeVisible()

  // A fresh installation lists every page the person may open: System & settings offers
  // the setup wizard and Administration, and the address opens Administration directly.
  await page.goto('/areas/system')
  const directory = page.locator('[data-slot="area-directory"]')
  await expect(
    directory.getByRole('link', { name: 'Setup wizard', exact: true }),
  ).toBeVisible()
  await expect(
    directory.getByRole('link', { name: 'Administration', exact: true }),
  ).toBeVisible()

  const started = performance.now()
  await page.goto('/console')
  await expect(
    page.getByRole('heading', { name: 'Administration', exact: true }),
  ).toBeVisible({ timeout: 5_000 })
  await expect(page.getByRole('tabpanel')).toContainText(email, {
    timeout: 5_000,
  })
  await expect(
    page.getByRole('tabpanel').getByRole('status', { name: /loading/i }),
  ).toHaveCount(0)
  const elapsed = performance.now() - started
  expect(elapsed, 'direct navigation').toBeLessThan(5_000)
  test.info().annotations.push({
    type: 'direct',
    description: `${Math.round(elapsed)} ms`,
  })
})
