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
import { signInFixture } from './fixture-sign-in'

const demoTenant = process.env.DEMO_TENANT ?? ''
const runRef = process.env.E2E_CAPTURE_RUN_REF ?? ''
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

    // The conversation holds only what this view shows: the sentence sent now. The
    // session's 601 messages are in the engine's index, never loaded whole.
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
    await expect(
      page.getByRole('status').filter({ hasText: /Jumped to message/ }),
    ).toHaveCount(1)

    // The panel: the whole session, searched on the engine; a message older than the
    // thread on screen is shown whole.
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
    const far = page.getByTestId('message-far')
    await expect(far).toContainText('then the needle-137 index')

    // A time: the first message at or after it, in the person's own zone.
    const local = await page.evaluate(() => {
      const d = new Date('2026-10-14T13:00:00Z')
      const p = (n: number) => String(n).padStart(2, '0')
      return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`
    })
    await panel.getByLabel('Go to date and time').fill(local)
    await panel.getByRole('button', { name: 'Go', exact: true }).click()
    await expect(far).toContainText('request 300')
    await expect(far).not.toContainText('needle-137')
    await far.getByRole('button', { name: 'Close' }).click()
    await expect(far).toHaveCount(0)
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
})
