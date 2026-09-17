// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// K3 I2 — CHANNEL ADMINISTRATION AND THE PERSONAL SEEN CURSOR IN THE BROWSER,
// against a live engine booted by the same harness as the I1 journey
// (global-setup.ts: custody, `--seed-demo`, the activation ceremony, second boot).
//
// Actors, all created through the real users and memberships routes by the demo
// superadmin (which only provisions — it is not a directory principal of any
// workspace): A (owner, full local grants on the channels it creates), D (role
// admin, therefore core `sessions:channel:admin` by the built-in verb tier, holding
// ONLY a local admin bit on the channel — no read bit), B (editor, recipient of the
// cursor journey) and E/F (editors, subjects of the grant acts).
//
// NOTHING on the accepted path is intercepted. Every assertion about state is made
// against the engine over HTTP, with tokens that never leave this process; every
// screenshot shows ids, versions and ETags the engine returned, never a bearer,
// never a cursor token, never a continuation, never an intention key.
import {
  expect,
  test,
  type APIRequestContext,
  type Locator,
  type Page,
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
import { engineEnv } from './global-setup'

const BASE = process.env.K3_E2E_BASE ?? ''
const TENANT = process.env.DEMO_TENANT ?? ''
const WORK = process.env.K3_E2E_WORK ?? ''
const DEMO_EMAIL = 'demo@olivares.local'
const DEMO_PASSWORD = 'olivares-demo-estate'
const A_EMAIL = 'k3-i2-a@olivares.local'
const B_EMAIL = 'k3-i2-b@olivares.local'
const D_EMAIL = 'k3-i2-d@olivares.local'
const E_EMAIL = 'k3-i2-e@olivares.local'
const F_EMAIL = 'k3-i2-f@olivares.local'
/** G1-B: a real VIEWER. Its role grants no `sessions:channel:admin` at all, so every
 *  administrative answer it gets comes from an authored, workspace-scoped policy plus a
 *  local ADMIN bit — the authority whoami cannot express. */
const G_EMAIL = 'k3-i2-g@olivares.local'
const MEMBER_PASSWORD = 'k3-i2-member-passphrase-42'
const STAMP = Date.now().toString(36)
/** More than the 256-row bound the grant writer used to have: the history of ONE
 * subject on the administrable channel is driven past it through the real routes. */
const HISTORY_GENERATIONS = 260
/** Two inbox pages at the smallest page size the console offers (25). */
const CURSOR_NOTICES = 26

test.describe.configure({ mode: 'serial' })

let tokenAdmin = ''
let tokenA = ''
let tokenB = ''
let tokenD = ''
let userA = ''
let userB = ''
let userD = ''
let userE = ''
let userF = ''
let userG = ''
let tokenG = ''
let wsBilling = ''
let adminChannelId = ''
let cursorChannelId = ''
let grantIdD = ''
const adminSlug = `k3-admin-${STAMP}`
const cursorSlug = `k3-cursor-${STAMP}`
const evidence: Record<string, unknown> = {}
const httpStatuses: Record<string, number[]> = {}

function evidenceDir(): string {
  const dir = path.join(WORK, 'evidence-i2')
  mkdirSync(dir, { recursive: true })
  return dir
}
async function shot(page: Page, name: string) {
  const file = path.join(evidenceDir(), `${name}.png`)
  await page.screenshot({ path: file, fullPage: true })
  const pub = path.join('playwright-report', 'k3-i2')
  mkdirSync(pub, { recursive: true })
  copyFileSync(file, path.join(pub, `${name}.png`))
}
function note(key: string, status: number) {
  ;(httpStatuses[key] ??= []).push(status)
}

/**
 * The fixtures provision 520 write acts to drive ONE subject's grant history past
 * the writer's historical 256-row bound, and the engine's inbound rate limiter
 * (per tenant, per endpoint class) answers 429 with a normative `Retry-After` long
 * before that. The harness HONOURS that header and retries; it never widens a
 * limit, and the count is published in the evidence so the provisioning cost is
 * visible. This is the harness being a well-behaved client — the console's own
 * 429 handling is a product path and is measured separately.
 */
let rateLimitedRetries = 0
async function api(
  request: APIRequestContext,
  token: string,
  method: 'GET' | 'POST' | 'PATCH' | 'PUT',
  route: string,
  body?: unknown,
  headers: Record<string, string> = {},
): Promise<{ status: number; json: unknown; headers: Record<string, string> }> {
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
  if (res.status() === 429 && rateLimitedRetries < 4000) {
    rateLimitedRetries++
    const after = Number(res.headers()['retry-after'] ?? '1')
    await new Promise((r) =>
      setTimeout(
        r,
        (Number.isFinite(after) ? Math.max(after, 1) : 1) * 1000 + 50,
      ),
    )
    return api(request, token, method, route, body, headers)
  }
  return { status: res.status(), json, headers: res.headers() }
}

/** A GET that tolerates the engine's TRANSIENT "could not look" (503) on this
 * single-writer SQLite estate; every retry is counted in the evidence. */
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

async function selectWorkspace(page: Page, name: string) {
  const trigger = page
    .getByRole('button', {
      name: /All workspaces|Billing|Default|Workspace not in list/i,
    })
    .first()
  await trigger.click()
  await page
    .getByRole('menuitem', { name: new RegExp(`^${name}`, 'i') })
    .first()
    .click()
}

/** The channel's current precondition ETag, read as an ADMIN through the grant
 * history (never through the read-tier channel read). */
async function adminEtag(
  request: APIRequestContext,
  token: string,
  channelId: string,
): Promise<string> {
  const r = await apiRead(
    request,
    token,
    `/v1/m/sessions/channels/${channelId}/grants?workspace_id=${wsBilling}&limit=1`,
  )
  expect(r.status, 'admin grant read').toBe(200)
  return (r.json as { etag: string }).etag
}

async function restartEngine() {
  const pidFile = path.join(WORK, 'engine.pid')
  const pid = Number(readFileSync(pidFile, 'utf8').trim())
  process.kill(pid, 'SIGTERM')
  for (let i = 0; i < 100 && existsSync(`/proc/${pid}`); i++)
    await new Promise((r) => setTimeout(r, 100))
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
    { detached: true, stdio: ['ignore', out, out], env: engineEnv(WORK) },
  )
  child.unref()
  writeFileSync(pidFile, String(child.pid))
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
}

/** Open the administrative sheet of `channelId` by deep link, on the Billing
 * workspace already selected in this page's storage. */
async function openAdminSheet(page: Page, channelId: string): Promise<Locator> {
  const read = page.waitForResponse(
    (r) =>
      r.url().includes(`/v1/m/sessions/channels/${channelId}/grants`) &&
      r.request().method() === 'GET',
  )
  await page.goto(`/communications/administration?admin_channel=${channelId}`)
  const res = await read
  note('GET grants (sheet open)', res.status())
  expect(res.status()).toBe(200)
  const sheet = page.getByRole('dialog')
  await expect(sheet.locator('[data-slot="admin-etag"]')).toBeVisible()
  return sheet
}

test.beforeAll(async ({ request }) => {
  test.setTimeout(1_800_000)
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
  // D holds the CORE admin permission through the built-in role tier (admin →
  // read|write|admin on module permissions); its LOCAL bits are decided per channel.
  userD = await createMember(request, D_EMAIL, 'admin')
  userE = await createMember(request, E_EMAIL, 'editor')
  userF = await createMember(request, F_EMAIL, 'editor')
  // G is a VIEWER: the built-in tier gives it no module admin permission whatsoever.
  userG = await createMember(request, G_EMAIL, 'viewer')
  tokenA = (await login(request, A_EMAIL, MEMBER_PASSWORD)).token
  tokenB = (await login(request, B_EMAIL, MEMBER_PASSWORD)).token
  tokenD = (await login(request, D_EMAIL, MEMBER_PASSWORD)).token
  tokenG = (await login(request, G_EMAIL, MEMBER_PASSWORD)).token
  const whoD = await api(request, tokenD, 'GET', '/v1/auth/whoami')
  const grantsD = (
    whoD.json as { grants: { tenant: string; permissions: string[] }[] }
  ).grants
  const coreAdminReflected = grantsD.some(
    (g) =>
      g.tenant === TENANT && g.permissions.includes('sessions:channel:admin'),
  )
  expect(
    coreAdminReflected,
    'whoami reflects sessions:channel:admin for D',
  ).toBe(true)
  const ready = await apiRead(
    request,
    tokenA,
    `/v1/m/sessions/channels?workspace_id=${wsBilling}&limit=1`,
  )
  expect(ready.status, 'K3 readiness (catalog as A)').toBe(200)

  // The administrable channel: A r/w/admin, D ADMIN ONLY (no read), B read.
  const created = await api(
    request,
    tokenA,
    'POST',
    '/v1/m/sessions/channels',
    {
      workspace_id: wsBilling,
      slug: adminSlug,
      name: 'K3 admin journey',
      description: 'Administered from the console',
      initial_grants: [
        {
          subject: { kind: 'user', ref: userA },
          can_read: true,
          can_write: true,
          can_admin: true,
        },
        {
          subject: { kind: 'user', ref: userD },
          can_read: false,
          can_write: false,
          can_admin: true,
        },
        {
          subject: { kind: 'user', ref: userB },
          can_read: true,
          can_write: false,
          can_admin: false,
        },
        // G1-B: the viewer holds a LOCAL ADMIN bit and NO local read. Administration is
        // not a read tier, and this fixture is what makes that testable rather than
        // asserted.
        {
          subject: { kind: 'user', ref: userG },
          can_read: false,
          can_write: false,
          can_admin: true,
        },
      ],
    },
  )
  expect(created.status, 'create admin channel').toBe(201)
  const receipt = created.json as {
    channel: { id: string }
    grants: { id: string; subject: { ref: string } }[]
    etag: string
  }
  adminChannelId = receipt.channel.id
  grantIdD = receipt.grants.find((g) => g.subject.ref === userD)?.id ?? ''
  expect(grantIdD).not.toBe('')

  // A grant history of MORE THAN 256 generations for ONE subject (E), driven through
  // the real grant and revoke routes with the ETag each mutation returned.
  let etag = receipt.etag
  const t0 = Date.now()
  for (let i = 0; i < HISTORY_GENERATIONS; i++) {
    const g = await api(
      request,
      tokenA,
      'POST',
      `/v1/m/sessions/channels/${adminChannelId}/grants`,
      {
        subject: { kind: 'user', ref: userE },
        can_read: true,
        can_write: false,
        can_admin: false,
      },
      { 'If-Match': etag },
    )
    expect(g.status, `history grant ${i + 1}`).toBe(200)
    const gj = g.json as {
      etag: string
      grant: { id: string; generation: number }
    }
    expect(gj.grant.generation).toBe(i + 1)
    etag = gj.etag
    const r = await api(
      request,
      tokenA,
      'POST',
      `/v1/m/sessions/channels/${adminChannelId}/grants/${gj.grant.id}/revoke`,
      undefined,
      { 'If-Match': etag },
    )
    expect(r.status, `history revoke ${i + 1}`).toBe(200)
    etag = (r.json as { etag: string }).etag
  }
  evidence.history = {
    generations: HISTORY_GENERATIONS,
    seconds: Math.round((Date.now() - t0) / 100) / 10,
    final_etag: etag,
  }

  // The cursor channel: A r/w/admin, B read; 26 notices to B (two inbox pages at 25).
  const cursorChannel = await api(
    request,
    tokenA,
    'POST',
    '/v1/m/sessions/channels',
    {
      workspace_id: wsBilling,
      slug: cursorSlug,
      name: 'K3 cursor journey',
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
    },
  )
  expect(cursorChannel.status, 'create cursor channel').toBe(201)
  cursorChannelId = (cursorChannel.json as { channel: { id: string } }).channel
    .id
  for (let i = 1; i <= CURSOR_NOTICES; i++) {
    const sent = await api(
      request,
      tokenA,
      'POST',
      '/v1/m/sessions/messages/send',
      {
        channel_id: cursorChannelId,
        recipient: { kind: 'user', ref: userB },
        content: {
          subject: `Cursor notice ${String(i).padStart(2, '0')} ${STAMP}`,
          blocks: [{ type: 'text', format: 'plain', text: `notice ${i}` }],
        },
      },
      { 'Idempotency-Key': uuidv7() },
    )
    expect(sent.status, `cursor notice ${i}`).toBe(201)
  }
  evidence.actors = { a: userA, b: userB, d: userD, e: userE, f: userF }
  evidence.workspaces = { billing: wsBilling }
  evidence.channels = { admin: adminChannelId, cursor: cursorChannelId }
})

test.afterAll(async ({ browser }) => {
  evidence.unavailable_read_retries = unavailableRetries
  evidence.rate_limited_retries = rateLimitedRetries
  evidence.http_statuses = httpStatuses
  evidence.browser = {
    name: browser.browserType().name(),
    version: browser.version(),
  }
  writeFileSync(
    path.join(evidenceDir(), 'journey-i2.json'),
    JSON.stringify(evidence, null, 2),
  )
})

// ─── 1 · D: core admin reflected, local ADMIN ONLY, no read ────────────────────────
test('D (role admin, local admin bit only) reaches the administration door, lists and opens the channel through the grant read; the read catalog hides it and the read-tier GET conceals it', async ({
  page,
  request,
}) => {
  await uiLogin(page, D_EMAIL, MEMBER_PASSWORD)
  await page.goto('/communications/administration')
  await expect(
    page.getByRole('heading', { name: 'Channel administration' }),
  ).toBeVisible()
  await selectWorkspace(page, 'Billing')
  await expect(
    page.getByRole('tab', { name: 'Administration', selected: true }),
  ).toBeVisible()
  const listed = page.waitForResponse((r) =>
    r.url().includes('/v1/m/sessions/channels/administration'),
  )
  await page.getByRole('button', { name: 'Refresh' }).click()
  const listing = await listed
  note('GET administration (D)', listing.status())
  expect(listing.status()).toBe(200)
  const row = page.getByRole('row').filter({ hasText: adminSlug })
  await expect(row).toHaveCount(1)
  await shot(page, 'i2-01-d-administrable-catalog')
  const read = page.waitForResponse((r) =>
    r.url().includes(`/v1/m/sessions/channels/${adminChannelId}/grants`),
  )
  await row.click()
  const grants = await read
  note('GET grants (D)', grants.status())
  expect(grants.status()).toBe(200)
  expect(grants.headers()['cache-control']).toContain('no-store')
  const sheet = page.getByRole('dialog')
  await expect(sheet).toContainText('K3 admin journey')
  await expect(sheet.locator('[data-slot="admin-etag"]')).toBeVisible()
  await sheet.getByRole('tab', { name: 'Grants' }).click()
  await expect(sheet.getByRole('row').filter({ hasText: userD })).toHaveCount(1)
  await shot(page, 'i2-02-d-admin-sheet-grants')
  await page.keyboard.press('Escape')
  await expect(sheet).toBeHidden()
  // The read catalog does not list the channel for D (no local read bit) …
  await page.getByRole('tab', { name: 'Channels' }).click()
  await page
    .getByRole('searchbox')
    .or(page.getByPlaceholder(/Search/))
    .first()
    .fill(adminSlug)
  await expect(
    page.getByRole('row').filter({ hasText: adminSlug }),
  ).toHaveCount(0)
  await shot(page, 'i2-03-d-read-catalog-hides-channel')
  // … and the read-tier point read conceals it, over HTTP with D's own token.
  const concealed = await api(
    request,
    tokenD,
    'GET',
    `/v1/m/sessions/channels/${adminChannelId}`,
  )
  // `api()` returns a NUMBER; only a Playwright Response has `.status()`.
  note('GET channel read-tier (D)', concealed.status)
  expect([403, 404]).toContain(concealed.status)
  const adminList = await apiRead(
    request,
    tokenD,
    `/v1/m/sessions/channels/administration?workspace_id=${wsBilling}&state=all&limit=50`,
  )
  expect(adminList.status).toBe(200)
  expect(
    (adminList.json as { items: { channel: { id: string } }[] }).items.map(
      (i) => i.channel.id,
    ),
  ).toContain(adminChannelId)
  evidence.d = {
    read_tier_status: concealed.status,
    admin_listing_status: adminList.status,
    note: 'core sessions:channel:admin reflected in whoami by the built-in admin role; local admin bit only. This is NOT the Cedar-only case of G1/F3, which stays open.',
  }
})

// ─── 2 · A: configuration PATCH under the read ETag, persisted across reload ──────
test('A edits name, description and an advanced field: only the changed fields are sent under If-Match of the read; the change persists across a reload and the ETag advances', async ({
  page,
  request,
}) => {
  await uiLogin(page, A_EMAIL, MEMBER_PASSWORD)
  await page.goto('/communications/administration')
  await selectWorkspace(page, 'Billing')
  const sheet = await openAdminSheet(page, adminChannelId)
  const etagBefore =
    (await sheet.locator('[data-slot="admin-etag"]').textContent())?.trim() ??
    ''
  expect(etagBefore).toMatch(/^"v\d+"$/)
  await sheet.getByLabel(/^Name/).fill('K3 admin journey (renamed)')
  await sheet
    .getByLabel('Description')
    .fill('Renamed from the administrative sheet')
  await sheet.getByRole('button', { name: 'Advanced options' }).click()
  await sheet.getByLabel('Max fanout').fill('7')
  await sheet.getByRole('button', { name: 'Review changes' }).click()
  await expect(
    sheet.getByText('Confirm the configuration change'),
  ).toBeVisible()
  await shot(page, 'i2-04-a-config-confirm')
  const patched = page.waitForResponse(
    (r) =>
      r.url().endsWith('/v1/m/sessions/channels') &&
      r.request().method() === 'PATCH',
  )
  await sheet.getByRole('button', { name: 'Confirm changes' }).click()
  const res = await patched
  note('PATCH channels (A)', res.status())
  expect(res.status()).toBe(200)
  expect(res.request().headers()['if-match']).toBe(etagBefore)
  expect(res.request().headers()['idempotency-key']).toBeUndefined()
  const body = JSON.parse(res.request().postData() ?? '{}') as Record<
    string,
    unknown
  >
  expect(body).toEqual({
    channel_id: adminChannelId,
    name: 'K3 admin journey (renamed)',
    description: 'Renamed from the administrative sheet',
    max_fanout: 7,
  })
  await expect(sheet.getByText('Configuration applied')).toBeVisible()
  await shot(page, 'i2-05-a-config-applied')
  // Reload: the sheet re-reads and shows the persisted values and the new ETag.
  await page.reload()
  const again = page.getByRole('dialog')
  await expect(again.getByLabel(/^Name/)).toHaveValue(
    'K3 admin journey (renamed)',
  )
  const etagAfter =
    (await again.locator('[data-slot="admin-etag"]').textContent())?.trim() ??
    ''
  expect(etagAfter).not.toBe(etagBefore)
  await shot(page, 'i2-06-a-config-after-reload')
  const viaHttp = await apiRead(
    request,
    tokenA,
    `/v1/m/sessions/channels/${adminChannelId}`,
  )
  expect(viaHttp.status).toBe(200)
  expect((viaHttp.json as { name: string; max_fanout: number }).name).toBe(
    'K3 admin journey (renamed)',
  )
  expect((viaHttp.json as { max_fanout: number }).max_fanout).toBe(7)
  evidence.patch = { etag_before: etagBefore, etag_after: etagAfter, body }
})

// ─── 3 · concurrent PATCH: conflict, nothing re-sent, new confirmation after re-read ─
test('a concurrent PATCH makes the confirmation a conflict: exactly one PATCH left, nothing re-sent, and a NEW confirmation after re-read succeeds with the new ETag', async ({
  page,
  request,
}) => {
  await uiLogin(page, A_EMAIL, MEMBER_PASSWORD)
  await page.goto('/communications/administration')
  await selectWorkspace(page, 'Billing')
  const sheet = await openAdminSheet(page, adminChannelId)
  const etagRead =
    (await sheet.locator('[data-slot="admin-etag"]').textContent())?.trim() ??
    ''
  // Meanwhile the channel moves through the API with the same ETag.
  const moved = await api(
    request,
    tokenA,
    'PATCH',
    '/v1/m/sessions/channels',
    { channel_id: adminChannelId, description: 'moved by the API' },
    { 'If-Match': etagRead },
  )
  expect(moved.status).toBe(200)
  const patches: string[] = []
  page.on('request', (r) => {
    if (r.url().endsWith('/v1/m/sessions/channels') && r.method() === 'PATCH')
      patches.push(r.headers()['if-match'] ?? '')
  })
  await sheet.getByLabel(/^Name/).fill('Stale attempt')
  await sheet.getByRole('button', { name: 'Review changes' }).click()
  const stale = page.waitForResponse(
    (r) =>
      r.url().endsWith('/v1/m/sessions/channels') &&
      r.request().method() === 'PATCH',
  )
  await sheet.getByRole('button', { name: 'Confirm changes' }).click()
  const staleRes = await stale
  note('PATCH channels stale (A)', staleRes.status())
  expect([409, 412]).toContain(staleRes.status())
  await expect(sheet.locator('[data-slot="config-conflict"]')).toBeVisible()
  await shot(page, 'i2-07-a-config-conflict')
  await sheet.getByRole('button', { name: 'Re-read and edit again' }).click()
  await expect(sheet.locator('[data-slot="admin-etag"]')).not.toHaveText(
    etagRead,
  )
  const etagNew =
    (await sheet.locator('[data-slot="admin-etag"]').textContent())?.trim() ??
    ''
  await page.waitForTimeout(300)
  expect(patches).toEqual([etagRead])
  // The edit is still on screen; a NEW confirmation carries the NEW ETag.
  await expect(sheet.getByLabel(/^Name/)).toHaveValue('Stale attempt')
  await sheet.getByRole('button', { name: 'Review changes' }).click()
  const fresh = page.waitForResponse(
    (r) =>
      r.url().endsWith('/v1/m/sessions/channels') &&
      r.request().method() === 'PATCH',
  )
  await sheet.getByRole('button', { name: 'Confirm changes' }).click()
  const freshRes = await fresh
  note('PATCH channels after re-read (A)', freshRes.status())
  expect(freshRes.status()).toBe(200)
  expect(freshRes.request().headers()['if-match']).toBe(etagNew)
  expect(patches).toEqual([etagRead, etagNew])
  await expect(sheet.getByText('Configuration applied')).toBeVisible()
  evidence.concurrentPatch = {
    stale_status: staleRes.status(),
    if_match_sequence: patches,
  }
})

// ─── 4 · history > 256, persisted vs temporal filters, pagination ──────────────────
test('the grant history of one subject is paginated past 256 generations on the server continuation; persisted-state and subject filters restart the chain; archived is not touched', async ({
  page,
}) => {
  await uiLogin(page, A_EMAIL, MEMBER_PASSWORD)
  await page.goto('/communications/administration')
  await selectWorkspace(page, 'Billing')
  const sheet = await openAdminSheet(page, adminChannelId)
  await sheet.getByRole('tab', { name: 'Grants' }).click()
  // Default: persisted ACTIVE only — the three initial grants.
  await expect(sheet.getByRole('row')).toHaveCount(1 + 3)
  const urls: string[] = []
  page.on('request', (r) => {
    if (r.url().includes(`/v1/m/sessions/channels/${adminChannelId}/grants`))
      urls.push(r.url())
  })
  await sheet.getByRole('combobox', { name: 'Subject kind' }).click()
  await page.getByRole('option', { name: 'User', exact: true }).click()
  await sheet.getByLabel('Subject reference').fill(userE)
  await sheet.getByRole('combobox', { name: 'Persisted state' }).click()
  await page.getByRole('option', { name: 'All (stored)' }).click()
  await sheet.getByRole('combobox', { name: 'Page size' }).click()
  await page.getByRole('option', { name: '100' }).click()
  await sheet.getByRole('button', { name: 'Apply filter' }).click()
  await expect(
    sheet.getByText('More generations exist beyond this page'),
  ).toBeVisible()
  await expect(sheet.getByRole('row')).toHaveCount(1 + 100)
  await sheet.getByRole('button', { name: 'Load more' }).click()
  await expect(sheet.getByRole('row')).toHaveCount(1 + 200)
  await sheet.getByRole('button', { name: 'Load more' }).click()
  await expect(sheet.getByRole('row')).toHaveCount(1 + HISTORY_GENERATIONS)
  await expect(
    sheet.getByText('More generations exist beyond this page'),
  ).toBeHidden()
  await shot(page, 'i2-08-a-grant-history-260')
  const withSubject = urls.filter((u) => u.includes(`subject_ref=${userE}`))
  expect(withSubject.length).toBeGreaterThanOrEqual(3)
  expect(withSubject[0]).toContain('state=all')
  expect(withSubject[0]).not.toContain('continuation=')
  expect(withSubject[1]).toContain('continuation=')
  expect(withSubject[2]).toContain('continuation=')
  expect(withSubject.every((u) => u.includes('subject_kind=user'))).toBe(true)
  evidence.historyPagination = {
    rows: HISTORY_GENERATIONS,
    requests: withSubject.length,
  }
})

// ─── 5 · grant from the form, revoke the exact generation, successor as a second act ─
test('A grants E from the form under the read ETag, revokes that exact generation, then grants a successor to B as a separate confirmation under the re-read ETag', async ({
  page,
  request,
}) => {
  await uiLogin(page, A_EMAIL, MEMBER_PASSWORD)
  await page.goto('/communications/administration')
  await selectWorkspace(page, 'Billing')
  const sheet = await openAdminSheet(page, adminChannelId)
  await sheet.getByRole('tab', { name: 'Grants' }).click()
  const etagRead =
    (await sheet.locator('[data-slot="admin-etag"]').textContent())?.trim() ??
    ''
  await sheet.getByRole('button', { name: 'Add grant' }).click()
  const form = sheet.getByRole('form', { name: 'New grant generation' })
  await form.getByRole('textbox', { name: 'Reference (ID)' }).fill(userE)
  await form.getByRole('checkbox', { name: 'Read' }).check()
  await form.getByRole('checkbox', { name: 'Write' }).check()
  await form.getByRole('button', { name: 'Review grant' }).click()
  await expect(sheet.getByText('Confirm the grant')).toBeVisible()
  await shot(page, 'i2-09-a-grant-confirm')
  const granted = page.waitForResponse(
    (r) =>
      r.url().endsWith(`/v1/m/sessions/channels/${adminChannelId}/grants`) &&
      r.request().method() === 'POST',
  )
  await sheet.getByRole('button', { name: 'Confirm grant' }).click()
  const g = await granted
  note('POST grant (A)', g.status())
  expect(g.status()).toBe(200)
  expect(g.request().headers()['if-match']).toBe(etagRead)
  const gBody = (await g.json()) as {
    grant: { id: string; generation: number }
    etag: string
  }
  expect(gBody.grant.generation).toBe(HISTORY_GENERATIONS + 1)
  await expect(sheet.getByText('Grant recorded')).toBeVisible()
  await shot(page, 'i2-10-a-grant-recorded')
  // The fresh read lists it as an ACTIVE generation; click it and revoke it.
  await sheet.getByRole('button', { name: 'Dismiss' }).click()
  const eRow = sheet.getByRole('row').filter({ hasText: userE })
  await expect(eRow).toHaveCount(1)
  await eRow.click()
  const detail = sheet.getByRole('region', { name: 'Generation detail' })
  await expect(detail).toContainText(gBody.grant.id)
  await detail.getByRole('button', { name: 'Revoke this generation' }).click()
  const revokeRegion = sheet.getByRole('region', {
    name: 'Revoke this exact generation',
  })
  await expect(revokeRegion).toContainText(gBody.grant.id)
  await expect(revokeRegion).toContainText(gBody.etag)
  const revoked = page.waitForResponse(
    (r) =>
      r.url().endsWith(`/grants/${gBody.grant.id}/revoke`) &&
      r.request().method() === 'POST',
  )
  await sheet.getByRole('button', { name: 'Confirm revocation' }).click()
  const rv = await revoked
  note('POST revoke (A)', rv.status())
  expect(rv.status()).toBe(200)
  expect(rv.request().headers()['if-match']).toBe(gBody.etag)
  expect(rv.request().postData()).toBeNull()
  await expect(sheet.getByText('Generation revoked')).toBeVisible()
  await shot(page, 'i2-11-a-revoked')
  // Revoke → grant a successor for B: two acts, the second under the RE-READ ETag.
  await sheet.getByRole('button', { name: 'Dismiss' }).click()
  const bRow = sheet.getByRole('row').filter({ hasText: userB })
  await bRow.click()
  await sheet
    .getByRole('button', { name: 'Revoke, then grant a successor' })
    .click()
  const bGrantId =
    (
      await sheet
        .getByRole('region', { name: 'Revoke this exact generation' })
        .locator('dd')
        .filter({ hasText: /^[0-9a-f-]{36}$/ })
        .first()
        .textContent()
    )?.trim() ?? ''
  const revokedB = page.waitForResponse(
    (r) =>
      r.url().endsWith(`/grants/${bGrantId}/revoke`) &&
      r.request().method() === 'POST',
  )
  await sheet.getByRole('button', { name: 'Confirm revocation' }).click()
  const rvB = await revokedB
  expect(rvB.status()).toBe(200)
  const etagAfterRevoke = ((await rvB.json()) as { etag: string }).etag
  await expect(sheet.getByText('Generation revoked')).toBeVisible()
  await expect(sheet.locator('[data-slot="admin-etag"]')).toHaveText(
    etagAfterRevoke,
  )
  await sheet
    .getByRole('button', { name: 'Grant a successor (separate act)' })
    .click()
  const successorForm = sheet.getByRole('form', {
    name: 'New grant generation',
  })
  await expect(
    successorForm.getByRole('textbox', { name: 'Reference (ID)' }),
  ).toHaveValue(userB)
  await expect(
    successorForm.getByRole('checkbox', { name: 'Read' }),
  ).toBeChecked()
  await successorForm.getByRole('checkbox', { name: 'Write' }).check()
  await successorForm.getByRole('button', { name: 'Review grant' }).click()
  await expect(
    sheet.getByText(`Successor of revoked generation ${bGrantId}`),
  ).toBeVisible()
  await shot(page, 'i2-12-a-successor-confirm')
  const successor = page.waitForResponse(
    (r) =>
      r.url().endsWith(`/v1/m/sessions/channels/${adminChannelId}/grants`) &&
      r.request().method() === 'POST',
  )
  await sheet.getByRole('button', { name: 'Confirm grant' }).click()
  const sRes = await successor
  note('POST successor grant (A)', sRes.status())
  expect(sRes.status()).toBe(200)
  expect(sRes.request().headers()['if-match']).toBe(etagAfterRevoke)
  const sBody = (await sRes.json()) as {
    grant: { id: string; supersedes_id?: string; can_write: boolean }
  }
  expect(sBody.grant.supersedes_id).toBe(bGrantId)
  expect(sBody.grant.can_write).toBe(true)
  await expect(sheet.getByText('Grant recorded')).toBeVisible()
  // The durable history, over HTTP: E's generation revoked, B's successor active.
  const history = await apiRead(
    request,
    tokenA,
    `/v1/m/sessions/channels/${adminChannelId}/grants?workspace_id=${wsBilling}&state=all&subject_kind=user&subject_ref=${userB}&limit=10`,
  )
  const items = (
    history.json as {
      items: { grant: { id: string; state: string; supersedes_id?: string } }[]
    }
  ).items
  expect(items.find((i) => i.grant.id === bGrantId)?.grant.state).toBe(
    'revoked',
  )
  expect(
    items.find((i) => i.grant.id === sBody.grant.id)?.grant.supersedes_id,
  ).toBe(bGrantId)
  evidence.grantActs = {
    e_grant: gBody.grant.id,
    e_generation: gBody.grant.generation,
    b_revoked: bGrantId,
    b_successor: sBody.grant.id,
  }
})

// ─── 6 · expiry observed ≠ revocation persisted ─────────────────────────────────────
test('an active grant whose TTL passed is shown stored-active and temporally expired, is NOT called revoked, refuses a successor until explicitly revoked, then accepts it', async ({
  page,
  request,
}) => {
  const etag = await adminEtag(request, tokenA, adminChannelId)
  const soon = new Date(Date.now() + 4000).toISOString()
  const g = await api(
    request,
    tokenA,
    'POST',
    `/v1/m/sessions/channels/${adminChannelId}/grants`,
    {
      subject: { kind: 'user', ref: userF },
      can_read: true,
      can_write: false,
      can_admin: false,
      expires_at: soon,
    },
    { 'If-Match': etag },
  )
  expect(g.status, 'short-lived grant for F').toBe(200)
  const fGrant = (g.json as { grant: { id: string } }).grant.id
  await new Promise((r) => setTimeout(r, 5000))
  await uiLogin(page, A_EMAIL, MEMBER_PASSWORD)
  await page.goto('/communications/administration')
  await selectWorkspace(page, 'Billing')
  const sheet = await openAdminSheet(page, adminChannelId)
  await sheet.getByRole('tab', { name: 'Grants' }).click()
  const fRow = sheet.getByRole('row').filter({ hasText: userF })
  await expect(fRow).toHaveCount(1) // persisted ACTIVE is the default filter
  await expect(fRow).toContainText('Active')
  await expect(fRow).toContainText('Expired')
  await expect(fRow).not.toContainText('Revoked')
  await fRow.click()
  await expect(sheet.getByText(/The engine did not revoke it/)).toBeVisible()
  await shot(page, 'i2-13-a-expired-active-not-revoked')
  // A successor WITHOUT revoking first: the engine refuses; nothing is retried.
  await sheet.getByRole('button', { name: 'Add grant' }).click()
  const form = sheet.getByRole('form', { name: 'New grant generation' })
  await form.getByRole('textbox', { name: 'Reference (ID)' }).fill(userF)
  await form.getByRole('checkbox', { name: 'Read' }).check()
  await form.getByRole('button', { name: 'Review grant' }).click()
  const refused = page.waitForResponse(
    (r) =>
      r.url().endsWith(`/v1/m/sessions/channels/${adminChannelId}/grants`) &&
      r.request().method() === 'POST',
  )
  await sheet.getByRole('button', { name: 'Confirm grant' }).click()
  const refusedRes = await refused
  note('POST grant over expired-active (A)', refusedRes.status())
  expect(refusedRes.status()).toBeGreaterThanOrEqual(400)
  await expect(
    sheet
      .locator('[data-slot="act-conflict"], [data-slot="act-refused"]')
      .first(),
  ).toBeVisible()
  await shot(page, 'i2-14-a-successor-refused-until-revoked')
  // Explicit revocation of the expired-active generation, then the successor.
  await sheet
    .getByRole('button', { name: /Re-read the history|Dismiss/ })
    .first()
    .click()
  await sheet.getByRole('row').filter({ hasText: userF }).click()
  await sheet.getByRole('button', { name: 'Revoke this generation' }).click()
  const revoked = page.waitForResponse(
    (r) =>
      r.url().endsWith(`/grants/${fGrant}/revoke`) &&
      r.request().method() === 'POST',
  )
  await sheet.getByRole('button', { name: 'Confirm revocation' }).click()
  expect((await revoked).status()).toBe(200)
  await expect(sheet.getByText('Generation revoked')).toBeVisible()
  await sheet.getByRole('button', { name: 'Dismiss' }).click()
  await sheet.getByRole('button', { name: 'Add grant' }).click()
  const form2 = sheet.getByRole('form', { name: 'New grant generation' })
  await form2.getByRole('textbox', { name: 'Reference (ID)' }).fill(userF)
  await form2.getByRole('checkbox', { name: 'Read' }).check()
  await form2.getByRole('button', { name: 'Review grant' }).click()
  const accepted = page.waitForResponse(
    (r) =>
      r.url().endsWith(`/v1/m/sessions/channels/${adminChannelId}/grants`) &&
      r.request().method() === 'POST',
  )
  await sheet.getByRole('button', { name: 'Confirm grant' }).click()
  const acceptedRes = await accepted
  note('POST grant after revoke (A)', acceptedRes.status())
  expect(acceptedRes.status()).toBe(200)
  await expect(sheet.getByText('Grant recorded')).toBeVisible()
  evidence.expiry = {
    f_grant: fGrant,
    refused_status: refusedRes.status(),
    accepted_after_revoke: acceptedRes.status(),
  }
})

// ─── 7 · authority lost while the sheet is open ───────────────────────────────────
test("revoking D's admin generation while D's sheet is open: the re-read is refused and every previously shown row and field is replaced", async ({
  browser,
  request,
}) => {
  const d = await (await browser.newContext()).newPage()
  await uiLogin(d, D_EMAIL, MEMBER_PASSWORD)
  await d.goto('/communications/administration')
  await selectWorkspace(d, 'Billing')
  const sheet = await openAdminSheet(d, adminChannelId)
  await expect(sheet.getByLabel(/^Name/)).toBeVisible()
  const etag = await adminEtag(request, tokenA, adminChannelId)
  const revoked = await api(
    request,
    tokenA,
    'POST',
    `/v1/m/sessions/channels/${adminChannelId}/grants/${grantIdD}/revoke`,
    undefined,
    { 'If-Match': etag },
  )
  expect(revoked.status).toBe(200)
  const reread = d.waitForResponse((r) =>
    r.url().includes(`/v1/m/sessions/channels/${adminChannelId}/grants`),
  )
  await sheet.getByRole('button', { name: 'Re-read' }).click()
  const status = (await reread).status()
  note('GET grants after revocation (D)', status)
  expect([403, 404]).toContain(status)
  await expect(sheet.locator('[data-slot="admin-read-failure"]')).toBeVisible()
  await expect(sheet.getByLabel(/^Name/)).toHaveCount(0)
  await expect(sheet.getByRole('tab', { name: 'Grants' })).toHaveCount(0)
  await shot(d, 'i2-15-d-authority-lost-content-replaced')
  await d.keyboard.press('Escape')
  await d.getByRole('button', { name: 'Refresh' }).click()
  await expect(d.getByRole('row').filter({ hasText: adminSlug })).toHaveCount(0)
  await shot(d, 'i2-16-d-catalog-without-channel')
  evidence.dRevocation = { grant_id: grantIdD, reread_status: status }
  await d.context().close()
})

// ─── 8 · the personal seen cursor ─────────────────────────────────────────────────
test('B marks a real page seen: GET prepares the target of the chosen page, only the confirmation PUTs its last delivery under the cursor ETag and its own key; the position is durable across an engine restart; a second page is a new intention', async ({
  browser,
  request,
}) => {
  const b = await (await browser.newContext()).newPage()
  await uiLogin(b, B_EMAIL, MEMBER_PASSWORD)
  await b.goto('/communications/inbox')
  await selectWorkspace(b, 'Billing')
  await b.getByRole('combobox', { name: 'Page size' }).click()
  await b.getByRole('option', { name: '25' }).click()
  await expect(
    b.getByText('More deliveries exist beyond this page'),
  ).toBeVisible()
  await b.getByRole('button', { name: 'Load more' }).click()
  await expect(
    b.getByText('More deliveries exist beyond this page'),
  ).toBeHidden()
  const section = b.getByRole('region', { name: 'Seen cursor' })
  const pages = section.getByRole('listitem')
  await expect(pages).toHaveCount(2)
  await shot(b, 'i2-17-b-inbox-two-pages-cursor-section')
  const cursorCalls: { method: string; url: string }[] = []
  b.on('request', (r) => {
    if (r.url().includes('/v1/m/sessions/inbox/cursors/personal/'))
      cursorCalls.push({ method: r.method(), url: r.url() })
  })
  // Page 1: the GET names ITS target and the PUT names its LAST delivery.
  const lastOfPage1 =
    (
      await pages
        .nth(0)
        .locator('[data-slot="cursor-page-last-delivery"]')
        .first()
        .textContent()
    )?.trim() ?? ''
  expect(lastOfPage1).toMatch(/^[0-9a-f-]{36}$/)
  const minted = b.waitForResponse(
    (r) =>
      r.url().includes('/v1/m/sessions/inbox/cursors/personal/') &&
      r.request().method() === 'GET',
  )
  await pages
    .nth(0)
    .getByRole('button', { name: 'Mark seen up to here' })
    .click()
  const mintRes = await minted
  note('GET cursor token (B)', mintRes.status())
  expect(mintRes.status()).toBe(200)
  expect(mintRes.url()).toContain(`/personal/${userB}?`)
  const mintBody = (await mintRes.json()) as {
    version: number
    etag: string
    cursor_id?: string
  }
  const dialog = b.getByRole('dialog')
  await expect(dialog.locator('[data-slot="cursor-last-delivery"]')).toHaveText(
    lastOfPage1,
  )
  expect(cursorCalls.filter((c) => c.method === 'PUT')).toHaveLength(0)
  const advanced = b.waitForResponse(
    (r) =>
      r.url().includes('/v1/m/sessions/inbox/cursors/personal/') &&
      r.request().method() === 'PUT',
  )
  await dialog
    .getByRole('button', { name: 'Confirm: mark seen up to here' })
    .click()
  const putRes = await advanced
  note('PUT cursor (B)', putRes.status())
  expect(putRes.status()).toBe(200)
  expect(putRes.url()).toBe(
    `${BASE}/v1/m/sessions/inbox/cursors/personal/${userB}`,
  )
  expect(putRes.request().headers()['if-match']).toBe(mintBody.etag)
  const key1 = putRes.request().headers()['idempotency-key'] ?? ''
  expect(key1).toMatch(
    /^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/,
  )
  const putBody = JSON.parse(putRes.request().postData() ?? '{}') as {
    cursor: string
    delivery_id: string
  }
  expect(putBody.delivery_id).toBe(lastOfPage1)
  const put1 = (await putRes.json()) as {
    version: number
    etag: string
    cursor_id: string
    replayed: boolean
    projection: {
      last_seen_seq?: number
      barrier_delivery_id?: string
      barrier_reason?: string
    }
  }
  expect(put1.replayed).toBe(false)
  const receipt = dialog.locator('[data-slot="cursor-receipt"]')
  await expect(receipt).toBeVisible()
  await shot(b, 'i2-18-b-cursor-receipt')
  const advancedFully = put1.projection.barrier_delivery_id === undefined
  if (advancedFully) {
    await expect(dialog.getByText('Cursor advanced')).toBeVisible()
    await expect(
      receipt.locator('[data-slot="cursor-last-seen-seq"]'),
    ).toHaveText(String(put1.projection.last_seen_seq))
  } else {
    await expect(dialog.getByText('Advance limited by a barrier')).toBeVisible()
    await expect(receipt.locator('[data-slot="cursor-barrier"]')).toHaveText(
      put1.projection.barrier_delivery_id as string,
    )
  }
  await dialog.getByRole('button', { name: 'Dismiss' }).click()
  // The chain was dropped: the cursor section now lists ONE loaded page again.
  await expect(
    b.getByRole('region', { name: 'Seen cursor' }).getByRole('listitem'),
  ).toHaveCount(1)
  // The engine, asked again for the same page target, now reports the durable version.
  const inboxNow = await apiRead(
    request,
    tokenB,
    `/v1/m/sessions/inbox?workspace_id=${wsBilling}&limit=25`,
  )
  expect(inboxNow.status).toBe(200)
  const target =
    (inboxNow.json as { cursor_target?: string }).cursor_target ?? ''
  expect(target).not.toBe('')
  const durable = await apiRead(
    request,
    tokenB,
    `/v1/m/sessions/inbox/cursors/personal/${userB}?workspace_id=${wsBilling}&target=${encodeURIComponent(target)}`,
  )
  expect(durable.status).toBe(200)
  const durableBody = durable.json as {
    version: number
    cursor_id?: string
    etag: string
  }
  expect(durableBody.version).toBe(put1.version)
  expect(durableBody.cursor_id).toBe(put1.cursor_id)
  // A replay of the SAME command, over HTTP, is answered as a replay and applies nothing twice.
  const replay = await api(
    request,
    tokenB,
    'PUT',
    `/v1/m/sessions/inbox/cursors/personal/${userB}`,
    putBody,
    { 'If-Match': mintBody.etag, 'Idempotency-Key': key1 },
  )
  note('PUT cursor replay (B)', replay.status)
  expect(replay.status).toBe(200)
  expect((replay.json as { replayed: boolean }).replayed).toBe(true)
  // Restart the engine on the same data directory: the durable cursor survives.
  await restartEngine()
  const afterRestart = await apiRead(
    request,
    tokenB,
    `/v1/m/sessions/inbox/cursors/personal/${userB}?workspace_id=${wsBilling}&target=${encodeURIComponent(target)}`,
  )
  expect(afterRestart.status).toBe(200)
  expect((afterRestart.json as { version: number }).version).toBe(put1.version)
  // THE DURABLE EFFECT, READ BACK IN THE BROWSER. A listing with no continuation
  // resumes from the cursor: `after := lineage.baseDeliverySeq`
  // (modules/sessions/communication_inbox_navigation.go). So after the advance the
  // deliveries that were marked seen are BEHIND the cursor and a fresh inbox shows
  // only what follows — the console applies no filter of its own, and this is what
  // "re-read from the durable cursor" means on the wire.
  await b.reload()
  await b.getByRole('combobox', { name: 'Page size' }).click()
  await b.getByRole('option', { name: '25' }).click()
  await expect(
    b.getByRole('row').filter({ hasText: `Cursor notice 26 ${STAMP}` }),
  ).toHaveCount(1)
  await expect(
    b.getByRole('row').filter({ hasText: `Cursor notice 01 ${STAMP}` }),
  ).toHaveCount(0)
  await expect(b.getByRole('button', { name: 'Load more' })).toHaveCount(0)
  const pages2 = b
    .getByRole('region', { name: 'Seen cursor' })
    .getByRole('listitem')
  await expect(pages2).toHaveCount(1)
  await shot(b, 'i2-19-b-inbox-resumed-from-durable-cursor')
  // A SECOND advance is a NEW intention: a target minted now, a key of its own, and
  // the CURRENT cursor ETag — never the one the first advance consumed.
  const minted2 = b.waitForResponse(
    (r) =>
      r.url().includes('/v1/m/sessions/inbox/cursors/personal/') &&
      r.request().method() === 'GET',
  )
  await pages2
    .nth(0)
    .getByRole('button', { name: 'Mark seen up to here' })
    .click()
  const mint2 = (await (await minted2).json()) as {
    version: number
    etag: string
  }
  expect(mint2.version).toBe(put1.version)
  expect(mint2.etag).not.toBe(mintBody.etag)
  const advanced2 = b.waitForResponse(
    (r) =>
      r.url().includes('/v1/m/sessions/inbox/cursors/personal/') &&
      r.request().method() === 'PUT',
  )
  await b
    .getByRole('dialog')
    .getByRole('button', { name: 'Confirm: mark seen up to here' })
    .click()
  const put2Res = await advanced2
  note('PUT cursor second advance (B)', put2Res.status())
  expect(put2Res.status()).toBe(200)
  expect(put2Res.request().headers()['if-match']).toBe(mint2.etag)
  expect(put2Res.request().headers()['idempotency-key']).not.toBe(key1)
  const put2 = (await put2Res.json()) as {
    version: number
    projection: { last_seen_seq?: number }
  }
  expect(put2.version).toBeGreaterThan(put1.version)
  await shot(b, 'i2-20-b-cursor-second-advance-receipt')
  evidence.cursor = {
    page1_last_delivery: lastOfPage1,
    put1: {
      version: put1.version,
      cursor_id: put1.cursor_id,
      projection: put1.projection,
    },
    replay_status: replay.status,
    after_restart_version: (afterRestart.json as { version: number }).version,
    put2: { version: put2.version, projection: put2.projection },
    put_calls_before_confirm: 0,
  }
  await b.context().close()
})

// ─── 9 · keyboard, focus, mobile, docs captures ─────────────────────────────────────
test('keyboard-only administration (focus returns to the trigger on close), the 390×640 viewport, and the docs captures in light and dark', async ({
  browser,
}) => {
  const a = await (await browser.newContext()).newPage()
  await uiLogin(a, A_EMAIL, MEMBER_PASSWORD)
  await a.goto('/communications/administration')
  await selectWorkspace(a, 'Billing')
  const row = a.getByRole('row').filter({ hasText: adminSlug })
  await expect(row).toHaveCount(1)
  // OPENED BY KEYBOARD, FROM A REAL TRIGGER. The "Administer by ID" form is a
  // button, so the origin of the sheet is an element that HAS focus when it is
  // activated — which is what makes "focus returns to the origin" a testable
  // contract. A row opened with the mouse never held focus, so there is nothing to
  // return it to; the grid's own Enter activation is exercised by the pointer legs
  // above and by the row-click legs of tests 1-7.
  await a.getByLabel('Administer a channel by ID').fill(adminChannelId)
  const trigger = a.getByRole('button', { name: 'Administer' })
  await trigger.focus()
  await a.keyboard.press('Enter')
  const sheet = a.getByRole('dialog')
  await expect(sheet.locator('[data-slot="admin-etag"]')).toBeVisible()
  // Focus is trapped in the sheet: Tab a few times never leaves it.
  for (let i = 0; i < 6; i++) await a.keyboard.press('Tab')
  const inside = await a.evaluate(
    () => !!document.activeElement?.closest('[role="dialog"]'),
  )
  expect(inside).toBe(true)
  // Reach the Grants tab by keyboard and the first row by arrow keys.
  const focusUntil = async (name: RegExp) => {
    for (let i = 0; i < 80; i++) {
      const text = await a.evaluate(
        () =>
          (document.activeElement as HTMLElement | null)?.textContent?.trim() ??
          '',
      )
      if (name.test(text)) return
      await a.keyboard.press('Tab')
    }
    throw new Error(`could not reach ${name} by Tab`)
  }
  // A tablist is a ROVING TABINDEX (WAI-ARIA APG): Tab reaches the SELECTED tab and
  // the arrow keys move between tabs. Reaching «Grants» with Tab would mean the tab
  // strip was a plain focus list, which is the pattern this console does not use.
  await focusUntil(/^Configuration$/)
  await a.keyboard.press('ArrowRight')
  // Radix moves roving focus in a timeout; give it the turn before asserting.
  await a.waitForTimeout(150)
  const onGrants = await a.evaluate(() =>
    (document.activeElement as HTMLElement | null)?.textContent?.trim(),
  )
  expect(onGrants).toBe('Grants')
  await a.keyboard.press('Enter')
  // Something UNIQUE to the Grants panel: every row carries "granted by A", so a
  // filter on A's id matches every generation A ever issued.
  await expect(sheet.getByRole('button', { name: 'Add grant' })).toBeVisible()
  await shot(a, 'i2-20-a-keyboard-grants-tab')
  await a.keyboard.press('Escape')
  await expect(sheet).toBeHidden()
  // Focus is RETURNED TO THE TRIGGER that opened the sheet.
  const returned = await a.evaluate(() =>
    (document.activeElement as HTMLElement | null)?.textContent?.trim(),
  )
  expect(returned).toBe('Administer')
  await a.context().close()

  // 390×640: one column, actions reachable, the sheet scrolls locally.
  const m = await (
    await browser.newContext({
      viewport: { width: 390, height: 640 },
      isMobile: true,
      hasTouch: true,
    })
  ).newPage()
  await uiLogin(m, A_EMAIL, MEMBER_PASSWORD)
  await m.goto('/communications/administration')
  await selectWorkspace(m, 'Billing')
  await expect(
    m.getByRole('heading', { name: 'Channel administration' }),
  ).toBeVisible()
  await shot(m, 'i2-21-a-administration-mobile')
  await m.goto(`/communications/administration?admin_channel=${adminChannelId}`)
  const ms = m.getByRole('dialog')
  await expect(ms.locator('[data-slot="admin-etag"]')).toBeVisible()
  await expect(ms.getByRole('button', { name: 'Re-read' })).toBeVisible()
  await expect(ms.getByRole('button', { name: 'Review changes' })).toBeVisible()
  const overflow = await m.evaluate(
    () => document.documentElement.scrollWidth > window.innerWidth + 1,
  )
  expect(overflow).toBe(false)
  await shot(m, 'i2-22-a-admin-sheet-mobile')
  await ms.getByRole('tab', { name: 'Grants' }).click()
  await expect(ms.getByRole('button', { name: 'Add grant' })).toBeVisible()
  await shot(m, 'i2-23-a-admin-grants-mobile')
  await m.context().close()

  // Docs captures of the administration door, light and dark.
  const docs = { viewport: { width: 1440, height: 1000 }, deviceScaleFactor: 2 }
  const dpage = await (await browser.newContext(docs)).newPage()
  await uiLogin(dpage, A_EMAIL, MEMBER_PASSWORD)
  await dpage.goto('/communications/administration')
  await selectWorkspace(dpage, 'Billing')
  await expect(
    dpage.getByRole('heading', { name: 'Channel administration' }),
  ).toBeVisible()
  await expect(
    dpage.getByRole('row').filter({ hasText: adminSlug }),
  ).toHaveCount(1)
  const shotDoc = async (theme: 'light' | 'dark') => {
    await dpage.getByRole('button', { name: 'Toggle theme' }).click()
    await dpage
      .getByRole('menuitem', { name: theme === 'dark' ? 'Dark' : 'Light' })
      .click()
    if (theme === 'dark')
      await expect(dpage.locator('html')).toHaveClass(/dark/)
    else await expect(dpage.locator('html')).not.toHaveClass(/dark/)
    const file = path.join(
      evidenceDir(),
      `communications-administration-${theme}.png`,
    )
    await dpage.screenshot({ path: file, fullPage: false })
    const pub = path.join('playwright-report', 'k3-i2')
    mkdirSync(pub, { recursive: true })
    copyFileSync(
      file,
      path.join(pub, `communications-administration-${theme}.png`),
    )
  }
  await shotDoc('light')
  await shotDoc('dark')
  await dpage.context().close()
  evidence.docsCaptures = [
    'communications-administration-light.png',
    'communications-administration-dark.png',
  ]
})

/* ── G1-B · scoped administration in the browser, on real authored policy ───────── */

/**
 * Publish an authored Cedar source through the real PDP route. Publishing REPLACES the
 * active source, which is what makes the two phases below deterministic: each owns the
 * policy it runs under, and the second is not reading the first's leftovers.
 */
async function publishAuthored(
  request: APIRequestContext,
  source: string,
): Promise<void> {
  const published = await api(
    request,
    tokenA,
    'POST',
    '/v1/m/governance/pdp/publish',
    { engine: 'cedar', source },
  )
  note('POST pdp/publish', published.status)
  expect(
    published.status,
    `publish authored policy: ${JSON.stringify(published.json)}`,
  ).toBe(200)
}

/** Every capability question this page asked, in order, recorded by OBSERVING the
 *  requests — never by intercepting or answering them. */
type AskedQuestion = {
  kind: string
  operation: string
  workspace_id?: string
  selectors?: { path?: Record<string, string>; body?: Record<string, string> }
}
function recordCapabilityQuestions(page: Page): AskedQuestion[] {
  const asked: AskedQuestion[] = []
  page.on('request', (req) => {
    if (!req.url().includes('/v1/auth/capabilities')) return
    if (req.method() !== 'POST') return
    const body = req.postDataJSON() as
      { schema_version?: number; questions?: AskedQuestion[] } | undefined
    expect(body?.schema_version, 'the console speaks schema 2').toBe(2)
    for (const q of body?.questions ?? []) asked.push(q)
  })
  return asked
}

/** The capability ANSWER the engine gave for one operation, read off the wire. */
async function capabilityAnswerFor(
  page: Page,
  operation: string,
): Promise<{ state: string; code: string; refresh_after_ms?: number }> {
  const res = await page.waitForResponse(
    async (r) => {
      if (!r.url().includes('/v1/auth/capabilities')) return false
      if (r.request().method() !== 'POST') return false
      const asked = r.request().postDataJSON() as {
        questions?: AskedQuestion[]
      }
      return (asked.questions ?? []).some((q) => q.operation === operation)
    },
    { timeout: 60_000 },
  )
  note('POST auth/capabilities', res.status())
  expect(res.status()).toBe(200)
  const body = (await res.json()) as {
    schema_version: number
    results: { state: string; code: string; refresh_after_ms?: number }[]
  }
  expect(body.schema_version).toBe(2)
  expect(body.results).toHaveLength(1)
  return body.results[0]
}

const CAP_SURFACE = 'GET /v1/m/sessions/channels/administration'
const CAP_SHEET = 'GET /v1/m/sessions/channels/{id}/grants'
const CAP_PATCH = 'PATCH /v1/m/sessions/channels'

// ─── G1-B/1 · workspace-scoped admission, end to end, with a false reflection ──────
test('G1-B — a VIEWER with authored workspace policy and a local ADMIN bit administers from the console: whoami denies it, the engine admits it, and the real PATCH lands', async ({
  browser,
  request,
}) => {
  test.setTimeout(600_000)

  // ⛔ THE REFLECTION SAYS NO, AND IT IS ASSERTED RATHER THAN ASSUMED. G holds the viewer
  //    role, which grants no module admin permission; nothing below can be explained by a
  //    permission set the console read.
  const whoG = await api(request, tokenG, 'GET', '/v1/auth/whoami')
  expect(whoG.status).toBe(200)
  const grantsG = (
    whoG.json as { grants: { tenant: string; permissions: string[] }[] }
  ).grants
  const reflectsAdmin = grantsG.some(
    (g) =>
      g.tenant === TENANT && g.permissions.includes('sessions:channel:admin'),
  )
  expect(
    reflectsAdmin,
    'whoami must NOT reflect sessions:channel:admin for G',
  ).toBe(false)
  // And its content read of the very channel it will administer stays refused: this is
  // administration WITHOUT read, not a read in disguise.
  const contentRead = await apiRead(
    request,
    tokenG,
    `/v1/m/sessions/channels/${adminChannelId}?workspace_id=${wsBilling}`,
  )
  note('GET channel as G (content read)', contentRead.status)
  expect(
    contentRead.status,
    'G has no local read bit: the read-tier GET must not succeed',
  ).not.toBe(200)
  // Before any policy exists the administrative collection is refused too — the control
  // that makes the admission below a CHANGE rather than a standing condition.
  const beforeAdmin = await apiRead(
    request,
    tokenG,
    `/v1/m/sessions/channels/administration?workspace_id=${wsBilling}&limit=1`,
  )
  note('GET administration as G (before policy)', beforeAdmin.status)
  expect(beforeAdmin.status, 'refused before the authored grant').toBe(403)

  // The real authored policy: one principal, one action, scoped to ONE workspace by slug.
  await publishAuthored(
    request,
    `permit(principal in User::"${userG}", action == Action::"sessions:channel:admin", resource)` +
      ` when { resource in Workspace::"billing" };`,
  )

  const g = await (await browser.newContext()).newPage()
  const asked = recordCapabilityQuestions(g)
  const collectionCalls: string[] = []
  const readTierCalls: string[] = []
  g.on('request', (req) => {
    const url = req.url()
    if (url.includes('/v1/m/sessions/channels/administration'))
      collectionCalls.push(`${req.method()} ${url}`)
    if (
      /\/v1\/m\/sessions\/channels\/[0-9a-f-]{36}(\?|$)/i.test(url) &&
      req.method() === 'GET'
    )
      readTierCalls.push(`${req.method()} ${url}`)
  })

  await uiLogin(g, G_EMAIL, MEMBER_PASSWORD)
  await g.goto('/communications/administration')
  await selectWorkspace(g, 'Billing')

  // (1) the exact administration SURFACE question for this workspace, answered reachable.
  const surface = await capabilityAnswerFor(g, CAP_SURFACE)
  expect(surface.state).toBe('reachable')
  expect(surface.code).toBe('admitted')
  expect(
    Number.isInteger(surface.refresh_after_ms) &&
      surface.refresh_after_ms! > 0 &&
      surface.refresh_after_ms! <= 30_000,
    'a positive carries a finite budget in (0, 30000]',
  ).toBe(true)
  const surfaceAsk = asked.find((q) => q.operation === CAP_SURFACE)
  expect(surfaceAsk?.kind).toBe('surface')
  expect(surfaceAsk?.workspace_id).toBe(wsBilling)
  expect(
    surfaceAsk?.selectors,
    'a collection carries no entity locator',
  ).toBeUndefined()

  // (2) the navigation link is offered despite the false reflection.
  await expect(
    g.getByRole('link', { name: 'Channel administration' }).first(),
  ).toBeVisible()

  // (3) the Administration tab is selected and the REAL collection loads.
  await expect(
    g.getByRole('tab', { name: 'Administration', selected: true }),
  ).toBeVisible()
  await expect(
    g.getByRole('heading', { name: 'Channel administration' }),
  ).toBeVisible()
  const row = g.getByRole('row').filter({ hasText: adminSlug })
  await expect(row).toHaveCount(1)
  await shot(g, 'g1b-1-collection')

  // (4) the row asks the EXACT grant-sheet operation with its path locator, and the real
  //     grants GET succeeds.
  const grantsRead = g.waitForResponse(
    (r) =>
      r.url().includes(`/v1/m/sessions/channels/${adminChannelId}/grants`) &&
      r.request().method() === 'GET',
  )
  await row.click()
  const sheetAnswer = await capabilityAnswerFor(g, CAP_SHEET)
  expect(sheetAnswer.state).toBe('allowed')
  expect(sheetAnswer.code).toBe('authorized')
  const sheetAsk = asked.find((q) => q.operation === CAP_SHEET)
  expect(sheetAsk?.kind).toBe('operation')
  expect(sheetAsk?.selectors?.path).toEqual({ id: adminChannelId })
  expect(sheetAsk?.workspace_id).toBe(wsBilling)
  const grantsRes = await grantsRead
  note('GET grants as G', grantsRes.status())
  expect(grantsRes.status()).toBe(200)
  const sheet = g.getByRole('dialog')
  await expect(sheet.locator('[data-slot="channel-admin-sheet"]')).toHaveCount(
    0,
  )
  await expect(g.locator('[data-slot="channel-admin-sheet"]')).toBeVisible()
  await expect(sheet.locator('[data-slot="admin-etag"]')).toBeVisible()
  await shot(g, 'g1b-1-sheet')

  // (5) edit, review, confirm — and the exact PATCH capability question must be asked
  //     BEFORE the real PATCH leaves.
  const newName = `G1-B renamed ${STAMP}`
  await sheet.getByRole('textbox', { name: 'Name', exact: true }).fill(newName)
  await sheet.getByRole('button', { name: 'Review changes' }).click()
  const patchAskedBefore = asked.filter((q) => q.operation === CAP_PATCH).length
  const patchCapability = capabilityAnswerFor(g, CAP_PATCH)
  const realPatch = g.waitForResponse(
    (r) =>
      r.url().includes('/v1/m/sessions/channels') &&
      r.request().method() === 'PATCH',
  )
  await sheet.getByRole('button', { name: 'Confirm changes' }).click()
  const patchAnswer = await patchCapability
  expect(patchAnswer.state).toBe('allowed')
  expect(patchAnswer.code).toBe('authorized')
  const patchRes = await realPatch
  note('PATCH channel as G', patchRes.status())
  expect(patchRes.status()).toBe(200)
  // The question travelled with the BODY locator the route declares, not a path one.
  const patchAsk = asked.filter((q) => q.operation === CAP_PATCH).at(-1)
  expect(patchAsk?.kind).toBe('operation')
  expect(patchAsk?.selectors?.body).toEqual({ channel_id: adminChannelId })
  expect(patchAsk?.selectors?.path).toBeUndefined()
  expect(
    asked.filter((q) => q.operation === CAP_PATCH).length,
    'confirming forced a NEW exact observation',
  ).toBeGreaterThan(patchAskedBefore)

  // (6) the receipt of the REAL mutation.
  await expect(g.getByText('Configuration applied')).toBeVisible()
  await shot(g, 'g1b-1-applied')

  // The engine really changed: read back through the administrative route, as G.
  const after = await apiRead(
    request,
    tokenG,
    `/v1/m/sessions/channels/${adminChannelId}/grants?workspace_id=${wsBilling}&limit=1`,
  )
  expect(after.status).toBe(200)
  expect((after.json as { channel: { name: string } }).channel.name).toBe(
    newName,
  )

  // The command palette offers the same door under the same positive.
  //
  // ⛔ THE PREFIX IS DELIMITED, AND THE DELIMITER IS THE ASSERTION. `cmdk` writes
  //    `data-value` verbatim from the item's own value, and the palette composes that value
  //    as `<kind>:<id> <label>` (command-menu.tsx). An EXACT match therefore never matched
  //    anything — measured: this step was reached for the first time on 2026-09-07 and
  //    failed with the palette open and the item rendered — and a bare prefix would also
  //    accept a future `view:communicationsAdministrationSomething`. The trailing space is
  //    what makes the prefix name exactly one id, and the count is asserted so the locator
  //    cannot quietly start matching two.
  await g.keyboard.press('Control+k')
  const paletteItem = g.locator(
    '[data-value^="view:communicationsAdministration "]',
  )
  await expect(paletteItem).toHaveCount(1)
  await expect(paletteItem).toBeVisible()
  await g.keyboard.press('Escape')

  evidence.g1bWorkspacePhase = {
    whoami_reflects_channel_admin: reflectsAdmin,
    content_read_status: contentRead.status,
    administration_before_policy: beforeAdmin.status,
    surface: surface,
    sheet: sheetAnswer,
    patch: patchAnswer,
    questions: asked.map((q) => ({
      kind: q.kind,
      operation: q.operation,
      selectors: q.selectors,
    })),
    read_tier_channel_gets: readTierCalls,
    renamed_to: newName,
  }
  // Administration never went through the read-tier channel GET, in the whole phase.
  expect(readTierCalls, 'administration is not a read').toEqual([])
  await g.context().close()
})

// ─── G1-B/2 · entity-only permit: the sheet opens, the collection never does ───────
test('G1-B — an ENTITY-ONLY permit opens the exact deep-linked sheet while the collection stays not_reachable, and the administration collection is never requested', async ({
  browser,
  request,
}) => {
  test.setTimeout(600_000)

  // The policy is REPLACED, not added to: this phase owns its source, so the previous
  // workspace-wide permit is gone rather than merely shadowed.
  await publishAuthored(
    request,
    `permit(principal in User::"${userG}", action == Action::"sessions:channel:admin",` +
      ` resource == Resource::"${adminChannelId}");`,
  )

  // Measured on the real routes first, so the browser is confirming an engine state that
  // was established independently of it.
  const collectionRefused = await apiRead(
    request,
    tokenG,
    `/v1/m/sessions/channels/administration?workspace_id=${wsBilling}&limit=1`,
  )
  note('GET administration as G (entity-only policy)', collectionRefused.status)
  expect(collectionRefused.status, 'the collection is refused').toBe(403)
  const sheetAllowed = await apiRead(
    request,
    tokenG,
    `/v1/m/sessions/channels/${adminChannelId}/grants?workspace_id=${wsBilling}&limit=1`,
  )
  note('GET grants as G (entity-only policy)', sheetAllowed.status)
  expect(sheetAllowed.status, 'the exact row is allowed').toBe(200)

  const g = await (await browser.newContext()).newPage()
  const asked = recordCapabilityQuestions(g)
  const collectionCalls: string[] = []
  const readTierCalls: string[] = []
  g.on('request', (req) => {
    const url = req.url()
    if (url.includes('/v1/m/sessions/channels/administration'))
      collectionCalls.push(`${req.method()} ${url}`)
    if (
      /\/v1\/m\/sessions\/channels\/[0-9a-f-]{36}(\?|$)/i.test(url) &&
      req.method() === 'GET'
    )
      readTierCalls.push(`${req.method()} ${url}`)
  })

  await uiLogin(g, G_EMAIL, MEMBER_PASSWORD)
  // Select the workspace on a page that carries NO deep link, so the surface question is
  // the one asked here and the collection's refusal is observed on its own.
  await g.goto('/communications')
  await selectWorkspace(g, 'Billing')
  const surface = await capabilityAnswerFor(g, CAP_SURFACE)
  expect(surface.state).toBe('not_reachable')
  expect(surface.code).toBe('not_permitted')
  expect(
    surface.refresh_after_ms,
    'a refusal carries no budget',
  ).toBeUndefined()
  // The door is gone from navigation on an ESTABLISHED refusal.
  await expect(
    g.getByRole('link', { name: 'Channel administration' }),
  ).toHaveCount(0)

  // ⛔ AND THE EXACT DEEP LINK STILL OPENS. Admitted to one row and refused the list is
  //    the ordinary shape of a scoped grant, not a contradiction.
  const grantsRead = g.waitForResponse(
    (r) =>
      r.url().includes(`/v1/m/sessions/channels/${adminChannelId}/grants`) &&
      r.request().method() === 'GET',
  )
  await g.goto(`/communications/administration?admin_channel=${adminChannelId}`)
  const sheetAnswer = await capabilityAnswerFor(g, CAP_SHEET)
  expect(sheetAnswer.state).toBe('allowed')
  expect(sheetAnswer.code).toBe('authorized')
  const grantsRes = await grantsRead
  note('GET grants as G (deep link)', grantsRes.status())
  expect(grantsRes.status()).toBe(200)
  await expect(g.locator('[data-slot="channel-admin-sheet"]')).toBeVisible()
  await expect(g.locator('[data-slot="admin-etag"]')).toBeVisible()
  await shot(g, 'g1b-2-entity-only-sheet')

  // THE TWO CONTROLS THIS PHASE EXISTS FOR.
  expect(
    collectionCalls,
    'the administration COLLECTION was never requested',
  ).toEqual([])
  expect(readTierCalls, 'no read-tier channel GET was made either').toEqual([])
  // And the console did ask the entity question — so "no collection call" is not passing
  // because nothing happened at all.
  expect(asked.some((q) => q.operation === CAP_SHEET)).toBe(true)

  evidence.g1bEntityPhase = {
    collection_route_status: collectionRefused.status,
    sheet_route_status: sheetAllowed.status,
    surface,
    sheet: sheetAnswer,
    collection_calls: collectionCalls,
    read_tier_channel_gets: readTierCalls,
    questions: asked.map((q) => ({
      kind: q.kind,
      operation: q.operation,
      selectors: q.selectors,
    })),
  }
  await g.context().close()
})
