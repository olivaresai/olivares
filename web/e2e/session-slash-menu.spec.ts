// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE "/" MENU OF THE SESSION COMPOSER, on a live seeded engine whose child is the fixture
// driver (no provider account, no model turn). The driver answers Claude Code's
// `initialize` request from .fixture-commands.json in the session's folder and pushes
// system/commands_changed when that file changes, the way the real tool does for a skill
// added to a folder it watches.
import AxeBuilder from '@axe-core/playwright'
import { expect, test, type Page } from '@playwright/test'
import { writeFileSync } from 'node:fs'
import { join } from 'node:path'

const demoTenant = process.env.DEMO_TENANT ?? ''
const sessionFolder = process.env.E2E_SESSION_FOLDER ?? ''
const EMAIL = 'demo@olivares.local'
const PASSWORD = 'olivares-demo-estate'

const BASE = [
  {
    name: 'review-notes',
    description: 'Review my notes (user)',
    argumentHint: '',
  },
  {
    name: 'deploy-check',
    description: 'Check the project before a deploy (project)',
    argumentHint: '<env>',
  },
  {
    name: 'compact',
    description: 'Free up context by summarizing the conversation so far',
    argumentHint: '<optional custom summarization instructions>',
    builtin: true,
  },
]

function publish(commands: object[]) {
  writeFileSync(
    join(sessionFolder, '.fixture-commands.json'),
    JSON.stringify({
      commands,
      agents: [{ name: 'Explore', description: 'Fast read-only search agent' }],
    }),
  )
}

async function openSession(page: Page, theme: 'dark' | 'light') {
  await page.context().addInitScript((t) => {
    window.localStorage.setItem('olivares.theme', t)
    window.localStorage.setItem('olivares.lang', 'en')
  }, theme)
  await page.goto('/login')
  await page.locator('input[type=email]').first().fill(EMAIL)
  await page.locator('input[type=password]').first().fill(PASSWORD)
  await page.locator('button[type=submit]').first().click()
  await page.waitForURL((url) => !url.pathname.startsWith('/login'), {
    timeout: 20_000,
  })
  await page.goto(
    `/sessions?session=run:${process.env.E2E_CAPTURE_RUN_REF}&pane=narrative`,
  )
  await page.getByTestId('launcher-input').waitFor()
}

test.describe('the "/" menu lists what the session’s tool reports', () => {
  test.setTimeout(120_000)

  test.beforeEach(() => {
    test.skip(
      !demoTenant || !sessionFolder,
      'DEMO_TENANT and E2E_SESSION_FOLDER not set — run through the live conversation runner',
    )
    publish(BASE)
  })

  for (const theme of ['dark', 'light'] as const) {
    test(`lists, narrows, moves and inserts by keyboard (${theme})`, async ({
      page,
    }) => {
      await page.setViewportSize({ width: 1280, height: 800 })
      await openSession(page, theme)
      const field = page.getByTestId('launcher-input')

      await field.fill('/')
      const list = page.getByRole('listbox', { name: 'Commands and skills' })
      await expect(list).toBeVisible()
      // The tool's own entries, with the source its text carries, and the agent labelled.
      await expect(list.getByRole('option')).toHaveCount(4)
      const notes = list.getByRole('option', { name: /review-notes/ })
      await expect(notes).toContainText('Review my notes')
      await expect(notes).toContainText('User')
      await expect(notes).not.toContainText('(user)')
      const check = list.getByRole('option', { name: /deploy-check/ })
      await expect(check).toContainText('<env>')
      await expect(check).toContainText('Project')
      await expect(list.getByRole('option', { name: /compact/ })).toContainText(
        'Built-in',
      )
      await expect(
        list.getByRole('option', { name: /@agent-Explore/ }),
      ).toContainText('Agent')

      // The menu sits above the field inside the viewport, not cut by the pane.
      const box = await list.boundingBox()
      const fieldBox = await field.boundingBox()
      expect(box && fieldBox).toBeTruthy()
      expect(box!.y).toBeGreaterThanOrEqual(0)
      expect(box!.y + box!.height).toBeLessThanOrEqual(fieldBox!.y + 1)
      await page.screenshot({
        path: join(
          process.env.E2E_CAPTURE_DIR ?? test.info().outputDir,
          `slash-menu-1280-${theme}.png`,
        ),
      })

      // Accessibility of the open menu, on the composer card.
      const axe = await new AxeBuilder({ page })
        .include('[data-testid="work-composer"]')
        .withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa'])
        .analyze()
      expect(axe.violations.map((v) => `${v.id}: ${v.nodes.length}`)).toEqual(
        [],
      )

      // Search, move, insert; Enter inserts instead of sending.
      await field.fill('/dep')
      await expect(list.getByRole('option')).toHaveCount(1)
      await expect(page.getByTestId('slash-status')).toHaveText(
        '/deploy-check, 1 of 1',
      )
      await field.fill('/')
      await field.press('ArrowDown')
      await expect(field).toHaveAttribute('aria-activedescendant', /-option-1$/)
      await expect(page.getByTestId('slash-status')).toHaveText(
        '/deploy-check, 2 of 4',
      )
      await field.press('Enter')
      await expect(field).toHaveValue('/deploy-check ')
      await expect(list).toBeHidden()
    })
  }

  test('a skill added to the folder appears without restarting anything', async ({
    page,
  }) => {
    await openSession(page, 'dark')
    const field = page.getByTestId('launcher-input')
    await field.fill('/')
    const list = page.getByRole('listbox', { name: 'Commands and skills' })
    await expect(list.getByRole('option', { name: /brand-new/ })).toHaveCount(0)
    await field.fill('')
    await expect(list).toBeHidden()

    // The tool notices a new file in its folder a moment later (the fixture polls every
    // 0.2 s) and says so; the next opening of the menu then asks again.
    publish([
      ...BASE,
      {
        name: 'brand-new',
        description:
          'Added to the project folder while the session ran (project)',
        argumentHint: '',
      },
    ])
    await page.waitForTimeout(1000)
    await field.fill('/')
    await expect(list.getByRole('option', { name: /brand-new/ })).toContainText(
      'Added to the project folder',
    )
  })

  test('the inserted command is an ordinary message: it reaches the session when sent', async ({
    page,
  }) => {
    await openSession(page, 'dark')
    const field = page.getByTestId('launcher-input')
    await field.fill('/com')
    await field.press('Enter')
    await expect(field).toHaveValue('/compact ')
    const conversation = page.getByTestId('session-conversation')
    const replies = conversation.locator('[data-kind="assistant"]')
    const before = await replies.count()
    await field.press('Enter')
    // The session took it as a message, like any typed one, and answered: the fixture's
    // turn is the same two assistant messages the conversation spec counts (a lead-in
    // before its tool call and the reply after the result).
    await expect(replies).toHaveCount(before + 2, { timeout: 15_000 })
  })

  test('at 390 px the menu stays inside the screen', async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 })
    await openSession(page, 'dark')
    const field = page.getByTestId('launcher-input')
    await field.fill('/')
    const list = page.getByRole('listbox', { name: 'Commands and skills' })
    await expect(list).toBeVisible()
    const box = await list.boundingBox()
    expect(box!.x).toBeGreaterThanOrEqual(0)
    expect(box!.x + box!.width).toBeLessThanOrEqual(390)
    await page.screenshot({
      path: join(
        process.env.E2E_CAPTURE_DIR ?? test.info().outputDir,
        'slash-menu-390-dark.png',
      ),
    })
  })
})
