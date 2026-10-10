// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { expect, test, type Page } from '@playwright/test'

const requested = '/settings?tab=profile&from=chunk-recovery'

async function failShell(page: Page, signedIn = false) {
  // Labelled API stand-ins only; the built console, import transport, module
  // map, error boundary, Retry control and auth/tenant guards are all real.
  await page.addInitScript(() => {
    localStorage.setItem('olivares.lang', 'en')
  })
  await page.route('**/v1/**', async (route) => {
    const path = new URL(route.request().url()).pathname
    if (path === '/v1/server-info') {
      await route.fulfill({
        json: { setup_required: false, sso_providers: [] },
      })
    } else if (path === '/v1/auth/browser-session') {
      await route.fulfill(
        signedIn
          ? {
              json: {
                csrf_token: 'shell-recovery-fixture-csrf',
                session_id: 'shell-recovery-fixture',
                expires_at: new Date(Date.now() + 3_600_000).toISOString(),
              },
            }
          : { status: 401, json: { error: { code: 'unauthorized' } } },
      )
    } else if (path === '/v1/auth/whoami') {
      await route.fulfill({
        json: {
          kind: 'user',
          user_id: 'shell-recovery-fixture-user',
          actor: 'shell-recovery@example.test',
          superadmin: false,
          grants: [{ tenant: 'shell-recovery-fixture-tenant', role: 'owner' }],
        },
      })
    } else {
      // Unmodelled decoration APIs must fail honestly, not masquerade as an
      // empty successful inventory. Settings preferences do not depend on them.
      await route.fulfill({
        status: 503,
        json: { error: { code: 'fixture_unavailable' } },
      })
    }
  })
  const transport = { unavailable: true, attempts: 0, documents: 0 }
  page.on('request', (request) => {
    if (request.isNavigationRequest() && request.frame() === page.mainFrame())
      transport.documents++
  })
  await page.route('**/assets/app-layout-*.js', async (route) => {
    transport.attempts++
    if (transport.unavailable)
      await route.fulfill({
        status: 503,
        body: 'Shell transport fixture unavailable',
      })
    else await route.continue()
  })
  await page.goto(requested)
  await expect(
    page.getByRole('heading', { name: 'This view crashed' }),
  ).toBeVisible()
  await expect(page.getByRole('alert')).toContainText('This view crashed')
  // Only the startup requests exist while the shell is unavailable. Let both
  // automatic recovery guards finish before measuring an explicit Retry.
  await page.waitForLoadState('networkidle')
  await expect(
    page.getByRole('heading', { name: 'This view crashed' }),
  ).toBeVisible()
  // Vite's load guard and the router each permit one automatic recovery.
  // Their error events can race; both must settle at the announced boundary.
  expect(transport.documents).toBeGreaterThan(1)
  expect(transport.documents).toBeLessThanOrEqual(3)
  expect(transport.attempts).toBeGreaterThan(1)
  expect(transport.attempts).toBeLessThanOrEqual(3)
  return transport
}

for (const signedIn of [false, true]) {
  test(`Retry recovers a cached failed shell import when ${signedIn ? 'signed in' : 'signed out'}, preserving the query`, async ({
    page,
  }) => {
    const transport = await failShell(page, signedIn)
    const initialDocuments = transport.documents
    const initialAttempts = transport.attempts
    transport.unavailable = false
    // Exercise the native, translated recovery control with the keyboard.
    await page.getByRole('button', { name: 'Retry', exact: true }).focus()
    await page.keyboard.press('Enter')
    if (signedIn) {
      await expect(
        page.getByRole('heading', { name: 'General', exact: true }),
      ).toBeVisible()
      expect(new URL(page.url()).pathname + new URL(page.url()).search).toBe(
        requested,
      )
    } else {
      await expect(
        page.getByRole('button', { name: 'Sign in', exact: true }),
      ).toBeVisible()
      const destination = new URL(page.url())
      expect(destination.pathname).toBe('/login')
      expect(destination.searchParams.get('returnTo')).toBe(requested)
    }
    await expect(
      page.getByRole('heading', { name: 'This view crashed' }),
    ).toHaveCount(0)
    expect(transport.documents).toBe(initialDocuments + 1)
    expect(transport.attempts).toBe(initialAttempts + 1)
  })
}

test('Retry leaves the announced shell error available without a reload loop when transport stays unavailable', async ({
  page,
}) => {
  const transport = await failShell(page)
  const initialDocuments = transport.documents
  const initialAttempts = transport.attempts
  for (const documents of [initialDocuments + 1, initialDocuments + 2]) {
    await Promise.all([
      page.waitForEvent('domcontentloaded', { timeout: 5000 }),
      page.getByRole('button', { name: 'Retry', exact: true }).click(),
    ])
    await expect.poll(() => transport.documents).toBe(documents)
    await expect(
      page.getByRole('heading', { name: 'This view crashed' }),
    ).toBeVisible()
    await expect(page.getByRole('alert')).toContainText('This view crashed')
    await expect(
      page.getByRole('button', { name: 'Retry', exact: true }),
    ).toBeVisible()
    await expect(page).toHaveURL(
      (url) => url.pathname + url.search === requested,
    )
    // Observe reload stability after the error is rendered, rather than using
    // a delay to wait for rendering. Automatic recovery must remain bounded.
    await page.waitForTimeout(1500)
    expect(transport.documents).toBe(documents)
    expect(transport.attempts).toBe(
      initialAttempts + documents - initialDocuments,
    )
  }
})
