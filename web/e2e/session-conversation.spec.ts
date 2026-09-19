// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE SESSION SURFACE AS A CONVERSATION, against a live seeded engine whose
// child is the fixture driver (no provider account, no model turn).
import { expect, test, type Page } from '@playwright/test'

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
    await page.goto('/')
    await composerReady(page)
    const scope = page.getByTestId('work-scope-line')
    await expect(scope).toBeVisible()
    const text = (await scope.innerText()).replace(/\s+/g, ' ')
    expect(text).not.toMatch(
      /Organization\s+[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}/i,
    )
    expect(text).toMatch(/Demo Estate|This node|No workspace/i)
  })

  test('starts a session from the composer, sends a sentence, sees a conversation', async ({
    page,
  }) => {
    test.skip(
      !fixtureOn,
      'CONVERSATION_FIXTURE=1 required — the fixture driver is not wired',
    )
    await prefer(page, 'dark', 'en')
    await signIn(page)
    await page.setViewportSize({ width: 1440, height: 900 })
    await page.goto('/sessions')
    await composerReady(page)

    const composer = page.getByTestId('work-composer')
    if ((await composer.getAttribute('data-attached')) !== 'true') {
      await page.getByTestId('launcher-profile').click()
      await page.getByRole('option').first().click()
      await page.getByTestId('launcher-input').fill('conversation-live')
      await page.getByTestId('launcher-start').click()
    }
    await expect(composer).toHaveAttribute('data-attached', 'true', {
      timeout: 30_000,
    })
    await expect(page.getByTestId('composer-session-state')).toBeVisible()
    await expect(page.getByTestId('composer-send')).toBeVisible()

    const turn = page.getByTestId('launcher-input')
    await turn.fill('Look up the readme')
    await page.getByTestId('composer-send').click()

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

  test('at 390 px the pane switcher still reaches the composer', async ({
    page,
  }) => {
    await prefer(page, 'dark', 'en')
    await signIn(page)
    await page.setViewportSize({ width: 390, height: 844 })
    await page.goto('/sessions')
    await page.getByTestId('pane-button-narrative').click()
    await composerReady(page)
    await expect(page.getByTestId('work-composer')).toBeVisible()
  })
})
