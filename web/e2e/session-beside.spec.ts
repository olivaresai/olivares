// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { expect, test } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'

test('two session narratives preserve URL, focus, and width across the breakpoint', async ({
  page,
}) => {
  test.skip(!process.env.DEMO_TENANT, 'Run with the live demo engine')
  await page.goto('/login')
  await page.locator('input[type=email]').first().fill('demo@olivares.local')
  await page.locator('input[type=password]').fill('olivares-demo-estate')
  await page.locator('button[type=submit]').click()
  await page.waitForURL((url) => !url.pathname.startsWith('/login'))
  await page.setViewportSize({ width: 1440, height: 1000 })
  await page.goto(
    process.env.E2E_CAPTURE_RUN_REF
      ? `/sessions?session=run:${process.env.E2E_CAPTURE_RUN_REF}`
      : '/sessions',
  )
  const rows = page.getByTestId('rail-row')
  await expect(rows.nth(1)).toBeVisible()
  if (!process.env.E2E_CAPTURE_RUN_REF) await rows.first().click()
  const first = new URL(page.url()).searchParams.get('session')!
  const secondRow = process.env.E2E_SECOND_RUN_REF
    ? rows.filter({ hasText: 'conversation-beside-fixture' })
    : rows.nth(1)
  const second = await secondRow.getAttribute('data-address')
  // The pointer path: the row's own button. It is a mouse and touch convenience, out of
  // the listbox's accessible tree and tab order, so it is found by its label.
  const rowButton = secondRow
    .locator('..')
    .locator('button[aria-label="Open beside"]')
  await secondRow.hover()
  await expect(rowButton).toHaveAttribute('tabindex', '-1')
  await rowButton.click()
  await expect
    .poll(() => new URL(page.url()).searchParams.get('beside'))
    .toBe(second)
  expect(new URL(page.url()).searchParams.get('session')).toBe(first)
  await page.keyboard.press('Alt+Shift+W')
  await expect
    .poll(() => new URL(page.url()).searchParams.has('beside'))
    .toBe(false)
  await secondRow.focus()
  await page.keyboard.press('Alt+Shift+O')
  await expect
    .poll(() => new URL(page.url()).searchParams.get('beside'))
    .toBe(second)
  const primary = page.getByRole('region', {
    name: 'Current session',
    exact: true,
  })
  const beside = page.getByRole('region', {
    name: 'Session beside',
    exact: true,
  })
  await expect(primary.getByTestId('session-narrative')).toBeVisible()
  await expect(beside.getByTestId('session-narrative')).toBeVisible()
  if (process.env.E2E_SECOND_RUN_REF) {
    const firstInput = primary.getByTestId('launcher-input')
    const secondInput = beside.getByTestId('launcher-input')
    await expect(firstInput).toBeEnabled()
    await expect(secondInput).toBeEnabled()
    expect(await firstInput.getAttribute('id')).not.toBe(
      await secondInput.getAttribute('id'),
    )
    await firstInput.fill('Draft stays in the current session')
    await secondInput.fill('Only the session beside receives this turn')
    const sent = page.waitForRequest(
      (request) =>
        request.method() === 'POST' && request.url().includes('/input'),
    )
    await secondInput.press('Enter')
    expect((await sent).url()).toContain(
      `/runs/${process.env.E2E_SECOND_RUN_REF}/input`,
    )
    await expect(firstInput).toHaveValue('Draft stays in the current session')
    await secondInput.click()
    await expect
      .poll(() => new URL(page.url()).searchParams.get('pane'))
      .toBe('beside')
    await page.setViewportSize({ width: 390, height: 1000 })
    await expect(beside).toBeVisible()
    await secondInput.press('Tab')
    await page.keyboard.press('/')
    await expect(secondInput).toBeFocused()
    await page.setViewportSize({ width: 1440, height: 1000 })
  }
  await page.reload()
  await expect(primary).toBeVisible()
  await expect(beside).toBeVisible()

  const captureDir = process.env.BESIDE_CAPTURE_DIR
  if (captureDir) mkdirSync(captureDir, { recursive: true })
  for (const width of [390, 1024, 1279, 1280, 1440]) {
    await page.setViewportSize({ width, height: 1000 })
    if (width < 1280) {
      await page
        .getByRole('button', { name: 'Current session', exact: true })
        .click()
      await expect(primary).toBeVisible()
      await expect(beside).toBeHidden()
      await page
        .getByRole('button', { name: 'Session beside', exact: true })
        .click()
      await expect(beside).toBeVisible()
      await expect(primary).toBeHidden()
      await page.reload()
      await expect(beside).toBeVisible()
    } else {
      await expect(primary).toBeVisible()
      await expect(beside).toBeVisible()
      const a = (await primary.boundingBox())!
      const b = (await beside.boundingBox())!
      expect(Math.abs(a.width - b.width)).toBeLessThanOrEqual(1)
      expect(b.x).toBeGreaterThanOrEqual(a.x + a.width - 1)
    }
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true)
    // The switch between columns exists below 1280 px and only there.
    const switchBar = page.getByRole('group', { name: 'Switch session' })
    if (width < 1280) await expect(switchBar).toBeVisible()
    else await expect(switchBar).toBeHidden()
    const visibleColumn = width < 1280 ? beside : primary
    await visibleColumn.focus()
    await page.keyboard.press('Alt+Shift+ArrowRight')
    const destination = width < 1280 ? primary : beside
    await expect(destination).toBeVisible()
    await expect
      .poll(() =>
        destination.evaluate(
          (element) =>
            element === document.activeElement ||
            element.contains(document.activeElement),
        ),
      )
      .toBe(true)
    await expect(destination.getByTestId('session-narrative')).toBeVisible()
    // Nothing moves once the layout for this width has settled: two reads, two frames apart.
    const settle = () =>
      page.evaluate(
        () =>
          new Promise<void>((done) =>
            requestAnimationFrame(() => requestAnimationFrame(() => done())),
          ),
      )
    await settle()
    const before = await destination.boundingBox()
    await settle()
    expect(await destination.boundingBox()).toEqual(before)
    if (captureDir) {
      for (const theme of ['dark', 'light']) {
        await page.evaluate((value) => {
          document.documentElement.classList.toggle('dark', value === 'dark')
        }, theme)
        await page.screenshot({
          path: join(captureDir, `beside-${width}-${theme}.png`),
        })
      }
    }
  }
  const audit = await new AxeBuilder({ page })
    .include('[data-testid="work-surface"]')
    .analyze()
  expect(
    audit.violations.filter(
      (v) =>
        v.id === 'duplicate-id-aria' ||
        v.id === 'nested-interactive' ||
        v.id === 'aria-required-children',
    ),
  ).toEqual([])
  await page.keyboard.press('Alt+Shift+W')
  await expect
    .poll(() => new URL(page.url()).searchParams.has('beside'))
    .toBe(false)
  expect(new URL(page.url()).searchParams.get('session')).toBe(first)
  await page.goBack()
  await expect
    .poll(() => new URL(page.url()).searchParams.get('beside'))
    .toBe(second)
})
