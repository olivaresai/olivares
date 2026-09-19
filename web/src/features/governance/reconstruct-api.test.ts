// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Wire-contract test for historical reconstruction: it mocks the LOW-LEVEL http
// client (not governanceApi) and asserts the exact HTTP method, path and body the
// console sends. A drift in verb, path or an accidental request body fails here.
import { afterEach, describe, expect, it, vi } from 'vitest'

const http = vi.hoisted(() => ({
  get: vi.fn(() => Promise.resolve({})),
  post: vi.fn(() => Promise.resolve({})),
  put: vi.fn(() => Promise.resolve({})),
  delete: vi.fn(() => Promise.resolve({})),
}))
vi.mock('@/lib/api', () => ({ http }))

import { governanceApi } from './api'

afterEach(() => {
  for (const fn of Object.values(http)) fn.mockClear()
})

describe('governanceApi decision reconstruction wire contract', () => {
  it('reconstruct → GET /decisions/{id}/reconstruct', async () => {
    await governanceApi.reconstructDecision('id-1')
    expect(http.get).toHaveBeenCalledWith(
      '/v1/m/governance/decisions/id-1/reconstruct',
    )
  })

  it('replay by decision id → POST /decisions/replay with that id', async () => {
    const body = { decision_id: 'id-1' }
    await governanceApi.replayDecision(body)
    expect(http.post).toHaveBeenCalledWith(
      '/v1/m/governance/decisions/replay',
      body,
    )
  })

  it('replay by question → POST /decisions/replay with at/principal/action', async () => {
    const body = {
      at: '2026-09-15T12:00:00Z',
      principal: 'agent-7',
      action: 'SELECT',
      resource: 'public.customers',
      resource_kind: 'postgres.table',
    }
    await governanceApi.replayDecision(body)
    expect(http.post).toHaveBeenCalledWith(
      '/v1/m/governance/decisions/replay',
      body,
    )
  })
})
