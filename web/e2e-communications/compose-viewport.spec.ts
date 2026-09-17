// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// K3 compose dialog — viewport and keyboard. A live engine booted by the existing
// I1 harness (global-setup.ts: custody, `--seed-demo`, activation, second boot).
// NOTHING on the accepted path is intercepted. Geometry is measured against the
// real viewport box, not against CSS class names: a control counts as reachable
// when it is visible, inside the viewport, and a pointer trial-click succeeds.
import {
  expect,
  test,
  type APIRequestContext,
  type Locator,
  type Page,
} from '@playwright/test'
import { copyFileSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import path from 'node:path'

const BASE = process.env.K3_E2E_BASE ?? ''
const TENANT = process.env.DEMO_TENANT ?? ''
const WORK = process.env.K3_E2E_WORK ?? ''
const DEMO_EMAIL = 'demo@olivares.local'
const DEMO_PASSWORD = 'olivares-demo-estate'
const A_EMAIL = 'k3-vp-a@olivares.local'
const B_EMAIL = 'k3-vp-b@olivares.local'
const MEMBER_PASSWORD = 'k3-vp-member-passphrase-42'
const STAMP = Date.now().toString(36)
const PHASE = process.env.COMPOSE_VIEWPORT_PHASE ?? 'after'
const STRICT = PHASE !== 'before'
// This spec's evidence (screenshots, measurements) lives in the harness's own per-run
// work directory by default — K3_E2E_WORK, exported by global-setup.ts beside the
// engine log and identity — never in a coordination directory outside the repository.
// COMPOSE_VIEWPORT_EVIDENCE is the explicit override; when it points somewhere else,
// the run's work directory keeps a copy. Nothing is ever copied onto itself.
const WORK_EVIDENCE = WORK ? path.join(WORK, 'evidence-compose-viewport') : ''
const EVIDENCE_ROOT = process.env.COMPOSE_VIEWPORT_EVIDENCE ?? WORK_EVIDENCE
const MIRROR =
  WORK_EVIDENCE !== '' &&
  path.resolve(WORK_EVIDENCE) !== path.resolve(EVIDENCE_ROOT)
const MOBILE = { width: 390, height: 640 }
const DESKTOP = { width: 1280, height: 720 }
const EXTRA_TEXT_BLOCKS = 8
const LONG_SUBJECT =
  'Viewport-probe subject with a long unbroken token ' + 'X'.repeat(80)
const LONG_TEXT =
  'Long body for the compose viewport probe.\n' +
  'unbroken-' +
  'supercalifragilisticexpialidocious'.repeat(8) +
  '\nSecond paragraph of the same block, still part of one notice.'

test.describe.configure({ mode: 'serial' })

let tokenAdmin = ''
let tokenA = ''
let tokenB = ''
let userA = ''
let userB = ''
let wsBilling = ''
let channelId = ''
const channelSlug = `k3-vp-${STAMP}`
const measurements: Record<string, unknown> = {}
const evidence: Record<string, unknown> = { phase: PHASE, stamp: STAMP }

function evidenceDir(): string {
  const dir = path.join(EVIDENCE_ROOT, 'screenshots', PHASE)
  mkdirSync(dir, { recursive: true })
  return dir
}

async function shot(page: Page, name: string) {
  const file = path.join(evidenceDir(), `${name}.png`)
  // Exact viewport, not a stitched full page: overflow would disappear.
  await page.screenshot({ path: file, fullPage: false })
  if (MIRROR) {
    mkdirSync(WORK_EVIDENCE, { recursive: true })
    copyFileSync(file, path.join(WORK_EVIDENCE, `${name}.png`))
  }
}

async function api(
  request: APIRequestContext,
  token: string,
  method: 'GET' | 'POST',
  route: string,
  body?: unknown,
) {
  const res = await request.fetch(`${BASE}${route}`, {
    method,
    headers: {
      Authorization: `Bearer ${token}`,
      'X-Olivares-Tenant': TENANT,
      ...(body !== undefined ? { 'Content-Type': 'application/json' } : {}),
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
  if (res.status() === 429) {
    const after = Number(res.headers()['retry-after'] ?? '1')
    await new Promise((r) =>
      setTimeout(
        r,
        (Number.isFinite(after) ? Math.max(after, 1) : 1) * 1000 + 50,
      ),
    )
    return api(request, token, method, route, body)
  }
  return { status: res.status(), json, headers: res.headers() }
}

async function apiRead(
  request: APIRequestContext,
  token: string,
  route: string,
) {
  let last = await api(request, token, 'GET', route)
  for (let i = 0; i < 8 && last.status === 503; i++) {
    await new Promise((r) => setTimeout(r, 1500))
    last = await api(request, token, 'GET', route)
  }
  return last
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

function composeDialog(page: Page): Locator {
  return page.locator('[data-slot="compose-dialog"]')
}

/** Footer Close, not the top-right X (both have the accessible name «Close»). */
function actionClose(dialog: Locator): Locator {
  return dialog.getByRole('button', { name: 'Close' }).first()
}

type Box = { x: number; y: number; w: number; h: number }

function inViewport(box: Box | null, vp: { width: number; height: number }) {
  if (!box) return false
  return (
    box.y >= -0.5 &&
    box.x >= -0.5 &&
    box.y + box.h <= vp.height + 0.5 &&
    box.x + box.w <= vp.width + 0.5
  )
}

async function boxOf(locator: Locator): Promise<Box | null> {
  const b = await locator.boundingBox()
  if (!b) return null
  return { x: b.x, y: b.y, w: b.width, h: b.height }
}

async function measureDialog(page: Page, label: string) {
  const dialog = composeDialog(page)
  const vp = page.viewportSize() ?? { width: 0, height: 0 }
  const geom = await dialog.evaluate((el) => {
    const r = el.getBoundingClientRect()
    return {
      x: r.x,
      y: r.y,
      w: r.width,
      h: r.height,
      top: r.top,
      bottom: r.bottom,
      right: r.right,
      left: r.left,
      scrollWidth: el.scrollWidth,
      clientWidth: el.clientWidth,
      scrollHeight: el.scrollHeight,
      clientHeight: el.clientHeight,
      docScrollWidth: document.documentElement.scrollWidth,
      innerWidth: window.innerWidth,
      innerHeight: window.innerHeight,
    }
  })
  const title = dialog.getByRole('heading', { name: 'Send a direct notice' })
  const cancel = dialog.getByRole('button', { name: 'Cancel' })
  const compose = dialog.getByRole('button', { name: /^Compose$/ })
  const confirm = dialog.getByRole('button', { name: 'Confirm and send' })
  const discard = dialog.getByRole('button', {
    name: 'Discard this intention and edit',
  })
  const close = actionClose(dialog)
  const addBlock = dialog.getByRole('button', { name: 'Add block' })
  const pick = async (loc: Locator) => {
    const visible = await loc.isVisible().catch(() => false)
    if (!visible) return { visible: false, box: null, inView: false }
    const box = await boxOf(loc)
    return { visible: true, box, inView: inViewport(box, vp) }
  }
  const record = {
    label,
    viewport: vp,
    dialog: geom,
    dialogFitsViewport:
      geom.top >= -0.5 &&
      geom.bottom <= vp.height + 0.5 &&
      geom.left >= -0.5 &&
      geom.right <= vp.width + 0.5,
    overflowX:
      geom.scrollWidth > geom.clientWidth + 1 ||
      geom.docScrollWidth > geom.innerWidth + 1,
    title: await pick(title),
    cancel: await pick(cancel),
    compose: await pick(compose),
    confirm: await pick(confirm),
    discard: await pick(discard),
    close: await pick(close),
    addBlock: await pick(addBlock),
  }
  measurements[label] = record
  return record
}

async function trialClick(locator: Locator): Promise<boolean> {
  try {
    await locator.click({ trial: true, timeout: 5_000 })
    return true
  } catch {
    return false
  }
}

async function activate(
  locator: Locator,
  name: string,
): Promise<{ trial: boolean; usedKeyboard: boolean }> {
  const trial = await trialClick(locator)
  measurements[`${name}.trial`] = trial
  if (STRICT) {
    expect(trial, `${name} is pointer-reachable in the viewport`).toBe(true)
    await locator.click()
    return { trial, usedKeyboard: false }
  }
  if (trial) {
    await locator.click()
    return { trial, usedKeyboard: false }
  }
  await locator.focus()
  await locator.page().keyboard.press('Enter')
  return { trial, usedKeyboard: true }
}

async function assertNoHorizontalOverflow(label: string) {
  const rec = measurements[label] as { overflowX?: boolean }
  if (STRICT) expect(rec.overflowX, `${label} horizontal overflow`).toBe(false)
}

async function openCompose(page: Page) {
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
  await row.click()
  const sheet = page.getByRole('dialog').filter({ hasText: 'Channel' }).first()
  const send = sheet.getByRole('button', { name: 'Send notice' })
  await expect(send).toBeVisible()
  return { sheet, send }
}

async function fillTallDraft(dialog: Locator, page: Page) {
  await dialog.getByRole('textbox', { name: 'Reference (ID)' }).fill(userB)
  await dialog.getByLabel(/^Subject/).fill(LONG_SUBJECT)
  const firstText = dialog.getByLabel(/^Text$/).first()
  await firstText.fill(LONG_TEXT)
  for (let i = 0; i < EXTRA_TEXT_BLOCKS; i++) {
    const add = dialog.getByRole('button', { name: 'Add block' })
    await expect(add).toBeEnabled()
    await add.click()
  }
  const drafts = dialog.locator('[data-slot="block-draft"]')
  const n = await drafts.count()
  expect(n).toBeGreaterThanOrEqual(1 + EXTRA_TEXT_BLOCKS)
  // Keep every block type the engine admits: text (already), plus one each of
  // reference, status and action_ref. The rest stay text with long bodies.
  const typed: Array<{
    index: number
    option: string
    fill: () => Promise<void>
  }> = [
    {
      index: 1,
      option: 'Reference',
      fill: async () => {
        const card = drafts.nth(1)
        await card.getByLabel('Reference kind').fill('work_item')
        await card.getByLabel(/^Reference$/).fill('wi-viewport-1')
        await card
          .getByLabel('Reference hash')
          .fill('sha256:' + 'ab'.repeat(32))
      },
    },
    {
      index: 2,
      option: 'Status',
      fill: async () => {
        await drafts.nth(2).getByLabel('Code').fill('in_review')
      },
    },
    {
      index: 3,
      option: 'Action reference',
      fill: async () => {
        const card = drafts.nth(3)
        await card.getByLabel('Reference kind').fill('runbook')
        await card.getByLabel(/^Reference$/).fill('rb-viewport-1')
      },
    },
  ]
  for (const step of typed) {
    const type = drafts.nth(step.index).getByLabel('Type')
    await type.click()
    await page.getByRole('option', { name: step.option, exact: true }).click()
    await step.fill()
  }
  for (let i = 4; i < n; i++) {
    const text = drafts.nth(i).getByLabel(/^Text$/)
    if (await text.isVisible())
      await text.fill(`Block ${i + 1} of the tall draft.\n${LONG_TEXT}`)
  }
  evidence.blocks_in_draft = n
  const add = dialog.getByRole('button', { name: 'Add block' })
  await expect(add).toBeEnabled()
}

test.beforeAll(async ({ request }) => {
  expect(BASE, 'K3_E2E_BASE is set by the global setup').not.toBe('')
  expect(TENANT, 'DEMO_TENANT is set by the global setup').not.toBe('')
  expect(
    EVIDENCE_ROOT,
    'an evidence directory: K3_E2E_WORK from the global setup, or COMPOSE_VIEWPORT_EVIDENCE',
  ).not.toBe('')
  tokenAdmin = (await login(request, DEMO_EMAIL, DEMO_PASSWORD)).token
  const ws = await api(request, tokenAdmin, 'GET', '/v1/workspaces?limit=100')
  expect(ws.status).toBe(200)
  const items = (
    ws.json as { items: { id: string; slug: string; is_default: boolean }[] }
  ).items
  wsBilling = items.find((w) => w.slug === 'billing')?.id ?? ''
  expect(wsBilling, 'the seeded Billing workspace').not.toBe('')
  userA = await createMember(request, A_EMAIL, 'owner')
  userB = await createMember(request, B_EMAIL, 'editor')
  tokenA = (await login(request, A_EMAIL, MEMBER_PASSWORD)).token
  tokenB = (await login(request, B_EMAIL, MEMBER_PASSWORD)).token
  const ready = await apiRead(
    request,
    tokenA,
    `/v1/m/sessions/channels?workspace_id=${wsBilling}&limit=1`,
  )
  expect(ready.status, 'K3 readiness (catalog as A)').toBe(200)
  const created = await api(
    request,
    tokenA,
    'POST',
    '/v1/m/sessions/channels',
    {
      workspace_id: wsBilling,
      slug: channelSlug,
      name: 'Compose viewport',
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
  expect(created.status, 'create channel').toBe(201)
  channelId = (created.json as { channel: { id: string } }).channel.id
  evidence.actors = { a: userA, b: userB }
  evidence.channel = { id: channelId, slug: channelSlug }
})

test.afterAll(async ({ browser }) => {
  evidence.browser = {
    name: browser.browserType().name(),
    version: browser.version(),
  }
  evidence.measurements = measurements
  if (WORK) {
    try {
      evidence.identity = JSON.parse(
        readFileSync(path.join(WORK, 'identity.json'), 'utf8'),
      )
    } catch {
      evidence.identity = null
    }
  }
  mkdirSync(EVIDENCE_ROOT, { recursive: true })
  writeFileSync(
    path.join(EVIDENCE_ROOT, `measurements-${PHASE}.json`),
    JSON.stringify(evidence, null, 2),
  )
  if (MIRROR) {
    mkdirSync(WORK_EVIDENCE, { recursive: true })
    writeFileSync(
      path.join(WORK_EVIDENCE, `measurements-${PHASE}.json`),
      JSON.stringify(evidence, null, 2),
    )
  }
})

test('mobile 390x640: tall draft keeps header and footer reachable; real send', async ({
  browser,
  request,
}) => {
  const page = await (
    await browser.newContext({
      viewport: MOBILE,
      isMobile: true,
      hasTouch: true,
    })
  ).newPage()
  await uiLogin(page, A_EMAIL, MEMBER_PASSWORD)
  const { send } = await openCompose(page)
  await send.click()
  const dialog = composeDialog(page)
  await expect(
    dialog.getByRole('heading', { name: 'Send a direct notice' }),
  ).toBeVisible()

  // Validation errors are part of the tall body the I2 note named.
  await dialog.getByRole('button', { name: /^Compose$/ }).click()
  await expect(dialog.getByRole('alert')).toBeVisible()
  await measureDialog(page, 'mobile-edit-errors')
  await shot(page, '01-mobile-edit-errors')

  await fillTallDraft(dialog, page)
  const edit = await measureDialog(page, 'mobile-edit-many-blocks')
  await shot(page, '02-mobile-edit-many-blocks')
  await assertNoHorizontalOverflow('mobile-edit-many-blocks')
  if (STRICT) {
    expect(edit.title.inView, 'title stays in the 390x640 viewport').toBe(true)
    expect(edit.compose.inView, 'Compose stays in the 390x640 viewport').toBe(
      true,
    )
    expect(edit.cancel.inView, 'Cancel stays in the 390x640 viewport').toBe(
      true,
    )
    expect(edit.dialogFitsViewport, 'dialog box fits 390x640').toBe(true)
  }

  const composed = await activate(
    dialog.getByRole('button', { name: /^Compose$/ }),
    'mobile-compose',
  )
  measurements['mobile-compose.usedKeyboard'] = composed.usedKeyboard
  await expect(dialog.getByText('Confirm the intention')).toBeVisible()
  const confirmM = await measureDialog(page, 'mobile-confirm')
  await shot(page, '03-mobile-confirm')
  if (STRICT) {
    expect(confirmM.confirm.inView, 'Confirm stays in view after review').toBe(
      true,
    )
    expect(confirmM.discard.inView, 'Discard stays in view after review').toBe(
      true,
    )
    expect(confirmM.dialogFitsViewport, 'confirm dialog fits 390x640').toBe(
      true,
    )
  }

  const sendResponse = page.waitForResponse((r) =>
    r.url().endsWith('/v1/m/sessions/messages/send'),
  )
  const confirmed = await activate(
    dialog.getByRole('button', { name: 'Confirm and send' }),
    'mobile-confirm-send',
  )
  measurements['mobile-confirm-send.usedKeyboard'] = confirmed.usedKeyboard
  const sent = await sendResponse
  expect(sent.status(), 'real send').toBe(201)
  const body = (await sent.json()) as {
    message_id: string
    delivery_id: string
    replayed: boolean
  }
  expect(body.replayed).toBe(false)
  await expect(dialog.getByText('Notice published').first()).toBeVisible()
  const receipt = await measureDialog(page, 'mobile-receipt')
  await shot(page, '04-mobile-receipt')
  if (STRICT) {
    expect(receipt.close.inView, 'Close stays in view after the receipt').toBe(
      true,
    )
    expect(receipt.dialogFitsViewport, 'receipt dialog fits 390x640').toBe(true)
  }
  const closed = await activate(actionClose(dialog), 'mobile-close')
  measurements['mobile-close.usedKeyboard'] = closed.usedKeyboard
  await expect(dialog).toBeHidden()

  const inboxB = await apiRead(
    request,
    tokenB,
    `/v1/m/sessions/inbox?workspace_id=${wsBilling}&limit=50`,
  )
  expect(inboxB.status).toBe(200)
  const itemsB = (inboxB.json as { items: { delivery: { id: string } }[] })
    .items
  expect(itemsB.map((i) => i.delivery.id)).toContain(body.delivery_id)
  evidence.notice = {
    message_id: body.message_id,
    delivery_id: body.delivery_id,
  }
  await page.context().close()
})

test('desktop: tall draft, review and receipt keep actions reachable', async ({
  browser,
}) => {
  const page = await (await browser.newContext({ viewport: DESKTOP })).newPage()
  await uiLogin(page, A_EMAIL, MEMBER_PASSWORD)
  const { send } = await openCompose(page)
  await send.click()
  const dialog = composeDialog(page)
  await expect(
    dialog.getByRole('heading', { name: 'Send a direct notice' }),
  ).toBeVisible()
  await fillTallDraft(dialog, page)
  const edit = await measureDialog(page, 'desktop-edit-many-blocks')
  await shot(page, '05-desktop-edit-many-blocks')
  await assertNoHorizontalOverflow('desktop-edit-many-blocks')
  if (STRICT) {
    expect(edit.compose.inView, 'desktop Compose in view').toBe(true)
    expect(edit.dialogFitsViewport, 'desktop dialog fits').toBe(true)
  }
  await activate(
    dialog.getByRole('button', { name: /^Compose$/ }),
    'desktop-compose',
  )
  await expect(dialog.getByText('Confirm the intention')).toBeVisible()
  const confirmM = await measureDialog(page, 'desktop-confirm')
  await shot(page, '06-desktop-confirm')
  if (STRICT) {
    expect(confirmM.confirm.inView, 'desktop Confirm in view').toBe(true)
    expect(confirmM.discard.inView, 'desktop Discard in view').toBe(true)
  }
  await activate(
    dialog.getByRole('button', { name: 'Discard this intention and edit' }),
    'desktop-discard',
  )
  await expect(dialog.getByLabel(/^Subject/)).toBeVisible()
  const after = await measureDialog(page, 'desktop-after-discard')
  await shot(page, '07-desktop-after-discard')
  if (STRICT) expect(after.compose.inView, 'Compose back in view').toBe(true)
  await page.context().close()
})

test('keyboard: focus stays in the compose dialog and returns to Send notice', async ({
  browser,
}) => {
  const page = await (
    await browser.newContext({
      viewport: MOBILE,
      isMobile: true,
      hasTouch: true,
    })
  ).newPage()
  await uiLogin(page, A_EMAIL, MEMBER_PASSWORD)
  const { send } = await openCompose(page)
  await send.focus()
  await page.keyboard.press('Enter')
  const dialog = composeDialog(page)
  await expect(
    dialog.getByRole('heading', { name: 'Send a direct notice' }),
  ).toBeVisible()
  for (let i = 0; i < 8; i++) await page.keyboard.press('Tab')
  const inside = await page.evaluate(() => {
    const active = document.activeElement
    if (!(active instanceof HTMLElement)) return false
    const dialogs = [...document.querySelectorAll('[role="dialog"]')]
    const compose = dialogs.find((d) =>
      d.textContent?.includes('Send a direct notice'),
    )
    return !!(compose && compose.contains(active))
  })
  expect(inside, 'Tab stays inside the compose dialog').toBe(true)
  await measureDialog(page, 'mobile-keyboard-trap')
  await shot(page, '08-mobile-keyboard-trap')
  await page.keyboard.press('Escape')
  await expect(dialog).toBeHidden()
  const returned = await page.evaluate(
    () =>
      (document.activeElement as HTMLElement | null)?.textContent?.trim() ?? '',
  )
  measurements['keyboard.returned'] = returned
  if (STRICT) expect(returned).toBe('Send notice')
  await page.context().close()
})
