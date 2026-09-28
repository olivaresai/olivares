// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The wire contract of the thirteen gitpublish routes (modules/gitpublish/routes.go): each
// wrapper sends exactly the method, path, query and body the engine mounts and decodes. The
// engine decodes strictly (DisallowUnknownFields → field_not_accepted), so an extra body
// field is a refusal, not a harmless extra.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { configureApiClient } from '@/lib/api/client'
import { gitpublishApi, gitpublishKeys } from './api'

interface Captured {
  url: string
  method: string
  tenant: string | null
  body?: unknown
}

let captured: Captured[] = []

beforeEach(() => {
  captured = []
  configureApiClient({
    getToken: () => 'olvs_test',
    getTenant: () => 'tenant-1',
    onUnauthorized: () => {},
  })
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init: RequestInit = {}) => {
      captured.push({
        url: String(url),
        method: init.method ?? 'GET',
        tenant: new Headers(init.headers).get('X-Olivares-Tenant'),
        body:
          init.body === undefined ? undefined : JSON.parse(String(init.body)),
      })
      const status = init.method === 'DELETE' ? 204 : 200
      return new Response(
        status === 204 ? null : JSON.stringify({ items: [] }),
        {
          status,
          headers: { 'Content-Type': 'application/json' },
        },
      )
    }),
  )
})

afterEach(() => vi.unstubAllGlobals())

const last = () => captured[captured.length - 1]!
const TENANT = { tenant: 'tenant-2' } as const

describe('gitpublish targets', () => {
  it('lists and reads targets', async () => {
    await gitpublishApi.targets()
    expect(last()).toMatchObject({
      method: 'GET',
      url: '/v1/m/gitpublish/targets',
    })
    await gitpublishApi.target('tg/1')
    expect(last()).toMatchObject({
      method: 'GET',
      url: '/v1/m/gitpublish/targets/tg%2F1',
    })
  })

  it('creates, replaces and deletes a target in the tenant the action was taken in', async () => {
    const input = {
      workspace_id: 'ws-1',
      credential_binding_id: 'cb-1',
      repository_binding_id: 'rb-1',
      push_prefix: 'agents/',
      merge_bases: ['main'],
    }
    await gitpublishApi.createTarget(input, TENANT)
    expect(last()).toEqual({
      method: 'POST',
      url: '/v1/m/gitpublish/targets',
      tenant: 'tenant-2',
      body: input,
    })

    // PUT refuses workspace_id (field_not_accepted); empty binding ids keep the current ones.
    const update = {
      expected_version: 3,
      credential_binding_id: '',
      repository_binding_id: '',
      push_prefix: 'bots/',
      merge_bases: ['main', 'release'],
    }
    await gitpublishApi.updateTarget('tg-1', update, TENANT)
    expect(last()).toEqual({
      method: 'PUT',
      url: '/v1/m/gitpublish/targets/tg-1',
      tenant: 'tenant-2',
      body: update,
    })
    expect(last().body).not.toHaveProperty('workspace_id')

    await gitpublishApi.deleteTarget('tg-1', TENANT)
    expect(last()).toMatchObject({
      method: 'DELETE',
      url: '/v1/m/gitpublish/targets/tg-1',
      tenant: 'tenant-2',
    })
  })
})

describe('gitpublish effects', () => {
  it('pushes, opens a pull request and merges on the target', async () => {
    const push = {
      operation_id: 'op-1',
      ref: 'refs/heads/agents/fix',
      expected_old: '',
      commit: 'a'.repeat(40),
      tree: 'b'.repeat(40),
      acknowledge_intent: '',
    }
    await gitpublishApi.push('tg-1', push, TENANT)
    expect(last()).toEqual({
      method: 'POST',
      url: '/v1/m/gitpublish/targets/tg-1/pushes',
      tenant: 'tenant-2',
      body: push,
    })

    const pr = {
      operation_id: 'op-2',
      head_ref: 'agents/fix',
      base: 'main',
      commit: 'a'.repeat(40),
      title: 'Fix',
      body: '',
      draft: true,
      acknowledge_intent: '',
    }
    await gitpublishApi.openPullRequest('tg-1', pr, TENANT)
    expect(last()).toEqual({
      method: 'POST',
      url: '/v1/m/gitpublish/targets/tg-1/pull-requests',
      tenant: 'tenant-2',
      body: pr,
    })

    const merge = {
      operation_id: 'op-3',
      number: 7,
      expected_head: 'c'.repeat(40),
      method: 'squash' as const,
      acknowledge_intent: '',
    }
    await gitpublishApi.merge('tg-1', merge, TENANT)
    expect(last()).toEqual({
      method: 'POST',
      url: '/v1/m/gitpublish/targets/tg-1/merges',
      tenant: 'tenant-2',
      body: merge,
    })
    // expected_result_tree and expected_base are unsupported_requirement: never sent.
    expect(last().body).not.toHaveProperty('expected_result_tree')
    expect(last().body).not.toHaveProperty('expected_base')
  })
})

describe('gitpublish intents', () => {
  it('lists a target’s intents by target_id and reads one with its observations', async () => {
    await gitpublishApi.intents('tg-1')
    expect(last()).toMatchObject({
      method: 'GET',
      url: '/v1/m/gitpublish/intents?target_id=tg-1',
    })
    await gitpublishApi.intent('in-1')
    expect(last()).toMatchObject({
      method: 'GET',
      url: '/v1/m/gitpublish/intents/in-1',
    })
    await gitpublishApi.observations('in-1')
    expect(last()).toMatchObject({
      method: 'GET',
      url: '/v1/m/gitpublish/intents/in-1/observations',
    })
  })

  it('reconciles without a body and abandons with a bounded reason', async () => {
    await gitpublishApi.reconcile('in-1', TENANT)
    expect(last()).toEqual({
      method: 'POST',
      url: '/v1/m/gitpublish/intents/in-1/reconcile',
      tenant: 'tenant-2',
      body: undefined,
    })
    await gitpublishApi.abandon('in-1', 'host outage', TENANT)
    expect(last()).toEqual({
      method: 'POST',
      url: '/v1/m/gitpublish/intents/in-1/abandon',
      tenant: 'tenant-2',
      body: { reason: 'host outage' },
    })
  })
})

describe('gitpublish keys', () => {
  it('put the tenant in every key', () => {
    expect(gitpublishKeys.targets('T')).toContain('T')
    expect(gitpublishKeys.target('T', 'tg')).toContain('T')
    expect(gitpublishKeys.intents('T', 'tg')).toContain('T')
    expect(gitpublishKeys.intent('T', 'in')).toContain('T')
    expect(gitpublishKeys.observations('T', 'in')).toContain('T')
  })
})
