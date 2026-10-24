// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A SENT MESSAGE SHOWS AT ONCE (dev#2787). Claude Code shows the person's message only
// when it takes the turn, so a message sent to a busy tool appeared seconds later. The
// console shows it as soon as the engine takes it, then only the tool's own row.
//
// Against a live seeded engine, through scripts/e2e-session-conversation.sh with
// E2E_SENT_FIXTURE=1. The session, the console and the engine's answers are the built
// binary's; the session's live state, its input and its output come from route
// fixtures, so the journey runs where the engine cannot confine a child and the tool's
// delay is the same on every run: it shows the message 2 s after it was sent.
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { expect, test, type Page, type Route } from '@playwright/test'

const runRef = process.env.E2E_CAPTURE_RUN_REF ?? ''
const on =
  !!process.env.DEMO_TENANT && !!runRef && process.env.E2E_SENT_FIXTURE === '1'

const user = (text: string) => ({
  type: 'user',
  message: { role: 'user', content: text },
})
const reply = (text: string) => ({
  type: 'assistant',
  parent_tool_use_id: null,
  message: { role: 'assistant', content: [{ type: 'text', text }] },
})
const result = { type: 'result', subtype: 'success', duration_ms: 2100 }

/** The run reads as running wherever the engine names it. */
function live(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(live)
  if (value && typeof value === 'object') {
    const out: Record<string, unknown> = {}
    for (const [k, v] of Object.entries(value)) out[k] = live(v)
    if (out.run_ref === runRef) {
      out.state = 'running'
      out.process_state = 'running'
      delete out.reason
    }
    return out
  }
  return value
}

async function session(page: Page, refuse: () => boolean) {
  const frames: unknown[] = [
    { type: 'system', subtype: 'init', model: 'claude-opus-5-5', tools: [] },
    user('continue'),
    reply('Done with the first part.'),
    result,
  ]
  const wake: (() => void)[] = []
  await page.route('**/v1/m/sessions/**', async (route: Route) => {
    const request = route.request()
    const url = new URL(request.url())
    if (url.pathname.endsWith(`/runs/${runRef}/input`)) {
      if (refuse())
        return route.fulfill({
          status: 409,
          contentType: 'application/json',
          body: JSON.stringify({
            error: {
              code: 'conflict',
              message: 'the session is not accepting input',
            },
          }),
        })
      const body = JSON.parse(request.postData() ?? '{}') as { line?: string }
      const sent = JSON.parse(body.line ?? '{}') as {
        message?: { content?: string }
      }
      const text = sent.message?.content ?? ''
      // A busy tool takes the message two seconds later.
      if (text === 'continue')
        setTimeout(() => {
          frames.push(user(text), reply('Continuing.'), result)
          wake.splice(0).forEach((w) => w())
        }, 2_000)
      return route.fulfill({
        status: 202,
        contentType: 'application/json',
        body: JSON.stringify({ accepted: true }),
      })
    }
    if (url.pathname.endsWith(`/runs/${runRef}/attach`)) {
      const from = Number(url.searchParams.get('from') ?? '0')
      if (from >= frames.length)
        await new Promise<void>((resolve) => {
          wake.push(resolve)
          setTimeout(resolve, 5_000)
        })
      const body = frames
        .slice(from)
        .map(
          (frame, i) =>
            `event: output\ndata: ${JSON.stringify({
              seq: from + i,
              stream: 'stdout',
              line: JSON.stringify(frame),
            })}\n\n`,
        )
        .join('')
      return route.fulfill({
        status: 200,
        contentType: 'text/event-stream',
        body: body || ': ping\n\n',
      })
    }
    if (request.method() !== 'GET') return route.continue()
    const response = await route.fetch()
    const type = response.headers()['content-type'] ?? ''
    if (!type.includes('application/json')) return route.fulfill({ response })
    return route.fulfill({ response, json: live(await response.json()) })
  })
}

async function open(page: Page, theme: 'dark' | 'light', width: number) {
  await page.context().addInitScript((t) => {
    window.localStorage.setItem('olivares.theme', t)
    window.localStorage.setItem('olivares.lang', 'en')
  }, theme)
  await page.setViewportSize({ width, height: width > 500 ? 900 : 844 })
  await page.goto('/login')
  await page.locator('input[type=email]').first().fill('demo@olivares.local')
  await page
    .locator('input[type=password]')
    .first()
    .fill('olivares-demo-estate')
  await page.locator('button[type=submit]').first().click()
  await page.waitForURL((url) => !url.pathname.startsWith('/login'), {
    timeout: 20_000,
  })
  await page.goto(`/sessions?session=run:${runRef}&pane=narrative`)
  await expect(
    page
      .getByTestId('session-conversation')
      .getByText('Done with the first part.'),
  ).toBeVisible({ timeout: 15_000 })
  await expect(page.getByTestId('launcher-input')).toBeEnabled({
    timeout: 15_000,
  })
}

// A long-polled attach can still be pending when a test ends.
test.afterEach(async ({ page }) => {
  await page.unrouteAll({ behavior: 'ignoreErrors' })
})

const operatorRows = (page: Page, text: string) =>
  page
    .getByTestId('session-conversation')
    .locator('[data-kind="operator"]', { hasText: text })

test('a sent message shows at once, once, and a refused one keeps its words', async ({
  page,
}) => {
  test.setTimeout(120_000)
  test.skip(
    !on,
    'E2E_SENT_FIXTURE=1 through scripts/e2e-session-conversation.sh',
  )
  let refusing = false
  await session(page, () => refusing)
  await open(page, 'dark', 1280)
  const input = page.getByTestId('launcher-input')
  const pending = page.getByTestId('conversation-sent')

  // The same words as a message the tool already shows: the new one is not hidden by it.
  await input.fill('continue')
  await input.press('Enter')
  await expect(pending).toHaveText(/continue/, { timeout: 300 })
  await expect(operatorRows(page, 'continue')).toHaveCount(1)
  // The tool takes it: its own row replaces the console's, with no duplicate.
  await expect(page.getByText('Continuing.')).toBeVisible({ timeout: 15_000 })
  await expect(pending).toHaveCount(0)
  await expect(operatorRows(page, 'continue')).toHaveCount(2)

  refusing = true
  await input.fill('Look up the readme')
  await input.press('Enter')
  await expect(
    page.getByText('the session is not accepting input').first(),
  ).toBeVisible({ timeout: 5_000 })
  await expect(input).toHaveValue('Look up the readme')
  await expect(pending).toHaveCount(0)
})

for (const theme of ['dark', 'light'] as const)
  for (const width of [1280, 390])
    test(`capture: a sent message before the tool shows it (${width} px, ${theme})`, async ({
      page,
    }) => {
      const captures = process.env.E2E_CONVERSATION_CAPTURES
      test.skip(!on || !captures, 'captures run with E2E_CONVERSATION_CAPTURES')
      await session(page, () => false)
      await open(page, theme, width)
      const input = page.getByTestId('launcher-input')
      await input.fill('Now run the tests and tell me what failed')
      await input.press('Enter')
      await expect(page.getByTestId('conversation-sent')).toBeVisible({
        timeout: 300,
      })
      mkdirSync(captures!, { recursive: true })
      await page.screenshot({
        path: join(captures!, `sent-message-${width}-${theme}.png`),
      })
    })
