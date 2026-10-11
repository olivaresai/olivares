// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { randomUUID } from 'node:crypto'
import { expect, test } from '@playwright/test'

const setupToken = process.env.PLAYWRIGHT_SETUP_TOKEN ?? ''
test.use({ screenshot: 'off', trace: 'off' })

test('fresh Community About identifies the person and keeps the principal ID selectable', async ({
  page,
}) => {
  test.skip(!setupToken, 'Requires a fresh engine and its setup token')
  const email = 'about@example.com'
  const password = `${randomUUID()}-Aa1!`
  await page.goto('/setup')
  await page.locator('#token').fill(setupToken)
  await page.locator('#setup-email').fill(email)
  await page.locator('#setup-password').fill(password)
  await page.getByRole('button', { name: /create administrator/i }).click()
  await page.waitForURL('**/onboarding')
  await page.goto('/settings')
  await page.getByRole('button', { name: 'About', exact: true }).click()
  await expect(
    page.getByText('Community edition — no license needed'),
  ).toBeVisible()
  await expect(page.getByText('Licensed to', { exact: true })).toHaveCount(0)
  await expect(page.locator('dd').filter({ hasText: email })).toBeVisible()
  const details = page.locator('details').filter({ hasText: 'Principal ID' })
  await expect(details.locator('p')).not.toBeVisible()
  await details.locator('summary').click()
  const id = details.locator('p')
  await expect(id).toHaveText(/^user:[a-z0-9-]+$/)
  await id.click()
  expect(await page.evaluate(() => window.getSelection()?.toString())).toBe(
    await id.textContent(),
  )
})
