// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { expect, test } from '@playwright/test'

const setupToken = process.env.PLAYWRIGHT_SETUP_TOKEN ?? ''

test('fresh installation exposes sign-in settings and preserves passkey policy across reloads', async ({
  page,
  context,
}) => {
  test.skip(!setupToken, 'Needs its own fresh engine and setup token')
  await page.setViewportSize({ width: 1440, height: 1000 })
  await page.goto('/setup')
  await page.locator('#token').fill(setupToken)
  await page.locator('#setup-email').fill('settings@example.com')
  await page.locator('#setup-password').fill('fixture-password-settings-42')
  await page.getByRole('button', { name: /create administrator/i }).click()
  await page.waitForURL('**/onboarding')
  await page.goto('/settings')
  await page
    .getByRole('button', { name: 'Sign-in and security', exact: true })
    .click()
  await expect(page.getByText('12 hours', { exact: true })).toBeVisible()
  await expect(page.getByText('Password', { exact: true })).toBeVisible()
  await expect(
    page.getByText(
      'No single sign-on provider is available on the sign-in page.',
    ),
  ).toBeVisible()
  await expect(
    page.getByText('No passkeys registered', { exact: true }),
  ).toBeVisible()
  await expect(
    page.getByRole('radio', { name: 'Off', exact: true }),
  ).toHaveAttribute('aria-checked', 'true')
  await page.screenshot({
    path: test.info().outputPath('fresh-sign-in.png'),
    fullPage: true,
  })

  await page.getByRole('radio', { name: 'Passkey', exact: true }).click()
  await expect(
    page.getByRole('alert').filter({ hasText: 'Add a passkey' }),
  ).toBeVisible()
  await expect(
    page.getByRole('radio', { name: 'Off', exact: true }),
  ).toHaveAttribute('aria-checked', 'true')

  const cdp = await context.newCDPSession(page)
  await cdp.send('WebAuthn.enable')
  const { authenticatorId } = await cdp.send(
    'WebAuthn.addVirtualAuthenticator',
    {
      options: {
        protocol: 'ctap2',
        transport: 'internal',
        hasResidentKey: true,
        hasUserVerification: true,
        isUserVerified: true,
        automaticPresenceSimulation: true,
      },
    },
  )
  try {
    await page
      .getByRole('button', { name: 'Register passkey', exact: true })
      .click()
    await page
      .getByLabel('Passkey name', { exact: true })
      .fill('Browser test passkey')
    await page
      .getByRole('dialog')
      .getByRole('button', { name: 'Register passkey', exact: true })
      .click()
    await expect(
      page.getByText('Browser test passkey', { exact: true }),
    ).toBeVisible()
    await page.getByRole('radio', { name: 'Passkey', exact: true }).click()
    await page
      .getByRole('button', { name: 'Authenticate with passkey', exact: true })
      .click()
    await expect(
      page.getByRole('radio', { name: 'Passkey', exact: true }),
    ).toHaveAttribute('aria-checked', 'true')
    await page.goto('/settings?section=signIn')
    await expect(
      page.getByRole('radio', { name: 'Passkey', exact: true }),
    ).toHaveAttribute('aria-checked', 'true')
    await expect(
      page.getByText('Browser test passkey', { exact: true }),
    ).toBeVisible()
    await page
      .getByText('Browser test passkey', { exact: true })
      .scrollIntoViewIfNeeded()
    await page.screenshot({
      path: test.info().outputPath('passkey-policy.png'),
      fullPage: true,
    })
    await page.getByRole('radio', { name: 'Off', exact: true }).click()
    await expect(
      page.getByRole('radio', { name: 'Off', exact: true }),
    ).toHaveAttribute('aria-checked', 'true')
  } finally {
    await cdp.send('WebAuthn.removeVirtualAuthenticator', { authenticatorId })
    await cdp.detach()
  }
})
