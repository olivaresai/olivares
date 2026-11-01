// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import { originLinks } from './origin-links'

describe('originLinks', () => {
  it('opens a session at its address on the work surface', () => {
    expect(originLinks('session', 'abc-123')).toEqual([
      {
        key: 'session',
        to: '/sessions',
        search: { session: 'sess:abc-123' },
        permission: 'sessions:live:read',
      },
    ])
  })

  it('offers no session link without an exact ref, or for an address the surface would refuse', () => {
    expect(originLinks('session', undefined)).toEqual([])
    expect(originLinks('session', '')).toEqual([])
    expect(originLinks('session', 'x'.repeat(300))).toEqual([])
  })

  it('opens the list that holds an agent or an identity', () => {
    expect(originLinks('agent', undefined)).toMatchObject([
      { to: '/console', search: { tab: 'agents' }, permission: 'tenant:admin' },
    ])
    expect(originLinks('identity', 'svc-1')).toMatchObject([
      {
        key: 'nhiRoster',
        to: '/identity',
        search: { tab: 'inventory' },
        permission: 'governance:identity:read',
      },
    ])
  })

  it('leads nowhere for a resource kind', () => {
    expect(originLinks('postgres.table', 'appdb.public.secrets')).toEqual([])
  })
})
