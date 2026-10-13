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
  idempotencyKey: string | null
  body: unknown
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
        idempotencyKey: headers.get('Idempotency-Key'),
        body: typeof init.body === 'string' ? JSON.parse(init.body) : null,
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

it('reads every page in the captured tenant and recognizes only the exact workspace pin', async () => {
  respond = (url) =>
    url.searchParams.get('cursor') === 'next'
      ? {
          items: [
            {
              ...pin,
              target_kind: 'workspace',
              target_id: 'ws-2',
              pack_id: 'pack-1',
              pack_revision_id: 'rev-2',
              members: ['deploy', 'review'],
            },
          ],
          has_more: false,
        }
      : {
          items: [
            {
              ...pin,
              target_kind: 'workspace',
              target_id: 'other-workspace',
              pack_id: 'pack-1',
              pack_revision_id: 'rev-2',
              members: ['deploy', 'review'],
            },
          ],
          has_more: true,
          cursor: 'next',
        }
  expect(
    await skillsApi.hasWorkspacePin(
      'ws-2',
      'pack-1',
      'rev-2',
      ['review', 'deploy'],
      {
        tenant: 'tenant-2',
      },
    ),
  ).toBe(true)
  expect(captured).toHaveLength(2)
  for (const request of captured) {
    expect(request.tenant).toBe('tenant-2')
    expect(request.url.searchParams.get('target_kind')).toBe('workspace')
    expect(request.url.searchParams.get('target_id')).toBe('ws-2')
  }
})

it('does not treat another revision or a restricted member selection as the requested full pin', async () => {
  respond = () => ({
    items: [
      {
        ...pin,
        target_kind: 'workspace',
        target_id: 'ws-2',
        pack_id: 'pack-1',
        pack_revision_id: 'rev-1',
        members: ['deploy', 'review'],
      },
      {
        ...pin,
        target_kind: 'workspace',
        target_id: 'ws-2',
        pack_id: 'pack-1',
        pack_revision_id: 'rev-2',
        members: ['review'],
      },
    ],
    has_more: false,
  })
  expect(
    await skillsApi.hasWorkspacePin(
      'ws-2',
      'pack-1',
      'rev-2',
      ['review', 'deploy'],
      {
        tenant: 'tenant-2',
      },
    ),
  ).toBe(false)
})

it('checks the captured authority before dispatching another assignment page', async () => {
  let current = true
  respond = () => {
    current = false
    return { items: [], has_more: true, cursor: 'next' }
  }
  await expect(
    skillsApi.hasWorkspacePin('ws-2', 'pack-1', 'rev-2', ['review', 'deploy'], {
      tenant: 'tenant-2',
      dispatchGuard: () => {
        if (!current) throw new Error('Retired authority')
      },
    }),
  ).rejects.toThrow('Retired authority')
  expect(captured).toHaveLength(1)
})

it('moves a pin with its version, its selected skills and the approval only when given', async () => {
  const selected: SkillAssignment = { ...pin, members: ['review', 'deploy'] }
  await skillsApi.repin(selected, 'rev-9', true, { tenant: 'tenant-2' })
  await skillsApi.repin(pin, 'rev-9', false, { tenant: 'tenant-2' })
  expect(captured).toHaveLength(2)
  expect(captured[0]).toMatchObject({
    method: 'PUT',
    ifMatch: '7',
    tenant: 'tenant-2',
    body: {
      target_kind: 'agent',
      target_id: 'agent-1',
      pack_revision_id: 'rev-9',
      members: ['review', 'deploy'],
      approve_active_content: true,
    },
  })
  expect(captured[0].url.pathname).toBe('/v1/m/skills/assignments/as%2F1')
  // No selection and no approval are sent as such: the engine reads omitted as all.
  expect(captured[1].body).toEqual({
    target_kind: 'agent',
    target_id: 'agent-1',
    pack_revision_id: 'rev-9',
  })
})

it('checks an update without applying and applies only with the reviewed digest and a retry key', async () => {
  await skillsApi.updateCheck('pack/1', { tenant: 'tenant-2' })
  await skillsApi.updateApply('pack/1', 'abc123', 'key-1', {
    tenant: 'tenant-2',
  })
  expect(captured[0]).toMatchObject({ method: 'POST', body: { apply: false } })
  expect(captured[0].idempotencyKey).toBeNull()
  expect(captured[0].url.pathname).toBe('/v1/m/skills/packs/pack%2F1/update')
  expect(captured[1]).toMatchObject({
    method: 'POST',
    idempotencyKey: 'key-1',
    body: { apply: true, expected_digest: 'abc123' },
  })
})
