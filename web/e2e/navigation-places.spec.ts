// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE NAVIGATION AND THE NOW HEADER IN A REAL BROWSER (#3121, the one left navigation of
// 0.5), on the seeded demo engine scripts/web-e2e-demo.sh boots. What jsdom cannot measure is
// measured here: the Now header keeps its height while the page loads, the search hint names
// this platform's key, the left navigation cuts no label and folds from the keyboard, the
// narrow drawer holds the same places, and a retired area directory keeps its query and hash.
//
// With CAPTURE_DIR set it also photographs the navigation at 1440 px and its drawer at 390 px
// (light), and Sessions at 1280 and 1920 px (dark), for the review.
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

/** Pin the language, the theme and the demo organization before the first document. */
async function prefer(page: Page, theme: 'light' | 'dark') {
  await page.addInitScript(
    ([t, tenant]) => {
      window.localStorage.setItem('olivares.theme', t)
      window.localStorage.setItem('olivares.lang', 'en')
      window.localStorage.setItem(
        'olivares.tenant',
        JSON.stringify({ state: { activeTenant: tenant }, version: 0 }),
      )
    },
    [theme, demoTenant],
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

test.describe('the navigation and the Now header (#3121) @nav3121', () => {
  test.skip(!demoTenant, 'needs the seeded demo engine (DEMO_TENANT)')

  test('Now keeps its header block still while the page loads, New session first', async ({
    page,
  }) => {
    await page.setViewportSize({ width: 1280, height: 900 })
    await prefer(page, 'dark')
    await signIn(page)
    // Every frame of the first seconds: the header's height and the top of the block
    // under it. A line that arrives late changes one of them.
    await page.addInitScript(() => {
      const w = window as unknown as { __frames: number[][] }
      w.__frames = []
      const tick = () => {
        const header = document.querySelector('[data-slot="page-header"]')
        const next = header?.nextElementSibling
        if (header && next && w.__frames.length < 400)
          w.__frames.push([
            header.getBoundingClientRect().height,
            next.getBoundingClientRect().top,
          ])
        requestAnimationFrame(tick)
      }
      requestAnimationFrame(tick)
    })
    // Every engine answer arrives late, so the page is observed before the tools, the
    // runs and the ledger answer: what a slow link shows, and where a late line would move
    // the page.
    await page.route('**/v1/**', async (route) => {
      await new Promise((done) => setTimeout(done, 800))
      await route.continue()
    })
    await page.goto('/')
    await expect(
      page.getByRole('heading', { level: 1, name: 'Now' }),
    ).toBeVisible()
    await page.waitForLoadState('networkidle')
    await page.waitForTimeout(1500)
    const frames = await page.evaluate(
      () => (window as unknown as { __frames: number[][] }).__frames,
    )
    expect(frames.length).toBeGreaterThan(10)
    const heights = [...new Set(frames.map(([h]) => Math.round(h)))]
    const tops = [...new Set(frames.map(([, top]) => Math.round(top)))]
    expect(heights, 'the Now header changed height while loading').toHaveLength(
      1,
    )
    expect(
      tops,
      'the content under the header moved while loading',
    ).toHaveLength(1)
    // The start line's verb is the first control of the page: New session, or the setup
    // step that comes before it when no tool is ready.
    const header = page.locator('[data-slot="page-header"]')
    await expect(
      header
        .locator('a[href], button')
        .first()
        .locator('xpath=ancestor::*[@data-testid="now-start"]'),
    ).toHaveCount(1)
    await page.unroute('**/v1/**')
    // "Partial data" at most once on the page.
    expect(
      await page.getByText('Partial data', { exact: true }).count(),
    ).toBeLessThanOrEqual(1)
    await shoot(page, 'now-1280-dark')
    await page.setViewportSize({ width: 1920, height: 1080 })
    await shoot(page, 'now-1920-dark')
  })

  test('the search hint names the key this platform has', async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 900 })
    await prefer(page, 'dark')
    await signIn(page)
    const search = page.getByRole('button', { name: 'Search and commands' })
    const platform = await page.evaluate(() => navigator.platform)
    const hint = /mac/i.test(platform) ? '⌘K' : 'Ctrl+K'
    // The hint is drawn, not text, so the button's text stays inside its name (WCAG 2.5.3).
    await expect(search.locator('kbd')).toHaveAttribute('data-keys', hint)
    await expect(search).toHaveText('Search')
    await expect(search).toHaveAttribute('title', 'Search and commands')
  })

  test('the left navigation names each place once, in its groups, and wraps no word', async ({
    page,
  }) => {
    await page.setViewportSize({ width: 1440, height: 900 })
    await prefer(page, 'light')
    await signIn(page)
    await page.goto('/')
    const nav = page.locator('[data-slot="sidebar"]')
    await expect(nav).toBeVisible()
    const groups = await nav
      .locator('section')
      .evaluateAll((s) => s.map((g) => g.getAttribute('aria-label')))
    expect(groups).toEqual(['Work', 'Tools', 'Control', 'Admin'])
    const places = await nav
      .locator('a[data-place]')
      .evaluateAll((links) => links.map((a) => a.textContent?.trim()))
    // A navigation that rendered nothing would pass every check below.
    expect(places.length).toBeGreaterThan(10)
    expect(places.filter((p, i) => places.indexOf(p) !== i)).toEqual([])
    expect(places[0]).toBe('Now')
    expect(places.at(-1)).toBe('Settings')
    // No second navigation: no places in the top bar, no All areas, no phone bar.
    await expect(page.locator('[data-slot="topbar"] nav')).toHaveCount(0)
    await expect(page.getByText('All areas', { exact: true })).toHaveCount(0)
    await expect(page.locator('[data-slot="phone-bar"]')).toHaveCount(0)
    // A label grows to a second line rather than being cut.
    const cut = await nav
      .locator('a[data-place] span:not(.sr-only)')
      .evaluateAll((spans) =>
        spans
          .filter((s) => s.scrollWidth > s.clientWidth + 1)
          .map((s) => s.textContent),
      )
    expect(cut).toEqual([])
    await shoot(page, 'navigation-desktop-light')
  })

  test('folds to its icons and opens again from the keyboard', async ({
    page,
  }) => {
    await page.setViewportSize({ width: 1440, height: 900 })
    await prefer(page, 'light')
    await signIn(page)
    await page.goto('/')
    const nav = page.locator('[data-slot="sidebar"]')
    const wide = (await nav.boundingBox())!.width
    await page.getByRole('button', { name: 'Collapse navigation' }).focus()
    await page.keyboard.press('Enter')
    await expect(nav).toHaveAttribute('data-folded', 'true')
    expect((await nav.boundingBox())!.width).toBeLessThan(wide)
    await page.getByRole('button', { name: 'Expand navigation' }).focus()
    await page.keyboard.press('Space')
    await expect(nav).not.toHaveAttribute('data-folded', 'true')
  })

  test('on a phone, the menu button opens the same navigation as a drawer', async ({
    page,
  }) => {
    await page.setViewportSize({ width: 1440, height: 900 })
    await prefer(page, 'light')
    await signIn(page)
    await page.goto('/sessions')
    const desktop = await page
      .locator('[data-slot="sidebar"] a[data-place]')
      .evaluateAll((links) => links.map((a) => a.getAttribute('data-place')))
    await page.setViewportSize({ width: 390, height: 844 })
    await expect(page.locator('[data-slot="sidebar"]')).toBeHidden()
    await page.getByRole('button', { name: 'Open navigation' }).click()
    const drawer = page.locator('[data-slot="navigation-drawer"]')
    await expect(drawer).toBeVisible()
    const narrow = await drawer
      .locator('a[data-place]')
      .evaluateAll((links) => links.map((a) => a.getAttribute('data-place')))
    expect(narrow).toEqual(desktop)
    await shoot(page, 'navigation-mobile-drawer-light')
    await drawer.getByRole('link', { name: 'Now' }).click()
    await expect(drawer).toBeHidden()
    await expect(page).toHaveURL(/\/$/)
  })

  test('a retired area directory lands on its place with its query and hash', async ({
    page,
  }) => {
    await prefer(page, 'light')
    await signIn(page)
    await page.goto('/areas/observation?from=2026-10-01#entry-7')
    await expect(page).toHaveURL(/\/audit\?from=2026-10-01#entry-7$/)
    await expect(
      page.locator('[data-slot="sidebar"] [aria-current="page"]'),
    ).toHaveAttribute('data-place', 'activity')
  })

  test('captures Sessions at both widths', async ({ page }) => {
    test.skip(!captureDir, 'captures only')
    await prefer(page, 'dark')
    await signIn(page)
    for (const [width, height] of [
      [1280, 900],
      [1920, 1080],
    ]) {
      await page.setViewportSize({ width, height })
      await page.goto('/sessions')
      // Sessions streams live, so the network never goes idle: wait for the bar, then
      // give the list its first answer.
      await page.locator('[data-slot="topbar"]').waitFor()
      await page.waitForTimeout(2500)
      await shoot(page, `sessions-${width}-dark`)
    }
  })
})
