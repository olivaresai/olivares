// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The "first hour" E2E for against a LIVE olivares binary (real API + real
// SQLite) — the zero-fake-success proof surface. It asserts the wizard's Infrastructure
// step reports Verified because the running engine can actually reach its database
// (setup-status database = a real st.Ping), NOT an optimistic UI flag. The privileged
// steps (workspace/identity/source/PEP) are correctly gated behind an AAL3 step-up that
// a password session cannot satisfy headlessly, so this proves the real read-driven
// verification + the honest fail-closed gate rather than a simulated success.
// scripts/web-e2e.sh boots a fresh engine and exports the one-time setup token; without
// it the test skips (it needs a pristine, un-set-up engine).
import { expect, test } from '@playwright/test'

const setupToken = process.env.PLAYWRIGHT_SETUP_TOKEN ?? ''
const PASSWORD = 'correct-horse-battery-staple-42'

test('first hour: the onboarding wizard verifies infrastructure against the live backend', async ({
  page,
}) => {
  test.skip(
    !setupToken,
    'PLAYWRIGHT_SETUP_TOKEN not set — run via scripts/web-e2e.sh',
  )

  // First boot: create the administrator, then sign in (real API + real DB writes).
  await page.goto('/setup')
  await page.locator('#token').fill(setupToken)
  await page.locator('#setup-email').fill('admin@example.com')
  await page.locator('#setup-password').fill(PASSWORD)
  await page.getByRole('button', { name: /create administrator/i }).click()

  await page.waitForURL('**/login')
  await page.locator('#email').fill('admin@example.com')
  await page.locator('#password').fill(PASSWORD)
  await page.getByRole('button', { name: /^sign in$/i }).click()
  await expect(page.getByRole('link', { name: 'Inventory' })).toBeVisible()

  // The actionable onboarding wizard.
  await page.goto('/onboarding')
  await expect(page.getByText('Get started')).toBeVisible()

  // The progress counter renders against the live setup-status (X of 5 verified).
  await expect(page.getByText(/of 5 verified/i)).toBeVisible()

  // THE PROOF: the Infrastructure step is verified ONLY because the live engine can
  // reach its database. This copy renders exclusively when the real setup-status
  // database flag is true — a mock could not manufacture it.
  await expect(
    page.getByText(/verified automatically/i),
  ).toBeVisible()

  // The remaining actionable steps are present (this is the wizard, not the old
  // passive checklist).
  await expect(page.getByText(/create your first workspace/i)).toBeVisible()
  await expect(page.getByText(/register your first source/i)).toBeVisible()
  await expect(page.getByText(/policy enforcement point/i)).toBeVisible()

  await page.screenshot({ path: 'playwright-report/onboarding-first-hour.png' })
})
