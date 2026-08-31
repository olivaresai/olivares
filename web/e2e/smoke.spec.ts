// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { expect, test } from '@playwright/test'

// This smoke drives the REAL flow against a live olivares binary serving the
// embedded bundle: first-boot setup → login → the authenticated shell, screenshot
// in both themes. scripts/web-e2e.sh boots a fresh engine and exports the one-time
// setup token; without it the test is skipped (it needs a pristine, un-set-up engine).
const setupToken = process.env.PLAYWRIGHT_SETUP_TOKEN ?? ''
const PASSWORD = 'correct-horse-battery-staple-42'

// This flow necessarily handles the one-time bootstrap token and a password.
// Keep the runner from retaining an automatic failure screenshot or trace;
// the explicit screenshots below are taken only with secrets masked or after
// the setup document (and therefore its token) has gone away.
test.use({ screenshot: 'off', trace: 'off' })

test('first-boot setup → login → shell, in light and dark', async ({
  page,
}) => {
  test.skip(
    !setupToken,
    'PLAYWRIGHT_SETUP_TOKEN not set — run via scripts/web-e2e.sh',
  )

  // First boot: no users yet → the app routes to setup.
  await page.goto('/setup')
  await page.locator('#token').fill(setupToken)
  await page.locator('#setup-email').fill('admin@example.com')
  await page.locator('#setup-password').fill(PASSWORD)
  // The one-time token and password are still live at this point. A failed submit
  // must not leave either value in an artifact retained by CI.
  await page.screenshot({
    path: 'playwright-report/setup-ready.png',
    mask: [page.locator('#token'), page.locator('#setup-password')],
  })
  await page.getByRole('button', { name: /create administrator/i }).click()

  // Setup closes → login.
  await page.waitForURL('**/login')
  await page.screenshot({ path: 'playwright-report/login-after-setup.png' })

  // The public auth shell owns the same theme control as the signed-in shell.
  await page.getByRole('button', { name: /toggle theme/i }).click()
  await page.getByRole('menuitem', { name: /^dark$/i }).click()
  await expect(page.locator('html')).toHaveClass(/dark/)
  await page.screenshot({ path: 'playwright-report/login-dark.png' })
  await page.getByRole('button', { name: /toggle theme/i }).click()
  await page.getByRole('menuitem', { name: /^light$/i }).click()
  await expect(page.locator('html')).not.toHaveClass(/dark/)

  await page.locator('#email').fill('admin@example.com')
  await page.locator('#password').fill(PASSWORD)
  await page.getByRole('button', { name: /^sign in$/i }).click()

  // The authenticated shell: brand, grouped navigation, the cockpit is live.
  // The wordmark is an outlined SVG with role="img" + aria-label (brand manual,
  // 2026-07-04) — there is no live text node, so getByText can never match it and
  // the accessible name is the right thing to assert anyway.
  await expect(
    page.getByRole('img', { name: 'Olivares AI' }).first(),
  ).toBeVisible()
  await expect(page.getByRole('link', { name: 'Inventory' })).toBeVisible()
  await page.screenshot({ path: 'playwright-report/shell-light.png' })

  // Toggle to dark and confirm the theme applies to <html>.
  await page.getByRole('button', { name: /toggle theme/i }).click()
  await page.getByRole('menuitem', { name: /^dark$/i }).click()
  await expect(page.locator('html')).toHaveClass(/dark/)
  await page.screenshot({ path: 'playwright-report/shell-dark.png' })
})
