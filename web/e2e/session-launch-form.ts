// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { expect, type Page } from '@playwright/test'

/** The supported launch form, for journeys that must choose an exact profile. */
export async function openAdvancedSession(page: Page) {
  await page.goto('/sessions')
  await page
    .locator('#work-pane-rail')
    .getByRole('button', { name: 'New session', exact: true })
    .click()
  const form = page.getByRole('dialog')
  await expect(form).toBeVisible()
  await form
    .getByRole('button', { name: /^(More options|Advanced launch options)$/ })
    .first()
    .waitFor()
  const advanced = form.getByRole('button', {
    name: 'Advanced launch options',
    exact: true,
  })
  if (!(await advanced.isVisible())) {
    await form
      .getByRole('button', { name: 'More options', exact: true })
      .click()
  }
  await advanced.click()
  await expect(
    form.getByRole('combobox', { name: 'Provider profile', exact: true }),
  ).toBeVisible()
  return form
}
