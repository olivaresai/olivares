// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { expect, type Page } from '@playwright/test'

// The pinned Playwright driver can capture password fields in its automatic AI
// page snapshot even with screenshot, trace and video off. This switch suppresses
// that snapshot; it does not suppress errors, which are contained below.
process.env.PLAYWRIGHT_NO_COPY_PROMPT = '1'

export async function signInFixture(
  page: Page,
  credentials: { email: string; password: string },
  timeout = 5_000,
) {
  try {
    await page.goto('/login', { timeout })
    await expect(page.locator('input[type=password]')).toHaveCount(1, {
      timeout,
    })
    await page.locator('input[type=email]').first().fill(credentials.email, {
      timeout,
    })
    await page
      .locator('input[type=password]')
      .first()
      .fill(credentials.password, {
        timeout,
      })
    await page.locator('button[type=submit]').first().click({ timeout })
    await page.waitForURL((url) => !url.pathname.startsWith('/login'), {
      timeout,
    })
  } catch {
    // A failed fill includes its value in the driver's call log. Do not retain
    // the original exception as a message, cause, attachment or diagnostic.
    throw new Error('Could not sign in to the disposable browser fixture')
  }
}
