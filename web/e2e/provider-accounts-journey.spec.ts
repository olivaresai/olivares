// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE PROVIDER-ACCOUNT JOURNEY against a REAL engine serving the embedded console, booted
// virgin for this spec by scripts/web-e2e.sh. Every authority decision and every effect
// is the engine's own. The administrator seeds through existing interfaces: first-boot
// setup, sign-in, POST /v1/m/sessions/provider-profiles, POST /v1/users and
// POST /v1/memberships. A second principal holding the editor role then adopts profiles
// through the console. The effect is read back from the engine as the administrator.
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
//   S2 A Playwright route on the member's page forwards the REAL adopt POST to the engine
//      with route.fetch(), so the engine decides and commits. It then either drops the
//      connection (the answer is lost after the commit), or holds the real answer until
//      the test releases it: as itself while the member's role changes, or as a dropped
//      connection while the member's read is refused.
//
// What this journey proves in a browser: adoption under an explicit name; an answer lost
// after a committed effect is reconciled by GET only; an adoption whose answer is lost while
// the engine refuses the member's account read is kept, and after the read is restored it is
// reconciled by GET; write revoked (editor -> viewer) while the POST's committed answer is
// parked still reports that answer honestly; and no adoption is ever duplicated or sent twice.
// What it does NOT prove: a read loss the console itself can see. No existing interface
// removes the account read from the member's whoami (every built-in role grants module
// reads, and removing a membership ends the session or moves the tenant). The real
// route-guard composition covers that case in vitest.
//
// No secret is captured. The setup token and both passwords are typed before any capture,
// and traces stay off because a trace records request headers, including the disposable
// bearer. The receipt holds references, names and counts only.
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import {
  expect,
  test,
  type BrowserContext,
  type Page,
  type Route,
} from '@playwright/test'

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

/** A request with the bearer and tenant the given page already holds. The credential never
 *  leaves the page: it is read from its own storage inside the page. */
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
      const token = String(state('olivares.session').token ?? '')
      const tenant = String(state('olivares.tenant').activeTenant ?? '')
      if (!token) throw new Error('no session in this page')
      const headers = new Headers({ Accept: 'application/json' })
      headers.set('Authorization', `Bearer ${token}`)
      if (tenant) headers.set('X-Olivares-Tenant', tenant)
      if (body !== undefined) headers.set('Content-Type', 'application/json')
      const res = await fetch(path, {
        method,
        headers,
        body: body === undefined ? undefined : JSON.stringify(body),
      })
      const text = await res.text()
      return { status: res.status, body: text ? JSON.parse(text) : null }
    },
    { path, method, body },
  )) as Answer<T>
}

async function signIn(page: Page, email: string, password: string) {
  await page.goto('/login')
  await page.locator('#email').fill(email)
  await page.locator('#password').fill(password)
  await page.getByRole('button', { name: /^sign in$/i }).click()
  await expect(
    page.getByRole('link', { name: 'Overview', exact: true }),
  ).toBeVisible()
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
  await member.getByRole('button', { name: 'Adopt a profile' }).click()
  const dialog = member.getByRole('dialog', {
    name: 'Adopt a provider profile',
  })
  await expect(dialog).toBeVisible()
  // The editor also holds profile read, so the picker loads before it is offered.
  await expect(dialog.getByRole('combobox', { name: 'Profile' })).toBeEnabled()
  return dialog
}

async function adoptByReference(member: Page, ref: string, name: string) {
  const dialog = await openAdoptDialog(member)
  await dialog.getByRole('combobox', { name: 'Profile' }).click()
  await member.getByRole('option', { name: 'Enter a reference…' }).click()
  await dialog.getByRole('textbox', { name: 'Profile reference' }).fill(ref)
  await dialog
    .getByRole('textbox', { name: 'Account name (optional)' })
    .fill(name)
  await dialog.getByRole('button', { name: 'Adopt', exact: true }).click()
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
    await test.step('first boot: create the administrator and sign in', async () => {
      await page.goto('/setup')
      await page.locator('#token').fill(setupToken)
      await page.locator('#setup-email').fill(ADMIN_EMAIL)
      await page.locator('#setup-password').fill(ADMIN_PASSWORD)
      await page.getByRole('button', { name: /create administrator/i }).click()
      await page.waitForURL('**/login')
      await signIn(page, ADMIN_EMAIL, ADMIN_PASSWORD)
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
      const user = await authed<{ id: string }>(page, '/v1/users', 'POST', {
        email: MEMBER_EMAIL,
        display_name: 'Accounts member',
        password: MEMBER_PASSWORD,
      })
      expect(user.status, 'the member account is created').toBe(201)
      memberId = user.body.id
      const granted = await authed(page, '/v1/memberships', 'POST', {
        user_id: memberId,
        tenant,
        role: 'editor',
      })
      expect([200, 201], 'the editor role is granted').toContain(granted.status)
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
      await member.goto('/provider-accounts')
      await expect(
        member.getByRole('heading', { name: 'Provider accounts' }),
      ).toBeVisible()
      await expect(member.getByText('No provider accounts')).toBeVisible()
    })

    await test.step('adopt profile A from the picker under an explicit name', async () => {
      const dialog = await openAdoptDialog(member)
      await dialog.getByRole('combobox', { name: 'Profile' }).click()
      await member
        .getByRole('option', {
          name: new RegExp(`Journey profile A \\(${refs[0]}\\)`),
        })
        .click()
      await dialog
        .getByRole('textbox', { name: 'Account name (optional)' })
        .fill('journey-a')
      await dialog.getByRole('button', { name: 'Adopt', exact: true }).click()
      await expect(
        member.getByRole('heading', { name: /journey-a/ }),
      ).toBeVisible()
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
        member.getByText(
          `The outcome of adopting ${refs[1]} is not known here.`,
        ),
      ).toBeVisible()
      await expect(
        member.getByText(`At this read, ${refs[1]} is the account journey-b.`),
      ).toBeVisible()
      expect(committed[refs[1]], 'the engine committed the adoption').toEqual([
        200,
      ])
      await member.unroute(ADOPT, dropAfterCommit)
      await member.getByRole('button', { name: 'Check again' }).click()
      await expect(
        member.getByText(`At this read, ${refs[1]} is the account journey-b.`),
      ).toBeVisible()
      await capture(member, '2-unknown-reconciled')
      // The transport is restored and the page reloads: the account is listed once, and
      // nothing re-sent the adoption.
      await member.reload()
      await expect(
        member.getByRole('button', { name: /journey-b/ }),
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
        dialog.getByText(`Adopting ${ref}. Waiting for the server's answer.`),
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
        member.getByText(`The outcome of adopting ${ref} is not known here.`),
      ).toBeVisible()
      const check = await checkAnswered
      expect(check.status(), 'the engine refuses the reconciliation read').toBe(
        403,
      )
      readRefusal.member_check_during = check.status()
      await expect(
        member.getByText('The check could not be completed.'),
      ).toBeVisible()
      await expect(member.getByText('Not authorized')).toBeVisible()
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
        member.getByText(`The outcome of adopting ${ref} is not known here.`),
        'the adoption is still held after the read is restored',
      ).toBeVisible()
      await member.getByRole('button', { name: 'Check again' }).click()
      await expect(
        member.getByText(`At this read, ${ref} is the account journey-d.`),
      ).toBeVisible()
      await capture(member, '4-read-restored-reconciled')
      await member.reload()
      await expect(
        member.getByRole('button', { name: /journey-d/ }),
      ).toBeVisible()
      expect(adoptPosts.filter((p) => p === ref)).toHaveLength(1)
      const named = (await engineAccounts(page)).filter(
        (a) => a.account_ref === ref,
      )
      expect(named).toEqual([
        expect.objectContaining({ account_ref: ref, name: 'journey-d' }),
      ])
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
          `Adopting ${refs[2]}. Waiting for the server's answer.`,
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
        member.getByText(
          `Adopting ${refs[2]}. The server has not answered yet.`,
        ),
      ).toBeVisible()
      await expect(
        member.getByRole('button', { name: 'Adopt a profile' }),
      ).toHaveCount(0)
      await capture(member, '5-parked-after-write-revoked')

      release()
      await expect(
        member.getByText(`${refs[2]} was adopted as journey-c.`),
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
