// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// One authorised console journey against a live `--seed-demo` engine:
// work item (list → detail → lease) → message → ack → handoff → reload.
// State after reload is the engine's, not a client store. Nothing on the
// accepted path is intercepted.
import {
  expect,
  test,
  type APIRequestContext,
  type Locator,
  type Page,
} from '@playwright/test'
import { copyFileSync, mkdirSync, writeFileSync } from 'node:fs'
import path from 'node:path'
import { withAwaitedProcessObservation } from './process-observation.ts'

const BASE = process.env.K3_E2E_BASE ?? ''
const TENANT = process.env.DEMO_TENANT ?? ''
const WORK = process.env.K3_E2E_WORK ?? ''
const DEMO_EMAIL = 'demo@olivares.local'
const DEMO_PASSWORD = 'olivares-demo-estate'
const A_EMAIL = 'k3-work-a@olivares.local'
const B_EMAIL = 'k3-work-b@olivares.local'
const MEMBER_PASSWORD = 'k3-work-member-passphrase-42'
const STAMP = Date.now().toString(36)
// An optional second home for the captures, for a caller who collects evidence
// outside the repository. Unset, the run writes only inside its own work and
// report directories: a test that ships must not reach into a tree that is not
// in this repository, and the path it used to hardcode is not published.
const REPORT_DIR = process.env.WORK_SURFACES_EVIDENCE_DIR ?? ''
const UUID_ANY =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i
const WORK_ITEMS = '/v1/m/sessions/work-items'

test.describe.configure({ mode: 'serial' })

let tokenAdmin = ''
let tokenA = ''
let tokenB = ''
let userA = ''
let userB = ''
let wsBilling = ''
let channelId = ''
let workItemId = ''
const workTitle = `Work surface ${STAMP}`
let messageId = ''
let deliveryId = ''
let handoffDeliveryId = ''
const evidence: Record<string, unknown> = {}

function evidenceDir(): string {
  const dir = path.join(WORK, 'evidence-work-surfaces')
  mkdirSync(dir, { recursive: true })
  return dir
}

async function shot(page: Page, name: string) {
  const file = path.join(evidenceDir(), `${name}.png`)
  await page.screenshot({ path: file, fullPage: true })
  if (REPORT_DIR) {
    mkdirSync(REPORT_DIR, { recursive: true })
    copyFileSync(file, path.join(REPORT_DIR, `${name}.png`))
  }
  const pub = path.join('playwright-report', 'work-surfaces')
  mkdirSync(pub, { recursive: true })
  copyFileSync(file, path.join(pub, `${name}.png`))
}

async function api(
  request: APIRequestContext,
  token: string,
  method: 'GET' | 'POST',
  route: string,
  body?: unknown,
  headers: Record<string, string> = {},
) {
  const res = await request.fetch(`${BASE}${route}`, {
    method,
    headers: {
      Authorization: `Bearer ${token}`,
      'X-Olivares-Tenant': TENANT,
      ...(body !== undefined ? { 'Content-Type': 'application/json' } : {}),
      ...headers,
    },
    data: body !== undefined ? JSON.stringify(body) : undefined,
  })
  const text = await res.text()
  let json: unknown
  try {
    json = text ? JSON.parse(text) : undefined
  } catch {
    json = undefined
  }
  return { status: res.status(), json, headers: res.headers() }
}

let unavailableRetries = 0
async function apiRead(
  request: APIRequestContext,
  token: string,
  route: string,
) {
  let last = await api(request, token, 'GET', route)
  for (let i = 0; i < 8 && last.status === 503; i++) {
    unavailableRetries++
    await new Promise((r) => setTimeout(r, 1500))
    last = await api(request, token, 'GET', route)
  }
  return last
}

function uuidv7(): string {
  const bytes = new Uint8Array(16)
  crypto.getRandomValues(bytes)
  const ms = BigInt(Date.now())
  for (let i = 0; i < 6; i++)
    bytes[i] = Number((ms >> BigInt(8 * (5 - i))) & 0xffn)
  bytes[6] = (bytes[6] & 0x0f) | 0x70
  bytes[8] = (bytes[8] & 0x3f) | 0x80
  const hex = Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('')
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`
}

async function login(
  request: APIRequestContext,
  email: string,
  password: string,
): Promise<{ token: string; userId: string }> {
  const res = await request.post(`${BASE}/v1/auth/login`, {
    data: { email, password },
  })
  expect(res.status(), `login ${email}`).toBe(200)
  const token = ((await res.json()) as { token: string }).token
  const who = await api(request, token, 'GET', '/v1/auth/whoami')
  expect(who.status).toBe(200)
  return { token, userId: (who.json as { user_id: string }).user_id }
}

async function createMember(
  request: APIRequestContext,
  email: string,
  role: string,
): Promise<string> {
  const created = await api(request, tokenAdmin, 'POST', '/v1/users', {
    email,
    password: MEMBER_PASSWORD,
  })
  expect(created.status, `create ${email}`).toBe(201)
  const id = (created.json as { id: string }).id
  const granted = await api(request, tokenAdmin, 'POST', '/v1/memberships', {
    user_id: id,
    tenant: TENANT,
    role,
  })
  expect(granted.status, `membership ${email}`).toBe(201)
  return id
}

async function uiLogin(page: Page, email: string, password: string) {
  await page.goto('/login')
  await page.locator('#email').fill(email)
  await page.locator('#password').fill(password)
  await page.getByRole('button', { name: /^sign in$/i }).click()
  await page.waitForURL((u) => !u.pathname.startsWith('/login'))
}

async function selectWorkspace(page: Page, name: 'Billing' | 'Default') {
  const trigger = page
    .getByRole('button', {
      name: /All workspaces|Billing|Default|Workspace not in list/i,
    })
    .first()
  await expect(trigger).toBeVisible()
  const label = ((await trigger.textContent()) ?? '').trim()
  if (new RegExp(`^${name}`, 'i').test(label)) return
  const openDialog = page.getByRole('dialog')
  if (
    (await openDialog.count()) > 0 &&
    (await openDialog.first().isVisible())
  ) {
    await page.keyboard.press('Escape')
    await expect(openDialog.first()).toBeHidden()
  }
  await trigger.click()
  await page
    .getByRole('menuitem', { name: new RegExp(`^${name}`, 'i') })
    .first()
    .click()
}

function localDateTimeValue(at: Date): string {
  const p = (n: number) => String(n).padStart(2, '0')
  return `${at.getFullYear()}-${p(at.getMonth() + 1)}-${p(at.getDate())}T${p(at.getHours())}:${p(at.getMinutes())}:${p(at.getSeconds())}`
}

function workSheet(page: Page): Locator {
  return page.getByRole('dialog').filter({
    has: page.getByRole('heading', {
      name: new RegExp(workTitle.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')),
    }),
  })
}

test.beforeEach(async ({ browser }) => {
  await withAwaitedProcessObservation(
    { role: 'browser', phase: 'beforeEach', browser },
    () => undefined,
  )
})

test.beforeAll(async ({ request }) => {
  expect(BASE, 'K3_E2E_BASE is set by the global setup').not.toBe('')
  expect(TENANT, 'DEMO_TENANT is set by the global setup').not.toBe('')
  tokenAdmin = (await login(request, DEMO_EMAIL, DEMO_PASSWORD)).token
  const ws = await api(request, tokenAdmin, 'GET', '/v1/workspaces?limit=100')
  expect(ws.status).toBe(200)
  const items = (ws.json as { items: { id: string; slug: string }[] }).items
  wsBilling = items.find((w) => w.slug === 'billing')?.id ?? ''
  expect(wsBilling, 'the seeded Billing workspace').not.toBe('')
  userA = await createMember(request, A_EMAIL, 'owner')
  userB = await createMember(request, B_EMAIL, 'editor')
  tokenA = (await login(request, A_EMAIL, MEMBER_PASSWORD)).token
  tokenB = (await login(request, B_EMAIL, MEMBER_PASSWORD)).token

  const channel = await api(
    request,
    tokenA,
    'POST',
    '/v1/m/sessions/channels',
    {
      workspace_id: wsBilling,
      slug: `work-${STAMP}`,
      name: 'Work surfaces channel',
      description: 'Carrier for the work-surfaces console journey',
      initial_grants: [
        {
          subject: { kind: 'user', ref: userA },
          can_read: true,
          can_write: true,
          can_admin: true,
        },
        {
          subject: { kind: 'user', ref: userB },
          can_read: true,
          can_write: true,
          can_admin: false,
        },
      ],
    },
  )
  expect(channel.status, 'create the work-surfaces channel').toBe(201)
  channelId = (channel.json as { channel: { id: string } }).channel.id

  const created = await api(
    request,
    tokenA,
    'POST',
    `${WORK_ITEMS}?mode=apply`,
    {
      workspace_id: wsBilling,
      work_kind: 'implementation',
      title: workTitle,
      brief_md: 'Console work surfaces: work, message, ack, handoff, reload.',
      context_refs: [],
      priority: 'p1',
      owner_kind: 'user',
      owner_ref: userA,
      provenance_kind: 'human',
      provenance_ref: `journey:work-surfaces-${STAMP}`,
      acceptance: [
        {
          criterion_key: 'transfer',
          ordinal: 0,
          statement: 'The authorised journey survives reload from the API.',
          required: true,
        },
      ],
    },
    { 'Idempotency-Key': uuidv7() },
  )
  expect(created.status, 'create work item').toBe(200)
  workItemId = (created.json as { result_id: string }).result_id
  expect(workItemId).toMatch(UUID_ANY)
  const ready = await api(
    request,
    tokenA,
    'POST',
    `${WORK_ITEMS}/${workItemId}/transitions?mode=apply`,
    { command: 'item.ready' },
    { 'If-Match': created.headers.etag, 'Idempotency-Key': uuidv7() },
  )
  expect(ready.status, 'ready work item').toBe(200)
  evidence.actors = { a: userA, b: userB }
  evidence.work_item_id = workItemId
  evidence.channel_id = channelId
})

test.afterAll(async ({ browser }) => {
  evidence.unavailable_read_retries = unavailableRetries
  evidence.browser = {
    name: browser.browserType().name(),
    version: browser.version(),
  }
  writeFileSync(
    path.join(evidenceDir(), 'work-surfaces-journey.json'),
    JSON.stringify(evidence, null, 2),
  )
})

test('authorised journey: work → message → ack → handoff → reload', async ({
  browser,
  request,
}) => {
  test.setTimeout(300_000)
  const ctxA = await browser.newContext()
  const a = await ctxA.newPage()
  await uiLogin(a, A_EMAIL, MEMBER_PASSWORD)

  await a.goto('/work')
  await selectWorkspace(a, 'Billing')
  await expect(
    a.getByRole('heading', { name: 'Work', exact: true }),
  ).toBeVisible()
  const row = a.getByRole('button', { name: new RegExp(workTitle) })
  await expect(row).toBeVisible()
  await shot(a, '01-work-list')

  const itemRead = a.waitForResponse(
    (r) =>
      r.request().method() === 'GET' &&
      new URL(r.url()).pathname === `${WORK_ITEMS}/${workItemId}`,
  )
  await row.click()
  expect((await itemRead).status()).toBe(200)
  const sheet = workSheet(a)
  await expect(sheet).toBeVisible()
  expect(new URL(a.url()).searchParams.get('item')).toBe(workItemId)
  await expect(sheet.locator('[data-slot="work-detail"]')).toBeVisible()
  await expect(
    sheet.getByRole('button', { name: 'Offer handoff' }),
  ).toBeEnabled()
  await shot(a, '02-work-detail')

  const leaseRead = a.waitForResponse(
    (r) =>
      r.request().method() === 'GET' &&
      new URL(r.url()).pathname === `${WORK_ITEMS}/${workItemId}/lease`,
  )
  await sheet.getByRole('tab', { name: 'Lease' }).click()
  expect((await leaseRead).status()).toBe(200)
  await expect(sheet.locator('[data-slot="work-lease"]')).toBeVisible()
  expect(new URL(a.url()).searchParams.get('detail')).toBe('lease')
  await shot(a, '03-work-lease')

  await a.goto('/communications')
  await expect(a.getByRole('heading', { name: 'Communications' })).toBeVisible()
  await selectWorkspace(a, 'Billing')
  await a
    .getByRole('searchbox')
    .or(a.getByPlaceholder(/Search/))
    .first()
    .fill(`work-${STAMP}`)
  const channelRow = a.getByRole('row').filter({ hasText: `work-${STAMP}` })
  await expect(channelRow).toHaveCount(1)
  await channelRow.click()
  const channelSheet = a.getByRole('dialog')
  await expect(channelSheet).toBeVisible()
  await channelSheet.getByRole('button', { name: 'Send notice' }).click()
  const compose = a
    .getByRole('dialog')
    .filter({ hasText: 'Send a direct notice' })
  await compose.getByRole('textbox', { name: 'Reference (ID)' }).fill(userB)
  await compose.getByLabel(/^Subject/).fill(`Work notice ${STAMP}`)
  await compose.getByLabel('Text').fill('Work surfaces message body')
  await compose.getByRole('button', { name: 'Compose' }).click()
  await expect(compose.getByText('Confirm the intention')).toBeVisible()
  const sendResponse = a.waitForResponse((r) =>
    r.url().endsWith('/v1/m/sessions/messages/send'),
  )
  await compose.getByRole('button', { name: 'Confirm and send' }).click()
  const sent = await sendResponse
  expect(sent.status()).toBe(201)
  const sentBody = (await sent.json()) as {
    message_id: string
    delivery_id: string
  }
  messageId = sentBody.message_id
  deliveryId = sentBody.delivery_id
  await expect(compose.getByText('Notice published').first()).toBeVisible()
  await ctxA.close()

  const ctxB = await browser.newContext()
  const b = await ctxB.newPage()
  await uiLogin(b, B_EMAIL, MEMBER_PASSWORD)
  await b.goto('/communications/inbox')
  await expect(
    b.getByRole('heading', { name: 'Communications inbox' }),
  ).toBeVisible()
  await selectWorkspace(b, 'Billing')
  const inboxRow = b
    .getByRole('row')
    .filter({ hasText: `Work notice ${STAMP}` })
  await expect(inboxRow).toHaveCount(1)
  await inboxRow.click()
  const deliverySheet = b.getByRole('dialog')
  await expect(
    deliverySheet.getByText('Work surfaces message body'),
  ).toBeVisible()
  await shot(b, '04-message-delivery')
  await deliverySheet.getByRole('button', { name: 'Acknowledge' }).click()
  const ackResponse = b.waitForResponse((r) =>
    r.url().endsWith(`/v1/m/sessions/deliveries/${deliveryId}/ack`),
  )
  await deliverySheet
    .getByRole('button', { name: 'Confirm acknowledgement' })
    .click()
  const acked = await ackResponse
  expect(acked.status()).toBe(200)
  await expect(deliverySheet.locator('[data-slot="ack-receipt"]')).toBeVisible()
  await ctxB.close()

  const ctxA2 = await browser.newContext()
  const a2 = await ctxA2.newPage()
  await uiLogin(a2, A_EMAIL, MEMBER_PASSWORD)
  await a2.goto('/work')
  await selectWorkspace(a2, 'Billing')
  await a2.goto(`/work?item=${workItemId}`)
  const sheet2 = workSheet(a2)
  await expect(sheet2).toBeVisible()
  await expect(sheet2.locator('[data-slot="work-offer-handoff"]')).toBeVisible()
  const freshRead = a2.waitForResponse(
    (r) =>
      r.request().method() === 'GET' &&
      new URL(r.url()).pathname === `${WORK_ITEMS}/${workItemId}`,
  )
  await sheet2.locator('[data-slot="work-offer-handoff"]').click()
  expect((await freshRead).status()).toBe(200)
  const offer = a2.getByRole('dialog').filter({
    has: a2.getByRole('heading', { name: 'Offer handoff', exact: true }),
  })
  await expect(offer).toBeVisible()
  await offer.getByLabel('Channel', { exact: true }).fill(channelId)
  await offer.getByRole('textbox', { name: 'Reference (ID)' }).fill(userB)
  await offer
    .getByLabel('Summary', { exact: true })
    .fill(`Work handoff ${STAMP}`)
  await offer
    .getByLabel('Next action', { exact: true })
    .fill('Continue the work-surfaces journey')
  await offer
    .getByLabel('Response deadline', { exact: true })
    .fill(localDateTimeValue(new Date(Date.now() + 30 * 60 * 60 * 1000)))
  await offer.getByRole('button', { name: 'Review offer' }).click()
  await expect(
    offer.locator('[data-slot="handoff-offer-intent"]'),
  ).toBeVisible()
  const offerPost = a2.waitForResponse(
    (r) =>
      r.request().method() === 'POST' &&
      new URL(r.url()).pathname === '/v1/m/sessions/handoffs',
  )
  await offer.getByRole('button', { name: 'Confirm and offer' }).click()
  const offered = await offerPost
  expect(offered.status()).toBe(201)
  const offerResult = (await offered.json()) as {
    handoff_id: string
    delivery_id: string
  }
  handoffDeliveryId = offerResult.delivery_id
  expect(offerResult.handoff_id).toMatch(UUID_ANY)
  await expect(offer.getByText('Offer created')).toBeVisible()
  await ctxA2.close()

  const ctxB2 = await browser.newContext()
  const b2 = await ctxB2.newPage()
  await uiLogin(b2, B_EMAIL, MEMBER_PASSWORD)
  await b2.goto('/communications/handoffs')
  await selectWorkspace(b2, 'Billing')
  await expect(
    b2.getByRole('heading', { level: 1, name: 'Handoffs', exact: true }),
  ).toBeVisible()
  const handoffRow = b2.getByRole('row').filter({ hasText: workItemId })
  await expect(handoffRow).toBeVisible()
  await handoffRow.click()
  const handoffSheet = b2.getByRole('dialog').filter({
    has: b2.getByRole('heading', { name: 'Handoff offer', exact: true }),
  })
  await expect(
    handoffSheet.locator('[data-slot="handoff-detail"]'),
  ).toBeVisible()
  await expect(handoffSheet.getByText(`Work handoff ${STAMP}`)).toBeVisible()
  await expect(
    handoffSheet.getByRole('button', { name: 'Accept responsibility' }),
  ).toBeEnabled()
  await ctxB2.close()

  const ctxA3 = await browser.newContext()
  const a3 = await ctxA3.newPage()
  await uiLogin(a3, A_EMAIL, MEMBER_PASSWORD)
  await a3.goto('/work')
  await selectWorkspace(a3, 'Billing')
  await a3.goto(`/work?item=${workItemId}&detail=lease`)
  const reloaded = workSheet(a3)
  await expect(reloaded).toBeVisible()
  expect(new URL(a3.url()).searchParams.get('item')).toBe(workItemId)
  expect(new URL(a3.url()).searchParams.get('detail')).toBe('lease')
  await expect(reloaded.locator('[data-slot="work-lease"]')).toBeVisible()
  const viaHttp = await apiRead(request, tokenA, `${WORK_ITEMS}/${workItemId}`)
  expect(viaHttp.status).toBe(200)
  expect((viaHttp.json as { item: { title: string } }).item.title).toBe(
    workTitle,
  )
  const ackHttp = await apiRead(
    request,
    tokenB,
    `/v1/m/sessions/deliveries/${deliveryId}`,
  )
  expect(ackHttp.status).toBe(200)
  expect(
    (ackHttp.json as { delivery: { acknowledged_at: string | null } }).delivery
      .acknowledged_at,
  ).not.toBeNull()
  const handoffHttp = await apiRead(
    request,
    tokenB,
    `/v1/m/sessions/deliveries/${handoffDeliveryId}/handoff`,
  )
  expect(handoffHttp.status).toBe(200)
  expect(
    (handoffHttp.json as { handoff: { state: string } }).handoff.state,
  ).toBe('offered')
  evidence.message_id = messageId
  evidence.delivery_id = deliveryId
  evidence.handoff_delivery_id = handoffDeliveryId
  await ctxA3.close()
})

test('keyboard reaches the work list, detail tabs and labelled lease actions', async ({
  browser,
}) => {
  const ctx = await browser.newContext()
  const page = await ctx.newPage()
  await uiLogin(page, A_EMAIL, MEMBER_PASSWORD)
  await page.goto('/work')
  await selectWorkspace(page, 'Billing')
  const row = page.getByRole('button', { name: new RegExp(workTitle) })
  await expect(row).toBeVisible()
  await row.focus()
  await expect(row).toBeFocused()
  await page.keyboard.press('Enter')
  const sheet = workSheet(page)
  await expect(sheet).toBeVisible()
  const leaseTab = sheet.getByRole('tab', { name: 'Lease' })
  await leaseTab.focus()
  await page.keyboard.press('Enter')
  await expect(sheet.locator('[data-slot="work-lease"]')).toBeVisible()
  await expect(
    sheet.getByRole('button', { name: 'Acquire' }),
  ).toHaveAccessibleName('Acquire')
  await expect(
    sheet.getByRole('tab', { name: 'Overview' }),
  ).toHaveAccessibleName('Overview')
  await ctx.close()
})
