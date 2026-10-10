// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { configureApiClient } from '@/lib/api/client'
import { skillsApi } from './api'
import type { SkillAssignment } from './types'

interface Captured {
  url: URL
  method: string
  tenant: string | null
  ifMatch: string | null
}

let captured: Captured[] = []
let respond: (url: URL) => unknown = () => ({ items: [] })

beforeEach(() => {
  captured = []
  respond = () => ({ items: [] })
  configureApiClient({
    getToken: () => 'olvs_test',
    getTenant: () => 'tenant-1',
    onUnauthorized: () => {},
  })
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init: RequestInit = {}) => {
      const parsed = new URL(String(url), 'http://console.test')
      const headers = new Headers(init.headers)
      captured.push({
        url: parsed,
        method: init.method ?? 'GET',
        tenant: headers.get('X-Olivares-Tenant'),
        ifMatch: headers.get('If-Match'),
      })
      return new Response(JSON.stringify(respond(parsed)), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }),
  )
})

afterEach(() => vi.unstubAllGlobals())

const pin: SkillAssignment = {
  id: 'as/1',
  target_kind: 'agent',
  target_id: 'agent-1',
  pack_id: 'pack-1',
  pack_revision_id: 'rev-1',
  members: null,
  version: 7,
}

describe('skills api', () => {
  it('unassigns with the pin version in If-Match, in the tenant acted in', async () => {
    await skillsApi.unassign(pin, { tenant: 'tenant-2' })
    const [request] = captured
    expect(request.method).toBe('DELETE')
    expect(request.url.pathname).toBe('/v1/m/skills/assignments/as%2F1')
    expect(request.ifMatch).toBe('7')
    expect(request.tenant).toBe('tenant-2')
  })

  it('lists a pack’s uses under the pack', async () => {
    await skillsApi.packAssignments('pack/1')
    const [request] = captured
    expect(request.url.pathname).toBe('/v1/m/skills/packs/pack%2F1/assignments')
    expect(request.url.searchParams.has('target_kind')).toBe(false)
  })

  it('follows the revision cursor until the latest revision is read', async () => {
    respond = (url) =>
      url.searchParams.get('cursor') === 'c1'
        ? {
            pack: { id: 'pack-1' },
            revisions: [{ id: 'rev-2' }],
            has_more_revisions: false,
          }
        : {
            pack: { id: 'pack-1' },
            revisions: [{ id: 'rev-1' }],
            revisions_cursor: 'c1',
            has_more_revisions: true,
          }
    const detail = await skillsApi.pack('pack-1')
    expect(detail.revisions.map((r) => r.id)).toEqual(['rev-1', 'rev-2'])
    expect(detail.has_more_revisions).toBe(false)
    expect(captured).toHaveLength(2)
  })
})
