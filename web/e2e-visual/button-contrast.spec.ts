// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { writeFile } from 'node:fs/promises'
import { expect, test } from '@playwright/test'

for (const theme of ['light', 'dark']) {
  for (const width of [1280, 390]) {
    test(`button contrast and components captures (${theme} ${width})`, async ({
      page,
    }, testInfo) => {
      await page.setViewportSize({ width, height: 900 })
      await page.goto(
        `/src/dev/components-page/index.html?view=primitives&theme=${theme}&motion=reduce`,
      )
      const board = page.locator('section[aria-labelledby="p1"]')
      await expect(board).toBeVisible()
      await page.evaluate(() => document.fonts.ready)
      await page.screenshot({
        path: testInfo.outputPath(
          `components-primitives-${width}-${theme}.png`,
        ),
        fullPage: true,
      })
      const buttons = [
        ['primary', board.locator('button.bg-accent').first()],
        ['secondary', board.locator('button.bg-active').nth(0)],
        ['outline', board.locator('button.bg-active').nth(1)],
        ['ghost', board.locator('button.bg-transparent').first()],
        ['destructive', board.locator('button.bg-bad-soft')],
        ['destructive-solid', board.locator('button.bg-danger-solid')],
        ['link', board.locator('button.underline')],
        ['icon', board.locator('[data-icon-actions="icon"] button').first()],
        [
          'icon-sm',
          board.locator('[data-icon-actions="icon-sm"] button').first(),
        ],
      ] as const
      const results = []
      for (const [variant, button] of buttons) {
        for (const state of [
          'normal',
          'hover',
          'disabled',
          'aria-disabled',
        ] as const) {
          await page.mouse.move(0, 0)
          await button.evaluate((node, currentState) => {
            node.toggleAttribute('disabled', currentState === 'disabled')
            if (currentState === 'aria-disabled')
              node.setAttribute('aria-disabled', 'true')
            else node.removeAttribute('aria-disabled')
          }, state)
          if (state === 'hover' || state === 'aria-disabled')
            await button.hover()
          // Reduce motion removes CSS transitions; measure the final rendered colors.
          const colors = await button.evaluate((node) => {
            const rgba = (value: string) => {
              const parts = value.match(/[\d.]+/g)!.map(Number)
              return [parts[0], parts[1], parts[2], parts[3] ?? 1]
            }
            const over = (top: number[], bottom: number[]) =>
              top
                .slice(0, 3)
                .map((c, i) => c * top[3] + bottom[i] * (1 - top[3]))
                .concat(1)
            const layers = []
            for (
              let el: Element | null = node.parentElement;
              el;
              el = el.parentElement
            )
              layers.push(rgba(getComputedStyle(el).backgroundColor))
            let background = [255, 255, 255, 1]
            for (const layer of layers.reverse())
              background = over(layer, background)
            const css = getComputedStyle(node)
            const brightness = Number(
              css.filter.match(/brightness\(([\d.]+)\)/)?.[1] ?? 1,
            )
            const filtered = (value: string) =>
              rgba(value).map((c, i) =>
                i < 3 ? Math.min(255, c * brightness) : c,
              )
            background = over(filtered(css.backgroundColor), background)
            const foreground = over(filtered(css.color), background)
            const luminance = (rgb: number[]) =>
              rgb
                .slice(0, 3)
                .map((c) => {
                  const channel = c / 255
                  return channel <= 0.04045
                    ? channel / 12.92
                    : ((channel + 0.055) / 1.055) ** 2.4
                })
                .reduce((sum, c, i) => sum + c * [0.2126, 0.7152, 0.0722][i], 0)
            const a = luminance(foreground)
            const b = luminance(background)
            return {
              foreground,
              background,
              ratio: (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05),
            }
          })
          results.push({ theme, width, variant, state, ...colors })
          expect
            .soft(colors.ratio, `${variant} ${state} ${theme}`)
            .toBeGreaterThanOrEqual(4.5)
        }
        await button.evaluate((node) => {
          node.removeAttribute('disabled')
          node.removeAttribute('aria-disabled')
        })
      }
      await writeFile(
        testInfo.outputPath('contrast.json'),
        JSON.stringify(results, null, 2),
      )
      await page.goto(
        `/src/dev/components-page/index.html?view=states&theme=${theme}&motion=reduce`,
      )
      await expect(page.locator('[data-slot="state-block"]')).toHaveCount(9)
      await page.screenshot({
        path: testInfo.outputPath(`components-states-${width}-${theme}.png`),
        fullPage: true,
      })
    })
  }
}
