// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { expect, test } from '@playwright/test'

// Failure-context snapshots can also record typed bootstrap credentials.
process.env.PLAYWRIGHT_NO_COPY_PROMPT = '1'

const setupToken = process.env.PLAYWRIGHT_SETUP_TOKEN ?? ''
const password = 'workspace-first-boot-test-password-42'

// Setup handles a bootstrap token. Retain only explicit, post-login captures.
test.describe.configure({ retries: 0 })
test.use({ screenshot: 'off', trace: 'off', video: 'off' })

test('fresh install opens its only workspace overview without a selection step', async ({
  page,
}, testInfo) => {
  test.skip(!setupToken, 'Requires a fresh engine and PLAYWRIGHT_SETUP_TOKEN')
  test.setTimeout(90_000)
  await page.setViewportSize({ width: 1280, height: 900 })
  await page.goto('/setup')
  await page.locator('#token').fill(setupToken)
  await page.locator('#setup-email').fill('admin@example.com')
  await page.locator('#setup-password').fill(password)
  const setupResponse = page.waitForResponse(
    (response) =>
      new URL(response.url()).pathname === '/v1/setup' &&
      response.request().method() === 'POST',
  )
  await page.getByRole('button', { name: /create administrator/i }).click()
  const setup = await setupResponse
  if (!setup.ok()) {
    await page.locator('#token').fill('')
    await page.locator('#setup-password').fill('')
  }
  expect(setup.ok(), `Setup HTTP status: ${setup.status()}`).toBe(true)
  await page.waitForURL('**/onboarding', { timeout: 15_000 })
  await expect(
    page.getByRole('link', { name: 'Now', exact: true }),
  ).toBeVisible()

  // A fresh install lists every area from the first sign-in: the Workspace page is in the
  // sidebar's area groups beside Sessions, and its address opens it.
  await page.reload()
  const areas = page.locator('[data-slot="sidebar-areas"]')
  await expect(areas.locator('a[href="/sessions"]')).toHaveCount(1)
  await expect(areas.locator('a[href="/workspace"]')).toHaveCount(1)
  const workspacesResponse = page.waitForResponse(
    (response) =>
      new URL(response.url()).pathname === '/v1/workspaces' &&
      response.request().method() === 'GET',
  )
  await page.goto('/workspace')
  const response = await workspacesResponse
  expect(response.ok()).toBe(true)
  const workspaces = await response.json()
  expect(workspaces.items).toHaveLength(1)
  expect(workspaces.has_more).toBe(false)
  const workspace = workspaces.items[0]
  expect(workspace.status).toBe('active')

  try {
    await expect(
      page.getByRole('heading', {
        level: 1,
        name: workspace.name,
        exact: true,
      }),
    ).toBeVisible()
    await expect(
      page.getByText('No workspace selected', { exact: true }),
    ).toHaveCount(0)
    await expect(
      page.getByText('No agents in this workspace yet.', { exact: true }),
    ).toBeVisible()
    await expect(page).toHaveURL(/\/workspace$/)
    await page.reload()
    await expect(
      page.getByRole('heading', {
        level: 1,
        name: workspace.name,
        exact: true,
      }),
    ).toBeVisible()
  } finally {
    for (const theme of ['light', 'dark']) {
      await page.getByRole('button', { name: /toggle theme/i }).click()
      await page
        .getByRole('menuitem', { name: new RegExp(`^${theme}$`, 'i') })
        .click()
      for (const width of [1280, 390]) {
        await page.setViewportSize({ width, height: 900 })
        await page.screenshot({
          path: testInfo.outputPath(`workspace-${theme}-${width}.png`),
          fullPage: true,
        })
      }
      await page.setViewportSize({ width: 1280, height: 900 })
    }
  }

  // #475: a fresh install runs without the inventory module, and /inventory then shows only
  // "is not enabled on this installation". Every link the overview offers opens a page
  // that works.
  const info = await (await page.request.get('/v1/server-info')).json()
  expect(info.modules_not_enabled).toContain('inventory')
  const hrefs = await page
    .locator('main a')
    .evaluateAll((links) => links.map((a) => a.getAttribute('href') ?? ''))
  expect(hrefs).not.toContain('/inventory')
  expect(hrefs.length).toBeGreaterThan(0)
  for (const href of hrefs) {
    await page.goto('/workspace')
    await page.locator(`main a[href="${href}"]`).first().click()
    await page.waitForURL(`**${href}`)
    await expect(page.locator('main')).not.toBeEmpty()
    await expect(page.locator('[data-slot="module-not-enabled"]')).toHaveCount(
      0,
    )
  }
})
