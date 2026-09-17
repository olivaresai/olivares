// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// K3 I1 — THE FIRST REAL COMMUNICATION JOURNEY IN THE BROWSER, against a live engine.
//
// Two real authenticated actors on a `--seed-demo` estate: A (the demo superadmin,
// owner of the demo tenant) and B (an editor created through the real users and
// memberships routes), plus C (an editor with a write grant and NO read grant).
// A creates a channel from the New-channel door with explicit grants, sends a
// direct notice from the channel card; B opens the inbox, the delivery and the
// message, acknowledges with the version it read, and re-reads the durable result —
// also after an engine restart and a page reload. Around the accepted path: stale
// CAS (412, nothing re-sent), an ambiguous transport result retried with the same
// key (one message, replayed receipt), a refused send with zero effect, revocation,
// pagination against a hidden channel, workspace and tenant changes, keyboard,
// mobile, light/dark and en/es evidence.
//
// NOTHING on the accepted path is intercepted. The two `page.route` handlers in
// this file serve the AMBIGUOUS cases only (the I1 send, and the I3 offer of the
// section below): each forwards the request to the engine and then drops the
// RESPONSE, which is exactly what a transport failure after the server applied
// the write looks like. Every assertion about state is made against the engine
// over HTTP, with a token that never leaves this process.
//
// K3 I3 — the WORK HANDOFF through the real console — follows the I1 tests in the
// section headed "K3 I3" at the end of this file: four finite tests selected with
// `--grep 'K3 I3:'`, their own request-only beforeAll and their own typed helpers.
import {
  expect,
  test,
  type APIRequestContext,
  type Locator,
  type Page,
  type Route,
} from '@playwright/test'
import { spawn } from 'node:child_process'
import {
  copyFileSync,
  existsSync,
  mkdirSync,
  openSync,
  readFileSync,
  writeFileSync,
} from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { engineEnv } from './global-setup'
import { withAwaitedProcessObservation } from './process-observation.ts'
import {
  engineLifecycleRequired,
  publishObservedEngine,
  stopObservedEngine,
  recordUnobservedSpawn,
} from './engine-lifecycle.ts'
// Wire types of the four I3 operations, derived from the generated OpenAPI paths by
// the communications feature itself, and the Work Interface's own projections.
// Type-only: nothing of the product is loaded into the runner.
import type {
  ChannelGrant,
  ChannelMutationResult,
  HandoffDetail,
  HandoffInboxPage,
  HandoffOfferInput,
  HandoffOfferResult,
  HandoffResponseInput,
  HandoffResponseResult,
} from '../src/features/communications/types'
import type {
  CommandResult,
  WorkLease,
  WorkSnapshot,
} from '../src/features/work/types'

const BASE = process.env.K3_E2E_BASE ?? ''
const TENANT = process.env.DEMO_TENANT ?? ''
const WORK = process.env.K3_E2E_WORK ?? ''
const DEMO_EMAIL = 'demo@olivares.local'
const DEMO_PASSWORD = 'olivares-demo-estate'
const A_EMAIL = 'k3-journey-a@olivares.local'
const B_EMAIL = 'k3-journey-b@olivares.local'
const C_EMAIL = 'k3-journey-c@olivares.local'
const MEMBER_PASSWORD = 'k3-journey-member-passphrase-42'
const STAMP = Date.now().toString(36)

test.describe.configure({ mode: 'serial' })

// ─── fixtures the journey provisions through the REAL API ───────────────────────
let tokenAdmin = ''
let tokenA = ''
let tokenB = ''
let tokenC = ''
let userA = ''
let userB = ''
let userC = ''
let wsBilling = ''
let wsDefault = ''
let channelId = ''
const channelSlug = `k3-journey-${STAMP}`
let grantIdB = ''
let messageId1 = ''
let deliveryId1 = ''
let deliveryId2 = ''
let auditHeadBeforeRefusal = 0
const evidence: Record<string, unknown> = {}

function evidenceDir(): string {
  const dir = path.join(WORK, 'evidence')
  mkdirSync(dir, { recursive: true })
  return dir
}
async function shot(page: Page, name: string) {
  const file = path.join(evidenceDir(), `${name}.png`)
  await page.screenshot({ path: file, fullPage: true })
  const pub = path.join('playwright-report', 'k3')
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

/**
 * A GET that tolerates the engine's TRANSIENT "could not look" (503
 * `evidence_unavailable`, verdict NO_HE_PODIDO_MIRAR): on this single-writer SQLite
 * estate a read issued right behind a write can fail to pin its facts and the engine
 * says so instead of answering empty. Reads are idempotent, so they are asked again
 * a bounded number of times; every retry is counted in the evidence, and a 503 that
 * persists is the verdict.
 */
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

/** A canonical (UUID v7) key for the API acts of the fixtures: the Ack normaliser
 * refuses a v4 with 400, so the harness mints what the console mints. */
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

async function selectWorkspace(page: Page, name: string) {
  const trigger = page
    .getByRole('button', {
      name: /All workspaces|Billing|Default|Workspace not in list/i,
    })
    .first()
  await trigger.click()
  // The seeded workspace is shown as «Default default · default» (name, slug, kind).
  await page
    .getByRole('menuitem', { name: new RegExp(`^${name}`, 'i') })
    .first()
    .click()
}

/** Restart the engine on the SAME data directory: what was durable survives. */
async function restartEngine() {
  const pidFile = path.join(WORK, 'engine.pid')
  if (engineLifecycleRequired()) await stopObservedEngine(WORK, 'restart')
  else {
    const pid = Number(readFileSync(pidFile, 'utf8').trim())
    process.kill(pid, 'SIGTERM')
    for (let i = 0; i < 100 && existsSync(`/proc/${pid}`); i++)
      await new Promise((r) => setTimeout(r, 100))
  }
  const identity = JSON.parse(
    readFileSync(path.join(WORK, 'identity.json'), 'utf8'),
  ) as { binary: string; data_dir: string; engine_port: number }
  const out = openSync(path.join(WORK, 'engine.log'), 'a')
  const child = spawn(
    identity.binary,
    [
      'serve',
      '--insecure',
      '--listen',
      `127.0.0.1:${identity.engine_port}`,
      '--grpc-listen',
      `127.0.0.1:${identity.engine_port + 1}`,
      '--data-dir',
      identity.data_dir,
    ],
    {
      detached: true,
      stdio: ['ignore', out, out],
      env: engineEnv(WORK),
    },
  )
  child.unref()
  if (
    child.pid === undefined ||
    !Number.isInteger(child.pid) ||
    !Number.isSafeInteger(child.pid) ||
    child.pid <= 0
  ) {
    throw new Error('engine did not start after restart')
  }
  writeFileSync(pidFile, String(child.pid))
  let observationPublished = false
  try {
    await withAwaitedProcessObservation(
      {
        role: 'engine',
        phase: 'restarted',
        pid: child.pid,
        expectedExecutable: identity.binary,
        expectedDataDir: identity.data_dir,
        expectedListen: `127.0.0.1:${identity.engine_port}`,
        expectedGrpcListen: `127.0.0.1:${identity.engine_port + 1}`,
      },
      async (receipt) => {
        observationPublished = receipt !== undefined
        await publishObservedEngine(WORK, receipt)
        for (let i = 0; i < 120; i++) {
          try {
            const r = await fetch(`${BASE}/healthz`)
            if (r.ok) return
          } catch {
            /* not yet */
          }
          await new Promise((r) => setTimeout(r, 500))
        }
        throw new Error('engine did not come back after restart')
      },
    )
  } catch (error) {
    try {
      if (!observationPublished) recordUnobservedSpawn(WORK, child)
    } catch {
      /* keep the original failure */
    }
    throw error
  }
}

test.beforeAll(async ({ request }) => {
  expect(BASE, 'K3_E2E_BASE is set by the global setup').not.toBe('')
  expect(TENANT, 'DEMO_TENANT is set by the global setup').not.toBe('')
  // The demo superadmin PROVISIONS (users, memberships, orgs, audit reads). It is not
  // a directory principal of any workspace, so the K3 routes answer it 503
  // evidence_unavailable by design; every communication act below is made by A, an
  // OWNER created through the real routes, and by B and C, two editors.
  tokenAdmin = (await login(request, DEMO_EMAIL, DEMO_PASSWORD)).token
  const ws = await api(request, tokenAdmin, 'GET', '/v1/workspaces?limit=100')
  expect(ws.status).toBe(200)
  const items = (
    ws.json as { items: { id: string; slug: string; is_default: boolean }[] }
  ).items
  wsBilling = items.find((w) => w.slug === 'billing')?.id ?? ''
  wsDefault = items.find((w) => w.is_default)?.id ?? ''
  expect(wsBilling, 'the seeded Billing workspace').not.toBe('')
  expect(wsDefault, 'the default workspace').not.toBe('')
  userA = await createMember(request, A_EMAIL, 'owner')
  userB = await createMember(request, B_EMAIL, 'editor')
  userC = await createMember(request, C_EMAIL, 'editor')
  tokenA = (await login(request, A_EMAIL, MEMBER_PASSWORD)).token
  tokenB = (await login(request, B_EMAIL, MEMBER_PASSWORD)).token
  tokenC = (await login(request, C_EMAIL, MEMBER_PASSWORD)).token
  // K3 readiness witness for the estate this run booted: the catalog answers A with 200.
  const ready = await apiRead(
    request,
    tokenA,
    `/v1/m/sessions/channels?workspace_id=${wsBilling}&limit=1`,
  )
  expect(ready.status, 'K3 readiness (catalog as A)').toBe(200)
  // Pagination fixture: 26 visible channels for B plus ONE hidden from B.
  for (let i = 1; i <= 26; i++) {
    const r = await api(request, tokenA, 'POST', '/v1/m/sessions/channels', {
      workspace_id: wsBilling,
      slug: `page-${STAMP}-${String(i).padStart(2, '0')}`,
      name: `Page ${i}`,
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
          can_write: false,
          can_admin: false,
        },
      ],
    })
    expect(r.status, `page channel ${i}`).toBe(201)
  }
  const hidden = await api(request, tokenA, 'POST', '/v1/m/sessions/channels', {
    workspace_id: wsBilling,
    slug: `hidden-${STAMP}`,
    name: 'Hidden from B',
    initial_grants: [
      {
        subject: { kind: 'user', ref: userA },
        can_read: true,
        can_write: true,
        can_admin: true,
      },
    ],
  })
  expect(hidden.status).toBe(201)
  evidence.actors = { a: userA, b: userB, c: userC }
  evidence.workspaces = { billing: wsBilling, default: wsDefault }
})

// Required mode is qualified only for the three named I1 cases selected by the
// later candidate grep. This hook observes the worker browser before each test
// body that actually runs — the I1 bodies and the four K3 I3 bodies selected by
// `--grep 'K3 I3:'` alike; it does not cover request-only beforeAll work (neither
// the I1 provisioning nor the I3 Channel/WorkItem provisioning below).
test.beforeEach(async ({ browser }) => {
  await withAwaitedProcessObservation(
    { role: 'browser', phase: 'beforeEach', browser },
    () => undefined,
  )
})

test.afterAll(async ({ browser }) => {
  evidence.unavailable_read_retries = unavailableRetries
  evidence.browser = {
    name: browser.browserType().name(),
    version: browser.version(),
  }
  writeFileSync(
    path.join(evidenceDir(), 'journey.json'),
    JSON.stringify(evidence, null, 2),
  )
})

// ─── A: create → catalog → card → send ───────────────────────────────────────────
test('A creates the channel from the New-channel door with explicit grants', async ({
  page,
  request,
}) => {
  await uiLogin(page, A_EMAIL, MEMBER_PASSWORD)
  await page.goto('/communications/new')
  await expect(page.getByRole('heading', { name: 'New channel' })).toBeVisible()
  await expect(page.getByText('Select a workspace')).toBeVisible()
  await selectWorkspace(page, 'Billing')
  await expect(
    page.getByRole('tab', { name: 'New channel', selected: true }),
  ).toBeVisible()
  await page.getByLabel(/^Slug/).fill(channelSlug)
  await page.getByLabel(/^Name/).fill('K3 journey')
  await page.getByLabel('Description').fill('First real journey')
  // Three explicit grants: A r/w/admin, B read only, C write only.
  await page
    .getByRole('button', { name: 'Add my account as a subject' })
    .click()
  await page.getByRole('button', { name: 'Add grant' }).click()
  await page.getByRole('button', { name: 'Add grant' }).click()
  const rows = page.locator('[data-slot="grant-row"]')
  await expect(rows).toHaveCount(3)
  await expect(
    rows.nth(0).getByRole('textbox', { name: 'Reference (ID)' }),
  ).toHaveValue(userA)
  for (const bit of ['Read', 'Write', 'Admin'])
    await rows.nth(0).getByRole('checkbox', { name: bit }).check()
  await rows.nth(1).getByRole('textbox', { name: 'Reference (ID)' }).fill(userB)
  await rows.nth(1).getByRole('checkbox', { name: 'Read' }).check()
  await rows.nth(2).getByRole('textbox', { name: 'Reference (ID)' }).fill(userC)
  await rows.nth(2).getByRole('checkbox', { name: 'Write' }).check()
  await shot(page, '01-create-form-explicit-grants')
  const createResponse = page.waitForResponse(
    (r) =>
      r.url().endsWith('/v1/m/sessions/channels') &&
      r.request().method() === 'POST',
  )
  await page.getByRole('button', { name: 'Create channel' }).click()
  const created = await createResponse
  expect(created.status()).toBe(201)
  await expect(page.getByText('Channel created')).toBeVisible()
  const receipt = (await created.json()) as {
    channel: { id: string; slug: string }
    grants: { id: string; subject: { ref: string } }[]
    etag: string
  }
  channelId = receipt.channel.id
  grantIdB = receipt.grants.find((g) => g.subject.ref === userB)?.id ?? ''
  expect(grantIdB).not.toBe('')
  await expect(page.locator('[data-slot="channel-created"]')).toContainText(
    channelId,
  )
  await expect(page.locator('[data-slot="grant-recorded"]')).toHaveCount(3)
  await shot(page, '02-create-receipt')
  // Durable effect, read over HTTP with A's own credential.
  const fresh = await apiRead(
    request,
    tokenA,
    `/v1/m/sessions/channels/${channelId}`,
  )
  expect(fresh.status).toBe(200)
  expect((fresh.json as { slug: string }).slug).toBe(channelSlug)
  expect(fresh.headers.etag).toBe('"v1"')
  evidence.channel = {
    id: channelId,
    slug: channelSlug,
    etag: fresh.headers.etag,
  }
})

test('A opens the catalog row, reads the card fresh with its ETag, and sends a typed notice to B', async ({
  page,
  request,
}) => {
  await uiLogin(page, A_EMAIL, MEMBER_PASSWORD)
  await page.goto('/communications')
  await expect(
    page.getByRole('heading', { name: 'Communications' }),
  ).toBeVisible()
  await selectWorkspace(page, 'Billing')
  await page
    .getByRole('searchbox')
    .or(page.getByPlaceholder(/Search/))
    .first()
    .fill(channelSlug)
  const row = page.getByRole('row').filter({ hasText: channelSlug })
  await expect(row).toHaveCount(1)
  await expect(row).toContainText('read')
  await expect(row).toContainText('write')
  await expect(row).toContainText('admin')
  const read = page.waitForResponse((r) =>
    r.url().endsWith(`/v1/m/sessions/channels/${channelId}`),
  )
  await row.click()
  expect((await read).status()).toBe(200)
  const sheet = page.getByRole('dialog')
  await expect(sheet.getByText('"v1"')).toBeVisible()
  await expect(sheet).toContainText('K3 journey')
  await shot(page, '03-channel-card-fresh-etag')
  await sheet.getByRole('button', { name: 'Send notice' }).click()
  const dialog = page
    .getByRole('dialog')
    .filter({ hasText: 'Send a direct notice' })
  await dialog.getByRole('textbox', { name: 'Reference (ID)' }).fill(userB)
  await dialog.getByLabel(/^Subject/).fill(`Deploy window ${STAMP}`)
  await dialog.getByLabel('Text').fill('Freeze at 18:00. <b>not html</b>')
  await dialog.getByRole('button', { name: 'Compose' }).click()
  await expect(dialog.getByText('Confirm the intention')).toBeVisible()
  const key =
    (
      await dialog
        .locator('dd')
        .filter({ hasText: /^[0-9a-f-]{36}$/ })
        .first()
        .textContent()
    )?.trim() ?? ''
  expect(key).toMatch(/^[0-9a-f-]{36}$/)
  await shot(page, '04-compose-confirm-immutable-intent')
  const sendResponse = page.waitForResponse((r) =>
    r.url().endsWith('/v1/m/sessions/messages/send'),
  )
  await dialog.getByRole('button', { name: 'Confirm and send' }).click()
  const sent = await sendResponse
  expect(sent.status()).toBe(201)
  expect(sent.request().headers()['idempotency-key']).toBe(key)
  expect(sent.request().headers()['if-plan-hash']).toBeUndefined()
  const body = (await sent.json()) as {
    message_id: string
    delivery_id: string
    replayed: boolean
  }
  messageId1 = body.message_id
  deliveryId1 = body.delivery_id
  expect(body.replayed).toBe(false)
  await expect(dialog.getByText('Notice published').first()).toBeVisible()
  await shot(page, '05-send-receipt-applied')
  // Causal control over HTTP: B's mailbox now carries the delivery; A's does not.
  const inboxB = await apiRead(
    request,
    tokenB,
    `/v1/m/sessions/inbox?workspace_id=${wsBilling}&limit=50`,
  )
  expect(inboxB.status).toBe(200)
  const itemsB = (inboxB.json as { items: { delivery: { id: string } }[] })
    .items
  expect(itemsB.map((i) => i.delivery.id)).toContain(deliveryId1)
  const inboxA = await apiRead(
    request,
    tokenA,
    `/v1/m/sessions/inbox?workspace_id=${wsBilling}&limit=50`,
  )
  expect(
    (inboxA.json as { items: { delivery: { id: string } }[] }).items.map(
      (i) => i.delivery.id,
    ),
  ).not.toContain(deliveryId1)
  evidence.notice1 = { key, message_id: messageId1, delivery_id: deliveryId1 }
})

// ─── B: inbox → delivery → message → Ack → re-read (restart, reload) ─────────────
test('B opens the inbox, the delivery and the message, acknowledges with the read version, and re-reads after an engine restart and a reload', async ({
  browser,
  request,
}) => {
  const ctx = await browser.newContext()
  const page = await ctx.newPage()
  await uiLogin(page, B_EMAIL, MEMBER_PASSWORD)
  await page.goto('/communications/inbox')
  await expect(
    page.getByRole('heading', { name: 'Communications inbox' }),
  ).toBeVisible()
  await selectWorkspace(page, 'Billing')
  const row = page
    .getByRole('row')
    .filter({ hasText: `Deploy window ${STAMP}` })
  await expect(row).toHaveCount(1)
  await expect(page.locator('body')).not.toContainText(/unread/i)
  await shot(page, '06-b-inbox')
  const readDelivery = page.waitForResponse((r) =>
    r.url().endsWith(`/v1/m/sessions/deliveries/${deliveryId1}`),
  )
  await row.click()
  expect((await readDelivery).status()).toBe(200)
  const sheet = page.getByRole('dialog')
  await expect(
    sheet.getByText('Freeze at 18:00. <b>not html</b>'),
  ).toBeVisible()
  expect(await sheet.locator('b').count()).toBe(0)
  await expect(sheet.getByText('v1')).toBeVisible()
  await shot(page, '07-b-delivery-sheet')
  // The message is ITS OWN read under message:read.
  const readMessage = page.waitForResponse((r) =>
    r.url().endsWith(`/v1/m/sessions/messages/${messageId1}`),
  )
  await sheet.getByRole('button', { name: 'Open message' }).click()
  expect((await readMessage).status()).toBe(200)
  const msg = page.getByRole('dialog').filter({
    has: page.getByRole('heading', { name: 'Message', exact: true }),
  })
  await expect(msg).toContainText(messageId1)
  await shot(page, '08-b-message-sheet')
  await page.keyboard.press('Escape')
  await expect(msg).toBeHidden()
  // Explicit Ack with If-Match "v1" and a key of its own.
  await sheet.getByRole('button', { name: 'Acknowledge' }).click()
  await expect(sheet.getByText('"v1"').first()).toBeVisible()
  await shot(page, '09-b-ack-confirm')
  const ackResponse = page.waitForResponse((r) =>
    r.url().endsWith(`/v1/m/sessions/deliveries/${deliveryId1}/ack`),
  )
  await sheet.getByRole('button', { name: 'Confirm acknowledgement' }).click()
  const acked = await ackResponse
  expect(acked.status()).toBe(200)
  expect(acked.request().headers()['if-match']).toBe('"v1"')
  expect(acked.request().headers()['idempotency-key']).toMatch(
    /^[0-9a-f-]{36}$/,
  )
  const ackBody = (await acked.json()) as {
    late: boolean
    replayed: boolean
    version: number
    fulfillment: { state: string }
  }
  expect(ackBody.replayed).toBe(false)
  expect(ackBody.late).toBe(false)
  await expect(sheet.locator('[data-slot="ack-receipt"]')).toBeVisible()
  await expect(sheet.locator('[data-slot="ack-late"]')).toHaveCount(0)
  // The durable row, re-read by the sheet: acknowledged, version advanced.
  await expect(sheet.locator('[data-slot="acknowledged-at"]')).not.toHaveText(
    '—',
  )
  await expect(sheet.getByText(`v${ackBody.version}`).first()).toBeVisible()
  await shot(page, '10-b-ack-receipt-and-reread')
  const viaHttp = await apiRead(
    request,
    tokenB,
    `/v1/m/sessions/deliveries/${deliveryId1}`,
  )
  expect(viaHttp.status).toBe(200)
  const d = viaHttp.json as {
    delivery: { version: number; acknowledged_at: string | null }
    fulfillment: { state: string; acknowledged: number }
  }
  expect(d.delivery.acknowledged_at).not.toBeNull()
  expect(d.delivery.version).toBe(ackBody.version)
  evidence.ack1 = { version: d.delivery.version, fulfillment: d.fulfillment }
  await page.keyboard.press('Escape')
  await expect(sheet).toBeHidden()

  // Restart the engine on the same data directory and read the result again, by reload
  // with the delivery deep link — a fresh read, not a cache.
  await restartEngine()
  await page.goto(`/communications/inbox?delivery=${deliveryId1}`)
  const reread = page.getByRole('dialog')
  await expect(reread.locator('[data-slot="acknowledged-at"]')).not.toHaveText(
    '—',
  )
  await expect(reread.getByText(`v${ackBody.version}`).first()).toBeVisible()
  await shot(page, '11-b-after-restart-and-reload')
  const afterRestart = await apiRead(
    request,
    tokenB,
    `/v1/m/sessions/deliveries/${deliveryId1}`,
  )
  expect(afterRestart.status).toBe(200)
  expect(
    (afterRestart.json as { delivery: { acknowledged_at: string | null } })
      .delivery.acknowledged_at,
  ).not.toBeNull()
  await ctx.close()
})

// ─── stale CAS: the version read is no longer current ────────────────────────────
test('a stale Ack is refused as a conflict (409 on this engine; the spec also lists 412): nothing is re-sent, re-reading shows the acknowledged row', async ({
  browser,
  request,
}) => {
  const sent = await api(
    request,
    tokenA,
    'POST',
    '/v1/m/sessions/messages/send',
    {
      channel_id: channelId,
      recipient: { kind: 'user', ref: userB },
      content: {
        subject: `Second notice ${STAMP}`,
        blocks: [{ type: 'text', format: 'plain', text: 'second' }],
      },
    },
    { 'Idempotency-Key': uuidv7() },
  )
  expect(sent.status).toBe(201)
  deliveryId2 = (sent.json as { delivery_id: string }).delivery_id
  const ctx = await browser.newContext()
  const page = await ctx.newPage()
  await uiLogin(page, B_EMAIL, MEMBER_PASSWORD)
  await selectWorkspaceViaStore(page)
  await page.goto(`/communications/inbox?delivery=${deliveryId2}`)
  const sheet = page.getByRole('dialog')
  await expect(sheet.getByText('v1')).toBeVisible()
  // Meanwhile the same recipient acknowledges over the API: the row moves to v2.
  const apiAck = await api(
    request,
    tokenB,
    'POST',
    `/v1/m/sessions/deliveries/${deliveryId2}/ack`,
    undefined,
    {
      'If-Match': '"v1"',
      'Idempotency-Key': uuidv7(),
    },
  )
  expect(apiAck.status).toBe(200)
  const ackRequests: string[] = []
  page.on('request', (r) => {
    if (r.url().endsWith(`/v1/m/sessions/deliveries/${deliveryId2}/ack`))
      ackRequests.push(r.headers()['if-match'] ?? '')
  })
  await sheet.getByRole('button', { name: 'Acknowledge' }).click()
  const stale = page.waitForResponse((r) =>
    r.url().endsWith(`/v1/m/sessions/deliveries/${deliveryId2}/ack`),
  )
  await sheet.getByRole('button', { name: 'Confirm acknowledgement' }).click()
  // MEASURED (run 12, 2026-09-06): the served engine answers a stale If-Match on the
  // Ack with 409 {"error":{"message":"conflict"}} — the Ack's version_mismatch wraps
  // store.ErrConflict (modules/sessions/communication_ack_service.go:39) and only the
  // inbox cursor's mismatch reaches 412. Both statuses are in the served contract for
  // this operation; the console reacts to both the same way. Recorded for root.
  const staleResponse = await stale
  const staleBody = await staleResponse.text()
  expect([409, 412]).toContain(staleResponse.status())
  await expect(sheet.locator('[data-slot="ack-conflict"]')).toBeVisible()
  await expect(
    sheet.locator(
      '[data-failure-kind="conflict"], [data-failure-kind="version_mismatch"]',
    ),
  ).toBeVisible()
  await shot(page, '12-b-stale-cas-412')
  await sheet.getByRole('button', { name: 'Re-read' }).click()
  await expect(sheet.locator('[data-slot="acknowledged-at"]')).not.toHaveText(
    '—',
  )
  await expect(sheet.locator('[data-slot="ack-conflict"]')).toHaveCount(0)
  await page.waitForTimeout(500)
  expect(ackRequests).toEqual(['"v1"'])
  const viaHttp = await apiRead(
    request,
    tokenB,
    `/v1/m/sessions/deliveries/${deliveryId2}`,
  )
  expect(
    (viaHttp.json as { delivery: { version: number } }).delivery.version,
  ).toBe(2)
  evidence.staleCas = {
    delivery_id: deliveryId2,
    browser_ack_requests: ackRequests,
    stale_ack_status: staleResponse.status(),
    stale_ack_body: staleBody,
  }
  await ctx.close()
})

/** The workspace selection persists in localStorage under the switcher's own key; a
 * fresh browser context has none, and the switcher is the only way to set it. This
 * uses the switcher on the inbox door before navigating with the deep link. */
async function selectWorkspaceViaStore(page: Page) {
  await page.goto('/communications/inbox')
  await selectWorkspace(page, 'Billing')
  await expect(page.getByRole('tab', { name: 'Inbox' })).toBeVisible()
}

// ─── ambiguous transport result: same key, replayed receipt, ONE message ─────────
test('an ambiguous send is retried with the same key and the engine replays it: exactly one message', async ({
  page,
  request,
}) => {
  await uiLogin(page, A_EMAIL, MEMBER_PASSWORD)
  await page.goto('/communications')
  await selectWorkspace(page, 'Billing')
  await page.goto(`/communications?channel=${channelId}`)
  const sheet = page.getByRole('dialog')
  await sheet.getByRole('button', { name: 'Send notice' }).click()
  const dialog = page
    .getByRole('dialog')
    .filter({ hasText: 'Send a direct notice' })
  await dialog.getByRole('textbox', { name: 'Reference (ID)' }).fill(userB)
  await dialog.getByLabel(/^Subject/).fill(`Third notice ${STAMP}`)
  await dialog.getByLabel('Text').fill('ambiguous transport')
  await dialog.getByRole('button', { name: 'Compose' }).click()
  const key =
    (
      await dialog
        .locator('dd')
        .filter({ hasText: /^[0-9a-f-]{36}$/ })
        .first()
        .textContent()
    )?.trim() ?? ''
  // The engine RECEIVES and APPLIES the send; the browser never sees the answer.
  let dropped = 0
  await page.route('**/v1/m/sessions/messages/send', async (route) => {
    if (dropped === 0) {
      dropped++
      await route.fetch()
      await route.abort('failed')
      return
    }
    await route.continue()
  })
  await dialog.getByRole('button', { name: 'Confirm and send' }).click()
  await expect(dialog.getByText('Outcome unknown').first()).toBeVisible()
  await shot(page, '13-a-ambiguous-outcome')
  const replay = page.waitForResponse((r) =>
    r.url().endsWith('/v1/m/sessions/messages/send'),
  )
  await dialog.getByRole('button', { name: 'Retry with the same key' }).click()
  const replayed = await replay
  expect(replayed.status()).toBe(200)
  expect(replayed.request().headers()['idempotency-key']).toBe(key)
  expect(((await replayed.json()) as { replayed: boolean }).replayed).toBe(true)
  await expect(dialog.getByText('Already published').first()).toBeVisible()
  await shot(page, '14-a-retry-same-key-replayed')
  await page.unroute('**/v1/m/sessions/messages/send')
  const inboxB = await apiRead(
    request,
    tokenB,
    `/v1/m/sessions/inbox?workspace_id=${wsBilling}&limit=200`,
  )
  const third = (
    inboxB.json as { items: { message: { content: { subject: string } } }[] }
  ).items.filter((i) => i.message.content.subject === `Third notice ${STAMP}`)
  expect(third).toHaveLength(1)
  evidence.ambiguous = { key, dropped, third_notice_rows: third.length }
})

// ─── refused send: zero effect ───────────────────────────────────────────────────
test('a notice to a recipient without a local read grant is refused and leaves no message, delivery or publish audit', async ({
  page,
  request,
}) => {
  const head = await api(request, tokenAdmin, 'GET', '/v1/audit?limit=1')
  auditHeadBeforeRefusal = (head.json as { head_seq: number }).head_seq
  const inboxCBefore = await apiRead(
    request,
    tokenC,
    `/v1/m/sessions/inbox?workspace_id=${wsBilling}&limit=50`,
  )
  expect(inboxCBefore.status).toBe(200)
  await uiLogin(page, A_EMAIL, MEMBER_PASSWORD)
  await page.goto('/communications')
  await selectWorkspace(page, 'Billing')
  await page.goto(`/communications?channel=${channelId}`)
  await page
    .getByRole('dialog')
    .getByRole('button', { name: 'Send notice' })
    .click()
  const dialog = page
    .getByRole('dialog')
    .filter({ hasText: 'Send a direct notice' })
  await dialog.getByRole('textbox', { name: 'Reference (ID)' }).fill(userC)
  await dialog.getByLabel(/^Subject/).fill(`Refused ${STAMP}`)
  await dialog.getByLabel('Text').fill('to a subject without read')
  await dialog.getByRole('button', { name: 'Compose' }).click()
  const refused = page.waitForResponse((r) =>
    r.url().endsWith('/v1/m/sessions/messages/send'),
  )
  await dialog.getByRole('button', { name: 'Confirm and send' }).click()
  const status = (await refused).status()
  expect([403, 404]).toContain(status)
  await expect(dialog.getByText('Send refused')).toBeVisible()
  await shot(page, '15-a-send-refused')
  const inboxCAfter = await apiRead(
    request,
    tokenC,
    `/v1/m/sessions/inbox?workspace_id=${wsBilling}&limit=50`,
  )
  expect((inboxCAfter.json as { items: unknown[] }).items).toHaveLength(
    (inboxCBefore.json as { items: unknown[] }).items.length,
  )
  const after = await api(
    request,
    tokenAdmin,
    'GET',
    `/v1/audit?from=${auditHeadBeforeRefusal + 1}&limit=200`,
  )
  const events = (
    (after.json as { items?: { action: string }[] }).items ?? []
  ).map((e) => e.action)
  expect(
    events.filter((a) => a === 'sessions.communication.message.publish'),
  ).toHaveLength(0)
  evidence.refused = { status, audit_events_after: events }
})

// ─── delivery-only B and sender-without-read C ───────────────────────────────────
test('B (read only) sees the channel without a write bit and cannot compose; C (write only) cannot read the channel at all', async ({
  browser,
}) => {
  const b = await (await browser.newContext()).newPage()
  await uiLogin(b, B_EMAIL, MEMBER_PASSWORD)
  await b.goto('/communications')
  await selectWorkspace(b, 'Billing')
  // Pagination first, while B still holds its read grant: 26 page channels + the
  // journey channel are visible, the hidden one never is, order and has_more hold.
  await b.getByRole('combobox', { name: 'Page size' }).click()
  await b.getByRole('option', { name: '25' }).click()
  await expect(
    b.getByText('More channels exist beyond this page'),
  ).toBeVisible()
  await expect(b.getByRole('row')).toHaveCount(26) // 25 rows + header
  await b.getByRole('button', { name: 'Load more' }).click()
  await expect(b.getByRole('row')).toHaveCount(28)
  await expect(b.getByText('More channels exist beyond this page')).toBeHidden()
  await expect(b.getByText(`hidden-${STAMP}`)).toHaveCount(0)
  await shot(b, '16-b-catalog-pagination-no-hidden')
  await b
    .getByRole('searchbox')
    .or(b.getByPlaceholder(/Search/i))
    .or(b.getByLabel(/^Search/i))
    .first()
    .fill(channelSlug)
  const row = b.getByRole('row').filter({ hasText: channelSlug })
  await expect(row).toContainText('read')
  await expect(row).not.toContainText('write')
  await row.click()
  const sheet = b.getByRole('dialog')
  await expect(sheet.getByText(/reports no local write bit/)).toBeVisible()
  await expect(sheet.getByRole('button', { name: 'Send notice' })).toHaveCount(
    0,
  )
  await shot(b, '17-b-delivery-only-no-compose')
  await b.context().close()

  const c = await (await browser.newContext()).newPage()
  await uiLogin(c, C_EMAIL, MEMBER_PASSWORD)
  await c.goto('/communications')
  await selectWorkspace(c, 'Billing')
  await expect(c.getByText(channelSlug)).toHaveCount(0)
  await c.getByLabel('Open a channel by ID').fill(channelId)
  const concealed = c.waitForResponse((r) =>
    r.url().endsWith(`/v1/m/sessions/channels/${channelId}`),
  )
  await c.getByRole('button', { name: 'Open' }).click()
  expect([403, 404]).toContain((await concealed).status())
  await expect(
    c.getByRole('dialog').locator('[data-slot="failure-notice"]'),
  ).toBeVisible()
  await expect(
    c.getByRole('dialog').getByRole('button', { name: 'Send notice' }),
  ).toHaveCount(0)
  await shot(c, '18-c-write-only-cannot-read')
  await c.context().close()
})

// ─── workspace and tenant changes ────────────────────────────────────────────────
test('changing the workspace or the tenant ends the previous scope: no rows survive, and no K3 request is made without a workspace', async ({
  browser,
  request,
}) => {
  const b = await (await browser.newContext()).newPage()
  await uiLogin(b, B_EMAIL, MEMBER_PASSWORD)
  await b.goto('/communications/inbox')
  await selectWorkspace(b, 'Billing')
  await expect(
    b.getByRole('row').filter({ hasText: `Deploy window ${STAMP}` }),
  ).toHaveCount(1)
  const k3Requests: string[] = []
  b.on('request', (r) => {
    if (
      r.url().includes('/v1/m/sessions/inbox') ||
      r.url().includes('/v1/m/sessions/channels')
    )
      k3Requests.push(r.url())
  })
  await selectWorkspace(b, 'Default')
  await expect(
    b.getByRole('row').filter({ hasText: `Deploy window ${STAMP}` }),
  ).toHaveCount(0)
  await expect(b.getByText('No deliveries')).toBeVisible()
  expect(k3Requests.every((u) => u.includes(`workspace_id=${wsDefault}`))).toBe(
    true,
  )
  await shot(b, '19-b-workspace-default-empty')
  await selectWorkspace(b, 'Billing')
  await expect(
    b.getByRole('row').filter({ hasText: `Deploy window ${STAMP}` }),
  ).toHaveCount(1)
  await b.context().close()

  // A second tenant for A: switching to it drops the workspace and makes NO K3 request.
  const org = await api(request, tokenAdmin, 'POST', '/v1/system/orgs', {
    name: `K3 other ${STAMP}`,
    slug: `k3-other-${STAMP}`,
  })
  expect(org.status).toBe(201)
  const otherTenant = (org.json as { tenant_id: string }).tenant_id
  const member = await api(request, tokenAdmin, 'POST', '/v1/memberships', {
    user_id: userA,
    tenant: otherTenant,
    role: 'owner',
  })
  expect(member.status).toBe(201)
  const a = await (await browser.newContext()).newPage()
  await uiLogin(a, A_EMAIL, MEMBER_PASSWORD)
  await a.goto('/communications')
  await selectWorkspace(a, 'Billing')
  await expect(a.getByRole('row').filter({ hasText: channelSlug })).toHaveCount(
    1,
  )
  const otherRequests: string[] = []
  a.on('request', (r) => {
    if (
      r.url().includes('/v1/m/sessions/') &&
      (r.headers()['x-olivares-tenant'] ?? '') === otherTenant
    )
      otherRequests.push(r.url())
  })
  // A member's tenant switcher labels each tenant by its short id and the role held
  // there (tenant-switcher.tsx shortId): the trigger shows the active one.
  await a
    .getByRole('button', { name: new RegExp(TENANT.slice(0, 8)) })
    .first()
    .click()
  // Two tenants minted in the same run can share the same 8-character prefix (UUID
  // v7 is time-ordered; measured in run 16), so the label alone is ambiguous: the
  // active tenant is the one carrying the check icon, the target is the other.
  const candidates = a.getByRole('menuitem', {
    name: new RegExp(otherTenant.slice(0, 8)),
  })
  await expect(candidates.first()).toBeVisible()
  let switched = false
  for (let i = 0; i < (await candidates.count()); i++) {
    const item = candidates.nth(i)
    if ((await item.locator('svg').count()) === 0) {
      await item.click()
      switched = true
      break
    }
  }
  expect(switched, 'a non-active tenant entry was offered').toBe(true)
  await expect(a.getByText('Select a workspace')).toBeVisible()
  await expect(a.getByRole('row').filter({ hasText: channelSlug })).toHaveCount(
    0,
  )
  await a.waitForTimeout(500)
  expect(otherRequests).toEqual([])
  await shot(a, '20-a-tenant-switch-no-workspace')
  evidence.tenantSwitch = {
    other_tenant: otherTenant,
    k3_requests_under_other_tenant: otherRequests.length,
  }
  await a.context().close()
})

// ─── revocation ──────────────────────────────────────────────────────────────────
test("after A revokes B's read grant, B's catalog and inbox no longer show the channel and an open delivery is replaced on re-read", async ({
  browser,
  request,
}) => {
  const b = await (await browser.newContext()).newPage()
  await uiLogin(b, B_EMAIL, MEMBER_PASSWORD)
  await b.goto('/communications/inbox')
  await selectWorkspace(b, 'Billing')
  await b.goto(`/communications/inbox?delivery=${deliveryId1}`)
  const sheet = b.getByRole('dialog')
  await expect(sheet.getByText(`Deploy window ${STAMP}`)).toBeVisible()
  const channel = await apiRead(
    request,
    tokenA,
    `/v1/m/sessions/channels/${channelId}`,
  )
  const revoked = await api(
    request,
    tokenA,
    'POST',
    `/v1/m/sessions/channels/${channelId}/grants/${grantIdB}/revoke`,
    {},
    { 'If-Match': channel.headers.etag },
  )
  expect(revoked.status).toBe(200)
  const reread = b.waitForResponse((r) =>
    r.url().endsWith(`/v1/m/sessions/deliveries/${deliveryId1}`),
  )
  await sheet.getByRole('button', { name: 'Re-read' }).click()
  const status = (await reread).status()
  expect([403, 404]).toContain(status)
  await expect(sheet.getByText(`Deploy window ${STAMP}`)).toHaveCount(0)
  await expect(
    sheet.locator('[data-slot="failure-notice"], [role="status"]').first(),
  ).toBeVisible()
  await shot(b, '21-b-revoked-delivery-replaced')
  await b.keyboard.press('Escape')
  await b.getByRole('button', { name: 'Refresh' }).click()
  await expect(
    b.getByRole('row').filter({ hasText: `Deploy window ${STAMP}` }),
  ).toHaveCount(0)
  await b.goto('/communications')
  await expect(b.getByRole('heading', { name: 'Communications' })).toBeVisible()
  await b
    .getByRole('searchbox')
    .or(b.getByPlaceholder(/Search/))
    .first()
    .fill(channelSlug)
  await expect(b.getByRole('row').filter({ hasText: channelSlug })).toHaveCount(
    0,
  )
  await shot(b, '22-b-revoked-catalog-without-channel')
  const inboxB = await apiRead(
    request,
    tokenB,
    `/v1/m/sessions/inbox?workspace_id=${wsBilling}&limit=200`,
  )
  expect(
    (inboxB.json as { items: { delivery: { id: string } }[] }).items.map(
      (i) => i.delivery.id,
    ),
  ).not.toContain(deliveryId1)
  evidence.revocation = { grant_id: grantIdB, reread_status: status }
  await b.context().close()
})

// ─── keyboard, mobile, light/dark, en/es ─────────────────────────────────────────
test('keyboard-only Ack, mobile viewport, light and dark themes, English and Spanish', async ({
  browser,
  request,
}) => {
  // A fresh grant for B (A administers) and a fifth notice, so the keyboard Ack is real.
  const channel = await apiRead(
    request,
    tokenA,
    `/v1/m/sessions/channels/${channelId}`,
  )
  const regrant = await api(
    request,
    tokenA,
    'POST',
    `/v1/m/sessions/channels/${channelId}/grants`,
    {
      subject: { kind: 'user', ref: userB },
      can_read: true,
      can_write: false,
      can_admin: false,
    },
    { 'If-Match': channel.headers.etag },
  )
  expect(regrant.status).toBe(200)
  const sent = await api(
    request,
    tokenA,
    'POST',
    '/v1/m/sessions/messages/send',
    {
      channel_id: channelId,
      recipient: { kind: 'user', ref: userB },
      content: {
        subject: `Keyboard notice ${STAMP}`,
        blocks: [{ type: 'text', format: 'plain', text: 'ack me by keyboard' }],
      },
    },
    { 'Idempotency-Key': uuidv7() },
  )
  expect(sent.status).toBe(201)
  const deliveryId5 = (sent.json as { delivery_id: string }).delivery_id

  const b = await (await browser.newContext()).newPage()
  await uiLogin(b, B_EMAIL, MEMBER_PASSWORD)
  await b.goto('/communications/inbox')
  await selectWorkspace(b, 'Billing')
  await b.goto(`/communications/inbox?delivery=${deliveryId5}`)
  const sheet = b.getByRole('dialog')
  await expect(sheet.getByText(`Keyboard notice ${STAMP}`)).toBeVisible()
  const focusUntil = async (name: string) => {
    for (let i = 0; i < 60; i++) {
      const text = await b.evaluate(
        () =>
          (document.activeElement as HTMLElement | null)?.textContent?.trim() ??
          '',
      )
      if (text === name) return
      await b.keyboard.press('Tab')
    }
    throw new Error(`could not reach "${name}" by Tab`)
  }
  await focusUntil('Acknowledge')
  await b.keyboard.press('Enter')
  await expect(sheet.getByText('Acknowledge this delivery')).toBeVisible()
  await focusUntil('Confirm acknowledgement')
  const ack = b.waitForResponse((r) =>
    r.url().endsWith(`/v1/m/sessions/deliveries/${deliveryId5}/ack`),
  )
  await b.keyboard.press('Enter')
  expect((await ack).status()).toBe(200)
  await expect(sheet.locator('[data-slot="ack-receipt"]')).toBeVisible()
  await shot(b, '23-b-keyboard-ack')
  await b.keyboard.press('Escape')
  await expect(sheet).toBeHidden()

  // Dark and light, through the product's own toggle.
  await b.getByRole('button', { name: 'Toggle theme' }).click()
  await b.getByRole('menuitem', { name: 'Dark' }).click()
  await expect(b.locator('html')).toHaveClass(/dark/)
  await shot(b, '24-b-inbox-dark')
  await b.getByRole('button', { name: 'Toggle theme' }).click()
  await b.getByRole('menuitem', { name: 'Light' }).click()
  await expect(b.locator('html')).not.toHaveClass(/dark/)
  await shot(b, '25-b-inbox-light')

  // Spanish, through the language the console persists.
  await b.evaluate(() => localStorage.setItem('olivares.lang', 'es'))
  await b.reload()
  await expect(
    b.getByRole('heading', { name: 'Bandeja de comunicaciones' }),
  ).toBeVisible()
  await expect(b.getByRole('tab', { name: 'Bandeja' })).toBeVisible()
  await shot(b, '26-b-inbox-es')
  await b.evaluate(() => localStorage.setItem('olivares.lang', 'en'))
  await b.context().close()

  // Mobile.
  const m = await (
    await browser.newContext({
      viewport: { width: 390, height: 844 },
      isMobile: true,
      hasTouch: true,
    })
  ).newPage()
  await uiLogin(m, B_EMAIL, MEMBER_PASSWORD)
  await m.goto('/communications/inbox')
  await selectWorkspace(m, 'Billing')
  await expect(
    m.getByRole('heading', { name: 'Communications inbox' }),
  ).toBeVisible()
  await shot(m, '27-b-inbox-mobile')
  await m.goto(`/communications/inbox?delivery=${deliveryId5}`)
  await expect(
    m.getByRole('dialog').getByText(`Keyboard notice ${STAMP}`),
  ).toBeVisible()
  await shot(m, '28-b-delivery-mobile')
  await m.context().close()
  evidence.keyboardMobileThemes = { delivery_id: deliveryId5 }
})

// ─── documentation captures of the three doors, on the real estate ───────────────
test('captures of the three doors in light and dark at the docs viewport, with real data', async ({
  browser,
}) => {
  const shotDoc = async (page: Page, id: string, theme: 'light' | 'dark') => {
    await page.getByRole('button', { name: 'Toggle theme' }).click()
    await page
      .getByRole('menuitem', { name: theme === 'dark' ? 'Dark' : 'Light' })
      .click()
    if (theme === 'dark') await expect(page.locator('html')).toHaveClass(/dark/)
    else await expect(page.locator('html')).not.toHaveClass(/dark/)
    const file = path.join(evidenceDir(), `${id}-${theme}.png`)
    await page.screenshot({ path: file, fullPage: false })
    const pub = path.join('playwright-report', 'k3')
    mkdirSync(pub, { recursive: true })
    copyFileSync(file, path.join(pub, `${id}-${theme}.png`))
  }
  const docs = { viewport: { width: 1440, height: 1000 }, deviceScaleFactor: 2 }
  const a = await (await browser.newContext(docs)).newPage()
  await uiLogin(a, A_EMAIL, MEMBER_PASSWORD)
  await a.goto('/communications')
  await selectWorkspace(a, 'Billing')
  await expect(a.getByRole('heading', { name: 'Communications' })).toBeVisible()
  await expect(a.getByRole('row').filter({ hasText: channelSlug })).toHaveCount(
    1,
  )
  await shotDoc(a, 'communications', 'light')
  await shotDoc(a, 'communications', 'dark')
  await a.goto('/communications/new')
  await expect(a.getByRole('heading', { name: 'New channel' })).toBeVisible()
  await expect(a.getByRole('button', { name: 'Create channel' })).toBeVisible()
  await shotDoc(a, 'communications-new', 'dark')
  await shotDoc(a, 'communications-new', 'light')
  await a.context().close()
  const b = await (await browser.newContext(docs)).newPage()
  await uiLogin(b, B_EMAIL, MEMBER_PASSWORD)
  await b.goto('/communications/inbox')
  await selectWorkspace(b, 'Billing')
  await expect(
    b.getByRole('heading', { name: 'Communications inbox' }),
  ).toBeVisible()
  await expect(
    b.getByRole('row').filter({ hasText: `Keyboard notice ${STAMP}` }),
  ).toHaveCount(1)
  await shotDoc(b, 'communications-inbox', 'light')
  await shotDoc(b, 'communications-inbox', 'dark')
  await b.context().close()
  evidence.docsCaptures = [
    'communications',
    'communications-inbox',
    'communications-new',
  ].flatMap((id) => [`${id}-light.png`, `${id}-dark.png`])
})

// ═══════════════════════════════════════════════════════════════════════════════
// K3 I3 — THE WORK HANDOFF THROUGH THE REAL CONSOLE (I3-BROWSER-CONSTRUCTION-1).
//
// Four finite tests, selected with `--grep 'K3 I3:'`. They provision their own
// Channel (A read/write/admin, B read/write) and three human-owned, never-leased
// WorkItems through the public Work Interface in a second request-only beforeAll,
// so no I3 test depends on an unselected I1 test. A offers through the actual
// WorkItem detail and offer dialog; B discovers the carrier on its own Handoffs
// door and responds through the actual sheet and dialog. Every domain fact is
// re-read over authenticated HTTP as an independent witness, and every separately
// created BrowserContext closes in `finally`. The only `page.route` of this section
// (test 3) forwards ONE matching offer to the real engine and drops only its
// response; ordinary offers and every response go to the engine unintercepted.
// Tokens, keys and request bodies stay in this process; the evidence file records
// identities, ETags, states, epochs, fences and safe network summaries only.
// ═══════════════════════════════════════════════════════════════════════════════

const I3_WORK_ITEMS = '/v1/m/sessions/work-items'
const I3_HANDOFFS = '/v1/m/sessions/handoffs'
const I3_HANDOFF_INBOX = '/v1/m/sessions/inbox/handoffs'
const I3_DELIVERIES = '/v1/m/sessions/deliveries'
const I3_RESPONSES_PATH =
  /^\/v1\/m\/sessions\/handoffs\/[0-9a-f-]{36}\/responses$/
const I3_CHANNEL_SLUG = `k3-i3-${STAMP}`
const UUID_ANY =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/
/** Canonical UUID v7: version nibble 7, RFC 4122 variant. What the console mints. */
const UUID_V7 =
  /^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/
const I3_LOCALES = ['en', 'es', 'de', 'fr', 'ja', 'ru', 'zh'] as const
type I3Locale = (typeof I3_LOCALES)[number]
const I3_STATE_LABEL = {
  offered: 'Offered',
  accepted: 'Accepted',
  rejected: 'Rejected',
} as const
type I3ListState = keyof typeof I3_STATE_LABEL
const I3_HERE = path.dirname(fileURLToPath(import.meta.url))

/** The public lease projection (work_model.go WorkLease), as this journey reads it. */
interface I3LeaseWitness {
  state: string
  fence: number
  live: boolean
  holder_sid: string
  renewal_count: number
  version: number
  end_reason: string
}
/** One WorkItem's current domain state, read over authenticated HTTP. */
interface I3ItemWitness {
  id: string
  workspace_id: string
  status: string
  owner_kind: string
  owner_ref: string
  owner_epoch: number
  leased: boolean
  claimable: boolean
  orphaned: boolean
  version: number
  /** The item's own event sequence; the engine advances it once per applied effect. */
  last_event_seq: number
  /** The HEADER ETag of the WorkItem read; the coordinate an offer must present. */
  etag: string
  lease: I3LeaseWitness
  observed_at: string
}
/** A WorkItem created and readied over the public Work Interface. */
interface I3WorkFixture {
  role: I3ItemRole
  id: string
  title: string
  /** The header ETag the `item.ready` transition published. */
  etag: string
  created: { etag: string; owner_epoch: number; lease_fence_present: boolean }
  readied: I3ItemWitness
}
type I3ItemRole = 'offered' | 'rejected' | 'lost'

/** What the browser observed on the real offer dispatch (private; not persisted). */
interface I3ObservedOffer {
  status: number
  ifMatch: string
  key: string
  body: HandoffOfferInput
  result: HandoffOfferResult
  etag: string | null
}
/** What the browser observed on the real response dispatch (private; not persisted). */
interface I3ObservedResponse {
  status: number
  method: string
  pathname: string
  postData: string
  ifMatch: string
  key: string
  body: HandoffResponseInput
  result: HandoffResponseResult
  /** The parsed wire object, so key ABSENCE can be asserted (not a decoded zero). */
  wire: Record<string, unknown>
}
interface I3DiscoveredHandoff {
  /** Derived from B's own selected row, never from A's receipt. */
  deliveryId: string
  detail: HandoffDetail
  sheet: Locator
}
interface I3FocusDescriptor {
  text: string
  ariaLabel: string
  labelledBy: string
  role: string
  inDialog: boolean
}
/** One returned Channel grant as safe receipt evidence: identities and bits only. */
interface I3GrantEvidence {
  label: 'A' | 'B' | 'unexpected'
  kind: string
  ref: string
  id: string
  state: string
  can_read: boolean
  can_write: boolean
  can_admin: boolean
}
/** A safe network summary: method, route and status, never headers or bodies. */
interface I3NetworkSummary {
  method: string
  route: string
  status: number
  outcome: string
}

interface I3Evidence {
  scope: {
    contract: string
    selector: string
    titles: string[]
    limits: string[]
  }
  channel?: {
    id: string
    slug: string
    workspace_id: string
    etag: string
    audit_seq: number
    grants: I3GrantEvidence[]
  }
  items?: Partial<Record<I3ItemRole, I3WorkFixture>>
  restarts: { after: string; at: string }[]
  process_observation: { work_dir_relative: string[] }
  test1?: Record<string, unknown>
  test2?: Record<string, unknown>
  test3?: Record<string, unknown>
  test4?: Record<string, unknown>
}
const i3: I3Evidence = {
  scope: {
    contract:
      'assessments/product/k3-i3-browser-qualification-inputs/I3-BROWSER-CONSTRUCTION-1.md',
    selector: "--grep 'K3 I3:'",
    titles: [
      'K3 I3: offered work survives restart and accepted ownership survives a second restart',
      'K3 I3: keyboard rejection preserves ownership and exact response replay has no new effect',
      'K3 I3: context changes and reload preserve honest handling of a lost offer response',
      'K3 I3: handoff views support keyboard focus, narrow layout, themes and seven locales',
    ],
    limits: [
      'selected-route locale coverage, not complete translated product acceptance',
      'no public Ack read route: Ack identity is retained from the response receipt only',
      'the dropped response of test 3 is the one named fault; ordinary offers and responses are unintercepted',
      'graceful flush is not inferred from a stopped pointer; durability is shown by domain rereads',
    ],
  },
  restarts: [],
  process_observation: {
    work_dir_relative: ['engine-lifecycle', 'process-observations'],
  },
}
evidence.i3 = i3

let i3ChannelId = ''
const i3Items: Partial<Record<I3ItemRole, I3WorkFixture>> = {}
function i3Fixture(role: I3ItemRole): I3WorkFixture {
  const fixture = i3Items[role]
  if (!fixture) throw new Error(`I3 fixture "${role}" was not provisioned`)
  return fixture
}

function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
}

/** A private screenshot in the evidence directory; nothing is copied to a report. */
async function i3Shot(page: Page, name: string) {
  await page.screenshot({
    path: path.join(evidenceDir(), `${name}.png`),
    fullPage: true,
  })
}

/** Sends a body VERBATIM (the exact bytes a UI dispatch carried), unlike `api`,
 * which re-serialises. Used only for the labelled HTTP replay/stale controls. */
async function apiRaw(
  request: APIRequestContext,
  token: string,
  route: string,
  rawBody: string,
  headers: Record<string, string>,
) {
  const res = await request.fetch(`${BASE}${route}`, {
    method: 'POST',
    headers: {
      Authorization: `Bearer ${token}`,
      'X-Olivares-Tenant': TENANT,
      'Content-Type': 'application/json',
      ...headers,
    },
    data: rawBody,
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

/** The engine's error envelope, reduced to the code it names. */
function i3ErrorCode(json: unknown): string {
  const error = (
    json as { error?: { code?: string; message?: string } } | undefined
  )?.error
  return error?.code ?? error?.message ?? ''
}

/** WorkItem + lease over authenticated HTTP: the independent state witness. */
async function readItemWitness(
  request: APIRequestContext,
  token: string,
  itemId: string,
): Promise<I3ItemWitness> {
  const item = await apiRead(request, token, `${I3_WORK_ITEMS}/${itemId}`)
  expect(item.status, `read WorkItem ${itemId}`).toBe(200)
  const snapshot = item.json as WorkSnapshot
  expect(snapshot.item.id).toBe(itemId)
  const lease = await apiRead(
    request,
    token,
    `${I3_WORK_ITEMS}/${itemId}/lease`,
  )
  expect(lease.status, `read WorkLease ${itemId}`).toBe(200)
  const l = lease.json as WorkLease
  return {
    id: snapshot.item.id,
    workspace_id: snapshot.item.workspace_id,
    status: snapshot.item.status,
    owner_kind: snapshot.item.owner_kind,
    owner_ref: snapshot.item.owner_ref,
    owner_epoch: snapshot.item.owner_epoch,
    leased: snapshot.item.leased,
    claimable: snapshot.item.claimable,
    orphaned: snapshot.item.orphaned,
    version: snapshot.item.version,
    last_event_seq: snapshot.item.last_event_seq,
    etag: item.headers.etag ?? '',
    lease: {
      state: l.state,
      fence: l.fence,
      live: l.live,
      holder_sid: l.holder_sid ?? '',
      renewal_count: l.renewal_count,
      version: l.version ?? 0,
      end_reason: l.end_reason ?? '',
    },
    observed_at: new Date().toISOString(),
  }
}

/** The exact vacant, human-owned shape createVacantHandoffWork leaves behind. */
function expectVacantHumanOwned(
  witness: I3ItemWitness,
  owner: string,
  epoch: number,
  label: string,
) {
  expect(witness.owner_kind, `${label}: owner kind`).toBe('user')
  expect(witness.owner_ref, `${label}: owner`).toBe(owner)
  expect(witness.owner_epoch, `${label}: owner epoch`).toBe(epoch)
  expect(witness.status, `${label}: status`).toBe('ready')
  expect(witness.leased, `${label}: leased`).toBe(false)
  expect(witness.claimable, `${label}: claimable`).toBe(false)
  expect(witness.orphaned, `${label}: orphaned`).toBe(false)
  expect(witness.lease.state, `${label}: lease state`).toBe('vacant')
  expect(witness.lease.fence, `${label}: lease fence`).toBe(0)
  expect(witness.lease.live, `${label}: lease live`).toBe(false)
  expect(witness.lease.holder_sid, `${label}: lease holder`).toBe('')
  expect(witness.lease.renewal_count, `${label}: renewals`).toBe(0)
}

/**
 * createVacantHandoffWork (cmd/olivares/communicationhandoffvacant_http_test.go),
 * over the public Work Interface only: create with `mode=apply`, then `item.ready`
 * under the header ETag the create published. No lease is acquired and no Store
 * table is written; the item keeps the vacant generation created with it.
 */
async function createReadyWorkItem(
  request: APIRequestContext,
  role: I3ItemRole,
  title: string,
): Promise<I3WorkFixture> {
  const created = await api(
    request,
    tokenA,
    'POST',
    `${I3_WORK_ITEMS}?mode=apply`,
    {
      workspace_id: wsBilling,
      work_kind: 'implementation',
      title,
      brief_md:
        'Created over the public Work Interface for the K3 I3 browser journey.',
      context_refs: [],
      priority: 'p1',
      owner_kind: 'user',
      owner_ref: userA,
      provenance_kind: 'human',
      provenance_ref: `journey:k3-i3-${STAMP}`,
      acceptance: [
        {
          criterion_key: 'transfer',
          ordinal: 0,
          statement: 'Ownership transfers with no execution in flight',
          required: true,
        },
      ],
    },
    { 'Idempotency-Key': uuidv7() },
  )
  expect(created.status, `create ${role} WorkItem`).toBe(200)
  const result = created.json as CommandResult
  expect(result.result_id, `create ${role}: result id`).toMatch(UUID_ANY)
  expect(result.owner_epoch, `create ${role}: owner epoch`).toBe(1)
  expect(
    'lease_fence' in (created.json as Record<string, unknown>),
    `create ${role}: no lease fence on the wire`,
  ).toBe(false)
  expect(created.headers.etag, `create ${role}: header ETag`).toBe('"v1"')
  const id = result.result_id as string
  const ready = await api(
    request,
    tokenA,
    'POST',
    `${I3_WORK_ITEMS}/${id}/transitions?mode=apply`,
    { command: 'item.ready' },
    { 'If-Match': created.headers.etag, 'Idempotency-Key': uuidv7() },
  )
  expect(ready.status, `ready ${role} WorkItem`).toBe(200)
  expect(ready.headers.etag, `ready ${role}: header ETag`).toMatch(/^"v\d+"$/)
  const readied = await readItemWitness(request, tokenA, id)
  expectVacantHumanOwned(readied, userA, 1, `readied ${role}`)
  expect(readied.etag, `ready ${role}: GET ETag equals the ready ETag`).toBe(
    ready.headers.etag,
  )
  return {
    role,
    id,
    title,
    etag: ready.headers.etag,
    created: {
      etag: created.headers.etag,
      owner_epoch: result.owner_epoch,
      lease_fence_present: false,
    },
    readied,
  }
}

/** Counts the requests a page issues that match, for "no second POST" assertions. */
function observeRequests(
  page: Page,
  matches: (url: URL, method: string) => boolean,
): { count: () => number; seen: () => string[] } {
  const seen: string[] = []
  page.on('request', (r) => {
    const url = new URL(r.url())
    if (matches(url, r.method())) seen.push(`${r.method()} ${url.pathname}`)
  })
  return { count: () => seen.length, seen: () => [...seen] }
}
const isOfferPost = (url: URL, method: string) =>
  method === 'POST' && url.pathname === I3_HANDOFFS
const isResponsePost = (url: URL, method: string) =>
  method === 'POST' && I3_RESPONSES_PATH.test(url.pathname)

/** The workspace switcher shows the active workspace; select only when needed. */
async function ensureWorkspace(page: Page, name: 'Billing' | 'Default') {
  const trigger = page
    .getByRole('button', {
      name: /All workspaces|Billing|Default|Workspace not in list/i,
    })
    .first()
  await expect(trigger).toBeVisible()
  const label = ((await trigger.textContent()) ?? '').trim()
  if (!new RegExp(`^${name}`, 'i').test(label))
    await selectWorkspace(page, name)
}

/** After an engine restart the console may or may not ask for credentials again.
 * Reauthenticate only through the actual login page when it does; report it. */
async function gotoSignedIn(
  page: Page,
  target: string,
  email: string,
): Promise<boolean> {
  await page.goto(target)
  // The console may redirect to the login page client-side after the first
  // authenticated read fails, so wait for whichever surface actually renders.
  const door = page.getByRole('heading', {
    level: 1,
    name: 'Handoffs',
    exact: true,
  })
  const loginField = page.locator('#email')
  await expect(door.or(loginField).first()).toBeVisible()
  if (!new URL(page.url()).pathname.startsWith('/login')) return false
  await uiLogin(page, email, MEMBER_PASSWORD)
  await page.goto(target)
  await expect(door).toBeVisible()
  return true
}

function localDateTimeValue(at: Date): string {
  const p = (n: number) => String(n).padStart(2, '0')
  return `${at.getFullYear()}-${p(at.getMonth() + 1)}-${p(at.getDate())}T${p(at.getHours())}:${p(at.getMinutes())}:${p(at.getSeconds())}`
}

function workItemSheet(page: Page, fixture: I3WorkFixture): Locator {
  return page.getByRole('dialog').filter({
    has: page.getByRole('heading', {
      name: new RegExp(escapeRegExp(fixture.title)),
    }),
  })
}

/** Opens the real WorkItem detail from the tenant-wide work list (no deep link). */
async function openWorkItemSheet(
  page: Page,
  fixture: I3WorkFixture,
): Promise<Locator> {
  const sheet = workItemSheet(page, fixture)
  if ((await sheet.count()) === 0 || !(await sheet.isVisible())) {
    const row = page.getByRole('button', {
      name: new RegExp(escapeRegExp(fixture.title)),
    })
    await expect(row).toBeVisible()
    await row.click()
  }
  await expect(sheet).toBeVisible()
  await expect(sheet.locator('[data-slot="work-offer-handoff"]')).toBeVisible()
  return sheet
}

function offerDialog(page: Page): Locator {
  return page.getByRole('dialog').filter({
    has: page.getByRole('heading', { name: 'Offer handoff', exact: true }),
  })
}

/** Presses «Offer handoff» and observes the host's own fresh, uncached WorkItem
 * read; returns the dialog and the header ETag that read published. */
async function openOfferDialog(
  page: Page,
  sheet: Locator,
  itemId: string,
): Promise<{ dialog: Locator; etag: string }> {
  const freshRead = page.waitForResponse(
    (r) =>
      r.request().method() === 'GET' &&
      new URL(r.url()).pathname === `${I3_WORK_ITEMS}/${itemId}`,
  )
  await sheet.locator('[data-slot="work-offer-handoff"]').click()
  const read = await freshRead
  expect(read.status()).toBe(200)
  const dialog = offerDialog(page)
  await expect(dialog).toBeVisible()
  await expect(dialog.locator('[data-slot="handoff-offer-item"]')).toBeVisible()
  return { dialog, etag: read.headers()['etag'] ?? '' }
}

interface I3OfferDraft {
  summary: string
  nextAction: string
  deadlineLocal: string
}
/** Fills the actual offer form and reviews it; returns the frozen key and the
 * precondition the review names, plus the UTC instant the deadline resolves to in
 * the browser's own zone. */
async function fillAndReviewOffer(
  page: Page,
  dialog: Locator,
  draft: I3OfferDraft,
): Promise<{ key: string; precondition: string; deadlineUtc: string }> {
  await dialog.getByLabel('Channel', { exact: true }).fill(i3ChannelId)
  await dialog.getByRole('textbox', { name: 'Reference (ID)' }).fill(userB)
  await dialog.getByLabel('Summary', { exact: true }).fill(draft.summary)
  await dialog.getByLabel('Next action', { exact: true }).fill(draft.nextAction)
  await dialog
    .getByLabel('Response deadline', { exact: true })
    .fill(draft.deadlineLocal)
  await expect(
    dialog.locator('[data-slot="handoff-offer-deadline-review"]'),
  ).toBeVisible()
  const deadlineUtc = await page.evaluate(
    (value) => new Date(value).toISOString().replace(/\.\d{3}Z$/, 'Z'),
    draft.deadlineLocal,
  )
  await dialog.getByRole('button', { name: 'Review offer' }).click()
  const intent = dialog.locator('[data-slot="handoff-offer-intent"]')
  await expect(intent).toBeVisible()
  const key =
    (
      await intent.locator('dt:text-is("Idempotency key") + dd').textContent()
    )?.trim() ?? ''
  const precondition =
    (
      await intent
        .locator('dt:text-is("Precondition (If-Match)") + dd')
        .textContent()
    )?.trim() ?? ''
  expect(key, 'the reviewed key is a canonical UUID v7').toMatch(UUID_V7)
  return { key, precondition, deadlineUtc }
}

/** Confirms ONCE and observes the real request and response, unintercepted. */
async function confirmOffer(
  page: Page,
  dialog: Locator,
): Promise<I3ObservedOffer> {
  const responded = page.waitForResponse(
    (r) =>
      r.request().method() === 'POST' &&
      new URL(r.url()).pathname === I3_HANDOFFS,
  )
  await dialog.getByRole('button', { name: 'Confirm and offer' }).click()
  const response = await responded
  const headers = response.request().headers()
  return {
    status: response.status(),
    ifMatch: headers['if-match'] ?? '',
    key: headers['idempotency-key'] ?? '',
    body: response.request().postDataJSON() as HandoffOfferInput,
    result: (await response.json()) as HandoffOfferResult,
    etag: response.headers()['etag'] ?? null,
  }
}

function handoffSheet(page: Page): Locator {
  return page.getByRole('dialog').filter({
    has: page.getByRole('heading', { name: 'Handoff offer', exact: true }),
  })
}
function respondDialog(page: Page, title: string): Locator {
  return page.getByRole('dialog').filter({
    has: page.getByRole('heading', { name: title, exact: true }),
  })
}

/**
 * B's own discovery: the Handoffs door, the explicit workspace, the explicit state
 * filter, the row naming the WorkItem, and the carrier Delivery ID read from THAT
 * row. Opening the row is the actual Delivery-bound detail read.
 */
async function discoverHandoff(
  page: Page,
  itemId: string,
  state: I3ListState,
): Promise<I3DiscoveredHandoff> {
  await ensureWorkspace(page, 'Billing')
  await expect(
    page.getByRole('heading', { level: 1, name: 'Handoffs', exact: true }),
  ).toBeVisible()
  await expect(
    page.getByRole('tab', { name: 'Handoffs', selected: true }),
  ).toBeVisible()
  if (state !== 'offered') {
    const listed = page.waitForResponse(
      (r) =>
        r.request().method() === 'GET' &&
        new URL(r.url()).pathname === I3_HANDOFF_INBOX &&
        new URL(r.url()).searchParams.get('state') === state,
    )
    await page.getByRole('combobox', { name: 'State', exact: true }).click()
    await page
      .getByRole('option', { name: I3_STATE_LABEL[state], exact: true })
      .click()
    expect((await listed).status()).toBe(200)
  }
  const row = page.getByRole('row').filter({ hasText: itemId })
  await expect(row).toHaveCount(1)
  await expect(row).toContainText(I3_STATE_LABEL[state])
  const deliveryId =
    (await row.getByRole('gridcell').last().textContent())?.trim() ?? ''
  expect(deliveryId, 'the carrier Delivery ID from the row').toMatch(UUID_ANY)
  const detailRead = page.waitForResponse(
    (r) =>
      r.request().method() === 'GET' &&
      new URL(r.url()).pathname === `${I3_DELIVERIES}/${deliveryId}/handoff`,
  )
  await row.click()
  const read = await detailRead
  expect(read.status()).toBe(200)
  const detail = (await read.json()) as HandoffDetail
  expect(new URL(page.url()).searchParams.get('handoff')).toBe(deliveryId)
  const sheet = handoffSheet(page)
  await expect(sheet.locator('[data-slot="handoff-detail"]')).toBeVisible()
  expect(detail.carrier.delivery_id).toBe(deliveryId)
  expect(detail.work_item.id).toBe(itemId)
  return { deliveryId, detail, sheet }
}

/** Observes the real response dispatch: method, path, exact body, precondition and
 * key stay in memory for the labelled HTTP controls. */
async function observeResponse(
  page: Page,
  press: () => Promise<void>,
): Promise<I3ObservedResponse> {
  const responded = page.waitForResponse(
    (r) =>
      r.request().method() === 'POST' &&
      I3_RESPONSES_PATH.test(new URL(r.url()).pathname),
  )
  await press()
  const response = await responded
  const request = response.request()
  const headers = request.headers()
  const wire = (await response.json()) as Record<string, unknown>
  return {
    status: response.status(),
    method: request.method(),
    pathname: new URL(request.url()).pathname,
    postData: request.postData() ?? '',
    ifMatch: headers['if-match'] ?? '',
    key: headers['idempotency-key'] ?? '',
    body: request.postDataJSON() as HandoffResponseInput,
    result: wire as unknown as HandoffResponseResult,
    wire,
  }
}

async function activeElement(page: Page): Promise<I3FocusDescriptor> {
  return page.evaluate(() => {
    const el = document.activeElement as HTMLElement | null
    const ids = el?.getAttribute('aria-labelledby')?.split(/\s+/) ?? []
    return {
      text: el?.textContent?.trim() ?? '',
      ariaLabel: el?.getAttribute('aria-label') ?? '',
      labelledBy: ids
        .map((id) => document.getElementById(id)?.textContent?.trim() ?? '')
        .join(' ')
        .trim(),
      role: el?.getAttribute('role') ?? el?.tagName.toLowerCase() ?? '',
      inDialog: el?.closest('[role="dialog"]') !== null,
    }
  })
}

/** Moves focus with Tab (or Shift+Tab) until the active element matches. */
async function tabUntil(
  page: Page,
  want: (focus: I3FocusDescriptor) => boolean,
  label: string,
  options: { backwards?: boolean; max?: number } = {},
) {
  const max = options.max ?? 60
  for (let i = 0; i <= max; i++) {
    if (want(await activeElement(page))) return
    if (i === max) break
    await page.keyboard.press(options.backwards ? 'Shift+Tab' : 'Tab')
  }
  throw new Error(
    `could not reach ${label} by ${options.backwards ? 'Shift+Tab' : 'Tab'}`,
  )
}

interface I3Viewport {
  width: number
  height: number
}
interface I3Rect {
  x: number
  y: number
  width: number
  height: number
}
/** The overlay's scroll facts, read from computed style and scroll metrics. */
interface I3ScrollMetrics {
  /** Which element the mounted design makes the vertical scroll container. */
  container: 'overlay' | 'descendant' | 'none'
  overflowX: string
  overflowY: string
  scrollTop: number
  scrollLeft: number
  scrollWidth: number
  clientWidth: number
  scrollHeight: number
  clientHeight: number
  /** The overlay element itself, whatever the container is. */
  overlayScrollWidth: number
  overlayClientWidth: number
}
interface I3ActionGeometry {
  name: string
  box: I3Rect
  /** Fully inside the viewport in the natural view, with no scroll. */
  inViewport: boolean
  withinOverlayX: boolean
  withinOverlayY: boolean
  /** Inside the designed scroll container's content extent at scroll 0. */
  withinScrollExtent: boolean
}
interface I3OverlayGeometry {
  overlay: I3Rect
  scroll: I3ScrollMetrics
  actions: I3ActionGeometry[]
  naturally_in_viewport: string[]
}

const inside = (box: I3Rect, frame: I3Rect) =>
  box.x >= frame.x - 0.5 &&
  box.y >= frame.y - 0.5 &&
  box.x + box.width <= frame.x + frame.width + 0.5 &&
  box.y + box.height <= frame.y + frame.height + 0.5
const viewportRect = (v: I3Viewport): I3Rect => ({
  x: 0,
  y: 0,
  width: v.width,
  height: v.height,
})

/** Lets the overlay's own enter transition finish, so the geometry is the settled
 * natural view rather than a frame of the slide-in. Only the overlay element's
 * animations are awaited; the 2 s race is a guard against a never-finishing
 * animation, not a pacing delay. No scroll, focus or click happens here. */
async function settleOverlay(overlay: Locator) {
  await overlay.evaluate((el) =>
    Promise.race([
      Promise.all(
        el.getAnimations().map((a) => a.finished.catch(() => undefined)),
      ),
      new Promise((resolve) => setTimeout(resolve, 2000)),
    ]),
  )
}

function scrollMetricsOf(overlay: Locator): Promise<I3ScrollMetrics> {
  return overlay.evaluate((el) => {
    const scrolls = (node: Element) => {
      const y = getComputedStyle(node).overflowY
      return y === 'auto' || y === 'scroll'
    }
    let container: 'overlay' | 'descendant' | 'none' = 'none'
    let target: Element | null = null
    if (scrolls(el)) {
      container = 'overlay'
      target = el
    } else {
      for (const child of Array.from(el.querySelectorAll('*'))) {
        if (scrolls(child)) {
          container = 'descendant'
          target = child
          break
        }
      }
    }
    const measured = target ?? el
    const style = getComputedStyle(measured)
    return {
      container,
      overflowX: style.overflowX,
      overflowY: style.overflowY,
      scrollTop: measured.scrollTop,
      scrollLeft: measured.scrollLeft,
      scrollWidth: measured.scrollWidth,
      clientWidth: measured.clientWidth,
      scrollHeight: measured.scrollHeight,
      clientHeight: measured.clientHeight,
      overlayScrollWidth: el.scrollWidth,
      overlayClientWidth: el.clientWidth,
    }
  })
}

/**
 * The NATURAL view of an open overlay: every named action's rectangle as it is the
 * moment the overlay has opened and rendered, with no scroll, focus, click or hover
 * in between. `boundingBox` and `toBeAttached` do not scroll. The designed scroll
 * container's offsets are read before and after the capture and must be zero both
 * times, which is what proves nothing repaired a position.
 */
async function captureNaturalGeometry(
  overlay: Locator,
  viewport: I3Viewport,
  names: string[],
): Promise<I3OverlayGeometry> {
  await settleOverlay(overlay)
  const before = await scrollMetricsOf(overlay)
  expect(before.scrollTop, 'natural view: no vertical scroll').toBe(0)
  expect(before.scrollLeft, 'natural view: no horizontal scroll').toBe(0)
  const overlayBox = await overlay.boundingBox()
  expect(overlayBox, 'the overlay has a box').not.toBeNull()
  if (!overlayBox) throw new Error('unreachable')
  const frame = viewportRect(viewport)
  const actions: I3ActionGeometry[] = []
  for (const name of names) {
    const control = overlay.getByRole('button', { name, exact: true })
    await expect(control, `${name} is offered`).toBeAttached()
    const box = await control.boundingBox()
    expect(box, `${name} has a natural box`).not.toBeNull()
    if (!box) throw new Error('unreachable')
    actions.push({
      name,
      box,
      inViewport: inside(box, frame),
      withinOverlayX:
        box.x >= overlayBox.x - 0.5 &&
        box.x + box.width <= overlayBox.x + overlayBox.width + 0.5,
      withinOverlayY:
        box.y >= overlayBox.y - 0.5 &&
        box.y + box.height <= overlayBox.y + overlayBox.height + 0.5,
      withinScrollExtent:
        box.y >= overlayBox.y - 0.5 &&
        box.y + box.height <= overlayBox.y + before.scrollHeight + 0.5,
    })
  }
  const after = await scrollMetricsOf(overlay)
  expect(after.scrollTop, 'the capture did not scroll').toBe(0)
  expect(after.scrollLeft, 'the capture did not scroll').toBe(0)
  return {
    overlay: overlayBox,
    scroll: before,
    actions,
    naturally_in_viewport: actions
      .filter((a) => a.inViewport)
      .map((a) => a.name),
  }
}

/**
 * The HandoffSheet as mounted (handoff-sheet.tsx, sheet.tsx): a full-height side
 * panel that IS the vertical scroll container (`overflow-y-auto` on SheetContent),
 * its close control absolute at the top, its footer after the protected detail.
 * Permitted by that design: vertical scrolling inside the panel. Prohibited: the
 * panel leaving the viewport, any horizontal overflow of the panel, an action
 * outside the panel's horizontal bounds or the viewport's width, an action outside
 * the panel's scrollable content (which no scroll could reach), and a close
 * control that is not naturally visible. Whether each action is naturally in the
 * viewport is recorded; keyboard reachability is asserted separately.
 */
function expectSheetGeometry(g: I3OverlayGeometry, viewport: I3Viewport) {
  expect(g.scroll.container, 'the panel is the designed scroll container').toBe(
    'overlay',
  )
  expect(['auto', 'scroll'], 'designed vertical scrolling').toContain(
    g.scroll.overflowY,
  )
  expect(
    g.scroll.scrollWidth,
    'no horizontal overflow inside the panel',
  ).toBeLessThanOrEqual(g.scroll.clientWidth + 1)
  expect(inside(g.overlay, viewportRect(viewport)), 'panel in viewport').toBe(
    true,
  )
  for (const a of g.actions) {
    expect(a.withinOverlayX, `${a.name} inside the panel horizontally`).toBe(
      true,
    )
    expect(a.box.x, `${a.name} left edge`).toBeGreaterThanOrEqual(-0.5)
    expect(a.box.x + a.box.width, `${a.name} right edge`).toBeLessThanOrEqual(
      viewport.width + 0.5,
    )
    expect(
      a.withinScrollExtent,
      `${a.name} inside the panel's scrollable content`,
    ).toBe(true)
  }
  const close = g.actions.find((a) => a.name === 'Close')
  expect(close?.inViewport, 'Close is naturally visible').toBe(true)
}

/**
 * The respond dialog as mounted (handoff-respond-dialog.tsx, dialog.tsx): a
 * centered panel capped at 85vh whose BODY is the only scroll region; header,
 * footer and close control sit outside it. Permitted by that design: vertical
 * scrolling of the body. Prohibited: the panel or any named action leaving the
 * viewport or the panel, and horizontal overflow. Every named action must be
 * naturally visible with no scroll at all.
 */
function expectDialogGeometry(g: I3OverlayGeometry, viewport: I3Viewport) {
  expect(g.scroll.container, 'the body is the designed scroll region').toBe(
    'descendant',
  )
  expect(['auto', 'scroll'], 'designed body scrolling').toContain(
    g.scroll.overflowY,
  )
  expect(
    g.scroll.overlayScrollWidth,
    'no horizontal overflow of the panel',
  ).toBeLessThanOrEqual(g.scroll.overlayClientWidth + 1)
  expect(
    g.scroll.scrollWidth,
    'no horizontal overflow of the body',
  ).toBeLessThanOrEqual(g.scroll.clientWidth + 1)
  expect(inside(g.overlay, viewportRect(viewport)), 'panel in viewport').toBe(
    true,
  )
  expect(g.overlay.height, 'panel capped at 85vh').toBeLessThanOrEqual(
    viewport.height * 0.85 + 1,
  )
  for (const a of g.actions) {
    expect(a.inViewport, `${a.name} naturally in the viewport`).toBe(true)
    expect(
      a.withinOverlayX && a.withinOverlayY,
      `${a.name} inside the panel`,
    ).toBe(true)
  }
}

/**
 * Deliberate keyboard reachability, kept apart from the natural view: Tab moves
 * focus to the named action, and the focused action must then be fully inside the
 * viewport and unobscured at its center (WCAG 2.4.11). The scrolling that follows
 * focus is the user's own movement, not a harness repair of the natural view: the
 * browser makes it on native Tab steps, and the panel makes it for the two moves
 * Radix performs with `preventScroll: true` — its mount focus and its Tab wrap.
 */
async function expectReachableByTab(
  page: Page,
  overlay: Locator,
  viewport: I3Viewport,
  name: string,
): Promise<I3Rect> {
  const control = overlay.getByRole('button', { name, exact: true })
  await tabUntil(
    page,
    (f) => f.inDialog && (f.text === name || f.ariaLabel === name),
    name,
    { max: 40 },
  )
  expect(
    await control.evaluate((el) => el === document.activeElement),
    `${name} has focus`,
  ).toBe(true)
  const box = await control.boundingBox()
  expect(box, `${name} has a box when focused`).not.toBeNull()
  if (!box) throw new Error('unreachable')
  expect(inside(box, viewportRect(viewport)), `${name} in viewport`).toBe(true)
  const unobscured = await control.evaluate((el) => {
    const r = el.getBoundingClientRect()
    const hit = document.elementFromPoint(
      r.left + r.width / 2,
      r.top + r.height / 2,
    )
    return hit !== null && (hit === el || el.contains(hit))
  })
  expect(unobscured, `${name} is not obscured when focused`).toBe(true)
  return box
}

/**
 * The sheet's initial focus is its own title, which precedes the asynchronously
 * loaded body, so a read that lands after the open cannot move the focused element.
 * Asserted on the real sheet at both moments; nothing here moves focus or scroll.
 */
async function expectTitleHoldsFocus(
  overlay: Locator,
  viewport: I3Viewport,
  when: string,
): Promise<I3Rect> {
  const title = overlay.getByRole('heading', {
    name: 'Handoff offer',
    exact: true,
  })
  expect(
    await title.evaluate((el) => el === document.activeElement),
    `the title holds focus ${when}`,
  ).toBe(true)
  const box = await title.boundingBox()
  expect(box, `the title has a box ${when}`).not.toBeNull()
  if (!box) throw new Error('unreachable')
  expect(
    inside(box, viewportRect(viewport)),
    `the title is in the viewport ${when}`,
  ).toBe(true)
  const unobscured = await title.evaluate((el) => {
    const r = el.getBoundingClientRect()
    const hit = document.elementFromPoint(
      r.left + r.width / 2,
      r.top + r.height / 2,
    )
    return hit !== null && (hit === el || el.contains(hit))
  })
  expect(unobscured, `the title is not obscured ${when}`).toBe(true)
  return box
}

async function setTheme(page: Page, theme: 'light' | 'dark') {
  await page.getByRole('button', { name: 'Toggle theme' }).click()
  await page
    .getByRole('menuitem', { name: theme === 'dark' ? 'Dark' : 'Light' })
    .click()
  if (theme === 'dark') await expect(page.locator('html')).toHaveClass(/dark/)
  else await expect(page.locator('html')).not.toHaveClass(/dark/)
}

/** The canonical feature resources this door renders, read from the source tree. */
function handoffLocaleStrings(lng: I3Locale): { title: string; tab: string } {
  const file = path.resolve(
    I3_HERE,
    '..',
    'src',
    'features',
    'communications',
    'i18n',
    `${lng}.json`,
  )
  const parsed = JSON.parse(readFileSync(file, 'utf8')) as {
    doors: { handoffs: { title: string } }
    tabs: { handoffs: string }
  }
  return { title: parsed.doors.handoffs.title, tab: parsed.tabs.handoffs }
}

// The I3 estate: its own Channel and three ready, never-held WorkItems, all
// through the public routes with A's own credential. Request-only; no browser.
test.beforeAll(async ({ request }) => {
  expect(tokenA, 'A is provisioned by the I1 beforeAll').not.toBe('')
  const created = await api(
    request,
    tokenA,
    'POST',
    '/v1/m/sessions/channels',
    {
      workspace_id: wsBilling,
      slug: I3_CHANNEL_SLUG,
      name: 'K3 I3 handoff',
      description: 'Carrier channel for the I3 browser journey',
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
  expect(created.status, 'create the I3 channel').toBe(201)
  // The real route response schema: the created Channel, its initial grants and
  // the fresh ETag. Identity, slug and workspace are asserted from the reply.
  const receipt = created.json as ChannelMutationResult
  i3ChannelId = receipt.channel.id
  expect(i3ChannelId, 'the returned channel identity').toMatch(UUID_ANY)
  expect(receipt.channel.slug, 'the returned slug').toBe(I3_CHANNEL_SLUG)
  expect(receipt.channel.workspace_id, 'the returned workspace').toBe(wsBilling)
  expect(receipt.etag, 'the fresh channel ETag').toBe('"v1"')
  expect(created.headers.etag, 'header ETag equals the body ETag').toBe(
    receipt.etag,
  )
  // The EXACT unordered grant set: user A read/write/admin and user B
  // read/write without admin, two unique subjects, no extra grant. A subject
  // that is neither A nor B fails here and is never relabelled as B.
  const grants: ChannelGrant[] = receipt.grants ?? []
  const grantTuple = (g: ChannelGrant) =>
    `${g.subject.kind}:${g.subject.ref}:read=${g.can_read}:write=${g.can_write}:admin=${g.can_admin}`
  expect(grants, 'exactly two grants returned').toHaveLength(2)
  expect(
    new Set(grants.map((g) => `${g.subject.kind}:${g.subject.ref}`)).size,
    'unique grant subjects',
  ).toBe(2)
  expect(grants.map(grantTuple).sort(), 'the exact grant set').toEqual(
    [
      `user:${userA}:read=true:write=true:admin=true`,
      `user:${userB}:read=true:write=true:admin=false`,
    ].sort(),
  )
  for (const g of grants) {
    expect(g.id, 'grant id').toMatch(UUID_ANY)
    expect(g.channel_id, 'grant channel').toBe(receipt.channel.id)
    expect(g.subject.kind, 'grant subject kind').toBe('user')
  }
  const grantLabel = (ref: string): I3GrantEvidence['label'] =>
    ref === userA ? 'A' : ref === userB ? 'B' : 'unexpected'
  i3.channel = {
    id: i3ChannelId,
    slug: receipt.channel.slug,
    workspace_id: receipt.channel.workspace_id,
    etag: receipt.etag,
    audit_seq: receipt.audit_seq,
    grants: grants.map((g) => ({
      label: grantLabel(g.subject.ref),
      kind: g.subject.kind,
      ref: g.subject.ref,
      id: g.id,
      state: g.state,
      can_read: g.can_read,
      can_write: g.can_write,
      can_admin: g.can_admin,
    })),
  }
  i3Items.offered = await createReadyWorkItem(
    request,
    'offered',
    `K3 I3 offered ${STAMP}`,
  )
  i3Items.rejected = await createReadyWorkItem(
    request,
    'rejected',
    `K3 I3 rejected ${STAMP}`,
  )
  i3Items.lost = await createReadyWorkItem(
    request,
    'lost',
    `K3 I3 lost ${STAMP}`,
  )
  i3.items = i3Items
})

// ─── I3 · 1: offer, transfer, and two durable restarts ───────────────────────────
test('K3 I3: offered work survives restart and accepted ownership survives a second restart', async ({
  browser,
  request,
}) => {
  const fixture = i3Fixture('offered')
  const summary = `K3 I3 offered context ${STAMP}`
  const before = await readItemWitness(request, tokenA, fixture.id)
  expectVacantHumanOwned(before, userA, 1, 'before offer')

  // A offers through the console, in its own context.
  let offer: I3ObservedOffer | null
  let reviewedKey: string
  let reviewedPrecondition: string
  let deadlineUtc: string
  let freshEtag: string
  let offerPostsA: number
  const ctxA = await browser.newContext()
  try {
    const a = await ctxA.newPage()
    const posts = observeRequests(a, isOfferPost)
    await uiLogin(a, A_EMAIL, MEMBER_PASSWORD)
    await a.goto('/work')
    await ensureWorkspace(a, 'Billing')
    const sheet = await openWorkItemSheet(a, fixture)
    const opened = await openOfferDialog(a, sheet, fixture.id)
    freshEtag = opened.etag
    expect(freshEtag, 'the fresh WorkItem read carries a header ETag').toMatch(
      /^"v\d+"$/,
    )
    const draft: I3OfferDraft = {
      summary,
      nextAction: `Continue ${summary}`,
      deadlineLocal: localDateTimeValue(
        new Date(Date.now() + 30 * 60 * 60 * 1000),
      ),
    }
    const reviewed = await fillAndReviewOffer(a, opened.dialog, draft)
    reviewedKey = reviewed.key
    reviewedPrecondition = reviewed.precondition
    deadlineUtc = reviewed.deadlineUtc
    expect(reviewedPrecondition).toBe(freshEtag)
    await i3Shot(a, 'i3-01-a-offer-review')
    offer = await confirmOffer(a, opened.dialog)
    await expect(
      opened.dialog.locator('[data-slot="handoff-offer-receipt"]'),
    ).toBeVisible()
    await expect(opened.dialog.getByText('Offer created')).toBeVisible()
    await expect(
      opened.dialog.locator('[data-slot="handoff-offer-receipt"]'),
    ).toContainText(offer.result.handoff_id)
    await i3Shot(a, 'i3-02-a-offer-receipt')
    offerPostsA = posts.count()
  } finally {
    await ctxA.close()
  }
  if (!offer) throw new Error('the offer was not observed')
  expect(offerPostsA, 'exactly one offer POST left the console').toBe(1)
  expect(offer.status).toBe(201)
  expect(offer.ifMatch, 'If-Match is the fresh WorkItem header ETag').toBe(
    freshEtag,
  )
  expect(offer.ifMatch).toBe(before.etag)
  expect(offer.key, 'the dispatched key is the reviewed key').toBe(reviewedKey)
  expect(offer.key).toMatch(UUID_V7)
  expect(offer.body.expected_owner_epoch).toBe(1)
  expect(offer.body.work_item_id).toBe(fixture.id)
  expect(offer.body.channel_id).toBe(i3ChannelId)
  expect(offer.body.recipient).toEqual({ kind: 'user', ref: userB })
  expect(offer.body.ack_deadline).toBe(deadlineUtc)
  expect(offer.body.handoff.summary).toBe(summary)
  expect(offer.result.state).toBe('offered')
  expect(offer.result.replayed).toBe(false)
  expect(offer.result.work_item_id).toBe(fixture.id)
  expect(offer.result.handoff_id).toMatch(UUID_ANY)
  expect(offer.result.delivery_id).toMatch(UUID_ANY)
  const afterOffer = await readItemWitness(request, tokenA, fixture.id)
  expectVacantHumanOwned(afterOffer, userA, 1, 'after offer')
  expect(afterOffer.lease.version).toBe(before.lease.version)

  // Restart 1: after the offer, before B discovers it.
  await restartEngine()
  i3.restarts.push({ after: 'offer', at: new Date().toISOString() })

  // B discovers on its own door, accepts through the UI, survives restart 2.
  const network: I3NetworkSummary[] = [
    {
      method: 'POST',
      route: I3_HANDOFFS,
      status: offer.status,
      outcome: offer.result.state,
    },
  ]
  let accepted: I3ObservedResponse | null
  let afterAccept: I3ItemWitness | null
  let afterRestart: I3ItemWitness | null
  let afterReload: I3ItemWitness | null
  let reauthenticated: boolean
  let discoveryDelivery: string
  let discoveredEtag: string
  let rediscoveredEtag: string
  let reloadedEtag: string
  let responsePostsB: number
  const ctxB = await browser.newContext()
  try {
    const b = await ctxB.newPage()
    const posts = observeRequests(b, isResponsePost)
    await uiLogin(b, B_EMAIL, MEMBER_PASSWORD)
    await b.goto('/communications/handoffs')
    const found = await discoverHandoff(b, fixture.id, 'offered')
    discoveryDelivery = found.deliveryId
    expect(
      found.deliveryId,
      "B's own carrier Delivery ID equals the offer receipt's",
    ).toBe(offer.result.delivery_id)
    expect(found.detail.handoff.id).toBe(offer.result.handoff_id)
    expect(found.detail.handoff.state).toBe('offered')
    expect(found.detail.offer_context).toBe('current')
    expect(found.detail.handoff.etag).toBe(offer.result.etag)
    discoveredEtag = found.detail.handoff.etag
    await expect(
      found.sheet.locator('[data-slot="handoff-detail"]'),
    ).toContainText(found.detail.handoff.etag)
    // Exact: the sheet also renders `nextAction`, which contains this summary, so a
    // substring match would select both fields.
    await expect(found.sheet.getByText(summary, { exact: true })).toBeVisible()
    await i3Shot(b, 'i3-03-b-discovered-after-restart')

    await found.sheet
      .getByRole('button', { name: 'Accept responsibility', exact: true })
      .click()
    const dialog = respondDialog(b, 'Accept responsibility')
    await expect(dialog).toBeVisible()
    await dialog.getByRole('button', { name: 'Review response' }).click()
    const intent = dialog.locator('[data-slot="handoff-respond-intent"]')
    await expect(intent).toBeVisible()
    // The confirmation contract puts the WorkItem identity in the dialog, not inside
    // the frozen operation coordinates. Assert it where the product renders it, and
    // require a visible element rather than text that merely exists in the tree.
    await expect(dialog).toContainText(fixture.id)
    await expect(dialog.getByText(fixture.id, { exact: true })).toBeVisible()
    // The frozen coordinates stay in `intent`, which is what that subsection is for.
    await expect(intent).toContainText(found.detail.handoff.etag)
    accepted = await observeResponse(b, () =>
      dialog.getByRole('button', { name: 'Confirm acceptance' }).click(),
    )
    await expect(
      dialog.locator('[data-slot="handoff-respond-receipt"]'),
    ).toBeVisible()
    await expect(dialog.getByText('Responsibility accepted')).toBeVisible()
    await expect(dialog.locator('[data-slot="handoff-no-lease"]')).toBeVisible()
    await expect(
      dialog.locator('[data-slot="handoff-respond-receipt"]'),
    ).toContainText(accepted.result.ack_id ?? '')
    await i3Shot(b, 'i3-04-b-accept-receipt')
    expect(accepted.status).toBe(200)
    expect(accepted.ifMatch, 'acceptance uses detail.handoff.etag').toBe(
      found.detail.handoff.etag,
    )
    expect(accepted.key).toMatch(UUID_V7)
    expect(accepted.body).toEqual({ transition: 'accept' })
    expect(accepted.result.state).toBe('accepted')
    expect(accepted.result.replayed).toBe(false)
    expect(accepted.result.owner_epoch).toBe(2)
    expect(accepted.result.handoff_id).toBe(offer.result.handoff_id)
    expect(accepted.result.work_item_id).toBe(fixture.id)
    expect(accepted.result.ack_id ?? '').toMatch(UUID_ANY)
    expect(accepted.result.command_id).toMatch(UUID_ANY)
    expect(
      'resulting_lease_fence' in accepted.wire,
      'no resulting_lease_fence key on the wire',
    ).toBe(false)
    network.push({
      method: 'POST',
      route: `${I3_HANDOFFS}/{id}/responses`,
      status: accepted.status,
      outcome: accepted.result.state,
    })
    afterAccept = await readItemWitness(request, tokenA, fixture.id)
    expectVacantHumanOwned(afterAccept, userB, 2, 'after acceptance')
    expect(afterAccept.lease.version).toBe(before.lease.version)
    await b.keyboard.press('Escape')
    await expect(dialog).toBeHidden()

    // Restart 2: after acceptance. B rediscovers under the explicit Accepted filter.
    await restartEngine()
    i3.restarts.push({ after: 'acceptance', at: new Date().toISOString() })
    reauthenticated = await gotoSignedIn(b, '/communications/handoffs', B_EMAIL)
    const again = await discoverHandoff(b, fixture.id, 'accepted')
    expect(again.deliveryId).toBe(offer.result.delivery_id)
    expect(again.detail.handoff.id).toBe(offer.result.handoff_id)
    expect(again.detail.handoff.state).toBe('accepted')
    expect(again.detail.offer_context).toBe('terminal')
    expect(again.detail.handoff.etag).toBe(accepted.result.etag)
    rediscoveredEtag = again.detail.handoff.etag
    await expect(
      again.sheet.locator('[data-slot="handoff-offer-context"]'),
    ).toHaveText('Already resolved')
    await expect(
      again.sheet.locator('[data-slot="handoff-not-respondable"]'),
    ).toBeVisible()
    await expect(
      again.sheet.getByRole('button', { name: 'Accept responsibility' }),
    ).toHaveCount(0)
    await expect(
      again.sheet.getByRole('button', { name: 'Reject handoff' }),
    ).toHaveCount(0)
    await i3Shot(b, 'i3-05-b-accepted-after-second-restart')
    afterRestart = await readItemWitness(request, tokenA, fixture.id)
    expectVacantHumanOwned(afterRestart, userB, 2, 'after second restart')
    expect(afterRestart.etag).toBe(afterAccept.etag)
    expect(afterRestart.version).toBe(afterAccept.version)
    expect(afterRestart.lease.version).toBe(afterAccept.lease.version)

    // A separate reload: the deep link re-reads current state; nothing is resent.
    const reread = b.waitForResponse(
      (r) =>
        r.request().method() === 'GET' &&
        new URL(r.url()).pathname ===
          `${I3_DELIVERIES}/${again.deliveryId}/handoff`,
    )
    await b.reload()
    const rereadResponse = await reread
    expect(rereadResponse.status()).toBe(200)
    const reloaded = (await rereadResponse.json()) as HandoffDetail
    expect(reloaded.handoff.state).toBe('accepted')
    expect(reloaded.handoff.etag).toBe(accepted.result.etag)
    reloadedEtag = reloaded.handoff.etag
    const sheet = handoffSheet(b)
    await expect(sheet.locator('[data-slot="handoff-detail"]')).toBeVisible()
    await expect(
      sheet.locator('[data-slot="handoff-not-respondable"]'),
    ).toBeVisible()
    afterReload = await readItemWitness(request, tokenA, fixture.id)
    expectVacantHumanOwned(afterReload, userB, 2, 'after reload')
    expect(afterReload.etag).toBe(afterAccept.etag)
    responsePostsB = posts.count()
  } finally {
    await ctxB.close()
  }
  expect(responsePostsB, 'exactly one response POST across both restarts').toBe(
    1,
  )
  i3.test1 = {
    work_item_id: fixture.id,
    handoff_id: offer.result.handoff_id,
    delivery_id: offer.result.delivery_id,
    discovery_delivery_id_from_row: discoveryDelivery,
    offer: {
      status: offer.status,
      if_match: offer.ifMatch,
      expected_owner_epoch: offer.body.expected_owner_epoch,
      key_is_uuid_v7: UUID_V7.test(offer.key),
      key_equals_reviewed: offer.key === reviewedKey,
      state: offer.result.state,
      replayed: offer.result.replayed,
      handoff_etag: offer.result.etag,
      ack_deadline_utc: offer.body.ack_deadline,
    },
    acceptance: {
      status: accepted?.status,
      if_match: accepted?.ifMatch,
      state: accepted?.result.state,
      owner_epoch: accepted?.result.owner_epoch,
      ack_id: accepted?.result.ack_id,
      command_id: accepted?.result.command_id,
      handoff_etag: accepted?.result.etag,
      replayed: accepted?.result.replayed,
      resulting_lease_fence_present: accepted
        ? 'resulting_lease_fence' in accepted.wire
        : null,
    },
    witnesses: { before, afterOffer, afterAccept, afterRestart, afterReload },
    handoff_etag_by_phase: {
      discovered: discoveredEtag,
      rediscovered: rediscoveredEtag,
      reloaded: reloadedEtag,
    },
    reauthenticated_after_second_restart: reauthenticated,
    response_posts_from_b: responsePostsB,
    network,
  }
})

// ─── I3 · 2: keyboard rejection, exact replay, stale response ────────────────────
test('K3 I3: keyboard rejection preserves ownership and exact response replay has no new effect', async ({
  browser,
  request,
}) => {
  const fixture = i3Fixture('rejected')
  const summary = `K3 I3 rejected context ${STAMP}`
  const before = await readItemWitness(request, tokenA, fixture.id)
  expectVacantHumanOwned(before, userA, 1, 'before offer')

  let offer: I3ObservedOffer | null
  const ctxA = await browser.newContext()
  try {
    const a = await ctxA.newPage()
    const posts = observeRequests(a, isOfferPost)
    await uiLogin(a, A_EMAIL, MEMBER_PASSWORD)
    await a.goto('/work')
    await ensureWorkspace(a, 'Billing')
    const sheet = await openWorkItemSheet(a, fixture)
    const opened = await openOfferDialog(a, sheet, fixture.id)
    const reviewed = await fillAndReviewOffer(a, opened.dialog, {
      summary,
      nextAction: `Continue ${summary}`,
      deadlineLocal: localDateTimeValue(
        new Date(Date.now() + 30 * 60 * 60 * 1000),
      ),
    })
    offer = await confirmOffer(a, opened.dialog)
    await expect(
      opened.dialog.locator('[data-slot="handoff-offer-receipt"]'),
    ).toBeVisible()
    expect(posts.count()).toBe(1)
    expect(offer.status).toBe(201)
    expect(offer.ifMatch).toBe(opened.etag)
    expect(offer.key).toBe(reviewed.key)
    expect(offer.result.state).toBe('offered')
  } finally {
    await ctxA.close()
  }
  if (!offer) throw new Error('the offer was not observed')
  const afterOffer = await readItemWitness(request, tokenA, fixture.id)
  expectVacantHumanOwned(afterOffer, userA, 1, 'after offer')

  let rejected: I3ObservedResponse | null
  let detailEtag: string
  let reasonLabel: string
  let focusAfterClose: string
  let containment = 0
  let responsePostsB: number
  const ctxB = await browser.newContext()
  try {
    const b = await ctxB.newPage()
    const posts = observeRequests(b, isResponsePost)
    await uiLogin(b, B_EMAIL, MEMBER_PASSWORD)
    await b.goto('/communications/handoffs')
    const found = await discoverHandoff(b, fixture.id, 'offered')
    expect(found.deliveryId).toBe(offer.result.delivery_id)
    expect(found.detail.handoff.id).toBe(offer.result.handoff_id)
    expect(found.detail.offer_context).toBe('current')
    detailEtag = found.detail.handoff.etag

    // The response dialog, opened by keyboard from the real invoking control.
    await tabUntil(
      b,
      (f) => f.inDialog && f.text === 'Reject handoff',
      'the Reject handoff control',
    )
    await b.keyboard.press('Enter')
    const dialog = respondDialog(b, 'Reject handoff')
    await expect(dialog).toBeVisible()
    await expect(dialog).toContainText(fixture.id)
    // Focus stays inside the nested dialog across a full Tab cycle.
    for (let i = 0; i < 10; i++) {
      await b.keyboard.press('Tab')
      await expect(dialog.locator(':focus')).toHaveCount(1)
      containment++
    }
    // A rejection reason is required: reviewing without one is refused locally.
    const reason = dialog.getByRole('combobox', { name: 'Reason code' })
    await expect(reason).toHaveAttribute('aria-required', 'true')
    await tabUntil(
      b,
      (f) => f.inDialog && f.text === 'Review response',
      'Review',
    )
    await b.keyboard.press('Enter')
    const problems = dialog.getByTestId('respond-problems')
    await expect(problems).toBeVisible()
    await expect(problems).toContainText('A reason code is required.')
    await expect(reason).toHaveAttribute('aria-invalid', 'true')
    await expect(
      dialog.locator('[data-slot="handoff-respond-intent"]'),
    ).toHaveCount(0)
    expect(posts.count()).toBe(0)
    // Choose a preset by keyboard, then review and confirm by keyboard.
    await tabUntil(
      b,
      (f) => f.inDialog && f.ariaLabel === 'Reason code',
      'the reason combobox',
      { backwards: true },
    )
    await b.keyboard.press('Enter')
    const option = b.getByRole('option', {
      name: 'Outside my scope',
      exact: true,
    })
    await expect(option).toBeVisible()
    // The installed Radix Select moves REAL DOM focus between options — the module
    // contains no aria-activedescendant — and it does so from a setTimeout after each
    // navigation key, taking the next candidate relative to the item that currently
    // holds focus. Firing keys faster than focus settles therefore steps from a stale
    // anchor. Anchor with Home, then step one option at a time and observe each
    // transition, so every assertion is real focus rather than a guessed delay.
    const options = b.getByRole('option')
    const optionCount = await options.count()
    await b.keyboard.press('Home')
    await expect(options.first()).toBeFocused()
    for (let i = 1; i < optionCount; i++) {
      if (await option.evaluate((el) => el === document.activeElement)) break
      await b.keyboard.press('ArrowDown')
      await expect(options.nth(i)).toBeFocused()
    }
    await expect(option).toBeFocused()
    await b.keyboard.press('Enter')
    await expect(reason).toHaveText('Outside my scope')
    reasonLabel = 'Outside my scope'
    await tabUntil(
      b,
      (f) => f.inDialog && f.text === 'Review response',
      'Review',
    )
    await b.keyboard.press('Enter')
    const intent = dialog.locator('[data-slot="handoff-respond-intent"]')
    await expect(intent).toBeVisible()
    // Same scope correction as the accept case: the identity is the dialog's, the
    // transition, reason and ETag are the frozen coordinates'.
    await expect(dialog).toContainText(fixture.id)
    await expect(dialog.getByText(fixture.id, { exact: true })).toBeVisible()
    await expect(intent).toContainText('reject')
    await expect(intent).toContainText('outside_scope')
    await expect(intent).toContainText(detailEtag)
    await i3Shot(b, 'i3-06-b-reject-review')
    await tabUntil(
      b,
      (f) => f.inDialog && f.text === 'Confirm rejection',
      'Confirm rejection',
    )
    rejected = await observeResponse(b, () => b.keyboard.press('Enter'))
    await expect(
      dialog.locator('[data-slot="handoff-respond-receipt"]'),
    ).toBeVisible()
    await expect(dialog.getByText('Handoff rejected')).toBeVisible()
    await expect(dialog.locator('[data-slot="handoff-no-lease"]')).toHaveCount(
      0,
    )
    await i3Shot(b, 'i3-07-b-reject-receipt')
    expect(rejected.status).toBe(200)
    expect(rejected.method).toBe('POST')
    expect(rejected.pathname).toBe(
      `${I3_HANDOFFS}/${offer.result.handoff_id}/responses`,
    )
    expect(rejected.ifMatch).toBe(detailEtag)
    expect(rejected.key).toMatch(UUID_V7)
    expect(rejected.body.transition).toBe('reject')
    expect(rejected.body.reason?.code).toBe('outside_scope')
    expect(rejected.result.state).toBe('rejected')
    expect(rejected.result.replayed).toBe(false)
    expect(rejected.result.owner_epoch).toBe(1)
    expect('ack_id' in rejected.wire, 'no Ack on a rejection').toBe(false)
    expect(
      'resulting_lease_fence' in rejected.wire,
      'no resulting_lease_fence key on the wire',
    ).toBe(false)
    expect(posts.count()).toBe(1)

    // Closing restores focus: to the invoking control if still mounted, or to the
    // sheet's own fallback (Re-read) once the terminal re-read removed it.
    await b.keyboard.press('Escape')
    await expect(dialog).toBeHidden()
    await expect
      .poll(async () => (await activeElement(b)).text)
      .toMatch(/^(Reject handoff|Re-read)$/)
    focusAfterClose = (await activeElement(b)).text
    const sheet = handoffSheet(b)
    await expect(
      sheet.locator('[data-slot="handoff-not-respondable"]'),
    ).toBeVisible()
    await expect(
      sheet.locator('[data-slot="handoff-terminal-reason"]'),
    ).toContainText('outside_scope')
    responsePostsB = posts.count()
  } finally {
    await ctxB.close()
  }
  if (!rejected) throw new Error('the rejection was not observed')
  const afterReject = await readItemWitness(request, tokenA, fixture.id)
  expectVacantHumanOwned(afterReject, userA, 1, 'after rejection')
  // The apply path advances the item's event sequence once before it branches, and
  // the work_item:cas write carries the row with it. A rejection therefore leaves
  // owner, epoch, lease and fence untouched — asserted above — while the row itself
  // moves by exactly one. Assert that step, not a frozen row.
  expect(afterReject.version).toBe(afterOffer.version + 1)
  expect(afterReject.last_event_seq).toBe(afterOffer.last_event_seq + 1)
  expect(afterReject.etag).toBe(`"v${afterReject.version}"`)
  expect(afterReject.lease.version).toBe(before.lease.version)
  const detailAfterReject = await apiRead(
    request,
    tokenB,
    `${I3_DELIVERIES}/${offer.result.delivery_id}/handoff`,
  )
  expect(detailAfterReject.status).toBe(200)
  const rejectedDetail = detailAfterReject.json as HandoffDetail
  expect(rejectedDetail.handoff.state).toBe('rejected')
  expect(rejectedDetail.offer_context).toBe('terminal')
  expect(rejectedDetail.terminal_reason?.code).toBe('outside_scope')
  expect(rejectedDetail.handoff.etag).toBe(rejected.result.etag)

  // HTTP CONTROL 1 (labelled replay): the same method, path, bytes, If-Match and
  // key the UI dispatched. The engine answers from its ledger; nothing changes.
  const responsesRoute = `${I3_HANDOFFS}/${offer.result.handoff_id}/responses`
  expect(rejected.pathname).toBe(responsesRoute)
  const replay = await apiRaw(
    request,
    tokenB,
    responsesRoute,
    rejected.postData,
    {
      'If-Match': rejected.ifMatch,
      'Idempotency-Key': rejected.key,
    },
  )
  expect(replay.status).toBe(200)
  const replayed = replay.json as HandoffResponseResult
  expect(replayed.replayed).toBe(true)
  expect(replayed.command_id).toBe(rejected.result.command_id)
  expect(replayed.handoff_id).toBe(rejected.result.handoff_id)
  expect(replayed.version).toBe(rejected.result.version)
  expect(replayed.owner_epoch).toBe(rejected.result.owner_epoch)
  expect(replayed.state).toBe('rejected')
  expect('ack_id' in (replay.json as Record<string, unknown>)).toBe(false)
  expect(
    'resulting_lease_fence' in (replay.json as Record<string, unknown>),
  ).toBe(false)
  const afterReplay = await readItemWitness(request, tokenA, fixture.id)
  expectVacantHumanOwned(afterReplay, userA, 1, 'after replay')
  // A replay is answered from the ledger: the row does not move again.
  expect(afterReplay.version).toBe(afterReject.version)
  expect(afterReplay.last_event_seq).toBe(afterReject.last_event_seq)
  expect(afterReplay.etag).toBe(afterReject.etag)
  const detailAfterReplay = await apiRead(
    request,
    tokenB,
    `${I3_DELIVERIES}/${offer.result.delivery_id}/handoff`,
  )
  expect((detailAfterReplay.json as HandoffDetail).handoff.etag).toBe(
    rejectedDetail.handoff.etag,
  )

  // HTTP CONTROL 2 (labelled stale): a NEW key with the OLD precondition. The
  // frozen route refuses it as the code-less 409 conflict class; no effect.
  const stale = await apiRaw(
    request,
    tokenB,
    responsesRoute,
    rejected.postData,
    {
      'If-Match': rejected.ifMatch,
      'Idempotency-Key': uuidv7(),
    },
  )
  expect(stale.status).toBe(409)
  expect(i3ErrorCode(stale.json)).toBe('conflict')
  const afterStale = await readItemWitness(request, tokenA, fixture.id)
  expectVacantHumanOwned(afterStale, userA, 1, 'after stale control')
  // A refused request applies no effect: the row is exactly where the rejection left it.
  expect(afterStale.version).toBe(afterReject.version)
  expect(afterStale.last_event_seq).toBe(afterReject.last_event_seq)
  expect(afterStale.etag).toBe(afterReject.etag)
  const detailAfterStale = await apiRead(
    request,
    tokenB,
    `${I3_DELIVERIES}/${offer.result.delivery_id}/handoff`,
  )
  expect((detailAfterStale.json as HandoffDetail).handoff.etag).toBe(
    rejectedDetail.handoff.etag,
  )
  i3.test2 = {
    work_item_id: fixture.id,
    handoff_id: offer.result.handoff_id,
    delivery_id: offer.result.delivery_id,
    offer: { status: offer.status, state: offer.result.state },
    ui_rejection: {
      status: rejected.status,
      if_match: rejected.ifMatch,
      key_is_uuid_v7: UUID_V7.test(rejected.key),
      reason_code: rejected.body.reason?.code,
      reason_label: reasonLabel,
      state: rejected.result.state,
      owner_epoch: rejected.result.owner_epoch,
      ack_present: 'ack_id' in rejected.wire,
      resulting_lease_fence_present: 'resulting_lease_fence' in rejected.wire,
      handoff_etag: rejected.result.etag,
      tab_cycle_contained_steps: containment,
      focus_after_close: focusAfterClose,
    },
    replay_control: {
      label:
        'HTTP replay: same method/path/bytes/If-Match/key as the UI dispatch',
      status: replay.status,
      replayed: replayed.replayed,
      same_command_id: replayed.command_id === rejected.result.command_id,
      same_version: replayed.version === rejected.result.version,
    },
    stale_control: {
      label: 'HTTP stale: new key, old If-Match',
      status: stale.status,
      code: i3ErrorCode(stale.json),
    },
    witnesses: { before, afterOffer, afterReject, afterReplay, afterStale },
    response_posts_from_b: responsePostsB,
    network: [
      {
        method: 'POST',
        route: I3_HANDOFFS,
        status: offer.status,
        outcome: 'offered',
      },
      {
        method: 'POST',
        route: `${I3_HANDOFFS}/{id}/responses`,
        status: rejected.status,
        outcome: 'rejected (UI)',
      },
      {
        method: 'POST',
        route: `${I3_HANDOFFS}/{id}/responses`,
        status: replay.status,
        outcome: 'replayed (HTTP control)',
      },
      {
        method: 'POST',
        route: `${I3_HANDOFFS}/{id}/responses`,
        status: stale.status,
        outcome: 'conflict (HTTP control)',
      },
    ] satisfies I3NetworkSummary[],
  }
})

// ─── I3 · 3: context changes and a lost offer response ───────────────────────────
test('K3 I3: context changes and reload preserve honest handling of a lost offer response', async ({
  browser,
  request,
}) => {
  const fixture = i3Fixture('lost')
  const summary = `K3 I3 lost-response context ${STAMP}`
  const before = await readItemWitness(request, tokenA, fixture.id)
  expectVacantHumanOwned(before, userA, 1, 'before offer')

  interface ForwardedOffer {
    status: number
    etag: string | null
    result: HandoffOfferResult
  }
  let forwarded: ForwardedOffer | null = null
  let offerPosts: string[]
  let postsAtUncertain: number
  let postsAfterReload: number
  let postsAtEnd: number
  let reviewedKey: string
  let contextEtagBack: string
  const ctxA = await browser.newContext()
  try {
    const a = await ctxA.newPage()
    const posts = observeRequests(a, isOfferPost)
    await uiLogin(a, A_EMAIL, MEMBER_PASSWORD)
    await a.goto('/work')
    await ensureWorkspace(a, 'Billing')
    let sheet = await openWorkItemSheet(a, fixture)
    const first = await openOfferDialog(a, sheet, fixture.id)
    await first.dialog.getByLabel('Summary', { exact: true }).fill(summary)
    await expect(
      first.dialog.getByLabel('Summary', { exact: true }),
    ).toHaveValue(summary)

    // The offer dialog and the item sheet are MODAL overlays, so the real
    // workspace switcher is reachable only once they are hidden. Hiding the panel
    // keeps the draft in the still-mounted, context-keyed host; the context move
    // below is what destroys it.
    await a.keyboard.press('Escape')
    await expect(first.dialog).toBeHidden()
    await a.keyboard.press('Escape')
    await expect(sheet).toBeHidden()

    // Billing → Default while the protected draft exists: the context-keyed host
    // is torn down, so the draft is not active in the wrong context.
    await selectWorkspace(a, 'Default')
    await expect(offerDialog(a)).toHaveCount(0)
    await expect(
      a.locator('[data-slot="handoff-retained-operation"]'),
    ).toHaveCount(0)
    sheet = await openWorkItemSheet(a, fixture)
    const underDefault = await openOfferDialog(a, sheet, fixture.id)
    await expect(underDefault.dialog).not.toContainText(summary)
    await expect(
      underDefault.dialog.locator(
        '[data-slot="handoff-offer-workspace-mismatch"]',
      ),
    ).toBeVisible()
    await expect(
      underDefault.dialog.getByRole('button', { name: 'Review offer' }),
    ).toBeDisabled()
    await expect(
      underDefault.dialog.locator('[data-slot="handoff-offer-form"]'),
    ).toHaveCount(0)
    await expect(
      underDefault.dialog.locator('[data-slot="handoff-offer-intent"]'),
    ).toHaveCount(0)
    await i3Shot(a, 'i3-08-a-offer-under-default-refused')
    expect(posts.count(), 'no mutation under the wrong workspace').toBe(0)
    await a.keyboard.press('Escape')
    await expect(underDefault.dialog).toBeHidden()
    await a.keyboard.press('Escape')
    await expect(sheet).toBeHidden()

    // Default → Billing: a fresh current read, an empty draft.
    await selectWorkspace(a, 'Billing')
    await expect(offerDialog(a)).toHaveCount(0)
    sheet = await openWorkItemSheet(a, fixture)
    const back = await openOfferDialog(a, sheet, fixture.id)
    contextEtagBack = back.etag
    expect(back.etag).toBe(before.etag)
    await expect(
      back.dialog.getByLabel('Summary', { exact: true }),
    ).toHaveValue('')
    await expect(
      back.dialog.locator('[data-slot="handoff-offer-workspace-mismatch"]'),
    ).toHaveCount(0)
    expect(posts.count()).toBe(0)

    // THE NAMED FAULT: one route handler forwards the one matching offer to the
    // real engine, retains its response privately, and discards only that response.
    const matcher = (url: URL) => url.pathname === I3_HANDOFFS
    const handler = async (route: Route) => {
      if (route.request().method() !== 'POST' || forwarded !== null) {
        await route.continue()
        return
      }
      const response = await route.fetch()
      forwarded = {
        status: response.status(),
        etag: response.headers()['etag'] ?? null,
        result: (await response.json()) as HandoffOfferResult,
      }
      await route.abort('failed')
    }
    await a.route(matcher, handler)
    try {
      const reviewed = await fillAndReviewOffer(a, back.dialog, {
        summary,
        nextAction: `Continue ${summary}`,
        deadlineLocal: localDateTimeValue(
          new Date(Date.now() + 30 * 60 * 60 * 1000),
        ),
      })
      reviewedKey = reviewed.key
      expect(reviewed.precondition).toBe(back.etag)
      const dispatched = a.waitForRequest(
        (r) =>
          r.method() === 'POST' && new URL(r.url()).pathname === I3_HANDOFFS,
      )
      await back.dialog
        .getByRole('button', { name: 'Confirm and offer' })
        .click()
      const request1 = await dispatched
      expect(request1.headers()['idempotency-key']).toBe(reviewed.key)
      expect(request1.headers()['if-match']).toBe(back.etag)
      // Client uncertainty, stated truthfully; no receipt, no automatic retry.
      await expect(
        back.dialog.locator('[data-slot="handoff-offer-uncertain"]'),
      ).toBeVisible()
      await expect(
        back.dialog.getByText('The result is not known'),
      ).toBeVisible()
      await expect(
        back.dialog.getByRole('button', { name: 'Retry with the same key' }),
      ).toBeVisible()
      await expect(
        back.dialog.locator('[data-slot="handoff-offer-receipt"]'),
      ).toHaveCount(0)
      await i3Shot(a, 'i3-09-a-lost-response-uncertain')
      postsAtUncertain = posts.count()
      expect(postsAtUncertain, 'one POST, no automatic second').toBe(1)
    } finally {
      await a.unroute(matcher, handler)
    }
    // Server receipt: the engine applied the offer whose response was dropped.
    expect(
      forwarded,
      'the one forwarded offer was answered by the engine',
    ).not.toBeNull()
    const server = forwarded as ForwardedOffer | null
    if (!server) throw new Error('unreachable')
    expect(server.status).toBe(201)
    expect(server.result.state).toBe('offered')
    expect(server.result.work_item_id).toBe(fixture.id)

    // Reload: local tracking ends; nothing is invented and nothing is resent.
    const listed = a.waitForResponse(
      (r) =>
        r.request().method() === 'GET' &&
        new URL(r.url()).pathname === I3_WORK_ITEMS,
    )
    await a.reload()
    expect((await listed).status()).toBe(200)
    postsAfterReload = posts.count()
    expect(
      postsAfterReload,
      'no POST between the uncertain state and the reload',
    ).toBe(1)
    await expect(a.getByRole('dialog')).toHaveCount(0)
    await expect(
      a.locator('[data-slot="handoff-retained-operation"]'),
    ).toHaveCount(0)
    await expect(a.locator('[data-slot="handoff-offer-receipt"]')).toHaveCount(
      0,
    )
    sheet = await openWorkItemSheet(a, fixture)
    const after = await openOfferDialog(a, sheet, fixture.id)
    await expect(
      after.dialog.locator('[data-slot="handoff-offer-form"]'),
    ).toBeVisible()
    await expect(
      after.dialog.locator('[data-slot="handoff-offer-intent"]'),
    ).toHaveCount(0)
    await expect(
      after.dialog.locator('[data-slot="handoff-offer-uncertain"]'),
    ).toHaveCount(0)
    await expect(
      after.dialog.getByRole('button', { name: 'Retry with the same key' }),
    ).toHaveCount(0)
    await expect(
      after.dialog.locator('[data-slot="handoff-offer-receipt"]'),
    ).toHaveCount(0)
    await expect(after.dialog).not.toContainText(reviewedKey)
    await i3Shot(a, 'i3-10-a-after-reload-no-retained-intent')
    postsAtEnd = posts.count()
    expect(postsAtEnd).toBe(1)
    offerPosts = posts.seen()
  } finally {
    await ctxA.close()
  }
  const server = forwarded as ForwardedOffer | null
  if (!server) throw new Error('the forwarded offer was not retained')

  // Post-reload observation through B's own permitted list/detail and the item.
  const list = await apiRead(
    request,
    tokenB,
    `${I3_HANDOFF_INBOX}?workspace_id=${wsBilling}&state=offered&limit=50`,
  )
  expect(list.status).toBe(200)
  const rows = (list.json as HandoffInboxPage).items.filter(
    (i) => i.work_item.id === fixture.id,
  )
  expect(
    rows,
    "exactly one offer for the item in B's offered page",
  ).toHaveLength(1)
  const row = rows[0]
  expect(row.carrier.delivery_id).toBe(server.result.delivery_id)
  expect(row.handoff.id).toBe(server.result.handoff_id)
  expect(row.handoff.state).toBe('offered')
  const detail = await apiRead(
    request,
    tokenB,
    `${I3_DELIVERIES}/${row.carrier.delivery_id}/handoff`,
  )
  expect(detail.status).toBe(200)
  const lostDetail = detail.json as HandoffDetail
  expect(lostDetail.handoff.state).toBe('offered')
  expect(lostDetail.offer_context).toBe('current')
  expect(lostDetail.content.summary).toBe(summary)
  const afterLost = await readItemWitness(request, tokenA, fixture.id)
  expectVacantHumanOwned(afterLost, userA, 1, 'after the lost response')
  expect(afterLost.lease.version).toBe(before.lease.version)
  i3.test3 = {
    work_item_id: fixture.id,
    context_movement: {
      sequence: ['Billing', 'Default', 'Billing'],
      draft_active_under_default: false,
      posts_under_default: 0,
      fresh_read_etag_on_return: contextEtagBack,
    },
    server_receipt: {
      label:
        'server receipt (forwarded request, response dropped by the harness)',
      status: server.status,
      state: server.result.state,
      handoff_id: server.result.handoff_id,
      delivery_id: server.result.delivery_id,
      handoff_etag: server.etag ?? server.result.etag,
    },
    client_uncertainty: {
      label: 'client uncertainty (no receipt, no automatic retry)',
      posts_when_uncertain: postsAtUncertain,
      posts_after_reload: postsAfterReload,
      posts_at_end: postsAtEnd,
      offer_posts: offerPosts,
    },
    post_reload_observation: {
      label: "post-reload observation (B's own list/detail and item witnesses)",
      b_row_delivery_id: row.carrier.delivery_id,
      b_detail_state: lostDetail.handoff.state,
      b_detail_context: lostDetail.offer_context,
      item: afterLost,
    },
    witnesses: { before, afterLost },
    network: [
      {
        method: 'POST',
        route: I3_HANDOFFS,
        status: server.status,
        outcome: 'offered; response dropped to the browser (named fault)',
      },
    ] satisfies I3NetworkSummary[],
  }
})

// ─── I3 · 4: keyboard, narrow layout, themes, seven locales, capture code ────────
test('K3 I3: handoff views support keyboard focus, narrow layout, themes and seven locales', async ({
  browser,
}) => {
  const offered = i3Fixture('lost')
  const accepted = i3Fixture('offered')
  const rejected = i3Fixture('rejected')
  const wideViewport = { width: 1440, height: 1000 }
  const narrowViewport = { width: 390, height: 844 }
  const locales: Record<string, unknown>[] = []
  const captures: Record<string, unknown>[] = []
  const sheetActions = [
    'Close',
    'Re-read',
    'Reject handoff',
    'Accept responsibility',
  ]
  const dialogActions = ['Close', 'Review response']
  const geometry: Record<string, unknown> = {}
  let responsePosts: number
  let narrowResponsePosts: number

  const wide = await browser.newContext({
    viewport: wideViewport,
    deviceScaleFactor: 2,
  })
  try {
    const b = await wide.newPage()
    const posts = observeRequests(b, isResponsePost)
    await uiLogin(b, B_EMAIL, MEMBER_PASSWORD)
    await b.goto('/communications/handoffs')
    await ensureWorkspace(b, 'Billing')
    // The labelled door and tab.
    await expect(
      b.getByRole('heading', { level: 1, name: 'Handoffs', exact: true }),
    ).toBeVisible()
    const door = b.getByRole('link', { name: 'Handoffs', exact: true }).first()
    await expect(door).toBeVisible()
    await expect(door).toHaveAttribute('href', '/communications/handoffs')
    await expect(
      b.getByRole('tab', { name: 'Handoffs', selected: true }),
    ).toBeVisible()

    // Actual current Accepted and Rejected detail views from the fixtures.
    const acceptedView = await discoverHandoff(b, accepted.id, 'accepted')
    expect(acceptedView.detail.handoff.state).toBe('accepted')
    await expect(
      acceptedView.sheet.locator('[data-slot="handoff-not-respondable"]'),
    ).toBeVisible()
    await b.keyboard.press('Escape')
    await expect(acceptedView.sheet).toBeHidden()
    const rejectedView = await discoverHandoff(b, rejected.id, 'rejected')
    expect(rejectedView.detail.handoff.state).toBe('rejected')
    await expect(
      rejectedView.sheet.locator('[data-slot="handoff-terminal-reason"]'),
    ).toBeVisible()
    await b.keyboard.press('Escape')
    await expect(rejectedView.sheet).toBeHidden()

    // The Offered view, opened by keyboard: grid → first cell → Enter.
    await b.goto('/communications/handoffs')
    await ensureWorkspace(b, 'Billing')
    const row = b.getByRole('row').filter({ hasText: offered.id })
    await expect(row).toHaveCount(1)
    await tabUntil(b, (f) => f.role === 'grid', 'the handoff grid', {
      max: 200,
    })
    await b.keyboard.press('ArrowDown')
    await expect
      .poll(async () => (await activeElement(b)).role)
      .toBe('gridcell')
    const detailRead = b.waitForResponse(
      (r) =>
        r.request().method() === 'GET' &&
        /\/v1\/m\/sessions\/deliveries\/[0-9a-f-]{36}\/handoff$/.test(
          new URL(r.url()).pathname,
        ),
    )
    await b.keyboard.press('Enter')
    const sheet = handoffSheet(b)
    await expect(sheet).toBeVisible()
    const titleAtOpen = await expectTitleHoldsFocus(
      sheet,
      wideViewport,
      'at the initial open',
    )
    expect((await detailRead).status()).toBe(200)
    await expect(sheet.locator('[data-slot="handoff-detail"]')).toBeVisible()
    await expect(sheet.locator('[data-slot="handoff-detail"]')).toContainText(
      offered.id,
    )
    // The loaded body renders below the title, so the focused element has not moved.
    const titleAfterRead = await expectTitleHoldsFocus(
      sheet,
      wideViewport,
      'after the detail loaded',
    )
    geometry.wide_initial_focus = {
      at_open: titleAtOpen,
      after_read: titleAfterRead,
    }
    // Natural-view geometry first: read before any Tab, scroll or click.
    const wideSheet = await captureNaturalGeometry(
      sheet,
      wideViewport,
      sheetActions,
    )
    expectSheetGeometry(wideSheet, wideViewport)
    geometry.wide_sheet = wideSheet
    // Containment, then deliberate keyboard reachability of each action.
    await expect(sheet.locator(':focus')).toHaveCount(1)
    for (let i = 0; i < 10; i++) {
      await b.keyboard.press('Tab')
      await expect(sheet.locator(':focus')).toHaveCount(1)
    }
    for (const name of sheetActions)
      await expectReachableByTab(b, sheet, wideViewport, name)
    // Nested dialog by keyboard: natural geometry, containment, reachability,
    // and closing restores the opener.
    await tabUntil(
      b,
      (f) => f.inDialog && f.text === 'Reject handoff',
      'Reject handoff',
    )
    await b.keyboard.press('Enter')
    const nested = respondDialog(b, 'Reject handoff')
    await expect(nested).toBeVisible()
    await expect(
      nested.locator('[data-slot="handoff-reject-form"]'),
    ).toBeVisible()
    const wideDialog = await captureNaturalGeometry(
      nested,
      wideViewport,
      dialogActions,
    )
    expectDialogGeometry(wideDialog, wideViewport)
    geometry.wide_dialog = wideDialog
    for (let i = 0; i < 6; i++) {
      await b.keyboard.press('Tab')
      await expect(nested.locator(':focus')).toHaveCount(1)
    }
    for (const name of dialogActions)
      await expectReachableByTab(b, nested, wideViewport, name)
    await b.keyboard.press('Escape')
    await expect(nested).toBeHidden()
    await expect
      .poll(async () => (await activeElement(b)).text)
      .toBe('Reject handoff')
    // Closing the sheet returns focus to its origin in the grid.
    await b.keyboard.press('Escape')
    await expect(sheet).toBeHidden()
    await expect
      .poll(async () => (await activeElement(b)).role)
      .toBe('gridcell')
    expect(posts.count(), 'no response was dispatched').toBe(0)

    // Light and dark through the product's own control, and the two real English
    // captures root will inspect (private evidence directory; nothing is copied
    // into the product tree here).
    for (const theme of ['light', 'dark'] as const) {
      await setTheme(b, theme)
      await expect(row).toHaveCount(1)
      const file = path.join(
        evidenceDir(),
        `communications-handoffs-${theme}.png`,
      )
      await b.screenshot({ path: file, fullPage: false })
      captures.push({
        file: path.basename(file),
        route: '/communications/handoffs',
        viewport: wideViewport,
        device_scale_factor: 2,
        theme,
        locale: 'en',
        workspace: 'Billing',
        populated_with: offered.id,
      })
    }
    await setTheme(b, 'light')

    // Seven locales through the persisted language preference; the actual
    // translated H1 and tab, no raw key presentation.
    for (const lng of I3_LOCALES) {
      await b.evaluate(
        (code) => localStorage.setItem('olivares.lang', code),
        lng,
      )
      await b.reload()
      const strings = handoffLocaleStrings(lng)
      const title = b.getByRole('heading', {
        level: 1,
        name: strings.title,
        exact: true,
      })
      await expect(title).toBeVisible()
      await expect(
        b.getByRole('tab', { name: strings.tab, exact: true, selected: true }),
      ).toBeVisible()
      await expect(b.locator('html')).toHaveAttribute('lang', lng)
      const body = b.locator('body')
      await expect(body).not.toContainText('doors.handoffs')
      await expect(body).not.toContainText('tabs.handoffs')
      await expect(body).not.toContainText('handoff.inbox.')
      expect(strings.title).not.toBe('doors.handoffs.title')
      locales.push({
        lng,
        h1: strings.title,
        tab: strings.tab,
        html_lang: lng,
      })
    }
    await b.evaluate(() => localStorage.setItem('olivares.lang', 'en'))
    responsePosts = posts.count()
  } finally {
    await wide.close()
  }

  // Narrow layout: the same door, tab, detail and actions inside 390×844.
  const narrow = await browser.newContext({
    viewport: narrowViewport,
    isMobile: true,
    hasTouch: true,
  })
  try {
    const m = await narrow.newPage()
    await uiLogin(m, B_EMAIL, MEMBER_PASSWORD)
    await m.goto('/communications/handoffs')
    await ensureWorkspace(m, 'Billing')
    await expect(
      m.getByRole('heading', { level: 1, name: 'Handoffs', exact: true }),
    ).toBeVisible()
    const narrowPosts = observeRequests(m, isResponsePost)
    const tab = m.getByRole('tab', { name: 'Handoffs', selected: true })
    await expect(tab).toBeVisible()
    // The door's tab in its natural position: no scroll before the measurement.
    const tabBox = await tab.boundingBox()
    expect(tabBox, 'the Handoffs tab has a natural box').not.toBeNull()
    if (!tabBox) throw new Error('unreachable')
    expect(
      inside(tabBox, viewportRect(narrowViewport)),
      'the Handoffs tab is naturally inside 390×844',
    ).toBe(true)
    geometry.narrow_tab = tabBox
    const found = await discoverHandoff(m, offered.id, 'offered')
    // At 390x844 the loaded body pushes every footer action past the fold, which is
    // exactly where Radix's first-tabbable default used to strand the focus.
    geometry.narrow_initial_focus = await expectTitleHoldsFocus(
      found.sheet,
      narrowViewport,
      'after the detail loaded at 390x844',
    )
    const narrowSheet = await captureNaturalGeometry(
      found.sheet,
      narrowViewport,
      sheetActions,
    )
    expectSheetGeometry(narrowSheet, narrowViewport)
    geometry.narrow_sheet = narrowSheet
    await i3Shot(m, 'i3-11-b-handoff-narrow')
    // Deliberate keyboard reachability at 390×844, then the nested dialog.
    await expect(found.sheet.locator(':focus')).toHaveCount(1)
    for (const name of sheetActions)
      await expectReachableByTab(m, found.sheet, narrowViewport, name)
    await tabUntil(
      m,
      (f) => f.inDialog && f.text === 'Reject handoff',
      'Reject handoff',
    )
    await m.keyboard.press('Enter')
    const narrowNested = respondDialog(m, 'Reject handoff')
    await expect(narrowNested).toBeVisible()
    await expect(
      narrowNested.locator('[data-slot="handoff-reject-form"]'),
    ).toBeVisible()
    const narrowDialog = await captureNaturalGeometry(
      narrowNested,
      narrowViewport,
      dialogActions,
    )
    expectDialogGeometry(narrowDialog, narrowViewport)
    geometry.narrow_dialog = narrowDialog
    for (const name of dialogActions)
      await expectReachableByTab(m, narrowNested, narrowViewport, name)
    await m.keyboard.press('Escape')
    await expect(narrowNested).toBeHidden()
    await expect
      .poll(async () => (await activeElement(m)).text)
      .toBe('Reject handoff')
    await m.keyboard.press('Escape')
    await expect(found.sheet).toBeHidden()
    narrowResponsePosts = narrowPosts.count()
  } finally {
    await narrow.close()
  }
  expect(narrowResponsePosts, 'no response was dispatched at 390×844').toBe(0)
  i3.test4 = {
    views: {
      accepted: accepted.id,
      rejected: rejected.id,
      offered: offered.id,
    },
    viewports: [wideViewport, narrowViewport],
    keyboard: {
      open: 'Tab to grid, ArrowDown, Enter',
      nested_open: 'Tab to Reject handoff, Enter',
      focus_after_nested_close: 'Reject handoff',
      focus_after_sheet_close: 'gridcell',
    },
    geometry,
    geometry_rule: {
      sheet:
        'panel is the vertical scroll container; horizontal overflow and off-panel actions prohibited; natural viewport presence recorded',
      dialog:
        'body is the only scroll region; every named action naturally in the viewport and panel',
      reachability:
        'Tab to each action; focused action fully in viewport and unobscured at its center',
      initial_focus:
        'the sheet title holds focus at the open and after the detail loads, in the viewport and unobscured',
    },
    themes: ['light', 'dark'],
    captures,
    locales,
    response_posts: responsePosts,
    narrow_response_posts: narrowResponsePosts,
  }
})
