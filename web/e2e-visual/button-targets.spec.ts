// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { expect, test } from '@playwright/test'

for (const theme of ['light', 'dark']) {
  for (const size of ['icon-sm', 'icon']) {
    test(`adjacent ${size} actions keep their own 40 px targets (${theme})`, async ({
      page,
    }) => {
      await page.goto(
        `/src/dev/components-page/index.html?view=primitives&theme=${theme}&motion=reduce`,
      )
      const actions = page.locator(`[data-icon-actions="${size}"]`)
      const first = actions.getByRole('button').first()
      const second = actions.getByRole('button').nth(1)
      await expect(first).toBeVisible()
      await actions.evaluate((node) => {
        node.addEventListener('click', (event) => {
          const button = (event.target as Element).closest('button')
          node.setAttribute(
            'data-clicked',
            button?.getAttribute('aria-label') ?? '',
          )
        })
      })
      const box = await first.boundingBox()
      expect(box).not.toBeNull()
      // The later destructive action must never intercept the earlier action's
      // visible edge, even with the 4 px gap used in table action rows.
      await page.mouse.click(
        box!.x + box!.width - 0.5,
        box!.y + box!.height / 2,
      )
      await expect(actions).toHaveAttribute(
        'data-clicked',
        (await first.getAttribute('aria-label')) as string,
      )
      const geometry = await actions.evaluate((node) =>
        [...node.querySelectorAll('button')].map((button) => {
          const rect = button.getBoundingClientRect()
          const css = getComputedStyle(button)
          const after = getComputedStyle(button, '::after')
          const hasHitRegion = after.content !== 'none'
          const left =
            rect.x +
            parseFloat(css.borderLeftWidth) +
            (hasHitRegion ? parseFloat(after.left) : 0)
          const top =
            rect.y +
            parseFloat(css.borderTopWidth) +
            (hasHitRegion ? parseFloat(after.top) : 0)
          return {
            left: hasHitRegion ? left : rect.x,
            top: hasHitRegion ? top : rect.y,
            width: hasHitRegion ? parseFloat(after.width) : rect.width,
            height: hasHitRegion ? parseFloat(after.height) : rect.height,
          }
        }),
      )
      for (const target of geometry) {
        expect(target.width).toBeGreaterThanOrEqual(40)
        expect(target.height).toBeGreaterThanOrEqual(40)
      }
      expect(geometry[0].left + geometry[0].width).toBeLessThanOrEqual(
        geometry[1].left,
      )
      // Exercise the expanded target edges, not just the glyph's center.
      for (const [index, button] of [first, second].entries()) {
        const target = geometry[index]
        const centerX = target.left + target.width / 2
        const centerY = target.top + target.height / 2
        for (const [x, y] of [
          [target.left + 0.5, centerY],
          [target.left + target.width - 0.5, centerY],
          [centerX, target.top + 0.5],
          [centerX, target.top + target.height - 0.5],
        ]) {
          await actions.evaluate((node) => node.removeAttribute('data-clicked'))
          await page.mouse.click(x, y)
          await expect(actions).toHaveAttribute(
            'data-clicked',
            (await button.getAttribute('aria-label')) as string,
          )
        }
      }
    })
  }
}
