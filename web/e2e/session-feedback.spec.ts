// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { expect, test } from '@playwright/test'

// Run against a disposable engine with one registered fixture-driver profile.
// The launcher owns the engine and its session processes, including teardown.
const enabled = process.env.E2E_SESSION_FEEDBACK === '1'

test('launch feedback leaves the session composer clear', async ({
  page,
}, testInfo) => {
  test.skip(
    !enabled,
    'Requires a disposable engine with a fixture-driver profile',
  )
  test.setTimeout(60_000)
  await page.addInitScript(() => {
    localStorage.setItem('olivares.lang', 'en')
  })
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto('/login')
  await page.locator('#email').fill(process.env.E2E_EMAIL!)
  await page.locator('#password').fill(process.env.E2E_PASSWORD!)
  await page.getByRole('button', { name: /^sign in$/i }).click()
  await page.waitForURL((url) => !url.pathname.startsWith('/login'))
  await page.goto('/sessions')
  await page
    .getByRole('main')
    .getByRole('button', { name: 'New session', exact: true })
    .first()
    .click()
  let dialog = page.getByRole('dialog')
  const advanced = dialog.getByRole('button', {
    name: 'Advanced launch options',
  })
  await expect(
    advanced.or(dialog.getByRole('button', { name: 'More options' })),
  ).toBeVisible()
  if (!(await advanced.isVisible())) {
    await dialog.getByRole('button', { name: 'More options' }).click()
  }
  await dialog.getByRole('button', { name: 'Advanced launch options' }).click()
  dialog = page.getByRole('dialog').last()
  await dialog.getByRole('combobox', { name: 'Provider profile' }).click()
  await page.getByRole('option', { name: /Claude stand-in/ }).click()
  await dialog.getByRole('button', { name: /^(Request launch|Start)$/ }).click()
  const composer = page.getByTestId('work-composer')
  const send = page.getByTestId('composer-send')
  await expect(send).toBeVisible()
  const toast = page
    .locator('[data-sonner-toast]')
    .filter({ hasText: 'Session launched' })
  await expect(toast).toBeVisible()
  // Hover pauses the real notification's dismiss timer while captures are made.
  await toast.hover()
  for (const viewport of [
    { width: 1440, height: 900 },
    { width: 1280, height: 720 },
    { width: 390, height: 844 },
  ]) {
    await page.setViewportSize(viewport)
    if (viewport.width < 768) {
      await page
        .getByRole('listbox', { name: 'Work rail' })
        .getByRole('option')
        .first()
        .click()
    }
    await expect(toast).toBeVisible()
    await expect(composer).toBeVisible()
    await expect(send).toBeVisible()
    await page.screenshot({
      path: testInfo.outputPath(`session-${viewport.width}.png`),
    })
    const notification = await toast.boundingBox()
    const input = await composer.boundingBox()
    expect(notification).not.toBeNull()
    expect(input).not.toBeNull()
    const intersects =
      notification!.x < input!.x + input!.width &&
      notification!.x + notification!.width > input!.x &&
      notification!.y < input!.y + input!.height &&
      notification!.y + notification!.height > input!.y
    expect
      .soft(intersects, `toast overlaps composer at ${viewport.width}px`)
      .toBe(false)
  }
  await expect.soft(page.getByTestId('can-message')).toHaveCount(0)
  await page.getByTestId('launcher-input').fill('Read the README')
  await send.click()
  await expect(page.getByTestId('session-conversation')).toContainText(
    "I'll look that up.",
  )
})
