// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { writeFileSync, mkdirSync, rmSync } from 'node:fs'
import { expect, test } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { sessionToken } from './live-session'

let fixtureMarker: string | null = null
test.afterEach(() => {
  if (fixtureMarker) rmSync(fixtureMarker, { force: true })
  fixtureMarker = null
})

test('answers a question through the real engine and records its audit', async ({
  page,
}) => {
  test.skip(
    !process.env.DEMO_TENANT,
    'requires the conversation fixture engine',
  )
  test.setTimeout(120_000)
  const marker = `${process.env.E2E_SESSION_FOLDER}/.fixture-cards`
  writeFileSync(marker, '')
  fixtureMarker = marker
  await page.addInitScript(() => {
    localStorage.setItem('olivares.theme', 'light')
    localStorage.setItem('olivares.lang', 'en')
  })
  await page.setViewportSize({ width: 1280, height: 900 })
  await page.goto('/login')
  await page.locator('input[type=email]').fill('demo@olivares.local')
  await page.locator('input[type=password]').fill('olivares-demo-estate')
  await page.locator('button[type=submit]').click()
  await page.waitForURL((url) => !url.pathname.startsWith('/login'))
  const ref = process.env.E2E_CAPTURE_RUN_REF!
  const headers = {
    Authorization: `Bearer ${await sessionToken(page)}`,
    'X-Olivares-Tenant': process.env.DEMO_TENANT!,
  }
  const prompt = await page.request.post(`/v1/m/sessions/runs/${ref}/input`, {
    headers,
    data: {
      line: JSON.stringify({
        type: 'user',
        message: { role: 'user', content: 'Ask me about the database' },
      }),
    },
  })
  expect(prompt.ok()).toBeTruthy()
  await page.goto(`/sessions?session=run:${ref}&pane=narrative`)
  const question = page.getByTestId('conversation-card-question')
  await expect(question).toBeVisible()
  await expect(page.getByTestId('conversation-card-plan')).toBeVisible()
  await expect(page.getByTestId('conversation-card-todos')).toBeVisible()
  await question.getByRole('radio', { name: /PostgreSQL/ }).focus()
  await page.keyboard.press('ArrowDown')
  await expect(question.getByRole('radio', { name: /SQLite/ })).toBeChecked()
  await page.keyboard.press('ArrowUp')
  await expect(
    question.getByRole('radio', { name: /PostgreSQL/ }),
  ).toBeChecked()
  await page.keyboard.press('2')
  await expect(question.getByRole('radio', { name: /SQLite/ })).toBeChecked()
  const output = process.env.CARDS_CAPTURE_DIR ?? 'test-results/cards'
  mkdirSync(output, { recursive: true })
  await page.screenshot({
    path: `${output}/conversation-1280-light.png`,
    fullPage: true,
  })
  const identity = await page.request.get('/v1/auth/whoami', { headers })
  expect(identity.ok()).toBeTruthy()
  const actor = (await identity.json()).actor as string
  const before = await page.request.get('/v1/audit?limit=1', { headers })
  expect(before.ok()).toBeTruthy()
  const beforeSeq = (await before.json()).head_seq as number
  const input = page.waitForResponse(
    (r) =>
      r.url().endsWith(`/runs/${ref}/input`) && r.request().method() === 'POST',
  )
  await question.getByRole('button', { name: 'Send answer' }).click()
  const accepted = await input
  expect(accepted.ok()).toBeTruthy()
  const sent = JSON.parse(accepted.request().postDataJSON().line)
  expect(sent.message.content).toBe('SQLite')
  await expect(
    page.getByText('Received on stdin: SQLite', { exact: true }),
  ).toBeVisible()
  await page.screenshot({
    path: `${output}/answer-1280-light.png`,
    fullPage: true,
  })
  const audit = await page.request.get(
    `/v1/audit?from=${beforeSeq + 1}&limit=100`,
    { headers },
  )
  expect(audit.ok()).toBeTruthy()
  const events = await audit.json()
  expect(events.items).toEqual(
    expect.arrayContaining([
      expect.objectContaining({
        action: 'authorization_decision.record',
        actor,
        seq: expect.any(Number),
      }),
    ]),
  )
  expect(
    events.items
      .filter(
        (event: { action: string; actor: string; seq: number }) =>
          event.action === 'authorization_decision.record' &&
          event.actor === actor,
      )
      .every((event: { seq: number }) => event.seq > beforeSeq),
  ).toBeTruthy()
  writeFileSync(`${output}/audit.json`, JSON.stringify(events, null, 2))
  writeFileSync(`${output}/input.json`, JSON.stringify(sent, null, 2))
  for (const width of [1280, 390]) {
    await page.setViewportSize({ width, height: 900 })
    for (const theme of ['light', 'dark']) {
      await page.evaluate((t) => {
        localStorage.setItem('olivares.theme', t)
        document.documentElement.classList.toggle('dark', t === 'dark')
      }, theme)
      await page.screenshot({
        path: `${output}/conversation-${width}-${theme}-answered.png`,
        fullPage: true,
      })
      expect(
        (
          await new AxeBuilder({ page })
            .include('[data-testid="session-conversation"]')
            .analyze()
        ).violations,
      ).toEqual([])
    }
  }
})
