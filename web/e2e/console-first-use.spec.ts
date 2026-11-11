// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { execFileSync } from 'node:child_process'
import {
  mkdtempSync,
  realpathSync,
  rmSync,
  unlinkSync,
  writeFileSync,
} from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test, expect } from '@playwright/test'
import { consoleTenant } from './console-preferences'
import { sessionToken } from './live-session'
import { openWorkingFold } from './open-working-fold'

process.env.PLAYWRIGHT_NO_COPY_PROMPT = '1'
const selectedModel = 'claude-sonnet-5-5' // https://platform.claude.com/docs/en/models/overview
test.use({
  actionTimeout: 15_000,
  screenshot: 'off',
  trace: 'off',
  video: 'off',
})
test.describe.configure({ retries: 0 })
// The person's own project: a Git repository with one committed file.
let project = ''
test.beforeAll(() => {
  project = realpathSync(mkdtempSync(join(tmpdir(), 'first-use-project-')))
  writeFileSync(join(project, 'hello.txt'), 'first-use file contents\n')
  // No inherited GIT_* variable may point these commands at another repository.
  const env = Object.fromEntries(
    Object.entries(process.env).filter(([k]) => !k.startsWith('GIT_')),
  )
  const git = (...args: string[]) =>
    execFileSync('git', args, { cwd: project, env })
  git('init', '-q', '-b', 'main')
  git('add', 'hello.txt')
  git(
    '-c',
    'user.name=First Use',
    '-c',
    'user.email=first-use@example.invalid',
    '-c',
    'commit.gpgsign=false',
    'commit',
    '-qm',
    'Initial project',
  )
})
test.afterAll(() => rmSync(project, { recursive: true, force: true }))

test('first use: setup, choose model and folder, work, change model, stop', async ({
  page,
}) => {
  test.setTimeout(180_000)
  const token = process.env.PLAYWRIGHT_SETUP_TOKEN
  // A missing runner is a failed gate, never a successful skipped test.
  expect(
    token,
    'scripts/console-first-use.sh supplies a fresh setup token',
  ).toBeTruthy()
  await page.addInitScript(() => localStorage.setItem('olivares.lang', 'en'))
  await page.goto('/setup')
  await page.locator('#token').fill(token!)
  await page.locator('#setup-email').fill('first-use@example.invalid')
  await page.locator('#setup-password').fill('first-use-browser-password-42')
  await page.getByRole('button', { name: /create administrator/i }).click()
  await page.waitForURL('**/sessions')

  const start = page.getByTestId('work-start')
  await expect(
    start.getByRole('heading', { name: 'Start a session', exact: true }),
  ).toBeVisible()
  // First run is one screen with one next step: no empty state beside the composer, and
  // no second instruction to get a tool ready (the tool here is signed in on the server).
  await expect(page.getByText('No sessions yet')).toHaveCount(0)
  await expect(page.getByTestId('tool-claude')).toHaveCount(0)
  const headers = {
    Authorization: `Bearer ${await sessionToken(page)}`,
    'X-Olivares-Tenant': await consoleTenant(page),
  }
  for (const path of ['provider-profiles', 'runs']) {
    const before = await page.request.get(`/v1/m/sessions/${path}`, { headers })
    expect(before.status()).toBe(200)
    expect((await before.json()).items).toEqual([])
  }
  const modelPill = start.getByRole('button', { name: /^Model:/ })
  await expect(modelPill, 'own-login models before any profile').toBeEnabled()
  await modelPill.click()
  await page.getByRole('option', { name: /^Claude Sonnet 5\.5/ }).click()
  await start.getByRole('button', { name: /^Effort:/ }).click()
  await page.getByRole('menuitemradio', { name: 'High', exact: true }).click()
  // The folder menu must name personal storage without exposing its internal path.
  const folderPill = start.getByRole('button', { name: /^Folder:/ })
  await expect.soft(folderPill).toContainText('My files')
  await folderPill.click()
  await expect
    .soft(page.getByRole('menu'))
    .not.toContainText(/tenant-|\/files\/|workspace_/i)
  await page.getByRole('menuitemradio', { name: /^Another folder/ }).click()
  await start
    .getByRole('textbox', { name: 'Folder', exact: true })
    .fill(project)
  await start
    .getByRole('textbox', { name: 'First message (optional)', exact: true })
    .fill('Read hello.txt')
  const created = page.waitForResponse(
    (r) =>
      r.request().method() === 'POST' &&
      new URL(r.url()).pathname === '/v1/m/sessions/runs',
  )
  await start.getByRole('button', { name: 'Start', exact: true }).click()
  const response = await created
  expect(response.status(), response.ok() ? '' : await response.text()).toBe(
    201,
  )
  const run = await response.json()
  expect(run.model_ref).toBe(selectedModel)
  expect(run.effort).toBe('high')
  expect(
    run.workspace_ref,
    'the first session must have a working folder',
  ).toBeTruthy()
  // The fixture holds the tool's first output until the finally below, so every
  // label here comes from the launch: on 0.7 these stayed placeholders.
  const composer = page.getByTestId('work-composer')
  const header = page.locator('[data-slot="thread-header"]')
  try {
    await expect(composer).toBeVisible()
    await expect(header).toBeVisible()
    // One read of the mounted frame, so a placeholder that settles later still fails.
    const firstFrame = `${await composer.innerText()}\n${await header.innerText()}`
    expect
      .soft(firstFrame)
      .not.toMatch(
        /not reported|^Model$|tenant[-_]|run_|workspace_|\/sessions\/workspaces\//im,
      )
    // The model and its effort are one picker, named for both.
    const model = composer.getByRole('button', {
      name: 'Model: Claude Sonnet 5.5, effort High',
      exact: true,
    })
    await expect(model).toBeEnabled()
    await expect(model).toHaveText('Claude Sonnet 5.5 · High')
    await expect(
      composer.getByRole('button', {
        name: 'Access: Edit files and run commands',
        exact: true,
      }),
    ).toBeVisible()
    await expect(header).toContainText('Claude Code')
    // Reachable on the first turn: the pill opens its picker.
    await model.click()
    await expect(page.getByPlaceholder('Search models')).toBeVisible()
    await page.keyboard.press('Escape')
    expect(
      await page.getByText('Identify clients').count(),
      'observe before the first answer',
    ).toBe(0)
  } finally {
    unlinkSync(process.env.PLAYWRIGHT_FIRST_TURN_GATE!)
  }
  const saved = await page.request.get(`/v1/m/sessions/runs/${run.run_ref}`, {
    headers,
  })
  expect(saved.status()).toBe(200)
  expect((await saved.json()).model_ref).toBe(selectedModel)
  const workspace = await page.request.get(
    `/v1/m/sessions/workspaces/${run.workspace_ref}`,
    { headers },
  )
  expect(workspace.status()).toBe(200)
  const folder = (await workspace.json()).root_path as string
  expect(folder, 'the session works in the typed folder').toBe(project)
  const runs = await page.request.get('/v1/m/sessions/runs', { headers })
  expect(runs.status()).toBe(200)
  expect((await runs.json()).items).toHaveLength(1)
  await expect(page.getByTestId('session-conversation')).toContainText(
    'Identify clients',
    { timeout: 30_000 },
  )

  const details = page.getByTestId('session-context')
  if (!(await details.isVisible()))
    await page.getByTestId('context-toggle').click()
  const panel = async (name: string) => {
    const choice = details.getByRole('radio', { name, exact: true })
    if (await choice.isVisible()) await choice.click()
    else {
      await details.locator('[data-slot="pane-tabs-more"]').click()
      await page.getByRole('menuitemradio', { name, exact: true }).click()
    }
  }
  // A change to the committed file is what Git-backed Changes lists.
  writeFileSync(join(folder, 'hello.txt'), 'first-use file contents\nupdated\n')
  await panel('Changes')
  await expect(details.getByTestId('session-pane-body')).toContainText(
    'hello.txt',
    { timeout: 15_000 },
  )
  await panel('Files')
  await details.getByText('hello.txt', { exact: true }).click()
  await expect(details.getByTestId('session-pane-body')).toContainText(
    'first-use file contents',
  )
  // The terminal is the drawer under the session; opening it starts a shell.
  await page
    .getByTestId('terminal-drawer')
    .getByRole('button', { name: 'Terminal', exact: true })
    .click()
  const terminal = page.getByTestId('terminal-screen')
  await expect(terminal).toContainText('$', { timeout: 30_000 })
  await page.locator('.xterm-helper-textarea').focus()
  await page.keyboard.type("printf 'first-use-pwd='; pwd")
  await page.keyboard.press('Enter')
  // A long path wraps across terminal rows: compare the text without its breaks.
  await expect
    .poll(async () => (await terminal.textContent())?.replace(/\s+/g, ''), {
      timeout: 30_000,
    })
    .toContain(`first-use-pwd=${folder}`)

  const width = async (testId: string) =>
    Math.round((await page.getByTestId(testId).boundingBox())?.width ?? 0)
  // Each step reads the count only after the size it set has reached the
  // shell: its waiter starts before the window changes.
  const sized = () => page.waitForResponse((r) => r.url().endsWith('/size'))

  // At 1440 x 800 with Details open (the spec's 1440, where 120 columns
  // leave a margin; at 1280 an 8 px cell leaves one): the drawer lies under
  // the whole sheet, Details included, and keeps at least 120 columns. The
  // window grows from 1280 x 720, so the drawer widens and a size call comes.
  const narrowSized = sized()
  await page.setViewportSize({ width: 1440, height: 800 })
  await narrowSized
  await expect(details).toBeVisible()
  await page.locator('.xterm-helper-textarea').focus()
  await page.keyboard.type(`clear; printf 'narrow=%s\\n' "$(tput cols)"`)
  await page.keyboard.press('Enter')
  await expect(terminal).toContainText(/narrow=\d+/, { timeout: 30_000 })
  const narrow = Number(
    /narrow=(\d+)/.exec((await terminal.textContent()) ?? '')![1],
  )
  console.log(
    `At 1440 px, Details open: ${narrow} columns; drawer ${await width('terminal-drawer')} px`,
  )
  expect(narrow, 'cols at 1440 px, Details open').toBeGreaterThanOrEqual(120)

  // J7.2: at 1600 px with Details open the shell has at least 120 columns, and a web
  // address it prints opens in a new tab on a click. The drawer widens, so a
  // size call comes.
  const wideSized = sized()
  await page.setViewportSize({ width: 1600, height: 900 })
  await wideSized
  await expect(details).toBeVisible()
  await page.locator('.xterm-helper-textarea').focus()
  await page.keyboard.type(
    `clear; printf 'https://example.invalid/j7\\nsize=%s,%s\\n' "$(tput lines)" "$(tput cols)"`,
  )
  await page.keyboard.press('Enter')
  await expect(terminal).toContainText(/size=\d+,\d+/, { timeout: 30_000 })
  const [rows, cols] = /size=(\d+),(\d+)/
    .exec((await terminal.textContent()) ?? '')!
    .slice(1)
    .map(Number)
  console.log(
    `J7.2 at 1600 px: ${cols} columns, ${rows} rows; drawer ${await width('terminal-drawer')} px, details ${await width('session-context')} px`,
  )
  expect(cols, 'tput cols at 1600 px with Details open').toBeGreaterThanOrEqual(
    120,
  )
  await page
    .context()
    .route('https://example.invalid/**', (r) => r.fulfill({ body: 'j7' }))
  const cells = (await page.locator('.xterm-screen').boundingBox())!
  const at = {
    x: cells.x + (cells.width / cols) * 5.5,
    y: cells.y + (cells.height / rows) * 0.5,
  }
  await page.mouse.move(at.x, at.y)
  const opened = page.context().waitForEvent('page')
  await page.mouse.click(at.x, at.y)
  const tab = await opened
  await expect.poll(() => tab.url()).toBe('https://example.invalid/j7')
  await tab.close()

  await page
    .getByRole('button', { name: 'Close Terminal 1', exact: true })
    .click()
  await page
    .getByRole('dialog', { name: 'Close Terminal 1?', exact: true })
    .getByRole('button', { name: 'Close terminal', exact: true })
    .click()

  await composer
    .getByRole('button', {
      name: 'Model: Claude Sonnet 5.5, effort High',
      exact: true,
    })
    .click()
  await page.getByRole('option', { name: /^Claude Opus 5\.5/ }).click()
  // A running session keeps its model; a changed choice offers a new session.
  await expect(page.getByText(/This session keeps/)).toBeVisible()
  await expect(page.getByTestId('composer-new-with-choice')).toBeVisible()
  await page.keyboard.press('Escape')
  await page.locator('[data-slot="sidebar"] a[data-place="sessions"]').click()
  await openWorkingFold(page)
  const listed = page
    .getByTestId('rail-row')
    .filter({ hasText: 'Read hello.txt' })
  await expect(listed).toBeVisible()
  await listed.click()
  await page.getByRole('button', { name: 'End session', exact: true }).click()
  await page
    .getByRole('dialog', { name: 'End this session?', exact: true })
    .getByRole('button', { name: 'End the session', exact: true })
    .click()
  await expect(
    page.getByRole('button', { name: 'Resume', exact: true }),
  ).toBeVisible()
  console.log('First-use journey reached Stop/Resume.')
})
