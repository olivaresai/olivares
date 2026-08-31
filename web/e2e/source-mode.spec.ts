// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { expect, test, type Page } from '@playwright/test'

const TOKEN = 'olvs_source_mode_e2e'

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

const kb = {
  id: 'k1',
  name: 'Product docs',
  classification: 'internal',
  residency_region: 'eu',
  embed_policy: 'local_only',
  embed_model: 'local-hash',
  dim: 256,
  default_acl: ['group:engineering'],
  status: 'active',
  doc_count: 2,
  chunk_count: 4,
}

async function seed(page: Page) {
  await page.addInitScript(
    ({ token }) => {
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
    },
    { token: TOKEN },
  )

  await page.route('**/v1/**', async (route) => {
    const url = new URL(route.request().url())
    const path = url.pathname
    let body: unknown = { items: [], has_more: false }

    if (path.endsWith('/v1/server-info')) body = serverInfo
    else if (path.endsWith('/v1/auth/whoami')) body = whoami
    else if (path.endsWith('/v1/system/orgs')) {
      body = {
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
      }
    } else if (path.endsWith('/v1/console/connectors')) {
      body = {
        connectors: [
          {
            kind: 'gdrive',
            title: 'Google Drive',
            transport: 'in_process',
            fields_known: true,
            fields: [],
          },
        ],
      }
    } else if (path.endsWith('/v1/console/sources')) {
      body = {
        sources: [
          {
            name: 'drive-export',
            kind: 'gdrive',
            tenant: 'acme',
            enabled: true,
            status: 'running',
            config: {},
          },
          {
            name: 'wiki-live',
            kind: 'confluence',
            tenant: 'acme',
            enabled: true,
            status: 'running',
            source_mode: 'live',
            config: { mode: 'live' },
          },
        ],
      }
    } else if (path.endsWith('/v1/m/knowledge/kbs')) {
      body = { items: [kb], has_more: false }
    } else if (path.endsWith('/v1/m/knowledge/kbs/k1')) {
      body = kb
    } else if (path.endsWith('/v1/m/knowledge/kbs/k1/documents')) {
      body = {
        items: [
          {
            id: 'doc-export',
            kb_ref: 'k1',
            source_kind: 'gdrive',
            source_ref: 'gdrive',
            source_doc_id: 'gd1',
            title: 'Export Runbook',
            content_type: 'text/plain',
            classification: 'internal',
            residency_region: 'eu',
            acl: [],
            content_hash: 'feedfacefeedface',
            redaction_count: 0,
            chunk_count: 2,
            status: 'indexed',
          },
          {
            id: 'doc-live',
            kb_ref: 'k1',
            source_kind: 'confluence',
            source_ref: 'confluence',
            source_mode: 'live',
            source_doc_id: 'cf1',
            title: 'Live Runbook',
            content_type: 'text/plain',
            classification: 'internal',
            residency_region: 'eu',
            acl: [],
            content_hash: 'deadbeefdeadbeef',
            redaction_count: 0,
            chunk_count: 2,
            status: 'indexed',
          },
        ],
        has_more: false,
      }
    } else if (path.endsWith('/v1/m/knowledge/kbs/k1/query')) {
      body = {
        lineage_id: 'ln1',
        results: [
          {
            chunk_id: 'ch-export',
            document_id: 'doc-export',
            source_kind: 'gdrive',
            source_ref: 'gdrive',
            title: 'Export Runbook',
            text: 'exported content',
            classification: 'internal',
            score: 0.91,
          },
          {
            chunk_id: 'ch-live',
            document_id: 'doc-live',
            source_kind: 'confluence',
            source_ref: 'confluence',
            source_mode: 'live',
            title: 'Live Runbook',
            text: 'live content',
            classification: 'internal',
            score: 0.88,
          },
        ],
        count: 2,
        embed_model: 'local-hash',
        egress: false,
      }
    }

    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(body),
    })
  })
}

test('source mode badges render and filter in connectors and retrieval', async ({
  page,
}) => {
  await seed(page)

  await page.goto('/console')
  await page.getByRole('tab', { name: 'Connectors', exact: true }).click()
  await expect(page.getByText('drive-export')).toBeVisible()
  await expect(page.getByText('wiki-live')).toBeVisible()
  await expect(
    page.locator('[data-source-mode="export"]').first(),
  ).toBeVisible()
  await expect(page.locator('[data-source-mode="live"]').first()).toBeVisible()

  await page.getByRole('combobox', { name: /mode filter/i }).click()
  await page.getByRole('option', { name: 'Live' }).click()
  await expect(page.getByText('wiki-live')).toBeVisible()
  await expect(page.getByText('drive-export')).toBeHidden()

  await page.goto('/knowledge')
  await page.getByText('Product docs').click()
  await expect(page.getByText('Export Runbook')).toBeVisible()
  await expect(page.getByText('Live Runbook')).toBeVisible()
  await page.getByRole('button', { name: /run retrieval/i }).click()
  const queryDialog = page.getByRole('dialog').filter({
    hasText: 'Governed retrieval',
  })
  await queryDialog.locator('#q-query').fill('runbook')
  await queryDialog.getByRole('button', { name: /^run retrieval$/i }).click()
  await expect(queryDialog.getByText('Export Runbook')).toBeVisible()
  await expect(queryDialog.getByText('Live Runbook')).toBeVisible()
  await expect(
    queryDialog.locator('[data-source-mode="export"]').first(),
  ).toBeVisible()
  await expect(
    queryDialog.locator('[data-source-mode="live"]').first(),
  ).toBeVisible()

  await queryDialog.getByRole('combobox', { name: /^mode$/i }).click()
  await page.getByRole('option', { name: 'Live' }).click()
  await expect(queryDialog.getByText('Live Runbook')).toBeVisible()
  await expect(queryDialog.getByText('Export Runbook')).toBeHidden()
})
