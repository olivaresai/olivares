// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE NAVIGATION AND THE NOW HEADER IN A REAL BROWSER (issue #3121), on the seeded demo
// engine scripts/web-e2e-demo.sh boots. What jsdom cannot measure is measured here: the Now
// header keeps its height and the content under it keeps its place while the page loads,
// the search hint names this platform's key, and every row of All areas fits one line.
//
// With CAPTURE_DIR set it also photographs Now, All areas and Sessions at 1280 and 1920 px,
// in the dark theme of the owner's reference screenshots, for the review.
//
// ⛔ THE DEMO GUARD: without DEMO_TENANT this would grade the login page; it skips instead.
import { expect, test, type Locator, type Page } from '@playwright/test'
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

/** Unfold every area of the sheet: each click takes one fold out of the set. */
async function unfoldAll(sheet: Locator) {
  const folded = sheet.locator('[data-nav-area] button[aria-expanded="false"]')
  while ((await folded.count()) > 0) await folded.first().click()
}

/** Open All areas from the account menu, as it first opens: every area folded. */
async function openAllAreas(page: Page) {
  await page.getByRole('button', { name: /, Account$/ }).click()
  await page.getByRole('menuitem', { name: 'All areas' }).click()
  const sheet = page.getByRole('dialog', { name: 'All areas' })
  await expect(sheet).toBeVisible()
  return sheet
}

/** Back to the top of the sheet's list, for the picture. */
async function toTop(sheet: Locator) {
  await sheet
    .locator('[data-radix-scroll-area-viewport]')
    .first()
    .evaluate((el) => el.scrollTo(0, 0))
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
    await expect(search).toContainText(hint)
    if (hint !== '⌘K') await expect(search).not.toContainText('⌘')
    await expect(search).toHaveAttribute('title', 'Search and commands')
  })

  test('All areas lists each page once, an area then its pages, each on one line', async ({
    page,
  }) => {
    await page.setViewportSize({ width: 1280, height: 900 })
    await prefer(page, 'dark')
    await signIn(page)
    await page.goto('/')
    const places = await page
      .getByRole('navigation', { name: 'Journeys' })
      .getByRole('link')
      .evaluateAll((links) => links.map((a) => a.getAttribute('href')))
    const sheet = await openAllAreas(page)
    await shoot(page, 'all-areas-folded-1280-dark')
    await unfoldAll(sheet)
    const rows = sheet.locator('[data-nav-row]')
    const hrefs = await rows.evaluateAll((links) =>
      links.map((a) => a.getAttribute('href')),
    )
    // A sheet that rendered nothing would pass every check below.
    expect(hrefs.length).toBeGreaterThan(20)
    // Each route once, and none the top bar already offers.
    expect(hrefs.filter((h, i) => hrefs.indexOf(h) !== i)).toEqual([])
    expect(hrefs.filter((h) => places.includes(h))).toEqual([])
    // Two levels: no heading between an area and its pages.
    expect(await sheet.locator('[data-nav-area] [role="group"]').count()).toBe(
      0,
    )
    // Every label on one line at the default width.
    const wrapped = await rows.evaluateAll((links) =>
      links
        .map((a) => {
          const label = a.querySelector('span') as HTMLElement | null
          if (!label) return null
          const style = getComputedStyle(label)
          const declared = parseFloat(style.lineHeight)
          // `normal` has no number: the browser's normal line is about 1.2 font sizes.
          const line = Number.isFinite(declared)
            ? declared
            : parseFloat(style.fontSize) * 1.2
          return label.getBoundingClientRect().height > line * 1.5
            ? label.textContent
            : null
        })
        .filter(Boolean),
    )
    expect(wrapped).toEqual([])
    await toTop(sheet)
    await shoot(page, 'all-areas-1280-dark')
    await page.setViewportSize({ width: 1920, height: 1080 })
    await shoot(page, 'all-areas-1920-dark')
  })

  test('on a phone, More lists the places the phone bar leaves out', async ({
    page,
  }) => {
    await page.setViewportSize({ width: 390, height: 844 })
    await prefer(page, 'dark')
    await signIn(page)
    await page.goto('/')
    await page.getByRole('button', { name: /^More/ }).click()
    const sheet = page.getByRole('dialog', { name: 'All areas' })
    await expect(sheet).toBeVisible()
    await unfoldAll(sheet)
    const hrefs = await sheet
      .locator('[data-nav-row]')
      .evaluateAll((links) => links.map((a) => a.getAttribute('href')))
    // Rules and Ledger have no room in the phone bar: their pages stay in their areas.
    expect(hrefs).toEqual(expect.arrayContaining(['/permissions', '/audit']))
    expect(hrefs).not.toContain('/sessions')
    await shoot(page, 'all-areas-390-dark')
  })

  test('captures the top bar on Sessions at both widths', async ({ page }) => {
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
    // The places fold to their icons when the bar runs out of room; each keeps a tooltip.
    await page.setViewportSize({ width: 1024, height: 768 })
    await page.goto('/')
    await expect(
      page.getByRole('heading', { level: 1, name: 'Now' }),
    ).toBeVisible()
    await page.waitForTimeout(1500)
    await shoot(page, 'now-1024-dark-folded')
  })
})
