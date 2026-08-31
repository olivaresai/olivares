// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { beforeEach, describe, expect, it, vi } from 'vitest'

const http = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }))
vi.mock('@/lib/api/client', () => ({ http }))

import { notifyApi } from './api'

beforeEach(() => vi.clearAllMocks())

describe('notify predicate API wrappers', () => {
  it('loads one route with an encoded id before a full update', async () => {
    await notifyApi.getRoute('route/1')
    expect(http.get).toHaveBeenCalledWith('/v1/m/notify/routes/route%2F1')
  })

  it('loads the match type catalog', async () => {
    await notifyApi.listMatchTypes()
    expect(http.get).toHaveBeenCalledWith('/v1/m/notify/match-types')
  })

  it('posts a signal for predicate evaluation', async () => {
    const body = {
      event_type: 'finding.created',
      kind: 'drift',
      severity: 'high' as const,
      source: 'scanner',
      subject_kind: 'agent',
    }
    await notifyApi.evaluateRoutes(body)
    expect(http.post).toHaveBeenCalledWith('/v1/m/notify/routes/evaluate', body)
  })

  it('lists route revisions with an encoded id and keyset cursor', async () => {
    await notifyApi.routeRevisions('route/1', {
      cursor: 'rev-cursor',
      limit: 50,
    })
    expect(http.get).toHaveBeenCalledWith(
      '/v1/m/notify/routes/route%2F1/revisions',
      { query: { cursor: 'rev-cursor', limit: 50 } },
    )
  })

  it('posts the selected route revision for restore', async () => {
    await notifyApi.restoreRoute('route/1', 'rev-2')
    expect(http.post).toHaveBeenCalledWith(
      '/v1/m/notify/routes/route%2F1/restore',
      { revision_id: 'rev-2' },
    )
  })
})
