// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { beforeEach, describe, expect, it, vi } from 'vitest'

const http = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }))
vi.mock('@/lib/api/client', () => ({ http }))

import { eventingApi } from './api'
import { subscriptionSchema } from './eventing-view'

beforeEach(() => vi.clearAllMocks())

describe('eventing API wrappers', () => {
  it('posts a bounded replay request', async () => {
    await eventingApi.replayEvents('sub/1', { from_seq: 12, to_seq: 40 })
    expect(http.post).toHaveBeenCalledWith(
      '/v1/m/eventing/subscriptions/sub/1/replay',
      { from_seq: 12, to_seq: 40 },
    )
  })

  it('passes the replay origin to the delivery list', async () => {
    await eventingApi.deliveries({ origin: 'replay' })
    expect(http.get).toHaveBeenCalledWith('/v1/m/eventing/deliveries', {
      query: { origin: 'replay' },
    })
  })

  it('lists subscription revisions with the keyset cursor', async () => {
    await eventingApi.subscriptionRevisions('sub-1', {
      cursor: 'rev-cursor',
      limit: 50,
    })
    expect(http.get).toHaveBeenCalledWith(
      '/v1/m/eventing/subscriptions/sub-1/revisions',
      { query: { cursor: 'rev-cursor', limit: 50 } },
    )
  })

  it('posts the selected subscription revision for restore', async () => {
    await eventingApi.restoreSubscription('sub-1', 'rev-2')
    expect(http.post).toHaveBeenCalledWith(
      '/v1/m/eventing/subscriptions/sub-1/restore',
      { revision_id: 'rev-2' },
    )
  })
})

const valid = {
  name: 'hook',
  endpoint: 'https://example.com',
  event_types: ['audit.event'],
  match_sources: '',
  role: 'viewer',
  auth_type: 'none' as const,
  auth_value: '',
  auth_header_name: '',
  max_attempts: 0,
  initial_interval_seconds: 0,
  description: '',
  enabled: true,
  sink_kind: '',
  sink_format: '',
  sink_cred: '',
}

describe('subscriptionSchema retry bounds', () => {
  it('accepts the zero sentinels and inclusive configured ranges', () => {
    for (const max_attempts of [0, 1, 20])
      expect(
        subscriptionSchema.safeParse({ ...valid, max_attempts }).success,
      ).toBe(true)
    for (const initial_interval_seconds of [0, 5, 3600])
      expect(
        subscriptionSchema.safeParse({ ...valid, initial_interval_seconds })
          .success,
      ).toBe(true)
  })

  it('rejects values between or outside the backend ranges', () => {
    for (const max_attempts of [-1, 21])
      expect(
        subscriptionSchema.safeParse({ ...valid, max_attempts }).success,
      ).toBe(false)
    for (const initial_interval_seconds of [-1, 1, 4, 3601])
      expect(
        subscriptionSchema.safeParse({ ...valid, initial_interval_seconds })
          .success,
      ).toBe(false)
  })
})
