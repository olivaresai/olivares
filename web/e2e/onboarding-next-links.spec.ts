// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The setup wizard's optional next steps against a LIVE olivares binary and a fresh store.
// A fresh install runs with most modules off, so a link that lands on a tab gated by one
// shows only "<module> is not enabled on this installation" and a button that restarts the
// engine. "Set policies" did that: the bare /claude-policy opened Drift & posture, which
// reads the Security module (#473). Every next step must open a page that works as it is.
import { expect, test } from '@playwright/test'

const setupToken = process.env.PLAYWRIGHT_SETUP_TOKEN ?? ''

// Setup carries a one-time token and password: no automatic traces, video or screenshots.
test.use({ screenshot: 'off', trace: 'off', video: 'off' })

// Setup consumes a one-time token: scripts/web-e2e.sh gives this spec its own engine.
test('the setup wizard next steps open pages that work on a fresh install', async ({
  page,
}) => {
  test.skip(!setupToken, 'Run via scripts/web-e2e.sh with a fresh engine')
  await page.goto('/setup')
  await page.locator('#token').fill(setupToken)
  await page.locator('#setup-email').fill('admin@example.com')
  await page.locator('#setup-password').fill('correct-horse-battery-staple-42')
  await page.getByRole('button', { name: /create administrator/i }).click()
  await page.waitForURL('**/onboarding')

  // The console learns which modules are off from server-info, and until that answer is
  // applied it shows every next step and mounts every gated panel. Open the wizard with the
  // answer in hand, and wait until the links follow it, before reading or clicking any.
  const info = await page.request.get('/v1/server-info')
  expect(info.ok()).toBe(true)
  const off =
    ((await info.json()) as { modules_not_enabled?: string[] })
      .modules_not_enabled ?? []
  const next = page.getByRole('region', { name: 'Next, when you need them' })
  async function openWizard() {
    await page.goto('/onboarding')
    await expect(next.getByRole('link').first()).toBeVisible()
    await expect(next.getByRole('link', { name: 'Set budgets' })).toHaveCount(
      off.includes('finops') ? 0 : 1,
    )
  }

  await openWizard()
  const names = await next.getByRole('link').allTextContents()
  expect(names).toContain('Set policies')

  for (const name of names) {
    await openWizard()
    await next.getByRole('link', { name, exact: true }).click()
    await page.waitForURL((u) => !u.pathname.startsWith('/onboarding'))
    const main = page.getByRole('main')
    await expect(main.getByRole('heading', { level: 1 }), name).toBeVisible()
    if (name === 'Set policies')
      await expect(
        main.getByRole('tab', { name: 'Managed settings' }),
      ).toHaveAttribute('aria-selected', 'true')
    await expect(
      main.locator('[data-slot="module-not-enabled"]'),
      name,
    ).toHaveCount(0)
  }
})
