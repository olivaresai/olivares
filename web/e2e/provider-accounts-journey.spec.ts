// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE PROVIDER-ACCOUNT JOURNEY against a REAL engine serving the embedded console, booted
// virgin for this spec by scripts/web-e2e.sh. Every authority decision and every effect
// is the engine's own. The administrator seeds through existing interfaces: first-boot
// setup with automatic sign-in, POST /v1/m/sessions/provider-profiles and POST /v1/users
// with an atomic first membership. POST /v1/memberships later revokes the editor role.
// A second principal holding the editor role then adopts profiles
// through the console and creates a managed account. Each effect is read back from the engine as the administrator.
// To withdraw the account read, the administrator authors a tenant ABAC deny policy on
// sessions:account:read (POST /v1/m/governance/policies). The engine then refuses that read
// with 403 while the member's session and whoami stay as they were. Deleting the policy
// restores the read. The rule has no principal selector, so it refuses every principal's
// account read in the tenant while it exists; nothing reads accounts as the administrator
// in that window.
//
// Synthetic seams, and nothing else is simulated:
//   S1 Profile homes are empty directories this spec creates under the harness's work
//      directory (E2E_WORK_DIR, removed by the harness's own cleanup). No provider CLI is
//      installed, and nobody signs in to a provider.
//   S2 A Playwright route on the member's page forwards REAL adopt/create POSTs to the engine
//      with route.fetch(), so the engine decides and commits. It then either drops the
//      connection (the answer is lost after the commit), or holds the real answer until
//      the test releases it: as itself while the member's role changes, or as a dropped
//      connection while the member's read is refused.
//
// What this journey proves in a browser: adoption under an explicit name; an answer lost
// after a committed effect is reconciled by GET only; an adoption whose answer is lost while
// the engine refuses the member's account read is kept, and after the read is restored it is
// reconciled by GET; write revoked (editor -> viewer) while the POST's committed answer is
// parked still reports that answer honestly; no adoption is duplicated; creation recovers a
// committed effect by replaying the identical request and returns the same account reference.
// What it does NOT prove: a read loss the console itself can see. No existing interface
// removes the account read from the member's whoami (every built-in role grants module
// reads, and removing a membership ends the session or moves the tenant). The real
// route-guard composition covers that case in vitest.
//
// No secret is captured. The setup token and both passwords are typed before any capture,
// and traces stay off because a trace records request headers, including the disposable
// session cookie and CSRF token. The receipt holds references, names and counts only.
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import {
  expect,
  test,
  type BrowserContext,
  type Page,
  type Route,
} from '@playwright/test'
import { JOURNEYS, stepHref } from '../src/features/navigation/journeys'

const [, , ACCOUNTS] = JOURNEYS.providerKeys

// A failure's automatic page snapshot can record typed form values (the setup token, passwords).
process.env.PLAYWRIGHT_NO_COPY_PROMPT = '1'

const setupToken = process.env.PLAYWRIGHT_SETUP_TOKEN ?? ''
const workDir = process.env.E2E_WORK_DIR ?? ''
const ADMIN_EMAIL = 'admin@example.com'
const ADMIN_PASSWORD = 'correct-horse-battery-staple-42'
const MEMBER_EMAIL = 'accounts-member@example.com'
const MEMBER_PASSWORD = 'member-horse-battery-staple-42'
const ADOPT = /\/v1\/m\/sessions\/provider-accounts\/[^/]+\/adopt$/

// A first-boot journey cannot be retried on the same engine: its setup token is spent.
test.describe.configure({ retries: 0 })
test.use({ screenshot: 'off', trace: 'off', video: 'off' })

interface Answer<T> {
  status: number
  body: T
}
interface AccountRow {
  account_ref: string
  name: string
}
interface WhoamiShape {
  grants: Array<{ tenant: string; permissions?: string[] }>
}

/** The browser sends its HttpOnly cookie. Recover CSRF metadata through the same
 * endpoint the console uses after reload; no credential leaves the page. */
async function authed<T>(
  page: Page,
  path: string,
  method = 'GET',
  body?: unknown,
): Promise<Answer<T>> {
  return (await page.evaluate(
    async ({ path, method, body }) => {
      const state = (key: string) =>
        (
          JSON.parse(localStorage.getItem(key) ?? '{}') as {
            state?: Record<string, unknown>
          }
        ).state ?? {}
      const tenant = String(state('olivares.tenant').activeTenant ?? '')
      const session = await fetch('/v1/auth/browser-session', {
        credentials: 'same-origin',
      })
      if (session.status !== 200)
        throw new Error(`browser session read failed (${session.status})`)
      const metadata = (await session.json()) as { csrf_token?: unknown }
      if (typeof metadata.csrf_token !== 'string' || !metadata.csrf_token)
        throw new Error('browser session has no CSRF token')
      const headers = new Headers({ Accept: 'application/json' })
      headers.set('X-CSRF-Token', metadata.csrf_token)
      if (tenant) headers.set('X-Olivares-Tenant', tenant)
      if (body !== undefined) headers.set('Content-Type', 'application/json')
      const res = await fetch(path, {
        method,
        credentials: 'same-origin',
        headers,
        body: body === undefined ? undefined : JSON.stringify(body),
      })
      const text = await res.text()
      return { status: res.status, body: text ? JSON.parse(text) : null }
    },
    { path, method, body },
  )) as Answer<T>
}

/** The page's own content. Every page locator below is scoped to it, so it never reaches the
 *  navigation rail, the breadcrumb or the toasts, which repeat page and account names. */
function content(page: Page) {
  return page.getByRole('main')
}

/** The Now link in the journey navigation, outside breadcrumbs and recent pages. */
function nowLink(page: Page) {
  return page
    .getByRole('navigation', { name: 'Journeys', exact: true })
    .getByRole('link', { name: 'Now', exact: true })
}

async function signIn(page: Page, email: string, password: string) {
  await page.goto('/login')
  await page.locator('#email').fill(email)
  await page.locator('#password').fill(password)
  await page.getByRole('button', { name: /^sign in$/i }).click()
  await expect(nowLink(page)).toBeVisible()
}

/** The engine's own account list, read by the administrator. */
async function engineAccounts(admin: Page) {
  const listed = await authed<{ items: AccountRow[] }>(
    admin,
    '/v1/m/sessions/provider-accounts?limit=100',
  )
  expect(listed.status, 'the administrator reads the account list').toBe(200)
  return listed.body.items
}

async function openAdoptDialog(member: Page) {
  await content(member)
    .getByRole('button', { name: 'Name a profile as an account', exact: true })
    .click()
  const dialog = member.getByRole('dialog', {
    name: 'Name a profile as an account',
    exact: true,
  })
  await expect(dialog).toBeVisible()
  // The editor also holds profile read, so the picker loads before it is offered.
  await expect(
    dialog.getByRole('combobox', { name: 'Profile', exact: true }),
  ).toBeEnabled()
  return dialog
}

/** An option of the open picker. Its list renders outside the dialog, in the one listbox. */
function pickerOption(member: Page, name: string | RegExp) {
  return member
    .getByRole('listbox')
    .getByRole('option', { name, exact: typeof name === 'string' })
}

async function adoptByReference(member: Page, ref: string, name: string) {
  const dialog = await openAdoptDialog(member)
  await dialog.getByRole('combobox', { name: 'Profile', exact: true }).click()
  await pickerOption(member, 'Enter a reference…').click()
  await dialog
    .getByRole('textbox', { name: 'Profile reference', exact: true })
    .fill(ref)
  await dialog
    .getByRole('textbox', { name: 'Account name (optional)', exact: true })
    .fill(name)
  await dialog
    .getByRole('button', { name: 'Name as account', exact: true })
    .click()
  return dialog
}

/** Makes the member's console re-read whoami the way it does in use: its whoami is fresh
 *  for 60 s and is re-read when the page becomes visible after that. */
async function observeWhoami(
  member: Page,
  settled: (whoami: WhoamiShape) => boolean,
) {
  await member.bringToFront()
  const deadline = Date.now() + 110_000
  while (Date.now() < deadline) {
    const answered = member
      .waitForResponse(
        (r) =>
          new URL(r.url()).pathname === '/v1/auth/whoami' && r.status() === 200,
        { timeout: 5_000 },
      )
      .catch(() => null)
    await member.evaluate(() =>
      window.dispatchEvent(new Event('visibilitychange')),
    )
    const response = await answered
    if (response && settled((await response.json()) as WhoamiShape)) return
  }
  throw new Error(
    'the member console never re-read whoami after the role change',
  )
}

test('provider accounts: adopt, reconcile lost answers by reading, keep an adoption across a refused read, and report a parked answer after write is revoked', async ({
  page,
  browser,
}) => {
  test.skip(
    !setupToken,
    'PLAYWRIGHT_SETUP_TOKEN not set — run via scripts/web-e2e.sh',
  )
  test.skip(!workDir, 'E2E_WORK_DIR not set — run via scripts/web-e2e.sh')
  test.setTimeout(420_000)
  page.setDefaultTimeout(15_000)

  const homes: string[] = []
  const refs: string[] = []
  const adoptPosts: string[] = []
  const committed: Record<string, number[]> = {}
  const readRefusal: Record<string, number> = {}
  let readDenyPolicy = ''
  let memberContext: BrowserContext | undefined
  let memberId = ''
  let tenant = ''
  const capture = (target: Page, name: string) =>
    target.screenshot({ path: test.info().outputPath(`${name}.png`) })

  try {
    await test.step('first boot: automatic administrator sign-in', async () => {
      await page.goto('/setup')
      await page.locator('#token').fill(setupToken)
      await page.locator('#setup-email').fill(ADMIN_EMAIL)
      await page.locator('#setup-password').fill(ADMIN_PASSWORD)
      await page.getByRole('button', { name: /create administrator/i }).click()
      await expect(page).toHaveURL(/\/onboarding$/)
      await expect(
        content(page).getByRole('heading', {
          name: 'Get started',
          exact: true,
        }),
      ).toBeVisible()
      tenant = await page.evaluate(() =>
        String(
          (
            JSON.parse(localStorage.getItem('olivares.tenant') ?? '{}') as {
              state?: { activeTenant?: string }
            }
          ).state?.activeTenant ?? '',
        ),
      )
      expect(tenant, 'the administrator has an active tenant').not.toBe('')
    })

    await test.step('authenticate with the browser cookie and refuse a write without CSRF', async () => {
      expect(
        await page.evaluate(() => localStorage.getItem('olivares.session')),
        'the browser holds no legacy bearer in storage',
      ).toBeNull()
      const whoami = await authed<WhoamiShape>(page, '/v1/auth/whoami')
      expect(whoami.status, 'the cookie authenticates the administrator').toBe(
        200,
      )
      expect(whoami.body.grants.some((g) => g.tenant === tenant)).toBe(true)
      const withoutCSRF = await page.evaluate(async (tenant) => {
        const response = await fetch('/v1/m/sessions/provider-profiles', {
          method: 'POST',
          credentials: 'same-origin',
          headers: {
            'Content-Type': 'application/json',
            'X-Olivares-Tenant': tenant,
          },
          body: '{}',
        })
        return response.status
      }, tenant)
      expect(withoutCSRF, 'a cookie alone cannot authorize a write').toBe(403)
      const withCSRF = await authed(
        page,
        '/v1/m/sessions/provider-profiles',
        'POST',
        {},
      )
      expect(
        withCSRF.status,
        'the same empty body with CSRF reaches request validation',
      ).toBe(400)
    })

    await test.step('seed four provider profiles over empty homes (seam S1)', async () => {
      for (const label of ['A', 'B', 'C', 'D']) {
        const root = mkdtempSync(join(workDir, `provider-account-${label}-`))
        homes.push(root)
        const config = join(root, 'config')
        const home = join(root, 'home')
        mkdirSync(config)
        mkdirSync(home)
        const created = await authed<{ profile_ref: string }>(
          page,
          '/v1/m/sessions/provider-profiles',
          'POST',
          {
            driver: 'claude',
            config_home: config,
            user_home: home,
            display_name: `Journey profile ${label}`,
          },
        )
        expect(created.status, `profile ${label} is created`).toBe(201)
        refs.push(created.body.profile_ref)
      }
    })

    await test.step('seed a member who holds the editor role', async () => {
      const user = await authed<{
        id: string
        membership: { user_id: string; tenant: string; role: string }
      }>(page, '/v1/users', 'POST', {
        email: MEMBER_EMAIL,
        display_name: 'Accounts member',
        password: MEMBER_PASSWORD,
        tenant,
        role: 'editor',
      })
      expect(user.status, 'the member account is created').toBe(201)
      memberId = user.body.id
      // The real creation API grants a new account its first membership atomically.
      // A separate grant to an existing non-member requires the holder's consent.
      expect(user.body.membership, 'the editor membership is created').toEqual(
        expect.objectContaining({
          user_id: memberId,
          tenant,
          role: 'editor',
        }),
      )
    })

    memberContext = await browser.newContext()
    const member = await memberContext.newPage()
    member.setDefaultTimeout(15_000)
    member.on('request', (request) => {
      const path = new URL(request.url()).pathname
      if (request.method() === 'POST' && ADOPT.test(path))
        adoptPosts.push(decodeURIComponent(path.split('/').at(-2) ?? ''))
    })

    await test.step('the member opens the accounts page', async () => {
      await signIn(member, MEMBER_EMAIL, MEMBER_PASSWORD)
      await member.goto(stepHref(ACCOUNTS))
      await expect(
        content(member).getByRole('heading', {
          name: 'Provider accounts',
          exact: true,
        }),
      ).toBeVisible()
      await expect(
        content(member).getByText('No provider accounts', { exact: true }),
      ).toBeVisible()
    })

    await test.step('adopt profile A from the picker under an explicit name', async () => {
      const dialog = await openAdoptDialog(member)
      await dialog
        .getByRole('combobox', { name: 'Profile', exact: true })
        .click()
      await pickerOption(
        member,
        new RegExp(`^Journey profile A \\(${refs[0]}\\)$`),
      ).click()
      await dialog
        .getByRole('textbox', { name: 'Account name (optional)', exact: true })
        .fill('journey-a')
      await dialog
        .getByRole('button', { name: 'Name as account', exact: true })
        .click()
      // The sheet uses the profile's display label; Name keeps the canonical account name.
      const detail = member.getByRole('dialog', {
        name: 'Journey profile A',
        exact: true,
      })
      await expect(
        detail.getByRole('heading', { name: 'Journey profile A', exact: true }),
      ).toBeVisible()
      await expect(detail.getByText('journey-a', { exact: true })).toBeVisible()
      await capture(member, '1-adopted-detail')
      await member.keyboard.press('Escape')
      expect(await engineAccounts(page)).toEqual(
        expect.arrayContaining([
          expect.objectContaining({ account_ref: refs[0], name: 'journey-a' }),
        ]),
      )
    })

    await test.step('adopt profile B; the engine commits it and the answer is lost (seam S2)', async () => {
      committed[refs[1]] = []
      const dropAfterCommit = async (route: Route) => {
        const response = await route.fetch()
        committed[refs[1]].push(response.status())
        await route.abort('connectionreset')
      }
      await member.route(ADOPT, dropAfterCommit)
      await adoptByReference(member, refs[1], 'journey-b')
      await expect(
        content(member).getByText(
          `Whether ${refs[1]} became an account is not known here.`,
          { exact: true },
        ),
      ).toBeVisible()
      await expect(
        content(member).getByText(
          `At this read, ${refs[1]} is the account journey-b.`,
          { exact: true },
        ),
      ).toBeVisible()
      expect(committed[refs[1]], 'the engine committed the adoption').toEqual([
        200,
      ])
      await member.unroute(ADOPT, dropAfterCommit)
      await content(member)
        .getByRole('button', { name: 'Check again', exact: true })
        .click()
      await expect(
        content(member).getByText(
          `At this read, ${refs[1]} is the account journey-b.`,
          { exact: true },
        ),
      ).toBeVisible()
      await capture(member, '2-unknown-reconciled')
      // The transport is restored and the page reloads: the account is listed once, and
      // nothing re-sent the adoption.
      await member.reload()
      await expect(
        content(member).getByRole('button', { name: /Journey profile B/ }),
      ).toBeVisible()
      expect(adoptPosts.filter((ref) => ref === refs[1])).toHaveLength(1)
      const named = (await engineAccounts(page)).filter(
        (a) => a.account_ref === refs[1],
      )
      expect(named).toEqual([
        expect.objectContaining({ account_ref: refs[1], name: 'journey-b' }),
      ])
    })

    await test.step('adopt profile D; its answer is lost while the engine refuses the member account read, and a GET reconciles it once the read is back (seam S2)', async () => {
      const ref = refs[3]
      committed[ref] = []
      let release: () => void = () => {}
      const parked = new Promise<void>((resolve) => {
        release = resolve
      })
      const holdThenDrop = async (route: Route) => {
        const response = await route.fetch()
        committed[ref].push(response.status())
        await parked
        await route.abort('connectionreset')
      }
      await member.route(ADOPT, holdThenDrop)
      const dialog = await adoptByReference(member, ref, 'journey-d')
      await expect
        .poll(() => committed[ref].length, { timeout: 30_000 })
        .toBe(1)
      expect(committed[ref], 'the engine committed the adoption').toEqual([200])
      await expect(
        dialog.getByText(
          `Naming ${ref} as an account. Waiting for the server's answer.`,
          {
            exact: true,
          },
        ),
      ).toBeVisible()

      // The administrator withdraws the account read with a tenant deny policy.
      const denied = await authed<{ id?: string } | null>(
        page,
        '/v1/m/governance/policies',
        'POST',
        {
          name: 'Journey: refuse provider-account reads',
          kind: 'abac',
          enabled: true,
          spec: {
            rules: [{ deny: true, permission: 'sessions:account:read' }],
          },
        },
      )
      // Recorded before any assertion, so `finally` removes a created policy on every path.
      readDenyPolicy = denied.body?.id ?? ''
      readRefusal.policy_created = denied.status
      expect(denied.status, 'the deny policy is created').toBe(201)
      expect(readDenyPolicy, 'the created policy has an id').not.toBe('')

      // The engine refuses the member's read. The session and the member's own view of its
      // permissions do not change, so the console stays in the same authority boundary.
      const refused = await authed(
        member,
        '/v1/m/sessions/provider-accounts?limit=100',
      )
      expect(refused.status, 'the engine refuses the member account read').toBe(
        403,
      )
      readRefusal.member_list_during = refused.status
      const whoami = await authed<WhoamiShape>(member, '/v1/auth/whoami')
      expect(whoami.status, 'the member session is still valid').toBe(200)
      expect(
        whoami.body.grants.find((g) => g.tenant === tenant)?.permissions ?? [],
        'whoami still lists the account read',
      ).toContain('sessions:account:read')

      // The committed answer is lost while the read is refused: the outcome is unknown, and
      // every read the console makes to reconcile it is refused by the engine.
      const checkAnswered = member.waitForResponse(
        (r) =>
          r.request().method() === 'GET' &&
          new URL(r.url()).pathname ===
            `/v1/m/sessions/provider-accounts/${ref}`,
      )
      release()
      await expect(
        content(member).getByText(
          `Whether ${ref} became an account is not known here.`,
          { exact: true },
        ),
      ).toBeVisible()
      const check = await checkAnswered
      expect(check.status(), 'the engine refuses the reconciliation read').toBe(
        403,
      )
      readRefusal.member_check_during = check.status()
      await expect(
        content(member).getByText('The check could not be completed.', {
          exact: true,
        }),
      ).toBeVisible()
      await expect(
        content(member).getByText('You do not have access to this.', {
          exact: true,
        }),
      ).toBeVisible()
      await member.unroute(ADOPT, holdThenDrop)
      await capture(member, '3-read-refused-intent-held')

      // Deleting the policy restores the read.
      const restored = await authed(
        page,
        `/v1/m/governance/policies/${readDenyPolicy}`,
        'DELETE',
      )
      expect(restored.status, 'the deny policy is deleted').toBe(204)
      readDenyPolicy = ''
      readRefusal.policy_deleted = restored.status
      const readAgain = await authed(
        member,
        '/v1/m/sessions/provider-accounts?limit=100',
      )
      expect(readAgain.status, 'the member reads accounts again').toBe(200)
      readRefusal.member_list_after = readAgain.status

      // The same boundary kept the adoption through the refusal, and a GET settles it.
      await expect(
        content(member).getByText(
          `Whether ${ref} became an account is not known here.`,
          { exact: true },
        ),
        'the adoption is still held after the read is restored',
      ).toBeVisible()
      await content(member)
        .getByRole('button', { name: 'Check again', exact: true })
        .click()
      await expect(
        content(member).getByText(
          `At this read, ${ref} is the account journey-d.`,
          { exact: true },
        ),
      ).toBeVisible()
      await capture(member, '4-read-restored-reconciled')
      await member.reload()
      await expect(
        content(member).getByRole('button', { name: /Journey profile D/ }),
      ).toBeVisible()
      expect(adoptPosts.filter((p) => p === ref)).toHaveLength(1)
      const named = (await engineAccounts(page)).filter(
        (a) => a.account_ref === ref,
      )
      expect(named).toEqual([
        expect.objectContaining({ account_ref: ref, name: 'journey-d' }),
      ])
    })

    await test.step('create a managed account and recover a committed answer with the same request', async () => {
      const creates: unknown[] = []
      const answers: number[] = []
      const routePattern = '**/v1/m/sessions/provider-accounts'
      const loseFirstAnswer = async (route: Route) => {
        if (route.request().method() !== 'POST') {
          await route.continue()
          return
        }
        creates.push(route.request().postDataJSON())
        const response = await route.fetch()
        answers.push(response.status())
        if (creates.length === 1) await route.abort('connectionreset')
        else await route.fulfill({ response })
      }
      await member.route(routePattern, loseFirstAnswer)
      await content(member)
        .getByRole('button', { name: 'Create account', exact: true })
        .click()
      const dialog = member.getByRole('dialog', {
        name: 'Create provider account',
      })
      await dialog
        .getByRole('textbox', { name: 'Provider driver' })
        .fill('codex')
      await dialog
        .getByRole('textbox', { name: 'Account name (optional)' })
        .fill('journey-managed')
      await dialog.getByRole('button', { name: 'Create', exact: true }).click()
      await expect(dialog.getByRole('alert')).toBeVisible()
      expect(answers).toEqual([201])
      const first = (await engineAccounts(page)).filter(
        (a) => a.name === 'journey-managed',
      )
      expect(first).toHaveLength(1)
      await member.keyboard.press('Escape')
      await content(member)
        .getByRole('tab', { name: 'Profiles', exact: true })
        .click()
      await content(member)
        .getByRole('tab', { name: 'Accounts', exact: true })
        .click()
      await content(member)
        .getByRole('button', { name: 'Retry creation', exact: true })
        .click()
      await expect(
        dialog.getByRole('textbox', { name: 'Provider driver' }),
      ).toBeDisabled()
      await dialog
        .getByRole('button', { name: 'Retry creation', exact: true })
        .click()
      await expect(
        member.getByRole('dialog', { name: /journey-managed/ }),
      ).toBeVisible()
      expect(answers).toEqual([201, 201])
      expect(creates[1]).toEqual(creates[0])
      expect(
        (await engineAccounts(page)).filter(
          (a) => a.name === 'journey-managed',
        ),
      ).toEqual(first)
      await capture(member, 'managed-account-recovered')
      await member.keyboard.press('Escape')
      await member.unroute(routePattern, loseFirstAnswer)
    })

    await test.step('adopt profile C; write is revoked while its committed answer is parked (seam S2)', async () => {
      committed[refs[2]] = []
      let release: () => void = () => {}
      const parked = new Promise<void>((resolve) => {
        release = resolve
      })
      const holdAfterCommit = async (route: Route) => {
        const response = await route.fetch()
        committed[refs[2]].push(response.status())
        await parked
        await route.fulfill({ response })
      }
      await member.route(ADOPT, holdAfterCommit)
      const dialog = await adoptByReference(member, refs[2], 'journey-c')
      await expect
        .poll(() => committed[refs[2]].length, { timeout: 30_000 })
        .toBe(1)
      expect(committed[refs[2]]).toEqual([200])
      await expect(
        dialog.getByText(
          `Naming ${refs[2]} as an account. Waiting for the server's answer.`,
          { exact: true },
        ),
      ).toBeVisible()

      const demoted = await authed(page, '/v1/memberships', 'POST', {
        user_id: memberId,
        tenant,
        role: 'viewer',
      })
      expect([200, 201], 'the member is moved to the viewer role').toContain(
        demoted.status,
      )
      await observeWhoami(
        member,
        (whoami) =>
          !whoami.grants.some(
            (g) =>
              g.tenant === tenant &&
              (g.permissions ?? []).includes('sessions:account:write'),
          ),
      )
      await expect(dialog).toBeHidden()
      await expect(
        content(member).getByText(
          `Naming ${refs[2]} as an account. The server has not answered yet.`,
          { exact: true },
        ),
      ).toBeVisible()
      await expect(
        content(member).getByRole('button', {
          name: 'Name a profile as an account',
          exact: true,
        }),
      ).toHaveCount(0)
      await capture(member, '5-parked-after-write-revoked')

      release()
      await expect(
        content(member).getByText(`${refs[2]} is now the account journey-c.`, {
          exact: true,
        }),
      ).toBeVisible()
      await member.unroute(ADOPT, holdAfterCommit)
      await capture(member, '6-parked-answer-reported')
      expect(adoptPosts.filter((ref) => ref === refs[2])).toHaveLength(1)
      const named = (await engineAccounts(page)).filter(
        (a) => a.account_ref === refs[2],
      )
      expect(named).toEqual([
        expect.objectContaining({ account_ref: refs[2], name: 'journey-c' }),
      ])
    })

    await test.step('no adoption was sent twice or duplicated', async () => {
      expect([...adoptPosts].sort()).toEqual([...refs].sort())
      const accounts = await engineAccounts(page)
      for (const ref of refs) {
        expect(accounts.filter((a) => a.account_ref === ref)).toHaveLength(1)
      }
    })
  } finally {
    // A deny policy left behind by a failed step is removed; the engine is discarded anyway.
    if (readDenyPolicy !== '') {
      const cleaned = await authed(
        page,
        `/v1/m/governance/policies/${readDenyPolicy}`,
        'DELETE',
      ).catch(() => null)
      readRefusal.policy_deleted_in_cleanup = cleaned?.status ?? 0
    }
    const receipt = {
      profiles: refs,
      adopt_posts_by_profile: Object.fromEntries(
        refs.map((ref) => [ref, adoptPosts.filter((p) => p === ref).length]),
      ),
      committed_status_by_profile: committed,
      read_refusal_statuses: readRefusal,
      member_seeded: memberId !== '',
      tenant_present: tenant !== '',
    }
    writeFileSync(
      test.info().outputPath('effect-receipt.json'),
      JSON.stringify(receipt, null, 2),
    )
    await memberContext?.close()
    for (const root of homes) rmSync(root, { recursive: true, force: true })
  }
})
