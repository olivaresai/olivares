// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import AxeBuilder from '@axe-core/playwright'
import { expect, test, type Page } from '@playwright/test'
import { execFileSync } from 'node:child_process'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { sessionToken } from './live-session'

// The terminal in the browser, on a real engine: a shell opens in a registered folder,
// runs git there, cannot write outside it, is recorded in the audit ledger, survives a
// reload, and is refused to a person without the right to write the folder. The session
// pane mounts the same component; its tab is covered by the console's component tests,
// because a session needs a vendor tool signed in.
process.env.PLAYWRIGHT_NO_COPY_PROMPT = '1'

const setupToken = process.env.PLAYWRIGHT_SETUP_TOKEN ?? ''
const password = 'terminal-journey-password-42'

test.describe.configure({ retries: 0 })
test.use({ screenshot: 'off', trace: 'off', video: 'off' })

const screen = (page: Page) => page.getByTestId('terminal-screen')

async function typeLine(page: Page, line: string) {
  await page.locator('.xterm-helper-textarea').focus()
  await page.keyboard.type(line)
  await page.keyboard.press('Enter')
}

async function openFolderTerminal(page: Page) {
  await page.goto('/sessions?tab=workspaces')
  await page.getByRole('button', { name: 'Browse files' }).first().click()
  await page.getByRole('tab', { name: 'Terminal' }).click()
}

test('a terminal in a folder: git status, confined, recorded, resumed, closed, refused', async ({
  page,
  browser,
}, testInfo) => {
  test.skip(!setupToken, 'Requires a fresh engine and PLAYWRIGHT_SETUP_TOKEN')
  test.setTimeout(240_000)
  const captures =
    process.env.E2E_CAPTURE_DIR ?? testInfo.outputPath('captures')
  fs.mkdirSync(captures, { recursive: true })

  // A real folder with a Git repository and one untracked file, and a place outside it.
  const folder = fs.mkdtempSync(path.join(os.tmpdir(), 'olv-terminal-folder-'))
  const outside = fs.mkdtempSync(path.join(os.tmpdir(), 'olv-terminal-out-'))
  execFileSync('git', ['init', '-q', '-b', 'main'], { cwd: folder })
  fs.writeFileSync(path.join(folder, 'hello.txt'), 'hello\n')

  await page.setViewportSize({ width: 1280, height: 900 })
  await page.goto('/setup')
  await page.locator('#token').fill(setupToken)
  await page.locator('#setup-email').fill('admin@example.com')
  await page.locator('#setup-password').fill(password)
  const setupResponse = page.waitForResponse(
    (r) =>
      new URL(r.url()).pathname === '/v1/setup' &&
      r.request().method() === 'POST',
  )
  await page.getByRole('button', { name: /create administrator/i }).click()
  const setup = await setupResponse
  if (!setup.ok()) {
    await page.locator('#token').fill('')
    await page.locator('#setup-password').fill('')
  }
  expect(setup.ok(), `Setup HTTP status: ${setup.status()}`).toBe(true)
  await page.waitForURL('**/onboarding', { timeout: 15_000 })

  const token = await sessionToken(page)
  const who = await page.request.get('/v1/auth/whoami', {
    headers: { Authorization: `Bearer ${token}` },
  })
  expect(who.ok()).toBe(true)
  const tenant = (await who.json()).grants[0].tenant as string
  const headers = {
    Authorization: `Bearer ${token}`,
    'X-Olivares-Tenant': tenant,
  }
  const registered = await page.request.post('/v1/m/sessions/workspaces', {
    headers,
    data: { name: 'Terminal folder', root_path: folder, mount_mode: 'rw' },
  })
  expect(registered.ok(), `register folder: HTTP ${registered.status()}`).toBe(
    true,
  )
  const ref = (await registered.json()).workspace_ref as string
  const terminals = `/v1/m/sessions/workspaces/${encodeURIComponent(ref)}/terminals`

  // OPENING THE TAB STARTS NOTHING: the shell starts when a person asks.
  const starts: string[] = []
  page.on('request', (r) => {
    if (r.method() === 'POST' && new URL(r.url()).pathname === terminals)
      starts.push(r.url())
  })
  await openFolderTerminal(page)
  await expect(
    page.getByRole('button', { name: 'Open terminal' }),
  ).toBeVisible()
  expect(starts).toHaveLength(0)

  // Open, and run git status in the folder.
  await page.getByRole('button', { name: 'Open terminal' }).click()
  await expect(screen(page)).toContainText(folder, { timeout: 30_000 })
  expect(starts).toHaveLength(1)
  await typeLine(page, 'git status')
  await expect(screen(page)).toContainText('Untracked files', {
    timeout: 20_000,
  })
  await expect(screen(page)).toContainText('hello.txt')

  // The same confinement as a session: the folder is writable, nothing outside it is.
  await typeLine(page, 'touch inside-ok; echo inside=$?')
  await expect(screen(page)).toContainText('inside=0', { timeout: 20_000 })
  expect(fs.existsSync(path.join(folder, 'inside-ok'))).toBe(true)
  await typeLine(page, `touch ${outside}/escaped; echo outside=$?`)
  await expect(screen(page)).toContainText('Permission denied', {
    timeout: 20_000,
  })
  expect(fs.existsSync(path.join(outside, 'escaped'))).toBe(false)

  await page.screenshot({
    path: path.join(captures, 'terminal-1280-light.png'),
  })
  const violations = (
    await new AxeBuilder({ page })
      .include('[data-testid="session-terminal"]')
      .analyze()
  ).violations.filter((v) => v.impact === 'serious' || v.impact === 'critical')
  expect(violations.map((v) => `${v.id}: ${v.help}`)).toEqual([])

  // Every start and every command line is in the audit ledger: one start and the three
  // lines typed above. (The ledger seals each line's text in the event's metadata; the list
  // names the events, the offline archive carries the text.)
  const audit = await page.request.get('/v1/audit', {
    headers,
    params: { limit: '500' },
  })
  expect(audit.ok()).toBe(true)
  const actions = ((await audit.json()).items as { action: string }[]).map(
    (event) => event.action,
  )
  expect(actions.filter((a) => a === 'sessions.terminal.start')).toHaveLength(1)
  expect(actions.filter((a) => a === 'sessions.terminal.command')).toHaveLength(
    3,
  )
  const exported = await page.request.get('/v1/audit/export', { headers })
  console.log(
    `audit export: HTTP ${exported.status()}, carries the typed line: ${(await exported.text()).includes('git status')}`,
  )

  // A reload finds the same shell: its earlier output is still on the screen.
  await page.reload()
  await page.getByRole('button', { name: 'Browse files' }).first().click()
  await page.getByRole('tab', { name: 'Terminal' }).click()
  await expect(screen(page)).toContainText('Untracked files', {
    timeout: 30_000,
  })
  expect(starts).toHaveLength(1)

  // Close ends the shell.
  await page.getByRole('button', { name: 'Close terminal' }).click()
  await expect(
    page.getByRole('button', { name: 'Open terminal' }),
  ).toBeVisible()

  // A person without the right to write the folder is refused, and nothing starts.
  const viewerMail = 'viewer@example.com'
  const created = await page.request.post('/v1/users', {
    headers,
    data: {
      email: viewerMail,
      password,
      tenant,
      role: 'viewer',
    },
  })
  expect(created.ok(), `create viewer: HTTP ${created.status()}`).toBe(true)
  const login = await page.request.post('/v1/auth/login', {
    data: { email: viewerMail, password },
  })
  expect(login.ok()).toBe(true)
  const viewerToken = (await login.json()).token as string
  const refused = await page.request.post(terminals, {
    headers: {
      Authorization: `Bearer ${viewerToken}`,
      'X-Olivares-Tenant': tenant,
    },
    data: { rows: 24, cols: 80 },
  })
  expect([403, 404]).toContain(refused.status())

  // At 390 px, on a touch screen: the terminal fits and the keys a touch keyboard lacks are there.
  const phone = await browser.newContext({
    baseURL: testInfo.project.use.baseURL,
    storageState: await page.context().storageState(),
    viewport: { width: 390, height: 844 },
    hasTouch: true,
    isMobile: true,
  })
  const small = await phone.newPage()
  await openFolderTerminal(small)
  await small.getByRole('button', { name: 'Open terminal' }).click()
  await expect(screen(small)).toContainText(folder.slice(-12), {
    timeout: 30_000,
  })
  await expect(small.getByTestId('terminal-keys')).toBeVisible()
  await small.getByRole('button', { name: 'Ctrl+C' }).click()
  await typeLine(small, 'echo narrow=$((6*7))')
  await expect(screen(small)).toContainText('narrow=42', { timeout: 20_000 })
  const overflow = await small.evaluate(
    () =>
      document.documentElement.scrollWidth -
      document.documentElement.clientWidth,
  )
  expect(overflow).toBeLessThanOrEqual(0)
  await small.screenshot({
    path: path.join(captures, 'terminal-390-light.png'),
  })
  await small.emulateMedia({ colorScheme: 'dark' })
  await small.waitForTimeout(500) // the theme's own transition
  await small.screenshot({ path: path.join(captures, 'terminal-390-dark.png') })
  const darkViolations = (
    await new AxeBuilder({ page: small })
      .include('[data-testid="session-terminal"]')
      .analyze()
  ).violations.filter((v) => v.impact === 'serious' || v.impact === 'critical')
  expect(darkViolations.map((v) => `${v.id}: ${v.help}`)).toEqual([])
  await small.getByRole('button', { name: 'Close terminal' }).click()
  await phone.close()

  fs.rmSync(folder, { recursive: true, force: true })
  fs.rmSync(outside, { recursive: true, force: true })
})
