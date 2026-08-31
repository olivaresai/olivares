// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/errors'
import { configureApiClient } from '@/lib/api/client'
import {
  isProtocolUnknown,
  listProtocolBindingSpecs,
  reconcileProtocolBinding,
  runProtocolBindingSpecCreate,
  runProtocolBindingSpecTransition,
} from './api'
import {
  buildProtocolBindingSpecInput,
  defaultProtocolComposerDraft,
} from './model'

interface Captured {
  url: string
  method: string
  headers: Record<string, string>
  body?: string
}

let captured: Captured[] = []
const TENANT_REQUEST = { tenant: 'tenant-1' } as const

function stubFetch(
  responder: (request: Captured) => {
    status?: number
    body?: unknown
    headers?: Record<string, string>
  },
) {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init: RequestInit = {}) => {
      const headers: Record<string, string> = {}
      new Headers(init.headers).forEach((value, key) => {
        headers[key.toLowerCase()] = value
      })
      const request = {
        url: String(url),
        method: init.method ?? 'GET',
        headers,
        body: init.body === undefined ? undefined : String(init.body),
      }
      captured.push(request)
      const response = responder(request)
      return new Response(JSON.stringify(response.body ?? {}), {
        status: response.status ?? 200,
        headers: {
          'Content-Type': 'application/json',
          ...(response.headers ?? {}),
        },
      })
    }),
  )
}

beforeEach(() => {
  captured = []
  configureApiClient({
    getToken: () => 'olvs_test',
    getTenant: () => 'tenant-1',
    onUnauthorized: () => {},
  })
})

afterEach(() => vi.unstubAllGlobals())

function input() {
  const draft = defaultProtocolComposerDraft()
  draft.bindingKey = 'peer-work'
  draft.localRef = 'work-1'
  draft.peerAuthority = 'peer.example'
  draft.remoteResourceRef = 'queue-1'
  draft.permissionProfileRef = 'profile:standard'
  return buildProtocolBindingSpecInput('workspace-1', draft)
}

describe('spec create validate → plan → apply', () => {
  it('pins the plan and reuses the caller-owned idempotency key', async () => {
    configureApiClient({ getTenant: () => 'tenant-2' })
    stubFetch((request) => ({
      body: request.url.includes('mode=apply')
        ? {
            verdict: 'CLEAN',
            code: 'draft_applied',
            validation: {
              verdict: 'CLEAN',
              code: 'capability_validated',
              observed_at: '2026-08-20T00:00:00Z',
            },
            plan_hash: 'plan-1',
            operation: 'draft',
            workspace_id: 'workspace-1',
            generation: 1,
            spec_hash: 'spec-1',
            mapping_hash: 'mapping-1',
            losses_hash: 'losses-1',
            spec: { id: 'spec-1', state: 'draft' },
          }
        : {
            verdict: 'CLEAN',
            code: 'draft_planned',
            validation: {
              verdict: 'CLEAN',
              code: 'capability_validated',
              observed_at: '2026-08-20T00:00:00Z',
            },
            plan_hash: request.url.includes('mode=plan') ? 'plan-1' : '',
            operation: 'draft',
            workspace_id: 'workspace-1',
            generation: 1,
            spec_hash: 'spec-1',
            mapping_hash: 'mapping-1',
            losses_hash: 'losses-1',
          },
      headers: request.url.includes('mode=apply')
        ? { ETag: '"v1"', 'Idempotency-Replayed': 'true' }
        : undefined,
    }))

    const body = input()
    await runProtocolBindingSpecCreate(body, 'validate', TENANT_REQUEST)
    const plan = await runProtocolBindingSpecCreate(
      body,
      'plan',
      TENANT_REQUEST,
    )
    const intention = '018f0000-0000-7000-8000-000000000010'
    const first = await runProtocolBindingSpecCreate(
      body,
      'apply',
      TENANT_REQUEST,
      {
        idempotencyKey: intention,
        planHash: plan.plan_hash,
      },
    )
    await runProtocolBindingSpecCreate(body, 'apply', TENANT_REQUEST, {
      idempotencyKey: intention,
      planHash: plan.plan_hash,
    })

    expect(captured.map((request) => request.url)).toEqual([
      '/v1/m/sessions/protocol-binding-specs?mode=validate',
      '/v1/m/sessions/protocol-binding-specs?mode=plan',
      '/v1/m/sessions/protocol-binding-specs?mode=apply',
      '/v1/m/sessions/protocol-binding-specs?mode=apply',
    ])
    expect(captured[2]?.headers['if-plan-hash']).toBe('plan-1')
    expect(captured[2]?.headers['idempotency-key']).toBe(intention)
    expect(captured[3]?.headers['idempotency-key']).toBe(intention)
    expect(
      captured.every(
        (request) => request.headers['x-olivares-tenant'] === 'tenant-1',
      ),
    ).toBe(true)
    expect(JSON.parse(captured[2]?.body ?? '{}')).not.toHaveProperty(
      'validation',
    )
    expect(first).toMatchObject({ replayed: true, etag: '"v1"' })
  })
})

describe('list and lifecycle preconditions', () => {
  it('always scopes the list to the selected workspace', async () => {
    stubFetch(() => ({ body: { items: [], has_more: false } }))
    await listProtocolBindingSpecs(
      {
        workspace_id: 'workspace-1',
        protocol: 'mcp',
        state: 'draft',
      },
      TENANT_REQUEST,
    )
    expect(captured[0]?.url).toContain('workspace_id=workspace-1')
    expect(captured[0]?.url).toContain('protocol=mcp')
    expect(captured[0]?.url).toContain('state=draft')
  })

  it('carries the fresh ETag through transition planning and apply', async () => {
    stubFetch((request) => ({
      body: {
        verdict: 'CLEAN',
        code: 'activate_planned',
        validation: {
          verdict: 'CLEAN',
          code: 'capability_validated',
          observed_at: '2026-08-20T00:00:00Z',
        },
        plan_hash: request.url.includes('mode=validate') ? '' : 'plan-2',
        operation: 'activate',
        spec: { id: 'spec-1', state: 'active' },
      },
    }))
    await runProtocolBindingSpecTransition(
      'spec-1',
      'activate',
      'validate',
      '"v4"',
      TENANT_REQUEST,
    )
    await runProtocolBindingSpecTransition(
      'spec-1',
      'activate',
      'plan',
      '"v4"',
      TENANT_REQUEST,
    )
    await runProtocolBindingSpecTransition(
      'spec-1',
      'activate',
      'apply',
      '"v4"',
      TENANT_REQUEST,
      {
        planHash: 'plan-2',
        idempotencyKey: '018f0000-0000-7000-8000-000000000011',
      },
    )
    expect(
      captured.every((request) => request.headers['if-match'] === '"v4"'),
    ).toBe(true)
    expect(captured[2]?.headers['if-plan-hash']).toBe('plan-2')
  })
})

describe('reconcile validate → plan → test → apply', () => {
  it('tests without an apply key, then records with the exact plan, ETag, and key', async () => {
    stubFetch((request) => ({
      body: request.url.includes('mode=plan')
        ? {
            verdict: 'LIMPIO',
            code: 'binding_reconcile_planned',
            plan_hash: 'plan-3',
            checks: [],
            command: 'binding.reconcile',
            row_effects: ['sessions.protocol_binding:update'],
            event_type: 'work.binding.observed',
            audit_action: 'sessions.work.binding.reconcile',
            permission: 'sessions:protocol-binding:write',
            external_calls: ['protocol_binding.get'],
          }
        : {
            verdict: 'LIMPIO',
            code: 'observed',
            observed_at: '2026-08-20T00:00:00Z',
            checks: [],
            plan_hash: request.url.includes('mode=validate') ? '' : 'plan-3',
            resource: { id: 'binding-1' },
          },
    }))
    await reconcileProtocolBinding(
      'binding-1',
      'validate',
      '"v7"',
      TENANT_REQUEST,
    )
    await reconcileProtocolBinding('binding-1', 'plan', '"v7"', TENANT_REQUEST)
    await reconcileProtocolBinding(
      'binding-1',
      'test',
      '"v7"',
      TENANT_REQUEST,
      { planHash: 'plan-3' },
    )
    await reconcileProtocolBinding(
      'binding-1',
      'apply',
      '"v7"',
      TENANT_REQUEST,
      {
        planHash: 'plan-3',
        idempotencyKey: '018f0000-0000-7000-8000-000000000012',
      },
    )
    expect(captured.map((request) => request.url)).toEqual([
      '/v1/m/sessions/protocol-bindings/binding-1/reconcile?mode=validate',
      '/v1/m/sessions/protocol-bindings/binding-1/reconcile?mode=plan',
      '/v1/m/sessions/protocol-bindings/binding-1/reconcile?mode=test',
      '/v1/m/sessions/protocol-bindings/binding-1/reconcile?mode=apply',
    ])
    expect(captured[2]?.headers['idempotency-key']).toBeUndefined()
    expect(captured[2]?.headers['if-plan-hash']).toBe('plan-3')
    expect(captured[3]?.headers['idempotency-key']).toBe(
      '018f0000-0000-7000-8000-000000000012',
    )
    expect(
      captured.every((request) => request.headers['if-match'] === '"v7"'),
    ).toBe(true)
  })
})

describe('unknown is a first-class outcome through both vocabularies', () => {
  it('recognizes 200 bodies and error envelopes without matching clean results', () => {
    expect(isProtocolUnknown({ verdict: 'UNKNOWN' })).toBe(true)
    expect(isProtocolUnknown({ verdict: 'NO_HE_PODIDO_MIRAR' })).toBe(true)
    expect(isProtocolUnknown({ verdict: 'CLEAN' })).toBe(false)
    expect(
      isProtocolUnknown(
        new ApiError(
          503,
          'observation_unavailable',
          'unknown',
          undefined,
          {},
          {
            verdict: 'NO_HE_PODIDO_MIRAR',
            code: 'observation_unavailable',
          },
        ),
      ),
    ).toBe(true)
  })
})
