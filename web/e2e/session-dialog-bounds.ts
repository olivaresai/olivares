// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { expect, type Page } from '@playwright/test'

/** Check the contents, not only document overflow: a modal can clip an oversized form. */
export async function expectSessionDialogBounds(page: Page) {
  const clipped = await page.getByRole('dialog').evaluate((dialog) => {
    const bounds = dialog.getBoundingClientRect()
    return [
      ...dialog.querySelectorAll<HTMLElement>(
        'form, input, textarea, button, [role="combobox"], [data-slot="path-text"]',
      ),
    ]
      .filter((control) => {
        const rect = control.getBoundingClientRect()
        return (
          rect.width > 1 &&
          rect.height > 1 &&
          (rect.left < bounds.left || rect.right > bounds.right)
        )
      })
      .map((control) => ({
        element: control.tagName,
        label:
          control.getAttribute('aria-label') ||
          control.dataset.slot ||
          control.textContent?.slice(0, 40),
        width: control.getBoundingClientRect().width,
        modalWidth: bounds.width,
      }))
  })
  expect(
    clipped,
    'New session controls and folder path fit inside the modal',
  ).toEqual([])
}
