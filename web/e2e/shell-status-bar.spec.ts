// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The shell on the built binary (#3161): the top bar holds the scope, the places, search,
// notifications and the account; the status bar under the work says how things stand.
// Stop all is absent while no session runs, present while one does, and goes through the
// kill switch's confirmation, which names how many sessions stop. The session reads are
// answered here so "running" is a fact of the test, not of the demo estate; the stop
// itself is answered here too, so the run engages nothing on the engine it shares.
//
// Run through scripts/docs-captures.sh e2e/shell-status-bar.spec.ts --grep @shell.
import { expect, test, type Page, type Route } from '@playwright/test'
import { mkdirSync } from 'node:fs'
import { resolve } from 'node:path'

const demoTenant = process.env.DEMO_TENANT ?? ''
const captureDir = process.env.CAPTURE_DIR
  ? resolve(process.env.CAPTURE_DIR)
  : ''
/** The themes to capture: Black joins once the theme store offers it. */
const THEMES = (process.env.CAPTURE_THEMES ?? 'light,dark').split(',')
const EMAIL = 'demo@olivares.local'
const PASSWORD = 'olivares-demo-estate'

/** Pin the language, the theme and the demo organization before the first document. */
async function prefer(page: Page, theme: string) {
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

/** One session the engine would report as running. */
function runningRun() {
  const now = new Date().toISOString()
  return {
    run_ref: 'e2e-status-bar-run',
    name: 'Review the release notes',
    engine: 'claude',
    transport: 'stream-json',
    permission_mode: 'default',
    isolation: 'native',
    state: 'running',
    last_event_seq: 1,
    created_at: now,
    started_at: now,
    last_activity_at: now,
    pep_provisioned: false,
    record_io: false,
  }
}

/** Answer the rail's session reads: `running` runs, and no live snapshots. */
async function sessions(page: Page, running: number) {
  const items = Array.from({ length: running }, (_, i) => ({
    ...runningRun(),
    run_ref: `e2e-status-bar-run-${i + 1}`,
  }))
  const answer = (body: unknown) => (route: Route) =>
    route.request().method() === 'GET'
      ? route.fulfill({ json: body })
      : route.fallback()
  await page.unrouteAll({ behavior: 'ignoreErrors' })
  await page.route(
    (url) => url.pathname === '/v1/m/sessions/runs',
    answer({ items, has_more: false }),
  )
  await page.route(
    (url) => url.pathname === '/v1/m/sessions/live',
    answer({ items: [], has_more: false }),
  )
}

/** Keep the demo estate's own sessions and add `running` running ones, for pictures
 * of the real pages with Stop all on screen. */
async function withRunning(page: Page, running: number) {
  await page.unrouteAll({ behavior: 'ignoreErrors' })
  await page.route(
    (url) => url.pathname === '/v1/m/sessions/runs',
    async (route) => {
      if (route.request().method() !== 'GET') return route.fallback()
      const response = await route.fetch()
      const json = (await response.json()) as { items: unknown[] }
      const added = Array.from({ length: running }, (_, i) => ({
        ...runningRun(),
        run_ref: `e2e-status-bar-run-${i + 1}`,
      }))
      return route.fulfill({
        response,
        json: { ...json, items: [...added, ...json.items] },
      })
    },
  )
}

async function shoot(page: Page, name: string) {
  if (!captureDir) return
  mkdirSync(captureDir, { recursive: true })
  await page.mouse.move(0, 0)
  await page.screenshot({ path: `${captureDir}/${name}.png` })
}

const statusBar = (page: Page) =>
  page.getByRole('contentinfo', { name: 'Status' })

/** One fact, one number, one word: the Sessions place's count is the status bar's open count. */
async function expectOneOpenCount(page: Page) {
  const open = statusBar(page).getByText(/^\d+ sessions? open$/)
  await expect(open).toBeVisible()
  await expect(statusBar(page)).not.toContainText('running')
  const count = (await open.textContent())?.match(/\d+/)?.[0] ?? ''
  await expect(
    page
      .locator('header[data-slot="topbar"] a[data-place="sessions"]')
      .locator('[data-slot="count-badge"]'),
  ).toHaveText(count)
}

test('@shell Stop all: absent with no running session, present with one, through the confirmation', async ({
  page,
}) => {
  await page.setViewportSize({ width: 1280, height: 800 })
  await prefer(page, 'dark')
  await sessions(page, 0)
  await signIn(page)
  await page.goto('/sessions')
  const bar = statusBar(page)
  await expect(bar).toBeVisible()
  await expect(bar.getByText('Engine ready')).toBeVisible()
  await expect(bar.getByTestId('deployment-identity')).toBeVisible()
  await expect(bar.getByRole('button', { name: 'Stop all' })).toHaveCount(0)
  // Nor anywhere in the top bar: it left the permanent chrome.
  await expect(
    page.locator('header[data-slot="topbar"]').getByRole('button', {
      name: 'Stop all',
    }),
  ).toHaveCount(0)

  await sessions(page, 1)
  await page.reload()
  // Open, not running: the count includes idle sessions, which are not working.
  await expect(bar.getByText('1 session open')).toBeVisible()
  await expect(bar).not.toContainText('running')
  await expectOneOpenCount(page)
  const stop = bar.getByRole('button', { name: 'Stop all' })
  await expect(stop).toBeVisible()

  let engaged: unknown = null
  await page.route(
    (url) => url.pathname === '/v1/m/governance/killswitch',
    (route) => {
      if (route.request().method() !== 'POST') return route.fallback()
      engaged = route.request().postDataJSON()
      return route.fulfill({
        status: 201,
        json: { id: 'ks-e2e', scope_kind: 'estate', status: 'active' },
      })
    },
  )
  await stop.click()
  const sheet = page.getByRole('dialog', { name: 'Stop all agents?' })
  await expect(sheet).toBeVisible()
  await expect(sheet).toContainText('1 open session stops.')
  expect(engaged).toBeNull()
  await shoot(page, 'stop-all-confirm-dark-1280')
  await sheet.getByRole('button', { name: 'Stop all agents' }).click()
  await expect(sheet).toBeHidden()
  expect(engaged).toMatchObject({ scope_kind: 'estate' })
})

for (const theme of THEMES)
  for (const [width, height] of [
    [1280, 800],
    [1920, 1080],
  ] as const)
    test(`@shell capture the shell — ${theme} ${width}`, async ({ page }) => {
      test.setTimeout(120_000)
      await page.setViewportSize({ width, height })
      await prefer(page, theme)
      await withRunning(page, 1)
      await signIn(page)
      for (const [name, path] of [
        ['sessions', '/sessions'],
        ['now', '/'],
      ] as const) {
        await page.goto(path)
        // On Sessions, one of the estate's own sessions, as the reference shows one.
        if (name === 'sessions') {
          const row = page.getByText('Profile slow analytics query').first()
          await row.waitFor({ timeout: 15_000 })
          await row.click()
          await expect(page.getByText('No session activity yet.')).toHaveCount(
            0,
          )
        }
        await expect(statusBar(page)).toBeVisible()
        await expect(
          statusBar(page).getByRole('button', { name: 'Stop all' }),
        ).toBeVisible()
        await expectOneOpenCount(page)
        // The console keeps a live feed open, so the network never idles: wait for
        // the page's own loading states to end, then let the last paint settle.
        await expect(page.locator('[aria-busy="true"]')).toHaveCount(0, {
          timeout: 30_000,
        })
        await page.waitForTimeout(1_500)
        await shoot(page, `shell-${name}-${theme}-${width}`)
      }
    })
