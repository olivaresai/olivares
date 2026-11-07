// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { expect, test } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { fixtureFor } from './fixtures'

const refs = [
  '019aaa00-0000-7000-8000-000000000001',
  '019aaa00-0000-7000-8000-000000000002',
]

test('compare racing sessions at desktop and phone widths', async ({
  page,
  baseURL,
}) => {
  const origin = new URL(baseURL!).origin
  await page.route('**/*', async (route) => {
    const url = new URL(route.request().url())
    if (url.origin !== origin) return route.abort()
    if (!url.pathname.startsWith('/v1/')) return route.continue()
    const match = /^\/v1\/m\/sessions\/runs\/([^/]+)(\/diff(?:\/file)?)?$/.exec(
      url.pathname,
    )
    if (match && refs.includes(match[1])) {
      const index = refs.indexOf(match[1])
      return route.fulfill({
        json:
          match[2] === '/diff/file'
            ? {
                path: 'greeting.txt',
                status: 'modified',
                original: 'Hello\n',
                modified:
                  index === 0 ? 'Hello, everyone\n' : 'Welcome, everyone\n',
                binary: false,
                truncated: false,
              }
            : match[2] === '/diff'
              ? {
                  branch: `olivares/choice-${index}`,
                  base: 'a'.repeat(40),
                  head: 'b'.repeat(40),
                  files: [{ path: 'greeting.txt', status: 'modified' }],
                  truncated: false,
                }
              : {
                  run_ref: refs[index],
                  name: index === 0 ? 'Claude Code' : 'Codex',
                  state: 'stopped',
                  workspace_ref: 'ws-race',
                  authz_workspace_id: 'workspace-race',
                  provider_driver: index === 0 ? 'claude' : 'codex',
                  worktree_branch: `olivares/choice-${index}`,
                  cost_micro_usd: index === 0 ? 250000 : 175000,
                },
      })
    }
    return route.fulfill({
      json: fixtureFor(url.pathname) ?? { items: [], has_more: false },
    })
  })
  await page.addInitScript(() => {
    localStorage.setItem('olivares.lang', 'en')
    localStorage.setItem('olivares.theme', 'light')
    localStorage.setItem(
      'olivares.tenant',
      JSON.stringify({ state: { activeTenant: 't-demo' }, version: 0 }),
    )
  })
  const shots = process.env.E2E_CAPTURE_DIR ?? 'test-results/race'
  mkdirSync(shots, { recursive: true })
  for (const [width, theme] of [
    [1280, 'light'],
    [390, 'dark'],
  ] as const) {
    await page.setViewportSize({ width, height: 900 })
    await page.emulateMedia({ colorScheme: theme, reducedMotion: 'reduce' })
    await page.goto(`/agentops?race=${refs.join(',')}`)
    await page.evaluate((value) => {
      localStorage.setItem('olivares.theme', value)
      document.documentElement.dataset.theme = value
      document.documentElement.classList.toggle('dark', value === 'dark')
    }, theme)
    await expect(
      page.getByRole('heading', { name: 'Compare sessions' }),
    ).toBeVisible()
    const choices = page.getByTestId('race-choice')
    await expect(choices).toHaveCount(2)
    for (const choice of await choices.all())
      await choice.getByRole('button', { name: 'greeting.txt' }).click()
    await expect(
      page.getByText('Hello, everyone', { exact: false }).first(),
    ).toBeVisible()
    // File clicks may scroll the console's internal pane. Capture the screen
    // from its heading, rather than the last control the fixture clicked.
    await page.evaluate(() => {
      for (const element of document.querySelectorAll('*')) {
        if (element instanceof HTMLElement && element.scrollTop > 0)
          element.scrollTop = 0
      }
    })
    await page.screenshot({
      path: join(shots, `race-${width}-${theme}.png`),
      fullPage: true,
    })
    const violations = (
      await new AxeBuilder({ page })
        .include('[data-testid="session-race"]')
        .analyze()
    ).violations
    expect(violations).toEqual([])
    const bounds = await choices.evaluateAll((elements) =>
      elements.map((element) => ({
        left: element.getBoundingClientRect().left,
        top: element.getBoundingClientRect().top,
      })),
    )
    if (width === 1280) expect(bounds[0].top).toBe(bounds[1].top)
    else expect(bounds[0].top).toBeLessThan(bounds[1].top)
  }
  await page.setViewportSize({ width: 1280, height: 900 })
  await page.emulateMedia({ colorScheme: 'light', reducedMotion: 'reduce' })
  await page.goto(`/agentops?race=${refs.join(',')}`)
  await page.getByRole('button', { name: 'Keep this one' }).first().click()
  await expect(page.getByRole('dialog')).toContainText('permanently discard')
  await page.screenshot({
    path: join(shots, 'race-confirm-1280-light.png'),
    fullPage: true,
  })
  expect(
    (await new AxeBuilder({ page }).include('[role="dialog"]').analyze())
      .violations,
  ).toEqual([])
})
