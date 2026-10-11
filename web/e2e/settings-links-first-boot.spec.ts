// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { expect, test } from '@playwright/test'

const setupToken = process.env.PLAYWRIGHT_SETUP_TOKEN ?? ''

// Setup consumes a one-time token: scripts/web-e2e.sh gives this spec its own engine.
test('no Settings link lands on a not-enabled notice on a fresh installation (#474)', async ({
  page,
}) => {
  test.skip(!setupToken, 'Run via scripts/web-e2e.sh with a fresh engine')
  await page.goto('/setup')
  await page.locator('#token').fill(setupToken)
  await page.locator('#setup-email').fill('admin@example.com')
  await page.locator('#setup-password').fill('correct-horse-battery-staple-42')
  await page.getByRole('button', { name: /create administrator/i }).click()
  await page.waitForURL('**/onboarding')

  // A Settings link is a full page load, and the module list arrives with server-info:
  // every check below waits for that read, then for the page's reads to settle.
  const open = async (href: string) => {
    const info = page.waitForResponse(
      (r) => new URL(r.url()).pathname === '/v1/server-info' && r.ok(),
    )
    await page.goto(href)
    await info
    await page.waitForLoadState('networkidle')
    await expect(page.getByRole('heading', { level: 1 })).toBeVisible()
  }
  const notEnabled = page.locator('[data-slot="module-not-enabled"]')

  // Control: this engine leaves deploy off, and the check sees its notice.
  await open('/deploy')
  await expect(notEnabled).toHaveCount(1)

  await open('/settings')
  const sections = page.getByRole('navigation', { name: 'Settings sections' })
  await expect(
    sections.getByRole('link', { name: 'Deploy and updates' }),
  ).toHaveCount(0)
  const hrefs = await sections
    .getByRole('link')
    .evaluateAll((links) => links.map((a) => a.getAttribute('href') ?? ''))
  expect(hrefs).toContain('/providers')
  for (const href of hrefs) {
    await open(href)
    await expect(notEnabled, href).toHaveCount(0)
  }
})
