// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { expect, test } from './fixtures/ollama'
import { spawnSync } from 'node:child_process'
import { mkdirSync, writeFileSync } from 'node:fs'
import type { BrowserContext } from '@playwright/test'

// Real product API, store, verified tool installation and session. Only the local
// model endpoint is the existing labelled stand-in; product traffic is untouched.
test.use({ screenshot: 'off', trace: 'off', video: 'off' })
let recording: BrowserContext | undefined
const visits: string[] = []
const requests: { path: string; status: number }[] = []
const takes: {
  width: number
  theme: string
  object: string
  target: string
}[] = []
test.afterEach(async () => {
  await recording?.close()
  mkdirSync('playwright-report', { recursive: true })
  writeFileSync(
    'playwright-report/palette-journey.json',
    JSON.stringify(
      { status: test.info().status, visits, requests, takes },
      null,
      2,
    ),
  )
})

test('palette opens known sessions, installed tools and settings at both widths', async ({
  page: setupPage,
  ollama,
}) => {
  let page = setupPage
  test.setTimeout(420_000)
  const token = process.env.PLAYWRIGHT_SETUP_TOKEN
  test.skip(!token, 'Requires a fresh engine and its one-time setup token')
  await page.goto('/setup')
  await page.locator('#token').fill(token!)
  await page.locator('#setup-email').fill('palette@example.com')
  await page.locator('#setup-password').fill('palette-first-hour-1273-password')
  await page.getByRole('button', { name: /create administrator/i }).click()
  await page.waitForURL('**/onboarding')
  // Recording begins after credential entry; authenticated state stays in memory.
  recording = await page
    .context()
    .browser()!
    .newContext({
      storageState: await page.context().storageState(),
      baseURL: new URL(page.url()).origin,
      recordVideo: { dir: 'playwright-report/palette-video' },
    })
  page = await recording.newPage()
  page.on('framenavigated', (frame) => {
    if (frame === page.mainFrame()) visits.push(frame.url())
  })
  page.on('response', (response) => {
    if (new URL(response.url()).pathname.startsWith('/v1/'))
      requests.push({
        path: new URL(response.url()).pathname,
        status: response.status(),
      })
  })
  await page.goto('/onboarding')
  await page
    .getByRole('link', { name: 'Add a local model', exact: true })
    .click()
  const provider = page.getByRole('dialog')
  await provider
    .getByRole('textbox', { name: /^Endpoint\b/ })
    .fill(ollama.endpoint)
  await provider
    .getByRole('button', { name: 'Add provider', exact: true })
    .click()
  await page.waitForURL('**/onboarding', { timeout: 120_000 })
  const start = page.getByRole('region', { name: 'Start a session' })
  await start
    .getByLabel('First message (optional)')
    .fill('Palette known session 1273')
  await start.getByRole('button', { name: 'Start', exact: true }).click()
  await page.waitForURL('**/sessions?**')
  await expect(
    page.getByText(ollama.response, { exact: false }).first(),
  ).toBeVisible({ timeout: 60_000 })
  const sessionURL = new URL(page.url())
  expect(sessionURL.searchParams.get('session')).toBeTruthy()
  const railTitle = page
    .getByTestId('rail-row-name')
    .first()
  await expect(railTitle).toBeAttached()
  const title = (await railTitle.textContent())?.trim()
  expect(title).toBeTruthy()
  // The launch URL names a run before discovery pairs it with the live session.
  // Use the actual rail's canonical identity, and prove its ordinary click first.
  const sessionRow = page.getByTestId('rail-row').first()
  const sessionID = await sessionRow.getAttribute('data-address')
  expect(sessionID).toBeTruthy()
  await sessionRow.click()
  await expect
    .poll(() => new URL(page.url()).searchParams.get('session'))
    .toBe(sessionID)

  await page.goto('/agent-tools')
  await page.getByRole('button', { name: 'Install', exact: true }).click()
  await page
    .getByRole('menuitem', { name: 'Install Codex', exact: true })
    .click()
  await page
    .getByRole('region', { name: 'Review installation', exact: true })
    .getByRole('button', { name: 'Install approved version', exact: true })
    .click()
  await expect(page.getByTestId('tool-codex')).toBeVisible({ timeout: 120_000 })

  for (const theme of ['light', 'dark']) {
    await page.keyboard.press('Control+k')
    await page.getByRole('dialog').getByRole('combobox').fill('Theme')
    await page
      .getByRole('dialog')
      .getByRole('option', {
        name: theme === 'light' ? 'Light' : 'Dark',
        exact: true,
      })
      .click()
    for (const width of [1280, 390]) {
      await page.setViewportSize({ width, height: 900 })
      for (const object of ['session', 'tool', 'setting'] as const) {
        await page.goto('/sessions')
        await expect(
          page.getByRole('listbox', { name: 'Work rail' }).getByRole('option').first(),
        ).toBeAttached()
        await page.keyboard.press('Control+k')
        const palette = page.getByRole('dialog')
        const query =
          object === 'session'
            ? title!
            : object === 'tool'
              ? 'Codex'
              : 'Sign-in'
        await palette.getByRole('combobox').fill(query)
        const result = palette.getByRole('option', {
          name:
            object === 'session'
              ? new RegExp(title!.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'))
              : object === 'tool'
                ? /^Codex AI tools$/
                : /Sign-in and security/,
        })
        await expect(result).toBeVisible()
        await page.screenshot({
          path: `playwright-report/palette-${theme}-${width}-${object}.png`,
        })
        await result.click()
        await expect(palette).not.toBeVisible()
        if (object === 'session') {
          await expect
            .poll(() => new URL(page.url()).searchParams.get('session'))
            .toBe(sessionID)
          await expect(
            page.getByText(ollama.response, { exact: false }).first(),
          ).toBeVisible()
        } else if (object === 'tool') {
          await expect(page).toHaveURL(/\/agent-tools#tool-codex$/)
          await expect(page.getByTestId('tool-codex')).toBeInViewport()
        } else {
          await expect(page).toHaveURL(/\/settings\?section=signIn$/)
          await expect(
            page.getByRole('heading', {
              name: 'Sign-in and security',
              exact: true,
            }),
          ).toBeVisible()
        }
        await page.screenshot({
          path: `playwright-report/target-${theme}-${width}-${object}.png`,
        })
        takes.push({ width, theme, object, target: page.url() })
      }
    }
  }

  // Provision the viewer through the product's native saved-login CLI, then use
  // its ordinary password sign-in. No seeded authentication or API interception.
  const binary = process.env.PLAYWRIGHT_CLI_BINARY
  const cliHome = process.env.PLAYWRIGHT_CLI_HOME
  expect(binary).toBeTruthy()
  expect(cliHome).toBeTruthy()
  const cli = (args: string[], input?: string) => {
    const result = spawnSync(binary!, args, {
      env: {
        PATH: process.env.PATH,
        HOME: cliHome,
        XDG_CONFIG_HOME: `${cliHome}/.config`,
      },
      input,
      encoding: 'utf8',
      timeout: 30_000,
    })
    expect(result.status, `Native CLI ${args.slice(0, 2).join(' ')}`).toBe(0)
    return result.stdout
  }
  cli(
    [
      'login',
      '--server',
      new URL(page.url()).origin,
      '--insecure',
      '--email',
      'palette@example.com',
      '--password-file',
      '-',
    ],
    'palette-first-hour-1273-password',
  )
  const tenants = JSON.parse(cli(['tenants', 'ls', '-o', 'json'])) as {
    items: { tenant_id: string }[]
  }
  expect(tenants.items.length).toBeGreaterThan(0)
  cli(
    [
      'users',
      'create',
      '--email',
      'palette-viewer@example.com',
      '--member-of',
      tenants.items[0].tenant_id,
      '--role',
      'viewer',
      '--password-file',
      '-',
    ],
    'palette-viewer-1273-password',
  )
  const viewer = await page.context().browser()!.newContext()
  try {
    const reader = await viewer.newPage()
    const toolReads: string[] = []
    reader.on('request', (request) => {
      if (new URL(request.url()).pathname.startsWith('/v1/m/agenttools/'))
        toolReads.push(new URL(request.url()).pathname)
    })
    await reader.goto(new URL('/login', page.url()).href)
    await reader.locator('#email').fill('palette-viewer@example.com')
    await reader.locator('#password').fill('palette-viewer-1273-password')
    await reader.getByRole('button', { name: 'Sign in', exact: true }).click()
    await reader.waitForURL((url) => url.pathname !== '/login')
    await reader.goto(new URL('/settings', page.url()).href)
    await expect(
      reader.getByRole('navigation', { name: 'Settings sections', exact: true }),
    ).toBeVisible()
    await reader.keyboard.press('Control+k')
    const palette = reader.getByRole('dialog')
    await palette.getByRole('combobox').fill('Sign-in')
    await expect(
      palette.getByRole('option', { name: /Sign-in and security/ }),
    ).toBeVisible()
    await palette.getByRole('combobox').fill('Codex')
    await expect(palette.getByRole('option', { name: /Codex/ })).toHaveCount(0)
    await expect(palette).toContainText('No matches')
    expect(toolReads).toEqual([])
    await reader.screenshot({
      path: 'playwright-report/palette-denied-viewer.png',
    })
  } finally {
    await viewer.close()
  }
})
