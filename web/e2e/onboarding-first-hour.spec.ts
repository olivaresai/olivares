// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The first hour against a LIVE olivares binary and a fresh store. A password-only
// owner lands on the setup wizard, which asks only for what the first session needs:
// install a tool and sign it in, or add an API key or a local model, then start a
// session. Nothing waits for a passkey or step-up, and nothing asks for a workspace
// (Default exists), a second administrator, a source or a policy. The 26.10.0 wizard
// showed seven steps, three of them behind a passkey prompt.
// The same engine then does the rest of the first job, one journey of the table at a
// time: an approval the session asks for, stop, resume and history, and the provider
// key's pages. Each walk is checked against JOURNEYS.
// scripts/web-e2e.sh boots a fresh engine and exports the one-time setup token; without
// it the test skips (it needs a pristine, un-set-up engine).
import { expect, test } from './fixtures/ollama'
import {
  JOURNEYS,
  stepHref,
  unwalked,
} from '../src/features/navigation/journeys'

const [WIZARD, PROVIDERS, SESSIONS] = JOURNEYS.firstHour

const setupToken = process.env.PLAYWRIGHT_SETUP_TOKEN ?? ''
const PASSWORD = 'correct-horse-battery-staple-42'

// Setup carries a one-time token and password. Capture only explicit screenshots
// after setup; automatic traces/video/failure screenshots could retain credentials.
test.use({ screenshot: 'off', trace: 'off', video: 'off' })

test('first hour: the setup wizard asks only for what the first session needs', async ({
  page,
  ollama,
}) => {
  test.setTimeout(300_000) // Includes the real verified tool download and install.
  test.skip(
    !setupToken,
    'PLAYWRIGHT_SETUP_TOKEN not set — run via scripts/web-e2e.sh',
  )
  // Every WebAuthn ceremony the console asks the browser for, and every refusal. The
  // counter exists only once the wrap is in place, so a failed wrap fails the test.
  await page.addInitScript(() => {
    const w = window as unknown as { webauthnCalls: number }
    const credentials = navigator.credentials
    const create = credentials.create.bind(credentials)
    const get = credentials.get.bind(credentials)
    credentials.create = (o) => (w.webauthnCalls++, create(o))
    credentials.get = (o) => (w.webauthnCalls++, get(o))
    w.webauthnCalls = 0
  })
  const refused: string[] = []
  // Every address the page passes through, in order, to compare with the journey table.
  const visited: string[] = []
  page.on('framenavigated', (frame) => {
    if (frame === page.mainFrame()) visited.push(frame.url())
  })
  page.on('response', (r) => {
    if (r.url().includes('/v1/') && r.status() >= 400)
      refused.push(`${r.status()} ${new URL(r.url()).pathname}`)
  })

  // First boot: the administrator, then the wizard (real API + real DB writes).
  await page.goto('/setup')
  await page.locator('#token').fill(setupToken)
  await page.locator('#setup-email').fill('admin@example.com')
  await page.locator('#setup-password').fill(PASSWORD)
  await page.getByRole('button', { name: /create administrator/i }).click()
  await page.waitForURL(`**${stepHref(WIZARD)}`)

  const main = page.getByRole('main')
  await expect(main.getByText('0 of 3 done')).toBeVisible()
  await expect(main.getByRole('heading', { level: 2 })).toHaveText([
    'Install an agent tool',
    'Sign it in',
    'Start a session',
    'Next, when you need them',
  ])
  await expect(main).not.toContainText(
    /first workspace|invite an administrator|first source|policy enforcement|managed-settings|provider profile|privileged read|audit ledger|step-up|\bAAL\d|passkey|security key|\bpending\b|of \d+ verified/i,
  )

  // A key or a local model is offered before any install.
  const install = main.getByRole('region', { name: 'Install an agent tool' })
  await expect(
    install.getByRole('link', { name: 'Add an API key' }),
  ).toHaveAttribute(
    'href',
    stepHref(PROVIDERS, { add: 'anthropic', returnTo: stepHref(WIZARD) }),
  )
  await expect(
    install.getByRole('link', { name: 'Add a local model' }),
  ).toHaveAttribute(
    'href',
    stepHref(PROVIDERS, { add: 'ollama', returnTo: stepHref(WIZARD) }),
  )

  await page.screenshot({
    path: 'playwright-report/onboarding-first-hour.png',
    fullPage: true,
  })
  // Follow the offered route through real provider, tool and profile creation.
  // Only the model endpoint is a labelled stand-in; no product API is mocked.
  await install.getByRole('link', { name: 'Add a local model' }).click()
  const dialog = page.getByRole('dialog')
  await dialog
    .getByRole('textbox', { name: /^Endpoint\b/ })
    .fill(ollama.endpoint)
  await dialog
    .getByRole('button', { name: 'Add provider', exact: true })
    .click()
  await page.waitForURL(`**${stepHref(WIZARD)}`, { timeout: 120_000 })
  await expect(main.getByText('2 of 3 done')).toBeVisible()
  await page.screenshot({
    path: 'playwright-report/onboarding-ready.png',
    fullPage: true,
  })
  const start = main.getByRole('region', { name: 'Start a session' })
  await start
    .getByLabel('First message (optional)')
    .fill('Say hello in one sentence.')
  await start.getByRole('button', { name: 'Start', exact: true }).click()
  await page.waitForURL(`**${stepHref(SESSIONS)}?**`)
  await expect(
    page.getByText(ollama.response, { exact: false }).first(),
  ).toBeVisible({
    timeout: 30_000,
  })
  await page.screenshot({
    path: 'playwright-report/onboarding-first-output.png',
    fullPage: true,
  })
  // A full navigation resets the document counter. Check the entire provider /
  // session journey before returning, then check the new wizard document below.
  expect(
    await page.evaluate(
      () => (window as unknown as { webauthnCalls: number }).webauthnCalls,
    ),
  ).toBe(0)
  expect(unwalked(JOURNEYS.firstHour, visited)).toEqual([])
  await page.goto(stepHref(WIZARD))
  await expect(main.getByText('3 of 3 done')).toBeVisible()

  await page.waitForLoadState('networkidle')
  expect(
    await page.evaluate(
      () => (window as unknown as { webauthnCalls: number }).webauthnCalls,
    ),
  ).toBe(0)
  expect(refused).toEqual([])
  await page.screenshot({
    path: 'playwright-report/onboarding-complete.png',
    fullPage: true,
  })

  const nav = page.getByRole('navigation')

  // APPROVALS: a session started to ask before each action asks for the shell command
  // the model wants, and waits in the queue the sidebar opens until a person approves.
  visited.splice(0)
  await start.getByRole('button', { name: 'More options' }).click()
  await start.getByRole('radio', { name: 'Ask before each action' }).check()
  await start
    .getByLabel('First message (optional)')
    .fill(`Please run: ${ollama.toolCommand}`)
  await start.getByRole('button', { name: 'Start', exact: true }).click()
  await page.waitForURL(`**${stepHref(SESSIONS)}?**`)
  await nav.getByRole('link', { name: /^Approvals\b/ }).click()
  await page.getByRole('button', { name: 'Approve', exact: true }).click({
    timeout: 60_000,
  })
  await page
    .getByRole('dialog')
    .getByRole('button', { name: 'Approve', exact: true })
    .click()
  await expect(page.getByText('Decision recorded — approved')).toBeVisible()
  expect(unwalked(JOURNEYS.approvals, visited)).toEqual([])

  // SESSIONS: the same session runs the approved command and keeps its history across a
  // reload, then stops and resumes with the conversation still on the page.
  visited.splice(0)
  await nav.getByRole('link', { name: /^Sessions\b/ }).click()
  await page
    .getByTestId('rail-row')
    .filter({ hasText: ollama.toolCommand })
    .click()
  const conversation = page.getByTestId('session-conversation')
  await expect(conversation).toContainText(ollama.toolDone, {
    timeout: 60_000,
  })
  await page.reload()
  await expect(conversation).toContainText(ollama.toolCommand, {
    timeout: 30_000,
  })
  await expect(conversation).toContainText(ollama.toolDone)
  await page.getByRole('button', { name: 'Stop', exact: true }).click()
  await page
    .getByRole('dialog', { name: 'Stop this session?', exact: true })
    .getByRole('button', { name: 'Stop the session', exact: true })
    .click()
  await page.getByRole('button', { name: 'Resume', exact: true }).click()
  await expect(
    page.getByRole('button', { name: 'Stop', exact: true }),
  ).toBeVisible({ timeout: 30_000 })
  await expect(conversation).toContainText(ollama.toolDone)
  expect(unwalked(JOURNEYS.sessions, visited)).toEqual([])

  // PROVIDER KEYS: the key the wizard added, the profile sessions launch with, and the
  // accounts a tool can sign in with instead. A fresh install lists every area from the
  // first sign-in, so the sidebar's area groups offer profiles and accounts beside the keys.
  visited.splice(0)
  await nav.getByRole('link', { name: 'AI tools', exact: true }).click()
  await page.getByRole('link', { name: 'API keys', exact: true }).click()
  await expect(page.getByRole('main')).toContainText(ollama.endpoint)
  const [, PROFILES, ACCOUNTS] = JOURNEYS.providerKeys
  const areas = page.locator('[data-slot="sidebar-areas"]')
  await expect(
    areas.getByRole('link', { name: 'API keys', exact: true }),
  ).toBeVisible()
  for (const name of ['Provider profiles', 'Provider accounts'])
    await expect(areas.getByRole('link', { name, exact: true })).toHaveCount(1)
  await page.goto(stepHref(PROFILES))
  await expect(
    page.getByRole('heading', { name: 'Provider profiles' }),
  ).toBeVisible()
  await page.goto(stepHref(ACCOUNTS))
  await expect(
    page.getByRole('heading', { name: 'Provider accounts' }),
  ).toBeVisible()
  expect(unwalked(JOURNEYS.providerKeys, visited)).toEqual([])
  expect(refused).toEqual([])
})
