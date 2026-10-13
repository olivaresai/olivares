// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { expect, test } from '@playwright/test'
import { signInFixture } from './fixture-sign-in'

// The Go lifecycle journey owns a disposable real API, signed releases, store
// and native child. Only the locally signed provider protocol is a stand-in.
const action = process.env.PLAYWRIGHT_TOOL_LIFECYCLE_ACTION
test.describe.configure({ retries: 0 })
test.use({ screenshot: 'off', trace: 'off', video: 'off' })

test('console tool lifecycle against the real scoped runtime', async ({
  page,
}) => {
  test.setTimeout(120_000)
  test.skip(
    !action,
    'run through TestToolLifecycleJourneyStartsReceiptBytesAndAuditsRemoval with OLIVARES_TOOL_JOURNEY_BROWSER=1',
  )
  await signInFixture(
    page,
    { email: 'tools@olivares.invalid', password: 'fixture-password-2026!' },
    30_000,
  )
  await page.evaluate((tenant) => {
    localStorage.setItem(
      'olivares.tenant',
      JSON.stringify({ state: { activeTenant: tenant }, version: 0 }),
    )
  }, process.env.PLAYWRIGHT_TOOL_LIFECYCLE_TENANT!)
  await page.goto('/agent-tools')
  await page.getByRole('button', { name: 'Options for Claude Code' }).click()
  await page.getByRole('menuitem', { name: 'Install details' }).click()
  if (action === 'pin' || action === 'clear') {
    await page
      .getByLabel('Workspace', { exact: true })
      .selectOption(process.env.PLAYWRIGHT_TOOL_LIFECYCLE_WORKSPACE!)
    await page
      .getByLabel('Session version', { exact: true })
      .selectOption(action === 'pin' ? '2.1.261' : '')
    await page
      .getByRole('button', { name: 'Save version', exact: true })
      .click()
    const saved = page.waitForResponse(
      (r) =>
        new URL(r.url()).pathname.endsWith('/agenttools/pins') &&
        r.request().method() === 'PUT',
    )
    await page
      .getByRole('button', { name: 'Confirm version', exact: true })
      .click()
    expect((await saved).status()).toBe(200)
    await expect(
      page.getByRole('button', { name: 'Confirm version', exact: true }),
    ).toHaveCount(0)
  } else {
    await page
      .getByRole('button', { name: 'Remove Claude Code 2.1.261', exact: true })
      .click()
    const removed = page.waitForResponse(
      (r) =>
        new URL(r.url()).pathname.endsWith('/agenttools/releases') &&
        r.request().method() === 'DELETE',
    )
    await page
      .getByRole('button', { name: 'Confirm removal', exact: true })
      .click()
    const response = await removed
    expect(response.status()).toBe(action === 'remove-in-use' ? 409 : 200)
    if (action === 'remove-in-use') {
      expect((await response.json()).error.code).toBe('tool_in_use')
      await expect(
        page.getByRole('dialog').last().getByRole('alert'),
      ).toContainText('uses this release')
    } else {
      await expect(
        page.getByRole('button', {
          name: 'Remove Claude Code 2.1.261',
          exact: true,
        }),
      ).toHaveCount(0)
    }
  }
})
