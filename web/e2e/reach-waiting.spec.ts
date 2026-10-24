// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Reach on the real binary (issue #2553): the MCP servers the access map shows for
// the seeded sessions are solid under their tool, and a new network request one of
// those sessions opens is dashed from the same tool until it is approved. Runs on
// the seeded demo estate (scripts/web-e2e-demo.sh e2e/reach-waiting.spec.ts -g
// 'Reach real use').
import { expect, test, type Page } from '@playwright/test'
import {
  browserSessionHeaders,
  journeyNowLink,
  type BrowserSession,
} from './browser-session'

const demoTenant = process.env.DEMO_TENANT ?? ''
const EMAIL = 'demo@olivares.local'
const PASSWORD = 'olivares-demo-estate'
const HOST = 'registry.terraform.io'
// The live Claude Code session of the seed (cmd/olivares/seed/seed.go SessionLive).
const SESSION = 'sess-coder-7a3f'
const SOLID_MCP =
  /Claude Code reaches filesystem \(MCP server\), github \(MCP server\)\./
// Separation of duty: the person who asks cannot approve, so a second one does.
const APPROVER_EMAIL = 'reach-approver@olivares.local'
const APPROVER_PASSWORD = 'reach-approver-passphrase'

async function signIn(
  page: Page,
  email = EMAIL,
  password = PASSWORD,
): Promise<BrowserSession> {
  await page.goto('/login')
  await page.locator('input[type=email]').first().fill(email)
  await page.locator('input[type=password]').first().fill(password)
  const login = page.waitForResponse(
    (r) =>
      r.url().endsWith('/v1/auth/login') && r.request().method() === 'POST',
  )
  await page.locator('button[type=submit]').first().click()
  const response = await login
  expect(response.status(), 'demo sign-in').toBe(200)
  await page.waitForURL((url) => !url.pathname.startsWith('/login'))
  return (await response.json()) as BrowserSession
}

test.describe('Reach real use', () => {
  test.skip(!demoTenant, 'needs the seeded demo estate (DEMO_TENANT)')

  test('session MCP reach is solid and a new network request is dashed until it is approved', async ({
    page,
    browser,
  }) => {
    const session = await signIn(page)
    const headers = browserSessionHeaders(session, demoTenant)

    const created = await page.request.post('/v1/m/governance/approvals', {
      headers,
      data: {
        action: 'network.connect',
        subject_kind: 'network_destination',
        subject_ref: HOST,
        session_ref: SESSION,
        reason: 'The provider upgrade reaches the Terraform registry.',
      },
    })
    expect(created.status(), await created.text()).toBe(201)
    const approval = (await created.json()) as { id: string; status: string }
    expect(approval.status).toBe('pending')

    await page.goto('/reach')
    const graph = page.getByRole('img', { name: /Reach graph/ })
    await expect(graph).toHaveAccessibleName(SOLID_MCP)
    await expect(graph).toHaveAccessibleName(
      /Claude Code is waiting for approval to reach registry\.terraform\.io \(Network endpoint\)\./,
    )
    await expect(page.locator(`[data-dest="${HOST}"]`)).toHaveClass(/is-wait/)
    for (const mcp of ['github', 'filesystem'])
      await expect(page.locator(`[data-dest="${mcp}"]`)).not.toHaveClass(
        /is-wait/,
      )
    await expect(page.locator('path.edge:not(.is-wait)')).toHaveCount(2)
    await expect(page.locator('path.edge.is-wait')).toHaveCount(1)
    await page.screenshot({
      path: 'test-results/reach-waiting.png',
      fullPage: true,
    })

    // Leave Reach inside the app, so coming back reads through its query cache.
    await journeyNowLink(page).click()
    await page.waitForURL((url) => !url.pathname.startsWith('/reach'))

    const approver = await page.request.post('/v1/users', {
      headers,
      data: {
        email: APPROVER_EMAIL,
        display_name: 'Reach approver',
        password: APPROVER_PASSWORD,
        superadmin: true,
      },
    })
    expect(approver.status(), await approver.text()).toBe(201)
    const other = await browser.newContext()
    const otherPage = await other.newPage()
    const otherSession = await signIn(
      otherPage,
      APPROVER_EMAIL,
      APPROVER_PASSWORD,
    )
    const decided = await otherPage.request.post(
      `/v1/m/governance/approvals/${encodeURIComponent(approval.id)}/decisions`,
      {
        headers: browserSessionHeaders(otherSession, demoTenant),
        data: { decision: 'approve', note: 'Registry allowed.' },
      },
    )
    expect(decided.status(), await decided.text()).toBeLessThan(300)
    expect(((await decided.json()) as { status: string }).status).toBe(
      'approved',
    )
    await other.close()

    await page
      .getByRole('navigation', { name: 'Journeys', exact: true })
      .getByRole('link', { name: 'Tools', exact: true })
      .click()
    await page.getByRole('link', { name: 'Reach', exact: true }).click()
    await page.waitForURL((url) => url.pathname === '/reach')
    await expect(page.getByText(/destinations/).first()).toBeVisible()
    await expect(page.locator(`[data-dest="${HOST}"]`)).toHaveCount(0)
    await expect(
      page.getByRole('img', { name: /Reach graph/ }),
    ).toHaveAccessibleName(SOLID_MCP)
    await page.screenshot({
      path: 'test-results/reach-approved.png',
      fullPage: true,
    })
  })
})
