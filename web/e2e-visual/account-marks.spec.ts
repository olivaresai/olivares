// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
// An account's display name and accent where the console names the account: the AI tools
// row and its editor, the session header, the New session picker and Now, at 1280 and 390
// px in both themes (SHOTS_DIR writes the captures). Accounts are fixtures; the console is
// the real one.
import { expect as baseExpect, test } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { fixtureFor, serverInfo } from './fixtures'

// Cold route chunks may take longer than five seconds on a shared browser runner.
const expect = baseExpect.configure({ timeout: 30_000 })
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
  display_name: name === 'work' ? 'Research' : name,
  accent: name === 'work' ? 'purple' : 'teal',
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
          : path === '/v1/m/agenttools/model-servers'
            ? { servers: [] }
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

for (const width of [1280, 390]) {
  for (const theme of ['light', 'dark']) {
    test(`account badges ${width} ${theme}`, async ({ page }) => {
      await page.setViewportSize({ width, height: 900 })
      await page.addInitScript(
        (theme) => localStorage.setItem('olivares.theme', theme),
        theme,
      )
      const shot = async (screen: string) => {
        if (shots)
          await page.screenshot({
            path: `${shots}/${screen}-${width}-${theme}.png`,
            fullPage: true,
          })
      }
      await page.goto('/agent-tools')
      const row = page.getByTestId('profile-work')
      await expect(row).toContainText('Research')
      await expect(row.locator('[data-provider-accent="purple"]')).toBeVisible()
      await shot('accounts')
      await row.getByRole('button').first().click()
      await expect(page.getByRole('dialog')).toContainText('Research')
      await page
        .getByRole('button', { name: 'Edit label and color', exact: true })
        .click()
      await expect(page.getByRole('combobox', { name: 'Color' })).toBeVisible()
      expect(
        (
          await new AxeBuilder({ page })
            .include('[role="dialog"]')
            .withTags(['wcag2a', 'wcag2aa', 'wcag21aa'])
            .analyze()
        ).violations,
      ).toEqual([])
      await shot('edit-account')
      await page.goto('/sessions?session=run%3Arun_usage')
      await expect(
        page.locator(
          '[data-slot="thread-header"] [data-testid="account-badge"]',
        ),
      ).toContainText('Research')
      const title = page.locator('[data-slot="thread-header"] h2')
      expect((await title.boundingBox())!.width).toBeGreaterThanOrEqual(48)
      expect(
        (
          await new AxeBuilder({ page })
            .include('[data-slot="thread-header"]')
            // The Session details toggle names a pane (aria-controls) that is not in the
            // document while it is closed: work-surface.tsx, not this change. Every other
            // node of the header, the account badge included, is still scanned.
            .exclude('[data-testid="context-toggle"]')
            .withTags(['wcag2a', 'wcag2aa', 'wcag21aa'])
            .analyze()
        ).violations,
      ).toEqual([])
      await shot('sessions')
      await page.goto('/sessions')
      await page
        .locator('#work-pane-rail')
        .getByRole('button', { name: 'New session', exact: true })
        .click()
      await page.getByRole('button', { name: /^Tool and account: / }).click()
      await expect(
        page
          .getByRole('menuitemradio', { name: /Research/ })
          .locator('[data-provider-accent="purple"]'),
      ).toBeVisible()
      await shot('picker')
      await page.goto('/now')
      await expect(page.getByTestId('account-badge').first()).toContainText(
        'Research',
      )
      await shot('now')
    })
  }
}
