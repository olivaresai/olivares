// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE MODEL IN USE (dev#2786), against a live seeded engine whose child is the fixture
// driver. The seeded session was launched with no model: the engine hands the tool the
// exact id of its Claude Code default, and the driver reports the model it was launched
// with.
// Then the driver goes on on another model with no word of it (.fixture-model), and the
// conversation and the header must say so.
//
// Run: E2E_SPECS=e2e/session-model-in-use.spec.ts scripts/e2e-session-conversation.sh
import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect, test, type Page } from '@playwright/test'

const demoTenant = process.env.DEMO_TENANT ?? ''
const fixtureOn = process.env.CONVERSATION_FIXTURE === '1'
const folder = process.env.E2E_SESSION_FOLDER ?? ''
const runRef = process.env.E2E_CAPTURE_RUN_REF ?? ''

async function signIn(page: Page) {
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
}

async function send(page: Page, text: string) {
  const input = page.getByTestId('launcher-input')
  await expect(input).toBeEnabled({ timeout: 15_000 })
  await input.fill(text)
  await input.press('Enter')
}

test('names the model in use and tells a change of model', async ({ page }) => {
  test.setTimeout(180_000)
  test.skip(
    !demoTenant || !fixtureOn || !folder || !runRef,
    'run through scripts/e2e-session-conversation.sh',
  )
  await page.context().addInitScript(() => {
    if (!window.localStorage.getItem('olivares.theme'))
      window.localStorage.setItem('olivares.theme', 'dark')
    window.localStorage.setItem('olivares.lang', 'en')
  })
  await page.setViewportSize({ width: 1280, height: 900 })
  await signIn(page)
  await page.goto(`/sessions?session=run:${runRef}&pane=narrative`)
  const conversation = page.getByTestId('session-conversation')
  const header = page.locator('[data-slot="thread-header"]')

  await send(page, 'First turn')
  await expect(
    conversation.locator('[data-kind="result"]').first(),
  ).toContainText('Claude Opus 5.5', { timeout: 15_000 })
  await expect(header.getByTestId('thread-tool')).toHaveText('Claude Code')
  await expect(page.getByTestId('thread-model-mismatch')).toHaveCount(0)

  writeFileSync(join(folder, '.fixture-model'), 'claude-opus-4-8\n')
  await send(page, 'Second turn')
  const notice = conversation.locator('[data-kind="system"]', {
    hasText: 'Model changed',
  })
  await expect(notice).toHaveText(
    /^Model changed at .+: Claude Opus 5\.5 → Claude Opus 4\.8$/,
    { timeout: 15_000 },
  )
  await expect(header.getByTestId('thread-tool')).toHaveText('Claude Code')
  await expect(page.getByTestId('thread-model-mismatch')).toHaveText(
    'Asked for Claude Opus 5.5, running Claude Opus 4.8',
  )
  // A reload replays the frames with the time the engine read them, not the replay's.
  const said = await notice.innerText()
  await page.reload()
  await expect(notice).toHaveText(said, { timeout: 15_000 })

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
      await expect(notice).toHaveText(said, { timeout: 15_000 })
      await expect(page.getByTestId('thread-model-mismatch')).toBeVisible()
      await notice.scrollIntoViewIfNeeded()
      await page.screenshot({
        path: join(captures, `model-in-use-${width}-${theme}.png`),
      })
    }
})

// THE SAME SCREEN WHERE NO CHILD CAN BE CONFINED. A host that refuses the engine's
// filesystem confinement (a container that is itself confined) starts no fixture child,
// so this variant serves the seeded session's attach stream from a route fixture: the
// session, its requested model and the console are the built binary's; the frames are
// the ones the live test above makes the driver write. Run with E2E_ATTACH_FIXTURE=1.
test('the change of model reads the same from recorded frames', async ({
  page,
}) => {
  test.skip(
    !demoTenant || !runRef || process.env.E2E_ATTACH_FIXTURE !== '1',
    'E2E_ATTACH_FIXTURE=1 through scripts/e2e-session-conversation.sh',
  )
  const reply = (model: string, text: string) => ({
    type: 'assistant',
    parent_tool_use_id: null,
    message: { role: 'assistant', model, content: [{ type: 'text', text }] },
  })
  const asked = (text: string) => ({
    type: 'user',
    message: { role: 'user', content: text },
  })
  const result = { type: 'result', subtype: 'success', duration_ms: 4200 }
  const frames = [
    { type: 'system', subtype: 'init', model: 'claude-opus-5-5', tools: [] },
    asked('Look up the readme'),
    reply(
      'claude-opus-5-5',
      'Two things matter:\n\n- **Identify clients:** list who calls the API\n- Check `README.md` for the title',
    ),
    result,
    asked('Now run the tests'),
    reply('claude-opus-4-8', 'Running `make test` now.'),
    result,
  ]
  const t0 = Date.parse('2026-10-21T12:03:00Z')
  const body = frames
    .map(
      (frame, seq) =>
        `event: output\ndata: ${JSON.stringify({
          seq,
          stream: 'stdout',
          line: JSON.stringify(frame),
          at: new Date(t0 + seq * 20_000).toISOString(),
        })}\n\n`,
    )
    .join('')
  await page.route(`**/v1/m/sessions/runs/${runRef}/attach**`, (route) =>
    route.fulfill({
      status: 200,
      contentType: 'text/event-stream',
      body: `${body}event: end\ndata: {}\n\n`,
    }),
  )
  await page.context().addInitScript(() => {
    if (!window.localStorage.getItem('olivares.theme'))
      window.localStorage.setItem('olivares.theme', 'dark')
    window.localStorage.setItem('olivares.lang', 'en')
  })
  await page.setViewportSize({ width: 1280, height: 900 })
  await signIn(page)
  await page.goto(`/sessions?session=run:${runRef}&pane=narrative`)
  const conversation = page.getByTestId('session-conversation')
  const header = page.locator('[data-slot="thread-header"]')
  const notice = conversation.locator('[data-kind="system"]', {
    hasText: 'Model changed',
  })
  await expect(notice).toHaveText(
    /^Model changed at .+: Claude Opus 5\.5 → Claude Opus 4\.8$/,
    { timeout: 15_000 },
  )
  await expect(
    conversation.locator('[data-kind="result"]').first(),
  ).toContainText('Claude Opus 5.5')
  await expect(
    conversation.locator('[data-kind="result"]').last(),
  ).toContainText('Claude Opus 4.8')
  await expect(page.getByTestId('thread-model-mismatch')).toHaveText(
    'Asked for Claude Opus 5.5, running Claude Opus 4.8',
  )
  await expect(header.getByTestId('thread-tool')).toHaveText('Claude Code')

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
      await expect(notice).toBeVisible({ timeout: 15_000 })
      await expect(page.getByTestId('thread-model-mismatch')).toBeVisible()
      await page.screenshot({
        path: join(captures, `model-in-use-${width}-${theme}.png`),
      })
    }
})
