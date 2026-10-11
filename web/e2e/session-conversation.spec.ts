// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE SESSION SURFACE AS A CONVERSATION, against a live seeded engine whose
// child is the fixture driver (no provider account, no model turn).
import { expect, test, type Page } from '@playwright/test'
import { openAdvancedSession } from './session-launch-form'
import { JOURNEYS, stepHref } from '../src/features/navigation/journeys'

const [SESSIONS] = JOURNEYS.sessions

const demoTenant = process.env.DEMO_TENANT ?? ''
const fixtureOn = process.env.CONVERSATION_FIXTURE === '1'
const EMAIL = 'demo@olivares.local'
const PASSWORD = 'olivares-demo-estate'

async function prefer(page: Page, theme: 'dark' | 'light', lang: string) {
  await page.context().addInitScript(
    ([t, l]) => {
      try {
        window.localStorage.setItem('olivares.theme', t)
        window.localStorage.setItem('olivares.lang', l)
      } catch {
        /* private mode */
      }
    },
    [theme, lang],
  )
}

async function signIn(page: Page) {
  await page.goto('/login')
  await expect(page.locator('input[type=password]')).toHaveCount(1)
  await page.locator('input[type=email]').first().fill(EMAIL)
  await page.locator('input[type=password]').first().fill(PASSWORD)
  await page.locator('button[type=submit]').first().click()
  await page.waitForURL((url) => !url.pathname.startsWith('/login'), {
    timeout: 20_000,
  })
}

async function composerReady(page: Page) {
  await page.getByTestId('work-composer').waitFor()
  await page
    .locator(
      '[data-testid="launcher-input"], [data-testid="launcher-add-provider"], [data-testid="launcher-blocked"], [data-testid="composer-send"]',
    )
    .first()
    .waitFor()
}

test.describe('the session surface is a conversation', () => {
  test.setTimeout(120_000)

  test.beforeEach(() => {
    test.skip(
      !demoTenant,
      'DEMO_TENANT not set — run through scripts/e2e-session-conversation.sh',
    )
  })

  test('the scope line names the organization, never a raw uuid', async ({
    page,
  }) => {
    await prefer(page, 'dark', 'en')
    await signIn(page)
    expect(
      process.env.E2E_CAPTURE_RUN_REF,
      'the fixture harness must supply a running session reference',
    ).toBeTruthy()
    await page.goto(
      `/sessions?session=run:${process.env.E2E_CAPTURE_RUN_REF}&pane=narrative`,
    )
    await composerReady(page)
    await page.getByTestId('composer-advanced').click()
    const scope = page.getByTestId('work-scope-line')
    await expect(scope).toBeVisible()
    const text = (await scope.innerText()).replace(/\s+/g, ' ')
    expect(text).not.toMatch(
      /Organization\s+[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}/i,
    )
    expect(text).toMatch(/Demo Estate|This node|No workspace/i)
  })

  test('starts a session from New session, sends a sentence with Enter, sees a conversation', async ({
    page,
  }) => {
    test.skip(
      !fixtureOn,
      'CONVERSATION_FIXTURE=1 required — the fixture driver is not wired',
    )
    await prefer(page, 'dark', 'en')
    await signIn(page)
    await page.setViewportSize({ width: 1440, height: 900 })
    const form = await openAdvancedSession(page)
    await form.getByRole('button', { name: 'Start', exact: true }).click()
    const composer = page.getByTestId('work-composer')
    await expect(composer).toHaveAttribute('data-attached', 'true', {
      timeout: 30_000,
    })
    await expect(page.getByTestId('launcher-input')).toBeVisible()
    await expect(page.getByTestId('composer-send')).toBeVisible()

    const turn = page.getByTestId('launcher-input')
    await turn.fill('Look up the readme')
    await turn.press('Enter')

    const conversation = page.getByTestId('session-conversation')
    await expect(conversation).toBeVisible()
    await expect(
      conversation.locator('[data-kind="assistant"]').first(),
    ).toContainText(/look that up/i, { timeout: 15_000 })
    await expect(
      conversation.locator('[data-kind="tool"]').first(),
    ).toContainText(/Read/i)
    await expect(conversation).not.toContainText(/modelUsage/)
    await expect(conversation).not.toContainText(/rate_limit_event/)
  })

  test('at 390 px a list row opens the thread and composer', async ({
    page,
  }) => {
    await prefer(page, 'dark', 'en')
    await signIn(page)
    await page.setViewportSize({ width: 390, height: 844 })
    await page.goto(stepHref(SESSIONS))
    const name = process.env.E2E_CLI_RUN_NAME
    expect(
      name,
      'the fixture harness must supply a running session name',
    ).toBeTruthy()
    const title = page
      .getByTestId('rail-row-name')
      .and(page.getByText(name!, { exact: true }))
    await page.getByTestId('rail-row').filter({ has: title }).click()
    await composerReady(page)
    await expect(page.getByTestId('work-composer')).toBeVisible()
  })
})
