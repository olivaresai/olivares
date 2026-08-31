// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { Buffer } from 'node:buffer'
import { readFile } from 'node:fs/promises'
import { expect, test, type Locator, type Page } from '@playwright/test'

// Functional tranche L4 runs against the same disposable, genuinely seeded engine as
// demo-graph.spec.ts. Responses are observed, never fulfilled by Playwright.
const demoTenant = process.env.DEMO_TENANT ?? ''
const DEMO_EMAIL = 'demo@olivares.local'
const DEMO_PASSWORD = 'olivares-demo-estate'

// Auth responses carry a live bearer for the disposable engine. Retaining a
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

interface RecordingItem {
  id: string
  subject: string
  status: string
  opened_at: string
}

interface RecordingConfigBody {
  namespaces: string[]
  consent: 'notice' | 'required'
  idle_seconds: number
  retention_days: number
  ai_summaries: boolean
  breakglass_always: boolean
  retention_enforced: boolean
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

function authHeaders(token: string): Record<string, string> {
  return {
    Authorization: `Bearer ${token}`,
    'X-Olivares-Tenant': demoTenant,
  }
}

async function loginDemo(page: Page): Promise<string> {
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
  const body = (await response.json()) as { token: string }
  await expect(
    page.getByRole('link', { name: 'Inventory', exact: true }),
  ).toBeVisible({ timeout: 15_000 })
  return body.token
}

async function waitForDemoRoster(page: Page, token: string): Promise<void> {
  await expect
    .poll(
      async () => {
        const response = await page.request.get('/v1/m/governance/identities', {
          headers: authHeaders(token),
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
  await expect(grid).not.toHaveAttribute('aria-rowcount')
  await expect(grid.getByText(emptyTitle, { exact: true })).toBeVisible()
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

  test('identity tabs, redirect checker, roster and passkey draft have real effects', async ({
    page,
  }) => {
    const token = await loginDemo(page)
    await waitForDemoRoster(page, token)

    // Install this before the first identity navigation. Counting only while
    // the WIF tab is selected would miss a forbidden eager prefetch on mount.
    let wifGets = 0
    const countWif = (request: { method(): string; url(): string }) => {
      if (
        request.method() === 'GET' &&
        new URL(request.url()).pathname === '/v1/m/identity/wif'
      ) {
        wifGets += 1
      }
    }
    page.on('request', countWif)

    const ssoRead = page.waitForResponse((response) => {
      const url = new URL(response.url())
      return (
        response.request().method() === 'GET' &&
        url.pathname === '/v1/m/identity/sso'
      )
    })
    await page.goto('/identity')
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

    const lifecycleRead = page.waitForResponse((response) => {
      const url = new URL(response.url())
      return url.pathname === '/v1/m/governance/nhi/posture'
    })
    await selectTab('NHI lifecycle', 'lifecycle')
    expect((await lifecycleRead).status()).toBe(200)
    await expect(
      page.getByRole('heading', { name: 'Lifecycle posture' }),
    ).toBeVisible()

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

    await selectTab('WIF graph', 'wif')
    await expect(
      page.getByRole('heading', { name: 'Step-up authentication required' }),
    ).toBeVisible()
    await page.waitForTimeout(500)
    expect(wifGets).toBe(0)
    page.off('request', countWif)

    const keysRead = page.waitForResponse((response) => {
      const url = new URL(response.url())
      return url.pathname === '/v1/m/identity/external-keys'
    })
    await selectTab('Key & residency posture', 'posture')
    expect((await keysRead).status()).toBe(200)
    await expect(
      page.getByRole('heading', { name: 'External keys (CMEK)' }),
    ).toBeVisible()

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
    await expect(register).toBeDisabled()
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
    const token = await loginDemo(page)
    await waitForDemoRoster(page, token)

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
    await page.goto('/settings')
    await page.getByRole('tab', { name: 'Appearance' }).click()

    await page.locator('#theme').click()
    await page.getByRole('option', { name: 'Dark', exact: true }).click()
    await expect(page.locator('html')).toHaveClass(/dark/)
    expect(
      await page.evaluate(() => localStorage.getItem('olivares.theme')),
    ).toBe('dark')

    await page.locator('#density').click()
    await page.getByRole('option', { name: 'Compact', exact: true }).click()
    expect(
      await page.evaluate(() => {
        const stored = localStorage.getItem('olivares.prefs')
        return stored
          ? (JSON.parse(stored) as { state?: { density?: string } }).state
              ?.density
          : null
      }),
    ).toBe('compact')

    await page.locator('#language').click()
    await page.getByRole('option', { name: 'Español', exact: true }).click()
    await expect(page.locator('html')).toHaveAttribute('lang', 'es')
    await expect(page.getByRole('heading', { name: 'Ajustes' })).toBeVisible()
    expect(
      await page.evaluate(() => localStorage.getItem('olivares.lang')),
    ).toBe('es')

    await page.reload()
    await expect(page.locator('html')).toHaveClass(/dark/)
    await expect(page.locator('html')).toHaveAttribute('lang', 'es')
    await page.getByRole('tab', { name: 'Apariencia' }).click()
    await expect(page.locator('#density')).toContainText('Compacta')
    await page.screenshot({ path: 'playwright-report/l4-settings-es-dark.png' })
  })

  test('recording filters, viewer navigation and policy save use live state', async ({
    page,
  }) => {
    const token = await loginDemo(page)

    // Touch a recorded namespace first, so the disposable engine has a live session.
    const graphRead = page.waitForResponse((response) => {
      const url = new URL(response.url())
      return url.pathname === '/v1/m/accessmap/graph'
    })
    await page.goto('/access-map')
    expect((await graphRead).status()).toBe(200)
    const noticeResponse = await page.request.get('/v1/m/recording/notice', {
      headers: authHeaders(token),
    })
    expect(noticeResponse.status()).toBe(200)
    const notice = (await noticeResponse.json()) as { session_id?: string }
    expect(notice.session_id).toBeTruthy()

    const sessionsRead = page.waitForResponse((response) => {
      const url = new URL(response.url())
      return (
        response.request().method() === 'GET' &&
        url.pathname === '/v1/m/recording/sessions' &&
        !url.searchParams.has('status') &&
        !url.searchParams.has('opened_after') &&
        !url.searchParams.has('opened_before')
      )
    })
    const configRead = page.waitForResponse((response) => {
      const url = new URL(response.url())
      return (
        response.request().method() === 'GET' &&
        url.pathname === '/v1/m/recording/config'
      )
    })
    await page.goto('/recordings')
    const sessionsResponse = await sessionsRead
    const configResponse = await configRead
    expect(sessionsResponse.status()).toBe(200)
    expect(configResponse.status()).toBe(200)
    const sessions = (await sessionsResponse.json()) as ListBody<RecordingItem>
    const original = (await configResponse.json()) as RecordingConfigBody
    expect(sessions.items.length).toBeGreaterThan(0)
    // /notice resolves the active recording by the exact authenticated credential;
    // selecting by that ID avoids confusing it with earlier logins by the same user.
    const currentSession = sessions.items.find(
      (item) => item.id === notice.session_id,
    )
    expect(currentSession).toBeDefined()
    const witness = currentSession!
    expect(witness.status).toBe('active')
    const recordingsGrid = page.getByRole('grid', { name: 'Recordings' })

    const sealedRead = page.waitForResponse((response) => {
      const url = new URL(response.url())
      return (
        url.pathname === '/v1/m/recording/sessions' &&
        url.searchParams.get('status') === 'sealed'
      )
    })
    await page.getByRole('combobox', { name: 'Filter by status' }).click()
    await page.getByRole('option', { name: 'Sealed', exact: true }).click()
    await expect(
      page.getByRole('combobox', { name: 'Filter by status' }),
    ).toContainText('Sealed')
    await expect
      .poll(() => new URL(page.url()).searchParams.get('status'))
      .toBe('sealed')
    const sealedResponse = await sealedRead
    expect(sealedResponse.status()).toBe(200)
    const sealed = (await sealedResponse.json()) as ListBody<RecordingItem>
    expect(
      sealed.items,
      'the fresh estate has active sessions, so ignoring status=sealed must fail',
    ).toHaveLength(0)
    await expectGridCardinality(recordingsGrid, 0, 'No recording sessions yet')

    const activeRead = page.waitForResponse((response) => {
      const url = new URL(response.url())
      return (
        url.pathname === '/v1/m/recording/sessions' &&
        url.searchParams.get('status') === 'active'
      )
    })
    await page.getByRole('combobox', { name: 'Filter by status' }).click()
    await page.getByRole('option', { name: 'Active', exact: true }).click()
    await expect(
      page.getByRole('combobox', { name: 'Filter by status' }),
    ).toContainText('Active')
    await expect
      .poll(() => new URL(page.url()).searchParams.get('status'))
      .toBe('active')
    const activeResponse = await activeRead
    expect(activeResponse.status()).toBe(200)
    const active = (await activeResponse.json()) as ListBody<RecordingItem>
    expect(active.items.length).toBeGreaterThan(0)
    expect(active.items.every((item) => item.status === 'active')).toBe(true)
    await expectGridCardinality(
      recordingsGrid,
      active.items.length,
      'No recording sessions yet',
    )

    const afterDay = witness!.opened_at.slice(0, 10)
    const tomorrow = new Date(`${afterDay}T00:00:00.000Z`)
    tomorrow.setUTCDate(tomorrow.getUTCDate() + 1)
    const beforeDay = tomorrow.toISOString().slice(0, 10)
    const afterIso = `${afterDay}T00:00:00.000Z`
    const beforeIso = `${beforeDay}T00:00:00.000Z`

    const futureRead = page.waitForResponse((response) => {
      const url = new URL(response.url())
      return (
        url.pathname === '/v1/m/recording/sessions' &&
        url.searchParams.get('opened_after') === beforeIso
      )
    })
    await page.getByLabel('Opened after').fill(beforeDay)
    const futureResponse = await futureRead
    expect(futureResponse.status()).toBe(200)
    const future = (await futureResponse.json()) as ListBody<RecordingItem>
    expect(
      future.items,
      'ignoring a future opened_after bound must expose the active witness and fail',
    ).toHaveLength(0)
    await expectGridCardinality(recordingsGrid, 0, 'No recording sessions yet')

    const afterRead = page.waitForResponse((response) => {
      const url = new URL(response.url())
      return (
        url.pathname === '/v1/m/recording/sessions' &&
        url.searchParams.get('opened_after') === afterIso
      )
    })
    await page.getByLabel('Opened after').fill(afterDay)
    const afterResponse = await afterRead
    expect(afterResponse.status()).toBe(200)
    const afterItems = (await afterResponse.json()) as ListBody<RecordingItem>
    expect(
      afterItems.items.every(
        (item) => Date.parse(item.opened_at) >= Date.parse(afterIso),
      ),
    ).toBe(true)
    await expectGridCardinality(
      recordingsGrid,
      afterItems.items.length,
      'No recording sessions yet',
    )

    const pastRead = page.waitForResponse((response) => {
      const url = new URL(response.url())
      return (
        url.pathname === '/v1/m/recording/sessions' &&
        url.searchParams.get('opened_after') === afterIso &&
        url.searchParams.get('opened_before') === afterIso
      )
    })
    await page.getByLabel('Opened before').fill(afterDay)
    const pastResponse = await pastRead
    expect(pastResponse.status()).toBe(200)
    const past = (await pastResponse.json()) as ListBody<RecordingItem>
    expect(
      past.items,
      'ignoring the midnight opened_before bound must expose the witness and fail',
    ).toHaveLength(0)
    await expectGridCardinality(recordingsGrid, 0, 'No recording sessions yet')

    const beforeRead = page.waitForResponse((response) => {
      const url = new URL(response.url())
      return (
        url.pathname === '/v1/m/recording/sessions' &&
        url.searchParams.get('opened_after') === afterIso &&
        url.searchParams.get('opened_before') === beforeIso
      )
    })
    await page.getByLabel('Opened before').fill(beforeDay)
    const beforeResponse = await beforeRead
    expect(beforeResponse.status()).toBe(200)
    const bounded = (await beforeResponse.json()) as ListBody<RecordingItem>
    expect(bounded.items.some((item) => item.id === witness!.id)).toBe(true)
    expect(
      bounded.items.every(
        (item) => Date.parse(item.opened_at) <= Date.parse(beforeIso),
      ),
    ).toBe(true)
    await expectGridCardinality(
      recordingsGrid,
      bounded.items.length,
      'No recording sessions yet',
    )

    // The search is deliberately recorded only as a loaded-page effect. An exact
    // opened_at value gives this page one DTO-backed match even though several
    // active sessions share the same subject.
    let listGets = 0
    const countLists = (request: { method(): string; url(): string }) => {
      if (
        request.method() === 'GET' &&
        new URL(request.url()).pathname === '/v1/m/recording/sessions'
      ) {
        listGets += 1
      }
    }
    page.on('request', countLists)
    const subjectSearch = page.getByPlaceholder('Search subject, grant…')
    await subjectSearch.fill(witness.opened_at)
    const subjectMatches = bounded.items.filter((item) =>
      item.opened_at.includes(witness.opened_at),
    )
    expect(subjectMatches).toHaveLength(1)
    await expectGridCardinality(
      recordingsGrid,
      subjectMatches.length,
      'No recording sessions yet',
    )
    await expect(
      page
        .getByRole('grid', { name: 'Recordings' })
        .getByRole('row')
        .filter({ hasText: witness.subject }),
    ).toHaveCount(subjectMatches.length)
    await page.waitForTimeout(500)
    expect(listGets).toBe(0)
    page.off('request', countLists)
    await subjectSearch.clear()

    const witnessIndex = bounded.items.findIndex(
      (item) => item.id === witness.id,
    )
    expect(witnessIndex).toBeGreaterThanOrEqual(0)
    const unifiedRead = page.waitForResponse((response) => {
      const url = new URL(response.url())
      return url.pathname === `/v1/m/recording/sessions/${witness.id}/unified`
    })
    await page
      .getByRole('grid', { name: 'Recordings' })
      .getByRole('row')
      .nth(witnessIndex + 1)
      .click()
    expect((await unifiedRead).status()).toBe(200)
    await page.waitForURL(`**/session-viewer/${witness.id}`)
    await expect(
      page.getByRole('heading', { name: 'Session Recording Viewer' }),
    ).toBeVisible()
    await page.getByRole('link', { name: 'Back to recordings' }).click()
    await page.waitForURL('**/recordings')

    // Draft-only controls must not write before Save.
    let configPuts = 0
    const countConfigPuts = (request: { method(): string; url(): string }) => {
      if (
        request.method() === 'PUT' &&
        new URL(request.url()).pathname === '/v1/m/recording/config'
      ) {
        configPuts += 1
      }
    }
    page.on('request', countConfigPuts)

    const namespaceWitness = original.namespaces.at(-1)
    expect(namespaceWitness).toBeTruthy()
    const removeNamespace = page.getByRole('button', {
      name: `Remove ${namespaceWitness}`,
    })
    await removeNamespace.click()
    const namespace = page.getByLabel('Add a recorded namespace')
    await namespace.fill(namespaceWitness!)
    await namespace.press('Enter')
    await expect(removeNamespace).toBeVisible()
    await removeNamespace.click()
    await namespace.fill(namespaceWitness!)
    await page.getByRole('button', { name: 'Add', exact: true }).click()
    await expect(removeNamespace).toBeVisible()

    const consent = page.getByRole('switch', {
      name: 'Require acknowledgement before privileged actions',
    })
    const consentWasRequired = original.consent === 'required'
    if (consentWasRequired) await expect(consent).toBeChecked()
    else await expect(consent).not.toBeChecked()
    await consent.click()
    if (consentWasRequired) await expect(consent).not.toBeChecked()
    else await expect(consent).toBeChecked()
    await consent.click()
    if (consentWasRequired) await expect(consent).toBeChecked()
    else await expect(consent).not.toBeChecked()

    const idle = page.getByLabel('Idle-seal timeout (seconds)')
    const retention = page.getByLabel('Retention class (days)')
    const ai = page.getByRole('switch', { name: 'AI-derived summaries' })
    await retention.fill(String(original.retention_days + 1))
    await expect(retention).toHaveValue(String(original.retention_days + 1))
    await retention.fill(String(original.retention_days))
    if (original.ai_summaries) await expect(ai).toBeChecked()
    else await expect(ai).not.toBeChecked()
    await ai.click()
    if (original.ai_summaries) await expect(ai).not.toBeChecked()
    else await expect(ai).toBeChecked()
    await ai.click()
    if (original.ai_summaries) await expect(ai).toBeChecked()
    else await expect(ai).not.toBeChecked()
    expect(configPuts).toBe(0)

    const changedIdle =
      original.idle_seconds < 86_400
        ? original.idle_seconds + 1
        : original.idle_seconds - 1
    await idle.fill(String(changedIdle))
    const saved = page.waitForResponse((response) => {
      const url = new URL(response.url())
      return (
        response.request().method() === 'PUT' &&
        url.pathname === '/v1/m/recording/config'
      )
    })
    try {
      await page.getByRole('button', { name: 'Save policy' }).click()
      const savedResponse = await saved
      expect(savedResponse.status()).toBe(200)
      expect(savedResponse.request().postDataJSON()).toEqual({
        namespaces: original.namespaces,
        consent: original.consent,
        idle_seconds: changedIdle,
        retention_days: original.retention_days,
        ai_summaries: original.ai_summaries,
      })
      await expect(page.getByText('Recording policy saved')).toBeVisible()
      const persisted = await page.request.get('/v1/m/recording/config', {
        headers: authHeaders(token),
      })
      expect(persisted.status()).toBe(200)
      const persistedBody = (await persisted.json()) as RecordingConfigBody
      expect({
        namespaces: persistedBody.namespaces,
        consent: persistedBody.consent,
        idle_seconds: persistedBody.idle_seconds,
        retention_days: persistedBody.retention_days,
        ai_summaries: persistedBody.ai_summaries,
      }).toEqual({
        namespaces: original.namespaces,
        consent: original.consent,
        idle_seconds: changedIdle,
        retention_days: original.retention_days,
        ai_summaries: original.ai_summaries,
      })
      await page.waitForTimeout(500)
      expect(configPuts).toBe(1)
      await page.screenshot({ path: 'playwright-report/l4-recordings.png' })
    } finally {
      const restored = await page.request.put('/v1/m/recording/config', {
        headers: authHeaders(token),
        data: {
          namespaces: original.namespaces,
          consent: original.consent,
          idle_seconds: original.idle_seconds,
          retention_days: original.retention_days,
          ai_summaries: original.ai_summaries,
        },
      })
      expect(restored.status()).toBe(200)
      const reread = await page.request.get('/v1/m/recording/config', {
        headers: authHeaders(token),
      })
      expect(reread.status()).toBe(200)
      const restoredBody = (await reread.json()) as RecordingConfigBody
      expect({
        namespaces: restoredBody.namespaces,
        consent: restoredBody.consent,
        idle_seconds: restoredBody.idle_seconds,
        retention_days: restoredBody.retention_days,
        ai_summaries: restoredBody.ai_summaries,
      }).toEqual({
        namespaces: original.namespaces,
        consent: original.consent,
        idle_seconds: original.idle_seconds,
        retention_days: original.retention_days,
        ai_summaries: original.ai_summaries,
      })
    }
    page.off('request', countConfigPuts)
    expect(configPuts).toBe(1)
  })

  test('audit filters, evidence controls and export reflect the live ledger', async ({
    page,
  }) => {
    const token = await loginDemo(page)

    // Generate a tenant event through a real self-audited module read.
    const graphRead = page.waitForResponse((response) => {
      return new URL(response.url()).pathname === '/v1/m/accessmap/graph'
    })
    await page.goto('/access-map')
    expect((await graphRead).status()).toBe(200)

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
    const auditGrid = page.getByRole('grid', { name: 'Audit ledger' })
    const witnessRow = auditGrid.getByRole('row').filter({
      has: page.getByRole('gridcell', {
        name: String(witness.seq),
        exact: true,
      }),
    })

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
    await expect(
      page.getByRole('button', { name: 'Verify chain' }),
    ).toBeVisible()

    // Opening a row is intentionally a pure projection of the already-read DTO.
    let auditGets = 0
    const countAuditGets = (request: { method(): string; url(): string }) => {
      const url = new URL(request.url())
      if (request.method() === 'GET' && url.pathname.startsWith('/v1/audit')) {
        auditGets += 1
      }
    }
    page.on('request', countAuditGets)
    await expect(witnessRow).toHaveCount(1)
    await witnessRow.click()
    const evidence = page.getByRole('dialog')
    await expect(
      evidence.getByRole('heading', { name: `Event #${witness.seq}` }),
    ).toBeVisible()
    await expect(evidence).toContainText(witness.action)
    await expect(evidence).toContainText('Chain integrity')
    expect(auditGets).toBe(0)
    await evidence.getByRole('button', { name: 'Close' }).click()
    await expect(evidence).toHaveCount(0)

    // The loaded-row search is local and says so in its placeholder.
    const loadedSearch = page.getByPlaceholder(
      'Filter loaded rows only (client-side)…',
    )
    await loadedSearch.fill(witness.hash)
    const visibleRows = auditGrid.locator('tbody tr')
    await expectGridCardinality(auditGrid, 1, 'No evidence yet')
    await expect(visibleRows).toHaveCount(1)
    await expect(visibleRows.first()).toContainText(witness.action)
    await page.waitForTimeout(500)
    expect(auditGets).toBe(0)
    await loadedSearch.clear()
    page.off('request', countAuditGets)

    const actionInput = page.getByLabel('Action prefix', { exact: true })
    const actionRead = page.waitForResponse((response) => {
      const url = new URL(response.url())
      return (
        url.pathname === '/v1/audit' &&
        url.searchParams.get('action') === witness.action
      )
    })
    await actionInput.fill(witness.action)
    const actionResponse = await actionRead
    expect(actionResponse.status()).toBe(200)
    const matching = (await actionResponse.json()) as ListBody<AuditItem>
    expect(matching.items.length).toBeGreaterThan(0)
    expect(
      matching.items.every((item) => item.action.startsWith(witness.action)),
    ).toBe(true)
    await expectGridCardinality(
      auditGrid,
      matching.items.length,
      'No evidence yet',
    )

    await page
      .getByRole('button', { name: 'Clear Action prefix filter' })
      .click()
    await expect
      .poll(() => new URL(page.url()).searchParams.has('action'))
      .toBe(false)
    await expect(actionInput).toHaveValue('')
    await expect(witnessRow).toHaveCount(1)

    const textFilters = [
      ['Search ledger', witness.action, 'q'],
      ['Actor', 'l4-no-such-actor', 'actor'],
      ['Target kind', 'l4-no-such-kind', 'target_kind'],
      ['Target ID', 'l4-no-such-id', 'target_id'],
    ] as const
    for (const [label, value, key] of textFilters) {
      const filtered = page.waitForResponse((response) => {
        const url = new URL(response.url())
        return (
          url.pathname === '/v1/audit' && url.searchParams.get(key) === value
        )
      })
      await page.getByLabel(label, { exact: true }).fill(value)
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
      } else {
        // These exact sentinels do not exist in the fresh ledger. If the handler
        // ignores a filter, its unfiltered rows make this assertion red.
        expect(filteredBody.items).toHaveLength(0)
      }
      await expectGridCardinality(
        auditGrid,
        filteredBody.items.length,
        'No evidence yet',
      )
      await page.getByRole('button', { name: `Clear ${label} filter` }).click()
      await expect
        .poll(() => new URL(page.url()).searchParams.has(key))
        .toBe(false)
      await expect(page.getByLabel(label, { exact: true })).toHaveValue('')
      await expect(witnessRow).toHaveCount(1)
    }

    for (const [label, value, key] of [
      ['Since', '2099-01-01T00:00', 'since'],
      ['Until', '2000-01-01T00:00', 'until'],
    ] as const) {
      const filtered = page.waitForResponse((response) => {
        const url = new URL(response.url())
        return url.pathname === '/v1/audit' && url.searchParams.has(key)
      })
      await page.getByLabel(label, { exact: true }).fill(value)
      const filteredResponse = await filtered
      expect(filteredResponse.status()).toBe(200)
      const filteredBody =
        (await filteredResponse.json()) as ListBody<AuditItem>
      expect(filteredBody.items).toHaveLength(0)
      await expectGridCardinality(auditGrid, 0, 'No evidence yet')
      await expect(page.getByLabel(label, { exact: true })).toHaveValue(value)
      await page.getByRole('button', { name: `Clear ${label} filter` }).click()
      await expect
        .poll(() => new URL(page.url()).searchParams.has(key))
        .toBe(false)
      await expect(page.getByLabel(label, { exact: true })).toHaveValue('')
      await expect(witnessRow).toHaveCount(1)
    }

    // Clear-all is one shared bridge in the inventory; exercise it once, explicitly.
    const ledgerSearch = page.getByLabel('Search ledger', { exact: true })
    await ledgerSearch.fill('audit')
    await actionInput.fill(witness.action)
    await expect(ledgerSearch).toHaveValue('audit')
    await expect(actionInput).toHaveValue(witness.action)
    await expect
      .poll(() => {
        const url = new URL(page.url())
        return [url.searchParams.get('q'), url.searchParams.get('action')]
      })
      .toEqual(['audit', witness.action])
    await page.getByRole('button', { name: 'Clear all filters' }).click()
    await expect
      .poll(() => {
        const url = new URL(page.url())
        return url.searchParams.has('q') || url.searchParams.has('action')
      })
      .toBe(false)
    await expect(ledgerSearch).toHaveValue('')
    await expect(actionInput).toHaveValue('')
    await expect(witnessRow).toHaveCount(1)

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
    const verifyStatus = page.getByRole('status').filter({ hasText: 'Chain' })
    await expect(verifyStatus).toContainText(
      `Chain · ${verdict.chain.checked} links`,
    )
    if (verdict.checkpoints.status === 'pending') {
      await expect(verifyStatus).toContainText('No signed checkpoints yet')
    } else {
      await expect(verifyStatus).toContainText(
        `Checkpoints · ${verdict.checkpoints.count} signed`,
      )
    }

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
    await page.getByRole('button', { name: 'Verification key' }).click()
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
    await page.getByRole('button', { name: 'Verification key' }).click()
    await expect(page.getByText('Ledger signing key')).toHaveCount(0)
    await page.waitForTimeout(500)
    expect(pubkeyGets).toBe(1)
    page.off('request', countPubkey)

    // Export one known event through the browser and then observe its audit side effect.
    await actionInput.fill(witness.action)
    await expect
      .poll(() => new URL(page.url()).searchParams.get('action'))
      .toBe(witness.action)
    await expect(witnessRow).toHaveCount(1)
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
    await page.getByRole('button', { name: 'Export', exact: true }).click()
    const exportResponse = await exportRead
    const download = await downloadReady
    expect(exportResponse.status()).toBe(200)
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
            { headers: authHeaders(token) },
          )
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
