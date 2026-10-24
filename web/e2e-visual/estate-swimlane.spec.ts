// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE LAST-HOUR SWIMLANE IN A REAL BROWSER (#2547). Estate over the demo estate's event log
// (src/features/home/swimlane.fixture.ts) with the clock held at its 16:42: the lanes match
// the events, no label overlaps a bar and axe finds nothing at 1440 and 1024 in both
// themes, and a drag across the lanes opens the Ledger on that range (since/until).
//
// Run: pnpm exec playwright test -c playwright.visual.config.ts e2e-visual/estate-swimlane.spec.ts
import AxeBuilder from '@axe-core/playwright'
import { expect, test, type Page } from '@playwright/test'
import {
  approvals,
  at,
  calls,
  events,
  iso,
  lives,
  NOW,
  run,
  runs as estateRuns,
} from '../src/features/home/swimlane.fixture'

// The demo estate, plus a session that failed at 16:38: its word sits by the now line,
// where a label is most likely to run into a bar.
const failed = run('bad', 'failed', {
  name: 'Run the migration dry run',
  provider_driver: 'claude',
  stopped_at: iso(at('16:38')),
  acts_for: { kind: 'user', name: 'Ana Ruiz' },
})
const runs = [...estateRuns, failed]
const eventsOf = (ref: string) =>
  ref === 'bad' ? calls('16:30', '16:38') : events[ref]
import { orangeScan } from './console-qa/orange'
import { overlapScan } from './console-qa/overlap'
import { fixtureFor } from './fixtures'

async function showEstate(page: Page, theme: string, ledger: string[] = []) {
  await page.route('**/v1/**', async (route) => {
    const url = new URL(route.request().url())
    const p = url.pathname
    if (p.endsWith('/stream'))
      return route.fulfill({
        status: 200,
        contentType: 'text/event-stream',
        body: ': connected\n\n',
      })
    const timeline = /^\/v1\/m\/sessions\/live\/by-id\/([^/]+)\/timeline$/.exec(
      p,
    )
    let payload: unknown
    if (timeline) {
      const owner = runs.find(
        (r) => r.live_ref === decodeURIComponent(timeline[1]!),
      )
      payload = { items: owner ? eventsOf(owner.run_ref) : [], has_more: false }
    } else if (p === '/v1/m/sessions/live')
      payload = { items: lives, has_more: false }
    else if (p === '/v1/m/sessions/runs')
      payload = { items: runs, has_more: false }
    else if (p === '/v1/m/governance/approvals')
      payload = { items: approvals, has_more: false }
    else {
      if (p === '/v1/audit') ledger.push(url.search)
      payload = fixtureFor(p) ?? { items: [], has_more: false }
    }
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(payload),
    })
  })
  await page.addInitScript(
    ({ theme }) => {
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
      localStorage.setItem('olivares.lang', 'en')
      localStorage.setItem('olivares.theme', theme)
    },
    { theme },
  )
  await page.clock.setFixedTime(NOW)
  await page.goto('/')
  const swimlane = page.getByTestId('estate-swimlane')
  await expect(swimlane.getByTestId('swimlane-lane')).toHaveCount(8)
  // Every lane read: the demo estate's 20 runs and the failed session's one, a bar each.
  await expect(swimlane.locator('[data-mark="run"]')).toHaveCount(21)
  await expect(swimlane.locator('[data-mark="failure"]')).toHaveCount(1)
  return swimlane
}

for (const theme of ['dark', 'light'])
  for (const width of [1440, 1024])
    test(`the last-hour swimlane at ${width} in ${theme}: axe 0, overlap 0`, async ({
      page,
    }) => {
      await page.setViewportSize({ width, height: 900 })
      const swimlane = await showEstate(page, theme)
      await expect(swimlane.locator('[data-mark="ask-flagged"]')).toHaveCount(1)
      await swimlane.screenshot({
        path: test.info().outputPath(`swimlane-${width}-${theme}.png`),
      })
      const audit = await new AxeBuilder({ page })
        .include('[data-testid="estate-swimlane"]')
        .analyze()
      expect(audit.violations.map((v) => `${v.id}: ${v.nodes.length}`)).toEqual(
        [],
      )
      expect(await page.evaluate(overlapScan)).toEqual([])
      // The waiting-for-approval mark is a warn tint, not a gradient: no visible glow (#2946).
      expect(
        (await page.evaluate(orangeScan)).hits.filter((h) => h.visible),
      ).toEqual([])
      // No title is cut so far that it says nothing: each keeps a visible box.
      for (const name of await swimlane.locator('li a .truncate').all())
        expect((await name.boundingBox())?.width ?? 0).toBeGreaterThan(40)
    })

test('a drag across the lanes opens the Ledger on that range', async ({
  page,
}) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const ledger: string[] = []
  const swimlane = await showEstate(page, 'dark', ledger)
  const plot = await swimlane.getByTestId('swimlane-plot').boundingBox()
  const lane = await swimlane.getByTestId('swimlane-lane').nth(2).boundingBox()
  expect(plot && lane).toBeTruthy()
  // From 16:02:30 to 16:31:30 of the hour 15:42 to 16:42: mid-minute, so a fraction of a
  // pixel cannot move the whole minutes the Ledger gets (16:02 to 16:32).
  const x = (minutes: number) => plot!.x + (plot!.width * minutes) / 60
  const y = lane!.y + lane!.height / 2
  await page.mouse.move(x(20.5), y)
  await page.mouse.down()
  await page.mouse.move(x(35), y, { steps: 5 })
  await page.mouse.move(x(49.5), y, { steps: 5 })
  await page.mouse.up()
  await expect(swimlane.getByTestId('swimlane-range')).toBeVisible()
  await swimlane.getByRole('link', { name: 'Open in the Ledger' }).click()
  await page.waitForURL((u) => u.pathname === '/audit')
  const address = new URL(page.url()).searchParams
  expect(address.get('since')).toBe('2026-10-17T16:02:00.000Z')
  expect(address.get('until')).toBe('2026-10-17T16:32:00.000Z')
  // The Ledger reads that window.
  await expect
    .poll(() =>
      ledger.some((q) => {
        const s = new URLSearchParams(q)
        return (
          s.get('since') === '2026-10-17T16:02:00.000Z' &&
          s.get('until') === '2026-10-17T16:32:00.000Z'
        )
      }),
    )
    .toBe(true)
})
