// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Control in the 0.5 map (#3389), in Chromium: Usage & cost is one click from the sidebar
// and leads with the account nearest its limit; the palette names each page by its place,
// never by a retired area; Reach keeps graph or table in the address. Each page passes axe,
// and its frame at 1280 px is kept as the review capture; the new Usage page is also kept
// dark and at 390 px. The clock is held on a fixed morning so each account's reset and
// today's sessions read as the figures a person would see.
import AxeBuilder from '@axe-core/playwright'
import { expect, test, type Page } from '@playwright/test'
import { fixtureFor } from './fixtures'

/** The morning the captures are read at; the fixtures below are dated from it. */
const NOW = new Date('2026-10-28T10:00:00Z')

const PROVIDERS = {
  providers: [
    {
      instance: 'claude/default',
      driver: 'claude',
      default: true,
      config_dir: '',
      state: 'ready',
      installed: true,
      plan: 'Max',
      limits: [
        { label: '5-hour', percent: 42, resets_at: '2026-10-28T12:18:00Z' },
        { label: 'Weekly', percent: 31, resets_at: '2026-10-31T00:00:00Z' },
      ],
      models: [],
      checked_at: '2026-10-28T09:58:00Z',
      source: 'claude /status',
    },
    {
      instance: 'codex/default',
      driver: 'codex',
      default: true,
      config_dir: '',
      state: 'ready',
      installed: true,
      plan: 'Team',
      limits: [
        { label: '5-hour', percent: 58, resets_at: '2026-10-28T10:41:00Z' },
        { label: 'Weekly', percent: 37, resets_at: '2026-11-01T00:00:00Z' },
      ],
      models: [],
      checked_at: '2026-10-28T09:58:00Z',
      source: 'codex /status',
    },
  ],
}

/** Two sessions working now, as the engine observes them (no launch record: each is its own row). */
const LIVE = {
  items: [
    {
      session_ref: 'sess-checkout',
      attribution: 'legacy',
      cc_state: 'active',
      engine: 'claude',
      input_tokens: 92_000,
      output_tokens: 21_000,
      cost_micro_usd: 3_120_000,
      event_count: 40,
      tool_call_count: 12,
      first_event_at: '2026-10-28T08:10:00Z',
      last_event_at: '2026-10-28T09:58:00Z',
      duration_seconds: 6480,
      critical: false,
    },
    {
      session_ref: 'sess-docs',
      attribution: 'legacy',
      cc_state: 'idle',
      engine: 'codex',
      input_tokens: 41_000,
      output_tokens: 9_000,
      cost_micro_usd: 1_450_000,
      event_count: 18,
      tool_call_count: 5,
      first_event_at: '2026-10-28T09:00:00Z',
      last_event_at: '2026-10-28T09:40:00Z',
      duration_seconds: 2400,
      critical: false,
    },
  ],
  has_more: false,
}

/** Two runs that ended this morning (one priced without tokens) and one from yesterday. */
const RUNS = {
  items: [
    {
      run_ref: 'run-tests',
      tenant_id: 't-demo',
      state: 'stopped',
      name: 'Fix the flaky login test',
      provider_driver: 'claude',
      input_tokens: 38_000,
      output_tokens: 7_500,
      cost_micro_usd: 1_180_000,
      created_at: '2026-10-28T07:30:00Z',
      last_activity_at: '2026-10-28T08:05:00Z',
    },
    {
      run_ref: 'run-report',
      tenant_id: 't-demo',
      state: 'stopped',
      name: 'Nightly report',
      provider_driver: 'codex',
      cost_micro_usd: 280_000,
      created_at: '2026-10-28T06:00:00Z',
      last_activity_at: '2026-10-28T06:20:00Z',
    },
    {
      run_ref: 'run-yesterday',
      tenant_id: 't-demo',
      state: 'stopped',
      name: 'Release notes',
      provider_driver: 'claude',
      input_tokens: 400_000,
      output_tokens: 90_000,
      cost_micro_usd: 9_000_000,
      created_at: '2026-10-27T14:00:00Z',
      last_activity_at: '2026-10-27T15:00:00Z',
    },
  ],
  has_more: false,
}

const GRAPH = {
  edges: [
    {
      id: 'e1',
      origin_kind: 'agent',
      origin_id: 'a1',
      origin_ref: 'claude-code-1',
      resource_kind: 'resource',
      resource_ref: 'storefront-web',
    },
    {
      id: 'e2',
      origin_kind: 'agent',
      origin_id: 'a1',
      origin_ref: 'claude-code-1',
      resource_kind: 'mcp_server',
      resource_ref: 'github',
    },
  ],
  has_more: false,
}

async function mockApi(page: Page, theme: 'light' | 'dark' = 'light') {
  await page.route('**/v1/**', async (route) => {
    const path = new URL(route.request().url()).pathname
    if (path.endsWith('/stream'))
      return route.fulfill({
        contentType: 'text/event-stream',
        body: ': connected\n\n',
      })
    const value =
      path === '/v1/m/agenttools/providers'
        ? PROVIDERS
        : path === '/v1/m/sessions/live'
          ? LIVE
          : path === '/v1/m/sessions/runs'
            ? RUNS
            : path === '/v1/m/accessmap/graph'
              ? GRAPH
              : (fixtureFor(path) ?? { items: [], has_more: false })
    await route.fulfill({
      contentType: 'application/json',
      body: JSON.stringify(value),
    })
  })
  await page.addInitScript(
    ({ theme }) => {
      localStorage.setItem(
        'olivares.tenant',
        JSON.stringify({ state: { activeTenant: 't-demo' }, version: 0 }),
      )
      localStorage.setItem('olivares.lang', 'en')
      localStorage.setItem('olivares.theme', theme)
    },
    { theme },
  )
  await page.clock.setFixedTime(NOW)
}

/** What Usage & cost says on the fixtures: the nearest limit first, then today's figures. */
async function expectUsageFigures(page: Page) {
  const accounts = page.getByRole('region', { name: 'Accounts' })
  const rows = accounts.getByRole('listitem')
  await expect(rows).toHaveCount(2)
  await expect(rows.nth(0)).toContainText('Codex · Team')
  await expect(rows.nth(0)).toContainText('58% 5-hour · 37% weekly')
  await expect(rows.nth(0)).toContainText('resets in 41 minutes')
  await expect(rows.nth(1)).toContainText('resets in 2 hours')
  const today = page.getByRole('region', { name: 'Today' })
  // Yesterday's run is not today's; the run that reported a cost and no tokens adds its
  // cost and is said apart, never as 0 tokens.
  await expect(today.getByTestId('usage-today')).toHaveText(
    '4 sessions · 171K input · 37,500 output · $6.03',
  )
  await expect(today).toContainText('1 session reported no tokens.')
}

async function axeClean(page: Page, where: string) {
  const axe = await new AxeBuilder({ page })
    .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa'])
    .analyze()
  expect(axe.violations, `axe violations ${where}`).toEqual([])
}

// Today is the browser's local day: UTC, so the fixtures' dates are the day the page counts.
test.use({ viewport: { width: 1280, height: 800 }, timezoneId: 'UTC' })

test('Usage & cost is one click from the sidebar and leads with the account nearest its limit', async ({
  page,
}, testInfo) => {
  await mockApi(page)
  await page.goto('/')
  const nav = page.getByRole('navigation', { name: 'Main navigation' })
  await nav.getByRole('link', { name: /^Usage & cost/ }).click()
  await expect(page).toHaveURL(/\/usage$/)
  await expectUsageFigures(page)
  await axeClean(page, '/usage')
  await page.screenshot({
    path: testInfo.outputPath('usage-desktop-light-1280.png'),
  })
})

for (const [theme, width] of [
  ['dark', 1280],
  ['light', 390],
] as const)
  test(`Usage & cost in ${theme} at ${width} px says the same figures, axe 0`, async ({
    page,
  }, testInfo) => {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 800 })
    await mockApi(page, theme)
    await page.goto('/usage')
    await expectUsageFigures(page)
    await axeClean(page, `/usage ${theme} ${width}`)
    await page.screenshot({
      path: testInfo.outputPath(
        `usage-${width === 390 ? 'phone' : 'desktop'}-${theme}-${width}.png`,
      ),
      fullPage: true,
    })
  })

test('the palette names each page by its place and offers no retired area', async ({
  page,
}, testInfo) => {
  await mockApi(page)
  await page.goto('/')
  // The search button the top bar shows (its ⌘K key opens the same palette).
  await page
    .locator('header[data-slot="topbar"] button[aria-keyshortcuts]')
    .click()
  await page.getByRole('combobox').fill('oversight')
  const usage = page.getByRole('option', { name: /^Usage/ })
  await expect(usage).toContainText('Control › Usage & cost')
  await expect(page.getByRole('option', { name: /^Oversight/ })).toHaveCount(0)
  await page.screenshot({
    path: testInfo.outputPath('palette-oversight-light-1280.png'),
  })
})

test('Reach opens the table a link names and writes the choice back', async ({
  page,
}, testInfo) => {
  await mockApi(page)
  await page.goto('/reach?view=table')
  await expect(page.getByRole('table')).toBeVisible()
  await page.getByRole('button', { name: 'Graph' }).click()
  await expect(page).toHaveURL(/\/reach$/)
  await page.getByRole('button', { name: 'Table' }).click()
  await expect(page).toHaveURL(/\/reach\?view=table$/)
  await axeClean(page, '/reach?view=table')
  await page.screenshot({
    path: testInfo.outputPath('reach-table-light-1280.png'),
  })
})
