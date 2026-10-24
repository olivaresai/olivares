// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// RESIZABLE PANES, in a real browser against a live `--seed-demo` engine, nothing intercepted:
// a handle drags and moves from the keyboard, the width is kept ON THE SERVER (the ui-state
// row), a browser that has never seen this person's choices gets it back, and at 390 px the
// mobile layout has no handles. jsdom has no layout and no second browser, so the vitest
// suites cannot show any of this.
//
// It carries the same DEMO_TENANT guard as session-work-surface.spec.ts, for the same reason:
// it signs in as the demo operator, so it runs through scripts/web-e2e-demo.sh. Set
// PANE_SHOTS to a directory to also keep the captures.
import { expect, test, type Page } from '@playwright/test'
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'

const demoTenant = process.env.DEMO_TENANT ?? ''
const EMAIL = 'demo@olivares.local'
const PASSWORD = 'olivares-demo-estate'
const LIST = 'Resize the session list'
const CONTEXT = 'Resize the context pane'

async function signIn(page: Page) {
  await page.goto('/login')
  await page.locator('input[type=email]').first().fill(EMAIL)
  await page.locator('input[type=password]').first().fill(PASSWORD)
  await page.locator('button[type=submit]').first().click()
  await page.waitForURL((url) => !url.pathname.startsWith('/login'), {
    timeout: 20_000,
  })
}

async function prefer(page: Page, theme: 'dark' | 'light') {
  await page.context().addInitScript((t) => {
    window.localStorage.setItem('olivares.theme', t)
    window.localStorage.setItem('olivares.lang', 'en')
  }, theme)
}

/** The widths the engine holds for this person. */
async function stored(page: Page): Promise<Record<string, number>> {
  const res = await page.request.get('/v1/m/consoleviews/ui-state', {
    headers: { 'X-Olivares-Tenant': demoTenant },
  })
  expect(res.status(), await res.text()).toBe(200)
  return ((await res.json()).pane_widths ?? {}) as Record<string, number>
}

async function shot(page: Page, name: string) {
  const dir = process.env.PANE_SHOTS
  if (!dir) return
  mkdirSync(dir, { recursive: true })
  await page.screenshot({ path: join(dir, `${name}.png`) })
}

const valueOf = async (page: Page, name: string) =>
  Number(
    await page.getByRole('separator', { name }).getAttribute('aria-valuenow'),
  )

for (const theme of ['light', 'dark'] as const) {
  test.describe(`panes (${theme})`, () => {
    test.setTimeout(120_000)
    test.beforeEach(() => {
      test.skip(
        !demoTenant,
        'DEMO_TENANT not set — run this through scripts/web-e2e-demo.sh',
      )
    })

    test('drag, keys and reset are kept on the server and follow the person to a fresh browser', async ({
      page,
    }) => {
      await prefer(page, theme)
      await signIn(page)
      // The context pane sits beside the thread, with a handle, only where the thread
      // keeps its 760 px: from 1441 px.
      await page.setViewportSize({ width: 1600, height: 900 })
      await page.goto('/sessions')
      const row = page.getByTestId('rail-row').first()
      await expect(row).toBeVisible()
      const address = await row.getAttribute('data-address')
      await page.goto(
        `/sessions?session=${encodeURIComponent(address as string)}&pane=context`,
      )
      const list = page.getByRole('separator', { name: LIST })
      const context = page.getByRole('separator', { name: CONTEXT })
      await expect(list).toBeVisible()
      await expect(context).toBeVisible()
      expect(await stored(page)).toEqual({})
      const listDefault = await valueOf(page, LIST)
      const contextDefault = await valueOf(page, CONTEXT)
      await shot(page, `1600-${theme}-default`)

      // DRAG the list edge 80 px to the right.
      const box = (await list.boundingBox())!
      await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2)
      await page.mouse.down()
      await page.mouse.move(box.x + box.width / 2 + 80, box.y + 200, {
        steps: 8,
      })
      await page.mouse.up()
      await expect
        .poll(() => valueOf(page, LIST))
        .toBeGreaterThan(listDefault + 40)
      const dragged = await valueOf(page, LIST)

      // KEYS: the context pane's handle is its left edge, so ArrowLeft widens it.
      await context.focus()
      await page.keyboard.press('Shift+ArrowLeft')
      const widened = contextDefault + 64
      await expect.poll(() => valueOf(page, CONTEXT)).toBe(widened)
      await shot(page, `1600-${theme}-resized`)

      // The engine holds both, after the debounce.
      await expect
        .poll(async () => stored(page))
        .toEqual({ 'sessions-list': dragged, 'sessions-context': widened })

      // A FRESH browser (no local state but the sign-in) gets them back.
      const other = await page
        .context()
        .browser()!
        .newContext({
          baseURL: new URL(page.url()).origin,
          colorScheme: theme,
          viewport: { width: 1600, height: 900 },
          storageState: await page.context().storageState(),
        })
      const fresh = await other.newPage()
      await fresh.addInitScript(() => window.localStorage.clear())
      await fresh.goto(
        `/sessions?session=${encodeURIComponent(address as string)}&pane=context`,
      )
      await expect(fresh.getByRole('separator', { name: LIST })).toBeVisible()
      await expect.poll(() => valueOf(fresh, LIST)).toBe(dragged)
      await expect.poll(() => valueOf(fresh, CONTEXT)).toBe(widened)
      await other.close()

      // RESET: double-click on one, Enter on the other; the server forgets both.
      await list.dblclick()
      await context.focus()
      await page.keyboard.press('Enter')
      await expect.poll(() => valueOf(page, LIST)).toBe(listDefault)
      await expect.poll(() => valueOf(page, CONTEXT)).toBe(contextDefault)
      await expect.poll(async () => stored(page)).toEqual({})
    })

    test('at 390 px the layout stays the mobile one: no handles', async ({
      page,
    }) => {
      await prefer(page, theme)
      await signIn(page)
      await page.setViewportSize({ width: 390, height: 844 })
      await page.goto('/sessions')
      await expect(page.getByTestId('rail-row').first()).toBeVisible()
      await expect(page.getByRole('separator', { name: LIST })).toBeHidden()
      await expect(page.getByRole('separator', { name: CONTEXT })).toBeHidden()
      expect(
        await page.evaluate(
          () => document.documentElement.scrollWidth <= window.innerWidth,
        ),
        'the page does not scroll sideways',
      ).toBe(true)
      await shot(page, `390-${theme}`)
    })
  })
}
