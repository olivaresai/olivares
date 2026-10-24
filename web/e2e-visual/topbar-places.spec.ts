// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The top bar's places never run under its search (issue #2764). Between 1024 and 1366 px the
// places' box had no room (flex-1 from a zero basis) and the links painted past it, under the
// search and Stop all. jsdom has no layout, so this measures the real page in Chromium: every
// place link must end before the search starts, and the page-wide overlap scan must find nothing.
// The scan is blind to sr-only runs by design, so at folded widths the explicit per-link loop
// below is the only guard for the places row: do not simplify it away as redundant.
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

// The places this build registers: Community's five, and Business adds Organization. A
// place that silently stops rendering must fail here, not pass unfolded and unmeasured.
const PLACE_KEYS = [
  'home',
  'sessions',
  'agent-tools',
  'rules',
  'audit',
  ...(BUSINESS_BUILD ? ['organization'] : []),
]

/**
 * The fold is measured after the first layout and settles one observer callback and one
 * React commit later: wait for the fonts, then two frames, so a font-driven re-measure is
 * not read mid-flight.
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

/** Every place ends before the search starts, and nothing overlaps on the page. */
async function expectPlacesClearOfSearch(page: Page, where: string) {
  const bar = page.locator('header[data-slot="topbar"]')
  await bar.waitFor()
  await settled(page)
  const search = await bar.locator('button[aria-keyshortcuts]').boundingBox()
  expect(search, where).not.toBeNull()
  const places = bar.locator('nav a[data-place]')
  expect(
    await places.evaluateAll((els) =>
      els.map((el) => el.getAttribute('data-place')),
    ),
    where,
  ).toEqual(PLACE_KEYS)
  const count = await places.count()
  for (let i = 0; i < count; i++) {
    const box = await places.nth(i).boundingBox()
    const name = await places.nth(i).getAttribute('data-place')
    expect(box, name ?? '').not.toBeNull()
    expect(box!.x + box!.width, name ?? '').toBeLessThanOrEqual(search!.x + 0.5)
  }
  expect(await page.evaluate(overlapScan), where).toEqual([])
  // The same axe pass the a11y spec runs, but at every measured width: the issue's
  // gate is "axe 0", and target-size is what a squeezed bar breaks.
  const axe = await new AxeBuilder({ page })
    .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa'])
    .analyze()
  expect(axe.violations, `axe violations ${where}`).toEqual([])
}

// A place with its label is wider than one folded to its icon. The bar used to fold the
// places from 1366 px down: the version, the scope's two switchers, Stop all, favorites
// and the theme took the room. Since #3161 the bar holds the scope, the places, search,
// notifications and the account, so every label shows at the widths people use. The fold
// itself (when a long language or Business's extra place runs out of room) is pinned by
// places-nav.fold.test.tsx.
const LABELLED_PLACE_MIN = 60
for (const width of [1600, 1366, 1100]) {
  test(`places keep their labels at ${width} px`, async ({ browser }) => {
    const context = await browser.newContext({
      viewport: { width, height: 900 },
    })
    const page = await context.newPage()
    try {
      await mockApi(page, 'light')
      await page.goto('/sessions')
      const bar = page.locator('header[data-slot="topbar"]')
      await bar.waitFor()
      await settled(page)
      for (const key of ['sessions', 'audit']) {
        const box = await bar.locator(`a[data-place="${key}"]`).boundingBox()
        expect(box, key).not.toBeNull()
        expect(box!.width, key).toBeGreaterThanOrEqual(LABELLED_PLACE_MIN)
      }
    } finally {
      await context.close()
    }
  })
}

// The Ledger's own query (#2554) lives in the audit filters today; it must stay a usable
// field at every width the places are measured at, not be squeezed by the bar, and while
// it is open nothing on the page runs under it either.
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
      await page.getByRole('button', { name: 'Filters' }).click()
      const query = page.getByRole('textbox', { name: 'Search ledger' })
      await query.waitFor()
      const box = await query.boundingBox()
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
      test(`places stay clear of the search on ${route} at ${width} px, ${theme}`, async ({
        browser,
      }) => {
        const context = await browser.newContext({
          viewport: { width, height: 900 },
        })
        const page = await context.newPage()
        try {
          await mockApi(page, theme)
          await page.goto(route)
          await expectPlacesClearOfSearch(
            page,
            `${route} at ${width} px, ${theme}`,
          )
        } finally {
          await context.close()
        }
      })
    }
  }
}

// The fixed fold was sized to the English Community labels; the wider sets paint under
// the search in the band the reviewer measured (about 1446–1600 px). Measure the room,
// not one language: the Spanish labels (42 characters against English's 30) and the
// Business edition (its Organization place plus the edition chip), alone and together.
const LANGUAGE_EDITION_CASES: ReadonlyArray<{
  lang: Lang
  edition: Edition
}> = [
  { lang: 'es', edition: 'community' },
  { lang: 'en', edition: 'business' },
  { lang: 'es', edition: 'business' },
]
for (const width of [1480, 1536] as const) {
  for (const { lang, edition } of LANGUAGE_EDITION_CASES) {
    test(`places stay clear of the search in ${lang}, ${edition} at ${width} px`, async ({
      browser,
    }) => {
      // The Organization place and its edition chip exist only in the Business build;
      // on Community this case would measure the five Community places and claim more.
      test.skip(
        edition === 'business' && !BUSINESS_BUILD,
        'the Organization place and edition chip are Business-build only',
      )
      const context = await browser.newContext({
        viewport: { width, height: 900 },
      })
      const page = await context.newPage()
      try {
        await mockApi(page, 'light', { lang, edition })
        await page.goto('/sessions')
        await expectPlacesClearOfSearch(
          page,
          `${lang}/${edition} at ${width} px`,
        )
      } finally {
        await context.close()
      }
    })
  }
}
