// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE ESTATE SHEET AND THE SESSION PAGE SHOW THE REAL CONVERSATION (#2549), against a live
// seeded engine whose session child is the zero-network fixture driver. A turn sent while
// the sheet is open streams into it; the full page shows the same conversation; Stop asks
// first in both; the sheet has no axe violation and no text overlap at 1440 and 1024.
//
// Run: E2E_SPECS=e2e/session-sheet.spec.ts scripts/e2e-session-conversation.sh
import AxeBuilder from '@axe-core/playwright'
import { expect, test, type Page } from '@playwright/test'
import { overlapScan } from '../e2e-visual/console-qa/overlap'
import { sessionToken } from './live-session'

const demoTenant = process.env.DEMO_TENANT ?? ''
const runRef = process.env.E2E_CAPTURE_RUN_REF ?? ''
const runName = process.env.E2E_CLI_RUN_NAME ?? ''
const EMAIL = 'demo@olivares.local'
const PASSWORD = 'olivares-demo-estate'
const REPLY = "I'll look that up."
const REPLY_AFTER_TOOL = 'Two things matter'

async function prefer(page: Page, theme: 'dark' | 'light') {
  await page.context().addInitScript((t) => {
    try {
      window.localStorage.setItem('olivares.theme', t)
      window.localStorage.setItem('olivares.lang', 'en')
    } catch {
      /* private mode */
    }
  }, theme)
}

async function signIn(page: Page) {
  await page.goto('/login')
  await page.locator('input[type=email]').first().fill(EMAIL)
  await page.locator('input[type=password]').first().fill(PASSWORD)
  await page.locator('button[type=submit]').first().click()
  await page.waitForURL((url) => !url.pathname.startsWith('/login'), {
    timeout: 20_000,
  })
}

/** One turn to the fixture session, as the composer sends it to a stream-json tool. */
async function sendTurn(page: Page, text: string) {
  const token = await sessionToken(page)
  const response = await page.request.post(
    `/v1/m/sessions/runs/${runRef}/input`,
    {
      headers: {
        Authorization: `Bearer ${token}`,
        'X-Olivares-Tenant': demoTenant,
      },
      data: {
        line: JSON.stringify({
          type: 'user',
          message: { role: 'user', content: text },
        }),
      },
    },
  )
  expect(response.status(), await response.text()).toBeLessThan(300)
}

const sheet = (page: Page) => page.getByTestId('session-sheet')

/** The assistant rows a view shows: a turn sent now adds to them while the view is open. */
const replies = (page: Page, scope: string) =>
  page.locator(`${scope} [data-kind="assistant"]`).count()

test.describe('the Estate sheet and the session page show the real conversation', () => {
  test.setTimeout(120_000)

  test.beforeEach(() => {
    test.skip(
      !demoTenant || !runRef,
      'DEMO_TENANT or E2E_CAPTURE_RUN_REF not set — run through scripts/e2e-session-conversation.sh',
    )
  })

  test('a turn streams into the sheet over Estate, and the page shows the same conversation', async ({
    page,
  }) => {
    await page.setViewportSize({ width: 1440, height: 900 })
    await prefer(page, 'dark')
    await signIn(page)
    await page.goto('/')
    const card = page
      .getByTestId('now-live-card')
      .filter({ hasText: runName })
      .first()
    await card.click()
    await expect(sheet(page)).toBeVisible()
    // The estate stays in place under the sheet, and the address names the session.
    expect(new URL(page.url()).pathname).toBe('/')
    // The address is the session's own (live:<ref> once the engine observes the run,
    // run:<ref> before), and the sheet is that session, named by its run.
    expect(new URL(page.url()).searchParams.get('session')).toMatch(
      /^(live|run):/,
    )
    await expect(page.getByRole('dialog', { name: runName })).toBeVisible()
    await expect(page.getByTestId('now-live')).toBeAttached()

    const log = sheet(page).getByRole('log')
    await sendTurn(page, 'Look up the readme')
    await expect(log).toContainText(REPLY, { timeout: 30_000 })
    await expect(log).toContainText(REPLY_AFTER_TOOL)
    await expect(
      sheet(page).locator('[data-kind="tool"]').first(),
    ).toContainText('Read')

    // The facts sit on their own line under the name, and Stop stays inside the sheet.
    const head = sheet(page).locator('[data-slot="thread-header"]')
    const name = await head.getByRole('heading', { level: 2 }).boundingBox()
    const facts = await head.locator('[data-slot="thread-facts"]').boundingBox()
    const stop = await head
      .getByRole('button', { name: 'Stop', exact: true })
      .boundingBox()
    const box = await sheet(page).boundingBox()
    expect(name && facts && stop && box).toBeTruthy()
    expect(facts!.y).toBeGreaterThanOrEqual(name!.y + name!.height - 1)
    expect(stop!.x + stop!.width).toBeLessThanOrEqual(box!.x + box!.width)
    expect(Math.abs(stop!.y - name!.y)).toBeLessThan(name!.height)

    // Back closes the sheet and leaves Estate.
    await page.goBack()
    await expect(sheet(page)).toBeHidden()
    expect(new URL(page.url()).searchParams.get('session')).toBeNull()
    expect(new URL(page.url()).pathname).toBe('/')

    // Closed with its own button, Back does not bring it back.
    await card.click()
    await expect(sheet(page)).toBeVisible()
    await sheet(page).getByRole('button', { name: 'Close' }).click()
    await expect(sheet(page)).toBeHidden()
    expect(new URL(page.url()).searchParams.get('session')).toBeNull()
    await page.goBack()
    await expect(sheet(page)).toBeHidden()

    // The full page: the same conversation, at full height, live while it is open.
    await page.goto('/')
    await card.click()
    await sheet(page).getByRole('link', { name: 'Open the session' }).click()
    await page.waitForURL((url) => url.pathname === '/sessions')
    const conversation = '[data-testid="session-conversation"]'
    await expect(page.locator(conversation)).toContainText(REPLY_AFTER_TOOL, {
      timeout: 30_000,
    })
    const before = await replies(page, conversation)
    await sendTurn(page, 'And the changelog')
    await expect
      .poll(() => replies(page, conversation), { timeout: 30_000 })
      .toBeGreaterThan(before)
  })

  test('Stop asks first in the sheet, in the page header and in Full controls', async ({
    page,
  }) => {
    await page.setViewportSize({ width: 1440, height: 900 })
    await prefer(page, 'dark')
    await signIn(page)
    const asks = async () => {
      const dialog = page.getByRole('dialog', { name: 'Stop this session?' })
      await expect(dialog).toBeVisible()
      // Keep the fixture session running for the next view.
      await dialog.getByRole('button', { name: 'Cancel' }).click()
      await expect(dialog).toBeHidden()
    }

    await page.goto(`/?session=run:${runRef}`)
    await sheet(page).getByRole('button', { name: 'Stop', exact: true }).click()
    await asks()
    await page.keyboard.press('Escape')
    await expect(sheet(page)).toBeHidden()

    await page.goto(`/sessions?session=run:${runRef}&pane=narrative`)
    const header = page.locator('[data-slot="thread-header"]')
    await header.getByRole('button', { name: 'Stop', exact: true }).click()
    await asks()

    await page.getByTestId('narrative-menu').click()
    await page.getByTestId('narrative-open-detail').click()
    await page
      .getByRole('dialog')
      .getByRole('button', { name: 'Stop', exact: true })
      .click()
    await asks()
  })

  // Each view sends its own turn first, so a test run alone still has a conversation.
  for (const width of [1440, 1024])
    for (const theme of ['dark', 'light'] as const)
      for (const view of ['sheet', 'page'] as const)
        test(`the ${view} at ${width} in ${theme}: axe 0, overlap 0`, async ({
          page,
        }) => {
          await page.setViewportSize({ width, height: 900 })
          await prefer(page, theme)
          await signIn(page)
          await page.goto(
            view === 'sheet'
              ? `/?session=run:${runRef}`
              : `/sessions?session=run:${runRef}&pane=narrative`,
          )
          const scope =
            view === 'sheet'
              ? '[data-testid="session-sheet"]'
              : '[data-testid="session-narrative"]'
          await page.locator(`${scope} [role="log"]`).waitFor()
          await sendTurn(page, `Capture at ${width} in ${theme}`)
          await expect(page.locator(`${scope} [role="log"]`)).toContainText(
            REPLY,
            { timeout: 30_000 },
          )
          await page.screenshot({
            path: test.info().outputPath(`${view}-${width}-${theme}.png`),
          })
          const audit = await new AxeBuilder({ page }).include(scope).analyze()
          expect(
            audit.violations.map((v) => `${v.id}: ${v.nodes.length}`),
          ).toEqual([])
          expect(await page.evaluate(overlapScan)).toEqual([])
        })
})
