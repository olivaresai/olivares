// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
// The usage of each provider account where the console names the account: the AI tools page
// (rows and the account list) and the New session picker, with an account at its limit
// marked before Start. Accounts and usage are fixtures; the console is the real one.
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
    if (path === '/v1/m/sessions/provider-accounts/ppf_work')
      return route.fulfill({ json: accounts[0] })
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
                    limits: [],
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
    '5-hour 41% · Weekly 17% · Monthly spend 85% · Credits 12.50',
  )
  await expect(page.getByTestId('profile-personal')).toContainText(
    '5-hour 100% · Weekly 62%',
  )
  await expect(page.getByTestId('profile-review')).not.toContainText('%')
  if (shots) await page.screenshot({ path: `${shots}/tools-1280-light.png` })
  // The account list, folded under "All accounts", says it too.
  await page.getByText('All accounts: details, labels and bindings').click()
  const row = page.getByRole('row', { name: /personal/ })
  await expect(row).toContainText('5-hour 100% · Weekly 62%')
  await expect(page.getByRole('row', { name: /review/ })).toContainText(
    'Usage unknown',
  )
  await row.scrollIntoViewIfNeeded()
  if (shots) await page.screenshot({ path: `${shots}/accounts-1280-light.png` })
  const report = await new AxeBuilder({ page })
    .disableRules(['color-contrast'])
    .analyze()
  expect(report.violations).toEqual([])
})

test('the New session picker shows the use and marks an account at its limit before Start', async ({
  page,
}) => {
  await page.goto('/sessions')
  await page
    .locator('#work-pane-rail')
    .getByRole('button', { name: 'New session', exact: true })
    .click()
  const dialog = page.getByRole('dialog')
  await dialog.getByRole('combobox', { name: 'Profile' }).click()
  await expect(page.getByRole('option', { name: /work/ })).toContainText(
    '5-hour 41% · Weekly 17% · Monthly spend 85% · Credits 12.50',
  )
  if (shots) await page.screenshot({ path: `${shots}/picker-1280-light.png` })
  await page.getByRole('option', { name: /personal/ }).click()
  await expect(dialog.getByRole('status')).toContainText(
    /5-hour limit reached, resets /,
  )
  await expect(
    dialog.getByRole('button', { name: 'Start', exact: true }),
  ).toBeEnabled()
  if (shots)
    await page.screenshot({ path: `${shots}/picker-at-limit-1280-light.png` })
  const report = await new AxeBuilder({ page })
    .include('[role="dialog"]:not([aria-hidden="true"])')
    .disableRules(['color-contrast'])
    .analyze()
  expect(report.violations).toEqual([])
})

test('session Details carries monthly spend and credits for its bound account', async ({
  page,
}) => {
  await page.goto('/sessions?session=run%3Arun_usage')
  await expect(page.getByTestId('session-account-usage')).toContainText(
    'Monthly spend 85% · Credits 12.50',
  )
  if (shots) await page.screenshot({ path: `${shots}/details-1280-light.png` })
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
    await page
      .getByRole('dialog')
      .getByRole('combobox', { name: 'Profile' })
      .click()
    const row = page.getByRole('option', { name: /work/ })
    await expect(row).toContainText('Monthly spend 85% · Credits 12.50')
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
