// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE BLACK THEME IN A REAL BROWSER, on the seeded demo engine scripts/web-e2e-demo.sh boots.
// jsdom checks the tokens and the store; here the browser paints them. Chosen in Settings,
// Black survives a reload, and the frame, the sheet and the top bar of every main place are
// true black (#000), while Light and Dark keep their own sheet.
//
// With CAPTURE_DIR set it also photographs Now, Sessions (two side by side), Tools, Rules and
// Ledger at 1280 and 1920 px in Light, Dark and Black, for the review.
//
// ⛔ THE DEMO GUARD: without DEMO_TENANT this would grade the login page; it skips instead.
import { expect, test, type Page } from '@playwright/test'
import { mkdirSync } from 'node:fs'
import { resolve } from 'node:path'

const demoTenant = process.env.DEMO_TENANT ?? ''
const captureDir = process.env.CAPTURE_DIR
  ? resolve(process.env.CAPTURE_DIR)
  : ''
const EMAIL = 'demo@olivares.local'
const PASSWORD = 'olivares-demo-estate'
const BLACK = 'rgb(0, 0, 0)'
const THEMES = ['light', 'dark', 'black'] as const
const HEIGHT = { 1280: 800, 1920: 1080 } as const

/**
 * Pin the language and the demo organization before the first document, and the theme
 * only when none is stored yet, so a reload keeps what the page itself chose.
 */
async function prefer(page: Page, theme?: (typeof THEMES)[number]) {
  await page.addInitScript(
    ([t, tenant]) => {
      if (t && !window.localStorage.getItem('olivares.theme'))
        window.localStorage.setItem('olivares.theme', t)
      window.localStorage.setItem('olivares.lang', 'en')
      window.localStorage.setItem(
        'olivares.tenant',
        JSON.stringify({ state: { activeTenant: tenant }, version: 0 }),
      )
    },
    [theme ?? '', demoTenant],
  )
}

async function signIn(page: Page) {
  await page.goto('/login')
  await expect(page.locator('input[type=password]')).toHaveCount(1)
  await page.locator('input[type=email]').first().fill(EMAIL)
  await page.locator('input[type=password]').first().fill(PASSWORD)
  await page.locator('button[type=submit]').first().click()
  await page.waitForURL((url) => !url.pathname.startsWith('/login'), {
    timeout: 30_000,
  })
}

async function shoot(page: Page, name: string) {
  if (!captureDir) return
  mkdirSync(captureDir, { recursive: true })
  await page.mouse.move(0, 0)
  await page.screenshot({ path: `${captureDir}/${name}.png` })
}

/** The classes the theme sets on the root element. */
function rootTheme(page: Page) {
  return page.evaluate(() => ({
    dark: document.documentElement.classList.contains('dark'),
    black: document.documentElement.classList.contains('black'),
  }))
}

/** The painted background of the frame, the sheet and the top bar, in that order. */
function backgrounds(page: Page) {
  return page.evaluate(() =>
    ['app-frame', 'sheet', 'topbar'].map((slot) => {
      const el = document.querySelector(`[data-slot="${slot}"]`)
      return el ? getComputedStyle(el).backgroundColor : 'missing'
    }),
  )
}

/** Wait for the page itself: its chunk and its first answers, so the picture is not a spinner. */
async function settle(page: Page) {
  const main = page.locator('#main-content')
  await expect(main).toBeVisible()
  await expect(main.getByRole('status', { name: 'Loading…' })).toHaveCount(0, {
    timeout: 20_000,
  })
  await page.evaluate(() => document.fonts.ready)
}

/** The places of the review; Sessions shows the first two sessions side by side. */
async function places(page: Page) {
  await page.goto('/sessions')
  const rows = page.getByTestId('rail-row')
  await expect(rows.nth(1)).toBeVisible()
  const split = new URLSearchParams({
    session: (await rows.nth(0).getAttribute('data-address')) ?? '',
    beside: (await rows.nth(1).getAttribute('data-address')) ?? '',
  })
  return [
    ['now', '/'],
    ['sessions-split', `/sessions?${split}`],
    ['tools', '/agent-tools'],
    ['rules', '/permissions'],
    ['ledger', '/audit'],
  ] as const
}

test.describe('the Black theme in the browser @theme-black', () => {
  test.beforeEach(() => {
    test.skip(!demoTenant, 'Run with the live demo engine')
  })

  test('chosen in Settings, Black paints true black and survives a reload', async ({
    page,
  }) => {
    await page.emulateMedia({ colorScheme: 'light' })
    await prefer(page)
    await signIn(page)
    await page.goto('/settings')
    const black = page.getByRole('radio', { name: 'Black' })
    await black.click()
    await expect(black).toHaveAttribute('aria-checked', 'true')
    expect(await rootTheme(page)).toEqual({ dark: true, black: true })
    expect(await backgrounds(page)).toEqual([BLACK, BLACK, BLACK])
    expect(
      await page.evaluate(() => window.localStorage.getItem('olivares.theme')),
    ).toBe('black')

    await page.reload()
    await expect(page.getByRole('radio', { name: 'Black' })).toHaveAttribute(
      'aria-checked',
      'true',
    )
    expect(await rootTheme(page)).toEqual({ dark: true, black: true })

    // Leaving Black for Dark takes the black class away and the grey sheet back.
    await page.getByRole('radio', { name: 'Dark' }).click()
    expect(await rootTheme(page)).toEqual({ dark: true, black: false })
    expect((await backgrounds(page))[1]).not.toBe(BLACK)
  })

  for (const theme of THEMES) {
    test(`Now, Sessions, Tools, Rules and Ledger at 1280 and 1920 px (${theme})`, async ({
      page,
    }) => {
      test.setTimeout(180_000)
      await prefer(page, theme)
      await signIn(page)
      await page.setViewportSize({ width: 1920, height: HEIGHT[1920] })
      for (const [name, path] of await places(page)) {
        await page.goto(path)
        await settle(page)
        const painted = await backgrounds(page)
        if (theme === 'black')
          expect(painted, name).toEqual([BLACK, BLACK, BLACK])
        else expect(painted.includes(BLACK), name).toBe(false)
        for (const width of [1280, 1920] as const) {
          await page.setViewportSize({ width, height: HEIGHT[width] })
          await settle(page)
          await shoot(page, `${name}-${width}-${theme}`)
        }
      }
    })
  }
})
