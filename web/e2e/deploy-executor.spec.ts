// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { expect, test, type Page } from '@playwright/test'

// Deploy on a fresh Community install, where no runtime executor is connected, and on an
// install that has one. scripts/web-e2e.sh runs this spec twice, each time on a virgin engine:
// once without an executor, once started with a test executor (OLIVARES_DEPLOY_EXECUTOR_CONFIG)
// and PLAYWRIGHT_DEPLOY_EXECUTOR=configured. Each run walks first boot.
const setupToken = process.env.PLAYWRIGHT_SETUP_TOKEN ?? ''
const executorConfigured =
  process.env.PLAYWRIGHT_DEPLOY_EXECUTOR === 'configured'
const shots = process.env.PLAYWRIGHT_SHOTS_DIR ?? 'playwright-report'
const EMAIL = 'admin@example.com'
const PASSWORD = 'correct-horse-battery-staple-42'

// The flow handles the one-time setup token and a password: no automatic artifacts.
test.use({ screenshot: 'off', trace: 'off' })
// Turning Deploy on restarts the engine once.
test.describe.configure({ timeout: 180_000 })

async function firstBoot(page: Page) {
  await page.goto('/setup')
  await page.locator('#token').fill(setupToken)
  await page.locator('#setup-email').fill(EMAIL)
  await page.locator('#setup-password').fill(PASSWORD)
  await page.getByRole('button', { name: /create administrator/i }).click()
  // Setup either signs the administrator in (the onboarding wizard) or hands over to /login.
  await page.waitForURL((url) => !url.pathname.startsWith('/setup'))
  if (new URL(page.url()).pathname.startsWith('/login')) {
    await page.locator('#email').fill(EMAIL)
    await page.locator('#password').fill(PASSWORD)
    await page.getByRole('button', { name: /^sign in$/i }).click()
    await page.waitForURL((url) => !url.pathname.startsWith('/login'))
  }
  // Both setup paths must paint the authenticated shell before callers open Deploy.
  await expect(
    page.getByRole('link', { name: 'Now', exact: true }),
  ).toBeVisible()
}

// Deploy is an optional module and a new installation starts with it off: the administrator
// turns it on from the page itself, the engine restarts into it and the page comes back.
async function openDeploy(page: Page) {
  await page.goto('/deploy')
  const heading = page.getByRole('heading', {
    name: 'Deployment & integration',
  })
  const turnOn = page.getByRole('button', { name: 'Turn on Deploy' })
  await expect(heading.or(turnOn)).toBeVisible()
  if (await turnOn.isVisible()) await turnOn.click()
  await expect(heading).toBeVisible({ timeout: 90_000 })
}

// The screen at 1280 and 390 px, light and dark.
async function capture(page: Page, name: string) {
  for (const width of [1280, 390]) {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 800 })
    for (const colorScheme of ['light', 'dark'] as const) {
      await page.emulateMedia({ colorScheme })
      await page.screenshot({
        path: `${shots}/${name}-${width}-${colorScheme}.png`,
        fullPage: true,
      })
    }
  }
  await page.setViewportSize({ width: 1280, height: 800 })
  await page.emulateMedia({ colorScheme: 'light' })
}

// A status code is engine vocabulary; the console never shows one.
async function expectNoStatusCode(page: Page) {
  await expect(page.locator('main')).not.toContainText(/\b503\b|\bHTTP\b/)
}

// The ledger tab resolves to its empty state; skeleton rows that never go away are a defect.
async function openOperations(page: Page) {
  await page.getByRole('tab', { name: 'Operations' }).click()
  await expect(page.locator('main [aria-busy="true"]')).toHaveCount(0)
  await expect(
    page.getByText('No deployment operations recorded yet'),
  ).toBeVisible()
}

test('without an executor, Deploy says what it needs before it offers a form', async ({
  page,
}) => {
  test.skip(
    !setupToken || executorConfigured,
    'needs a virgin engine without a runtime executor (scripts/web-e2e.sh)',
  )
  await firstBoot(page)
  await openDeploy(page)

  const empty = page.locator('main [data-slot="empty-state"]')
  await expect(empty).toContainText('Deploying needs a runtime executor')
  await expect(empty).toContainText('OLIVARES_DEPLOY_EXECUTOR_CONFIG')
  await expect(
    empty.getByRole('link', { name: 'How to connect an executor' }),
  ).toHaveAttribute(
    'href',
    'https://docs.olivares.ai/reference/modules/vii-deploy/#connect-an-executor',
  )
  // The page's primary slot does not open the declaration form.
  await expect(
    page.locator('[data-page-primary-action]').getByRole('button'),
  ).toHaveCount(0)
  await expectNoStatusCode(page)
  // Declaring desired state stays possible, as the quieter header action.
  await expect(
    page.getByRole('button', { name: 'Declare deployment' }),
  ).toHaveCount(1)
  await capture(page, 'deploy-no-executor')

  await openOperations(page)
  await expectNoStatusCode(page)
  await page.screenshot({ path: `${shots}/deploy-no-executor-operations.png` })

  await page.getByRole('tab', { name: 'Wirings' }).click()
  await expectNoStatusCode(page)
})

test('with an executor, Deploy offers the full declaration with a Subject that exists', async ({
  page,
}) => {
  test.skip(
    !setupToken || !executorConfigured,
    'needs a virgin engine started with OLIVARES_DEPLOY_EXECUTOR_CONFIG',
  )
  await firstBoot(page)

  // One agent to deploy, created through the public API the way an integrator would.
  const login = await page.request.post('/v1/auth/login', {
    data: { email: EMAIL, password: PASSWORD },
  })
  expect(login.status()).toBe(200)
  const { token } = (await login.json()) as { token: string }
  const who = await page.request.get('/v1/auth/whoami', {
    headers: { Authorization: `Bearer ${token}` },
  })
  const { grants } = (await who.json()) as { grants: { tenant: string }[] }
  const agent = await page.request.post('/v1/agents', {
    headers: {
      Authorization: `Bearer ${token}`,
      'X-Olivares-Tenant': grants[0].tenant,
    },
    data: { name: 'billing-bot', kind: 'claude-code' },
  })
  expect(agent.status(), await agent.text()).toBe(201)

  await openDeploy(page)
  const empty = page.locator('main [data-slot="empty-state"]')
  await expect(empty).toContainText('No deployments declared yet')
  await expectNoStatusCode(page)
  await capture(page, 'deploy-executor')

  await page
    .locator('[data-page-primary-action]')
    .getByRole('button', { name: 'Declare deployment' })
    .click()
  const dialog = page.getByRole('dialog', { name: 'Declare deployment' })
  await dialog.getByRole('combobox', { name: 'Subject', exact: true }).click()
  await page.getByRole('option', { name: 'billing-bot' }).click()
  await dialog.getByRole('textbox', { name: 'Name' }).fill('billing')
  await dialog.getByRole('textbox', { name: 'Environment' }).fill('prod')
  await dialog
    .getByRole('textbox', { name: 'Target' })
    .fill('docker.host/node1')
  await page.screenshot({ path: `${shots}/deploy-executor-form.png` })

  const created = page.waitForResponse(
    (r) =>
      r.request().method() === 'POST' &&
      new URL(r.url()).pathname === '/v1/m/deploy/definitions',
  )
  await dialog.getByRole('button', { name: 'Declare deployment' }).click()
  const response = await created
  expect(response.status()).toBe(201)
  expect(await response.json()).toMatchObject({
    subject_kind: 'agent',
    subject_ref: 'billing-bot',
    name: 'billing',
  })
  await expect(
    page.getByRole('gridcell', { name: 'billing', exact: true }),
  ).toBeVisible()

  await openOperations(page)
  await expectNoStatusCode(page)
  await page.screenshot({ path: `${shots}/deploy-executor-operations.png` })

  // Each ambiguity is isolated so an earlier case cannot force the fallback for it.
  for (const { agents, reference, screenshot } of [
    {
      agents: [
        { name: 'billing', external_id: 'billing-primary' },
        { name: '\u0085billing\u0085', external_id: 'billing-next-line' },
      ],
      reference: 'billing-next-line',
      screenshot: 'deploy-next-line-agent-form',
    },
    {
      agents: [
        { name: 'billing', external_id: 'billing-primary' },
        { name: ' billing ', external_id: 'billing-padded' },
      ],
      reference: 'billing-padded',
      screenshot: 'deploy-padded-agent-form',
    },
    {
      agents: [
        { name: 'ops', external_id: 'billing' },
        { name: 'billing', external_id: 'billing-2' },
      ],
      reference: 'billing-2',
      screenshot: 'deploy-agent-reference-collision-form',
    },
    {
      agents: [{ name: 'billing-bot', external_id: 'billing-secondary' }],
      reference: 'billing-secondary',
      screenshot: 'deploy-duplicate-agent-form',
    },
  ]) {
    const agentIDs: string[] = []
    for (const subject of agents) {
      const agent = await page.request.post('/v1/agents', {
        headers: {
          Authorization: `Bearer ${token}`,
          'X-Olivares-Tenant': grants[0].tenant,
        },
        data: { ...subject, kind: 'claude-code' },
      })
      expect(agent.status(), await agent.text()).toBe(201)
      const registered = (await agent.json()) as { id: string; name: string }
      expect(registered.name).toBe(subject.name)
      agentIDs.push(registered.id)
    }
    await page.reload()
    await page.getByRole('button', { name: 'Declare deployment' }).click()
    await dialog
      .getByRole('textbox', { name: 'Subject', exact: true })
      .fill(reference)
    await dialog.getByRole('textbox', { name: 'Name' }).fill(reference)
    await dialog.getByRole('textbox', { name: 'Environment' }).fill('prod')
    await dialog
      .getByRole('textbox', { name: 'Target' })
      .fill('docker.host/node1')
    await page.screenshot({ path: `${shots}/${screenshot}.png` })
    const created = page.waitForResponse(
      (r) =>
        r.request().method() === 'POST' &&
        new URL(r.url()).pathname === '/v1/m/deploy/definitions',
    )
    await dialog.getByRole('button', { name: 'Declare deployment' }).click()
    const response = await created
    expect(response.status()).toBe(201)
    expect(await response.json()).toMatchObject({
      subject_kind: 'agent',
      subject_ref: reference,
      name: reference,
    })
    await expect(dialog).not.toBeVisible()
    for (const id of agentIDs) {
      const deleted = await page.request.delete(`/v1/agents/${id}`, {
        headers: {
          Authorization: `Bearer ${token}`,
          'X-Olivares-Tenant': grants[0].tenant,
        },
      })
      expect(deleted.status()).toBe(204)
    }
  }

  // Capabilities is optional; declaring desired MCP state must not read its dormant routes.
  const headers = { Authorization: `Bearer ${token}` }
  const selection = await page.request.get('/v1/console/modules', { headers })
  expect(selection.status()).toBe(200)
  const { modules } = (await selection.json()) as {
    modules: { name: string; selected: boolean }[]
  }
  const disabled = await page.request.put('/v1/console/modules', {
    headers,
    data: {
      selected: modules
        .filter((m) => m.selected && m.name !== 'capabilities')
        .map((m) => m.name),
    },
  })
  expect(disabled.status()).toBe(200)
  await expect(async () => {
    const info = await page.request.get('/v1/server-info')
    expect(info.status()).toBe(200)
    expect((await info.json()).modules_not_enabled).toContain('capabilities')
  }).toPass({ timeout: 90_000 })
  const capabilityReads: string[] = []
  page.on('request', (request) => {
    if (new URL(request.url()).pathname.startsWith('/v1/m/capabilities/')) {
      capabilityReads.push(request.url())
    }
  })
  await page.reload()
  await page.getByRole('button', { name: 'Declare deployment' }).click()
  await dialog.getByRole('combobox', { name: 'Subject kind' }).click()
  await page.getByRole('option', { name: 'MCP server' }).click()
  await dialog
    .getByRole('textbox', { name: 'Subject', exact: true })
    .fill('github-mcp')
  await dialog.getByRole('textbox', { name: 'Name' }).fill('github-deploy')
  await dialog.getByRole('textbox', { name: 'Environment' }).fill('prod')
  await dialog
    .getByRole('textbox', { name: 'Target' })
    .fill('docker.host/node1')
  await page.screenshot({ path: `${shots}/deploy-capabilities-off-form.png` })
  const mcpCreated = page.waitForResponse(
    (r) =>
      r.request().method() === 'POST' &&
      new URL(r.url()).pathname === '/v1/m/deploy/definitions',
  )
  await dialog.getByRole('button', { name: 'Declare deployment' }).click()
  const mcpResponse = await mcpCreated
  expect(mcpResponse.status()).toBe(201)
  expect(await mcpResponse.json()).toMatchObject({
    subject_kind: 'mcp_server',
    subject_ref: 'github-mcp',
    name: 'github-deploy',
  })
  await expect(dialog).not.toBeVisible()
  expect(capabilityReads).toEqual([])
})
