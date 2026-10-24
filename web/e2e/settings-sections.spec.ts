// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// SETTINGS HOSTS AI TOOLS AND WORKSPACES (#2905), against a live `--seed-demo` engine.
// Nothing on this path is intercepted. Like console-keyboard-routes.spec.ts it needs the
// seeded estate (a demo login) and skips visibly without it; scripts/web-e2e-demo.sh runs it.
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

/** A full page load drops this marker; a section change inside the console keeps it. */
const markPage = (page: Page) =>
  page.evaluate(() => {
    ;(window as unknown as { __settingsStays: boolean }).__settingsStays = true
  })
const pageStayed = (page: Page) =>
  page.evaluate(
    () =>
      (window as unknown as { __settingsStays?: boolean }).__settingsStays ===
      true,
  )

test.describe('Settings sections', () => {
  test.beforeEach(() => {
    test.skip(
      !demoTenant,
      'DEMO_TENANT not set — run this through scripts/web-e2e-demo.sh',
    )
  })

  test('AI tools > add a profile > Workspaces > Back never leaves /settings', async ({
    page,
  }, testInfo) => {
    await signInFixture(page, credentials, 20_000)
    await page.goto('/settings')
    const rows = sectionList(page)
    // The room's page title; its "installed here" box has an h3 of the same name.
    const aiToolsTitle = page.getByRole('heading', {
      level: 1,
      name: 'AI tools',
    })
    await expect(rows).toBeVisible()
    await markPage(page)
    const pathnames: string[] = []
    page.on('framenavigated', (frame) => {
      if (frame === page.mainFrame())
        pathnames.push(new URL(frame.url()).pathname)
    })

    // The list is buttons only: no row is a link to another route.
    await expect(rows.getByRole('link')).toHaveCount(0)

    await rows.getByRole('button', { name: 'AI tools' }).click()
    await expect(page).toHaveURL(/\/settings\?section=aiTools$/)
    await expect(aiToolsTitle).toBeVisible()
    await expect(
      rows.getByRole('button', { name: 'AI tools' }),
    ).toHaveAttribute('aria-current', 'page')

    // Add a profile: the seeded estate has a tool installed, so the dialog opens on this
    // page, creates the profile (the next free name) and moves on to that profile's sign-in.
    const add = page.getByRole('button', { name: 'Add profile' }).first()
    await expect(add).toBeVisible({ timeout: 20_000 })
    await add.click()
    const addDialog = page.getByRole('dialog', { name: 'Add a profile' })
    await expect(addDialog).toBeVisible()
    await addDialog.getByRole('button', { name: 'Create' }).click()
    const signIn = page.getByRole('dialog', { name: /^Sign in / })
    await expect(signIn).toBeVisible()
    expect(new URL(page.url()).pathname).toBe('/settings')
    const profile = (await signIn.getByRole('heading').first().innerText())
      .replace(/^Sign in /, '')
      .trim()
    await signIn.getByRole('button', { name: 'Not now' }).click()
    await expect(page.getByRole('dialog')).toHaveCount(0)
    // The new profile is in the room's list, on the same page.
    await expect(page.getByText(profile, { exact: true }).first()).toBeVisible()

    await rows.getByRole('button', { name: 'Workspaces' }).click()
    await expect(page).toHaveURL(/\/settings\?section=workspaces$/)
    await expect(
      rows.getByRole('button', { name: 'Workspaces' }),
    ).toHaveAttribute('aria-current', 'page')
    await expect(aiToolsTitle).toHaveCount(0)

    // Back returns to the section before it, inside Settings; once more, to General.
    await page.goBack()
    await expect(page).toHaveURL(/\/settings\?section=aiTools$/)
    await expect(aiToolsTitle).toBeVisible()
    await page.goBack()
    expect(new URL(page.url()).pathname).toBe('/settings')
    expect(section(page)).toBeNull()
    await expect(page.getByRole('radiogroup', { name: 'Theme' })).toBeVisible()

    // Forward, then Reload keep the section.
    await page.goForward()
    await page.goForward()
    await expect(page).toHaveURL(/\/settings\?section=workspaces$/)
    expect(await pageStayed(page)).toBe(true)
    await page.reload()
    await expect(
      sectionList(page).getByRole('button', { name: 'Workspaces' }),
    ).toHaveAttribute('aria-current', 'page')

    expect(
      pathnames.filter((pathname) => pathname !== '/settings'),
      'every navigation of the journey stayed on /settings',
    ).toEqual([])
    writeFileSync(
      testInfo.outputPath('journey.json'),
      JSON.stringify({ pathnames, profileCreated: true }, null, 2),
    )
  })

  test('the command palette opens the section inside Settings', async ({
    page,
  }) => {
    await signInFixture(page, credentials, 20_000)
    await page.goto('/settings')
    await expect(sectionList(page)).toBeVisible()
    await markPage(page)
    await page.keyboard.press('ControlOrMeta+k')
    await page.getByRole('combobox').fill('AI tools')
    const entry = page.locator('[role=option][data-value^="settings:aiTools "]')
    await expect(entry).toBeVisible()
    await entry.click()
    await expect(page).toHaveURL(/\/settings\?section=aiTools$/)
    await expect(
      sectionList(page).getByRole('button', { name: 'AI tools' }),
    ).toHaveAttribute('aria-current', 'page')
    expect(await pageStayed(page)).toBe(true)
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
        // Each hosted section, and the same room at its own route (which stays published).
        for (const [name, path] of [
          ['settings-aiTools', '/settings?section=aiTools'],
          ['settings-workspaces', '/settings?section=workspaces'],
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
        // Every hosted section and every route is axe clean, at both widths and in both schemes.
        for (const name of Object.keys(found)) {
          expect(nodes[name], `axe violations on ${name}`).toEqual([])
        }
      })
    }
  }
})
