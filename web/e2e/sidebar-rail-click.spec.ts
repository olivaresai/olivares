// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// EU-17 (Business 06): at 1280x900 "All areas" was painted over the session rail and took
// the click, so a session could not be opened from the sidebar. With twelve operated
// sessions every visible rail row must receive its own click, All areas must sit below the
// rail, and at 390 px (no sidebar) a session opens from the Sessions list. Light and dark.
import { expect, test, type Page } from '@playwright/test'

const EMAIL = process.env.E2E_EMAIL ?? 'demo@olivares.local'
const PASSWORD = process.env.E2E_PASSWORD ?? 'olivares-demo-estate'

const now = Date.now()
const runs = Array.from({ length: 12 }, (_, i) => ({
  run_ref: `01a0f8bd-0000-7000-8000-${String(i).padStart(12, '0')}`,
  name: `Session number ${i + 1}`,
  state: 'running',
  transport: 'stream-json',
  permission_mode: 'default',
  isolation: 'native',
  last_event_seq: 1,
  pep_provisioned: true,
  record_io: false,
  critical: false,
  provider_driver: 'claude',
  workspace_path: `/srv/project-${i}`,
  created_at: new Date(now - i * 60_000).toISOString(),
  started_at: new Date(now - i * 60_000).toISOString(),
}))

async function signIn(page: Page) {
  await page.route('**/v1/m/sessions/runs?**', (r) =>
    r.fulfill({ json: { items: runs, has_more: false } }),
  )
  await page.route('**/v1/m/sessions/live?**', (r) =>
    r.fulfill({ json: { items: [], has_more: false } }),
  )
  await page.goto('/login')
  await page.locator('input[type=email]').first().fill(EMAIL)
  await page.locator('input[type=password]').first().fill(PASSWORD)
  await page.locator('button[type=submit]').first().click()
  await page.waitForURL((url) => !url.pathname.startsWith('/login'), {
    timeout: 20_000,
  })
}

/** Rows whose centre is answered by something other than the row itself. */
function stolenRows(page: Page) {
  return page.evaluate(() => {
    const rail = document.querySelector('[data-slot="session-rail"]')
    const box = rail?.getBoundingClientRect()
    if (!box) return ['no rail']
    return [...rail!.querySelectorAll<HTMLElement>('[data-rail-row]')]
      .filter((r) => {
        const b = r.getBoundingClientRect()
        return b.bottom > box.top && b.top < box.bottom
      })
      .filter((r) => {
        const b = r.getBoundingClientRect()
        const y = Math.min(b.top + b.height / 2, box.bottom - 2)
        const hit = document.elementFromPoint(b.left + b.width / 2, y)
        return !hit || !r.contains(hit)
      })
      .map((r) => r.getAttribute('aria-label') ?? '?')
  })
}

for (const scheme of ['light', 'dark'] as const) {
  test(`the sidebar rail takes its own clicks at 1280x900 (${scheme})`, async ({
    page,
  }) => {
    await page.setViewportSize({ width: 1280, height: 900 })
    await page.emulateMedia({ colorScheme: scheme })
    await signIn(page)
    await page.goto('/')
    const rail = page.locator('[data-slot="session-rail"]')
    // The rail lists at most eight per group; the twelve still fill it past its height.
    await expect(rail.locator('[data-rail-row]')).toHaveCount(8)
    expect(await stolenRows(page)).toEqual([])
    const areas = page.getByRole('button', { name: /All areas/ })
    const railBox = await rail.boundingBox()
    const areasBox = await areas.boundingBox()
    expect(areasBox!.y).toBeGreaterThanOrEqual(
      railBox!.y + railBox!.height - 0.5,
    )
    const last = rail.locator('[data-rail-row]').last()
    await last.scrollIntoViewIfNeeded()
    await last.click()
    await expect(page).toHaveURL(/session=/)
  })

  test(`a session opens from the Sessions list at 390 px (${scheme})`, async ({
    page,
  }) => {
    await page.setViewportSize({ width: 390, height: 844 })
    await page.emulateMedia({ colorScheme: scheme })
    await signIn(page)
    await page.goto('/sessions')
    // The narrow layout keeps a hidden copy of the list: take the one on screen.
    const row = page
      .getByText('Session number 10')
      .filter({ visible: true })
      .first()
    await row.scrollIntoViewIfNeeded()
    await row.click()
    await expect(page).toHaveURL(/session=/)
  })
}
