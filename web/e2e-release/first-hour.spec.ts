// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { randomBytes } from 'node:crypto'
import { readFileSync, writeFileSync } from 'node:fs'
import { expect, test } from '@playwright/test'
import {
  JOURNEYS,
  stepHref,
  unwalked,
} from '../src/features/navigation/journeys'
import {
  expectReleaseVersion,
  READY_MS,
  RELEASE_VERSION,
  SLOW_MS,
  surfaceOptions,
  watchSurface,
} from './surface'

const [WIZARD, PROVIDERS, SESSIONS] = JOURNEYS.firstHour

// The one path every tool takes: provider, install, a session that asks before it
// acts, stop and resume. Only the provider is a labelled stand-in (provider.py, on
// the engine's loopback); the strings below are the ones it matches and answers.
const MODEL = 'first-hour-stub'
const ANSWER = 'FIRST_HOUR_PROVIDER_STUB_OK'
const RESUMED = 'FIRST_HOUR_RESUMED_STUB_OK'
// Only running it prints first-hour-42; provider.py answers ANSWER on that alone.
const COMMAND = 'echo first-hour-$((6*7))'
const RESUME_PROMPT = 'Answer once more after the resume.'
// `prepared`: the page prepares the tool after the key's first passing test (docs:
// add-a-provider); an OpenAI-compatible key prepares nothing, Codex installs in stage 03.
const TOOLS = [
  // A local model, as before: OpenCode's own route.
  {
    key: 'opencode',
    name: 'OpenCode',
    kind: 'Ollama',
    endpoint: '',
    prepared: true,
  },
  {
    key: 'codex',
    name: 'Codex',
    kind: 'OpenAI-compatible',
    endpoint: '/v1',
    prepared: false,
  },
  {
    key: 'claude',
    name: 'Claude Code',
    kind: 'Anthropic',
    endpoint: '',
    prepared: true,
  },
] as const
// Every planned stage: setup, then provider, install and session per tool, then
// seven settings sections and four pages. A run that stops early never passes.
const JOURNEY_STAGES = 1 + 3 * TOOLS.length
const ALL_STAGES = JOURNEY_STAGES + 7 + 4

test('release container: a fresh administrator reaches a first answer and the console pages', async ({
  page,
}, info) => {
  const version = process.env.PLAYWRIGHT_RELEASE_VERSION
  const tokenFile = process.env.PLAYWRIGHT_SETUP_TOKEN_FILE
  expect(
    version,
    'release version is mandatory; never skip qualification',
  ).toMatch(RELEASE_VERSION)
  expect(tokenFile, 'fresh Compose setup token file is mandatory').toBeTruthy()
  test.setTimeout(25 * 60_000)
  const token = readFileSync(tokenFile!, 'utf8').trim()
  const password = randomBytes(24).toString('hex')
  // Generated keys the stand-in accepts; a key kind needs one to be registered.
  const keys = TOOLS.map(() => randomBytes(24).toString('hex'))
  const secrets = [token, password, ...keys]
  // Compose cleanup redacts these generated values before publishing its log.
  if (process.env.FIRST_HOUR_SECRETS_FILE)
    writeFileSync(
      process.env.FIRST_HOUR_SECRETS_FILE,
      JSON.stringify(secrets),
      {
        mode: 0o600,
      },
    )
  const failures = new Set<string>()
  const completed: string[] = []
  const journeyFailures = new Set<string>()
  const redact = (text: string) =>
    secrets.reduce((out, secret) => out.replaceAll(secret, '<redacted>'), text)
  const record = (message: string) => failures.add(redact(message))
  await page.exposeFunction('__firstHourFinding', record)
  // Loads past the console's 5 s target: evidence, not a failure (#1177).
  const slow: string[] = []
  await page.exposeFunction('__firstHourSlow', (message: string) => {
    slow.push(message)
  })
  await page.addInitScript(watchSurface, surfaceOptions(version!))
  page.on('console', (message) => {
    if (message.type() === 'error')
      record(
        `${new URL(page.url()).pathname}: console error: ${message.text()}`,
      )
  })
  page.on('pageerror', (error) => record(`page error: ${error.message}`))
  const visited: string[] = []
  page.on('framenavigated', (frame) => {
    if (frame === page.mainFrame()) visited.push(frame.url())
  })
  const capture = async (name: string) => {
    // Every stage ends on a ready page: waiting returns as soon as it is, and a page
    // still loading after READY_MS fails the stage (#1177).
    await page
      .waitForFunction(() => !window.__firstHourLoading?.(), undefined, {
        timeout: READY_MS,
      })
      .catch(() => record(`${name}: still loading after ${READY_MS / 1000} s`))
    try {
      await expectReleaseVersion(page, version!)
    } catch (error) {
      record(
        `${name}: ${error instanceof Error ? error.message : String(error)}`,
      )
    }
    // Compared here as well as on the watcher's tick, so a stage never ends on an
    // identity nobody read, or on a page the watcher is not installed on.
    try {
      const versions = await page.evaluate(() => window.__firstHourVersions?.())
      if (!versions)
        record(`${name}: the surface watcher is not installed on this page`)
      else {
        console.log(
          `UI version ${name}: ${redact(versions.join(', ')) || 'missing'}`,
        )
        for (const found of versions)
          if (found !== version)
            record(
              `${name}: page version ${found} differs from release ${version}`,
            )
        if (!versions.includes(version!))
          record(
            `${name}: release version ${version} was not read from the console identity`,
          )
      }
    } catch (error) {
      record(
        `${name}: identity versions unreadable: ${error instanceof Error ? error.message : String(error)}`,
      )
    }
    const path = info.outputPath(`${name}.png`)
    await page.screenshot({
      path,
      fullPage: true,
      mask: [page.locator('#token, input[type=password]')],
    })
    await info.attach(name, { path, contentType: 'image/png' })
  }
  const stage = async (name: string, action: () => Promise<void>) => {
    await test.step(name, async () => {
      try {
        await action()
        completed.push(name)
      } catch (error) {
        const failure = `${name}: ${error instanceof Error ? error.message : String(error)}`
        record(failure)
        if (/^0[1-4]-/.test(name)) journeyFailures.add(redact(failure))
      } finally {
        await capture(name)
      }
    })
  }
  const visit = async (path: string) => {
    await page.goto(path)
    await expect(page.getByRole('main')).toBeVisible()
    await expect(
      page.locator(
        '.animate-spin:visible, .animate-pulse:visible, [aria-busy=true]:visible',
      ),
    ).toHaveCount(0)
  }
  try {
    await stage('01-setup-administrator', async () => {
      await page.goto('/setup')
      await expect(page.locator('#token')).toBeVisible()
      await capture('setup-empty')
      // Mask the token in the video, too; this changes only paint. No seeded auth,
      // storageState, passkey, request interception or bypass of the setup form.
      await page.addStyleTag({
        content: '#token { -webkit-text-security: disc !important; }',
      })
      await page.locator('#token').fill(token)
      await page.locator('#setup-email').fill('first-hour@example.invalid')
      await page.locator('#setup-password').fill(password)
      await page.getByRole('button', { name: /create administrator/i }).click()
      await page.waitForURL(`**${stepHref(WIZARD)}`)
      await expect(
        page.getByRole('link', { name: 'Now', exact: true }),
      ).toBeVisible()
    })
    // Keep later pages measurable even when the first answer is blocked. Every
    // failed stage remains a failure; none is retried, seeded or marked skipped.
    for (const [index, tool] of TOOLS.entries()) {
      await stage(`02-provider-${tool.key}`, async () => {
        const name = `First-hour ${tool.name} stub`
        await page.goto(stepHref(PROVIDERS))
        await page.getByRole('button', { name: /^Add provider$/ }).click()
        const dialog = page.getByRole('dialog', {
          name: 'Add a provider',
          exact: true,
        })
        await dialog.getByRole('combobox').click()
        await page.getByRole('option', { name: tool.kind, exact: true }).click()
        await dialog
          .getByRole('textbox', { name: 'Name', exact: true })
          .fill(name)
        if (tool.kind === 'Anthropic')
          await dialog
            .getByRole('button', { name: 'Advanced: custom endpoint' })
            .click()
        await dialog
          .getByRole('textbox', { name: /^Endpoint\b/ })
          .fill(`http://127.0.0.1:11434${tool.endpoint}`)
        if (tool.kind !== 'Ollama')
          await dialog.getByLabel('API key').fill(keys[index])
        await dialog
          .getByRole('textbox', { name: 'Default model', exact: true })
          .fill(MODEL)
        await capture(`provider-${tool.key}-filled`)
        await dialog
          .getByRole('button', { name: 'Add provider', exact: true })
          .click()
        await expect(dialog).toBeHidden()
        const row = page.getByRole('row').filter({ hasText: name })
        await row
          .getByRole('button', { name: 'Test connection', exact: true })
          .click()
        await expect(row.getByText('Accepted', { exact: true })).toBeVisible()
        // A new key's first passing test prepares its tool (docs: add-a-provider), and
        // leaving the page cancels that. Wait as a person does: Add provider comes back
        // when the preparation is done, and says so (#1088).
        await expect(
          page.getByRole('button', { name: /^Add provider$/ }),
        ).toBeEnabled({ timeout: 180_000 })
        await expect(
          page.getByText(/^Provider saved\. Session setup failed/),
        ).toHaveCount(0)
        if (tool.prepared)
          await expect(
            page
              .getByRole('main')
              .getByText('Ready to start a session.', { exact: true }),
          ).toBeVisible()
      })
    }
    for (const tool of TOOLS) {
      await stage(`03-install-${tool.key}`, async () => {
        await page.goto('/agent-tools')
        // A tool that is not installed is one line with an Install menu; one that a
        // provider's first test already installed is a boxed list with Update in its menu.
        // The engine runs one install at a time: the item is enabled once that job is done.
        const missing = page.getByTestId('not-installed')
        let review
        if ((await missing.getByText(tool.name).count()) > 0) {
          await missing.getByRole('button', { name: /^Install/ }).click()
          review = page.getByRole('menuitem', {
            name: `Install ${tool.name}`,
            exact: true,
          })
        } else {
          await page
            .getByRole('button', {
              name: `Options for ${tool.name}`,
              exact: true,
            })
            .click()
          review = page.getByRole('menuitem', { name: 'Update', exact: true })
        }
        await expect(review).toBeEnabled({ timeout: 180_000 })
        await review.click()
        await page
          .getByRole('button', {
            name: 'Install approved version',
            exact: true,
          })
          .click()
        await expect(
          page.getByText(new RegExp(`^Installation complete · ${tool.name} `)),
        ).toBeVisible({ timeout: 180_000 })
      })
    }
    for (const tool of TOOLS) {
      await stage(`04-session-${tool.key}`, async () => {
        await page.goto(stepHref(SESSIONS))
        await page
          .getByRole('button', { name: 'New session', exact: true })
          .first()
          .click()
        const dialog = page.getByRole('dialog', {
          name: 'New session',
          exact: true,
        })
        await dialog
          .getByRole('radio', { name: tool.name, exact: true })
          .click()
        await dialog
          .getByRole('textbox', {
            name: 'First message (optional)',
            exact: true,
          })
          .fill(`${tool.name}: run this command: ${COMMAND}`)
        await dialog
          .getByRole('button', { name: 'More options', exact: true })
          .click()
        await dialog
          .getByRole('radio', { name: 'Ask before each action', exact: true })
          .check()
        await capture(`new-session-${tool.key}-filled`)
        await dialog.getByRole('button', { name: 'Start', exact: true }).click()
        // The launch retries the first message for up to 15 s while the tool starts.
        await expect(dialog).toBeHidden({ timeout: 30_000 })
        await page.waitForURL(/[?&]session=run(:|%3A)/)
        const session = page.url()
        // The tool asks before it runs the command; the administrator approves it
        // in the queue the sidebar opens, on the row its first message named.
        await page
          .getByRole('navigation')
          .getByRole('link', { name: /^Approvals\b/ })
          .click()
        await page
          .getByRole('row')
          .filter({ hasText: `${tool.name}: run this command` })
          .getByRole('button', { name: 'Approve', exact: true })
          .click({ timeout: 90_000 })
        await page
          .getByRole('dialog')
          .getByRole('button', { name: 'Approve', exact: true })
          .click()
        await expect(
          page.getByText('Decision recorded — approved'),
        ).toBeVisible()
        await page.goto(session)
        // No prompt contains these sentinels: only the provider's answer after the
        // approved command's own output, and after the resume, supplies them.
        const conversation = page.getByTestId('session-conversation')
        await expect(conversation).toContainText(ANSWER, { timeout: 90_000 })
        await page.getByRole('button', { name: 'Stop', exact: true }).click()
        await page
          .getByRole('dialog', { name: 'Stop this session?', exact: true })
          .getByRole('button', { name: 'Stop the session', exact: true })
          .click()
        await page
          .getByRole('button', { name: 'Resume', exact: true })
          .click({ timeout: 30_000 })
        await expect(
          page.getByRole('button', { name: 'Stop', exact: true }),
        ).toBeVisible({ timeout: 60_000 })
        await page
          .getByTestId('launcher-input')
          .fill(RESUME_PROMPT, { timeout: 30_000 })
        await page.getByTestId('composer-send').click()
        await expect(conversation).toContainText(RESUMED, { timeout: 90_000 })
        await expect(conversation).toContainText(ANSWER)
        // The first-hour steps come from the journeys table the console specs walk.
        expect(unwalked(JOURNEYS.firstHour, visited)).toEqual([])
      })
    }
    for (const [section, label] of [
      ['general', 'General'],
      ['keyboard', 'Keyboard'],
      ['signIn', 'Sign-in and security'],
      ['edition', 'Edition & modules'],
      ['tracing', 'Tracing'],
      ['signing', 'Report signing'],
      ['about', 'About'],
    ]) {
      await stage(`05-settings-${section}`, async () => {
        await visit('/settings')
        await page
          .getByRole('navigation', { name: 'Settings sections', exact: true })
          .getByRole('button', { name: label, exact: true })
          .click()
        await expect(
          page.locator('.animate-spin:visible, .animate-pulse:visible'),
        ).toHaveCount(0)
      })
    }
    await stage('06-deploy', () => visit('/deploy'))
    await stage('07-workspaces', async () => {
      await visit('/sessions')
      await page.getByTestId('sessions-list-menu').click()
      await page
        .getByRole('menuitem', { name: 'Workspaces', exact: true })
        .click()
      const panel = page.getByRole('tabpanel', {
        name: 'Workspaces',
        exact: true,
      })
      await expect(panel).toBeVisible()
      await expect(
        panel.locator(
          '.animate-spin:visible, .animate-pulse:visible, [aria-busy=true]:visible',
        ),
      ).toHaveCount(0)
    })
    for (const [name, path] of [
      ['08-sessions', '/sessions'],
      ['09-onboarding', '/onboarding'],
    ] as const) {
      await stage(name, () => visit(path))
    }
  } finally {
    for (const failure of failures) console.log(`FAIL ${failure}`)
    for (const load of slow) console.log(`SLOW ${load}`)
    console.log(
      `SLOW loads over the console's ${SLOW_MS / 1000} s target: ${slow.length}`,
    )
    console.log(
      `O1 J1-release-container: ${journeyFailures.size === 0 && completed.filter((name) => /^0[1-4]-/.test(name)).length === JOURNEY_STAGES ? 'PASS' : 'FAIL'}`,
    )
    console.log(
      `O7 J7-release-container: ${failures.size === 0 && completed.length === ALL_STAGES ? 'PASS' : 'FAIL'}`,
    )
    // Persist independently of reporters: only explicit evidence is uploaded.
    const failureList = info.outputPath('failure-list.json')
    writeFileSync(
      failureList,
      JSON.stringify(
        { release: version, completed, failures: [...failures], slow },
        null,
        2,
      ),
    )
    await info.attach('failure-list', {
      path: failureList,
      contentType: 'application/json',
    })
  }
  expect(
    [...failures],
    'all first-hour stages and surface guards must pass',
  ).toEqual([])
})
