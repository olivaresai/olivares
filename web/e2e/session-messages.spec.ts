// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE MESSAGE NAVIGATOR (#2129), against a live seeded engine whose child is the fixture
// driver, over a long conversation in the tool's own transcript: open the session, reach
// the message just sent from the rail, search a phrase far back, go to a time, by mouse and
// by keyboard, in both themes. Run through scripts/e2e-session-conversation.sh with
// E2E_MESSAGES_FIXTURE=1 E2E_SPECS=e2e/session-messages.spec.ts.
import AxeBuilder from '@axe-core/playwright'
import { expect, test, type Page } from '@playwright/test'
import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { signInFixture } from './fixture-sign-in'

const demoTenant = process.env.DEMO_TENANT ?? ''
const runRef = process.env.E2E_CAPTURE_RUN_REF ?? ''
const folder = process.env.E2E_SESSION_FOLDER ?? ''
const SENT = 'Look up the readme'

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

async function openSession(page: Page) {
  await signInFixture(
    page,
    { email: 'demo@olivares.local', password: 'olivares-demo-estate' },
    20_000,
  )
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto(`/sessions?session=run:${runRef}&pane=narrative`)
  await page.getByTestId('launcher-input').waitFor()
}

test.describe('the message navigator on a long session', () => {
  test.setTimeout(120_000)

  test.beforeEach(() => {
    test.skip(
      !demoTenant || !runRef,
      'run through scripts/e2e-session-conversation.sh with E2E_MESSAGES_FIXTURE=1',
    )
  })

  test('reaches the message just sent, a phrase far back and a time', async ({
    page,
  }) => {
    await prefer(page, 'dark')
    await openSession(page)

    // The conversation restores the seeded history. The new sentence appears once,
    // only after it is sent, and joins the engine's message index.
    const turn = page.getByTestId('launcher-input')
    await turn.fill(SENT)
    await turn.press('Enter')
    const conversation = page.getByTestId('session-conversation')
    const sent = conversation
      .locator('[data-kind="operator"]')
      .filter({ hasText: SENT })
    await expect(sent).toHaveCount(1, { timeout: 30_000 })

    // The rail: marks for what was sent, and a click reaches its row.
    const rail = page.getByTestId('message-rail')
    await expect(rail).toBeVisible()
    const marks = rail.getByTestId('message-mark')
    const count = await marks.count()
    expect(count).toBeGreaterThan(1)
    expect(count).toBeLessThanOrEqual(24)
    await expect(marks.last()).toHaveAccessibleName(new RegExp(SENT))
    await marks.last().hover()
    await expect(page.getByTestId('message-preview')).toContainText(SENT)
    await marks.last().click()
    await expect(sent.locator('xpath=ancestor::li[1]')).toHaveClass(/ring-2/)
    await expect(sent).toBeInViewport()
    await expect(
      page.getByRole('status').filter({ hasText: /Jumped to message/ }),
    ).toHaveCount(1)

    // The panel searches the whole session on the engine, then reaches the restored
    // row (including a row outside the virtual list's current window).
    const bar = page.getByTestId('message-bar')
    await bar.getByRole('button', { name: /^Messages/ }).click()
    const panel = page.getByTestId('message-panel')
    await expect(panel).toBeVisible()
    await expect(panel.getByTestId('message-result').first()).toContainText(
      SENT,
    )
    await panel.getByLabel("Search this session's messages").fill('needle-137')
    await expect(panel.getByTestId('message-result')).toHaveCount(1)
    await expect(panel.getByTestId('message-result')).toContainText(
      'migrate the sqlite schema',
    )
    await panel.getByTestId('message-result').click()
    const older = conversation
      .locator('[data-kind="operator"]')
      .filter({ hasText: 'then the needle-137 index' })
    await expect(older.locator('xpath=ancestor::li[1]')).toHaveClass(/ring-2/)
    await expect(older).toBeInViewport()

    // A time: the first message at or after it, in the person's own zone.
    const local = await page.evaluate(() => {
      const d = new Date('2026-10-14T13:00:00Z')
      const p = (n: number) => String(n).padStart(2, '0')
      return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`
    })
    await panel.getByLabel('Go to date and time').fill(local)
    await panel.getByRole('button', { name: 'Go', exact: true }).click()
    const timed = conversation
      .locator('[data-kind="operator"]')
      .filter({ hasText: /^request 300$/ })
    await expect(timed.locator('xpath=ancestor::li[1]')).toHaveClass(/ring-2/)
    await expect(timed).toBeInViewport()
  })

  for (const theme of ['dark', 'light'] as const) {
    test(`is operable by keyboard alone and passes axe in the ${theme} theme`, async ({
      page,
    }) => {
      await prefer(page, theme)
      await openSession(page)
      const marks = page.getByTestId('message-rail').getByTestId('message-mark')
      await expect(marks.first()).toBeVisible({ timeout: 30_000 })

      // Keyboard: a mark takes focus, previews, and Enter reaches the message.
      await marks.first().focus()
      await expect(page.getByTestId('message-preview')).toBeVisible()
      // Drawn, not only in the page: an overflow on the rail cut the preview to its own
      // 16 px (toBeVisible does not see a clip).
      const clipped = await page
        .getByTestId('message-preview')
        .evaluate((el) => {
          const rail = el.closest('nav')!
          const style = getComputedStyle(rail)
          if (style.overflowX === 'visible' && style.overflowY === 'visible')
            return false
          const a = el.getBoundingClientRect()
          const b = rail.getBoundingClientRect()
          return a.left < b.left || a.right > b.right
        })
      expect(clipped, 'the preview is cut by the rail').toBe(false)
      await page.keyboard.press('Enter')
      await expect(
        page.getByRole('status').filter({ hasText: /message \d+/i }),
      ).toHaveCount(1)

      // The panel opens from the keyboard and the search is typed into.
      const toggle = page
        .getByTestId('message-bar')
        .getByRole('button', { name: /^Messages/ })
      await toggle.focus()
      await page.keyboard.press('Enter')
      await expect(toggle).toHaveAttribute('aria-expanded', 'true')
      await page.getByLabel("Search this session's messages").focus()
      await page.keyboard.type('request 5')
      await expect(page.getByTestId('message-result').first()).toBeVisible()

      const found = await new AxeBuilder({ page })
        .include('[data-testid="message-bar"]')
        .include('[data-testid="message-rail"]')
        .analyze()
      expect(found.violations).toEqual([])
      expect(
        await page.evaluate(
          () => document.documentElement.scrollWidth <= window.innerWidth,
        ),
      ).toBe(true)
    })
  }

  // A long message sits in the long conversation's virtual list, which measures every row
  // it shows. The replies around it and a change of width (the navigation collapsing)
  // measure them again, and the view must survive it: a fold of such a message crashed it
  // (React #185, the list measuring without end), so the guard stays for the next fold.
  test('keeps a long session whole through a long message, its replies and a change of width', async ({
    page,
  }) => {
    await prefer(page, 'light')
    await openSession(page)
    await page.setViewportSize({ width: 1280, height: 900 })
    if (folder) writeFileSync(join(folder, '.fixture-echo-user'), '')
    const turn = page.getByTestId('launcher-input')
    const conversation = page.getByTestId('session-conversation')
    // Last in the file and with words of its own: the tests before it count what they send.
    const replies = conversation.locator('[data-kind="assistant"]', {
      hasText: 'Two things matter',
    })
    const before = await replies.count()
    await turn.fill('Check the build log')
    await turn.press('Enter')
    await expect(replies).toHaveCount(before + 1, { timeout: 30_000 })
    await turn.fill(
      Array.from(
        { length: 20 },
        (_, i) => `Brief line ${i + 1}: check the step.`,
      ).join('\n'),
    )
    await turn.press('Enter')
    await expect(replies).toHaveCount(before + 2, { timeout: 30_000 })
    const message = conversation.locator('[data-kind="operator"]').filter({
      hasText: 'Brief line 20: check the step.',
    })
    const latest = conversation.getByRole('button', { name: 'Jump to latest' })
    if (await latest.isVisible()) await latest.click()
    await expect(message).toBeVisible()
    const text = message.locator('[data-slot="user-message-text"]')
    const toggle = message.getByRole('button', {
      name: 'Show all',
      exact: true,
    })
    await expect(toggle).toHaveAttribute('aria-expanded', 'false')
    const folded = await text.evaluate(
      (el) => el.getBoundingClientRect().height,
    )
    expect(folded).toBeLessThan(230)
    await toggle.click()
    await expect(
      message.getByRole('button', { name: 'Show less' }),
    ).toHaveAttribute('aria-expanded', 'true')
    await expect
      .poll(() => text.evaluate((el) => el.getBoundingClientRect().height))
      .toBeGreaterThan(folded * 2)
    await message.getByRole('button', { name: 'Show less' }).click()
    await expect
      .poll(() => text.evaluate((el) => el.getBoundingClientRect().height))
      .toBe(folded)
    await expect(message).toBeInViewport()
    // The navigation's fold changes the column's width (Sessions starts folded: the
    // button then reads Expand).
    await page
      .getByRole('button', { name: /^(Collapse|Expand) navigation$/ })
      .click()
    await page.waitForTimeout(1000)
    await expect(page.getByText('This view crashed')).toHaveCount(0)
    await expect(conversation.getByRole('log')).toBeVisible()
    await expect(message).toBeInViewport()
    await page.screenshot({
      path:
        process.env.E2E_FOLD_CAPTURE ??
        'test-results/folded-message-1280-light.png',
    })
    await page.setViewportSize({ width: 390, height: 844 })
    await page.evaluate(() => document.documentElement.classList.add('dark'))
    await expect(
      message.getByRole('button', { name: 'Show all', exact: true }),
    ).toBeVisible()
    const found = await new AxeBuilder({ page })
      .include('[data-kind="operator"]')
      .analyze()
    expect(found.violations).toEqual([])
    await page.screenshot({
      path:
        process.env.E2E_FOLD_MOBILE_CAPTURE ??
        'test-results/folded-message-390-dark.png',
    })
  })
})
