// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { beforeEach, describe, expect, it, vi } from 'vitest'

const http = vi.hoisted(() => ({
  get: vi.fn(),
  put: vi.fn(),
  post: vi.fn(),
  del: vi.fn(),
}))
vi.mock('@/lib/api/client', () => ({ http }))

import { catalogApi, catalogKeys } from './api'

beforeEach(() => vi.clearAllMocks())

/** The engine registers TWO route pairs with TWO handlers (`modules/catalog/api.go:58,67`).
 *  These cells exist because the paths are spelled out literally rather than composed from
 *  the kind: a template would still work at runtime, and nothing here would notice the day
 *  a kind stopped matching its route. */
describe('admission policy routes — one literal path per kind', () => {
  it('reads the MCP policy from its own route', async () => {
    await catalogApi.admissionPolicy('mcp')
    expect(http.get).toHaveBeenCalledWith('/v1/m/catalog/mcp-admission/policy')
  })

  it('reads the connector policy from its own route', async () => {
    await catalogApi.admissionPolicy('connector')
    expect(http.get).toHaveBeenCalledWith(
      '/v1/m/catalog/connector-admission/policy',
    )
  })

  it('writes each policy to the matching route', async () => {
    const body = { require_signed: false, require_subject_digest: false }
    await catalogApi.putAdmissionPolicy('mcp', body)
    expect(http.put).toHaveBeenCalledWith(
      '/v1/m/catalog/mcp-admission/policy',
      body,
    )
    await catalogApi.putAdmissionPolicy('connector', body)
    expect(http.put).toHaveBeenCalledWith(
      '/v1/m/catalog/connector-admission/policy',
      body,
    )
  })

  // Shared cache keys would make the second card render the FIRST kind's policy — an
  // operator would read connector trust anchors that are really the MCP ones.
  it('caches the two policies under distinct keys', () => {
    expect(catalogKeys.admissionPolicy('t1', 'mcp')).not.toEqual(
      catalogKeys.admissionPolicy('t1', 'connector'),
    )
  })
})
