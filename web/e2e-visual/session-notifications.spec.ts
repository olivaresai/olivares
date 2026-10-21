// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { expect, test, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { fixtureFor } from './fixtures'

test.use({ permissions: ['notifications'] })

// The real console, the real browser Notification API and the rail's real background polling,
// with labelled rail HTTP fixtures. Headless Chromium never hides a tab when another one is
// raised, so only document.hidden/visibilityState are driven by the test (window.__setHidden).
async function prepare(page: Page, origin: string, readState: () => string) {
  await page.context().grantPermissions(['notifications'], { origin })
  await page.route('**/*', (route) => {
    const url = new URL(route.request().url())
    if (url.origin !== origin) return route.abort()
    if (!url.pathname.startsWith('/v1/')) return route.continue()
    if (url.pathname === '/v1/m/sessions/runs')
      return route.fulfill({
        json: {
          items: [
            {
              run_ref: 'notification-fixture',
              name: 'Review changes (fixture)',
              state: readState(),
              transport: 'stream-json',
              permission_mode: 'default',
              isolation: 'native',
              provider_driver: 'claude',
              last_event_seq: 0,
              created_at: '2026-10-14T06:00:00Z',
              started_at: '2026-10-14T06:00:00Z',
            },
          ],
          has_more: false,
        },
      })
    if (url.pathname === '/v1/m/sessions/live')
      return route.fulfill({ json: { items: [], has_more: false } })
    if (url.pathname.endsWith('/stream'))
      return route.fulfill({
        contentType: 'text/event-stream',
        body: ': connected\n\n',
      })
    if (url.pathname === '/v1/m/consoleviews/favorites')
      return route.fulfill({ json: { stored: true, favorites: [] } })
    if (url.pathname === '/v1/m/consoleviews/ui-state')
      return route.fulfill({ json: { stored: true, sidebar: 'full' } })
    const fixture = fixtureFor(url.pathname)
    return fixture !== null && fixture !== undefined
      ? route.fulfill({ json: fixture })
      : route.fulfill({
          status: 404,
          json: {
            error: { code: 'not_found', message: 'No fixture for this read' },
          },
        })
  })
  await page.addInitScript(() => {
    localStorage.setItem('olivares.lang', 'en')
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
    let hiddenNow = false
    Object.defineProperty(document, 'hidden', {
      configurable: true,
      get: () => hiddenNow,
    })
    Object.defineProperty(document, 'visibilityState', {
      configurable: true,
      get: () => (hiddenNow ? 'hidden' : 'visible'),
    })
    Object.defineProperty(window, '__setHidden', {
      value: (value: boolean) => {
        hiddenNow = value
        document.dispatchEvent(new Event('visibilitychange'))
      },
    })
    const receipts: { title: string; body: string }[] = []
    Object.defineProperty(window, '__sessionNotificationReceipts', {
      value: receipts,
    })
    const NativeNotification = window.Notification
    window.Notification = class extends NativeNotification {
      static get permission() {
        return NativeNotification.permission
      }
      static requestPermission() {
        return NativeNotification.requestPermission()
      }
      constructor(title: string, options?: NotificationOptions) {
        super(title, options)
        receipts.push({ title, body: options?.body ?? '' })
      }
    }
  })
}

const receipts = (page: Page) =>
  page.evaluate(
    () =>
      Reflect.get(window, '__sessionNotificationReceipts') as {
        title: string
        body: string
      }[],
  )

test('Notify me is keyboard accessible and a hidden tab receives one notification', async ({
  page,
  baseURL,
}, info) => {
  let state = 'running'
  await prepare(page, baseURL!, () => state)
  await page.goto('/settings')
  const permission = await page.evaluate(() => Notification.permission)
  test.skip(
    permission !== 'granted',
    `Native notification delivery is unavailable: this browser reports ${permission} after the explicit permission grant. This is NOT RUN, not a delivery pass.`,
  )
  const button = page.getByRole('button', { name: 'Notify me', exact: true })
  await expect(button).toBeVisible()
  await expect(button).toHaveAttribute('aria-pressed', 'false')
  await expect(button).toHaveAccessibleDescription(/background/)
  await button.focus()
  await button.press('Enter')
  await expect(button).toHaveAttribute('aria-pressed', 'true')
  expect(await button.ariaSnapshot()).toContain('button "Notify me" [pressed]')
  expect(
    (await new AxeBuilder({ page }).include('button[aria-pressed]').analyze())
      .violations,
  ).toEqual([])
  const title = await page.title()
  for (const width of [1280, 390]) {
    for (const theme of ['light', 'dark']) {
      await page.setViewportSize({ width, height: 900 })
      await page.evaluate(
        (selected) =>
          document.documentElement.classList.toggle(
            'dark',
            selected === 'dark',
          ),
        theme,
      )
      await button.scrollIntoViewIfNeeded()
      await expect(button).toBeVisible()
      await page.screenshot({
        path: info.outputPath(`notify-${width}-${theme}.png`),
      })
    }
  }
  {
    await page.evaluate(() =>
      (Reflect.get(window, '__setHidden') as (v: boolean) => void)(true),
    )
    await expect.poll(() => page.evaluate(() => document.hidden)).toBe(true)
    state = 'waiting_approval'
    await expect
      .poll(() => receipts(page), { timeout: 20_000 })
      .toEqual([
        { title: 'Review changes (fixture)', body: 'This session needs you.' },
      ])
    await expect(page).toHaveTitle(`(1) ${title}`)
    // Observe another actual background poll, rather than manually invalidating the cache.
    await page.waitForResponse(
      (response) => new URL(response.url()).pathname === '/v1/m/sessions/runs',
    )
    expect(await receipts(page)).toHaveLength(1)
  }
})

test('a visible tab showing Settings delivers no desktop notification', async ({
  page,
  baseURL,
}) => {
  let state = 'running'
  await prepare(page, baseURL!, () => state)
  await page.goto('/settings')
  const permission = await page.evaluate(() => Notification.permission)
  test.skip(
    permission !== 'granted',
    `Native notification delivery is unavailable: this browser reports ${permission} after the explicit permission grant. This is NOT RUN, not a delivery pass.`,
  )
  await page.getByRole('button', { name: 'Notify me', exact: true }).click()
  expect(await page.evaluate(() => document.hidden)).toBe(false)
  const title = await page.title()
  state = 'waiting_approval'
  await expect(page).toHaveTitle(`(1) ${title}`, { timeout: 20_000 })
  expect(await receipts(page)).toHaveLength(0)
})
