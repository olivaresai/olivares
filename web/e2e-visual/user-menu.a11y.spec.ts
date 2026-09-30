// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import AxeBuilder from '@axe-core/playwright'
import { expect, test, type Page } from '@playwright/test'
import { fixtureFor } from './fixtures'

// Exercise the real shell and portalled Account menu with synthetic HTTP only.
async function prepare(page: Page, baseURL: string | undefined, theme: string) {
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
  await page.addInitScript((selectedTheme) => {
    localStorage.setItem('olivares.theme', selectedTheme)
    localStorage.setItem('olivares.lang', 'en')
    localStorage.setItem(
      'olivares.session',
      JSON.stringify({
        state: {
          token: 'olvs_demo',
          sessionId: 's1',
          expiresAt: '2030-01-01T00:00:00Z',
        },
        version: 0,
      }),
    )
    localStorage.setItem(
      'olivares.tenant',
      JSON.stringify({ state: { activeTenant: 't-demo' }, version: 0 }),
    )
  }, theme)
}

for (const width of [1280, 390]) {
  for (const theme of ['light', 'dark']) {
    test.describe(`Account ${width} ${theme}`, () => {
      test.use({
        viewport: { width, height: 900 },
        contextOptions: { reducedMotion: 'reduce' },
      })

      test('open menu is accessible and preserves keyboard navigation', async ({
        page,
        baseURL,
      }, testInfo) => {
        await prepare(page, baseURL, theme)
        await page.goto('/')
        const account = page.getByRole('button', {
          name: 'Account',
          exact: true,
        })
        const menu = page.getByRole('menu')
        const settings = menu.getByRole('menuitem', {
          name: 'Settings',
          exact: true,
        })
        const signOut = menu.getByRole('menuitem', {
          name: 'Sign out',
          exact: true,
        })
        await expect(account).toBeVisible()

        for (const key of ['Enter', 'Space', 'ArrowDown']) {
          await account.focus()
          await account.press(key)
          await expect(menu).toBeVisible()
          await expect(settings).toBeFocused()
          await page.keyboard.press('ArrowDown')
          await expect(signOut).toBeFocused()
          await page.keyboard.press('ArrowUp')
          await expect(settings).toBeFocused()
          await page.keyboard.press('End')
          await expect(signOut).toBeFocused()
          await page.keyboard.press('Home')
          await expect(settings).toBeFocused()
          // Radix menu items retain their existing roving-focus Tab behavior.
          await page.keyboard.press('Tab')
          await expect(settings).toBeFocused()
          await page.keyboard.press('Shift+Tab')
          await expect(settings).toBeFocused()
          await page.keyboard.press('Escape')
          await expect(menu).toBeHidden()
          await expect(account).toBeFocused()
        }

        await testInfo.attach('keyboard', {
          body: JSON.stringify({
            opening: ['Enter', 'Space', 'ArrowDown'],
            navigation: ['ArrowDown', 'ArrowUp', 'End', 'Home'],
            tab: ['Tab', 'Shift+Tab'],
            dismissal: 'Escape returns focus to Account',
          }),
          contentType: 'application/json',
        })

        await account.press('Enter')
        await expect(settings).toBeFocused()
        await menu.evaluate(async (node) => {
          await Promise.all(
            node.getAnimations({ subtree: true }).map((a) => a.finished),
          )
        })
        const axe = await new AxeBuilder({ page })
          .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa'])
          .analyze()
        await expect(menu).toBeVisible()
        await testInfo.attach('axe-open-menu', {
          body: JSON.stringify(axe, null, 2),
          contentType: 'application/json',
        })
        await testInfo.attach('menu-accessibility-tree', {
          body: await menu.ariaSnapshot(),
          contentType: 'text/plain',
        })
        await page.screenshot({
          path: testInfo.outputPath('account-open.png'),
          fullPage: true,
        })
        expect(axe.violations).toEqual([])
      })
    })
  }
}
