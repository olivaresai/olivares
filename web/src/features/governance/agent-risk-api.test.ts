// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Wire-contract test for the agent-risk endpoints: it mocks the LOW-LEVEL http
// client (not governanceApi) and asserts the exact HTTP method, path and body the
// console sends — the fixed backend contract (governance.go:523-527). A drift in
// verb, path or an accidental request body fails here.
import { afterEach, describe, expect, it, vi } from 'vitest'

const http = vi.hoisted(() => ({
  get: vi.fn(() => Promise.resolve({})),
  post: vi.fn(() => Promise.resolve({})),
  put: vi.fn(() => Promise.resolve({})),
  delete: vi.fn(() => Promise.resolve({})),
}))
vi.mock('@/lib/api', () => ({ http }))

import { governanceApi } from './api'

const BASE = '/v1/m/governance/agent-risk-profiles'

afterEach(() => {
  for (const fn of Object.values(http)) fn.mockClear()
})

describe('governanceApi agent-risk wire contract', () => {
  it('list → GET base with cursor/limit query', async () => {
    await governanceApi.listAgentRiskProfiles({ cursor: 'c1', limit: 100 })
    expect(http.get).toHaveBeenCalledWith(BASE, {
      query: { cursor: 'c1', limit: 100 },
    })
  })

  it('get → GET base/{id}', async () => {
    await governanceApi.getAgentRiskProfile('id-1')
    expect(http.get).toHaveBeenCalledWith(`${BASE}/id-1`)
  })

  it('classify → POST base/classify with {agent_id}', async () => {
    await governanceApi.classifyAgentRisk({ agent_id: 'agent-uuid' })
    expect(http.post).toHaveBeenCalledWith(`${BASE}/classify`, {
      agent_id: 'agent-uuid',
    })
  })

  it('setTier → PUT base/{id}/tier with {tier}', async () => {
    await governanceApi.setAgentRiskTier('id-1', { tier: 'high' })
    expect(http.put).toHaveBeenCalledWith(`${BASE}/id-1/tier`, { tier: 'high' })
  })

  it('setTier clear → PUT base/{id}/tier with {tier:""}', async () => {
    await governanceApi.setAgentRiskTier('id-1', { tier: '' })
    expect(http.put).toHaveBeenCalledWith(`${BASE}/id-1/tier`, { tier: '' })
  })

  it('review → POST base/{id}/review with NO body (single argument)', async () => {
    await governanceApi.reviewAgentRisk('id-1')
    expect(http.post).toHaveBeenCalledWith(`${BASE}/id-1/review`)
    // The review endpoint takes no request body (agentrisk.go
    // handleReviewAgentRisk) — assert exactly one call argument, no body slipped in.
    expect(http.post.mock.calls[0]).toHaveLength(1)
  })
})
