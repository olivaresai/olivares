// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import {
  expect,
  test,
  type APIResponse,
  type Locator,
  type Page,
  type Response,
} from '@playwright/test'

// CONSOLE-FUNC-01 lot 6 runs against a disposable engine with the real module
// handlers and embedded console. Playwright never routes, fulfils, aborts, or
// rewrites HTTP: every request assertion below observes that engine.
const demoTenant = process.env.DEMO_TENANT ?? ''
const DEMO_EMAIL = 'demo@olivares.local'
const DEMO_PASSWORD = 'olivares-demo-estate'
const SECOND_ADMIN_EMAIL = 'l6-second-admin@olivares.local'
const SECOND_ADMIN_PASSWORD = 'l6-second-admin-passphrase'
const VIEWER_EMAIL = 'l6-viewer@olivares.local'
const VIEWER_PASSWORD = 'l6-viewer-passphrase'

test.use({ trace: 'off' })
test.describe.configure({ mode: 'serial', timeout: 300_000 })

interface ListBody<T> {
  items: T[]
  has_more?: boolean
}

interface Workspace {
  id: string
  name: string
  is_default: boolean
}

interface UserRecord {
  id: string
  email: string
}

interface EventSubscription {
  id: string
  name: string
  enabled: boolean
  description?: string
  auth_type: string
  auth_value_hint?: string
}

interface Delivery {
  id: string
  subscription: string
  event_seq: number
  event_type: string
  status: string
  origin: string
  attempts: number
  last_status?: string
}

interface DeployDefinition {
  id: string
  name: string
  current_version: number
  applied_version: number
  desired_status: string
  spec: Record<string, unknown>
}

interface Schedule {
  id: string
  name: string
  subject_ref: string
  desired_status: string
  trigger_kind: string
  cadence_spec: string
}

interface FireReceipt {
  approval_ref?: string
  op_status: string
  gate_status: string
  detail?: string
}

function authHeaders(token: string): Record<string, string> {
  return {
    Authorization: `Bearer ${token}`,
    'X-Olivares-Tenant': demoTenant,
  }
}

async function preparePage(page: Page): Promise<void> {
  await page.addInitScript(
    ([tenant]) => {
      localStorage.setItem(
        'olivares.tenant',
        JSON.stringify({ state: { activeTenant: tenant }, version: 0 }),
      )
      localStorage.setItem('olivares.lang', 'en')
      localStorage.setItem('olivares.theme', 'light')
    },
    [demoTenant],
  )
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
  expect(response.status(), `real login for ${email}`).toBe(200)
  const body = (await response.json()) as { token: string }
  await expect(
    page.getByRole('link', { name: 'Inventory', exact: true }),
  ).toBeVisible({ timeout: 20_000 })
  return body.token
}

async function loginAPI(page: Page, email: string, password: string) {
  const response = await page.request.post('/v1/auth/login', {
    data: { email, password },
  })
  expect(response.status(), `API login for ${email}`).toBe(200)
  return ((await response.json()) as { token: string }).token
}

async function selectDefaultWorkspace(page: Page): Promise<void> {
  await page.getByRole('button', { name: /All workspaces/i }).click()
  await page.getByRole('menuitem').filter({ hasText: 'Default' }).click()
  await expect(page.getByRole('button', { name: /^Default$/i })).toBeVisible()
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

async function expectJSON<T>(
  response: APIResponse,
  status: number,
): Promise<T> {
  expect(response.status()).toBe(status)
  return (await response.json()) as T
}

async function rowFor(page: Page, text: string): Promise<Locator> {
  const cell = page.getByText(text, { exact: true }).first()
  await expect(cell).toBeVisible({ timeout: 30_000 })
  return cell.locator('xpath=ancestor::tr[1]')
}

async function subscriptionCard(page: Page, name: string): Promise<Locator> {
  const label = page.getByText(name, { exact: true }).first()
  await expect(label).toBeVisible({ timeout: 30_000 })
  return label.locator(
    'xpath=ancestor::div[contains(@class,"rounded-lg") and contains(@class,"border")][1]',
  )
}

async function chooseSubscriptionAction(
  page: Page,
  name: string,
  action: string,
): Promise<void> {
  const card = await subscriptionCard(page, name)
  await card.getByRole('button', { name: `Actions for ${name}` }).click()
  await page.getByRole('menuitem', { name: action, exact: true }).click()
}

async function chooseScheduleAction(
  page: Page,
  name: string,
  action: string,
): Promise<void> {
  const row = await rowFor(page, name)
  await row.getByRole('button', { name: `Actions for ${name}` }).click()
  await page.getByRole('menuitem', { name: action, exact: true }).click()
}

function fieldCombobox(container: Locator, label: string): Locator {
  return container
    .locator('label')
    .filter({ hasText: new RegExp(`^${label}$`, 'i') })
    .locator('..')
    .getByRole('combobox')
}

async function chooseDifferentOption(
  page: Page,
  combobox: Locator,
): Promise<void> {
  const current = (await combobox.textContent())?.trim() ?? ''
  await combobox.click()
  const options = page.getByRole('option')
  const count = await options.count()
  expect(
    count,
    'a repeated composer selector exposes a catalog',
  ).toBeGreaterThan(0)
  let chosen = options.first()
  for (let index = 0; index < count; index += 1) {
    const candidate = options.nth(index)
    if (((await candidate.textContent()) ?? '').trim() !== current) {
      chosen = candidate
      break
    }
  }
  await chosen.click()
}

test.beforeEach(async ({ page }) => {
  test.skip(
    !demoTenant,
    'DEMO_TENANT not set — launch the disposable lot-6 engine',
  )
  await preparePage(page)
})

test('protocol composer persists a draft and disables it through all three modes', async ({
  page,
}) => {
  const token = await loginDemo(page)
  await selectDefaultWorkspace(page)
  const bindingKey = `l6-ui-a2a-${Date.now()}`
  const workspaces = await getJSON<ListBody<Workspace>>(
    page,
    token,
    '/v1/workspaces?limit=100',
  )
  const workspace = workspaces.items.find((item) => item.is_default)
  expect(workspace, 'seeded default workspace').toBeTruthy()

  const specsRead = observe(
    page,
    'GET',
    '/v1/m/sessions/protocol-binding-specs',
    200,
  )
  const bindingsRead = observe(
    page,
    'GET',
    '/v1/m/sessions/protocol-bindings',
    200,
  )
  await page.goto('/communications/protocol-bindings')
  const [specsResponse, bindingsResponse] = await Promise.all([
    specsRead,
    bindingsRead,
  ])
  expect(new URL(specsResponse.url()).searchParams.get('workspace_id')).toBe(
    workspace!.id,
  )
  expect(new URL(bindingsResponse.url()).searchParams.get('workspace_id')).toBe(
    workspace!.id,
  )
  await expect(
    page.getByRole('heading', { name: 'Protocol bindings' }),
  ).toBeVisible()

  const refreshedSpecs = observe(
    page,
    'GET',
    '/v1/m/sessions/protocol-binding-specs',
    200,
  )
  await page.getByRole('button', { name: 'Refresh' }).click()
  await refreshedSpecs

  let filtered = observe(
    page,
    'GET',
    '/v1/m/sessions/protocol-binding-specs',
    200,
  )
  await page.getByRole('combobox', { name: 'Protocol filter' }).click()
  await page.getByRole('option', { name: 'A2A', exact: true }).click()
  expect(new URL((await filtered).url()).searchParams.get('protocol')).toBe(
    'a2a',
  )

  filtered = observe(page, 'GET', '/v1/m/sessions/protocol-binding-specs', 200)
  await page.getByRole('combobox', { name: 'State filter' }).click()
  await page.getByRole('option', { name: 'Draft', exact: true }).click()
  expect(new URL((await filtered).url()).searchParams.get('state')).toBe(
    'draft',
  )

  await page.getByRole('tab', { name: 'Instances' }).click()
  filtered = observe(page, 'GET', '/v1/m/sessions/protocol-bindings', 200)
  await page.getByRole('combobox', { name: 'Evidence verdict filter' }).click()
  await page.getByRole('option', { name: 'Not observed' }).click()
  expect(new URL((await filtered).url()).searchParams.get('verdict')).toBe(
    'UNKNOWN',
  )
  await page.getByRole('tab', { name: 'Specifications' }).click()

  await page.getByRole('button', { name: 'New draft' }).click()
  const dialog = page.getByRole('dialog', {
    name: 'Compose protocol binding draft',
  })
  await expect(dialog).toBeVisible()
  await expect(
    dialog.getByRole('list', { name: 'Composer progress' }),
  ).toBeVisible()

  // The successor control is reachable, but no active predecessor exists on the
  // real engine. Return to initial generation instead of inventing one.
  await dialog.getByRole('combobox', { name: 'Generation workflow' }).click()
  await page
    .getByRole('option', { name: 'Successor of an active generation' })
    .click()
  await expect(
    dialog.getByRole('combobox', { name: 'Active predecessor' }),
  ).toBeVisible()
  await dialog.getByRole('combobox', { name: 'Generation workflow' }).click()
  await page
    .getByRole('option', { name: 'Initial generation (generation 1)' })
    .click()

  await dialog.getByRole('textbox', { name: 'Binding key' }).fill(bindingKey)
  await dialog.getByRole('combobox', { name: 'Protocol' }).click()
  await page.getByRole('option', { name: 'MCP', exact: true }).click()
  await dialog.getByRole('combobox', { name: 'Protocol' }).click()
  await page.getByRole('option', { name: 'A2A', exact: true }).click()
  await dialog.getByRole('textbox', { name: 'Protocol version' }).fill('1.0.1')
  await dialog.getByRole('combobox', { name: 'Direction' }).click()
  await page.getByRole('option', { name: 'Bidirectional' }).click()
  await dialog
    .getByRole('textbox', { name: 'Peer authority' })
    .fill('l6-peer.invalid')

  await dialog.getByRole('button', { name: 'Next' }).click()
  await dialog.getByRole('combobox', { name: 'Local resource kind' }).click()
  await page.getByRole('option', { name: 'Agent', exact: true }).click()
  await dialog.getByRole('combobox', { name: 'Local resource kind' }).click()
  await page.getByRole('option', { name: 'Work item', exact: true }).click()
  await dialog
    .getByRole('textbox', { name: 'Local resource ID' })
    .fill('l6-work-item')
  await dialog
    .getByRole('textbox', { name: 'Remote resource kind' })
    .fill('task')
  await dialog
    .getByRole('textbox', { name: 'Remote resource reference' })
    .fill('l6-remote-task')

  await dialog.getByRole('button', { name: 'Next' }).click()
  const sourceControls = dialog.getByRole('combobox', { name: 'Source path' })
  const targetControls = dialog.getByRole('combobox', { name: 'Target path' })
  const cardinalityControls = dialog.getByRole('combobox', {
    name: 'Cardinality',
  })
  const transformControls = dialog.getByRole('combobox', { name: 'Transform' })
  expect(await sourceControls.count()).toBeGreaterThan(0)
  expect(await targetControls.count()).toBe(await sourceControls.count())
  expect(await cardinalityControls.count()).toBe(await sourceControls.count())
  expect(await transformControls.count()).toBe(await sourceControls.count())
  await chooseDifferentOption(page, sourceControls.first())
  await chooseDifferentOption(page, targetControls.first())
  await chooseDifferentOption(page, cardinalityControls.first())
  await chooseDifferentOption(page, transformControls.first())

  const mappingCount = await sourceControls.count()
  const addMapping = dialog.getByRole('button', { name: 'Add mapping' })
  if (await addMapping.isEnabled()) {
    await addMapping.click()
    await expect(sourceControls).toHaveCount(mappingCount + 1)
    await dialog.getByRole('button', { name: 'Remove' }).last().click()
    await expect(sourceControls).toHaveCount(mappingCount)
  }
  const declareLoss = dialog
    .getByRole('button', { name: 'Declare loss' })
    .first()
  if (await declareLoss.isVisible()) await declareLoss.click()

  await dialog.getByRole('button', { name: 'Next' }).click()
  const losses = dialog.getByRole('textbox', { name: 'Reason code' })
  if ((await losses.count()) > 0) {
    await losses.first().fill('l6-semantic-gap')
    await dialog
      .getByRole('checkbox', { name: 'Loss explicitly accepted' })
      .first()
      .click()
    await dialog
      .getByRole('textbox', { name: 'Acceptance reference' })
      .first()
      .fill('approval:l6-audit')
  }
  const addLoss = dialog.getByRole('button', { name: 'Add loss' })
  if (await addLoss.isEnabled()) {
    const before = await losses.count()
    await addLoss.click()
    await expect(losses).toHaveCount(before + 1)
    await dialog.getByRole('button', { name: 'Remove' }).last().click()
    await expect(losses).toHaveCount(before)
  }
  while ((await losses.count()) > 0) {
    await dialog.getByRole('button', { name: 'Remove' }).last().click()
  }
  await dialog
    .getByRole('textbox', { name: 'Rule references' })
    .fill('rule:l6-protocol\npolicy:l6-mapping')
  await dialog
    .getByRole('textbox', { name: 'Permission profile reference' })
    .fill('profile:l6-standard')
  await expect(
    dialog.getByRole('region', { name: 'Effective permission preview' }),
  ).toContainText('sessions:protocol-binding:write')

  await dialog.getByRole('button', { name: 'Back' }).click()
  await expect(
    dialog.getByRole('button', { name: 'Add mapping' }),
  ).toBeVisible()
  // Restore the catalog's canonical mapping after exercising every repeated
  // selector above; the handler must receive a valid declarative mapping, not a
  // browser-only combination assembled merely to touch controls.
  await dialog.getByRole('button', { name: 'Connection' }).click()
  await dialog.getByRole('combobox', { name: 'Protocol' }).click()
  await page.getByRole('option', { name: 'MCP', exact: true }).click()
  await dialog.getByRole('combobox', { name: 'Protocol' }).click()
  await page.getByRole('option', { name: 'A2A', exact: true }).click()
  await dialog.getByRole('button', { name: 'Next' }).click()
  await dialog.getByRole('button', { name: 'Next' }).click()
  await dialog.getByRole('button', { name: 'Next' }).click()
  await dialog.getByRole('button', { name: 'Next' }).click()
  const downloadReady = page.waitForEvent('download')
  await dialog
    .getByRole('button', {
      name: 'Export desired spec and server evidence as JSON',
    })
    .click()
  expect((await downloadReady).suggestedFilename()).toBe(
    `${bindingKey}-generation-1.json`,
  )

  const validate = observe(
    page,
    'POST',
    '/v1/m/sessions/protocol-binding-specs',
  )
  const planned = page.waitForResponse((response) => {
    const url = new URL(response.url())
    return (
      response.request().method() === 'POST' &&
      url.pathname === '/v1/m/sessions/protocol-binding-specs' &&
      url.searchParams.get('mode') === 'plan'
    )
  })
  await dialog.getByRole('button', { name: 'Validate and plan' }).click()
  const validateResponse = await validate
  const validateURL = new URL(validateResponse.url())
  expect(validateURL.searchParams.get('mode')).toBe('validate')
  expect(validateResponse.status()).toBe(200)
  const validateBody = (await validateResponse.json()) as {
    verdict: string
    validation: { verdict: string; code: string }
  }
  expect(validateBody.verdict).toBe('CLEAN')
  expect(validateBody.validation.verdict).toBe('UNKNOWN')

  const planResponse = await planned
  expect(planResponse.status()).toBe(200)
  await expect(
    dialog.getByRole('region', { name: 'Plan → apply comparison' }),
  ).toBeVisible()

  const apply = page.waitForResponse((response) => {
    const url = new URL(response.url())
    return (
      response.request().method() === 'POST' &&
      url.pathname === '/v1/m/sessions/protocol-binding-specs' &&
      url.searchParams.get('mode') === 'apply'
    )
  })
  const openedDetail = page.waitForResponse((response) => {
    const url = new URL(response.url())
    return (
      response.request().method() === 'GET' &&
      /^\/v1\/m\/sessions\/protocol-binding-specs\/[^/]+$/.test(url.pathname)
    )
  })
  await dialog.getByRole('button', { name: 'Create planned draft' }).click()
  const applyResponse = await apply
  expect(applyResponse.status()).toBe(201)
  const applyBody = (await applyResponse.json()) as {
    verdict: string
    code: string
    spec: {
      id: string
      binding_key: string
      state: string
      validation: { verdict: string; code: string }
    }
  }
  expect(applyBody.verdict).toBe('CLEAN')
  expect(applyBody.spec.binding_key).toBe(bindingKey)
  expect(applyBody.spec.state).toBe('draft')
  expect(applyBody.spec.validation.verdict).toBe('UNKNOWN')
  expect(applyBody.spec.validation.code).toBe('capability_validator_unwired')
  const openedDetailResponse = await openedDetail
  expect(new URL(openedDetailResponse.url()).pathname).toBe(
    `/v1/m/sessions/protocol-binding-specs/${applyBody.spec.id}`,
  )

  const persisted = await getJSON<
    ListBody<{ id: string; binding_key: string; state: string }>
  >(
    page,
    token,
    `/v1/m/sessions/protocol-binding-specs?workspace_id=${workspace!.id}&limit=100`,
  )
  expect(
    persisted.items.find((item) => item.binding_key === bindingKey),
  ).toMatchObject({ id: applyBody.spec.id, state: 'draft' })

  await expect(dialog).not.toBeVisible()
  const detail = page.getByRole('dialog', { name: 'Binding specification' })
  await expect(detail).toBeVisible()
  await expect(
    detail.getByText(
      'Activation requires a timestamped CLEAN witness issued by the server.',
    ),
  ).toBeVisible()
  await expect(detail.getByRole('button', { name: 'Activate' })).toBeDisabled()
  await expect(detail.getByRole('button', { name: 'Disable' })).toBeEnabled()

  const transitionPath = `/v1/m/sessions/protocol-binding-specs/${applyBody.spec.id}/disable`
  const refreshedDetail = observe(
    page,
    'GET',
    `/v1/m/sessions/protocol-binding-specs/${applyBody.spec.id}`,
    200,
  )
  const disableValidate = page.waitForResponse((response) => {
    const url = new URL(response.url())
    return (
      response.request().method() === 'POST' &&
      url.pathname === transitionPath &&
      url.searchParams.get('mode') === 'validate'
    )
  })
  const disablePlan = page.waitForResponse((response) => {
    const url = new URL(response.url())
    return (
      response.request().method() === 'POST' &&
      url.pathname === transitionPath &&
      url.searchParams.get('mode') === 'plan'
    )
  })
  await detail.getByRole('button', { name: 'Disable' }).click()
  const [refreshResponse, disableValidationResponse, disablePlanResponse] =
    await Promise.all([refreshedDetail, disableValidate, disablePlan])
  expect(refreshResponse.status()).toBe(200)
  expect(disableValidationResponse.status()).toBe(200)
  expect(disablePlanResponse.status()).toBe(200)
  expect(
    (await disableValidationResponse.json()) as { verdict: string },
  ).toMatchObject({ verdict: 'CLEAN' })
  expect(
    (await disablePlanResponse.json()) as { verdict: string },
  ).toMatchObject({ verdict: 'CLEAN' })

  const transitionDialog = page.getByRole('dialog', {
    name: 'Disable specification',
  })
  const disableApply = page.waitForResponse((response) => {
    const url = new URL(response.url())
    return (
      response.request().method() === 'POST' &&
      url.pathname === transitionPath &&
      url.searchParams.get('mode') === 'apply'
    )
  })
  const disabledDetailRead = observe(
    page,
    'GET',
    `/v1/m/sessions/protocol-binding-specs/${applyBody.spec.id}`,
    200,
  )
  await transitionDialog.getByRole('button', { name: 'Disable' }).click()
  const disableApplyResponse = await disableApply
  expect(disableApplyResponse.status()).toBe(200)
  const disabledBody = (await disableApplyResponse.json()) as {
    verdict: string
    spec: { id: string; state: string }
  }
  expect(disabledBody.verdict).toBe('CLEAN')
  expect(disabledBody.spec).toMatchObject({
    id: applyBody.spec.id,
    state: 'disabled',
  })
  await expect(
    transitionDialog.getByText('The returned specification is disabled.'),
  ).toBeVisible()
  await disabledDetailRead
  await transitionDialog.getByRole('button', { name: 'Close' }).click()
  await expect(transitionDialog).not.toBeVisible()
  await expect(detail.getByText('Disabled', { exact: true })).toBeVisible({
    timeout: 30_000,
  })
})

test('eventing authors, revisions, bulk work, replay, filters, and DLQ are live', async ({
  page,
}) => {
  const token = await loginDemo(page)
  await page.context().grantPermissions(['clipboard-read', 'clipboard-write'], {
    origin: 'http://127.0.0.1:27650',
  })
  const subscriptionName = `l6-ui-event-${Date.now()}`
  const endpoint = `https://127.0.0.1:27659/${subscriptionName}`

  const initialSubscriptions = observe(
    page,
    'GET',
    '/v1/m/eventing/subscriptions',
    200,
  )
  const policyRead = observe(page, 'GET', '/v1/m/eventing/egress-policy', 200)
  const compatRead = observe(
    page,
    'GET',
    '/v1/m/eventing/egress-policy/compat',
    200,
  )
  const catalogRead = observe(page, 'GET', '/v1/m/eventing/event-types', 200)
  await page.goto('/eventing')
  await Promise.all([initialSubscriptions, policyRead, compatRead, catalogRead])
  await expect(
    page.getByRole('heading', { name: 'Webhooks & event subscriptions' }),
  ).toBeVisible()
  await expect(
    page.getByRole('heading', { name: 'Destination policy' }),
  ).toBeVisible()
  await expect(
    page.getByRole('heading', { name: 'What enforcing would break' }),
  ).toBeVisible()
  await expect(page.getByText('Policy in force')).toBeVisible()
  await expect(page.getByText('Armed', { exact: true }).last()).toBeVisible()

  await page.getByRole('button', { name: 'New subscription' }).click()
  const createDialog = page.getByRole('dialog', { name: 'New subscription' })
  await createDialog
    .getByRole('textbox', { name: 'Name' })
    .fill(subscriptionName)
  await createDialog
    .getByRole('textbox', { name: 'Endpoint URL' })
    .fill(endpoint)
  await createDialog
    .locator('label')
    .filter({ hasText: /^work\.item\.created/ })
    .getByRole('checkbox')
    .click()
  await createDialog
    .getByRole('textbox', { name: 'Match sources' })
    .fill('olivares.sessions')
  await fieldCombobox(createDialog, 'Role').click()
  await page.getByRole('option', { name: 'editor', exact: true }).click()
  await fieldCombobox(createDialog, 'Auth type').click()
  await page.getByRole('option', { name: 'Bearer', exact: true }).click()
  await createDialog
    .getByRole('textbox', { name: 'Auth value' })
    .fill('l6-create-bearer')
  await fieldCombobox(createDialog, 'SIEM sink').click()
  await page.getByRole('option', { name: 'HTTPS (OCSF)' }).click()
  await fieldCombobox(createDialog, 'Sink format').click()
  await page.getByRole('option', { name: 'json', exact: true }).click()
  await createDialog.getByRole('spinbutton', { name: 'Max attempts' }).fill('1')
  await createDialog
    .getByRole('spinbutton', { name: 'Initial interval (s)' })
    .fill('5')
  await createDialog
    .getByRole('textbox', { name: 'Description' })
    .fill('lot 6 live create')
  const enabledSwitch = createDialog.getByRole('switch', { name: 'Enabled' })
  await enabledSwitch.click()
  await enabledSwitch.click()

  const createdRead = observe(page, 'POST', '/v1/m/eventing/subscriptions', 201)
  await createDialog
    .getByRole('button', { name: 'Create subscription' })
    .click()
  const created = (await (await createdRead).json()) as EventSubscription & {
    secret: string
    endpoint: string
    event_types: string[]
    sink_kind: string
    sink_format: string
  }
  expect(created).toMatchObject({
    name: subscriptionName,
    enabled: true,
    endpoint,
    auth_type: 'bearer',
    sink_kind: 'https',
    sink_format: 'json',
  })
  expect(created.event_types).toContain('work.item.created')
  expect(created.secret.length).toBeGreaterThan(20)

  const secretDialog = page.getByRole('dialog', {
    name: 'Subscription secret',
  })
  await expect(secretDialog.locator('code')).toHaveText(created.secret)
  await secretDialog.locator('code').locator('..').getByRole('button').click()
  await expect(
    secretDialog.getByText('Secret copied to clipboard'),
  ).toBeVisible()
  await secretDialog.getByRole('button', { name: 'Done' }).click()
  await expect(await subscriptionCard(page, subscriptionName)).toContainText(
    'Enabled',
  )

  await chooseSubscriptionAction(page, subscriptionName, 'Edit')
  const editDialog = page.getByRole('dialog', { name: 'Edit subscription' })
  await editDialog
    .getByRole('textbox', { name: 'Description' })
    .fill('lot 6 live update')
  await editDialog.getByRole('spinbutton', { name: 'Max attempts' }).fill('2')
  const editRefresh = observe(
    page,
    'GET',
    `/v1/m/eventing/subscriptions/${created.id}`,
    200,
  )
  const editedWrite = observe(
    page,
    'PUT',
    `/v1/m/eventing/subscriptions/${created.id}`,
    200,
  )
  await editDialog.getByRole('button', { name: 'Save changes' }).click()
  await editRefresh
  const edited = (await (await editedWrite).json()) as EventSubscription & {
    description: string
    max_attempts: number
  }
  expect(edited).toMatchObject({
    id: created.id,
    description: 'lot 6 live update',
    max_attempts: 2,
  })

  const historyRead = observe(
    page,
    'GET',
    `/v1/m/eventing/subscriptions/${created.id}/revisions`,
    200,
  )
  const editedCard = await subscriptionCard(page, subscriptionName)
  await editedCard
    .getByRole('button', {
      name: `View revision history for ${subscriptionName}`,
    })
    .click()
  await historyRead
  const history = page.getByRole('dialog', {
    name: `Revision history — ${subscriptionName}`,
  })
  await expect(
    history.getByRole('heading', { name: 'Snapshot diff' }),
  ).toBeVisible()
  await expect(history.getByText('Created', { exact: true })).toBeVisible()
  await expect(history.getByText('Updated', { exact: true })).toBeVisible()
  const restoreButtons = history.getByRole('button', {
    name: 'Restore this revision',
  })
  expect(await restoreButtons.count()).toBeGreaterThanOrEqual(2)
  await restoreButtons.first().click()
  const restoreDialog = page.getByRole('dialog', { name: 'Restore revision?' })
  await expect(
    restoreDialog.getByText(
      'This action is recorded in the tamper-evident audit ledger.',
    ),
  ).toBeVisible()
  const restoreWrite = observe(
    page,
    'POST',
    `/v1/m/eventing/subscriptions/${created.id}/restore`,
    200,
  )
  await restoreDialog.getByRole('button', { name: 'Restore revision' }).click()
  await restoreWrite
  await history.getByRole('button', { name: 'Close' }).click()

  const rowCheckbox = page.getByRole('checkbox', {
    name: `Select row ${created.id}`,
  })
  await rowCheckbox.click()
  await expect(page.getByText('1 selected', { exact: true })).toBeVisible()
  let bulkRefresh = observe(
    page,
    'GET',
    `/v1/m/eventing/subscriptions/${created.id}`,
    200,
  )
  let bulkWrite = observe(
    page,
    'PUT',
    `/v1/m/eventing/subscriptions/${created.id}`,
    200,
  )
  await page.getByRole('button', { name: 'Disable selected' }).click()
  await Promise.all([bulkRefresh, bulkWrite])
  await expect(await subscriptionCard(page, subscriptionName)).toContainText(
    'Disabled',
  )

  bulkRefresh = observe(
    page,
    'GET',
    `/v1/m/eventing/subscriptions/${created.id}`,
    200,
  )
  bulkWrite = observe(
    page,
    'PUT',
    `/v1/m/eventing/subscriptions/${created.id}`,
    200,
  )
  await page.getByRole('button', { name: 'Enable selected' }).click()
  await Promise.all([bulkRefresh, bulkWrite])
  await expect(await subscriptionCard(page, subscriptionName)).toContainText(
    'Enabled',
  )
  await page.getByRole('button', { name: 'Clear selection' }).click()
  await expect(page.getByText('1 selected', { exact: true })).not.toBeVisible()

  const testDelivery = observe(
    page,
    'POST',
    `/v1/m/eventing/subscriptions/${created.id}/test`,
    200,
  )
  await (
    await subscriptionCard(page, subscriptionName)
  )
    .getByRole('button', { name: 'Test' })
    .click()
  const testResult = (await (await testDelivery).json()) as {
    delivered: boolean
    outcome: string
  }
  expect(testResult.delivered).toBe(false)
  expect(testResult.outcome).toBeTruthy()

  const rotateSecret = observe(
    page,
    'POST',
    `/v1/m/eventing/subscriptions/${created.id}/rotate-secret`,
    200,
  )
  await chooseSubscriptionAction(page, subscriptionName, 'Rotate secret')
  const rotatedSecret = (await (await rotateSecret).json()) as {
    secret: string
  }
  expect(rotatedSecret.secret).not.toBe(created.secret)
  await expect(secretDialog.locator('code')).toHaveText(rotatedSecret.secret)
  await secretDialog.locator('code').locator('..').getByRole('button').click()
  await secretDialog.getByRole('button', { name: 'Done' }).click()

  await chooseSubscriptionAction(page, subscriptionName, 'Rotate auth')
  const rotateAuthDialog = page.getByRole('dialog', {
    name: `Rotate auth credential — ${subscriptionName}`,
  })
  await rotateAuthDialog
    .getByRole('textbox', { name: 'New credential' })
    .fill('l6-rotated-bearer')
  const rotateAuth = observe(
    page,
    'POST',
    `/v1/m/eventing/subscriptions/${created.id}/rotate-auth`,
    200,
  )
  await rotateAuthDialog
    .getByRole('button', { name: 'Rotate credential' })
    .click()
  const rotatedAuth = (await (await rotateAuth).json()) as EventSubscription
  expect(rotatedAuth.auth_type).toBe('bearer')
  expect(rotatedAuth.auth_value_hint).toBeTruthy()
  await expect(rotateAuthDialog).not.toBeVisible()

  await chooseSubscriptionAction(page, subscriptionName, 'Replay')
  const replayDialog = page.getByRole('dialog', {
    name: `Replay events — ${subscriptionName}`,
  })
  await replayDialog
    .getByRole('spinbutton', { name: 'From sequence' })
    .fill('1')
  await replayDialog
    .getByRole('spinbutton', { name: 'To sequence (optional)' })
    .fill('1')
  const replayWrite = observe(
    page,
    'POST',
    `/v1/m/eventing/subscriptions/${created.id}/replay`,
    200,
  )
  await replayDialog
    .getByRole('button', { name: 'Replay', exact: true })
    .click()
  const replayResult = (await (await replayWrite).json()) as {
    replayed: number
    has_more: boolean
  }
  expect(replayResult).toMatchObject({ replayed: 1, has_more: false })
  const replayDeliveries = observe(
    page,
    'GET',
    '/v1/m/eventing/deliveries',
    200,
  )
  await replayDialog
    .getByRole('button', { name: 'View replayed deliveries' })
    .click()
  expect(
    new URL((await replayDeliveries).url()).searchParams.get('origin'),
  ).toBe('replay')

  let deliveriesFiltered = observe(
    page,
    'GET',
    '/v1/m/eventing/deliveries',
    200,
  )
  await page.getByRole('combobox', { name: 'Subscription' }).click()
  await page.getByRole('option', { name: subscriptionName }).click()
  expect(
    new URL((await deliveriesFiltered).url()).searchParams.get('subscription'),
  ).toBe(created.id)
  deliveriesFiltered = observe(page, 'GET', '/v1/m/eventing/deliveries', 200)
  await page.getByRole('combobox', { name: 'Status' }).click()
  await page.getByRole('option', { name: 'Dead', exact: true }).click()
  expect(
    new URL((await deliveriesFiltered).url()).searchParams.get('status'),
  ).toBe('dead')
  deliveriesFiltered = observe(page, 'GET', '/v1/m/eventing/deliveries', 200)
  await page.getByRole('combobox', { name: 'Event type' }).click()
  await page.getByRole('option', { name: 'work.item.created' }).click()
  expect(
    new URL((await deliveriesFiltered).url()).searchParams.get('event_type'),
  ).toBe('work.item.created')
  deliveriesFiltered = observe(page, 'GET', '/v1/m/eventing/deliveries', 200)
  await page.getByRole('combobox', { name: 'Origin' }).click()
  await page.getByRole('option', { name: 'Live', exact: true }).click()
  expect(
    new URL((await deliveriesFiltered).url()).searchParams.get('origin'),
  ).toBe('live')
  await page.getByRole('combobox', { name: 'Origin' }).click()
  await page.getByRole('option', { name: 'Replay', exact: true }).click()
  await expect(page.getByRole('combobox', { name: 'Origin' })).toContainText(
    'Replay',
  )

  const eventsRead = observe(page, 'GET', '/v1/m/eventing/events', 200)
  await page.getByRole('tab', { name: 'Events' }).click()
  await eventsRead
  const eventFiltered = observe(page, 'GET', '/v1/m/eventing/events', 200)
  await page.getByRole('combobox', { name: 'Filter by type' }).click()
  await page.getByRole('option', { name: 'work.item.created' }).click()
  expect(new URL((await eventFiltered).url()).searchParams.get('type')).toBe(
    'work.item.created',
  )
  await page.getByText('#1', { exact: true }).locator('..').click()
  await expect(
    page.locator('pre').filter({ hasText: 'work_item_id' }),
  ).toBeVisible()

  const deadLettersRead = observe(
    page,
    'GET',
    '/v1/m/eventing/dead-letters',
    200,
  )
  await page.getByRole('tab', { name: 'Dead letters' }).click()
  await deadLettersRead
  const seededDeadRow = page
    .getByText(/^Subscription: l6-dead \(/)
    .locator('..')
  await expect(seededDeadRow).toBeVisible({ timeout: 30_000 })
  const redeliverWrite = observe(
    page,
    'POST',
    '/v1/m/eventing/deliveries/01a041f9-f58e-7aed-9b00-49458098862b/redeliver',
    202,
  )
  const refreshedDeadLetters = observe(
    page,
    'GET',
    '/v1/m/eventing/dead-letters',
    200,
  )
  await seededDeadRow.getByRole('button', { name: 'Redeliver' }).click()
  await Promise.all([redeliverWrite, refreshedDeadLetters])

  await page.getByRole('tab', { name: 'Subscriptions' }).click()
  await chooseSubscriptionAction(page, subscriptionName, 'Delete')
  const deleteDialog = page.getByRole('dialog', { name: 'Delete' })
  await expect(
    deleteDialog.getByText(
      'This action is recorded in the tamper-evident audit ledger.',
    ),
  ).toBeVisible()
  const deleteWrite = observe(
    page,
    'DELETE',
    `/v1/m/eventing/subscriptions/${created.id}`,
    204,
  )
  await deleteDialog.getByRole('button', { name: 'Confirm' }).click()
  await deleteWrite
  const remaining = await getJSON<ListBody<EventSubscription>>(
    page,
    token,
    '/v1/m/eventing/subscriptions',
  )
  expect(remaining.items.some((item) => item.id === created.id)).toBe(false)
})

test('deploy plans GitOps live and governs desired-state authoring end to end', async ({
  browser,
  page,
}) => {
  const token = await loginDemo(page)
  const definitionName = `l6-ui-deploy-${Date.now()}`

  let viewerLogin = await page.request.post('/v1/auth/login', {
    data: { email: VIEWER_EMAIL, password: VIEWER_PASSWORD },
  })
  if (viewerLogin.status() === 401) {
    const userResponse = await page.request.post('/v1/users', {
      headers: authHeaders(token),
      data: { email: VIEWER_EMAIL, password: VIEWER_PASSWORD },
    })
    const viewer = await expectJSON<UserRecord>(userResponse, 201)
    const membershipResponse = await page.request.post('/v1/memberships', {
      headers: authHeaders(token),
      data: { user_id: viewer.id, tenant: demoTenant, role: 'viewer' },
    })
    expect(membershipResponse.status()).toBe(201)
    viewerLogin = await page.request.post('/v1/auth/login', {
      data: { email: VIEWER_EMAIL, password: VIEWER_PASSWORD },
    })
  }
  const viewerToken = (await expectJSON<{ token: string }>(viewerLogin, 200))
    .token
  const viewerDefinitions = await page.request.get('/v1/m/deploy/definitions', {
    headers: authHeaders(viewerToken),
  })
  expect(viewerDefinitions.status()).toBe(200)

  const definitionsRead = observe(page, 'GET', '/v1/m/deploy/definitions', 200)
  await page.goto('/deploy')
  await definitionsRead
  await expect(
    page.getByRole('heading', { name: 'Deployment & integration' }),
  ).toBeVisible()

  const refreshedDefinitions = observe(
    page,
    'GET',
    '/v1/m/deploy/definitions',
    200,
  )
  await page.getByRole('button', { name: 'Refresh' }).click()
  await refreshedDefinitions
  const definitionsSearch = page.getByPlaceholder('Search definitions…')
  await definitionsSearch.fill('l6-gitops')
  const gitopsRow = await rowFor(page, 'l6-gitops')
  const gitopsDetailRead = observe(
    page,
    'GET',
    '/v1/m/deploy/definitions/01a04208-28ed-7bcc-b20b-e90ee885ab7d',
    200,
  )
  await gitopsRow.click()
  await gitopsDetailRead
  const gitopsDetail = page.getByRole('dialog', { name: 'l6-gitops' })
  await expect(gitopsDetail.getByText('Up to date')).toBeVisible()
  await expect(gitopsDetail.getByText('gitops.local/l6')).toBeVisible()

  await gitopsDetail.getByRole('button', { name: 'Plan' }).click()
  const planDialog = page.getByRole('dialog', { name: 'Run a dry-run plan?' })
  await expect(
    planDialog.getByText(
      'This action is recorded in the tamper-evident audit ledger.',
    ),
  ).toBeVisible()
  const planWrite = observe(
    page,
    'POST',
    '/v1/m/deploy/definitions/01a04208-28ed-7bcc-b20b-e90ee885ab7d/plan',
    200,
  )
  await planDialog.getByRole('button', { name: 'Run plan' }).click()
  const plan = (await (await planWrite).json()) as {
    plan_hash: string
    up_to_date: boolean
    changes: unknown[]
  }
  expect(plan.plan_hash).toMatch(/^[0-9a-f]{64}$/)
  expect(plan).toMatchObject({ up_to_date: true, changes: [] })
  const planHeading = gitopsDetail.getByRole('heading', {
    name: 'Plan result',
  })
  await expect(planHeading).toBeVisible()
  await planHeading.locator('..').getByRole('button', { name: 'Close' }).click()

  await gitopsDetail.getByRole('button', { name: 'Verify' }).click()
  const verifyDialog = page.getByRole('dialog', {
    name: 'Verify against live infrastructure?',
  })
  const verifyWrite = observe(
    page,
    'POST',
    '/v1/m/deploy/definitions/01a04208-28ed-7bcc-b20b-e90ee885ab7d/verify',
    200,
  )
  await verifyDialog.getByRole('button', { name: 'Verify' }).click()
  const verify = (await (await verifyWrite).json()) as {
    in_sync: boolean
    drift: Array<{ kind: string; resource: string; detail: string }>
  }
  expect(verify.in_sync).toBe(false)
  expect(verify.drift).toEqual([
    expect.objectContaining({
      kind: 'unknown',
      resource: 'observability',
      detail:
        'reconciliation delegated to the GitOps controller; configure a status endpoint to observe',
    }),
  ])
  const verifyHeading = gitopsDetail.getByRole('heading', {
    name: 'Verify result',
  })
  await expect(verifyHeading).toBeVisible()
  await verifyHeading
    .locator('..')
    .getByRole('button', { name: 'Close' })
    .click()

  await gitopsDetail.getByRole('button', { name: 'Apply' }).click()
  const applyDialog = page.getByRole('dialog', {
    name: 'Apply to live infrastructure?',
  })
  const requestApply = applyDialog.getByRole('button', {
    name: 'Request apply',
  })
  await expect(requestApply).toBeDisabled()
  const applyPhrase = applyDialog.getByRole('textbox', {
    name: 'Confirmation phrase',
  })
  await applyPhrase.fill('apply')
  await expect(requestApply).toBeDisabled()
  await applyPhrase.fill('APPLY')
  await expect(requestApply).toBeEnabled()
  await applyDialog.getByRole('button', { name: 'Cancel' }).click()

  await gitopsDetail.getByRole('button', { name: 'Retire' }).click()
  const retireDialog = page.getByRole('dialog', {
    name: 'Retire from live infrastructure?',
  })
  const requestRetire = retireDialog.getByRole('button', {
    name: 'Request retire',
  })
  await expect(requestRetire).toBeDisabled()
  await retireDialog
    .getByRole('textbox', { name: 'Confirmation phrase' })
    .fill('RETIRE')
  await expect(requestRetire).toBeEnabled()
  await retireDialog.getByRole('button', { name: 'Cancel' }).click()
  await gitopsDetail.getByRole('button', { name: 'Close' }).click()

  await definitionsSearch.fill('')
  await page.getByRole('button', { name: 'Declare deployment' }).click()
  const createDialog = page.getByRole('dialog', {
    name: 'Declare deployment',
  })
  await createDialog.getByRole('combobox', { name: 'Subject kind' }).click()
  await page.getByRole('option', { name: 'mcp_server', exact: true }).click()
  await createDialog.getByRole('combobox', { name: 'Subject kind' }).click()
  await page.getByRole('option', { name: 'agent', exact: true }).click()
  await createDialog
    .getByRole('textbox', { name: 'Subject' })
    .fill('l6-ui-agent')
  await createDialog.getByRole('textbox', { name: 'Name' }).fill(definitionName)
  await createDialog.getByRole('textbox', { name: 'Environment' }).fill('l6-ui')
  await createDialog
    .getByRole('textbox', { name: 'Target' })
    .fill('docker.host/l6-ui')
  await createDialog.getByRole('textbox', { name: 'Runtime' }).fill('docker')
  await createDialog
    .getByRole('textbox', { name: 'Source (GitOps)' })
    .fill('git:local/l6-ui#v1')
  await createDialog
    .getByRole('textbox', { name: 'Image' })
    .fill('registry.invalid/l6-ui:v1')
  await createDialog
    .getByRole('textbox', { name: 'Command' })
    .fill('/bin/l6-ui')
  await createDialog.getByRole('spinbutton', { name: 'Replicas' }).fill('1')

  await createDialog.getByRole('button', { name: 'Add resource' }).click()
  await createDialog.getByRole('textbox', { name: 'Key' }).fill('cpu')
  await createDialog.getByRole('textbox', { name: 'Value' }).fill('100m')
  await createDialog.getByRole('button', { name: 'Add resource' }).click()
  await createDialog.getByRole('button', { name: 'Remove' }).nth(1).click()
  await expect(createDialog.getByRole('textbox', { name: 'Key' })).toHaveCount(
    1,
  )

  await createDialog.getByRole('button', { name: 'Add reference' }).click()
  await createDialog
    .getByRole('textbox', { name: 'Name' })
    .last()
    .fill('L6_TOKEN')
  await createDialog
    .getByRole('textbox', { name: 'Secret reference' })
    .fill('env:L6_UI_TOKEN')

  await createDialog.getByRole('button', { name: 'Add wiring' }).click()
  await createDialog
    .getByRole('textbox', { name: 'Resource kind' })
    .fill('postgres.table')
  await createDialog
    .getByRole('textbox', { name: 'Resource', exact: true })
    .fill('public.l6_ui')
  await createDialog.getByRole('combobox', { name: 'Mode' }).click()
  await page.getByRole('option', { name: 'Read/Write' }).click()
  await createDialog
    .getByRole('textbox', { name: 'Secret reference (optional)' })
    .fill('vault:secret/data/l6-ui#dsn')
  await createDialog.getByRole('button', { name: 'Add wiring' }).click()
  await createDialog.getByRole('button', { name: 'Remove' }).last().click()
  await expect(
    createDialog.getByRole('textbox', { name: 'Resource kind' }),
  ).toHaveCount(1)

  const createWrite = observe(page, 'POST', '/v1/m/deploy/definitions', 201)
  await createDialog.getByRole('button', { name: 'Declare deployment' }).click()
  const created = (await (await createWrite).json()) as DeployDefinition
  expect(created).toMatchObject({
    name: definitionName,
    current_version: 1,
    applied_version: 0,
    desired_status: 'active',
  })
  expect(created.spec).toMatchObject({
    image: 'registry.invalid/l6-ui:v1',
    replicas: 1,
  })

  await definitionsSearch.fill(definitionName)
  const createdRow = await rowFor(page, definitionName)
  const createdDetailRead = observe(
    page,
    'GET',
    `/v1/m/deploy/definitions/${created.id}`,
    200,
  )
  await createdRow.click()
  await createdDetailRead
  const createdDetail = page.getByRole('dialog', { name: definitionName })
  await expect(createdDetail.getByText('Declared, never applied')).toBeVisible()
  await createdDetail.getByRole('button', { name: 'Edit' }).click()
  const editDialog = page.getByRole('dialog', { name: 'Edit deployment' })
  await expect(
    editDialog.getByRole('combobox', { name: 'Subject kind' }),
  ).toBeDisabled()
  await editDialog
    .getByRole('textbox', { name: 'Target' })
    .fill('docker.host/l6-ui-edited')
  await editDialog
    .getByRole('textbox', { name: 'Source (GitOps)' })
    .fill('git:local/l6-ui#v2')
  await editDialog
    .getByRole('textbox', { name: 'Change note' })
    .fill('lot 6 edit')
  await editDialog
    .getByRole('textbox', { name: 'Image' })
    .fill('registry.invalid/l6-ui:v2')
  await editDialog.getByRole('spinbutton', { name: 'Replicas' }).fill('2')
  await editDialog.getByRole('textbox', { name: 'Value' }).fill('200m')
  const editWrite = observe(
    page,
    'PUT',
    `/v1/m/deploy/definitions/${created.id}`,
    200,
  )
  await editDialog.getByRole('button', { name: 'Save revision' }).click()
  const edited = (await (await editWrite).json()) as DeployDefinition
  expect(edited).toMatchObject({ current_version: 2, applied_version: 0 })
  expect(edited.spec).toMatchObject({
    image: 'registry.invalid/l6-ui:v2',
    replicas: 2,
  })

  const revisionsRead = observe(
    page,
    'GET',
    `/v1/m/deploy/definitions/${created.id}/revisions`,
    200,
  )
  await createdDetail
    .getByRole('button', { name: 'Versions & rollback' })
    .click()
  await revisionsRead
  const revisionsDialog = page.getByRole('dialog', {
    name: 'Versions & rollback',
  })
  await expect(revisionsDialog.getByText('Version 1')).toBeVisible()
  await expect(revisionsDialog.getByText('Version 2')).toBeVisible()
  await revisionsDialog.getByRole('button', { name: 'Roll back' }).click()
  const rollbackDialog = page.getByRole('dialog', {
    name: 'Roll back desired state?',
  })
  await rollbackDialog
    .getByRole('textbox', { name: 'Change note (optional)' })
    .fill('lot 6 rollback')
  const rollbackConfirm = rollbackDialog.getByRole('button', {
    name: 'Roll back',
  })
  await expect(rollbackConfirm).toBeDisabled()
  const rollbackPhrase = rollbackDialog.getByRole('textbox', {
    name: 'Confirmation phrase',
  })
  await rollbackPhrase.fill('rollback')
  await expect(rollbackConfirm).toBeDisabled()
  await rollbackPhrase.fill('ROLLBACK')
  await expect(rollbackConfirm).toBeEnabled()
  const rollbackWrite = observe(
    page,
    'POST',
    `/v1/m/deploy/definitions/${created.id}/rollback`,
    200,
  )
  await rollbackConfirm.click()
  const rolledBack = (await (await rollbackWrite).json()) as DeployDefinition
  expect(rolledBack).toMatchObject({ current_version: 3, applied_version: 0 })
  const rolledBackDetail = await getJSON<DeployDefinition>(
    page,
    token,
    `/v1/m/deploy/definitions/${created.id}`,
  )
  expect(rolledBackDetail.spec).toMatchObject({
    image: 'registry.invalid/l6-ui:v1',
    replicas: 1,
  })
  await revisionsDialog.getByRole('button', { name: 'Close' }).click()

  await createdDetail.getByRole('button', { name: 'Delete' }).click()
  const deleteDialog = page.getByRole('dialog', {
    name: 'Delete deployment definition?',
  })
  await expect(
    deleteDialog.getByText(
      'This action is recorded in the tamper-evident audit ledger.',
    ),
  ).toBeVisible()
  const deleteWrite = observe(
    page,
    'DELETE',
    `/v1/m/deploy/definitions/${created.id}`,
    204,
  )
  await deleteDialog.getByRole('button', { name: 'Delete definition' }).click()
  await deleteWrite
  const definitionsAfterDelete = await getJSON<ListBody<DeployDefinition>>(
    page,
    token,
    '/v1/m/deploy/definitions',
  )
  expect(
    definitionsAfterDelete.items.some((item) => item.id === created.id),
  ).toBe(false)

  const wiringsRead = observe(page, 'GET', '/v1/m/deploy/wirings', 200)
  await page.getByRole('tab', { name: 'Wirings' }).click()
  await wiringsRead
  await page.getByPlaceholder('Search wirings…').fill('l6-gitops-agent')
  await expect(page.getByText('public.l6', { exact: true })).toBeVisible()
  await expect(page.getByText('Attribution unavailable')).toBeVisible()

  const operationsRead = observe(page, 'GET', '/v1/m/deploy/operations', 200)
  await page.getByRole('tab', { name: 'Operations' }).click()
  await operationsRead
  await page.getByPlaceholder('Search operations…').fill('apply')
  await expect(page.getByText('Applied', { exact: true })).toBeVisible()
  await expect(page.getByText('Approved', { exact: true })).toBeVisible()

  const viewerContext = await browser.newContext()
  const viewerPage = await viewerContext.newPage()
  await preparePage(viewerPage)
  await loginDemo(viewerPage, VIEWER_EMAIL, VIEWER_PASSWORD)
  const viewerDefinitionsRead = observe(
    viewerPage,
    'GET',
    '/v1/m/deploy/definitions',
    200,
  )
  await viewerPage.goto('/deploy')
  await viewerDefinitionsRead
  await expect(
    viewerPage.getByRole('button', { name: 'Declare deployment' }),
  ).not.toBeVisible()
  await viewerPage.getByPlaceholder('Search definitions…').fill('l6-gitops')
  const viewerGitops = await rowFor(viewerPage, 'l6-gitops')
  const viewerDetailRead = observe(
    viewerPage,
    'GET',
    '/v1/m/deploy/definitions/01a04208-28ed-7bcc-b20b-e90ee885ab7d',
    200,
  )
  await viewerGitops.click()
  await viewerDetailRead
  const viewerDetail = viewerPage.getByRole('dialog', { name: 'l6-gitops' })
  await expect(
    viewerDetail.getByRole('button', { name: 'Apply' }),
  ).toBeDisabled()
  await expect(
    viewerDetail.getByRole('button', { name: 'Retire' }),
  ).toBeDisabled()
  await expect(
    viewerDetail.getByRole('button', { name: 'Plan' }),
  ).not.toBeVisible()
  await expect(
    viewerDetail.getByRole('button', { name: 'Edit' }),
  ).not.toBeVisible()
  await viewerContext.close()
})

test('orchestration governs topology, schedules, revisions, and a two-person fire live', async ({
  page,
}) => {
  const token = await loginDemo(page)
  const secondAdminToken = await loginAPI(
    page,
    SECOND_ADMIN_EMAIL,
    SECOND_ADMIN_PASSWORD,
  )
  await page.context().grantPermissions(['clipboard-read', 'clipboard-write'], {
    origin: 'http://127.0.0.1:27650',
  })
  const scheduleName = `l6-ui-schedule-${Date.now()}`
  const directReads: string[] = []
  page.on('request', (request) => {
    const url = new URL(request.url())
    if (
      request.method() === 'GET' &&
      (url.pathname === '/v1/m/orchestration/graph/neighbors' ||
        url.pathname === '/v1/m/orchestration/timeline' ||
        /^\/v1\/m\/orchestration\/schedules\/[^/]+$/.test(url.pathname))
    ) {
      directReads.push(url.pathname)
    }
  })

  const schedulesBefore = await getJSON<ListBody<Schedule>>(
    page,
    token,
    '/v1/m/orchestration/schedules',
  )
  const manual = schedulesBefore.items.find(
    (schedule) => schedule.name === 'l6-manual',
  )
  expect(
    manual,
    'the live seed includes the governed manual schedule',
  ).toBeDefined()

  const graphRead = observe(page, 'GET', '/v1/m/orchestration/graph', 200)
  await page.goto('/orchestration')
  const graphResponse = await graphRead
  const graph = (await graphResponse.json()) as {
    nodes: unknown[]
    edges: unknown[]
    coverage: { caveats: string[] }
  }
  expect(graph.nodes).toHaveLength(6)
  expect(graph.edges).toHaveLength(4)
  expect(graph.coverage.caveats[0]).toContain('ABSENT, not zero')
  const graphRegion = page.getByRole('group', {
    name: 'Communication graph',
  })
  await expect(graphRegion).toBeVisible()
  await expect(graphRegion.getByText('sess-coder-7a3f')).toBeVisible()
  const viewport = graphRegion.locator('.react-flow__viewport')
  const initialTransform = await viewport.getAttribute('style')
  await graphRegion.locator('.react-flow__controls-zoomin').click()
  await expect
    .poll(() => viewport.getAttribute('style'))
    .not.toBe(initialTransform)
  await graphRegion.locator('.react-flow__controls-zoomout').click()
  await graphRegion.locator('.react-flow__controls-fitview').click()

  let flowsRead = observe(page, 'GET', '/v1/m/orchestration/flows', 200)
  await page.getByRole('tab', { name: 'Flows' }).click()
  await flowsRead
  await expect(await rowFor(page, 'sess-coder-7a3f')).toContainText(
    'worker-indexer',
  )
  const flowFilter = page.getByRole('combobox', { name: 'Filter by state' })
  flowsRead = observe(page, 'GET', '/v1/m/orchestration/flows', 200)
  await flowFilter.click()
  await page.getByRole('option', { name: 'Completed', exact: true }).click()
  const filteredFlows = await flowsRead
  expect(new URL(filteredFlows.url()).searchParams.get('state')).toBe(
    'completed',
  )
  await expect(await rowFor(page, 'sess-coder-7a3f')).toContainText('Completed')

  const schedulesRead = observe(
    page,
    'GET',
    '/v1/m/orchestration/schedules',
    200,
  )
  await page.getByRole('tab', { name: 'Schedules' }).click()
  await schedulesRead
  await expect(await rowFor(page, 'l6-nightly')).toContainText('30 2 * * *')
  await expect(await rowFor(page, 'l6-manual')).toContainText('Manual')

  await page.getByRole('button', { name: 'New schedule' }).click()
  const createDialog = page.getByRole('dialog', { name: 'New schedule' })
  await createDialog.getByRole('textbox', { name: 'Name' }).fill(scheduleName)
  const subjectKind = createDialog.getByRole('combobox', {
    name: 'Subject kind',
  })
  await subjectKind.click()
  await page.getByRole('option', { name: 'Swarm', exact: true }).click()
  await subjectKind.click()
  await page.getByRole('option', { name: 'Agent', exact: true }).click()
  const triggerKind = createDialog.getByRole('combobox', {
    name: 'Trigger kind',
  })
  await triggerKind.click()
  await page.getByRole('option', { name: 'Event', exact: true }).click()
  await triggerKind.click()
  await page.getByRole('option', { name: 'Manual', exact: true }).click()
  await createDialog
    .getByRole('textbox', { name: 'Subject reference' })
    .fill('l6-ui-agent-v1')
  await createDialog
    .getByRole('textbox', { name: 'Cadence specification' })
    .fill('operator initiated')
  await createDialog.getByRole('spinbutton', { name: 'Grace factor' }).fill('4')
  const createWrite = observe(
    page,
    'POST',
    '/v1/m/orchestration/schedules',
    201,
  )
  const createRefresh = observe(
    page,
    'GET',
    '/v1/m/orchestration/schedules',
    200,
  )
  await createDialog.getByRole('button', { name: 'Create schedule' }).click()
  const created = (await (await createWrite).json()) as Schedule
  await createRefresh
  expect(created).toMatchObject({
    name: scheduleName,
    subject_ref: 'l6-ui-agent-v1',
    trigger_kind: 'manual',
    desired_status: 'active',
  })

  await chooseScheduleAction(page, scheduleName, 'Edit')
  const editDialog = page.getByRole('dialog', { name: 'Edit schedule' })
  await expect(
    editDialog.getByRole('combobox', { name: 'Subject kind' }),
  ).toBeDisabled()
  await expect(
    editDialog.getByRole('combobox', { name: 'Trigger kind' }),
  ).toBeDisabled()
  await editDialog
    .getByRole('textbox', { name: 'Subject reference' })
    .fill('l6-ui-agent-v2')
  await editDialog
    .getByRole('textbox', { name: 'Cadence specification' })
    .fill('operator initiated v2')
  await editDialog.getByRole('spinbutton', { name: 'Grace factor' }).fill('5')
  const editWrite = observe(
    page,
    'PATCH',
    `/v1/m/orchestration/schedules/${created.id}`,
    200,
  )
  const editRefresh = observe(page, 'GET', '/v1/m/orchestration/schedules', 200)
  await editDialog.getByRole('button', { name: 'Save changes' }).click()
  const editedResponse = await editWrite
  const edited = (await editedResponse.json()) as Schedule
  await editRefresh
  expect(edited).toMatchObject({
    id: created.id,
    subject_ref: 'l6-ui-agent-v2',
    cadence_spec: 'operator initiated v2',
  })
  expect(editedResponse.request().postDataJSON()).toEqual({
    subject_ref: 'l6-ui-agent-v2',
    cadence_spec: 'operator initiated v2',
    grace_factor: 5,
  })

  const historyRead = observe(
    page,
    'GET',
    `/v1/m/orchestration/schedules/${created.id}/revisions`,
    200,
  )
  await chooseScheduleAction(page, scheduleName, 'History')
  await historyRead
  const history = page.getByRole('dialog', {
    name: `Revision history — ${scheduleName}`,
  })
  await expect(history.getByText('Created', { exact: true })).toBeVisible()
  await expect(history.getByText('Updated', { exact: true })).toBeVisible()
  await expect(history.getByRole('checkbox', { checked: true })).toHaveCount(2)
  await expect(
    history.getByRole('textbox', { name: 'Earlier revision' }),
  ).toBeVisible()
  await expect(
    history.getByRole('textbox', { name: 'Later revision' }),
  ).toBeVisible()
  await history
    .getByRole('button', { name: 'Restore this revision' })
    .first()
    .click()
  const restoreDialog = page.getByRole('dialog', { name: 'Restore revision?' })
  const restoreWrite = observe(
    page,
    'POST',
    `/v1/m/orchestration/schedules/${created.id}/restore`,
    200,
  )
  await restoreDialog.getByRole('button', { name: 'Restore revision' }).click()
  const restored = (await (await restoreWrite).json()) as Schedule
  expect(restored).toMatchObject({
    id: created.id,
    subject_ref: 'l6-ui-agent-v1',
    cadence_spec: 'operator initiated',
  })
  await history.getByRole('button', { name: 'Close' }).click()

  await chooseScheduleAction(page, scheduleName, 'Pause')
  let statusDialog = page.getByRole('dialog', {
    name: `Pause ${scheduleName}?`,
  })
  let statusWrite = observe(
    page,
    'PATCH',
    `/v1/m/orchestration/schedules/${created.id}`,
    200,
  )
  let statusRefresh = observe(page, 'GET', '/v1/m/orchestration/schedules', 200)
  await statusDialog.getByRole('button', { name: 'Pause schedule' }).click()
  expect((await (await statusWrite).json()) as Schedule).toMatchObject({
    desired_status: 'paused',
  })
  await statusRefresh

  await chooseScheduleAction(page, scheduleName, 'Resume')
  statusDialog = page.getByRole('dialog', {
    name: `Resume ${scheduleName}?`,
  })
  statusWrite = observe(
    page,
    'PATCH',
    `/v1/m/orchestration/schedules/${created.id}`,
    200,
  )
  statusRefresh = observe(page, 'GET', '/v1/m/orchestration/schedules', 200)
  await statusDialog.getByRole('button', { name: 'Resume schedule' }).click()
  expect((await (await statusWrite).json()) as Schedule).toMatchObject({
    desired_status: 'active',
  })
  await statusRefresh

  await chooseScheduleAction(page, scheduleName, 'Retire')
  statusDialog = page.getByRole('dialog', {
    name: `Retire ${scheduleName}?`,
  })
  const retireButton = statusDialog.getByRole('button', {
    name: 'Retire schedule',
  })
  const retirePhrase = statusDialog.getByRole('textbox', {
    name: 'Confirmation phrase',
  })
  await retirePhrase.fill('RETIRE')
  await expect(retireButton).toBeDisabled()
  await retirePhrase.fill(scheduleName)
  await expect(retireButton).toBeEnabled()
  statusWrite = observe(
    page,
    'PATCH',
    `/v1/m/orchestration/schedules/${created.id}`,
    200,
  )
  statusRefresh = observe(page, 'GET', '/v1/m/orchestration/schedules', 200)
  await retireButton.click()
  expect((await (await statusWrite).json()) as Schedule).toMatchObject({
    desired_status: 'retired',
  })
  await statusRefresh

  await chooseScheduleAction(page, 'l6-manual', 'Fire')
  const fireDialog = page.getByRole('dialog', { name: 'Fire l6-manual' })
  const phase1Write = observe(
    page,
    'POST',
    `/v1/m/orchestration/schedules/${manual!.id}/fire`,
    202,
  )
  await fireDialog.getByRole('button', { name: 'Request approval' }).click()
  const phase1 = (await (await phase1Write).json()) as FireReceipt & {
    requires_approval: boolean
  }
  expect(phase1).toMatchObject({
    op_status: 'requested',
    gate_status: 'pending',
    requires_approval: true,
  })
  expect(phase1.approval_ref).toBeTruthy()
  await fireDialog.getByRole('button', { name: 'Copy', exact: true }).click()
  await expect
    .poll(() => page.evaluate(() => navigator.clipboard.readText()))
    .toBe(phase1.approval_ref)

  const decision = await page.request.post(
    `/v1/m/governance/approvals/${phase1.approval_ref}/decisions`,
    {
      headers: authHeaders(secondAdminToken),
      data: { decision: 'approve', note: 'independent lot 6 decision' },
    },
  )
  expect(decision.status()).toBe(200)

  const phase2Write = observe(
    page,
    'POST',
    `/v1/m/orchestration/schedules/${manual!.id}/fire`,
    200,
  )
  await fireDialog.getByRole('button', { name: 'Execute' }).click()
  const phase2 = (await (await phase2Write).json()) as FireReceipt
  expect(phase2).toMatchObject({
    approval_ref: phase1.approval_ref,
    op_status: 'declared_not_fired',
    gate_status: 'approved',
    detail: 'approved; no dispatcher wired (declared, not fired)',
  })
  await expect(
    fireDialog.getByText('Approved, not actuated', { exact: true }),
  ).toBeVisible()

  await fireDialog
    .getByRole('link', {
      name: 'Open the governance approvals queue',
    })
    .click()
  await expect(page).toHaveURL(/\/permissions$/)

  const graphReadAgain = observe(page, 'GET', '/v1/m/orchestration/graph', 200)
  await page.goto('/orchestration')
  await graphReadAgain
  const schedulesReadAgain = observe(
    page,
    'GET',
    '/v1/m/orchestration/schedules',
    200,
  )
  await page.getByRole('tab', { name: 'Schedules' }).click()
  await schedulesReadAgain

  const decisionRead = observe(
    page,
    'GET',
    `/v1/m/orchestration/schedules/${manual!.id}/decisions`,
    200,
  )
  const decisionPicker = page.getByRole('combobox', {
    name: 'Select a schedule',
  })
  await decisionPicker.click()
  await page.getByRole('option', { name: 'l6-manual', exact: true }).click()
  await decisionRead
  await expect(
    page
      .getByText('Approved, not actuated (no dispatcher)', { exact: true })
      .first(),
  ).toBeVisible()

  const estateRead = observe(page, 'GET', '/v1/m/orchestration/decisions', 200)
  await decisionPicker.click()
  await page
    .getByRole('option', { name: 'All decisions (whole estate)', exact: true })
    .click()
  await estateRead
  await expect(page.getByText(/only view in the console/i)).toBeVisible()

  expect(
    directReads,
    'neighbors, timeline, and schedule(id) have API wrappers but no UI caller',
  ).toEqual([])
})
