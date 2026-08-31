// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { expect, test, type Page } from '@playwright/test'

/**
 * Visual e2e for the management views (capabilities, catalog, permissions,
 * deploy, knowledge), in light AND dark. This is HERMETIC: it seeds a session in
 * localStorage and intercepts every /v1 call with fixtures, so it renders the real
 * views with representative data WITHOUT a backend or a login round-trip. Run it via
 * scripts/web-visual-e2e.sh (boots `vite dev` and points PLAYWRIGHT_BASE_URL at it).
 *
 * It captures a screenshot per view per theme into playwright-report/ — the visual
 * record that all four management areas render correctly in both schemes.
 */

const TOKEN = 'olvs_e2e_token'

const whoami = {
  kind: 'user',
  user_id: 'u1',
  actor: 'admin@olivares.ai',
  display_name: 'Admin',
  superadmin: true,
  grants: [{ tenant: 't1', role: 'owner' }],
}

const serverInfo = {
  version: '0.1.0-e2e',
  engine: 'olivares',
  setup_required: false,
  license: { status: 'community', licensee: '' },
}

// One representative row per default tab (fields mirror each module's list DTO).
const FIXTURES: Record<string, unknown> = {
  // The shell's org switcher (superadmin) calls this — must be a valid envelope.
  '/v1/system/orgs': {
    items: [
      {
        id: 'o1',
        tenant_id: 't1',
        name: 'Acme Corp',
        slug: 'acme',
        status: 'active',
        created_at: '2026-01-01T00:00:00Z',
      },
    ],
    has_more: false,
  },
  '/v1/m/capabilities/servers': {
    items: [
      {
        id: 's1',
        name: 'github',
        transport: 'stdio',
        version: '1.4.0',
        status: 'active',
        connection: 'connected',
        tool_count: 5,
        has_config: true,
        config_revision: 3,
      },
    ],
    has_more: false,
  },
  '/v1/m/catalog/entries': {
    items: [
      {
        id: 'e1',
        kind: 'mcp_server',
        name: 'GitHub MCP',
        slug: 'github-mcp',
        version: '1.2.0',
        status: 'approved',
        summary: 'Curated GitHub MCP server',
        owner_ref: 'team-platform',
        signed: true,
        sig_alg: 'ed25519',
        signed_by: 'a1b2c3d4e5f6a7b8',
        content_hash: 'deadbeefcafef00d',
      },
    ],
    has_more: false,
  },
  '/v1/m/governance/approvals': {
    items: [
      {
        id: 'a1',
        subject_kind: 'deployment',
        subject_ref: 'support-bot',
        action: 'deploy.apply',
        requested_by: 'user:abc123',
        status: 'pending',
        required_approvals: 1,
        approve_count: 0,
        reject_count: 0,
        reason: 'Ship the v2 support bot to production',
        escalated: false,
        expires_at: '2999-01-01T00:00:00Z',
      },
    ],
    has_more: false,
  },
  '/v1/m/deploy/definitions': {
    items: [
      {
        id: 'd1',
        subject_kind: 'agent',
        subject_ref: 'support-bot',
        name: 'support-bot',
        environment: 'production',
        target: 'k8s',
        runtime: 'container',
        desired_status: 'running',
        current_version: 4,
        applied_version: 4,
        up_to_date: true,
        spec_hash: 'abc123def456',
        source_ref: 'git:infra#a1b2c3',
      },
    ],
    has_more: false,
  },
  '/v1/m/knowledge/kbs': {
    items: [
      {
        id: 'k1',
        name: 'Product docs',
        classification: 'internal',
        residency_region: 'eu',
        embed_policy: 'managed',
        embed_model: 'text-embedding-3-large',
        dim: 1536,
        default_acl: ['group:engineering'],
        status: 'ready',
        doc_count: 12,
        chunk_count: 340,
      },
    ],
    has_more: false,
  },
}

const VIEWS: {
  id: string
  path: string
}[] = [
  { id: 'capabilities', path: '/capabilities' },
  { id: 'catalog', path: '/catalog' },
  { id: 'permissions', path: '/permissions' },
  { id: 'deploy', path: '/deploy' },
  { id: 'knowledge', path: '/knowledge' },
]

async function seed(page: Page, theme: 'light' | 'dark') {
  await page.addInitScript(
    ({ token, theme }) => {
      localStorage.setItem(
        'olivares.session',
        JSON.stringify({
          state: {
            token,
            sessionId: 's-e2e',
            expiresAt: '2999-01-01T00:00:00Z',
          },
          version: 0,
        }),
      )
      localStorage.setItem(
        'olivares.tenant',
        JSON.stringify({ state: { activeTenant: 't1' }, version: 0 }),
      )
      // theme store reads this RAW (no JSON) before first paint.
      localStorage.setItem('olivares.theme', theme)
    },
    { token: TOKEN, theme },
  )

  await page.route('**/v1/**', async (route) => {
    const url = new URL(route.request().url())
    const path = url.pathname
    let body: unknown
    if (path.endsWith('/v1/server-info')) body = serverInfo
    else if (path.endsWith('/v1/auth/whoami')) body = whoami
    else {
      const hit = Object.keys(FIXTURES).find((p) => path.includes(p))
      // Default to a valid empty list envelope so any unmocked list endpoint the
      // shell or a view calls degrades to an empty state, never an undefined.map.
      body = hit ? FIXTURES[hit] : { items: [], has_more: false }
    }
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(body),
    })
  })
}

for (const theme of ['light', 'dark'] as const) {
  test(`Management views render — ${theme}`, async ({ page }) => {
    await seed(page, theme)
    for (const view of VIEWS) {
      await page.goto(view.path)
      // The authenticated shell + a view heading prove the route rendered (not the
      // Suspense spinner / login redirect).
      await expect(page.getByRole('heading', { level: 1 }).first()).toBeVisible(
        {
          timeout: 15_000,
        },
      )
      await expect(page.locator('html')).toHaveClass(
        theme === 'dark' ? /dark/ : /^(?!.*dark).*$/,
      )
      await page.waitForLoadState('networkidle')
      await page.screenshot({
        path: `playwright-report/mgmt-${view.id}-${theme}.png`,
        fullPage: true,
      })
    }
  })
}
