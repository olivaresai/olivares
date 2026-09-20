// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { expect, test } from '@playwright/test'

// Drives the REAL Claude Code OPERATE portal against a live olivares binary
// serving the embedded bundle: first-boot setup → login → navigate to the portal →
// the governed empty states, the create form, and the workspaces tab.
//
// The full create→attach→stop LIFECYCLE needs a real `claude` binary + a wired
// Runner/CredentialSource (deny-closed without them — there is no `claude` in CI), so
// that path is proven by the modules/sessions fakeRunner tests (runtime_test.go,
// runtime_governance_test.go). This e2e proves the operator-facing portal is wired,
// navigable and honest with no SSH. scripts/web-e2e.sh boots the engine and exports
// the one-time setup token; without it the test is skipped.
const setupToken = process.env.PLAYWRIGHT_SETUP_TOKEN ?? ''
const PASSWORD = 'correct-horse-battery-staple-42'

test('Claude Code portal: setup → login → navigate → create form → workspaces', async ({
  page,
}) => {
  test.skip(
    !setupToken,
    'PLAYWRIGHT_SETUP_TOKEN not set — run via scripts/web-e2e.sh',
  )

  // First boot → setup → login (superadmin sees every nav group).
  await page.goto('/setup')
  await page.locator('#token').fill(setupToken)
  await page.locator('#setup-email').fill('admin@example.com')
  await page.locator('#setup-password').fill(PASSWORD)
  await page.getByRole('button', { name: /create administrator/i }).click()

  await page.waitForURL('**/login')
  await page.locator('#email').fill('admin@example.com')
  await page.locator('#password').fill(PASSWORD)
  await page.getByRole('button', { name: /^sign in$/i }).click()

  // THE ENTRY IS INSIDE A NAV AREA, AND SIGN-IN LANDS ON THE ROOT. The sidebar
  // renders a closed area's panel hidden, and the root belongs to no area, so this
  // link is not painted yet and waiting for it here waits forever. The portal is
  // reached by its own address first; that makes its area the active one, which is
  // what paints the entry the operator actually uses — so the entry is still
  // asserted, and still clicked.
  await page.goto('/agentops')

  // The portal is registered in the sidebar as "Operate sessions". `exact` is
  // load-bearing: neighbouring entries still mention Claude Code, so a substring
  // match would resolve to more than one link.
  const navLink = page.getByRole('link', { name: 'Operate sessions', exact: true })
  await expect(navLink).toBeVisible()
  await navLink.click()

  // The portal renders its heading and the honest empty state (a fresh estate has no
  // sessions at all yet) — no SSH, no fabricated rows. The heading is the operate
  // room's own title; the nav entry that opens it is the broader "Operate sessions".
  await expect(
    page.getByRole('heading', { name: 'Claude Code' }),
  ).toBeVisible()
  await expect(page.getByText('No sessions yet')).toBeVisible()
  // The origin facet is the whole point of the merge: one place to look, whether the
  // session was discovered or launched.
  await expect(page.getByLabel('All sources')).toBeVisible()

  // The create form is the visual equivalent of the CLI launch.
  await page.getByRole('button', { name: /New session/i }).click()
  await expect(
    page.getByText('New Claude Code session'),
  ).toBeVisible()
  // The privileged-mode warning must be honest BEFORE launch, so this actually
  // PICKS bypassPermissions and asserts the warning — the previous version only
  // clicked at the control and asserted nothing, and it clicked at a role that does
  // not exist: the trigger is a <button> that sets role="combobox" explicitly, so
  // the explicit role wins and getByRole('button') can never match it. Measured in
  // Chromium: <BUTTON role="combobox" aria-label="Permission mode">.
  await page
    .getByRole('combobox', { name: /Permission mode/i })
    .first()
    .click()
  await page.getByRole('option', { name: 'bypassPermissions' }).click()
  await expect(page.getByText(/privileged launch/i).first()).toBeVisible()

  // Close the SELECT, then the DIALOG. Two Escapes, not one: the first only
  // dismisses whatever popup is on top, and a dialog left open swallows every click
  // that follows — which is how this reads as "the Workspaces tab is missing".
  await page.keyboard.press('Escape')
  await page.keyboard.press('Escape')
  await expect(page.getByText('New Claude Code session')).toBeHidden()

  // The workspaces tab is the governed file plane.
  await page.getByRole('tab', { name: 'Workspaces' }).click()
  await expect(page.getByText('No workspaces registered')).toBeVisible()

  await page.screenshot({ path: 'playwright-report/agentops.png' })
})
