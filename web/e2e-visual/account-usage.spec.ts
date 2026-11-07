// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
// The usage of each provider account where the console names the account: its share of the
// week and of five hours on the AI tools page (rows and the account list), the New session
// picker and Details, with an account at its limit marked before Start; every window by name
// only on Usage, behind the account's row. Accounts and usage are fixtures; the console is
// the real one.
import AxeBuilder from '@axe-core/playwright'
import { expect, test } from '@playwright/test'
import { fixtureFor, serverInfo } from './fixtures'

const shots = process.env.SHOTS_DIR

const window = (name: string, utilization: number, resets_at?: string) => ({
  window: name,
  label: name,
  utilization,
  observed_at: '2026-10-29T10:00:00Z',
  ...(resets_at ? { resets_at } : {}),
})
const account = (
  ref: string,
  name: string,
  driver: string,
  usage?: object[],
) => ({
  account_ref: ref,
  name,
  driver,
  environment_ref: 'xenv_1',
  state: 'active',
  home_mode: 'managed',
  home_generation: 1,
  home_relative: '',
  isolation_level: 'shared',
  auth_source: 'provider_account_home',
  identity: '',
  identity_source: 'none',
  ...(usage ? { usage } : {}),
  created_at: '2026-10-01T10:00:00Z',
  updated_at: '2026-10-01T10:00:00Z',
})
const accounts = [
  account('ppf_work', 'work', 'claude', [
    window('five_hour', 41),
    window('seven_day', 17),
    { ...window('monthly_spend', 85), label: 'Monthly spend' },
    { ...window('seven_day:fable', 0), label: 'Weekly (Fable)' },
    {
      window: 'credits',
      label: 'Credits',
      credit: { balance: '12.50', unlimited: false, has_credits: true },
      observed_at: '2026-10-29T10:00:00Z',
    },
  ]),
  account('ppf_personal', 'personal', 'claude', [
    window('five_hour', 100, '2030-01-01T10:00:00Z'),
    window('seven_day', 62),
  ]),
  account('ppf_review', 'review', 'codex'),
]

const usageRun = {
  run_ref: 'run_usage',
  name: 'Monthly account check',
  transport: 'stream-json',
  permission_mode: 'default',
  isolation: 'native',
  state: 'idle',
  last_event_seq: 0,
  pep_provisioned: true,
  record_io: true,
  critical: false,
  provider_driver: 'claude',
  provider_profile_ref: 'ppf_work',
  workspace_path: '/project',
}

test.beforeEach(async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 })
  await page.addInitScript(() => {
    localStorage.setItem(
      'olivares.tenant',
      JSON.stringify({ state: { activeTenant: 't-demo' }, version: 0 }),
    )
    localStorage.setItem('olivares.lang', 'en')
    localStorage.setItem('olivares.theme', 'light')
  })
  await page.route('**/v1/**', async (route) => {
    const path = new URL(route.request().url()).pathname
    if (path.endsWith('/stream'))
      return route.fulfill({
        contentType: 'text/event-stream',
        body: ': connected\n\n',
      })
    if (path === '/v1/m/sessions/runs')
      return route.fulfill({ json: { items: [usageRun], has_more: false } })
    if (path === '/v1/m/sessions/runs/run_usage')
      return route.fulfill({ json: usageRun })
    // Details reads the run's activity; the engine always sends its lists.
    if (path === '/v1/m/sessions/runs/run_usage/activity')
      return route.fulfill({
        json: {
          driver: 'claude',
          transcript: 'read',
          context_tokens: 0,
          compactions: [],
          agents: [],
          tasks: [],
          files: [],
          todos: [],
          skills: [],
          mcp_servers: [],
        },
      })
    if (path === '/v1/m/sessions/provider-accounts/ppf_work')
      return route.fulfill({ json: accounts[0] })
    // Details reads the run's own profile; a named one is an account.
    if (path === '/v1/m/sessions/provider-profiles/ppf_work')
      return route.fulfill({
        json: {
          profile_ref: 'ppf_work',
          driver: 'claude',
          environment_ref: 'xenv_1',
          account_name: 'work',
          state: 'active',
          local_environment: false,
        },
      })
    // AI tools lists the local model servers; the engine always answers with the list.
    if (path === '/v1/m/agenttools/model-servers')
      return route.fulfill({ json: { servers: [] } })
    const json =
      path === '/v1/m/sessions/provider-profiles/readiness'
        ? {
            tools: [
              { driver: 'claude', ready: true, reason: 'own_login' },
              { driver: 'codex', ready: true, reason: 'own_login' },
            ],
          }
        : path === '/v1/m/sessions/provider-accounts'
          ? { items: accounts, has_more: false }
          : path === '/v1/m/agenttools/sign-in'
            ? { driver: 'claude', installed: true, signed_in: true }
            : path === '/v1/m/agenttools/providers'
              ? {
                  providers: ['claude', 'codex'].map((driver) => ({
                    instance: driver,
                    driver,
                    default: true,
                    config_dir: `/home/op/.${driver}`,
                    state: 'ready',
                    installed: true,
                    version: `${driver}-cli 1.2.3`,
                    // The tool's own login reports its windows by the engine's label alone.
                    limits:
                      driver === 'claude'
                        ? [
                            { label: '5-hour', percent: 37 },
                            { label: 'Weekly', percent: 77 },
                            { label: 'Weekly (Fable)', percent: 5 },
                          ]
                        : [],
                    models: [],
                    checked_at: '2026-10-29T10:00:00Z',
                    source: `${driver} --version`,
                  })),
                }
              : path === '/v1/server-info'
                ? serverInfo
                : (fixtureFor(path) ?? { items: [], has_more: false })
    await route.fulfill({ json })
  })
})

test('the AI tools page shows each account its use, and unknown as unknown', async ({
  page,
}) => {
  await page.goto('/agent-tools')
  await expect(page.getByTestId('profile-work')).toContainText(
    '17% of the week · 41% of 5 hours',
  )
  await expect(page.getByTestId('profile-work')).not.toContainText(
    /Fable|Monthly|Credits/,
  )
  await expect(page.getByTestId('profile-personal')).toContainText(
    '62% of the week · 100% of 5 hours',
  )
  await expect(page.getByTestId('profile-review')).not.toContainText('%')
  if (shots) await page.screenshot({ path: `${shots}/tools-1280-light.png` })
  // The account list, folded under "All accounts", says it too.
  await page.getByText('All accounts: details, labels and bindings').click()
  const row = page.getByRole('row', { name: /personal/ })
  await expect(row).toContainText('62% of the week · 100% of 5 hours')
  await expect(page.getByRole('row', { name: /review/ })).toContainText(
    'Usage unknown',
  )
  await row.scrollIntoViewIfNeeded()
  if (shots) await page.screenshot({ path: `${shots}/accounts-1280-light.png` })
  const report = await new AxeBuilder({ page })
    .disableRules(['color-contrast'])
    .analyze()
  expect(report.violations).toEqual([])
  // The tool's own login, in its Install details, says the same share and no window name.
  await page.getByRole('button', { name: 'Options for Claude Code' }).click()
  await page.getByRole('menuitem', { name: 'Install details' }).click()
  const login = page.getByRole('article', { name: /Claude Code/ })
  await expect(login.getByTestId('account-usage')).toHaveText(
    '77% of the week · 37% of 5 hours',
  )
  await expect(login).not.toContainText('Fable')
  if (shots)
    await page.screenshot({ path: `${shots}/install-details-1280-light.png` })
})

test('the New session picker shows the use and marks an account at its limit before Start', async ({
  page,
}) => {
  await page.goto('/sessions')
  await page
    .locator('#work-pane-rail')
    .getByRole('button', { name: 'New session', exact: true })
    .click()
  await page.getByRole('button', { name: /^Tool and account: / }).click()
  await expect(page.getByRole('menuitemradio', { name: /work/ })).toContainText(
    '17% of the week · 41% of 5 hours',
  )
  await expect(
    page.getByRole('menuitemradio', { name: /work/ }),
  ).not.toContainText(/Fable|Monthly|Credits/)
  if (shots) await page.screenshot({ path: `${shots}/picker-1280-light.png` })
  // An account at its limit is marked in the list, with when it resets, and is not offered.
  const atLimit = page.getByRole('menuitemradio', { name: /personal/ })
  await expect(atLimit).toContainText(/5-hour limit reached, resets /)
  await expect(atLimit).toHaveAttribute('aria-disabled', 'true')
  if (shots)
    await page.screenshot({ path: `${shots}/picker-at-limit-1280-light.png` })
  const report = await new AxeBuilder({ page })
    .include('[role="menu"]')
    .disableRules(['color-contrast'])
    .analyze()
  expect(report.violations).toEqual([])
})

test("session Details says its bound account's share in the same words", async ({
  page,
}) => {
  await page.goto('/sessions?session=run%3Arun_usage')
  // Details is closed by default; its facts fold the account's usage.
  await page.getByRole('button', { name: 'Session details' }).click()
  await page.getByRole('radio', { name: 'Details' }).click()
  await page.getByTestId('session-facts-more').locator('summary').click()
  await expect(page.getByTestId('session-account-usage')).toHaveText(
    'Account usage17% of the week · 41% of 5 hours',
  )
  if (shots) await page.screenshot({ path: `${shots}/details-1280-light.png` })
})

test("Usage opens an account's row to every window by name, with when it resets", async ({
  page,
}) => {
  await page.goto('/usage')
  const work = page.locator('li[data-account="ppf_work"]')
  await expect(work).toContainText('17% of the week · 41% of 5 hours')
  await expect(work.getByText('Fable only 0%')).toBeHidden()
  await work.locator('summary').click()
  for (const text of [
    '5-hour 41%',
    'Weekly 17%',
    'Monthly spend 85%',
    'Fable only 0%',
    'Credits 12.50',
  ])
    await expect(work.getByText(text, { exact: true })).toBeVisible()
  const personal = page.locator('li[data-account="ppf_personal"]')
  await personal.locator('summary').click()
  await expect(personal.getByRole('listitem').first()).toContainText(
    /^5-hour 100%Resets /,
  )
  if (shots) await page.screenshot({ path: `${shots}/usage-1280-light.png` })
  const report = await new AxeBuilder({ page })
    .disableRules(['color-contrast'])
    .analyze()
  expect(report.violations).toEqual([])
})

for (const [width, theme] of [
  [390, 'light'],
  [390, 'dark'],
  [1280, 'dark'],
] as const) {
  test(`account usage remains readable in the ${width}px ${theme} session picker`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height: 900 })
    await page.addInitScript(
      (theme) => localStorage.setItem('olivares.theme', theme),
      theme,
    )
    await page.goto('/sessions')
    await page
      .locator('#work-pane-rail')
      .getByRole('button', { name: 'New session', exact: true })
      .click()
    await page.getByRole('button', { name: /^Tool and account: / }).click()
    const row = page.getByRole('menuitemradio', { name: /work/ })
    await expect(row).toContainText('17% of the week · 41% of 5 hours')
    expect(await row.evaluate((el) => el.scrollHeight <= el.clientHeight)).toBe(
      true,
    )
    const box = await page.getByRole('listbox').boundingBox()
    expect(box!.x).toBeGreaterThanOrEqual(0)
    expect(box!.x + box!.width).toBeLessThanOrEqual(width)
    if (shots)
      await page.screenshot({ path: `${shots}/picker-${width}-${theme}.png` })
    const report = await new AxeBuilder({ page })
      .include('[data-slot="select-content"]')
      .disableRules(['color-contrast'])
      .analyze()
    expect(report.violations).toEqual([])
  })
}
