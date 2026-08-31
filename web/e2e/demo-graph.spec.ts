// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { expect, test, type Page } from '@playwright/test'

// UI tramo of the E2E: the embedded web rendering REAL seeded data from a live
// olivares (no /v1 mocks). scripts/web-e2e-demo.sh boots `serve --seed-demo`
// and passes the demo tenant id; the spec performs the REAL login and only seeds
// the *active tenant selection* (a superadmin has no default membership), exactly
// as the org switcher would set it. Skipped unless DEMO_TENANT is provided.
const demoTenant = process.env.DEMO_TENANT ?? ''
const DEMO_EMAIL = 'demo@olivares.local'
const DEMO_PASSWORD = 'olivares-demo-estate'

// loginDemo observes a live bearer response. Never retain it in a failure trace;
// the explicit screenshots below are taken only on authenticated views.
test.use({ trace: 'off' })

interface LiveAccessEdge {
  id: string
  origin_id: string
  origin_kind: string
  origin_ref?: string
  resource_id: string
  resource_kind?: string
  resource_ref?: string
  mode: string
  confidence: string
  signal_source?: string
  signal_sources?: string
  tool_ref?: string
}

interface LiveAccessGraph {
  nodes: Array<{ id: string; ref?: string }>
  edges: LiveAccessEdge[]
}

interface LiveDrift {
  unexpected_accesses: Array<{
    edge: LiveAccessEdge
    reconciliation_pending?: boolean
  }>
  unused_grants: Array<{ edge: LiveAccessEdge }>
  unexpected_count: number
  unused_count: number
}

function authHeaders(token: string): Record<string, string> {
  return {
    Authorization: `Bearer ${token}`,
    'X-Olivares-Tenant': demoTenant,
  }
}

async function loginDemo(page: Page): Promise<string> {
  const loginResponse = page.waitForResponse((response) => {
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
  const response = await loginResponse
  expect(response.status(), 'the demo login must reach the real handler').toBe(
    200,
  )
  const body = (await response.json()) as { token: string }
  await expect(
    page.getByRole('link', { name: 'Inventory', exact: true }),
  ).toBeVisible({ timeout: 15_000 })
  return body.token
}

function nodeIDs(edges: LiveAccessEdge[]): string[] {
  const ids = new Set<string>()
  for (const edge of edges) {
    ids.add(edge.origin_id)
    ids.add(edge.resource_id)
  }
  return [...ids].sort()
}

async function expectFlowMatches(
  page: Page,
  edges: LiveAccessEdge[],
): Promise<void> {
  await expect(page.getByText('High-density view')).toHaveCount(0)
  await expect
    .poll(async () =>
      page.locator('.react-flow__edge').evaluateAll((elements) =>
        elements
          .map((element) => element.getAttribute('data-id'))
          .filter((id): id is string => id !== null)
          .sort(),
      ),
    )
    .toEqual(edges.map((edge) => edge.id).sort())
  await expect
    .poll(async () =>
      page.locator('.react-flow__node').evaluateAll((elements) =>
        elements
          .map((element) => element.getAttribute('data-id'))
          .filter((id): id is string => id !== null)
          .sort(),
      ),
    )
    .toEqual(nodeIDs(edges))
}

async function waitForDemoAccessEstate(
  page: Page,
  token: string,
): Promise<void> {
  await expect
    .poll(
      async () => {
        const [graphResponse, driftResponse] = await Promise.all([
          page.request.get('/v1/m/accessmap/graph?limit=500', {
            headers: authHeaders(token),
          }),
          page.request.get('/v1/m/accessmap/drift', {
            headers: authHeaders(token),
          }),
        ])
        if (!graphResponse.ok() || !driftResponse.ok()) return false
        const graph = (await graphResponse.json()) as LiveAccessGraph
        const drift = (await driftResponse.json()) as LiveDrift
        const hasSensitiveResource = graph.nodes.some(
          (node) => node.ref === 'appdb.public.secrets',
        )
        const hasFirmDrift = drift.unexpected_accesses.some(
          ({ edge, reconciliation_pending: pending }) =>
            edge.origin_ref === 'svc_pool' &&
            edge.resource_ref === 'appdb.public.logs' &&
            !pending,
        )
        return hasSensitiveResource && hasFirmDrift
      },
      {
        message:
          'the asynchronous demo gather must materialize the sensitive resource and firm drift witness',
        timeout: 15_000,
      },
    )
    .toBe(true)
}

async function expectOneRequestAfterQuietPeriod(
  page: Page,
  count: () => number,
): Promise<void> {
  await expect.poll(count).toBe(1)
  await page.waitForTimeout(500)
  expect(count()).toBe(1)
}

function textMatches(edge: LiveAccessEdge, query: string): boolean {
  return [
    edge.origin_ref,
    edge.resource_ref,
    edge.tool_ref,
    edge.resource_kind,
    edge.origin_kind,
    edge.signal_sources,
  ]
    .filter(Boolean)
    .join(' ')
    .toLowerCase()
    .includes(query.toLowerCase())
}

test.describe('embedded web over real seeded data', () => {
  test.describe.configure({ timeout: 120_000 })

  test.skip(
    !demoTenant,
    'DEMO_TENANT not set — run via scripts/web-e2e-demo.sh',
  )

  test.beforeEach(async ({ page }) => {
    // Pre-select the demo org (the persisted tenant store the switcher writes) so
    // X-Olivares-Tenant is the demo tenant. Auth itself is the real login below.
    await page.addInitScript(
      ([tenant]) => {
        localStorage.setItem(
          'olivares.tenant',
          JSON.stringify({ state: { activeTenant: tenant }, version: 0 }),
        )
        localStorage.setItem('olivares.lang', 'en')
      },
      [demoTenant],
    )
  })

  test('real graph refresh, filters and drift match live DTOs', async ({
    page,
  }) => {
    const token = await loginDemo(page)
    await waitForDemoAccessEstate(page, token)

    // The product's visual differentiator, rendering REAL seeded edges.
    const initialGraphResponse = page.waitForResponse((response) => {
      const url = new URL(response.url())
      return (
        response.request().method() === 'GET' &&
        url.pathname === '/v1/m/accessmap/graph' &&
        url.searchParams.get('limit') === '500'
      )
    })
    await page.getByRole('link', { name: /access map/i }).click()
    await page.waitForURL('**/access-map')
    const initialResponse = await initialGraphResponse
    expect(initialResponse.status()).toBe(200)
    let graph = (await initialResponse.json()) as LiveAccessGraph
    // React Flow draws nodes from the live /v1/m/accessmap/graph response.
    await expect(page.locator('.react-flow__node').first()).toBeAttached({
      timeout: 15_000,
    })
    await expectFlowMatches(page, graph.edges)
    await page.screenshot({ path: 'playwright-report/demo-access-map.png' })

    // Refresh is a new real read, not an animation-only click or a cache hit.
    let refreshGets = 0
    const countRefresh = (request: { method(): string; url(): string }) => {
      const url = new URL(request.url())
      if (
        request.method() === 'GET' &&
        url.pathname === '/v1/m/accessmap/graph'
      ) {
        refreshGets += 1
      }
    }
    page.on('request', countRefresh)
    const refreshed = page.waitForResponse((response) => {
      const url = new URL(response.url())
      return (
        response.request().method() === 'GET' &&
        url.pathname === '/v1/m/accessmap/graph'
      )
    })
    await page.getByRole('button', { name: 'Refresh', exact: true }).click()
    const refreshedResponse = await refreshed
    expect(refreshedResponse.status()).toBe(200)
    graph = (await refreshedResponse.json()) as LiveAccessGraph
    await expectOneRequestAfterQuietPeriod(page, () => refreshGets)
    await expectFlowMatches(page, graph.edges)
    page.off('request', countRefresh)

    const search = page.getByLabel('Search origin, resource, tool…')
    const modeGroup = page.getByRole('group', { name: 'Access mode' })
    const read = modeGroup.getByRole('button', { name: 'Read', exact: true })
    const write = modeGroup.getByRole('button', {
      name: 'Write',
      exact: true,
    })
    const attributed = page.getByRole('button', {
      name: 'Only attributed',
      exact: true,
    })
    const signal = page.getByRole('combobox', { name: 'Signal' })

    await search.fill('appdb.public.secrets')
    await expectFlowMatches(
      page,
      graph.edges.filter((edge) => textMatches(edge, 'appdb.public.secrets')),
    )
    await page.getByRole('button', { name: 'Clear', exact: true }).click()

    await read.click()
    await expectFlowMatches(
      page,
      graph.edges.filter((edge) => edge.mode === 'read'),
    )
    await read.click()

    await write.click()
    await expectFlowMatches(
      page,
      graph.edges.filter(
        (edge) => edge.mode === 'write' || edge.mode === 'readwrite',
      ),
    )
    await write.click()

    await attributed.click()
    await expectFlowMatches(
      page,
      graph.edges.filter((edge) => edge.confidence === 'attributed'),
    )
    await attributed.click()

    await signal.click()
    await page.getByRole('option', { name: 'pg_audit', exact: true }).click()
    await expectFlowMatches(
      page,
      graph.edges.filter((edge) => edge.signal_source === 'pg_audit'),
    )
    await signal.click()
    await page.getByRole('option', { name: 'All signals', exact: true }).click()

    // Clear resets a genuinely combined filter, including the portal-backed select.
    await search.fill('appdb')
    await read.click()
    await attributed.click()
    await signal.click()
    await page.getByRole('option', { name: 'pg_audit', exact: true }).click()
    await expectFlowMatches(
      page,
      graph.edges.filter(
        (edge) =>
          edge.mode === 'read' &&
          edge.confidence === 'attributed' &&
          edge.signal_source === 'pg_audit' &&
          textMatches(edge, 'appdb'),
      ),
    )
    await page.getByRole('button', { name: 'Clear', exact: true }).click()
    await expect(search).toHaveValue('')
    await expect(read).toHaveAttribute('aria-pressed', 'false')
    await expect(attributed).toHaveAttribute('aria-pressed', 'false')
    await expect(signal).toContainText('All signals')
    await expectFlowMatches(page, graph.edges)

    // The PERMITTED-vs-OBSERVED drift overlay reveals the seeded unexpected access.
    const driftResponse = page.waitForResponse((response) => {
      const url = new URL(response.url())
      return (
        response.request().method() === 'GET' &&
        url.pathname.endsWith('/v1/m/accessmap/drift')
      )
    })
    await page.getByRole('button', { name: /permitted vs observed/i }).click()
    const response = await driftResponse
    expect(
      response.status(),
      'the live drift handler must answer the click',
    ).toBe(200)
    const payload = (await response.json()) as LiveDrift
    expect(payload.unexpected_count).toBe(payload.unexpected_accesses.length)
    expect(payload.unused_count).toBe(payload.unused_grants.length)
    const findingCount =
      payload.unexpected_accesses.length + payload.unused_grants.length
    expect(
      findingCount,
      'the demo seed must expose at least one drift row for this functional check',
    ).toBeGreaterThan(0)
    await expect(page.getByRole('heading', { name: /drift/i })).toBeVisible()
    await expect(page.locator('aside').getByRole('listitem')).toHaveCount(
      findingCount,
    )
    await page.screenshot({
      path: 'playwright-report/demo-access-map-drift.png',
    })
  })

  test('drift finding opens, re-checks and routes to both real owners', async ({
    page,
  }) => {
    const token = await loginDemo(page)
    await waitForDemoAccessEstate(page, token)

    const openPoolFinding = async (): Promise<LiveAccessEdge> => {
      const graphRead = page.waitForResponse((response) => {
        const url = new URL(response.url())
        return (
          response.request().method() === 'GET' &&
          url.pathname === '/v1/m/accessmap/graph'
        )
      })
      await page.goto('/access-map')
      expect((await graphRead).status()).toBe(200)
      await expect(page.locator('.react-flow__node').first()).toBeVisible({
        timeout: 15_000,
      })

      const driftRead = page.waitForResponse((response) => {
        const url = new URL(response.url())
        return (
          response.request().method() === 'GET' &&
          url.pathname === '/v1/m/accessmap/drift'
        )
      })
      await page.getByRole('button', { name: /permitted vs observed/i }).click()
      const response = await driftRead
      expect(response.status()).toBe(200)
      const drift = (await response.json()) as LiveDrift
      const witness = drift.unexpected_accesses.find(
        ({ edge, reconciliation_pending: pending }) =>
          edge.origin_ref === 'svc_pool' &&
          edge.resource_ref === 'appdb.public.logs' &&
          !pending,
      )
      expect(
        witness,
        'the demo seed must retain the svc_pool → logs firm drift witness',
      ).toBeDefined()
      const edge = witness!.edge
      const row = page
        .locator('aside li button')
        .filter({ hasText: edge.origin_ref ?? '' })
        .filter({ hasText: edge.resource_ref ?? '' })
      await expect(row).toHaveCount(1)
      await row.click()
      const dialog = page.getByRole('dialog')
      await expect(
        dialog.getByRole('heading', { name: 'Access edge' }),
      ).toBeVisible()
      await expect(dialog).toContainText(edge.origin_ref ?? '')
      await expect(dialog).toContainText(edge.resource_ref ?? '')
      return edge
    }

    const edge = await openPoolFinding()
    const dialog = page.getByRole('dialog')

    // Closing must clear the selected edge, not merely hide its content visually.
    await dialog.getByRole('button', { name: 'Close' }).click()
    await expect(dialog).toHaveCount(0)
    const row = page
      .locator('aside li button')
      .filter({ hasText: edge.origin_ref ?? '' })
      .filter({ hasText: edge.resource_ref ?? '' })
    await row.click()

    let recheckGets = 0
    const countRecheck = (request: { method(): string; url(): string }) => {
      const url = new URL(request.url())
      if (
        request.method() === 'GET' &&
        url.pathname === '/v1/m/accessmap/drift'
      ) {
        recheckGets += 1
      }
    }
    page.on('request', countRecheck)
    const rechecked = page.waitForResponse((response) => {
      const url = new URL(response.url())
      return (
        response.request().method() === 'GET' &&
        url.pathname === '/v1/m/accessmap/drift'
      )
    })
    await page
      .getByRole('dialog')
      .getByRole('button', { name: 'Re-check the drift' })
      .click()
    const recheckedResponse = await rechecked
    expect(recheckedResponse.status()).toBe(200)
    const recheckedDrift = (await recheckedResponse.json()) as LiveDrift
    expect(
      recheckedDrift.unexpected_accesses.some(
        ({ edge: candidate }) => candidate.id === edge.id,
      ),
      'the fresh drift DTO must still contain the edge named by the rendered verdict',
    ).toBe(true)
    await expectOneRequestAfterQuietPeriod(page, () => recheckGets)
    page.off('request', countRecheck)
    await expect(page.getByRole('dialog').getByRole('status')).toHaveText(
      'Re-checked: this edge is still in the drift set.',
    )

    await page
      .getByRole('dialog')
      .getByRole('button', { name: 'Open source bindings' })
      .click()
    await page.waitForURL('**/console?tab=bindings')
    await expect(
      page.getByRole('tab', { name: 'Source bindings' }),
    ).toHaveAttribute('aria-selected', 'true')
    await expect(
      page.getByRole('heading', { name: 'Source bindings' }),
    ).toBeVisible()

    await openPoolFinding()
    const identitiesRead = page.waitForResponse((response) => {
      const url = new URL(response.url())
      return (
        response.request().method() === 'GET' &&
        url.pathname === '/v1/m/governance/identities'
      )
    })
    await page
      .getByRole('dialog')
      .getByRole('button', { name: 'Open the NHI roster' })
      .click()
    await page.waitForURL('**/identity?tab=inventory')
    const identitiesResponse = await identitiesRead
    expect(identitiesResponse.status()).toBe(200)
    const identities = (await identitiesResponse.json()) as {
      items: Array<{ ref: string }>
    }
    expect(identities.items.some((item) => item.ref === 'svc_pool')).toBe(true)
    await expect(
      page.getByRole('tab', { name: 'Identity inventory' }),
    ).toHaveAttribute('aria-selected', 'true')
    await expect(
      page.getByRole('grid', { name: 'Non-human and human identities' }),
    ).toContainText('svc_pool')
  })

  test('executive dashboard mounts the spend section', async ({ page }) => {
    await loginDemo(page)

    await page.goto('/dashboards')
    // This check covers the route and section mount; KPI semantics live elsewhere.
    await expect(page.getByText(/spend trend/i)).toBeVisible({
      timeout: 15_000,
    })
    await page.screenshot({ path: 'playwright-report/demo-dashboard.png' })
  })

  test('resource attack-path analysis sends resource_id to the live exfil handler', async ({
    page,
  }) => {
    const token = await loginDemo(page)
    await waitForDemoAccessEstate(page, token)

    const graphRead = page.waitForResponse((response) => {
      const url = new URL(response.url())
      return (
        response.request().method() === 'GET' &&
        url.pathname === '/v1/m/accessmap/graph'
      )
    })
    await page.goto('/access-map')
    const graphResponse = await graphRead
    expect(graphResponse.status()).toBe(200)
    const graph = (await graphResponse.json()) as LiveAccessGraph
    const resourceNode = graph.nodes.find(
      (node) => node.ref === 'appdb.public.secrets',
    )
    expect(
      resourceNode,
      'the demo graph must expose the sensitive resource selected by this test',
    ).toBeDefined()
    const sensitiveResource = page
      .locator('.react-flow__node')
      .filter({ hasText: 'appdb.public.secrets' })
    await expect(sensitiveResource).toBeVisible({ timeout: 15_000 })

    let exfilGets = 0
    const countExfil = (request: { method(): string; url(): string }) => {
      const url = new URL(request.url())
      if (
        request.method() === 'GET' &&
        url.pathname === '/v1/m/accessmap/attack-paths/exfil'
      ) {
        exfilGets += 1
      }
    }
    page.on('request', countExfil)
    await sensitiveResource.click()
    await expect(
      page.getByRole('button', { name: /analyse attack paths/i }),
    ).toBeVisible()
    await page.waitForTimeout(500)
    expect(
      exfilGets,
      'selecting a resource must not auto-run the self-audited exfil analysis',
    ).toBe(0)
    const exfilResponse = page.waitForResponse((response) => {
      const url = new URL(response.url())
      return (
        response.request().method() === 'GET' &&
        url.pathname === '/v1/m/accessmap/attack-paths/exfil'
      )
    })
    await page.getByRole('button', { name: /analyse attack paths/i }).click()
    const response = await exfilResponse
    const requestUrl = new URL(response.url())

    expect(
      requestUrl.searchParams.get('resource_id'),
      'exfil must send resource_id; the real handler rejects agent_id with 400',
    ).toBe(resourceNode!.id)
    expect(
      requestUrl.searchParams.has('agent_id'),
      'exfil must not send the agent-only query parameter',
    ).toBe(false)
    expect(
      response.status(),
      'the live exfil handler must accept the resource-rooted request',
    ).toBe(200)
    await expectOneRequestAfterQuietPeriod(page, () => exfilGets)
    page.off('request', countExfil)
    const payload = (await response.json()) as { paths?: unknown[] }
    expect(
      Array.isArray(payload.paths),
      'the live exfil response must carry a paths array; missing is unreadable, not empty',
    ).toBe(true)
    const paths = payload.paths!
    const exfilSection = page.locator('section').filter({
      has: page.getByRole('heading', { name: /exfiltration/i }),
    })
    await expect(exfilSection).toBeVisible()
    if (paths.length === 0) {
      await expect(exfilSection.getByText('No paths found.')).toBeVisible()
    } else {
      await expect(exfilSection.locator(':scope > ul > li')).toHaveCount(
        paths.length,
      )
    }
    await page.screenshot({
      path: 'playwright-report/demo-access-map-exfil.png',
    })
  })
})
