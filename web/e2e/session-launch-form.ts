// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { expect, type Page } from '@playwright/test'

/** The supported launch form, for journeys that must choose an exact profile: New session
 * focuses the start composer, and its folder pill's More options opens the advanced launch. */
export async function openAdvancedSession(page: Page) {
  await page.goto('/sessions')
  await page
    .locator('#work-pane-rail')
    .getByRole('button', { name: 'New session', exact: true })
    .click()
  await page.getByRole('button', { name: /^Folder: / }).click()
  await page
    .getByRole('menuitem', { name: 'More options', exact: true })
    .click()
  const form = page.getByRole('dialog')
  await expect(
    form.getByRole('combobox', { name: 'Account', exact: true }),
  ).toBeVisible()
  return form
}
