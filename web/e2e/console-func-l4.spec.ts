// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { readConsolePreferences } from './console-preferences'
import { Buffer } from 'node:buffer'
import { readFile } from 'node:fs/promises'
import { expect, test, type Locator, type Page } from '@playwright/test'
import {
  browserSessionHeaders,
  journeyNowLink,
  type BrowserSession,
} from './browser-session'
import { assertSessionAccepted } from './live-session'

// Functional tranche L4 runs against the same disposable, genuinely seeded engine as
// demo-graph.spec.ts. Responses are observed, never fulfilled by Playwright.
const demoTenant = process.env.DEMO_TENANT ?? ''
const DEMO_EMAIL = 'demo@olivares.local'
const DEMO_PASSWORD = 'olivares-demo-estate'

// Login sets a live HttpOnly session for the disposable engine. Retaining a
// Playwright trace on failure would copy it into an artifact; explicit visual
// evidence is captured only after authentication and contains no credential.
test.use({ trace: 'off' })

interface ListBody<T> {
  items: T[]
  has_more?: boolean
}

interface IdentityItem {
  id: string
  ref: string
  name?: string
}

interface AuditItem {
  id: string
  seq: number
  occurred_at: string
  actor: string
  action: string
  target_kind?: string
  target_id?: string
  hash: string
  prev_hash: string
}

function authHeaders(session: BrowserSession): Record<string, string> {
  return browserSessionHeaders(session, demoTenant)
}

async function loginDemo(page: Page): Promise<BrowserSession> {
  const loginRead = page.waitForResponse((response) => {
    const url = new URL(response.url())
    return (
      response.request().method() === 'POST' &&
      url.pathname === '/v1/auth/login'
    )
  })
  await page.goto('/login')
  await page.locator('#email').fill(DEMO_EMAIL)
  await page.locator('#password').fill(DEMO_PASSWORD)
  await page.getByRole('button', { name: /^sign in$/i }).click()
  const response = await loginRead
  expect(response.status()).toBe(200)
  const body = (await response.json()) as BrowserSession
  // WHAT A FRESH SIGN-IN PAINTS. The console lands on the root, which belongs to no
  // navigation area, and the sidebar hides a closed area's panel — so the entries on
  // screen are the areas and the overview above them, not a leaf like Inventory. The
  // name is exact because neighbouring entries contain it.
  await expect(journeyNowLink(page)).toBeVisible({ timeout: 15_000 })
  authHeaders(body)
  return body
}

async function waitForDemoRoster(
  page: Page,
  session: BrowserSession,
): Promise<void> {
  await expect
    .poll(
      async () => {
        const response = await page.request.get('/v1/m/governance/identities', {
          headers: authHeaders(session),
        })
        if (!response.ok()) return []
        const body = (await response.json()) as ListBody<IdentityItem>
        return body.items.map((item) => item.ref).sort()
      },
      {
        message:
          'the real demo source must materialize its three stable identities',
        timeout: 15_000,
      },
    )
    .toEqual(['app_role', 'etl_role', 'svc_pool'])
}

async function expectGridCardinality(
  grid: Locator,
  count: number,
  emptyTitle: string,
): Promise<void> {
  // DataTable virtualizes above 100 rows, so DOM <tr> count is not the dataset.
  // Its APG contract exposes the exact data cardinality plus the header instead.
  if (count > 0) {
    await expect(grid).toHaveAttribute('aria-rowcount', String(count + 1))
    return
  }
  await expect(grid).toHaveAttribute('aria-rowcount', '1')
  await expect(grid.locator('tbody tr')).toHaveCount(0)
  const statusId = await grid.getAttribute('aria-describedby')
  expect(statusId).toBeTruthy()
  await expect(
    grid
      .page()
      .locator(`[id="${statusId}"]`)
      .getByText(emptyTitle, { exact: true }),
  ).toBeVisible()
}

test.describe('CONSOLE-FUNC-01 lot 4 over a disposable live engine', () => {
  test.describe.configure({ timeout: 120_000 })

  test.skip(
    !demoTenant,
    'DEMO_TENANT not set — run via scripts/web-e2e-demo.sh',
  )

  test.beforeEach(async ({ page }) => {
    await page.addInitScript(
      ([tenant]) => {
        localStorage.setItem(
          'olivares.tenant',
          JSON.stringify({ state: { activeTenant: tenant }, version: 0 }),
        )
        if (localStorage.getItem('olivares.lang') === null) {
          localStorage.setItem('olivares.lang', 'en')
        }
        if (localStorage.getItem('olivares.theme') === null) {
          localStorage.setItem('olivares.theme', 'light')
        }
      },
      [demoTenant],
    )
  })

  test('identity tabs, redirect checker, inventory and passkey draft have real effects', async ({
    page,
  }) => {
    const session = await loginDemo(page)
    const info = await page.request.get('/v1/server-info', {
      headers: authHeaders(session),
    })
    expect(info.status()).toBe(200)
    const { edition } = (await info.json()) as { edition: string }
    const identity = await page.request.get('/v1/m/governance/identities', {
      headers: authHeaders(session),
    })
    expect([200, 501]).toContain(identity.status())
    if (edition === 'community') expect(identity.status()).toBe(200)
    await waitForDemoRoster(page, session)

    const ssoRead = page.waitForResponse((response) => {
      const url = new URL(response.url())
      return (
        response.request().method() === 'GET' &&
        url.pathname === '/v1/m/identity/sso'
      )
    })
    await page.goto('/identity?tab=federation')
    const ssoResponse = await ssoRead
    expect(ssoResponse.status()).toBe(200)
    const sso = (await ssoResponse.json()) as { redirect_uri?: string }
    expect(sso.redirect_uri).toBeTruthy()

    const redirect = page.getByLabel('Check your IdP redirect URI')
    await redirect.fill(`${sso.redirect_uri}/wrong`)
    await expect(redirect).toHaveAttribute('aria-invalid', 'true')
    await expect(
      page.getByRole('status').filter({ hasText: 'Does not match exactly.' }),
    ).toBeVisible()
    await redirect.fill(sso.redirect_uri!)
    await expect(redirect).toHaveAttribute('aria-invalid', 'false')
    await expect(
      page.getByRole('status').filter({ hasText: 'Exact match' }),
    ).toBeVisible()

    const selectTab = async (name: string, key: string): Promise<void> => {
      const trigger = page.getByRole('tab', { name, exact: true })
      await trigger.click()
      await expect(trigger).toHaveAttribute('aria-selected', 'true')
      await expect
        .poll(() => new URL(page.url()).searchParams.get('tab'))
        .toBe(key)
      const panelId = await trigger.getAttribute('aria-controls')
      expect(panelId).toBeTruthy()
      await expect(page.locator(`[id="${panelId}"]`)).toBeVisible()
    }

    const rosterRead = page.waitForResponse((response) => {
      const url = new URL(response.url())
      return (
        response.request().method() === 'GET' &&
        url.pathname === '/v1/m/governance/identities'
      )
    })
    await selectTab('Identity inventory', 'inventory')
    const rosterResponse = await rosterRead
    expect(rosterResponse.status()).toBe(200)
    const roster = (await rosterResponse.json()) as ListBody<IdentityItem>
    expect(roster.items.map((item) => item.ref).sort()).toEqual([
      'app_role',
      'etl_role',
      'svc_pool',
    ])
    const grid = page.getByRole('grid', {
      name: 'Non-human and human identities',
    })
    await expectGridCardinality(
      grid,
      roster.items.length,
      'No identities found',
    )

    const rosterSearch = page.getByPlaceholder('Search identities…')
    await rosterSearch.fill('svc_pool')
    const poolRow = grid.getByRole('row').filter({ hasText: 'svc_pool' })
    await expect(poolRow).toHaveCount(1)
    await expectGridCardinality(grid, 1, 'No identities found')
    await poolRow.click()
    await expect(page.getByRole('dialog')).toContainText('svc_pool')
    await page
      .getByRole('dialog')
      .getByRole('button', { name: 'Close' })
      .click()
    await expect(page.getByRole('dialog')).toHaveCount(0)

    await poolRow.click()
    const graphRead = page.waitForResponse((response) => {
      const url = new URL(response.url())
      return (
        response.request().method() === 'GET' &&
        url.pathname === '/v1/m/accessmap/graph'
      )
    })
    await page
      .getByRole('dialog')
      .getByRole('button', { name: 'View in access map' })
      .click()
    expect((await graphRead).status()).toBe(200)
    await page.waitForURL('**/access-map?focus=svc_pool')
    expect([...new URL(page.url()).searchParams.entries()]).toEqual([
      ['focus', 'svc_pool'],
    ])
    await expect(page.getByLabel('Search origin, resource, tool…')).toHaveValue(
      'svc_pool',
    )

    await page.goto('/identity?tab=inventory')
    await selectTab('SSO & SCIM', 'federation')
    await expect(
      page.getByRole('heading', { name: 'Single sign-on (OIDC / SAML)' }),
    ).toBeVisible()

    if (edition === 'community') {
      for (const name of [
        'NHI lifecycle',
        'WIF graph',
        'Key & residency posture',
      ]) {
        await expect(page.getByRole('tab', { name, exact: true })).toHaveCount(
          0,
        )
      }
    }

    const mcpRead = page.waitForResponse((response) => {
      const url = new URL(response.url())
      return (
        response.request().method() === 'GET' &&
        url.pathname === '/v1/m/security/findings' &&
        url.searchParams.get('kind') === 'mcp_auth'
      )
    })
    await selectTab('MCP auth', 'mcp')
    const mcpResponse = await mcpRead
    expect(mcpResponse.status()).toBe(200)
    const mcpFindings = (await mcpResponse.json()) as ListBody<{ id: string }>
    await expect(
      page.getByRole('heading', {
        name: 'MCP authorization',
        exact: true,
      }),
    ).toBeVisible()
    if (mcpFindings.items.length === 0) {
      await expect(
        page.getByText('No MCP authorization findings', { exact: true }),
      ).toBeVisible()
    } else {
      await expect(
        page
          .getByRole('list', { name: 'MCP authorization' })
          .getByRole('listitem'),
      ).toHaveCount(mcpFindings.items.length)
    }

    const credentialsRead = page.waitForResponse((response) => {
      const url = new URL(response.url())
      return url.pathname === '/v1/auth/webauthn/credentials'
    })
    await selectTab('Privileged login', 'login')
    expect((await credentialsRead).status()).toBe(200)
    await expect(
      page.getByRole('heading', { name: 'Session assurance' }),
    ).toBeVisible()

    let registerPosts = 0
    const countRegister = (request: { method(): string; url(): string }) => {
      const url = new URL(request.url())
      if (
        request.method() === 'POST' &&
        url.pathname.includes('/v1/auth/webauthn/register')
      ) {
        registerPosts += 1
      }
    }
    page.on('request', countRegister)
    await page.getByRole('button', { name: 'Register passkey' }).click()
    const registerDialog = page.getByRole('dialog', {
      name: 'Register new passkey',
    })
    await expect(registerDialog).toBeVisible()
    const register = registerDialog.getByRole('button', {
      name: 'Register passkey',
    })
    // An empty draft uses the device name; cancelling must still send no registration.
    await expect(register).toBeEnabled()
    await registerDialog.getByLabel('Passkey name').fill('L4 disposable key')
    await expect(register).toBeEnabled()
    await registerDialog.getByRole('button', { name: 'Cancel' }).click()
    await expect(registerDialog).toHaveCount(0)
    await page.waitForTimeout(500)
    expect(registerPosts).toBe(0)
    page.off('request', countRegister)

    await page.screenshot({ path: 'playwright-report/l4-identity-login.png' })
  })

  test('identity roster retry recovers from one transport failure into live data', async ({
    page,
  }) => {
    const session = await loginDemo(page)
    await waitForDemoRoster(page, session)

    let failedGets = 0
    await page.route('**/v1/m/governance/identities**', async (route) => {
      const request = route.request()
      const url = new URL(request.url())
      if (
        request.method() === 'GET' &&
        url.pathname === '/v1/m/governance/identities' &&
        failedGets === 0
      ) {
        failedGets += 1
        await route.abort('failed')
        return
      }
      await route.continue()
    })
    await page.goto('/identity?tab=inventory')
    const alert = page.getByRole('alert')
    await expect(alert).toBeVisible()
    const recovered = page.waitForResponse((response) => {
      const url = new URL(response.url())
      return url.pathname === '/v1/m/governance/identities'
    })
    await alert.getByRole('button', { name: 'Retry' }).click()
    expect((await recovered).status()).toBe(200)
    await expect(
      page.getByRole('grid', { name: 'Non-human and human identities' }),
    ).toContainText('svc_pool')
    expect(failedGets).toBe(1)
    await page.unroute('**/v1/m/governance/identities**')
  })

  test('settings persist theme, density and language locally across reloads', async ({
    page,
  }) => {
    await loginDemo(page)
    // Settings opens on General, where theme, density and language are rows of one page.
    await page.goto('/settings')
    await page.getByRole('button', { name: 'General', exact: true }).click()

    await page.getByRole('radio', { name: 'Dark', exact: true }).click()
    await expect(page.locator('html')).toHaveClass(/dark/)
    expect(
      await page.evaluate(() => localStorage.getItem('olivares.theme')),
    ).toBe('dark')

    await page.getByRole('radio', { name: 'Compact', exact: true }).click()
    await expect(page.locator('html')).toHaveAttribute(
      'data-density',
      'compact',
    )

    await page.locator('#language').click()
    await page.getByRole('option', { name: 'Español', exact: true }).click()
    await expect(page.locator('html')).toHaveAttribute('lang', 'es')
    await expect(
      page.getByRole('heading', { name: 'General', exact: true }),
    ).toBeVisible()
    await expect
      .poll(async () => (await readConsolePreferences(page))['olivares.lang'])
      .toBe('es')

    await page.reload()
    await expect(page.locator('html')).toHaveClass(/dark/)
    await expect(page.locator('html')).toHaveAttribute('lang', 'es')
    await page.getByRole('button', { name: 'General', exact: true }).click()
    await expect(
      page.getByRole('radio', { name: 'Compacta', exact: true }),
    ).toHaveAttribute('aria-checked', 'true')
    await page.screenshot({ path: 'playwright-report/l4-settings-es-dark.png' })
  })

  test('recording is a Business feature: Community refuses every route with business_required', async ({
    page,
  }) => {
    const session = await loginDemo(page)
    const refused = await page.request.get('/v1/m/recording/notice', {
      headers: authHeaders(session),
    })
    expect(refused.status()).toBe(501)
    const body = (await refused.json()) as { error?: { code?: string } }
    expect(body.error?.code).toBe('business_required')
  })

  test('audit filters, evidence controls and export reflect the live ledger', async ({
    page,
  }) => {
    const session = await loginDemo(page)

    // Generate a tenant event through a real self-audited module read.
    const ssoRead = page.waitForResponse((response) => {
      return new URL(response.url()).pathname === '/v1/m/identity/sso'
    })
    await page.goto('/identity?tab=federation')
    expect((await ssoRead).status()).toBe(200)

    const initialRead = page.waitForResponse((response) => {
      const url = new URL(response.url())
      return (
        response.request().method() === 'GET' &&
        url.pathname === '/v1/audit' &&
        !url.searchParams.has('action')
      )
    })
    await page.goto('/audit')
    const initialResponse = await initialRead
    expect(initialResponse.status()).toBe(200)
    const initial = (await initialResponse.json()) as ListBody<AuditItem>
    expect(initial.items.length).toBeGreaterThan(0)
    const witness = initial.items[0]!
    const displayedInt = new Intl.NumberFormat(
      (await page.locator('html').getAttribute('lang')) ?? 'en',
      { maximumFractionDigits: 0 },
    )
    // The Ledger place (issue #2554): the entries are sentences in one list, the filters
    // are tokens in the top bar's query field, and an entry opens beside the list.
    const ledger = page.getByRole('list', { name: 'Ledger entries' })
    const entries = ledger.locator('[data-slot="entry"]')
    const query = page.getByRole('textbox', { name: 'Ledger query' })
    await expect(ledger).toBeVisible()
    // The query takes the room of the search it replaces and grows with its filters. The
    // page title is never under it, with no filter, with filters applied or with an entry
    // open: the title gives way and truncates.
    const viewport = page.viewportSize() ?? { width: 1280, height: 720 }
    // The field itself, not its slot, against the title the bar shows.
    const coveredPlaces = async () => {
      const read = await page.evaluate(() => {
        const field = document
          .querySelector('[data-slot="topbar-search"] [role="search"]')!
          .getBoundingClientRect()
        const title = document
          .querySelector('[data-slot="topbar-title"]')!
          .getBoundingClientRect()
        return {
          field: field.width,
          covered:
            title.width > 0 &&
            title.left < field.right &&
            field.left < title.right,
        }
      })
      expect(read.field).toBeGreaterThan(0)
      return read.covered ? ['the page title'] : []
    }
    // Every applied filter's key and value, and how many px of each do not read whole.
    const clippedTokens = async () => {
      const parts = await page
        .locator('[data-slot="topbar-search"] li')
        .evaluateAll((items) =>
          items.flatMap((li) =>
            [...li.querySelectorAll('span')]
              .filter((s) => s.children.length === 0)
              .map((s) => s.scrollWidth - s.clientWidth),
          ),
        )
      expect(parts.length).toBeGreaterThan(0)
      return parts.filter((px) => px > 0)
    }
    // With no filter: the title at the reference width and where room is short.
    await page.setViewportSize({ width: 1440, height: 900 })
    await expect(page.locator('[data-slot="topbar-search"]')).toBeVisible()
    expect(await coveredPlaces()).toEqual([])
    await page.setViewportSize({ width: 1101, height: 900 })
    await expect(page.locator('[data-slot="topbar-search"]')).toBeVisible()
    expect(await coveredPlaces()).toEqual([])
    await page.setViewportSize(viewport)
    // DTO cardinality checks below intentionally inspect the complete loaded ledger.
    const showInternal = page.getByRole('button', {
      name: 'Show internal records',
      exact: true,
    })
    if (await showInternal.isVisible()) await showInternal.click()
    const witnessRow = ledger.locator(`li[data-seq="${witness.seq}"]`)

    await page.setViewportSize({ width: 1440, height: 900 })
    try {
      const twoTokens = page.waitForResponse((response) => {
        const url = new URL(response.url())
        return (
          url.pathname === '/v1/audit' &&
          url.searchParams.get('action') === witness.action &&
          url.searchParams.has('since')
        )
      })
      await query.fill(`action:${witness.action} since:7d`)
      await query.press('Enter')
      expect((await twoTokens).status()).toBe(200)
      await expect(page.locator('[data-slot="topbar-search"] li')).toHaveCount(
        2,
      )
      expect(await coveredPlaces()).toEqual([])
      // At the reference width the applied filters read whole: the title gives way.
      expect(await clippedTokens()).toEqual([])
      // Where room is short the filters take it from the title, never cover it.
      await page.setViewportSize({ width: 1101, height: 900 })
      expect(await coveredPlaces()).toEqual([])
      expect(await clippedTokens()).toEqual([])
      await page.setViewportSize({ width: 1440, height: 900 })
      await witnessRow.locator('[data-slot="entry"]').click()
      await expect(
        page.getByRole('complementary', {
          name: `Entry ${displayedInt.format(witness.seq)}`,
        }),
      ).toBeVisible()
      expect(await coveredPlaces()).toEqual([])
      await page.getByRole('button', { name: 'Close the panel' }).click()
      // The action token goes first: an action-only read here would be the cached answer
      // of the typed action query below, which must reach the engine.
      const applied = page.locator('[data-slot="topbar-search"] li')
      await page
        .getByRole('button', { name: `Remove action:${witness.action}` })
        .click()
      await expect(applied).toHaveCount(1)
      await query.press('Backspace')
      await expect(applied).toHaveCount(0)
      // The filters gone, the title is still clear of the field.
      expect(await coveredPlaces()).toEqual([])
    } finally {
      await page.setViewportSize(viewport)
    }

    const systemRead = page.waitForResponse((response) => {
      const url = new URL(response.url())
      return (
        response.request().method() === 'GET' &&
        url.pathname === '/v1/audit/system'
      )
    })
    await page.getByRole('combobox', { name: 'Ledger scope' }).click()
    await page.getByRole('option', { name: 'System ledger' }).click()
    expect((await systemRead).status()).toBe(200)
    await expect
      .poll(() => new URL(page.url()).searchParams.get('scope'))
      .toBe('system')
    await expect(
      page.getByRole('combobox', { name: 'Ledger scope' }),
    ).toContainText('System ledger')
    await expect(
      page.getByText(
        'The system-tenant chain records cross-tenant administrative operations and is read-only here. Verification and export operate on tenant ledgers.',
        { exact: true },
      ),
    ).toBeVisible()
    await expect(
      page.getByRole('button', { name: 'Verify chain' }),
    ).toHaveCount(0)
    await expect(page.getByRole('button', { name: 'Export' })).toHaveCount(0)

    await page.getByRole('combobox', { name: 'Ledger scope' }).click()
    await page.getByRole('option', { name: 'Tenant ledger' }).click()
    await expect(
      page.getByRole('combobox', { name: 'Ledger scope' }),
    ).toContainText('Tenant ledger')
    await expect
      .poll(() => new URL(page.url()).searchParams.get('scope'))
      .toBeNull()
    // This is the already-proven tenant key and may be served from Query cache;
    // requiring a second HTTP response would make cache freshness a test input.
    await expect(witnessRow).toHaveCount(1)
    const info = await page.request.get('/v1/server-info', {
      headers: authHeaders(session),
    })
    expect(info.status()).toBe(200)
    const { edition } = (await info.json()) as { edition: string }
    await expect(
      page.getByRole('button', { name: 'Verify chain' }),
    ).toHaveCount(edition === 'community' ? 0 : 1)

    // Opening an entry projects the already-read DTO: its next entry is in the window
    // the list read, so the entry costs no read of its own.
    let auditGets = 0
    const countAuditGets = (request: { method(): string; url(): string }) => {
      const url = new URL(request.url())
      if (request.method() === 'GET' && url.pathname.startsWith('/v1/audit')) {
        auditGets += 1
      }
    }
    page.on('request', countAuditGets)
    await witnessRow.locator('[data-slot="entry"]').click()
    const evidence = page.getByRole('complementary', {
      name: `Entry ${displayedInt.format(witness.seq)}`,
    })
    await expect(evidence.getByRole('heading').first()).toBeVisible()
    const sequence = evidence
      .locator('dl > div')
      .filter({ has: page.getByText('Sequence', { exact: true }) })
    await expect(sequence.locator('dd')).toHaveText(String(witness.seq))
    await expect(evidence).toContainText(witness.action)
    await expect(
      evidence.getByRole('group', { name: 'Position in the chain' }),
    ).toContainText(witness.hash.slice(0, 6))
    expect(auditGets).toBe(0)
    page.off('request', countAuditGets)
    await page.getByRole('button', { name: 'Close the panel' }).click()
    await expect(evidence).toHaveCount(0)

    // A typed token is the API filter of the same name: the request carries it and the
    // list shows exactly what the engine answered.
    const actionRead = page.waitForResponse((response) => {
      const url = new URL(response.url())
      return (
        url.pathname === '/v1/audit' &&
        url.searchParams.get('action') === witness.action
      )
    })
    await query.fill(`action:${witness.action}`)
    await query.press('Enter')
    const actionResponse = await actionRead
    expect(actionResponse.status()).toBe(200)
    const matching = (await actionResponse.json()) as ListBody<AuditItem>
    expect(matching.items.length).toBeGreaterThan(0)
    expect(
      matching.items.every((item) => item.action.startsWith(witness.action)),
    ).toBe(true)
    await expect(entries).toHaveCount(matching.items.length)
    // The applied filter reads whole: what is being typed gives way, the filter does not.
    await expect(
      page
        .locator('[data-slot="topbar-search"] li')
        .filter({ hasText: witness.action }),
    ).toBeVisible()
    expect(await clippedTokens()).toEqual([])

    await page
      .getByRole('button', { name: `Remove action:${witness.action}` })
      .click()
    await expect
      .poll(() => new URL(page.url()).searchParams.has('action'))
      .toBe(false)
    await expect(witnessRow).toHaveCount(1)

    const textFilters = [
      [witness.action, witness.action, 'q'],
      ['person:user:l4-no-such-actor', 'user:l4-no-such-actor', 'actor'],
      ['kind:l4-no-such-kind', 'l4-no-such-kind', 'target_kind'],
      ['target:l4-no-such-id', 'l4-no-such-id', 'target_id'],
    ] as const
    for (const [typed, value, key] of textFilters) {
      const filtered = page.waitForResponse((response) => {
        const url = new URL(response.url())
        return (
          url.pathname === '/v1/audit' && url.searchParams.get(key) === value
        )
      })
      await query.fill(typed)
      await query.press('Enter')
      const filteredResponse = await filtered
      expect(filteredResponse.status()).toBe(200)
      const filteredBody =
        (await filteredResponse.json()) as ListBody<AuditItem>
      if (key === 'q') {
        const q = value.toLowerCase()
        expect(filteredBody.items.length).toBeGreaterThan(0)
        expect(
          filteredBody.items.every((item) =>
            [item.action, item.actor, item.target_kind, item.target_id]
              .filter(Boolean)
              .join(' ')
              .toLowerCase()
              .includes(q),
          ),
        ).toBe(true)
        await expect(entries).toHaveCount(filteredBody.items.length)
      } else {
        // These exact sentinels do not exist in the fresh ledger. If the handler
        // ignores a filter, its unfiltered rows make this assertion red.
        expect(filteredBody.items).toHaveLength(0)
        await expect(page.getByText('No evidence yet')).toBeVisible()
      }
      await page.getByRole('button', { name: `Remove ${typed}` }).click()
      await expect
        .poll(() => new URL(page.url()).searchParams.has(key))
        .toBe(false)
      await expect(witnessRow).toHaveCount(1)
    }

    for (const [typed, key] of [
      ['since:2099-01-01T00:00', 'since'],
      ['until:2000-01-01T00:00', 'until'],
    ] as const) {
      const filtered = page.waitForResponse((response) => {
        const url = new URL(response.url())
        return url.pathname === '/v1/audit' && url.searchParams.has(key)
      })
      await query.fill(typed)
      await query.press('Enter')
      const filteredResponse = await filtered
      expect(filteredResponse.status()).toBe(200)
      const filteredBody =
        (await filteredResponse.json()) as ListBody<AuditItem>
      expect(filteredBody.items).toHaveLength(0)
      await expect(page.getByText('No evidence yet')).toBeVisible()
      await page
        .getByRole('button', { name: `Remove ${typed}:00.000Z` })
        .click()
      await expect
        .poll(() => new URL(page.url()).searchParams.has(key))
        .toBe(false)
      await expect(witnessRow).toHaveCount(1)
    }

    // One typed query carries several filters; Backspace in the empty field takes them
    // back, the last first.
    await query.fill(`audit action:${witness.action}`)
    await query.press('Enter')
    await expect
      .poll(() => {
        const url = new URL(page.url())
        return [url.searchParams.get('q'), url.searchParams.get('action')]
      })
      .toEqual(['audit', witness.action])
    await query.press('Backspace')
    await query.press('Backspace')
    await expect
      .poll(() => {
        const url = new URL(page.url())
        return url.searchParams.has('q') || url.searchParams.has('action')
      })
      .toBe(false)
    await expect(witnessRow).toHaveCount(1)

    // A key the API has no filter for is refused out loud and asks nothing.
    await query.fill('tool:codex')
    await query.press('Enter')
    await expect(page.getByRole('alert')).toContainText(
      'tool:codex is not a ledger filter',
    )
    await query.fill('')

    if (edition === 'community') {
      await expect(
        page.getByRole('button', { name: 'Export', exact: true }),
      ).toHaveCount(0)
      for (const route of ['/v1/audit/verify', '/v1/audit/export']) {
        const refused = await page.request.get(route, {
          headers: authHeaders(session),
        })
        expect(refused.status()).toBe(501)
      }
      await page.screenshot({
        path: 'playwright-report/l4-audit-community.png',
      })
      return
    }

    const verifyRead = page.waitForResponse((response) => {
      return new URL(response.url()).pathname === '/v1/audit/verify'
    })
    await page.getByRole('button', { name: 'Verify chain' }).click()
    const verifyResponse = await verifyRead
    expect(verifyResponse.status()).toBe(200)
    const verdict = (await verifyResponse.json()) as {
      ok: boolean
      chain: { ok: boolean; checked: number }
      checkpoints: { status: string; count: number }
    }
    expect(verdict.ok).toBe(true)
    expect(verdict.chain.ok).toBe(true)
    expect(verdict.chain.checked).toBeGreaterThan(0)
    const chainText = `Chain · ${displayedInt.format(verdict.chain.checked)} links`
    const verifyStatus = page.getByRole('dialog').filter({
      has: page.getByText(chainText, { exact: true }),
    })
    await expect(verifyStatus).toContainText(
      `Chain · ${displayedInt.format(verdict.chain.checked)} links`,
    )
    if (verdict.checkpoints.status === 'pending') {
      await expect(verifyStatus).toContainText('No signed checkpoints yet')
    } else {
      await expect(verifyStatus).toContainText(
        `Checkpoints · ${displayedInt.format(verdict.checkpoints.count)} signed`,
      )
    }
    await page.keyboard.press('Escape')

    // Export one known event through the browser and then observe its audit side effect.
    await query.fill(`action:${witness.action}`)
    await query.press('Enter')
    await expect
      .poll(() => new URL(page.url()).searchParams.get('action'))
      .toBe(witness.action)
    await expect(witnessRow).toHaveCount(1)

    // The key that verifies an export offline is read once, when Export opens.
    let pubkeyGets = 0
    const countPubkey = (request: { method(): string; url(): string }) => {
      if (
        request.method() === 'GET' &&
        new URL(request.url()).pathname === '/v1/audit/pubkey'
      ) {
        pubkeyGets += 1
      }
    }
    page.on('request', countPubkey)
    const pubkeyRead = page.waitForResponse((response) => {
      return new URL(response.url()).pathname === '/v1/audit/pubkey'
    })
    await page.getByRole('button', { name: 'Export', exact: true }).click()
    const pubkeyResponse = await pubkeyRead
    expect(pubkeyResponse.status()).toBe(200)
    const pubkey = (await pubkeyResponse.json()) as {
      algorithm: string
      public_key: string
    }
    expect(pubkey.algorithm.toLowerCase()).toBe('ed25519')
    const pubkeyBytes = Buffer.from(pubkey.public_key, 'base64')
    expect(pubkeyBytes).toHaveLength(32)
    expect(
      pubkeyBytes.toString('base64'),
      'the browser must receive one canonical Ed25519 key, not permissively decoded garbage',
    ).toBe(pubkey.public_key)
    await expect(page.getByText('Ledger signing key')).toBeVisible()

    await page.getByLabel('From seq').fill(String(witness.seq))
    await page.getByLabel('To seq').fill(String(witness.seq))
    await page.getByRole('combobox', { name: 'Export format' }).click()
    await page.getByRole('option', { name: 'OCSF', exact: true }).click()
    const useCurrent = page.getByRole('checkbox', {
      name: 'Use current filters & range',
    })
    await expect(useCurrent).toBeChecked()
    await useCurrent.click()
    await expect(useCurrent).not.toBeChecked()
    await useCurrent.click()
    await expect(useCurrent).toBeChecked()
    expect(pubkeyGets).toBe(1)
    page.off('request', countPubkey)

    // verify reports how many contiguous links existed before this export. Use
    // the next sequence as the lower bound so a row left by an earlier retry
    // cannot satisfy the side-effect assertion for this click.
    const exportAuditFloor = verdict.chain.checked + 1

    const exportRead = page.waitForResponse((response) => {
      const url = new URL(response.url())
      return (
        response.request().method() === 'GET' &&
        url.pathname === '/v1/audit/export' &&
        url.searchParams.get('format') === 'ocsf' &&
        url.searchParams.get('from') === String(witness.seq) &&
        url.searchParams.get('to') === String(witness.seq) &&
        url.searchParams.get('action') === witness.action
      )
    })
    const downloadReady = page.waitForEvent('download')
    await page.getByRole('button', { name: 'Download', exact: true }).click()
    const exportResponse = await exportRead
    // The read is judged before the download is awaited: a refused export draws a toast and
    // saves nothing, and would otherwise surface only as a download that never comes.
    expect(exportResponse.status()).toBe(200)
    const download = await downloadReady
    expect(exportResponse.headers()['content-type']).toContain(
      'application/x-ndjson',
    )
    const downloadPath = await download.path()
    expect(downloadPath).not.toBeNull()
    // Chromium hands Content-Disposition bodies to the download stream; its
    // Playwright Response body may legitimately be empty. The saved download
    // is the operator's artifact and therefore the canonical bytes to inspect.
    const exportText = await readFile(downloadPath!, 'utf8')
    expect(exportText.length).toBeGreaterThan(0)
    const lines = exportText.trim().split('\n')
    expect(lines).toHaveLength(2)
    const eventLine = JSON.parse(lines[0] ?? '{}') as {
      class_uid?: number
      api?: { operation?: string }
      unmapped?: Record<string, unknown>
    }
    expect(eventLine.class_uid).toBe(6003)
    expect(eventLine.api?.operation).toBe(witness.action)
    expect(eventLine.unmapped?.['ai.olivares.audit.seq']).toBe(witness.seq)
    expect(eventLine.unmapped?.['ai.olivares.audit.hash']).toBe(witness.hash)
    const terminator = JSON.parse(lines.at(-1) ?? '{}') as {
      export_complete?: boolean
      count?: number
      last_seq?: number
    }
    expect(terminator).toEqual(
      expect.objectContaining({
        export_complete: true,
        count: 1,
        last_seq: witness.seq,
      }),
    )
    expect(download.suggestedFilename()).toBe('olivares-audit-ocsf.ndjson')

    await expect
      .poll(
        async () => {
          const response = await page.request.get(
            `/v1/audit?from=${exportAuditFloor}&action=audit.export&limit=100`,
            { headers: authHeaders(session) },
          )
          assertSessionAccepted(response)
          if (!response.ok()) return false
          const body = (await response.json()) as ListBody<AuditItem>
          return body.items.some(
            (item) =>
              item.action === 'audit.export' && item.seq >= exportAuditFloor,
          )
        },
        { message: 'export must append its audit.export evidence row' },
      )
      .toBe(true)

    await page.screenshot({ path: 'playwright-report/l4-audit-export.png' })
  })
})
