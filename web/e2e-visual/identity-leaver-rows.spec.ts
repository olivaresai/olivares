// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { expect, test, type Page } from '@playwright/test'
import { browserSession, fixtureFor } from './fixtures'

// The Identity -> Federation "Leaver evidence" card paints one row per audit event. jsdom has no
// layout, so the 390 px behaviour is measured here, in a real browser, against ledger events in the
// shape the engine sends (core/api/dto.go AuditEventDTO: `occurred_at`, 36-character target ids).
const ROWS = 10
// The last row's id has no hyphen to break at: only `break-all` keeps it inside the card.
const targetId = (n: number) =>
  n === ROWS
    ? 'a3f1c9e07b4d4e6a9f0c5d2b81e6c7f4a3f1c9e07b4d4e6a9f0c5d2b81e6c7f4'
    : `3f2a9c1e-7b4d-4e6a-9f0c-${String(n).padStart(12, '0')}`

async function mockApi(page: Page, theme: string) {
  await page.route('**/v1/**', async (route) => {
    const url = new URL(route.request().url())
    const path = url.pathname
    if (path === '/v1/auth/browser-session')
      return route.fulfill({ json: browserSession })
    if (path.endsWith('/stream'))
      return route.fulfill({
        contentType: 'text/event-stream',
        body: ': connected\n\n',
      })
    if (path === '/v1/audit' && url.searchParams.get('action'))
      return route.fulfill({
        json: {
          items: Array.from({ length: ROWS }, (_, i) => ({
            id: `evt-${i}`,
            seq: i + 1,
            // The engine's time layout: nine fractional digits (core/model/time.go).
            occurred_at: `2026-10-0${(i % 9) + 1}T09:30:00.123456789Z`,
            actor: 'scim',
            actor_kind: 'service',
            action: 'scim.user.deprovision',
            target_kind: 'user',
            target_id: targetId(i + 1),
            prev_hash: '00',
            hash: '01',
          })),
          has_more: false,
          scan_complete: true,
          head_seq: ROWS,
        },
      })
    return route.fulfill({
      json: fixtureFor(path) ?? { items: [], has_more: false },
    })
  })
  await page.addInitScript(
    ({ theme }) => {
      localStorage.setItem('olivares.lang', 'en')
      localStorage.setItem('olivares.theme', theme)
      localStorage.setItem(
        'olivares.tenant',
        JSON.stringify({ state: { activeTenant: 't-demo' }, version: 0 }),
      )
    },
    { theme },
  )
}

async function openLeaverCard(page: Page, width: number, theme: string) {
  await page.setViewportSize({ width, height: 900 })
  await mockApi(page, theme)
  await page.goto('/identity?tab=federation')
  const heading = page.getByRole('heading', { name: 'Leaver evidence' })
  await expect(heading).toBeVisible()
  const card = heading.locator('xpath=..')
  await expect(card.locator('dl > div')).toHaveCount(ROWS)
  await page.evaluate(() => document.fonts.ready)
  return card
}

// What a row says does not depend on the width or the theme: one run.
test('every leaver row says when it was processed', async ({ page }) => {
  const card = await openLeaverCard(page, 390, 'light')
  const rows = card.locator('dl > div')
  await expect(card.locator('dl time')).toHaveCount(ROWS)
  for (let i = 0; i < ROWS; i += 1) {
    await expect(rows.nth(i).locator('time')).toHaveAttribute(
      'datetime',
      /^2026-10-0\d/,
    )
  }
})

for (const theme of ['light', 'dark']) {
  for (const width of [1280, 390, 320]) {
    test(`no leaver row paints outside its card at ${width} in ${theme}`, async ({
      page,
    }, testInfo) => {
      const card = await openLeaverCard(page, width, theme)
      // The id, the badge and the time are fully inside the card's box, so nothing is lost to an
      // ancestor that clips. The page itself must not grow either.
      const outside = await card.evaluate((cardEl) => {
        const box = cardEl.getBoundingClientRect()
        const lost: string[] = []
        // Every element of a row: the label cell, the value cell and what each holds (id, badge, time).
        for (const el of cardEl.querySelectorAll(
          'dl dt, dl dd, dl dt *, dl dd *',
        )) {
          const range = document.createRange()
          range.selectNodeContents(el)
          const r = range.getBoundingClientRect()
          if (!r.width && !r.height) continue
          // The card's own box grows with its content, so the viewport bounds it too.
          if (
            r.right > box.right + 0.5 ||
            r.left < box.left - 0.5 ||
            r.right > window.innerWidth + 0.5
          )
            lost.push(
              `${el.textContent} right=${Math.round(r.right)} card=${Math.round(box.right)}`,
            )
        }
        return lost
      })
      expect(outside, 'row content inside the card').toEqual([])
      expect(
        await page.evaluate(() => document.documentElement.scrollWidth),
      ).toBeLessThanOrEqual(width)

      await card.screenshot({
        path: testInfo.outputPath(`leaver-card-${width}-${theme}.png`),
      })
    })
  }
}
