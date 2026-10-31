// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The top bar never paints its title under its search, and the left navigation never cuts a
// label (0.5; the places left the bar). jsdom has no layout, so this measures the real page in
// Chromium: the title must end before the search starts, the page-wide overlap scan must find
// nothing, axe must find nothing, and every place label in the sidebar must fit its box.
import AxeBuilder from '@axe-core/playwright'
import { expect, test, type Page } from '@playwright/test'
import { BUSINESS_BUILD } from '../src/features/business-build'
import { fixtureFor, serverInfo } from './fixtures'
import { overlapScan } from './console-qa/overlap'

const WIDTHS = [1440, 1366, 1280, 1100, 1024, 900] as const
const ROUTES = ['/sessions', '/audit'] as const
const THEMES = ['light', 'dark'] as const
type Lang = 'en' | 'es'
type Edition = 'community' | 'business'

async function mockApi(
  page: Page,
  theme: (typeof THEMES)[number],
  {
    lang = 'en',
    edition = 'community',
  }: { lang?: Lang; edition?: Edition } = {},
) {
  await page.route('**/v1/**', async (route) => {
    const path = new URL(route.request().url()).pathname
    if (path.endsWith('/stream'))
      return route.fulfill({
        contentType: 'text/event-stream',
        body: ': connected\n\n',
      })
    if (edition === 'business' && path === '/v1/server-info')
      return route.fulfill({
        json: {
          ...serverInfo,
          edition: 'business',
          license: {
            status: 'valid',
            licensee: 'Fixture',
            support_profile: 'business',
          },
        },
      })
    if (edition === 'business' && path.endsWith('/consoleviews/ui-state'))
      return route.fulfill({
        json: { stored: true, sidebar: 'full', show_edition_badge: true },
      })
    const value = path.endsWith('/auth/browser-session')
      ? {
          csrf_token: 'fixture-csrf',
          session_id: 'fixture-session',
          expires_at: '2030-01-01T00:00:00Z',
        }
      : path.endsWith('/favorites')
        ? { stored: true, favorites: [] }
        : (fixtureFor(path) ?? { items: [], has_more: false })
    await route.fulfill({
      contentType: 'application/json',
      body: JSON.stringify(value),
    })
  })
  await page.addInitScript(
    ({ chosen, lng }) => {
      localStorage.setItem(
        'olivares.tenant',
        JSON.stringify({ state: { activeTenant: 't-demo' }, version: 0 }),
      )
      localStorage.setItem('olivares.lang', lng)
      localStorage.setItem('olivares.theme', chosen)
    },
    { chosen: theme, lng: lang },
  )
}

/**
 * Layout settles one React commit after the first paint: wait for the fonts, then two
 * frames, so a font-driven re-layout is not read mid-flight.
 */
async function settled(page: Page) {
  await page.evaluate(() => document.fonts.ready)
  await page.evaluate(
    () =>
      new Promise((resolve) =>
        requestAnimationFrame(() => requestAnimationFrame(resolve)),
      ),
  )
}

/** The page title ends before the search starts, and nothing overlaps on the page. */
async function expectBarClear(page: Page, where: string) {
  const bar = page.locator('header[data-slot="topbar"]')
  await bar.waitFor()
  await settled(page)
  // The search: from 1100 px the Ledger puts its own query field in the bar (measured at
  // its left edge, not its ⌘K key, and only once the page has claimed the slot); the
  // shell's search button everywhere else.
  const ledgerField =
    new URL(page.url()).pathname === '/audit' &&
    (page.viewportSize()?.width ?? 0) >= 1100
  const searchBox = ledgerField
    ? bar.locator('[data-slot="topbar-search"] [role="search"]')
    : bar.locator('button[aria-keyshortcuts]')
  await searchBox.waitFor()
  await settled(page)
  const search = await searchBox.boundingBox()
  expect(search, where).not.toBeNull()
  // No places in the bar: the left navigation is the only one.
  expect(await bar.locator('nav').count(), where).toBe(0)
  const title = await bar.locator('[data-slot="topbar-title"]').boundingBox()
  expect(title, where).not.toBeNull()
  expect(title!.x + title!.width, where).toBeLessThanOrEqual(search!.x + 0.5)
  expect(await page.evaluate(overlapScan), where).toEqual([])
  // The same axe pass the a11y spec runs, but at every measured width: the issue's
  // gate is "axe 0", and target-size is what a squeezed bar breaks.
  const axe = await new AxeBuilder({ page })
    .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa'])
    .analyze()
  expect(axe.violations, `axe violations ${where}`).toEqual([])
}

// The left navigation grows a row to a second line rather than cut a label: measured in
// every shipped Latin language at the column's width, where a cut shows as a span whose
// content is wider than its box.
for (const lang of ['en', 'es'] as const) {
  for (const edition of ['community', 'business'] as const) {
    test(`the left navigation cuts no label in ${lang}, ${edition}`, async ({
      browser,
    }) => {
      test.skip(
        edition === 'business' && !BUSINESS_BUILD,
        'Business places are Business-build only',
      )
      const context = await browser.newContext({
        viewport: { width: 1440, height: 900 },
      })
      const page = await context.newPage()
      try {
        await mockApi(page, 'light', { lang, edition })
        await page.goto('/sessions')
        const nav = page.locator('[data-slot="sidebar"]')
        await nav.waitFor()
        await settled(page)
        const places = nav.locator('a[data-place]')
        expect(await places.count()).toBeGreaterThan(10)
        expect(
          await places
            .locator('span:not(.sr-only)')
            .evaluateAll((spans) =>
              spans
                .filter((s) => s.scrollWidth > s.clientWidth + 1)
                .map((s) => s.textContent),
            ),
        ).toEqual([])
      } finally {
        await context.close()
      }
    })
  }
}

// The Ledger's own query (#2554): in the top bar's search from 1100 px, at the head of the
// page below. It must stay a usable field at every measured width, not be squeezed by the
// bar, and nothing on the page runs under it either.
const LEDGER_QUERY_MIN = 200
for (const width of WIDTHS) {
  test(`the Ledger query keeps a usable width at ${width} px`, async ({
    browser,
  }) => {
    const context = await browser.newContext({
      viewport: { width, height: 900 },
    })
    const page = await context.newPage()
    try {
      await mockApi(page, 'light')
      await page.goto('/audit')
      await page.locator('header[data-slot="topbar"]').waitFor()
      const field = page
        .getByRole('search')
        .filter({ has: page.getByRole('textbox', { name: 'Ledger query' }) })
      await field.waitFor()
      await settled(page)
      const box = await field.boundingBox()
      expect(box).not.toBeNull()
      expect(box!.width).toBeGreaterThanOrEqual(LEDGER_QUERY_MIN)
      expect(await page.evaluate(overlapScan)).toEqual([])
    } finally {
      await context.close()
    }
  })
}

for (const theme of THEMES) {
  for (const route of ROUTES) {
    for (const width of WIDTHS) {
      test(`the bar stays clear on ${route} at ${width} px, ${theme}`, async ({
        browser,
      }) => {
        const context = await browser.newContext({
          viewport: { width, height: 900 },
        })
        const page = await context.newPage()
        try {
          await mockApi(page, theme)
          await page.goto(route)
          await expectBarClear(page, `${route} at ${width} px, ${theme}`)
        } finally {
          await context.close()
        }
      })
    }
  }
}
