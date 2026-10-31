// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The Sessions rail keeps its Working section shut, so a spec that reaches for the
// row of a running session opens the fold first, as a person would. Waits for the rail to be
// drawn, then opens the fold only when it is there and shut.
import type { Page } from '@playwright/test'

export async function openWorkingFold(page: Page): Promise<void> {
  await page
    .getByTestId('work-rail')
    .waitFor({ timeout: 15_000 })
    .catch(() => undefined)
  const toggle = page.getByRole('button', { name: /^Working\s*\d+$/ }).first()
  if (
    (await toggle.count()) > 0 &&
    (await toggle.getAttribute('aria-expanded')) === 'false'
  )
    await toggle.click()
}
