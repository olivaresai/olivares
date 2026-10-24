// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Transport contract of the reach view: it reads the engine's EXISTING endpoints
// (access-map graph, agents list, live sessions, pending approval queue) — issue #2553 forbids a
// new mechanism, so these paths are the whole surface. Both list reads carry a
// ceiling (the registry's list-ceiling ratchet) and the view labels every page cut.
import { beforeEach, describe, expect, it, vi } from 'vitest'

const { http } = vi.hoisted(() => ({
  http: { get: vi.fn() },
}))

vi.mock('@/lib/api/client', () => ({ http }))

const { reachApi, reachKeys } = await import('./api')
const { sessionsKeys } = await import('@/features/sessions/api')

const LIST_CEILING = 100

describe('reach transport contract', () => {
  beforeEach(() => http.get.mockReset())

  it('reads the access-map graph with no query (the engine defaults the page)', async () => {
    http.get.mockResolvedValue({ edges: [], has_more: false })
    await reachApi.graph()
    expect(http.get).toHaveBeenCalledWith('/v1/m/accessmap/graph')
  })

  it('reads the agents list with its ceiling', async () => {
    http.get.mockResolvedValue({ items: [] })
    await reachApi.agents()
    expect(http.get).toHaveBeenCalledWith('/v1/agents', {
      query: { limit: LIST_CEILING },
    })
  })

  it('reads the live-sessions page the session names read (one page, shared key)', async () => {
    http.get.mockResolvedValue({ items: [] })
    await reachApi.sessions()
    expect(http.get).toHaveBeenCalledWith(
      '/v1/m/sessions/live',
      expect.objectContaining({ query: { limit: 200 } }),
    )
    expect(reachKeys.sessions('t1')).toEqual(
      sessionsKeys.live('t1', { limit: 200 }),
    )
  })

  it('reads only PENDING approvals, with its ceiling (a decided request must never dash a destination)', async () => {
    http.get.mockResolvedValue({ items: [] })
    await reachApi.pendingApprovals()
    expect(http.get).toHaveBeenCalledWith('/v1/m/governance/approvals', {
      query: { status: 'pending', limit: LIST_CEILING },
    })
  })
})
