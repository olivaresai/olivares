// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { expect, test, type Page } from '@playwright/test'
import { fixtureFor } from './fixtures'

// Keyboard focus shows a ring (WCAG 2.2 SC 2.4.7). The Button and the Switch hide the
// browser's outline (`outline-none`) and draw their own (`focus-visible:outline-2`); the
// dialog's close button draws a box-shadow ring (`focus-visible:ring-2`) instead.
// VISUAL_PORT=5217 pnpm exec playwright test -c playwright.visual.config.ts focus-ring.spec.ts

// A button that hides the browser's outline and draws its own.
const OWN_OUTLINE = 'button.outline-none[class~="focus-visible:outline-2"]'

// The real shell, signed in, with synthetic HTTP only.
async function prepare(page: Page, baseURL: string | undefined) {
  await page.route('**/*', (route) => {
    const url = new URL(route.request().url())
    if (url.origin !== baseURL) return route.abort()
    if (!url.pathname.startsWith('/v1/')) return route.continue()
    if (url.pathname.endsWith('/stream')) {
      return route.fulfill({
        contentType: 'text/event-stream',
        body: ': connected\n\n',
      })
    }
    return route.fulfill({
      json: fixtureFor(url.pathname) ?? { items: [], has_more: false },
    })
  })
  await page.addInitScript(() => {
    localStorage.setItem('olivares.lang', 'en')
    localStorage.setItem(
      'olivares.tenant',
      JSON.stringify({ state: { activeTenant: 't-demo' }, version: 0 }),
    )
  })
}

// Press Tab (keyboard focus is what `:focus-visible` matches) until the focused
// element matches `selector`, and return it.
async function tabUntil(page: Page, selector: string) {
  for (let tabs = 0; tabs < 80; tabs++) {
    await page.keyboard.press('Tab')
    const hit = await page.evaluate(
      (s) => document.activeElement?.matches(s) ?? false,
      selector,
    )
    if (hit) return page.locator(':focus')
  }
  throw new Error(`Tab never reached ${selector}`)
}

test.beforeEach(async ({ page, baseURL }) => {
  await prepare(page, baseURL)
})

test('Tab from the top reaches a button and paints its 2 px outline', async ({
  page,
}) => {
  await page.goto('/settings')
  await expect(page.getByRole('switch').first()).toBeVisible()
  const button = await tabUntil(page, OWN_OUTLINE)
  await expect(button).not.toHaveCSS('outline-style', 'none')
  await expect(button).toHaveCSS('outline-width', '2px')
})

test('a Settings switch paints its 2 px outline under keyboard focus', async ({
  page,
}) => {
  await page.goto('/settings')
  await expect(page.getByRole('switch').first()).toBeVisible()
  const toggle = await tabUntil(page, '[data-slot="switch"]')
  await expect(toggle).not.toHaveCSS('outline-style', 'none')
  await expect(toggle).toHaveCSS('outline-width', '2px')
})

test('the dialog close button paints its ring under keyboard focus', async ({
  page,
}) => {
  await page.goto('/sessions?newSession=1')
  await expect(page.getByRole('dialog')).toBeVisible()
  const close = await tabUntil(page, '[role="dialog"] > button:last-child')
  await expect(close).toHaveAccessibleName('Close')
  await expect(close).not.toHaveCSS('box-shadow', 'none')
})
