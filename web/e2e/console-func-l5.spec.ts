// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import {
  expect,
  test,
  type Locator,
  type Page,
  type Response,
} from '@playwright/test'

// CONSOLE-FUNC-01 lot 5 runs only against a disposable --seed-demo engine. No
// response is fulfilled or rewritten by Playwright: every HTTP assertion below
// observes the engine that also serves the embedded console bundle.
const demoTenant = process.env.DEMO_TENANT ?? ''
const DEMO_EMAIL = 'demo@olivares.local'
const DEMO_PASSWORD = 'olivares-demo-estate'
const SECOND_ADMIN_EMAIL = 'l5-second-admin@olivares.local'
const SECOND_ADMIN_PASSWORD = 'l5-second-admin-passphrase'
const BACKUP_PASSPHRASE = 'l5-disposable-passphrase'

test.use({ trace: 'off' })
test.describe.configure({ mode: 'serial', timeout: 240_000 })

interface ListBody<T> {
  items: T[]
  has_more?: boolean
}

interface HealthCheck {
  id: string
  name?: string
  subject_ref: string
  desired_status: string
  state: string
}

interface Org {
  tenant_id: string
  name: string
  slug: string
  status: string
}

interface NotifyRoute {
  id: string
  name: string
  enabled: boolean
  destination: string
}

interface Backup {
  id: string
  filename: string
  notes: string
}

function authHeaders(token: string): Record<string, string> {
  return {
    Authorization: `Bearer ${token}`,
    'X-Olivares-Tenant': demoTenant,
  }
}

async function loginDemo(
  page: Page,
  email = DEMO_EMAIL,
  password = DEMO_PASSWORD,
): Promise<string> {
  const loginRead = page.waitForResponse((response) => {
    const url = new URL(response.url())
    return (
      response.request().method() === 'POST' &&
      url.pathname === '/v1/auth/login'
    )
  })
  await page.goto('/login')
  await page.locator('#email').fill(email)
  await page.locator('#password').fill(password)
  await page.getByRole('button', { name: /^sign in$/i }).click()
  const response = await loginRead
  expect(response.status(), 'the disposable demo login must be real').toBe(200)
  const body = (await response.json()) as { token: string }
  await expect(
    page.getByRole('link', { name: 'Inventory', exact: true }),
  ).toBeVisible({
    timeout: 20_000,
  })
  return body.token
}

function observe(
  page: Page,
  method: string,
  path: string,
  status?: number,
): Promise<Response> {
  return page.waitForResponse(
    (response) => {
      const url = new URL(response.url())
      return (
        response.request().method() === method &&
        url.pathname === path &&
        (status === undefined || response.status() === status)
      )
    },
    { timeout: 30_000 },
  )
}

async function getJSON<T>(page: Page, token: string, path: string): Promise<T> {
  const response = await page.request.get(path, { headers: authHeaders(token) })
  expect(response.ok(), `GET ${path}`).toBe(true)
  return (await response.json()) as T
}

async function ensureCheck(
  page: Page,
  token: string,
  subjectRef: string,
  name: string,
): Promise<HealthCheck> {
  const listed = await getJSON<ListBody<HealthCheck>>(
    page,
    token,
    '/v1/m/health/checks?limit=1000',
  )
  const existing = listed.items.find((item) => item.subject_ref === subjectRef)
  if (existing) return existing
  const response = await page.request.post('/v1/m/health/checks', {
    headers: authHeaders(token),
    data: {
      name,
      subject_kind: 'agent',
      subject_ref: subjectRef,
      expected_interval_seconds: 60,
      grace_factor: 2,
      sla_target_ppm: 999000,
      desired_status: 'active',
    },
  })
  expect(response.status(), 'health seed create').toBe(201)
  return (await response.json()) as HealthCheck
}

async function deleteCheckBySubject(
  page: Page,
  token: string,
  subjectRef: string,
): Promise<void> {
  const listed = await getJSON<ListBody<HealthCheck>>(
    page,
    token,
    '/v1/m/health/checks?limit=1000',
  )
  const existing = listed.items.find((item) => item.subject_ref === subjectRef)
  if (!existing) return
  const response = await page.request.delete(
    `/v1/m/health/checks/${existing.id}`,
    {
      headers: authHeaders(token),
    },
  )
  expect(response.status()).toBe(204)
}

async function reportHealth(
  page: Page,
  token: string,
  checkId: string,
  state: 'healthy' | 'degraded' | 'down',
): Promise<void> {
  const response = await page.request.post(
    `/v1/m/health/checks/${checkId}/report`,
    {
      headers: authHeaders(token),
      data: {
        state,
        latency_ms: state === 'down' ? 321 : 12,
        detail: 'l5-disposable',
      },
    },
  )
  expect(response.status(), `report ${state}`).toBe(200)
}

async function ensureOrg(
  page: Page,
  token: string,
  slug: string,
  name: string,
): Promise<Org> {
  let listed = await getJSON<ListBody<Org>>(page, token, '/v1/system/orgs')
  const old = listed.items.find((org) => org.slug === slug)
  if (old) {
    const removed = await page.request.delete(
      `/v1/system/orgs/${old.tenant_id}`,
      {
        headers: authHeaders(token),
      },
    )
    expect(removed.status(), `remove stale ${slug}`).toBe(204)
  }
  const created = await page.request.post('/v1/system/orgs', {
    headers: authHeaders(token),
    data: { name, slug, data_region: '' },
  })
  expect(created.status(), `create disposable org ${slug}`).toBe(201)
  const org = (await created.json()) as Org
  listed = await getJSON<ListBody<Org>>(page, token, '/v1/system/orgs')
  expect(listed.items.some((item) => item.tenant_id === org.tenant_id)).toBe(
    true,
  )
  return org
}

async function ensureRoute(page: Page, token: string): Promise<NotifyRoute> {
  const listed = await getJSON<ListBody<NotifyRoute>>(
    page,
    token,
    '/v1/m/notify/routes',
  )
  for (const route of listed.items.filter((item) =>
    item.name.startsWith('l5-route'),
  )) {
    const removed = await page.request.delete(
      `/v1/m/notify/routes/${route.id}`,
      {
        headers: authHeaders(token),
      },
    )
    expect(removed.status()).toBe(204)
  }
  const created = await page.request.post('/v1/m/notify/routes', {
    headers: authHeaders(token),
    data: {
      name: 'l5-route-live',
      enabled: true,
      destination: 'l5-unprovisioned-sink',
      match_types: ['finding.reported'],
      match_kinds: [],
      match_sources: [],
      match_subject_kinds: [],
      min_severity: '',
      dedup_window_seconds: 0,
      throttle_window_seconds: 0,
      priority: 7,
    },
  })
  expect(created.status()).toBe(201)
  return (await created.json()) as NotifyRoute
}

async function rowFor(page: Page, text: string): Promise<Locator> {
  const cell = page.getByText(text, { exact: true }).first()
  await expect(cell).toBeVisible({ timeout: 30_000 })
  return cell.locator('xpath=ancestor::tr[1]')
}

async function cardFor(page: Page, text: string): Promise<Locator> {
  const label = page.getByText(text, { exact: true }).first()
  await expect(label).toBeVisible({ timeout: 30_000 })
  return label.locator('..')
}

async function selectEndpoint(
  page: Page,
  method: string,
  path: string,
): Promise<void> {
  await page.getByRole('textbox', { name: 'Filter endpoints…' }).fill(path)
  const tree = page.getByRole('navigation', { name: 'API endpoints' })
  const candidate = tree.getByRole('button', {
    name: `${method} ${path}`,
    exact: true,
  })
  await expect(candidate).toHaveCount(1)
  await candidate.click()
  await expect(candidate).toHaveAttribute('aria-current', 'true')
}

test.beforeEach(async ({ page }) => {
  test.skip(
    !demoTenant,
    'DEMO_TENANT not set — launch a disposable seeded engine',
  )
  await page.addInitScript(
    ([tenant]) => {
      localStorage.setItem(
        'olivares.tenant',
        JSON.stringify({ state: { activeTenant: tenant }, version: 0 }),
      )
      localStorage.setItem('olivares.lang', 'en')
      localStorage.setItem('olivares.theme', 'light')
      localStorage.removeItem('olivares:api-playground')
    },
    [demoTenant],
  )
})

test('health controls fire real handlers and detail links reach their destinations', async ({
  page,
}) => {
  const token = await loginDemo(page)
  const check = await ensureCheck(page, token, 'l5-live-agent', 'L5 live agent')
  const alternateCheck = await ensureCheck(
    page,
    token,
    'l5-alternate-agent',
    'L5 alternate agent',
  )
  await reportHealth(page, token, check.id, 'healthy')
  await reportHealth(page, token, check.id, 'down')
  await reportHealth(page, token, alternateCheck.id, 'healthy')
  await deleteCheckBySubject(page, token, 'l5-ui-created')

  const loaded = observe(page, 'GET', '/v1/m/health/status', 200)
  await page.goto('/health')
  await loaded
  // Read the same live handler through the authenticated request context. On a
  // busy host Chromium can retain a navigation response body behind the health
  // SSE connection even though the engine already completed it; the API context
  // makes the body ownership explicit while the browser response above proves
  // the console issued its own GET.
  const statusBody = await getJSON<ListBody<HealthCheck>>(
    page,
    token,
    '/v1/m/health/status',
  )
  expect(
    statusBody.items.some((item) => item.subject_ref === 'l5-live-agent'),
  ).toBe(true)
  await expect(
    page.getByRole('heading', { name: 'Health & SLA' }),
  ).toBeVisible()
  await expect(await rowFor(page, 'L5 live agent')).toContainText('Down')
  const alternateSubject = statusBody.items.find(
    (item) => item.subject_ref !== 'l5-live-agent',
  )
  expect(
    alternateSubject,
    'seed-demo must expose a second health subject',
  ).toBeTruthy()

  const publicLink = page.getByRole('link', { name: 'Public status page' })
  await expect(publicLink).toHaveAttribute('href', '/status-page')
  await expect(publicLink).toHaveAttribute('target', '_blank')

  const refreshed = observe(page, 'GET', '/v1/m/health/status', 200)
  await page.getByRole('button', { name: 'Refresh' }).last().click()
  await refreshed

  const downRead = observe(page, 'GET', '/v1/m/health/status', 200)
  await page.getByRole('combobox', { name: 'All states' }).click()
  await page.getByRole('option', { name: 'Down', exact: true }).click()
  expect(new URL((await downRead).url()).searchParams.get('state')).toBe('down')
  const agentRead = observe(page, 'GET', '/v1/m/health/status', 200)
  await page.getByRole('combobox', { name: 'All kinds' }).click()
  await page.getByRole('option', { name: 'Agent', exact: true }).click()
  expect(
    new URL((await agentRead).url()).searchParams.get('subject_kind'),
  ).toBe('agent')
  await page.getByPlaceholder('Search subject…').fill('l5-live')
  await expect(await rowFor(page, 'L5 live agent')).toBeVisible()
  const allStatesRead = observe(page, 'GET', '/v1/m/health/status', 200)
  await page.getByRole('combobox', { name: 'All states' }).click()
  await page.getByRole('option', { name: 'All states', exact: true }).click()
  await allStatesRead

  await (await rowFor(page, 'L5 live agent')).click()
  let sheet = page.getByRole('dialog')
  await expect(sheet).toContainText('l5-live-agent')
  const slaRead = observe(page, 'GET', '/v1/m/health/sla', 200)
  await sheet.getByRole('button', { name: 'View SLA' }).click()
  await expect(page.getByRole('tab', { name: 'SLA' })).toHaveAttribute(
    'aria-selected',
    'true',
  )
  const slaURL = new URL((await slaRead).url())
  expect(slaURL.searchParams.get('subject_ref')).toBe('l5-live-agent')
  const windowRead = observe(page, 'GET', '/v1/m/health/sla', 200)
  await page.getByRole('combobox', { name: 'Window' }).click()
  await page.getByRole('option', { name: '24h' }).click()
  expect(
    new URL((await windowRead).url()).searchParams.get('window_seconds'),
  ).toBe('86400')
  const alternateSlaRead = observe(page, 'GET', '/v1/m/health/sla', 200)
  await page.getByRole('combobox', { name: 'Pick a subject…' }).click()
  await page
    .getByRole('option', {
      name: alternateSubject!.name || alternateSubject!.subject_ref,
      exact: true,
    })
    .click()
  expect(
    new URL((await alternateSlaRead).url()).searchParams.get('subject_ref'),
  ).toBe(alternateSubject!.subject_ref)

  await page.getByRole('tab', { name: 'Status' }).click()
  await (await rowFor(page, 'L5 live agent')).click()
  sheet = page.getByRole('dialog')
  const timelineRead = observe(page, 'GET', '/v1/m/health/events', 200)
  await sheet.getByRole('button', { name: 'View timeline' }).click()
  await expect(page.getByRole('tab', { name: 'Timeline' })).toHaveAttribute(
    'aria-selected',
    'true',
  )
  const timelineURL = new URL((await timelineRead).url())
  expect(timelineURL.searchParams.get('subject_ref')).toBe('l5-live-agent')
  const alternateTimelineRead = observe(page, 'GET', '/v1/m/health/events', 200)
  await page.getByRole('combobox', { name: 'Pick a subject…' }).click()
  await page
    .getByRole('option', {
      name: alternateSubject!.name || alternateSubject!.subject_ref,
      exact: true,
    })
    .click()
  expect(
    new URL((await alternateTimelineRead).url()).searchParams.get(
      'subject_ref',
    ),
  ).toBe(alternateSubject!.subject_ref)

  // Exercise the sheet's own dismiss action as a distinct control from its two
  // destination buttons: those buttons close it by changing parent state.
  await page.getByRole('tab', { name: 'Status' }).click()
  await (await rowFor(page, 'L5 live agent')).click()
  sheet = page.getByRole('dialog')
  await expect(sheet).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(sheet).not.toBeVisible()

  const checksRead = observe(page, 'GET', '/v1/m/health/checks', 200)
  await page.getByRole('tab', { name: 'Checks' }).click()
  await checksRead
  await page.getByRole('button', { name: 'About probe reports' }).hover()
  await expect(
    page.getByText(/Probe report ingestion is machine-facing/i),
  ).toBeVisible()
  await page.getByRole('button', { name: 'Create check' }).click()
  let dialog = page.getByRole('dialog')
  await dialog.getByRole('combobox', { name: 'Subject kind' }).click()
  await page.getByRole('option', { name: 'MCP', exact: true }).click()
  await dialog.getByRole('combobox', { name: 'Desired status' }).click()
  await page.getByRole('option', { name: 'Paused', exact: true }).click()
  await dialog.getByRole('button', { name: 'Cancel' }).click()
  await expect(dialog).not.toBeVisible()
  await page.getByRole('button', { name: 'Create check' }).click()
  dialog = page.getByRole('dialog')
  await dialog.getByLabel('Subject reference').fill('l5-ui-created')
  await dialog.getByLabel('Name').fill('L5 UI created')
  await dialog.getByLabel('Expected interval (seconds)').fill('90')
  await dialog.getByLabel('Grace factor').fill('3')
  await dialog.getByLabel('SLA target (PPM)').fill('998000')
  const created = observe(page, 'POST', '/v1/m/health/checks', 201)
  await dialog.getByRole('button', { name: 'Create check' }).click()
  const createdResponse = await created
  const createdBody = createdResponse.request().postDataJSON() as Record<
    string,
    unknown
  >
  const createdCheck = (await createdResponse.json()) as HealthCheck
  expect(createdBody).toMatchObject({
    subject_ref: 'l5-ui-created',
    expected_interval_seconds: 90,
    grace_factor: 3,
    sla_target_ppm: 998000,
  })

  await page
    .getByRole('button', { name: 'Edit health check for L5 UI created' })
    .click()
  dialog = page.getByRole('dialog')
  await dialog.getByLabel('Name').fill('L5 UI renamed')
  const edited = observe(
    page,
    'PUT',
    `/v1/m/health/checks/${createdCheck.id}`,
    200,
  )
  await dialog.getByRole('button', { name: 'Save' }).click()
  await edited

  let lifecycle = observe(
    page,
    'PUT',
    `/v1/m/health/checks/${createdCheck.id}`,
    200,
  )
  await page
    .getByRole('button', { name: 'Pause health check for L5 UI renamed' })
    .click()
  expect(
    ((await lifecycle).request().postDataJSON() as { desired_status: string })
      .desired_status,
  ).toBe('paused')
  lifecycle = observe(
    page,
    'PUT',
    `/v1/m/health/checks/${createdCheck.id}`,
    200,
  )
  await page
    .getByRole('button', { name: 'Resume health check for L5 UI renamed' })
    .click()
  expect(
    ((await lifecycle).request().postDataJSON() as { desired_status: string })
      .desired_status,
  ).toBe('active')
  lifecycle = observe(
    page,
    'PUT',
    `/v1/m/health/checks/${createdCheck.id}`,
    200,
  )
  await page
    .getByRole('button', { name: 'Retire health check for L5 UI renamed' })
    .click()
  expect(
    ((await lifecycle).request().postDataJSON() as { desired_status: string })
      .desired_status,
  ).toBe('retired')
  // The DataTable indexes the subject_ref accessor; the friendly name is rendered
  // inside that cell but is deliberately not a separate searchable column.
  await page.getByPlaceholder('Search health checks…').fill('l5-ui-created')
  await page
    .getByRole('button', { name: 'Delete health check for L5 UI renamed' })
    .click()
  dialog = page.getByRole('dialog')
  const deleted = observe(
    page,
    'DELETE',
    `/v1/m/health/checks/${createdCheck.id}`,
    204,
  )
  await dialog.getByRole('button', { name: 'Delete' }).click()
  await deleted
  const afterDelete = await getJSON<ListBody<HealthCheck>>(
    page,
    token,
    '/v1/m/health/checks?limit=1000',
  )
  expect(afterDelete.items.some((item) => item.id === createdCheck.id)).toBe(
    false,
  )

  await page.getByRole('tab', { name: 'Incidents' }).click()
  const allIncidents = observe(page, 'GET', '/v1/m/health/incidents', 200)
  await page.getByRole('combobox', { name: 'All' }).click()
  await page.getByRole('option', { name: 'All', exact: true }).click()
  await allIncidents
  await page.getByPlaceholder('Search incident…').fill('l5-live-agent')
  const resolveButton = page.getByRole('button', { name: 'Resolve' }).first()
  await expect(resolveButton).toBeVisible()
  const incidentRow = resolveButton.locator('xpath=ancestor::tr[1]')
  await expect(incidentRow).toContainText('l5-live-agent')
  const incidentResponse = page.waitForResponse(
    (response) =>
      response.request().method() === 'POST' &&
      /\/v1\/m\/health\/incidents\/[^/]+\/resolve$/.test(
        new URL(response.url()).pathname,
      ),
  )
  await resolveButton.click()
  expect((await incidentResponse).status()).toBe(200)

  const connectorsRead = observe(page, 'GET', '/v1/connectors/health', 200)
  await page.getByRole('tab', { name: 'Connectors' }).click()
  await connectorsRead
  const connectorsRefresh = observe(page, 'GET', '/v1/connectors/health', 200)
  await page.getByRole('button', { name: 'Refresh' }).last().click()
  await connectorsRefresh
  await page.getByRole('combobox', { name: 'Mode filter' }).click()
  await page.getByRole('option', { name: 'Export', exact: true }).click()
  await page.getByPlaceholder('Search connector…').fill('l5-no-connector')

  const dependenciesRead = observe(
    page,
    'GET',
    '/v1/m/health/dependencies',
    200,
  )
  await page.getByRole('tab', { name: 'Dependencies' }).click()
  await dependenciesRead
  const graphNode = page.locator('.react-flow__node').first()
  await expect(graphNode).toBeVisible()
  await graphNode.click()
  await expect(graphNode).toHaveClass(/selected/)
  const graphViewport = page.locator('.react-flow__viewport')
  const transformBeforeZoom = await graphViewport.getAttribute('style')
  await page.getByRole('button', { name: 'Zoom in' }).click()
  await expect
    .poll(() => graphViewport.getAttribute('style'))
    .not.toBe(transformBeforeZoom)
  await page.getByRole('button', { name: 'Zoom out' }).click()
  await page.getByRole('button', { name: 'Fit view' }).click()

  const systemRead = observe(page, 'GET', '/v1/console/health-summary', 200)
  const keysRead = observe(page, 'GET', '/v1/console/keys', 200)
  await page.getByRole('tab', { name: 'System' }).click()
  await systemRead
  const keyInventory = (await (await keysRead).json()) as {
    keys: Array<{ purpose: string; fingerprint?: string }>
  }
  expect(
    keyInventory.keys.some(
      (key) => key.purpose === 'audit' && Boolean(key.fingerprint),
    ),
  ).toBe(true)
  await page
    .getByRole('button', { name: 'Copy full fingerprint for audit' })
    .click()
  await expect(page.getByText('Fingerprint copied')).toBeVisible()
  const updateCheck = observe(page, 'POST', '/v1/console/update-check', 501)
  await page.getByRole('button', { name: 'Check now' }).click()
  await updateCheck
})

test('alerting route lifecycle, delivery log and dead-letter requeue are live', async ({
  page,
}) => {
  const token = await loginDemo(page)
  const route = await ensureRoute(page, token)
  // The route-test endpoint records a synchronous delivery but deliberately does
  // not fabricate durable outbox work. Drive a real health transition over the
  // event bus so the DLQ assertion below observes the production pump instead.
  await deleteCheckBySubject(page, token, 'l5-alert-dlq')
  const dlqCheck = await ensureCheck(
    page,
    token,
    'l5-alert-dlq',
    'L5 alert DLQ signal',
  )
  await reportHealth(page, token, dlqCheck.id, 'down')
  await expect
    .poll(
      async () => {
        const outbox = await getJSON<
          ListBody<{ route_ref?: string; status: string }>
        >(page, token, '/v1/m/notify/outbox?status=dead&limit=100')
        return outbox.items.some(
          (item) => item.route_ref === route.id && item.status === 'dead',
        )
      },
      {
        message: 'the real notify pump must dead-letter the health signal',
        timeout: 30_000,
      },
    )
    .toBe(true)
  const loaded = observe(page, 'GET', '/v1/m/notify/routes', 200)
  // TestSignalPanel is part of the default Routes panel, so its catalog read is
  // eager with navigation; attach before goto rather than after paint.
  const matchTypes = observe(page, 'GET', '/v1/m/notify/match-types', 200)
  await page.goto('/alerting')
  await loaded
  const matchTypesResponse = await matchTypes
  await expect(page.getByText('l5-route-live', { exact: true })).toBeVisible()

  const destinations = observe(page, 'GET', '/v1/m/notify/destinations', 200)
  await page.getByRole('button', { name: 'New route' }).click()
  let dialog = page.getByRole('dialog')
  expect(
    ((await (await destinations).json()) as { destinations: string[] })
      .destinations,
  ).toEqual([])
  expect(
    ((await matchTypesResponse.json()) as { match_types: unknown[] })
      .match_types.length,
  ).toBeGreaterThan(0)
  const minSeverity = dialog.getByRole('combobox').nth(1)
  await expect(minSeverity).toBeVisible()
  await minSeverity.click()
  await page.getByRole('option', { name: 'High', exact: true }).click()
  await dialog.getByRole('checkbox').first().click()
  await dialog.getByRole('switch', { name: 'Enabled' }).click()
  await dialog.getByRole('button', { name: 'Cancel' }).click()

  const routeRow = await rowFor(page, 'l5-route-live')
  await routeRow.getByRole('checkbox').click()
  let updated = observe(page, 'PUT', `/v1/m/notify/routes/${route.id}`, 200)
  await page.getByRole('button', { name: 'Disable selected' }).click()
  expect(
    ((await updated).request().postDataJSON() as { enabled: boolean }).enabled,
  ).toBe(false)
  updated = observe(page, 'PUT', `/v1/m/notify/routes/${route.id}`, 200)
  await page.getByRole('button', { name: 'Enable selected' }).click()
  expect(
    ((await updated).request().postDataJSON() as { enabled: boolean }).enabled,
  ).toBe(true)
  await page.getByRole('button', { name: 'Clear selection' }).click()
  await expect(
    page.getByRole('button', { name: 'Clear selection' }),
  ).toHaveCount(0)

  const revisions = observe(
    page,
    'GET',
    `/v1/m/notify/routes/${route.id}/revisions`,
    200,
  )
  await page
    .getByRole('button', { name: 'View revision history for l5-route-live' })
    .click()
  await revisions
  await expect(page.getByRole('dialog')).toContainText('Revision history')
  const restored = observe(
    page,
    'POST',
    `/v1/m/notify/routes/${route.id}/restore`,
    200,
  )
  await page
    .getByRole('button', { name: 'Restore this revision' })
    .first()
    .click()
  await page.getByRole('button', { name: 'Restore revision' }).click()
  await restored
  const historyDialog = page.getByRole('dialog')
  await historyDialog.getByRole('button', { name: 'Close' }).click()
  await expect(historyDialog).not.toBeVisible()

  const tested = observe(
    page,
    'POST',
    `/v1/m/notify/routes/${route.id}/test`,
    200,
  )
  await page.getByRole('button', { name: 'Test route l5-route-live' }).click()
  const testedBody = (await (await tested).json()) as {
    status: string
    destination: string
  }
  expect(testedBody).toEqual(
    expect.objectContaining({
      status: 'unknown_destination',
      destination: 'l5-unprovisioned-sink',
    }),
  )

  await page.getByRole('button', { name: 'Edit route l5-route-live' }).click()
  dialog = page.getByRole('dialog')
  await expect(dialog.getByLabel('Name')).toBeDisabled()
  await dialog.getByLabel('Priority').fill('8')
  updated = observe(page, 'PUT', `/v1/m/notify/routes/${route.id}`, 200)
  await dialog.getByRole('button', { name: 'Save route' }).click()
  expect(
    (await updated).request().postDataJSON() as {
      name: string
      priority: number
    },
  ).toEqual(expect.objectContaining({ name: 'l5-route-live', priority: 8 }))

  await page.getByRole('combobox', { name: 'Event type' }).click()
  await page.getByRole('option', { name: /finding\.reported/i }).click()
  const signalPanel = page
    .getByRole('heading', { name: 'Test signal' })
    .locator('..')
  const severity = signalPanel.getByRole('combobox').nth(1)
  await expect(severity).toBeVisible()
  await severity.click()
  await page.getByRole('option', { name: 'Critical', exact: true }).click()
  const evaluated = observe(page, 'POST', '/v1/m/notify/routes/evaluate', 200)
  await page.getByRole('button', { name: 'Evaluate' }).click()
  const verdict = (await (await evaluated).json()) as {
    items: Array<{ id: string }>
  }
  expect(verdict.items.some((item) => item.id === route.id)).toBe(true)

  const deliveries = observe(page, 'GET', '/v1/m/notify/deliveries', 200)
  await page.getByRole('tab', { name: 'Deliveries' }).click()
  const deliveriesBody = (await (await deliveries).json()) as ListBody<{
    route_ref?: string
  }>
  expect(deliveriesBody.items.some((item) => item.route_ref === route.id)).toBe(
    true,
  )
  const failedFilter = observe(page, 'GET', '/v1/m/notify/deliveries', 200)
  await page.getByRole('combobox', { name: 'Filter by status' }).click()
  await page.getByRole('option', { name: 'Failed' }).click()
  expect(new URL((await failedFilter).url()).searchParams.get('status')).toBe(
    'failed',
  )

  const deadRead = observe(page, 'GET', '/v1/m/notify/outbox', 200)
  await page.getByRole('tab', { name: 'Dead letters' }).click()
  const deadBody = (await (await deadRead).json()) as ListBody<{
    id: string
    route_ref?: string
    status: string
  }>
  expect(
    deadBody.items.some(
      (item) => item.route_ref === route.id && item.status === 'dead',
    ),
  ).toBe(true)
  const outboxFilter = observe(page, 'GET', '/v1/m/notify/outbox', 200)
  await page.getByRole('combobox', { name: 'Filter by outbox status' }).click()
  await page.getByRole('option', { name: 'Queued', exact: true }).click()
  expect(new URL((await outboxFilter).url()).searchParams.get('status')).toBe(
    'queued',
  )
  await page.getByRole('combobox', { name: 'Filter by outbox status' }).click()
  await page.getByRole('option', { name: 'Dead-lettered' }).click()
  const requeue = page
    .getByRole('button', { name: /Requeue this notification/i })
    .first()
  await requeue.click()
  dialog = page.getByRole('dialog')
  await dialog.getByRole('button', { name: 'Cancel' }).click()
  await expect(dialog).not.toBeVisible()
  await requeue.click()
  dialog = page.getByRole('dialog')
  const requeued = page.waitForResponse(
    (response) =>
      response.request().method() === 'POST' &&
      /\/v1\/m\/notify\/outbox\/[^/]+\/redeliver$/.test(
        new URL(response.url()).pathname,
      ),
  )
  await dialog.getByRole('button', { name: 'Requeue notification' }).click()
  const requeuedResponse = await requeued
  expect(requeuedResponse.status()).toBe(200)
  expect((await requeuedResponse.json()) as { status: string }).toEqual(
    expect.objectContaining({ status: 'queued' }),
  )

  await page.getByRole('tab', { name: 'Routes' }).click()
  await page.getByRole('button', { name: 'Delete route l5-route-live' }).click()
  dialog = page.getByRole('dialog')
  await dialog.getByRole('button', { name: 'Cancel' }).click()
  await expect(dialog).not.toBeVisible()
  await page.getByRole('button', { name: 'Delete route l5-route-live' }).click()
  dialog = page.getByRole('dialog')
  const removed = observe(
    page,
    'DELETE',
    `/v1/m/notify/routes/${route.id}`,
    204,
  )
  await dialog.getByRole('button', { name: 'Delete route' }).click()
  await removed
  const after = await getJSON<ListBody<NotifyRoute>>(
    page,
    token,
    '/v1/m/notify/routes',
  )
  expect(after.items.some((item) => item.id === route.id)).toBe(false)
  await deleteCheckBySubject(page, token, 'l5-alert-dlq')
})

test('tenant lifecycle and API playground destructive send change disposable state', async ({
  page,
}) => {
  const token = await loginDemo(page)
  const lifecycleOrg = await ensureOrg(
    page,
    token,
    'l5-lifecycle',
    'L5 Lifecycle',
  )
  const deleteOrg = await ensureOrg(page, token, 'l5-delete-ui', 'L5 Delete UI')
  const playgroundOrg = await ensureOrg(
    page,
    token,
    'l5-delete-playground',
    'L5 Delete Playground',
  )

  const tenantsRead = observe(page, 'GET', '/v1/system/orgs', 200)
  await page.goto('/tenants')
  await tenantsRead
  let lifecycleRow = await cardFor(page, 'L5 Lifecycle')
  await lifecycleRow.getByRole('button', { name: 'Withdraw service' }).click()
  let dialog = page.getByRole('dialog')
  await dialog.getByRole('button', { name: 'Cancel' }).click()
  await expect(dialog).not.toBeVisible()
  lifecycleRow = await cardFor(page, 'L5 Lifecycle')
  await lifecycleRow.getByRole('button', { name: 'Withdraw service' }).click()
  dialog = page.getByRole('dialog')
  let changed = observe(
    page,
    'PUT',
    `/v1/system/orgs/${lifecycleOrg.tenant_id}/status`,
    200,
  )
  await dialog.getByRole('textbox').fill('l5-lifecycle')
  await dialog
    .getByRole('button', { name: 'Withdraw service', exact: true })
    .click()
  expect(
    ((await changed).request().postDataJSON() as { status: string }).status,
  ).toBe('suspended')
  lifecycleRow = await cardFor(page, 'L5 Lifecycle')
  await lifecycleRow.getByRole('button', { name: 'Restore service' }).click()
  dialog = page.getByRole('dialog')
  changed = observe(
    page,
    'PUT',
    `/v1/system/orgs/${lifecycleOrg.tenant_id}/status`,
    200,
  )
  await dialog
    .getByRole('button', { name: 'Restore service', exact: true })
    .click()
  expect(
    ((await changed).request().postDataJSON() as { status: string }).status,
  ).toBe('active')

  const deleteRow = await cardFor(page, 'L5 Delete UI')
  await deleteRow.getByRole('button', { name: 'Delete' }).click()
  dialog = page.getByRole('dialog')
  await expect(dialog).toContainText(/cloud control plane, not to this engine/i)
  await dialog.getByRole('button', { name: 'Cancel' }).click()
  await expect(dialog).not.toBeVisible()
  await deleteRow.getByRole('button', { name: 'Delete' }).click()
  dialog = page.getByRole('dialog')
  await dialog.getByRole('textbox').fill('l5-delete-ui')
  const deleted = observe(
    page,
    'DELETE',
    `/v1/system/orgs/${deleteOrg.tenant_id}`,
    204,
  )
  await dialog.getByRole('button', { name: 'Delete permanently' }).click()
  await deleted
  expect(
    (await getJSON<ListBody<Org>>(page, token, '/v1/system/orgs')).items.some(
      (item) => item.tenant_id === deleteOrg.tenant_id,
    ),
  ).toBe(false)

  const residencyRegistry = observe(page, 'GET', '/v1/system/residency', 200)
  const residencyOrgs = observe(page, 'GET', '/v1/system/orgs', 200)
  await page.goto('/residency')
  await residencyRegistry
  await residencyOrgs
  const residencyRow = await rowFor(page, 'L5 Lifecycle')
  await residencyRow.getByRole('button', { name: 'Set region' }).click()
  await expect(page.getByRole('dialog')).toContainText(
    /hardware|verification|security key/i,
  )
  await page.keyboard.press('Escape')

  const publicRead = observe(page, 'GET', '/status', 200)
  await page.goto('/status-page')
  await publicRead
  await expect(
    page.getByRole('heading', { name: 'Olivares Control Plane — Status' }),
  ).toBeVisible()
  const publicRefresh = observe(page, 'GET', '/status', 200)
  await page.getByRole('button', { name: 'Refresh' }).click()
  await publicRefresh

  const stableSpec = observe(page, 'GET', '/openapi.json', 200)
  const betaSpec = observe(page, 'GET', '/openapi.beta.json', 200)
  await page.goto('/api-playground')
  await stableSpec
  await betaSpec
  await expect(
    page.getByRole('heading', { name: 'API Playground' }),
  ).toBeVisible()

  const group = page
    .getByRole('navigation', { name: 'API endpoints' })
    .locator(':scope > div > button')
    .first()
  await expect(group).toHaveAttribute('aria-expanded', 'true')
  await group.click()
  await expect(group).toHaveAttribute('aria-expanded', 'false')
  await group.click()
  await expect(group).toHaveAttribute('aria-expanded', 'true')
  await selectEndpoint(page, 'GET', '/status')
  await page.getByRole('tab', { name: 'Headers' }).click()
  await page.getByPlaceholder('Header name').fill('X-L5-Probe')
  await page.getByPlaceholder('Value').fill('measured')
  await page.getByRole('button', { name: 'Add custom header' }).click()
  await expect(
    page.getByRole('button', { name: 'Remove X-L5-Probe header' }),
  ).toBeVisible()
  await page
    .getByText('X-L5-Probe', { exact: true })
    .locator('..')
    .getByRole('textbox')
    .fill('measured-after-add')
  await page.getByRole('button', { name: 'Remove X-L5-Probe header' }).click()
  await page.getByRole('button', { name: 'curl' }).click()
  const statusSent = observe(page, 'GET', '/status', 200)
  await page.getByRole('button', { name: 'Send' }).click()
  await statusSent
  await expect(page.getByText('200', { exact: true }).last()).toBeVisible()
  await page.getByRole('tab', { name: /^Headers \(/ }).click()
  await page.getByRole('tab', { name: 'Body', exact: true }).click()

  await selectEndpoint(page, 'GET', '/v1/users')
  await page.getByRole('tab', { name: 'Query', exact: true }).click()
  await page
    .getByText('limit', { exact: true })
    .locator('xpath=../..')
    .getByRole('textbox')
    .fill('10')

  await selectEndpoint(page, 'POST', '/v1/users')
  await page.getByRole('tab', { name: 'Body', exact: true }).first().click()
  const requestBody = page.getByRole('textbox', { name: 'Request body' })
  await requestBody.fill(
    JSON.stringify({
      email: 'playground-not-sent@olivares.local',
      password: 'not-sent-password',
    }),
  )
  await expect(requestBody).toContainText('playground-not-sent')

  await selectEndpoint(page, 'DELETE', '/v1/system/orgs/{tenant_id}')
  await page.getByRole('tab', { name: 'Path Params' }).click()
  await page.getByPlaceholder('Enter tenant_id').fill(playgroundOrg.tenant_id)
  const playgroundDelete = observe(
    page,
    'DELETE',
    `/v1/system/orgs/${playgroundOrg.tenant_id}`,
    204,
  )
  await page.getByRole('button', { name: 'Send' }).click()
  await playgroundDelete
  expect(
    (await getJSON<ListBody<Org>>(page, token, '/v1/system/orgs')).items.some(
      (item) => item.tenant_id === playgroundOrg.tenant_id,
    ),
  ).toBe(false)

  await selectEndpoint(page, 'GET', '/v1/m/health/stream')
  const stream = observe(page, 'GET', '/v1/m/health/stream', 200)
  await page.getByRole('button', { name: 'Send' }).click()
  await stream
  await page.getByRole('button', { name: 'Cancel' }).click()
  await expect(page.getByRole('button', { name: 'Send' })).toBeVisible()

  await page.getByRole('tab', { name: /^History/ }).click()
  await expect(
    page.getByText('/v1/system/orgs/{tenant_id}', { exact: true }),
  ).toBeVisible()
  await page.getByRole('button', { name: 'Clear request history' }).click()
  await expect(page.getByText('No requests yet')).toBeVisible()
  await expect
    .poll(async () => {
      const raw = await page.evaluate(() =>
        localStorage.getItem('olivares:api-playground'),
      )
      if (!raw) return -1
      const persisted = JSON.parse(raw) as { state?: { history?: unknown[] } }
      return persisted.state?.history?.length ?? -1
    })
    .toBe(0)
  await page.getByRole('tab', { name: 'Response', exact: true }).click()
  await expect(
    page.getByRole('tab', { name: 'Response', exact: true }),
  ).toHaveAttribute('aria-selected', 'true')
})

test('backup create, inspect, download, restore and delete use a disposable bundle', async ({
  page,
}) => {
  const token = await loginDemo(page)
  const users = await getJSON<ListBody<{ email: string }>>(
    page,
    token,
    '/v1/users?limit=1000',
  )
  if (!users.items.some((user) => user.email === SECOND_ADMIN_EMAIL)) {
    const secondAdmin = await page.request.post('/v1/users', {
      headers: authHeaders(token),
      data: {
        email: SECOND_ADMIN_EMAIL,
        display_name: 'L5 second restore approver',
        password: SECOND_ADMIN_PASSWORD,
        superadmin: true,
      },
    })
    expect(
      secondAdmin.status(),
      'create a distinct disposable superadmin',
    ).toBe(201)
  }
  const backupNote = `l5-live-backup-${Date.now()}`
  const loaded = observe(page, 'GET', '/v1/console/dr/backups', 200)
  await page.goto('/backups')
  const initial = (await (await loaded).json()) as { items: Backup[] }
  expect(initial.items.length).toBeGreaterThan(0)

  await page.getByRole('button', { name: 'Create Backup' }).click()
  let dialog = page.getByRole('dialog')
  await page.keyboard.press('Escape')
  await expect(dialog).not.toBeVisible()
  await page.getByRole('button', { name: 'Create Backup' }).click()
  dialog = page.getByRole('dialog')
  await dialog.getByLabel('Notes').fill(backupNote)
  await dialog.getByLabel('Passphrase').fill(BACKUP_PASSPHRASE)
  const triggered = observe(page, 'POST', '/v1/console/dr/backup', 202)
  await dialog.getByRole('button', { name: 'Start Backup' }).click()
  const jobId = ((await (await triggered).json()) as { job_id: string }).job_id
  await expect
    .poll(
      async () => {
        const jobs = await getJSON<{
          items: Array<{ id: string; status: string }>
        }>(page, token, '/v1/console/dr/jobs')
        return jobs.items.find((job) => job.id === jobId)?.status
      },
      { timeout: 60_000, message: 'the live backup job must finish' },
    )
    .toBe('completed')
  await dialog.getByRole('button', { name: 'Close' }).first().click()
  await page.reload()

  const backups = await getJSON<{ items: Backup[] }>(
    page,
    token,
    '/v1/console/dr/backups',
  )
  const created = backups.items.find((backup) =>
    backup.notes.startsWith(backupNote),
  )
  expect(created, 'created backup must be listed').toBeTruthy()
  await page.getByPlaceholder('Search backups...').fill(backupNote)
  const detail = observe(
    page,
    'GET',
    `/v1/console/dr/backups/${created!.id}`,
    200,
  )
  await page
    .getByRole('button', { name: `Inspect ${created!.filename}` })
    .click()
  await detail
  await expect(page.getByRole('dialog')).toContainText(created!.filename)
  await page.keyboard.press('Escape')

  const downloadResponse = observe(
    page,
    'GET',
    `/v1/console/dr/backups/${created!.id}/download`,
    200,
  )
  const downloadEvent = page.waitForEvent('download')
  await page
    .getByRole('button', { name: `Download ${created!.filename}` })
    .click()
  await downloadResponse
  const download = await downloadEvent
  expect(download.suggestedFilename()).toBe(created!.filename)

  await page.getByRole('tab', { name: 'Schedule' }).click()
  await expect(page.getByLabel('Enable automated backups')).toBeVisible()
  const enabled = await page.getByLabel('Enable automated backups').isChecked()
  if (!enabled) await page.getByLabel('Enable automated backups').click()
  await page.getByLabel('Cron Expression').fill('0 3 * * *')
  await page.getByLabel('Retention (days)').fill('31')
  await page.getByLabel('Require dual-control for restore').click()
  await page.getByLabel('Require dual-control for restore').click()
  const scheduleSaved = observe(page, 'PUT', '/v1/console/dr/schedule', 200)
  await page.getByRole('button', { name: 'Save Schedule' }).click()
  expect((await scheduleSaved).request().postDataJSON()).toMatchObject({
    enabled: true,
    cron: '0 3 * * *',
    retain_days: 31,
    require_dual_control_restore: false,
  })

  const bundle = await page.request.get(
    `/v1/console/dr/backups/${created!.id}/download`,
    { headers: authHeaders(token) },
  )
  expect(bundle.status()).toBe(200)
  const bundlePayload = {
    name: created!.filename,
    mimeType: 'application/octet-stream',
    buffer: await bundle.body(),
  }
  await page.getByRole('button', { name: 'Restore', exact: true }).click()
  dialog = page.getByRole('dialog')
  const chooseFile = page.waitForEvent('filechooser')
  await dialog.getByRole('button', { name: 'Choose File' }).click()
  await (await chooseFile).setFiles(bundlePayload)
  const uploaded = observe(page, 'POST', '/v1/console/dr/restore/upload', 200)
  await dialog.getByRole('button', { name: 'Upload' }).click()
  const uploadId = ((await (await uploaded).json()) as { upload_id: string })
    .upload_id
  await dialog.getByLabel('Passphrase').fill(BACKUP_PASSPHRASE)
  await dialog.getByRole('button', { name: 'Apply Restore' }).click()
  const confirm = page.getByRole('dialog').last()
  await confirm.getByRole('textbox').fill('RESTORE')
  const applied = observe(
    page,
    'POST',
    `/v1/console/dr/restore/${uploadId}/apply`,
    202,
  )
  await confirm.getByRole('button', { name: 'Restore Now' }).click()
  const restoreJob = ((await (await applied).json()) as { job_id: string })
    .job_id
  await expect
    .poll(
      async () => {
        const jobs = await getJSON<{
          items: Array<{ id: string; status: string; error?: string }>
        }>(page, token, '/v1/console/dr/jobs')
        return jobs.items.find((job) => job.id === restoreJob)?.status
      },
      { timeout: 90_000, message: 'the disposable restore must finish' },
    )
    .toMatch(/^(completed|failed)$/)
  const terminalRestore = (
    await getJSON<{
      items: Array<{ id: string; status: string; error?: string }>
    }>(page, token, '/v1/console/dr/jobs')
  ).items.find((job) => job.id === restoreJob)
  expect(
    terminalRestore,
    'the restore job must remain inspectable',
  ).toBeTruthy()
  if (terminalRestore!.status === 'failed') {
    // This base's engine refuses its own fresh SQLite snapshot at the guard
    // preflight. Keep that backend negative explicit while proving the console
    // renders it and leaves Close reachable; the audit records completion as an
    // evidence gap, never as a successful restore.
    expect(terminalRestore!.error).toContain('guard')
    await expect(dialog).toContainText('Failed')
  }
  await expect(
    dialog.getByRole('button', { name: 'Close' }).first(),
  ).toBeVisible()
  await dialog.getByRole('button', { name: 'Close' }).first().click()

  // Arm dual-control through the console and re-submit the same disposable
  // bundle. The first account can only create the pending request; a distinct
  // stable user account must perform the second destructive confirmation.
  const dualControl = page.getByLabel('Require dual-control for restore')
  await expect(dualControl).not.toBeChecked()
  await dualControl.click()
  const armed = observe(page, 'PUT', '/v1/console/dr/schedule', 200)
  await page.getByRole('button', { name: 'Save Schedule' }).click()
  expect((await armed).request().postDataJSON()).toMatchObject({
    require_dual_control_restore: true,
  })

  await page.getByRole('tab', { name: 'Backups' }).click()
  await page.getByRole('button', { name: 'Restore', exact: true }).click()
  dialog = page.getByRole('dialog')
  await dialog.locator('#restore-file-input').setInputFiles(bundlePayload)
  const dualUpload = observe(page, 'POST', '/v1/console/dr/restore/upload', 200)
  await dialog.getByRole('button', { name: 'Upload' }).click()
  const dualUploadId = (
    (await (await dualUpload).json()) as { upload_id: string }
  ).upload_id
  await dialog.getByLabel('Passphrase').fill(BACKUP_PASSPHRASE)
  await dialog.getByRole('button', { name: 'Apply Restore' }).click()
  let destructiveConfirm = page.getByRole('dialog').last()
  await destructiveConfirm.getByRole('textbox').fill('RESTORE')
  const queuedForApproval = observe(
    page,
    'POST',
    `/v1/console/dr/restore/${dualUploadId}/apply`,
    202,
  )
  await destructiveConfirm.getByRole('button', { name: 'Restore Now' }).click()
  const awaiting = (await (await queuedForApproval).json()) as {
    awaiting_approval: boolean
    request_id: string
  }
  expect(awaiting.awaiting_approval).toBe(true)
  expect(awaiting.request_id).toBeTruthy()
  await expect(dialog).toContainText('Awaiting a second approver')
  await dialog.getByRole('button', { name: 'Close' }).first().click()

  await page.evaluate(() => localStorage.removeItem('olivares.session'))
  const secondToken = await loginDemo(
    page,
    SECOND_ADMIN_EMAIL,
    SECOND_ADMIN_PASSWORD,
  )
  const pendingRead = observe(
    page,
    'GET',
    '/v1/console/dr/restore/pending',
    200,
  )
  await page.goto('/backups')
  const pendingBody = (await (await pendingRead).json()) as {
    items: Array<{ request_id: string }>
  }
  expect(
    pendingBody.items.some((item) => item.request_id === awaiting.request_id),
  ).toBe(true)

  const approve = page.getByRole('button', {
    name: 'Approve & Restore',
    exact: true,
  })
  await approve.click()
  dialog = page.getByRole('dialog')
  await page.keyboard.press('Escape')
  await expect(dialog).not.toBeVisible()

  await approve.click()
  dialog = page.getByRole('dialog')
  await dialog.getByRole('button', { name: 'Cancel' }).click()
  await expect(dialog).not.toBeVisible()

  await approve.click()
  dialog = page.getByRole('dialog')
  await dialog.getByLabel('Passphrase').fill(BACKUP_PASSPHRASE)
  await dialog
    .getByRole('button', { name: 'Approve & Restore', exact: true })
    .click()
  destructiveConfirm = page.getByRole('dialog').last()
  await destructiveConfirm.getByRole('textbox').fill('RESTORE')
  const approved = observe(
    page,
    'POST',
    `/v1/console/dr/restore/${dualUploadId}/approve`,
    202,
  )
  await destructiveConfirm
    .getByRole('button', { name: 'Approve & Restore Now' })
    .click()
  const dualRestoreJob = ((await (await approved).json()) as { job_id: string })
    .job_id
  await expect
    .poll(
      async () => {
        const jobs = await getJSON<{
          items: Array<{ id: string; status: string }>
        }>(page, secondToken, '/v1/console/dr/jobs')
        return jobs.items.find((job) => job.id === dualRestoreJob)?.status
      },
      {
        timeout: 90_000,
        message: 'the dual-control restore must reach a terminal state',
      },
    )
    .toMatch(/^(completed|failed)$/)
  const dualTerminal = (
    await getJSON<{
      items: Array<{ id: string; status: string; error?: string }>
    }>(page, secondToken, '/v1/console/dr/jobs')
  ).items.find((job) => job.id === dualRestoreJob)
  expect(dualTerminal).toBeTruthy()
  if (dualTerminal!.status === 'failed') {
    expect(dualTerminal!.error).toContain('guard')
  }
  expect(
    (
      await getJSON<{ items: Array<{ request_id: string }> }>(
        page,
        secondToken,
        '/v1/console/dr/restore/pending',
      )
    ).items.some((item) => item.request_id === awaiting.request_id),
  ).toBe(false)
  await expect(page.getByRole('dialog')).not.toBeVisible()

  await page.goto('/backups')
  const afterRestore = await getJSON<{ items: Backup[] }>(
    page,
    token,
    '/v1/console/dr/backups',
  )
  const deletable = afterRestore.items.find(
    (backup) => backup.id !== created!.id,
  )
  expect(
    deletable,
    'a separate startup backup is needed for destructive delete',
  ).toBeTruthy()
  await page.getByPlaceholder('Search backups...').fill(deletable!.filename)
  await page
    .getByRole('button', { name: `Delete ${deletable!.filename}` })
    .click()
  dialog = page.getByRole('dialog')
  const removed = observe(
    page,
    'DELETE',
    `/v1/console/dr/backups/${deletable!.id}`,
    204,
  )
  await dialog.getByRole('button', { name: 'Delete' }).click()
  await removed
  expect(
    (
      await getJSON<{ items: Backup[] }>(page, token, '/v1/console/dr/backups')
    ).items.some((backup) => backup.id === deletable!.id),
  ).toBe(false)
})
