// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { expect, test, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { signInFixture } from './fixture-sign-in'

const setupToken = process.env.PLAYWRIGHT_SETUP_TOKEN ?? ''

test('two browsers list and end other sign-ins accessibly', async ({
  browser,
  request,
}) => {
  test.skip(!setupToken, 'Needs its own fresh engine and setup token')
  const first = await browser.newContext({
    viewport: { width: 1280, height: 1000 },
  })
  const second = await browser.newContext({
    viewport: { width: 1280, height: 1000 },
  })
  const pairedPhone = await browser.newContext({
    viewport: { width: 1280, height: 1000 },
  })
  const pairedTablet = await browser.newContext({
    viewport: { width: 1280, height: 1000 },
  })
  const capturePublic = async (page: Page, name: string) => {
    await page.emulateMedia({ colorScheme: 'light' })
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
    await page.screenshot({
      path: test.info().outputPath(`${name}-1280-light.png`),
    })
  }
  try {
    const firstPage = await first.newPage()
    const secondPage = await second.newPage()
    const credentials = {
      email: 'settings@example.com',
      password: 'fixture-password-settings-42',
    }
    await firstPage.goto('/setup')
    await capturePublic(firstPage, 'setup')
    await firstPage.locator('#token').fill(setupToken)
    await firstPage.locator('#setup-email').fill(credentials.email)
    await firstPage.locator('#setup-password').fill(credentials.password)
    await firstPage
      .getByRole('button', { name: /create administrator/i })
      .click()
    await firstPage.waitForURL('**/onboarding')

    await secondPage.goto('/login')
    await capturePublic(secondPage, 'login')
    await signInFixture(secondPage, credentials)
    await secondPage.goto('/settings')
    await secondPage
      .getByRole('button', { name: 'Sign-in and security', exact: true })
      .click()
    await expect(secondPage.getByText('Your sign-ins')).toBeVisible()
    await expect(
      secondPage.getByText('This session', { exact: true }),
    ).toBeVisible()
    await expect(secondPage.getByText('Browser', { exact: true })).toHaveCount(
      2,
    )

    for (const scheme of ['light', 'dark'] as const) {
      await secondPage.emulateMedia({ colorScheme: scheme })
      for (const width of [1280, 390]) {
        await secondPage.setViewportSize({ width, height: 1000 })
        const results = await new AxeBuilder({ page: secondPage }).analyze()
        expect(results.violations).toEqual([])
        await secondPage
          .getByText('Your sign-ins', { exact: true })
          .evaluate((el) => el.scrollIntoView({ block: 'start' }))
        await secondPage.screenshot({
          path: test.info().outputPath(`sign-ins-${width}-${scheme}.png`),
        })
      }
    }

    // Pair two named devices through the public, explicit one-time-link flow.
    // Ending the lost phone must leave the other paired device and caller live.
    const phonePage = await pairedPhone.newPage()
    await phonePage.goto('/accept-invite')
    await capturePublic(phonePage, 'accept-invite')
    const tabletPage = await pairedTablet.newPage()
    let previousLink = ''
    for (const [name, page] of [
      ['My phone', phonePage],
      ['Spare tablet', tabletPage],
    ] as const) {
      await secondPage.getByLabel('Device name').fill(name)
      await secondPage
        .getByRole('button', { name: 'Pair a device', exact: true })
        .click()
      const link = secondPage.getByLabel('One-time link')
      await expect(link).toBeVisible()
      await expect(link).not.toHaveValue(previousLink)
      const value = await link.inputValue()
      previousLink = value
      await page.goto(value)
      await expect(
        page.getByRole('button', { name: 'Pair this device', exact: true }),
      ).toBeVisible()
      await expect(page).toHaveURL(/\/pair$/)
      if (name === 'My phone') {
        for (const scheme of ['light', 'dark'] as const) {
          await page.emulateMedia({ colorScheme: scheme })
          for (const width of [1280, 390]) {
            await page.setViewportSize({ width, height: 1000 })
            expect(
              (await new AxeBuilder({ page }).analyze()).violations,
            ).toEqual([])
            await page.screenshot({
              path: test
                .info()
                .outputPath(`pair-device-${width}-${scheme}.png`),
            })
          }
        }
      }
      await page
        .getByRole('button', { name: 'Pair this device', exact: true })
        .click()
      await page.waitForURL((url) => url.pathname !== '/pair')
      await page.goto('/settings')
      await page
        .getByRole('button', { name: 'Sign-in and security', exact: true })
        .click()
      const currentRow = page
        .getByRole('row')
        .filter({ hasText: 'This session' })
      await expect(currentRow).toContainText(name)
      // A consumed link never establishes a second session.
      const replay = await request.post('/v1/auth/sessions/pairing/accept', {
        data: {
          token: new URLSearchParams(new URL(value).hash.slice(1)).get('pair'),
        },
      })
      expect(replay.status()).toBe(401)
    }
    await secondPage.goto('/settings')
    await secondPage
      .getByRole('button', { name: 'Sign-in and security', exact: true })
      .click()
    await expect(
      secondPage.getByRole('row').filter({ hasText: 'Spare tablet' }),
    ).toBeVisible()
    await secondPage
      .getByRole('row')
      .filter({ hasText: 'My phone' })
      .getByRole('button', { name: /^End / })
      .click()
    await secondPage
      .getByRole('dialog')
      .getByRole('button', { name: 'End', exact: true })
      .click()
    await expect(
      secondPage.getByRole('row').filter({ hasText: 'My phone' }),
    ).toHaveCount(0)
    await phonePage.reload()
    await phonePage.waitForURL(/\/login/)
    await tabletPage.reload()
    await expect(
      tabletPage.getByText('This session', { exact: true }),
    ).toBeVisible()
    await expect(
      tabletPage.getByRole('row').filter({ hasText: 'Spare tablet' }),
    ).toBeVisible()

    await secondPage
      .getByRole('button', { name: 'End all others', exact: true })
      .click()
    await secondPage
      .getByRole('dialog', { name: 'End all other sign-ins?' })
      .getByRole('button', { name: 'End all others', exact: true })
      .click()
    await expect(
      secondPage.getByRole('button', { name: 'End all others', exact: true }),
    ).toHaveCount(0)
    await expect(
      secondPage.getByText('This session', { exact: true }),
    ).toBeVisible()

    await firstPage.reload()
    await firstPage.waitForURL(/\/login/)
    await expect(
      firstPage.getByRole('button', { name: /sign in/i }).first(),
    ).toBeVisible()

    // Provision a real second member so the People capture includes the new
    // action (the current account deliberately has no self-revocation button).
    const login = await request.post('/v1/auth/login', {
      data: credentials,
    })
    expect(login.status()).toBe(200)
    const { token } = (await login.json()) as { token: string }
    const headers = { Authorization: `Bearer ${token}` }
    try {
      const whoami = await request.get('/v1/auth/whoami', {
        headers,
      })
      expect(whoami.status()).toBe(200)
      const { grants } = (await whoami.json()) as {
        grants: { tenant: string }[]
      }
      const created = await request.post('/v1/users', {
        headers,
        data: {
          email: 'member@example.com',
          password: 'fixture-password-member-42',
          tenant: grants[0].tenant,
          role: 'viewer',
        },
      })
      expect(created.status()).toBe(201)
    } finally {
      const logout = await request.post('/v1/auth/logout', {
        headers,
      })
      expect(logout.status()).toBe(204)
    }
    await secondPage.emulateMedia({ colorScheme: 'light' })
    await secondPage.setViewportSize({ width: 1280, height: 1000 })
    await secondPage.goto('/identity?tab=people')
    await expect(
      secondPage.getByRole('button', {
        name: 'End sign-ins for member@example.com?',
        exact: true,
      }),
    ).toBeVisible()
    expect(
      (await new AxeBuilder({ page: secondPage }).analyze()).violations,
    ).toEqual([])
    await secondPage.screenshot({
      path: test.info().outputPath('people-1280-light.png'),
    })
  } finally {
    await Promise.all([
      first.close(),
      second.close(),
      pairedPhone.close(),
      pairedTablet.close(),
    ])
  }
})
