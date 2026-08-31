// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// E1 — the public /accept-invite leg against a LIVE olivares binary. The
// engine emails accept_url=…/accept-invite#token=… (core/api/handlers_onboarding.go);
// this proves that URL actually lands on a registered, session-free page and that a
// bad token gets the backend's honest, coarse rejection (a real 400 invite_invalid,
// no oracle) — not a blank SPA 404, which was the P0 this session fixed.
//
// The full invite→accept→login round-trip is NOT driven here: creating an invite
// (POST /v1/onboard) requires membership:write behind an AAL3 step-up that a
// headless password session cannot satisfy (same gate the onboarding-first-hour
// spec documents), so the redeem-with-a-real-token path is proven by the component
// tests (accept-invite.test.tsx, api mocked) + the Go handler tests
// (core/api/handlers_console_test.go accept-invite cases). See the skipped test
// below for the shape the flow takes if a headless AAL3 credential ever exists.
import { expect, test } from '@playwright/test'

const setupToken = process.env.PLAYWRIGHT_SETUP_TOKEN ?? ''
const PASSWORD = 'correct-horse-battery-staple-42'

// This spec consumes a one-time bootstrap token before exercising the public
// invite route. Never retain its token/password in automatic failure artifacts.
test.use({ screenshot: 'off', trace: 'off' })

test('accept-invite: public page renders without a session and rejects a bogus token honestly', async ({
  page,
}) => {
  test.skip(
    !setupToken,
    'PLAYWRIGHT_SETUP_TOKEN not set — run via scripts/web-e2e.sh',
  )

  // Configure this virgin engine first, without creating a session. On a truly
  // unconfigured engine /login correctly redirects to /setup, so testing the
  // invite page's "Sign in" destination requires this real prerequisite.
  await page.goto('/setup')
  await page.locator('#token').fill(setupToken)
  await page.locator('#setup-email').fill('invite-admin@example.com')
  await page.locator('#setup-password').fill(PASSWORD)
  await page.getByRole('button', { name: /create administrator/i }).click()
  await page.waitForURL('**/login')

  // No session, no token → the page explains the link is incomplete (no form).
  await page.goto('/accept-invite')
  await expect(page.getByRole('alert')).toContainText(/incomplete/i)
  await page.screenshot({
    path: 'playwright-report/accept-invite-missing-token.png',
  })
  await Promise.all([
    page.waitForURL('**/login'),
    page.getByRole('link', { name: /sign in/i }).click(),
  ])

  // No session, bogus token → the form renders and the LIVE backend's coarse
  // invalid-token rejection surfaces (one message for unknown/expired/used).
  //
  // The detour through /login is load-bearing, and NOT a workaround for a defect.
  // A goto that only changes the fragment is a SAME-DOCUMENT navigation: the page
  // never remounts, so it never re-reads the hash — and reading the token exactly
  // once at mount, then scrubbing it out of the URL, is a deliberate security
  // property (web/src/app/pages/accept-invite.tsx:60-64) that keeps the token out
  // of history and referrers. A real invitee arrives by a fresh document load from
  // their email, which is what this now reproduces. Chasing the previous failure
  // into the component would have traded a security property for a green test.
  await page.goto('/login')
  await page.goto('/accept-invite#token=olvi_bogus_token')
  await page.locator('#invite-password').fill(PASSWORD)
  await page.locator('#invite-confirm').fill(PASSWORD)
  await page.getByRole('button', { name: /activate account/i }).click()
  await expect(page.getByRole('alert')).toContainText(/not valid/i)
})

// Full round-trip: admin invites → invitee accepts → invitee signs in. Blocked
// headlessly because POST /v1/onboard is gated behind an AAL3 step-up (hardware
// authenticator) that Playwright cannot satisfy with a password session — the
// same fail-closed gate onboarding-first-hour.spec.ts proves. Documented here so
// the flow is executable the day a headless AAL3 test credential exists.
test.skip('accept-invite: invite → accept → login round-trip (needs headless AAL3)', async ({
  page,
}) => {
  void page
})
