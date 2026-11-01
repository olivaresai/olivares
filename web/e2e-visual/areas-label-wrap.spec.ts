// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A left-navigation label wraps between words, never inside one ("Operacione / s de / modelos",
// FILM3 review 2026-10-16, es at 1920x1080 @2x). The rows grow to a second line rather than
// cut a label (sidebar.tsx), so a word that is wider than the row must not be the thing that
// gives. Measured in a real browser, because jsdom has no layout: each word of each label (a
// word ends at a space or a hyphen) is asked for its client rects, and a word that paints on
// two lines is a split word. Spaceless scripts (ja, zh) have no words to split.
import { expect, test, type Page } from '@playwright/test'
import { fixtureFor } from './fixtures'

const LOCALES = ['de', 'en', 'es', 'fr', 'ru'] as const
const VIEWPORTS = [
  { width: 1440, height: 900 },
  { width: 1920, height: 1080 },
]

async function mockApi(page: Page, lang: string) {
  await page.route('**/v1/**', async (route) => {
    const path = new URL(route.request().url()).pathname
    if (path.endsWith('/stream'))
      return route.fulfill({
        contentType: 'text/event-stream',
        body: ': connected\n\n',
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
  await page.addInitScript((lng) => {
    localStorage.setItem(
      'olivares.tenant',
      JSON.stringify({ state: { activeTenant: 't-demo' }, version: 0 }),
    )
    localStorage.setItem('olivares.lang', lng)
  }, lang)
}

for (const lang of LOCALES) {
  for (const viewport of VIEWPORTS) {
    test(`left navigation labels keep whole words in ${lang} at ${viewport.width}`, async ({
      browser,
    }) => {
      const context = await browser.newContext({
        viewport,
        deviceScaleFactor: 2,
      })
      const page = await context.newPage()
      try {
        await mockApi(page, lang)
        await page.goto('/model-operations')
        await page.locator('[data-slot="sidebar"]').waitFor()
        // The language is the thing under test: a detection that fell back to English
        // would measure English labels in every case and pass.
        await expect(page.locator('html')).toHaveAttribute('lang', lang)
        await page.evaluate(() => document.fonts.ready)
        const rows = await page
          .locator('[data-slot="sidebar"] a[data-place]:visible')
          .count()
        expect(rows, 'the navigation rendered its places').toBeGreaterThan(10)

        const split = await page.evaluate(() => {
          const out: string[] = []
          for (const row of document.querySelectorAll(
            '[data-slot="sidebar"] a[data-place]',
          )) {
            if (!row.getClientRects().length) continue
            const label = row.querySelector<HTMLElement>('span')
            const text = label?.firstChild
            // A painted row whose label is not one plain text node cannot be measured, and
            // skipping it would let the check pass having looked at nothing.
            if (!label || !text || text.nodeType !== Node.TEXT_NODE) {
              out.push(`unmeasured row: ${row.textContent}`)
              continue
            }
            const value = text.textContent ?? ''
            // A break after a space or a hyphen is a word boundary; a word is what lies
            // between two of them.
            for (const m of value.matchAll(/[^\s-]+/g)) {
              const range = document.createRange()
              range.setStart(text, m.index!)
              range.setEnd(text, m.index! + m[0].length)
              const rects = Array.from(range.getClientRects())
              if (new Set(rects.map((r) => Math.round(r.top))).size > 1) {
                const width = Math.round(rects.reduce((w, r) => w + r.width, 0))
                out.push(
                  `${value} → ${m[0]} is ${width}px wide, the label has ${label.clientWidth}px`,
                )
              }
            }
          }
          return out
        })
        expect(split, 'words painted on two lines').toEqual([])
      } finally {
        await context.close()
      }
    })
  }
}
