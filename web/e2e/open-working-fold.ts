// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The Sessions rail keeps its Working section shut, so a spec that reaches for the
// row of a running session opens the fold first, as a person would. Waits for the rail to be
// drawn, then opens the fold only when it is there and shut. The toggle exists only while a
// session works, so it can be gone by the time it is read: one short read, and a toggle that
// vanished (before the read or before the click) means there is nothing to open.
import type { Page } from '@playwright/test'

export async function openWorkingFold(page: Page): Promise<void> {
  await page
    .getByTestId('work-rail')
    .waitFor({ timeout: 15_000 })
    .catch(() => undefined)
  const toggle = page.getByRole('button', { name: /^Working\s*\d+$/ }).first()
  const expanded = await toggle
    .getAttribute('aria-expanded', { timeout: 2_000 })
    .catch(() => null)
  if (expanded === 'false')
    await toggle.click({ timeout: 2_000 }).catch(() => undefined)
}
