// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  consoleApi,
  type ModelAccessDTO,
  type ModelGroupDTO,
} from '@/features/console/api'
import { evalsApi } from '@/features/evals/api'
import { finopsApi } from '@/features/finops/api'
import { knowledgeApi } from '@/features/knowledge/api'
import { modelsApi } from '@/features/models/api'
import { reportingApi } from '@/features/reporting/api'
import { __resetRefreshState, configureApiClient } from '@/lib/api/client'

const tenantA = { tenant: 'tenant-A' } as const
const sentRequests: Array<{ url: string; tenant: string | null }> = []

const group: ModelGroupDTO = {
  id: 'group-1',
  name: 'governed',
  member_refs: ['model-1'],
  family_selectors: [],
  tier_selectors: [],
}

const access: ModelAccessDTO = {
  id: 'access-1',
  subject_kind: 'user',
  subject_ref: 'alice',
  target_kind: 'model',
  target_ref: 'model-1',
  surfaces: [],
}

beforeEach(() => {
  __resetRefreshState()
  configureApiClient({
    getToken: () => 'olvs_test',
    getTenant: () => 'tenant-B',
    onUnauthorized: () => {},
    refreshSession: undefined,
    getExpiresAt: undefined,
  })
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init?: RequestInit) => {
      sentRequests.push({
        url,
        tenant: new Headers(init?.headers).get('X-Olivares-Tenant'),
      })
      return new Response(JSON.stringify({ items: [], deleted: true }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
  configureApiClient({
    getToken: () => null,
    getTenant: () => null,
    onUnauthorized: () => {},
    refreshSession: undefined,
    getExpiresAt: undefined,
  })
  __resetRefreshState()
  sentRequests.length = 0
})

describe('the censused production surface forwards its captured tenant', () => {
  it('uses A for every scoped wrapper while the client fallback already points at B', async () => {
    await evalsApi.calibrationItems(undefined, tenantA)
    await evalsApi.runCalibration({}, tenantA)

    await finopsApi.costCenters(undefined, tenantA)
    await finopsApi.costCenterMappings('cost-center-1', tenantA)
    await finopsApi.outcomes(undefined, tenantA)
    await finopsApi.modelRates(undefined, tenantA)
    await finopsApi.seatUtilization({ provider: 'anthropic' }, tenantA)
    await finopsApi.statements(undefined, tenantA)
    await finopsApi.statement('statement-1', tenantA)
    await finopsApi.createCostCenter({}, tenantA)
    await finopsApi.updateCostCenter('cost-center-1', {}, tenantA)
    await finopsApi.deleteCostCenter('cost-center-1', tenantA)
    await finopsApi.createCostCenterMapping('cost-center-1', {}, tenantA)
    await finopsApi.deleteCostCenterMapping(
      'cost-center-1',
      'mapping-1',
      tenantA,
    )
    await finopsApi.ingestOutcome({}, tenantA)
    await finopsApi.generateStatements(
      { period: 'monthly', period_start: '2026-08-01T00:00:00Z' },
      tenantA,
    )

    await knowledgeApi.scans({}, tenantA)
    await knowledgeApi.scanSource('sharepoint', tenantA)
    await knowledgeApi.dlpRules(tenantA)
    await knowledgeApi.setDlpRules({}, tenantA)
    await knowledgeApi.deleteDlpRule('rule-1', tenantA)

    await consoleApi.listAgents({ limit: 1 }, tenantA)
    await modelsApi.models(tenantA)
    await modelsApi.modelGroups(tenantA)
    await modelsApi.modelAccess(tenantA)

    await consoleApi.listModelGroups(tenantA)
    await consoleApi.createModelGroup(group, tenantA)
    await consoleApi.updateModelGroup('group-1', group, tenantA)
    await consoleApi.deleteModelGroup('group-1', tenantA)
    await consoleApi.listModelAccess(tenantA)
    await consoleApi.createModelAccess(access, tenantA)
    await consoleApi.updateModelAccess('access-1', access, tenantA)
    await consoleApi.deleteModelAccess('access-1', tenantA)

    await reportingApi.listSchedules(tenantA)
    await reportingApi.scheduleRuns('schedule-1', tenantA)
    await reportingApi.createSchedule(
      {
        report_type: 'compliance-evidence',
        format: 'html',
        cron: '0 8 * * 1',
        enabled: true,
      },
      tenantA,
    )
    await reportingApi.deleteSchedule('schedule-1', tenantA)

    expect(sentRequests).toHaveLength(37)
    expect(sentRequests).toEqual(
      sentRequests.map(({ url }) => ({ url, tenant: 'tenant-A' })),
    )
  })
})
