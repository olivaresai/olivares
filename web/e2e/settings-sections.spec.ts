// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// SETTINGS HOLDS PERSONAL SECTIONS ONLY (0.5 one navigation), against a live `--seed-demo`
// engine. AI tools and Files & workspaces are places of the sidebar; Settings' old addresses
// for them (#2905) land on those pages. Nothing on this path is intercepted. Like
// console-keyboard-routes.spec.ts it needs the seeded estate (a demo login) and skips visibly
// without it; scripts/web-e2e-demo.sh runs it.
import AxeBuilder from '@axe-core/playwright'
import { expect, test, type Page } from '@playwright/test'
import { writeFileSync } from 'node:fs'
import { signInFixture } from './fixture-sign-in'

const demoTenant = process.env.DEMO_TENANT ?? ''
const credentials = {
  email: 'demo@olivares.local',
  password: 'olivares-demo-estate',
}

const sectionList = (page: Page) =>
  page.getByRole('navigation', { name: 'Settings sections' })
const section = (page: Page) => new URL(page.url()).searchParams.get('section')

test.describe('Settings sections', () => {
  test.beforeEach(() => {
    test.skip(
      !demoTenant,
      'DEMO_TENANT not set — run this through scripts/web-e2e-demo.sh',
    )
  })

  test('lists no AI tools or Workspaces row, and their old addresses land on those pages', async ({
    page,
  }) => {
    await signInFixture(page, credentials, 20_000)
    await page.goto('/settings')
    const rows = sectionList(page)
    await expect(rows).toBeVisible()
    // The list is buttons only: no row is a link to another route.
    await expect(rows.getByRole('link')).toHaveCount(0)
    for (const name of ['AI tools', 'Workspaces', 'Updates'])
      await expect(rows.getByRole('button', { name })).toHaveCount(0)

    // The setup wizard's "Add an API key" address keeps its form request across the move.
    await page.goto(
      '/settings?section=aiTools&add=anthropic&returnTo=%2Fonboarding',
    )
    await expect(page).toHaveURL(
      /\/providers\?(?=.*add=anthropic)(?=.*returnTo=%2Fonboarding)/,
    )
    await page.goto('/settings?section=workspaces')
    await expect(page).toHaveURL(/\/workspace$/)
    // A replace: Back leaves for the page before, never the retired address.
    await page.goBack()
    expect(section(page)).not.toBe('workspaces')
  })

  test('the command palette opens AI tools as its own page', async ({
    page,
  }) => {
    await signInFixture(page, credentials, 20_000)
    await page.goto('/settings')
    await expect(sectionList(page)).toBeVisible()
    await page.keyboard.press('ControlOrMeta+k')
    await page.getByRole('combobox').fill('AI tools')
    await expect(
      page.locator('[role=option][data-value^="settings:aiTools "]'),
    ).toHaveCount(0)
    await page.locator('[role=option][data-value^="view:agent-tools "]').click()
    await expect(page).toHaveURL(/\/agent-tools$/)
  })

  const views = [
    { name: '1280', width: 1280, height: 900 },
    { name: '390', width: 390, height: 844 },
  ] as const
  for (const view of views) {
    for (const colorScheme of ['light', 'dark'] as const) {
      test(`captures and axe at ${view.name} px, ${colorScheme}`, async ({
        page,
      }, testInfo) => {
        await page.setViewportSize({ width: view.width, height: view.height })
        await page.emulateMedia({ colorScheme })
        await signInFixture(page, credentials, 20_000)
        const found: Record<string, string[]> = {}
        // The nodes behind each violation, kept in the evidence so a failure names its element.
        const nodes: Record<string, string[]> = {}
        // The hosted section, and each room at its own route (which stays published).
        for (const [name, path] of [
          ['settings-setup', '/settings?section=setup'],
          ['page-providers', '/providers'],
          ['page-workspace', '/workspace'],
          ['page-onboarding', '/onboarding'],
        ] as const) {
          await page.goto(path)
          await expect(
            page.locator('#main-content').getByRole('heading').first(),
          ).toBeVisible({ timeout: 20_000 })
          // Let the room's first reads settle before the capture.
          await page.waitForTimeout(1_500)
          await page.screenshot({
            path: testInfo.outputPath(
              `${name}-${view.name}-${colorScheme}.png`,
            ),
            fullPage: true,
          })
          const result = await new AxeBuilder({ page })
            .include('#main-content')
            .withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa'])
            .analyze()
          found[name] = result.violations.map((v) => v.id).sort()
          nodes[name] = result.violations.flatMap((v) =>
            v.nodes.map((n) => `${v.id}: ${n.target.join(' ')}`),
          )
        }
        writeFileSync(
          testInfo.outputPath(`axe-${view.name}-${colorScheme}.json`),
          JSON.stringify({ found, nodes }, null, 2),
        )
        // The hosted section and every route are axe clean, at both widths and in both schemes.
        for (const name of Object.keys(found)) {
          expect(nodes[name], `axe violations on ${name}`).toEqual([])
        }
      })
    }
  }
})
