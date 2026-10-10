// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { readFileSync } from 'node:fs'
import { expect, test } from '@playwright/test'

// Assert against the shipped catalogs, so a renamed string fails here instead of
// silently passing a negative check.
const sessionsCopy = Object.fromEntries(
  (['en', 'es'] as const).map((lang) => [
    lang,
    JSON.parse(
      readFileSync(
        new URL(`../src/features/sessions/i18n/${lang}.json`, import.meta.url),
        'utf8',
      ),
    ),
  ]),
)

// A disposable engine and an actual refused launch, supplied by the journey harness.
// No intercepted API response stands in for the persisted engine reason.
const runRef = process.env.E2E_FAILED_RUN_REF
const reason = process.env.E2E_FAILED_RUN_REASON

for (const lang of ['en', 'es'] as const) {
  for (const theme of ['light', 'dark'] as const) {
    test(`failed session explains its isolation refusal (${lang}, ${theme})`, async ({
      page,
    }, testInfo) => {
      test.skip(
        !runRef || !reason,
        'Requires a disposable engine with an isolation-refused session',
      )
      await page.addInitScript(
        ([language, appearance]) => {
          localStorage.setItem('olivares.lang', language)
          localStorage.setItem('olivares.theme', appearance)
        },
        [lang, theme],
      )
      await page.setViewportSize({ width: 1280, height: 900 })
      await page.goto('/login')
      await page
        .locator('input[type=email]')
        .first()
        .fill('demo@olivares.local')
      await page.locator('input[type=password]').fill('olivares-demo-estate')
      await page.locator('button[type=submit]').click()
      await page.waitForURL((url) => !url.pathname.startsWith('/login'))
      await page.goto(`/sessions?session=run:${runRef}&pane=narrative`)
      const failure = page.getByTestId('session-failure').first()
      await expect(failure).toBeVisible()
      await expect(failure).toContainText(reason!)
      await expect(page.getByTestId('work-composer')).toHaveCount(0)
      const copy = sessionsCopy[lang]
      await expect(page.getByText(copy.conversation.empty)).toHaveCount(0)
      await expect(failure).toContainText(
        copy.card.hostIsolationRemedy.split('. ')[0].slice(0, 28),
      )
      await expect(
        page.getByRole('button', {
          name: copy.card.actions.startAgain,
          exact: true,
        }),
      ).toHaveCount(0)
      await expect(page.getByText(copy.rail.noObservation)).toHaveCount(0)
      for (const width of [1280, 390]) {
        await page.setViewportSize({ width, height: 900 })
        await expect(failure).toBeVisible()
        expect(
          await page.evaluate(() => document.documentElement.scrollWidth),
        ).toBeLessThanOrEqual(width)
        await page.screenshot({
          path: testInfo.outputPath(`failure-${lang}-${theme}-${width}.png`),
        })
      }
    })
  }
}
