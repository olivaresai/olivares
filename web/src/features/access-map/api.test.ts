// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { beforeEach, describe, expect, it, vi } from 'vitest'

const { http } = vi.hoisted(() => ({
  http: { get: vi.fn() },
}))

vi.mock('@/lib/api/client', () => ({ http }))

const { accessMapApi } = await import('./api')

describe('access-map attack-path transport contract', () => {
  beforeEach(() => http.get.mockReset())

  it('exfil sends resource_id because the real handler refuses agent_id', async () => {
    http.get.mockResolvedValue({ paths: [] })

    await accessMapApi.attackExfil('resource-7')

    expect(
      http.get,
      'EXFIL_QUERY_CONTRACT: /attack-paths/exfil requires resource_id=resource-7 and must not send agent_id',
    ).toHaveBeenCalledWith('/v1/m/accessmap/attack-paths/exfil', {
      query: { resource_id: 'resource-7' },
    })
  })
})
