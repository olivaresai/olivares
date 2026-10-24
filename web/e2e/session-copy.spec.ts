// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// COPYING A MESSAGE (dev#2788), against a live seeded engine whose child is the fixture
// driver: it echoes the person's message as the tool does, then replies with a list and a
// code block. Selecting the words and copying them, and the Copy action, both put the
// exact text on the clipboard, for the person's message, the reply and its code block;
// the keyboard alone reaches Copy.
//
// Run: E2E_SPECS=e2e/session-copy.spec.ts scripts/e2e-session-conversation.sh
import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect, test, type Locator, type Page } from '@playwright/test'
import { signInFixture } from './fixture-sign-in'

const demoTenant = process.env.DEMO_TENANT ?? ''
const fixtureOn = process.env.CONVERSATION_FIXTURE === '1'
const folder = process.env.E2E_SESSION_FOLDER ?? ''
const runRef = process.env.E2E_CAPTURE_RUN_REF ?? ''

const ASKED = 'Look up the readme'
const REPLY =
  'Two things matter:\n\n- Identify clients: list who calls the API\n- Check README.md for the title\n\nmake test'

const clipboard = (page: Page) =>
  page.evaluate(() => navigator.clipboard.readText())

/** Drags the pointer across the words and copies them with the keyboard: words in a
 * button, or under `user-select: none`, are not selected by a drag. */
async function selectAndCopy(page: Page, words: Locator) {
  await page.evaluate(() => navigator.clipboard.writeText(''))
  const box = (await words.boundingBox())!
  const y = box.y + box.height / 2
  await page.mouse.move(box.x - 3, y)
  await page.mouse.down()
  await page.mouse.move(box.x + box.width + 3, y, { steps: 5 })
  await page.mouse.up()
  await page.keyboard.press('ControlOrMeta+c')
}

test('copies a message, a reply and a code block, by selection and by Copy', async ({
  page,
}) => {
  test.setTimeout(180_000)
  test.skip(
    !demoTenant || !fixtureOn || !folder || !runRef,
    'run through scripts/e2e-session-conversation.sh',
  )
  await page.context().grantPermissions(['clipboard-read', 'clipboard-write'])
  await page.context().addInitScript(() => {
    if (!window.localStorage.getItem('olivares.theme'))
      window.localStorage.setItem('olivares.theme', 'dark')
    window.localStorage.setItem('olivares.lang', 'en')
  })
  writeFileSync(join(folder, '.fixture-echo-user'), '')
  await page.setViewportSize({ width: 1280, height: 900 })
  await signInFixture(
    page,
    { email: 'demo@olivares.local', password: 'olivares-demo-estate' },
    20_000,
  )
  await page.goto(`/sessions?session=run:${runRef}&pane=narrative`)
  const input = page.getByTestId('launcher-input')
  await expect(input).toBeEnabled({ timeout: 15_000 })
  await input.fill(ASKED)
  await input.press('Enter')

  const conversation = page.getByTestId('session-conversation')
  const askedRow = page.locator('[data-kind="operator"]', { hasText: ASKED })
  const replyRow = page.locator('[data-kind="assistant"]', {
    hasText: 'Two things matter',
  })
  const asked = conversation.locator(askedRow)
  const reply = conversation.locator(replyRow)
  await expect(asked).toHaveCount(1, { timeout: 30_000 })
  await expect(reply).toHaveCount(1, { timeout: 30_000 })
  // The row is its words: nothing else is in its text (dev#2786's journey reads it).
  await expect(asked).toHaveText(ASKED)

  // Selection: the words of each can be selected, and a copy takes them as shown.
  await selectAndCopy(page, asked.locator('span').first())
  expect(await page.evaluate(() => window.getSelection()?.toString())).toBe(
    ASKED,
  )
  await expect.poll(() => clipboard(page)).toBe(ASKED)
  await selectAndCopy(page, reply.getByText('Two things matter:'))
  await expect.poll(() => clipboard(page)).toBe('Two things matter:')
  const code = reply.locator('[data-slot="reply-code"] code')
  await selectAndCopy(page, code)
  await expect.poll(() => clipboard(page)).toBe('make test')

  // Copy: the person's message as sent, the reply as read, the code exactly.
  const copyOf = (row: Locator) =>
    conversation
      .locator('li', { has: row })
      .getByRole('button', { name: 'Copy message' })
  await copyOf(askedRow).click()
  await expect.poll(() => clipboard(page)).toBe(ASKED)
  await copyOf(replyRow).click()
  await expect.poll(() => clipboard(page)).toBe(REPLY)
  await reply.getByRole('button', { name: 'Copy code' }).click()
  await expect.poll(() => clipboard(page)).toBe('make test')

  // The keyboard alone: from the composer, back to the reply's Copy, and Enter.
  await page.evaluate(() => navigator.clipboard.writeText(''))
  await input.focus()
  const label = () =>
    page.evaluate(() => document.activeElement?.getAttribute('aria-label'))
  for (let i = 0; i < 12 && (await label()) !== 'Copy message'; i++)
    await page.keyboard.press('Shift+Tab')
  expect(await label()).toBe('Copy message')
  await page.keyboard.press('Enter')
  await expect.poll(() => clipboard(page)).toBe(REPLY)
  for (let i = 0; i < 6 && (await label()) !== 'Copy code'; i++)
    await page.keyboard.press('Shift+Tab')
  expect(await label()).toBe('Copy code')
  await page.keyboard.press('Enter')
  await expect.poll(() => clipboard(page)).toBe('make test')

  const captures = process.env.E2E_CONVERSATION_CAPTURES
  if (!captures) return
  mkdirSync(captures, { recursive: true })
  for (const theme of ['dark', 'light'] as const)
    for (const width of [1280, 390]) {
      await page.evaluate((t) => {
        window.localStorage.setItem('olivares.theme', t)
      }, theme)
      await page.setViewportSize({ width, height: width > 500 ? 900 : 844 })
      await page.reload()
      await expect(reply).toHaveCount(1, { timeout: 15_000 })
      await copyOf(askedRow).scrollIntoViewIfNeeded()
      await copyOf(askedRow).click()
      await expect(
        conversation.locator('li', { has: askedRow }).getByText('Copied'),
      ).toBeAttached()
      await page.screenshot({
        path: join(captures, `session-copy-${width}-${theme}.png`),
      })
    }
})
