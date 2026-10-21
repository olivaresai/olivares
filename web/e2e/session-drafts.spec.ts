// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { expect, test } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'

const first = process.env.E2E_DRAFT_RUN_A
const second = process.env.E2E_DRAFT_RUN_B

test('session drafts survive switching and reload, and clear on send and sign-out', async ({
  page,
}) => {
  test.setTimeout(120_000)
  test.skip(
    !first || !second,
    'Requires two live sessions from the draft journey runner',
  )
  await page.addInitScript(() => localStorage.setItem('olivares.lang', 'en'))
  await page.goto('/login')
  await page.locator('input[type=email]').fill('demo@olivares.local')
  await page.locator('input[type=password]').fill('olivares-demo-estate')
  await page.locator('button[type=submit]').click()
  await page.waitForURL((url) => !url.pathname.startsWith('/login'))

  const field = page.getByRole('textbox', { name: 'Session turn' })
  const drafts = () =>
    page.evaluate(() =>
      Object.keys(sessionStorage).filter((key) =>
        key.startsWith('olivares.draft.'),
      ),
    )
  const open = async (run: string) => {
    await page.goto(`/sessions?session=run:${run}&pane=narrative`)
    await expect(field).toBeEnabled()
  }
  let sends = 0
  page.on('request', (request) => {
    if (
      request.method() === 'POST' &&
      /\/runs\/[^/]+\/input$/.test(new URL(request.url()).pathname)
    )
      sends++
  })
  await open(first!)
  await field.fill('First browser draft')
  await open(second!)
  await expect(field).toHaveValue('')
  await field.fill('Second browser draft')
  await open(first!)
  await expect(field).toHaveValue('First browser draft')
  await page.reload()
  await expect(field).toHaveValue('First browser draft')
  expect(await drafts()).toHaveLength(2)
  expect(sends).toBe(0)
  const accessibility = await new AxeBuilder({ page })
    .include('[data-testid="work-composer"]')
    .withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa'])
    .analyze()
  expect(accessibility.violations).toEqual([])

  await page.getByTestId('composer-send').click()
  await expect(field).toHaveValue('')
  expect(await drafts()).toHaveLength(1)
  expect(sends).toBe(1)
  await page.reload()
  await expect(field).toHaveValue('')
  await open(second!)
  await expect(field).toHaveValue('Second browser draft')

  // Age the stored draft, then let the actual composer read it on reload.
  await page.evaluate(() => {
    for (const key of Object.keys(sessionStorage).filter((key) =>
      key.startsWith('olivares.draft.'),
    )) {
      const draft = JSON.parse(sessionStorage.getItem(key)!)
      draft.updatedAt = Date.now() - 25 * 60 * 60 * 1000
      sessionStorage.setItem(key, JSON.stringify(draft))
    }
  })
  await page.reload()
  await expect(field).toHaveValue('')
  expect(await drafts()).toHaveLength(0)
  await field.fill('Draft before signing out')
  expect(await drafts()).toHaveLength(1)
  await page.getByRole('button', { name: /account|user menu/i }).click()
  await page.getByRole('menuitem', { name: /sign out/i }).click()
  await page.waitForURL((url) => url.pathname.startsWith('/login'))
  expect(await drafts()).toHaveLength(0)
})
