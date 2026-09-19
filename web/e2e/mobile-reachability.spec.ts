// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { expect, test, type Locator } from '@playwright/test'
import { signInFixture } from './fixture-sign-in'

// Run with the golden fixture stopped after profile-get, before session-create.
const email = process.env.E2E_EMAIL ?? ''
const password = process.env.E2E_PASSWORD ?? ''
const profile = process.env.E2E_PROFILE_NAME ?? ''
test.use({
  viewport: { width: 390, height: 844 },
  screenshot: 'off',
  trace: 'off',
  video: 'off',
  actionTimeout: 5_000,
  navigationTimeout: 5_000,
})

async function targetIsReachable(target: Locator) {
  await expect(target).toBeVisible()
  const measurement = await target.evaluate((element) => {
    const r = element.getBoundingClientRect()
    const x = r.x + r.width / 2
    const y = r.y + r.height / 2
    const hit = document.elementFromPoint(x, y)
    return {
      x,
      y,
      width: r.width,
      height: r.height,
      viewportWidth: innerWidth,
      viewportHeight: innerHeight,
      hit: hit !== null && (hit === element || element.contains(hit)),
    }
  })
  expect(measurement.width).toBeGreaterThan(0)
  expect(measurement.height).toBeGreaterThan(0)
  expect(measurement.x).toBeGreaterThanOrEqual(0)
  expect(measurement.y).toBeGreaterThanOrEqual(0)
  expect(measurement.x).toBeLessThan(measurement.viewportWidth)
  expect(measurement.y).toBeLessThan(measurement.viewportHeight)
  expect(measurement.hit, JSON.stringify(measurement)).toBe(true)
}

test.describe('phone draft controls are reachable', () => {
  test.skip(
    process.env.E2E_EMPTY_SESSIONS !== '1' || !email || !password || !profile,
    'requires the real prelaunch golden fixture with an empty tenant',
  )

  for (const surface of [
    { name: 'docked', url: '/sessions?pane=narrative' },
    { name: 'Home', url: '/' },
  ]) {
    for (const input of ['pointer', 'keyboard'] as const) {
      test(`${surface.name} profile through ${input}`, async ({ page }) => {
        await page.addInitScript(() => {
          localStorage.setItem('olivares.theme', 'light')
        })
        await signInFixture(page, { email, password })
        await expect(page.locator('html')).not.toHaveClass(/dark/)
        await page.goto(surface.url)
        const composer = page.getByTestId('work-composer')
        await expect(composer).toBeVisible()
        await expect(composer).not.toHaveAttribute('data-attached', 'true')
        const field = composer.getByTestId('launcher-input')
        const summary = composer.getByTestId('composer-advanced')
        const picker = composer.getByTestId('launcher-profile')
        await field.fill('mobile reachability')
        if (input === 'pointer') {
          await targetIsReachable(summary)
          await summary.click()
          await targetIsReachable(picker)
          await picker.click()
        } else {
          await field.press('Tab')
          await expect(summary).toBeFocused()
          await page.keyboard.press('Enter')
          await page.keyboard.press('Tab')
          await expect(picker).toBeFocused()
          await targetIsReachable(picker)
          await page.keyboard.press('ArrowDown')
        }
        const option = page.getByRole('option', { name: profile, exact: true })
        await expect(page.getByRole('option')).toHaveCount(1)
        await expect(option).toHaveCount(1)
        if (input === 'pointer') {
          await targetIsReachable(option)
          await option.click()
        } else {
          await expect(option).toBeFocused()
          await page.keyboard.press('Enter')
        }
        await expect(picker).toContainText(profile)
        await expect(composer.getByTestId('launcher-start')).toBeEnabled()
      })
    }
  }
})
