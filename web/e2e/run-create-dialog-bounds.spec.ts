// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import {
  expect,
  test as base,
  type APIRequestContext,
  type Locator,
  type Page,
  type APIResponse,
} from '@playwright/test'

/** This oracle owns one uniquely named run through a test-scoped teardown.
 * Root's launcher separately owns the engine/browser processes. No failed read
 * establishes absence, ownership or cleanup. No runtime qualification is implied
 * by compiling this file.
 */
const EMAIL = process.env.E2E_EMAIL ?? ''
const PASSWORD = process.env.E2E_PASSWORD ?? ''
const PROFILE_REF = process.env.E2E_PROFILE_REF ?? ''
/** Painted as `<display_name or profile_ref> · <driver>`; the fixture supplies it whole. */
const PROFILE_LABEL = process.env.E2E_PROFILE_LABEL ?? ''
/** Root's fixture guarantees uniqueness; it is how an UNKNOWN effect is resolved. */
const RUN_NAME = process.env.E2E_RUN_NAME ?? ''

const RUNS_PATH = '/v1/m/sessions/runs'
const ORIGIN = new URL(process.env.PLAYWRIGHT_BASE_URL ?? 'http://127.0.0.1:8456').origin
const CLEANUP_BUDGET_MS = 30_000
const CALL_TIMEOUT_MS = 5_000
/** RunState (types.ts): pending | running | idle | stopped | failed | cleaned. */
const TERMINAL: readonly string[] = ['stopped', 'failed', 'cleaned']

const VIEWPORTS = [
  { name: '1920x1080', width: 1920, height: 1080 },
  { name: '1280x720', width: 1280, height: 720 },
  { name: '390x844', width: 390, height: 844 },
] as const

class Inability extends Error {}
const deadlineAfter = (milliseconds: number) => performance.now() + milliseconds
function timeLeft(deadline: number, what: string) {
  const left = Math.floor(deadline - performance.now())
  if (left <= 0) throw new Inability(`${what}: deadline exhausted`)
  return Math.min(CALL_TIMEOUT_MS, left)
}
async function within<T>(promise: Promise<T>, deadline: number, what: string): Promise<T> {
  let timer: ReturnType<typeof setTimeout> | undefined
  const milliseconds = deadline - performance.now()
  if (milliseconds <= 0) {
    // The caller's promise may already have started. Observe its rejection.
    void promise.catch(() => undefined)
    throw new Inability(`${what}: deadline exhausted`)
  }
  try {
    return await Promise.race([
      promise,
      new Promise<never>((_, reject) => {
        timer = setTimeout(() => reject(new Inability(`${what}: deadline exhausted`)), milliseconds)
      }),
    ])
  } finally {
    clearTimeout(timer)
  }
}

type RunRow = { run_ref: string; name?: string; provider_profile_ref?: string; state: string }
const isRun = (v: unknown): v is RunRow => {
  if (!v || typeof v !== 'object') return false
  const r = v as Partial<RunRow>
  return typeof r.run_ref === 'string' && r.run_ref.length > 0 &&
    typeof r.state === 'string' &&
    (r.name === undefined || typeof r.name === 'string') &&
    (r.provider_profile_ref === undefined || typeof r.provider_profile_ref === 'string')
}
const isRunList = (v: unknown): v is { items: RunRow[] } =>
  !!v && typeof v === 'object' && Array.isArray((v as { items?: unknown }).items) &&
  (v as { items: unknown[] }).items.every(isRun)
const matchesFixture = (r: RunRow) => r.name === RUN_NAME && r.provider_profile_ref === PROFILE_REF
const matching = (rows: RunRow[]) => rows.filter(matchesFixture).map((r) => r.run_ref).sort()
async function checkedBody<T>(response: APIResponse, shape: (v: unknown) => v is T, what: string) {
  if (!response.ok()) throw new Inability(`${what}: HTTP ${response.status()}`)
  let body: unknown
  try { body = await response.json() } catch { throw new Inability(`${what}: unreadable JSON`) }
  if (!shape(body)) throw new Inability(`${what}: invalid response shape`)
  return body
}
async function readChecked<T>(
  api: APIRequestContext, path: string, shape: (v: unknown) => v is T,
  what: string, deadline = deadlineAfter(CALL_TIMEOUT_MS),
): Promise<T> {
  try {
    return await checkedBody(await api.get(`${ORIGIN}${path}`, {
      timeout: timeLeft(deadline, what),
    }), shape, what)
  } catch (error) {
    throw new Inability(`${what}: ${error}`)
  }
}
async function exactOwnedRun(api: APIRequestContext, ref: string, deadline: number) {
  const row = await readChecked(api, `${RUNS_PATH}/${encodeURIComponent(ref)}`, isRun, 'owned run', deadline)
  if (row.run_ref !== ref || !matchesFixture(row)) throw new Inability('owned run: reference or fixture identity differs')
  return row
}

type CreateObservation = { status: number; body: RunRow; sentName: unknown; sentProfile: unknown }
/** Private lifecycle interface: arm once, observe, settle once from fixture teardown. */
class OwnedLaunch {
  calls = 0
  private observation?: Promise<{ value: CreateObservation } | { error: unknown }>
  private possibleEffect = false
  private closed = false
  private pointer?: Promise<{ error?: unknown }>
  private readonly page: Page
  // A declared field, not a parameter property: the suites are type-checked with
  // erasable syntax only, so a constructor cannot also declare state.
  constructor(page: Page) {
    this.page = page
  }

  readonly onRequest = (request: import('@playwright/test').Request) => {
    const url = new URL(request.url())
    if (request.method() === 'POST' && url.origin === ORIGIN && url.pathname === RUNS_PATH) {
      this.calls++
      this.possibleEffect = true
    }
  }

  private arm() {
    if (this.closed || this.observation) throw new Error('admission is closed or already armed')
    const deadline = deadlineAfter(20_000)
    this.observation = (async () => {
      const response = await this.page.waitForResponse((r) => {
        const url = new URL(r.url())
        return r.request().method() === 'POST' && url.origin === ORIGIN && url.pathname === RUNS_PATH
      }, { timeout: 20_000 })
      const body: unknown = await within(response.json(), deadline, 'create response body')
      if (!isRun(body)) throw new Inability('create response: invalid run shape')
      const sent = response.request().postDataJSON() as Record<string, unknown> | null
      return { status: response.status(), body, sentName: sent?.name, sentProfile: sent?.provider_profile_ref }
    })().then(value => ({ value }), error => ({ error }))
  }

  async create(submit: Locator) {
    // All authority to start a pointer lives here; a body continuing after its
    // timeout cannot submit after fixture teardown has closed this owner.
    if (this.closed) throw new Inability('admission owner is closed')
    this.arm()
    this.possibleEffect = true
    this.pointer = submit.click({ timeout: CALL_TIMEOUT_MS }).then(
      () => ({}), error => ({ error }),
    )
    const click = await this.pointer
    if (click.error) throw click.error
    await this.admitted()
  }

  private async admitted() {
    if (!this.observation) throw new Error('create observer not armed')
    const observed = await this.observation
    if ('error' in observed) throw new Inability(`create response: ${observed.error}`)
    const value = observed.value
    expect(value.status, 'HTTP 201 Created').toBe(201)
    expect(value.sentName, 'submitted name').toBe(RUN_NAME)
    expect(value.sentProfile, 'submitted profile').toBe(PROFILE_REF)
    expect(matchesFixture(value.body), 'echoed fixture identity').toBe(true)
    const row = await exactOwnedRun(this.page.request, value.body.run_ref, deadlineAfter(CALL_TIMEOUT_MS))
    console.log(`[admitted] run_ref=${row.run_ref} observed_state=${row.state}`)
    expect(this.calls, 'exactly one create POST').toBe(1)
  }

  async settle() {
    this.closed = true
    const deadline = deadlineAfter(CLEANUP_BUDGET_MS)
    const notes: string[] = []
    // Join the bounded pointer first, including when the test body timed out.
    if (this.pointer) {
      const pointer = await within(this.pointer, deadline, 'settle pointer')
      notes.push(pointer.error ? 'pointer=failed' : 'pointer=completed')
    }
    // Consume the observer even when the pointer action or test body failed.
    if (this.observation) {
      const result = await within(this.observation, deadline, 'settle create observer')
      notes.push('error' in result ? 'create_response=unreadable' : `create_response=HTTP${result.value.status}`)
    }
    if (!this.possibleEffect) return
    const api = this.page.request
    const observe = async <T>(read: (until: number) => Promise<T>, until: number): Promise<T> => {
      let last: unknown
      for (let attempt = 0; attempt < 3; attempt++) {
        try { return await read(until) } catch (error) {
          last = error
          notes.push(`observation_attempt=${attempt + 1} unavailable=${error}`)
          if (until <= performance.now()) break
        }
      }
      throw new Inability(`checked observation unavailable: ${last}`)
    }
    let ref = ''
    try {
      // Root supplies a unique name; the baseline requires it to be absent.
      // A response ref alone never authorizes a mutation.
      while (!ref) {
        const candidates = matching((await observe(until => readChecked(api, RUNS_PATH, isRunList, 'resolve effect', until), deadline)).items)
        if (candidates.length > 1) throw new Inability('effect UNKNOWN: multiple matching rows')
        if (candidates.length === 1) ref = candidates[0]
        else await new Promise(resolve => setTimeout(resolve, Math.min(250, timeLeft(deadline, 'resolve effect'))))
      }
      let row = await observe(until => exactOwnedRun(api, ref, until), deadline)
      if (!TERMINAL.includes(row.state)) {
        try {
          const stop = await api.post(`${ORIGIN}${RUNS_PATH}/${encodeURIComponent(ref)}/stop`, {
            timeout: timeLeft(deadline, 'stop'),
          })
          notes.push(`stop=HTTP${stop.status()}`)
        } catch (error) { notes.push(`stop=unreadable ${error}`) }
        // Preserve ten seconds for the later corroboration/cleanup/readback.
        const pollingDeadline = deadline - 10_000
        while (performance.now() < pollingDeadline && !TERMINAL.includes(row.state)) {
          try { row = await observe(until => exactOwnedRun(api, ref, until), pollingDeadline) }
          catch (error) { notes.push(`terminal_read=unavailable ${error}`) }
          if (!TERMINAL.includes(row.state) && performance.now() < pollingDeadline) {
            await new Promise(resolve => setTimeout(resolve, Math.min(250, timeLeft(pollingDeadline, 'terminal observation'))))
          }
        }
      }
      notes.push(`terminal=${row.state}`)
      // Repeat the exact identity read before the second mutating operation.
      let corroborated = false
      try {
        row = await observe(until => exactOwnedRun(api, ref, until), deadline)
        corroborated = true
      } catch (error) { notes.push(`before_cleanup=unavailable ${error}`) }
      let cleaned: RunRow | undefined
      let cleanupError: unknown
      try {
        if (!corroborated) throw new Inability('cleanup not authorized by an exact checked identity')
        cleaned = await checkedBody(await api.post(`${ORIGIN}${RUNS_PATH}/${encodeURIComponent(ref)}/cleanup`, {
          timeout: timeLeft(deadline, 'cleanup'),
        }), isRun, 'cleanup')
      } catch (error) { cleanupError = error }
      // An error in cleanup does not suppress a safe independent readback.
      const readback = await observe(until => exactOwnedRun(api, ref, until), deadline)
      notes.push(`cleanup=${cleaned?.state ?? 'UNREAD'} readback=${readback.state}`)
      if (!TERMINAL.includes(row.state) || cleanupError || !cleaned || cleaned.run_ref !== ref || !matchesFixture(cleaned) ||
          cleaned.state !== 'cleaned' || readback.state !== 'cleaned') {
        throw new Inability(`cleanup not proved: ${cleanupError ?? 'result/readback mismatch'}`)
      }
    } finally {
      console.log(`[settle] run=${ref || 'UNKNOWN'} ${notes.join(' ')}`)
    }
  }
}

// Teardown time is independent of test-body exhaustion. Root's process owner
// must allow both phases; a killed worker is never successful run cleanup.
const test = base.extend<{ launch: OwnedLaunch }>({
  launch: [async ({ page }, provideLaunch) => {
    const launch = new OwnedLaunch(page)
    page.on('request', launch.onRequest)
    try { await provideLaunch(launch) } finally {
      try { await launch.settle() } finally { page.off('request', launch.onRequest) }
    }
  }, { timeout: 35_000 }],
})
test.use({ screenshot: 'off', trace: 'off' })
test.setTimeout(130_000)

async function signIn(page: Page) {
  await page.goto('/login')
  await page.locator('#email').fill(EMAIL)
  await page.locator('#password').fill(PASSWORD)
  await page.getByRole('button', { name: /^sign in$/i }).click()
  await page.waitForURL((url) => !url.pathname.startsWith('/login'))
}

/** Fresh mount per case: Cancel does not reset the form; only a create does. */
async function openDialog(page: Page) {
  await page.goto('/agentops')
  const trigger = page
    .locator(
      'xpath=//button[not(ancestor::*[@data-testid="work-surface"])][normalize-space(.)="New session"]',
    )
    .first()
  await expect(trigger).toBeVisible()
  await trigger.click()
  const dialog = page.getByRole('dialog')
  await expect(dialog).toBeVisible()
  return { dialog, trigger }
}

/**
 * Selects by exact unique painted label, then waits for the real readiness
 * RESPONSE and the RENDERED panel with its checks — a selected label alone is
 * not the profile-dependent body this form's height depends on.
 */
async function selectProfileAndAwaitReadiness(page: Page, dialog: Locator) {
  const combo = dialog.getByRole('combobox', { name: /profile/i }).first()
  await combo.click()
  const option = page.getByRole('option', { name: PROFILE_LABEL, exact: true })
  await expect(option).toHaveCount(1)
  const observed = page.waitForResponse((r) => {
    const url = new URL(r.url())
    return r.request().method() === 'GET' && url.origin === ORIGIN &&
      url.pathname === `/v1/m/sessions/provider-profiles/${PROFILE_REF}/launch-readiness` &&
      url.searchParams.get('transport') === 'stream-json' && url.searchParams.get('isolation') === 'native'
  }, { timeout: 15_000 }).then(value => ({ value }), error => ({ error }))
  // Consume the result even if clicking fails.
  let clickError: unknown
  try { await option.click() } catch (error) { clickError = error }
  const result = await observed
  if ('error' in result) throw new Inability(`readiness response: ${result.error}`)
  if (clickError) throw clickError
  const response = result.value
  if (!response.ok()) throw new Inability(`readiness HTTP${response.status()}`)
  const body = await within(response.json(), deadlineAfter(CALL_TIMEOUT_MS), 'readiness body') as {
    profile_ref: string; configuration_state: string
    selection: { transport: string; isolation: string }
    checks: { check: string; state: string; code: string }[]
  }
  expect(body.profile_ref).toBe(PROFILE_REF)
  expect(body.selection).toEqual({ transport: 'stream-json', isolation: 'native' })
  expect(['ready', 'unknown'], `observed readiness=${body.configuration_state}`).toContain(body.configuration_state)
  expect(Array.isArray(body.checks) && body.checks.length > 0, 'readiness checks').toBe(true)
  const panel = dialog.locator('[data-testid="launch-readiness"]')
  await expect(panel, 'readiness panel has a visible layout').toBeVisible()
  const rows = panel.locator('[data-check]')
  await expect(rows).toHaveCount(body.checks.length)
  for (let i = 0; i < body.checks.length; i++) await expect(rows.nth(i)).toBeVisible()
  await expect.poll(async () => rows.evaluateAll(elements => elements.map(el => ({
    check: el.getAttribute('data-check'), state: el.getAttribute('data-state'), code: el.getAttribute('data-code'),
  })))).toEqual(body.checks.map(({ check, state, code }) => ({ check, state, code })))
  await expect(dialog.getByRole('button', { name: /request launch/i }),
    `submit must reflect permitting readiness ${body.configuration_state}`).toBeEnabled()
  return body.configuration_state
}

/** Everything measured BEFORE any assertion, so the artifact holds the numbers. */
async function measure(dialog: Locator) {
  const box = await dialog.boundingBox()
  const region = await dialog.evaluate((el) => {
    const owners = Array.from(el.querySelectorAll('*')).filter((n) =>
      /(auto|scroll)/.test(getComputedStyle(n).overflowY),
    )
    const active = owners.filter((n) => n.scrollHeight > n.clientHeight + 1)
    const se = document.scrollingElement ?? document.documentElement
    return {
      owners: owners.length,
      active: active.length,
      top: active[0]?.scrollTop ?? -1,
      scrollingElementOverflow: se.scrollHeight - se.clientHeight,
      bodyOverflow: document.body.scrollHeight - document.body.clientHeight,
      rootOverflowY: getComputedStyle(document.documentElement).overflowY,
      bodyOverflowY: getComputedStyle(document.body).overflowY,
      documentScrolls: se.scrollHeight > se.clientHeight + 1 &&
        !['hidden', 'clip'].includes(getComputedStyle(se).overflowY) &&
        !['hidden', 'clip'].includes(getComputedStyle(document.body).overflowY),
    }
  })
  return { box, region }
}

/** elementFromPoint must land on THIS element or inside it. */
async function hitsSelf(page: Page, control: Locator) {
  const box = await control.boundingBox()
  if (!box) return { isSelf: false, box: null }
  const handle = await control.elementHandle()
  const isSelf = await page.evaluate(
    ([el, x, y]) => {
      const hit = document.elementFromPoint(x as number, y as number)
      return !!hit && (hit === el || (el as Element).contains(hit))
    },
    [handle, box.x + box.width / 2, box.y + box.height / 2] as const,
  )
  await handle?.dispose()
  return { isSelf, box }
}

test('New session is pointer-reachable and admits exactly one settled launch', async ({
  page, launch,
}) => {
  // Missing fixture input is an inability, never a skip that counts as a pass.
  expect(
    EMAIL && PASSWORD && PROFILE_REF && PROFILE_LABEL && RUN_NAME,
    'fixture must supply E2E_EMAIL, E2E_PASSWORD, E2E_PROFILE_REF, E2E_PROFILE_LABEL and E2E_RUN_NAME',
  ).toBeTruthy()

  const api = page.request
  await signIn(page)
  expect(new URL(page.url()).origin, 'sign-in kept the configured origin').toBe(ORIGIN)
  const before = matching(
    (await readChecked(api, RUNS_PATH, isRunList, 'baseline run list')).items,
  )

  expect(before, 'the unique fixture name must not already exist').toEqual([])

  // ── THE REJECTED CONTROL ────────────────────────────────────────────────
  await test.step('missing profile keeps the control disabled and admits nothing', async () => {
    await page.setViewportSize({ width: 1280, height: 720 })
    const { dialog } = await openDialog(page)
    await dialog.locator('input').first().fill(RUN_NAME)
    const submit = dialog.getByRole('button', { name: /request launch/i })
    const handle = await submit.elementHandle()
    await expect(submit).toBeDisabled()
    const stillDisabled = await page.evaluate(
      (el) => !!el && el.isConnected && (el as HTMLButtonElement).disabled,
      handle,
    )
    const current = await submit.elementHandle()
    const same = await page.evaluate(([a, b]) => a === b, [handle, current])
    await current?.dispose()
    await handle?.dispose()
    expect(same, 'the locator still names the retained control').toBe(true)
    expect(stillDisabled, 'the SAME attached element is still disabled').toBe(true)
    expect(launch.calls, 'no POST to the exact create endpoint').toBe(0)
    const after = matching(
      (await readChecked(api, RUNS_PATH, isRunList, 'negative-proof run list')).items,
    )
    expect(after, 'no new run row matching this fixture identity').toEqual(before)
    await page.keyboard.press('Escape')
    await expect(dialog).toBeHidden()
  })

  // ── GEOMETRY, WITH A RENDERED READINESS BODY ────────────────────────────
  for (const vp of VIEWPORTS) {
    await test.step(vp.name, async () => {
      await page.setViewportSize({ width: vp.width, height: vp.height })
      const { dialog, trigger } = await openDialog(page)
      await dialog.locator('input').first().fill(`${RUN_NAME}-probe`)
      const readiness = await selectProfileAndAwaitReadiness(page, dialog)

      const submit = dialog.getByRole('button', { name: /request launch/i })
      const enabled = await submit.isEnabled()
      const { box, region } = await measure(dialog)
      const cancel = await hitsSelf(page, dialog.getByRole('button', { name: /^cancel$/i }))
      const close = await hitsSelf(page, dialog.getByRole('button', { name: /^close$/i }))
      const submitHit = enabled ? await hitsSelf(page, submit) : null
      console.log(
        `[measure] ${vp.name} readiness=${readiness} enabled=${enabled} dialog=${JSON.stringify(box)} region=${JSON.stringify(region)} cancel=${JSON.stringify(cancel)} close=${JSON.stringify(close)} submit=${JSON.stringify(submitHit)}`,
      )

      // A refusal here is preserved as an observation and ends this leg WITHOUT
      // a geometry verdict; it is never reported as reachability.
      expect(
        enabled,
        `submit not enabled at ${vp.name} with readiness ${readiness}: refusal observed, no geometry verdict`,
      ).toBe(true)

      expect(box, 'dialog has a layout box').not.toBeNull()
      expect(box!.x, 'left edge inside viewport').toBeGreaterThanOrEqual(0)
      expect(box!.x + box!.width, 'right edge inside viewport').toBeLessThanOrEqual(vp.width)
      expect(box!.y, 'top edge inside the viewport').toBeGreaterThanOrEqual(0)
      expect(box!.y + box!.height, 'BOTTOM edge inside the viewport').toBeLessThanOrEqual(vp.height)
      expect(region.owners, 'one structural scroll owner').toBe(1)
      expect(region.documentScrolls, 'the document is not the scroller').toBe(false)

      // A fitting form legitimately has zero ACTIVE scrollers.
      if (region.active > 0) {
        expect(region.active, 'exactly one active scroller').toBe(1)
        const owner = await dialog.evaluateHandle(el => Array.from(el.querySelectorAll('*')).find(n =>
          /(auto|scroll)/.test(getComputedStyle(n).overflowY) && n.scrollHeight > n.clientHeight + 1))
        await page.mouse.move(box!.x + box!.width / 2, box!.y + box!.height / 2)
        await page.mouse.wheel(0, region.top > 0 ? -400 : 400)
        await expect.poll(async () => {
          const current = (await measure(dialog)).region
          return current.active === 1 && current.top >= 0 && current.top !== region.top
        }, { timeout: 3_000 }).toBe(true)
        const after = await measure(dialog)
        const sameOwner = await dialog.evaluate((el, previous) => {
          const active = Array.from(el.querySelectorAll('*')).filter(n =>
            /(auto|scroll)/.test(getComputedStyle(n).overflowY) && n.scrollHeight > n.clientHeight + 1)
          return active.length === 1 && active[0] === previous && !!previous?.isConnected
        }, owner)
        await owner.dispose()
        const controls = {
          cancel: await hitsSelf(page, dialog.getByRole('button', { name: /^cancel$/i })),
          close: await hitsSelf(page, dialog.getByRole('button', { name: /^close$/i })),
          submit: await hitsSelf(page, submit),
        }
        console.log(`[after-wheel] ${vp.name} ${JSON.stringify({ ...after, sameOwner, controls })}`)
        expect(sameOwner, 'the same internal owner scrolled').toBe(true)
        expect(after.region.owners).toBe(1)
        expect(after.region.active).toBe(1)
        expect(after.region.documentScrolls).toBe(false)
        expect(after.region.top).toBeGreaterThanOrEqual(0)
        expect(after.region.top).not.toBe(region.top)
        expect(after.box!.x).toBeGreaterThanOrEqual(0)
        expect(after.box!.y).toBeGreaterThanOrEqual(0)
        expect(after.box!.x + after.box!.width).toBeLessThanOrEqual(vp.width)
        expect(after.box!.y + after.box!.height).toBeLessThanOrEqual(vp.height)
        for (const [name, hit] of Object.entries(controls)) {
          expect(hit.isSelf, `${name} remains pointer-reachable after wheel`).toBe(true)
          expect(hit.box!.x).toBeGreaterThanOrEqual(0)
          expect(hit.box!.x + hit.box!.width).toBeLessThanOrEqual(vp.width)
          expect(hit.box!.y).toBeGreaterThanOrEqual(0)
          expect(hit.box!.y + hit.box!.height).toBeLessThanOrEqual(vp.height)
        }
      }

      for (const [label, hit] of [
        ['cancel', cancel],
        ['close', close],
        ['submit', submitHit!],
      ] as const) {
        expect(hit.box, `${label} has a layout box`).not.toBeNull()
        expect(hit.box!.x, `${label} left edge`).toBeGreaterThanOrEqual(0)
        expect(hit.box!.x + hit.box!.width, `${label} right edge`).toBeLessThanOrEqual(vp.width)
        expect(hit.box!.y + hit.box!.height, `${label} bottom edge inside the viewport`).toBeLessThanOrEqual(vp.height)
        expect(hit.box!.y, `${label} top edge inside the viewport`).toBeGreaterThanOrEqual(0)
        expect(hit.isSelf, `elementFromPoint lands on ${label} itself`).toBe(true)
      }

      const focusable = dialog.locator('button:not([disabled]),input:not([disabled]),[tabindex="0"]')
      const visible = await focusable.evaluateAll(elements => elements
        .filter(el => el.getClientRects().length > 0 && getComputedStyle(el).visibility !== 'hidden')
        .map((el, index) => ({ tag: el.tagName, index })))
      expect(visible.length, 'dialog has multiple focus targets').toBeGreaterThan(1)
      // Cross both ends through actual Tab events, starting from an observed
      // focused control; assertions test containment after more than one cycle.
      await dialog.getByRole('button', { name: /^close$/i }).focus()
      for (const key of ['Tab', 'Shift+Tab']) {
        for (let i = 0; i <= visible.length; i++) {
          const previous = await page.evaluateHandle(() => document.activeElement)
          await page.keyboard.press(key)
          const observation = await dialog.evaluate((el, prior) => ({
            inside: el.contains(document.activeElement), moved: document.activeElement !== prior,
          }), previous)
          await previous.dispose()
          expect(observation.inside, `${key} stays trapped across a full cycle`).toBe(true)
          expect(observation.moved, `${key} moves focus`).toBe(true)
        }
      }

      await page.keyboard.press('Escape')
      await expect(dialog).toBeHidden()
      const th = await trigger.elementHandle()
      const restored = await page.evaluate((el) => document.activeElement === el, th)
      await th?.dispose()
      expect(restored, 'Escape restored focus to the trigger').toBe(true)
    })
  }

  await test.step('Cancel and Close are actually clickable', async () => {
    await page.setViewportSize({ width: 1280, height: 720 })
    for (const name of [/^cancel$/i, /^close$/i]) {
      const { dialog } = await openDialog(page)
      await dialog.getByRole('button', { name }).click()
      await expect(dialog).toBeHidden()
    }
  })

  await test.step('one pointer create is admitted and persisted', async () => {
    await page.setViewportSize({ width: 1280, height: 720 })
    const { dialog } = await openDialog(page)
    await dialog.locator('input').first().fill(RUN_NAME)
    await selectProfileAndAwaitReadiness(page, dialog)
    const submit = dialog.getByRole('button', { name: /request launch/i })
    expect((await hitsSelf(page, submit)).isSelf).toBe(true)
    await launch.create(submit)
  })
  // The launch fixture settles even when the click or this body throws/times out.
})
