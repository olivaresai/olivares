// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE WIRE CONTRACT of the eight I1 operations, measured through the REAL client:
// exact paths, exact query, exact headers and exact bodies — and the two things a
// body alone cannot say: that a replay is a replay, and that an Ack's If-Match is the
// strong ETag of the version the operator READ, never a guess.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { __resetRefreshState, configureApiClient } from '@/lib/api/client'
import { ApiError, NetworkError } from '@/lib/api/errors'
import {
  createCapabilityPermit,
  type CapabilityContext,
  type NormalizedCapabilityQuestion,
} from '@/lib/auth/capabilities'
import {
  ackDelivery,
  advanceCursor,
  createChannel,
  getChannel,
  getCursorToken,
  getDelivery,
  getHandoffDetail,
  getMessage,
  grantChannel,
  listAdministrableChannels,
  listAgents,
  listChannelGrants,
  listChannels,
  listHandoffInbox,
  listInbox,
  listMembers,
  offerHandoff,
  respondToHandoff,
  revokeChannelGrant,
  sendNotice,
  updateChannel,
} from './api'
import {
  administrationSurfaceQuestion,
  grantSheetQuestion,
} from './capabilities'
import { classifyFailure } from './errors'
import {
  buildAckIntent,
  buildCursorIntent,
  buildGrantIntent,
  buildHandoffOfferIntent,
  buildHandoffResponseIntent,
  buildRevokeIntent,
  buildSendIntent,
  buildUpdateChannelIntent,
  deliveryEtag,
  buildCreateIntent,
} from './intent'
import type { SendNoticeInput } from './types'

interface Sent {
  url: string
  method: string
  headers: Headers
  body: string | undefined
}
const sent: Sent[] = []

function respond(
  status: number,
  body: unknown,
  headers: Record<string, string> = {},
) {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init?: RequestInit) => {
      sent.push({
        url,
        method: init?.method ?? 'GET',
        headers: new Headers(init?.headers),
        body: typeof init?.body === 'string' ? init.body : undefined,
      })
      return new Response(body === undefined ? null : JSON.stringify(body), {
        status,
        headers: { 'Content-Type': 'application/json', ...headers },
      })
    }),
  )
}

const TENANT = { tenant: 't-1' } as const
const WS = '0192f2c0-aaaa-7000-8000-000000000001'
const SCOPE = { tenant: 't-1', workspace: WS, boundary: 'b1' }

/* ── the admission the two administrative reads travel under ─────────────────── */

/** The live authority these wire cases are taken under. The permits below are REAL
 *  (`createCapabilityPermit`), so their `isCurrent()` is the product's own rule and not
 *  a stub that always agrees. */
const CONTEXT: CapabilityContext = {
  principalKind: 'user',
  actor: 'user:u-1',
  tenant: 't-1',
  credentialGeneration: 0,
  workspace: WS,
  lifetime: 0,
}
/** A current permit for exactly one question: deadline far past every case's clock. */
const admitted = (
  question: NormalizedCapabilityQuestion | null,
  live: () => CapabilityContext | null = () => CONTEXT,
) =>
  createCapabilityPermit(
    CONTEXT,
    question as NormalizedCapabilityQuestion,
    performance.now() + 30_000,
    live,
  )
const surface = (ws = WS) => admitted(administrationSurfaceQuestion(ws))
const entity = (channel: string, ws = WS) =>
  admitted(grantSheetQuestion(ws, channel))
const READ = {
  message: {
    id: 'm1',
    version: 1,
    channel_id: 'c1',
    thread_id: 'th',
    state: 'published',
    sender: { kind: 'user', ref: 'u1' },
    content: { subject: 's', blocks: [{ type: 'text', text: 'x' }] },
    urgency: 'normal',
    ack_policy: 'each_required',
    available_at: '2026-09-06T00:00:00Z',
  },
  delivery: {
    id: 'd1',
    version: 3,
    message_id: 'm1',
    recipient: { kind: 'user', ref: 'u2' },
    delivery_seq: 1,
    required: true,
    state: 'delivered',
    available_at: '2026-09-06T00:00:00Z',
  },
  fulfillment: {
    state: 'pending',
    required: 1,
    acknowledged: 0,
    viable: 1,
    unmet: 1,
  },
}

beforeEach(() => {
  sent.length = 0
  __resetRefreshState()
  configureApiClient({
    getToken: () => 'olvs_test',
    getTenant: () => 'ambient-tenant',
    onUnauthorized: () => {},
  })
})
afterEach(() => {
  vi.unstubAllGlobals()
})

describe('communications wire contract', () => {
  it('GET /channels sends the explicit workspace, limit and the opaque continuation, under the intent tenant', async () => {
    respond(200, { items: [], has_more: false })
    await listChannels({ workspace_id: WS, limit: 25 }, TENANT)
    expect(sent[0].url).toBe(
      `/v1/m/sessions/channels?workspace_id=${WS}&limit=25`,
    )
    expect(sent[0].method).toBe('GET')
    expect(sent[0].headers.get('X-Olivares-Tenant')).toBe('t-1')
    respond(200, { items: [], has_more: false })
    await listChannels(
      { workspace_id: WS, limit: 200, continuation: 'c3n1.opaque' },
      TENANT,
    )
    expect(sent[1].url).toBe(
      `/v1/m/sessions/channels?workspace_id=${WS}&limit=200&continuation=c3n1.opaque`,
    )
  })

  it('POST /channels sends the body verbatim and returns the ETag; it carries NO idempotency key', async () => {
    const body = {
      workspace_id: WS,
      slug: 'ops',
      name: 'Ops',
      kind: 'coordination' as const,
      initial_grants: [
        {
          subject: { kind: 'user' as const, ref: 'u1' },
          can_read: true,
          can_write: false,
          can_admin: false,
        },
      ],
    }
    respond(
      201,
      { channel: { id: 'c1' }, grants: [], etag: '"v1"', audit_seq: 7 },
      { ETag: '"v1"' },
    )
    const out = await createChannel(buildCreateIntent(SCOPE, body), TENANT)
    expect(sent[0].url).toBe('/v1/m/sessions/channels')
    expect(sent[0].method).toBe('POST')
    expect(JSON.parse(sent[0].body!)).toEqual(body)
    expect(sent[0].headers.get('Idempotency-Key')).toBeNull()
    expect(out.etag).toBe('"v1"')
  })

  it('GET /channels/{id} encodes the id and returns the ETag beside the channel', async () => {
    respond(200, { id: 'c 1' }, { ETag: '"v4"' })
    const out = await getChannel('c 1', TENANT)
    expect(sent[0].url).toBe('/v1/m/sessions/channels/c%201')
    expect(out.etag).toBe('"v4"')
    expect(out.channel).toEqual({ id: 'c 1' })
  })

  it('POST /messages/send carries the INTENT key and body, no plan hash; a retry re-sends the same key', async () => {
    const body: SendNoticeInput = {
      channel_id: 'c1',
      recipient: { kind: 'user', ref: 'u2' },
      content: {
        subject: 'hello',
        blocks: [{ type: 'text', format: 'plain', text: 'hi' }],
      },
      urgency: 'high',
    }
    const intent = buildSendIntent(SCOPE, 'c1', body)
    // Canonical: UUID v7, the only form the Ack normaliser accepts.
    expect(intent.key).toMatch(
      /^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/,
    )
    respond(201, { replayed: false, message_id: 'm1' })
    const first = await sendNotice(intent, TENANT)
    expect(sent[0].url).toBe('/v1/m/sessions/messages/send')
    expect(sent[0].headers.get('Idempotency-Key')).toBe(intent.key)
    expect(sent[0].headers.get('If-Plan-Hash')).toBeNull()
    expect(JSON.parse(sent[0].body!)).toEqual(body)
    expect(first.replayed).toBe(false)
    expect(first.status).toBe(201)
    // The engine replays from its ledger: 200 + replayed, and the console SAYS replay.
    respond(200, { replayed: true, message_id: 'm1' })
    const second = await sendNotice(intent, TENANT)
    expect(sent[1].headers.get('Idempotency-Key')).toBe(intent.key)
    expect(sent[1].body).toBe(sent[0].body)
    expect(second.replayed).toBe(true)
  })

  it('a built intent is frozen: its body cannot be edited after confirmation', () => {
    const intent = buildSendIntent(SCOPE, 'c1', {
      channel_id: 'c1',
      recipient: { kind: 'user', ref: 'u2' },
      content: { subject: 's', blocks: [{ type: 'text', text: 'x' }] },
    })
    expect(Object.isFrozen(intent)).toBe(true)
    expect(() => {
      ;(intent as { key: string }).key = 'other'
    }).toThrow()
  })

  it('GET /inbox, /deliveries/{id} and /messages/{id} use their exact paths', async () => {
    respond(200, { items: [], has_more: false })
    await listInbox({ workspace_id: WS, limit: 50 }, TENANT)
    expect(sent[0].url).toBe(`/v1/m/sessions/inbox?workspace_id=${WS}&limit=50`)
    respond(200, READ)
    await getDelivery('d/1', TENANT)
    expect(sent[1].url).toBe('/v1/m/sessions/deliveries/d%2F1')
    respond(200, READ)
    await getMessage('m1', TENANT)
    expect(sent[2].url).toBe('/v1/m/sessions/messages/m1')
  })

  it('POST /deliveries/{id}/ack is bodyless and carries If-Match "vN" from the READ version plus its own key', async () => {
    const intent = buildAckIntent(SCOPE, 'd1', 3)
    expect(intent.etag).toBe('"v3"')
    expect(intent.key).toMatch(
      /^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/,
    )
    expect(deliveryEtag(12)).toBe('"v12"')
    respond(
      200,
      { replayed: false, late: false, ack_id: 'a1', etag: '"v4"' },
      { ETag: '"v4"' },
    )
    const out = await ackDelivery(intent, TENANT)
    expect(sent[0].url).toBe('/v1/m/sessions/deliveries/d1/ack')
    expect(sent[0].method).toBe('POST')
    expect(sent[0].body).toBeUndefined()
    expect(sent[0].headers.get('If-Match')).toBe('"v3"')
    expect(sent[0].headers.get('Idempotency-Key')).toBe(intent.key)
    expect(out.etag).toBe('"v4"')
  })

  it('the directory reads ask for a ceiling and the agents read is workspace-bound', async () => {
    respond(200, { items: [], has_more: false })
    await listMembers(TENANT)
    expect(sent[0].url).toBe('/v1/members?limit=200')
    respond(200, { items: [], has_more: false })
    await listAgents(WS, TENANT)
    expect(sent[1].url).toBe(`/v1/agents?workspace_id=${WS}&limit=200`)
  })
})

describe('I2 wire contract — administration and the personal cursor', () => {
  const RECIPIENT = '0192f2c0-eeee-7000-8000-00000000000b'
  const CHANNEL = '0192f2c0-bbbb-7000-8000-000000000001'
  const GRANT = '0192f2c0-9999-7000-8000-000000000001'

  it('GET /channels/administration sends workspace, the explicit persisted state, limit and the opaque continuation; no If-None-Match', async () => {
    respond(200, { items: [], has_more: false })
    await listAdministrableChannels(
      { workspace_id: WS, state: 'all', limit: 50 },
      { ...TENANT, admission: surface() },
    )
    expect(sent[0].url).toBe(
      `/v1/m/sessions/channels/administration?workspace_id=${WS}&state=all&limit=50`,
    )
    expect(sent[0].method).toBe('GET')
    expect(sent[0].headers.get('If-None-Match')).toBeNull()
    expect(sent[0].headers.get('X-Olivares-Tenant')).toBe('t-1')
    respond(200, { items: [], has_more: false })
    await listAdministrableChannels(
      {
        workspace_id: WS,
        state: 'archived',
        limit: 200,
        continuation: 'c3a1.next',
      },
      { ...TENANT, admission: surface() },
    )
    expect(sent[1].url).toBe(
      `/v1/m/sessions/channels/administration?workspace_id=${WS}&state=archived&limit=200&continuation=c3a1.next`,
    )
  })

  it('GET /channels/{id}/grants sends the persisted state, both subject parts or neither, limit and continuation; never If-None-Match', async () => {
    respond(200, { items: [], has_more: false })
    await listChannelGrants(
      CHANNEL,
      { workspace_id: WS, state: 'active', limit: 100 },
      { ...TENANT, admission: entity(CHANNEL) },
    )
    expect(sent[0].url).toBe(
      `/v1/m/sessions/channels/${CHANNEL}/grants?workspace_id=${WS}&state=active&limit=100`,
    )
    expect(sent[0].headers.get('If-None-Match')).toBeNull()
    respond(200, { items: [], has_more: false })
    await listChannelGrants(
      CHANNEL,
      {
        workspace_id: WS,
        state: 'all',
        limit: 25,
        subject_kind: 'user',
        subject_ref: 'u 1',
        continuation: 'c3g1.page-2',
      },
      { ...TENANT, admission: entity(CHANNEL) },
    )
    expect(sent[1].url).toBe(
      // `u+1`, not `u%201`: the shared client builds the query with
      // URLSearchParams, which is form encoding — a space is `+` there. What this
      // pins is that the reference is ENCODED by the client and never spliced raw.
      `/v1/m/sessions/channels/${CHANNEL}/grants?workspace_id=${WS}&state=all&subject_kind=user&subject_ref=u+1&limit=25&continuation=c3g1.page-2`,
    )
    // A kind without a reference is refused by the engine as a pair: neither is sent.
    respond(200, { items: [], has_more: false })
    await listChannelGrants(
      CHANNEL,
      { workspace_id: WS, state: 'revoked', limit: 50, subject_kind: 'agent' },
      { ...TENANT, admission: entity(CHANNEL) },
    )
    expect(sent[2].url).not.toContain('subject_kind')
  })

  it('PATCH /channels carries If-Match of the CHANNEL ETag read, the channel_id and ONLY the changed fields, and NO idempotency key', async () => {
    const intent = buildUpdateChannelIntent(SCOPE, CHANNEL, '"v7"', {
      channel_id: CHANNEL,
      name: 'Renamed',
      max_fanout: 4,
    })
    respond(
      200,
      { channel: { id: CHANNEL, version: 8 }, etag: '"v8"', audit_seq: 3 },
      { ETag: '"v8"' },
    )
    const out = await updateChannel(intent, TENANT)
    expect(sent[0].url).toBe('/v1/m/sessions/channels')
    expect(sent[0].method).toBe('PATCH')
    expect(sent[0].headers.get('If-Match')).toBe('"v7"')
    expect(sent[0].headers.get('Idempotency-Key')).toBeNull()
    expect(JSON.parse(sent[0].body!)).toEqual({
      channel_id: CHANNEL,
      name: 'Renamed',
      max_fanout: 4,
    })
    expect(out.etag).toBe('"v8"')
    expect(Object.isFrozen(intent)).toBe(true)
  })

  it('POST /channels/{id}/grants carries If-Match of the channel and the explicit generation body; no idempotency key', async () => {
    const body = {
      subject: { kind: 'user' as const, ref: 'u2' },
      can_read: true,
      can_write: false,
      can_admin: true,
      expires_at: '2030-01-01T00:00:00.000Z',
    }
    const intent = buildGrantIntent(SCOPE, CHANNEL, '"v2"', body)
    respond(
      200,
      {
        channel: { id: CHANNEL, version: 3 },
        grant: { id: GRANT, generation: 2 },
        etag: '"v3"',
        audit_seq: 4,
      },
      { ETag: '"v3"' },
    )
    const out = await grantChannel(intent, TENANT)
    expect(sent[0].url).toBe(`/v1/m/sessions/channels/${CHANNEL}/grants`)
    expect(sent[0].method).toBe('POST')
    expect(sent[0].headers.get('If-Match')).toBe('"v2"')
    expect(sent[0].headers.get('Idempotency-Key')).toBeNull()
    expect(JSON.parse(sent[0].body!)).toEqual(body)
    expect(out.result.grant).toMatchObject({ id: GRANT, generation: 2 })
    expect(out.etag).toBe('"v3"')
  })

  it('POST /channels/{id}/grants/{grant_id}/revoke is bodyless, names the EXACT generation, and carries If-Match of the channel', async () => {
    const intent = buildRevokeIntent(SCOPE, CHANNEL, GRANT, '"v3"')
    respond(
      200,
      { channel: { id: CHANNEL, version: 4 }, etag: '"v4"', audit_seq: 5 },
      { ETag: '"v4"' },
    )
    const out = await revokeChannelGrant(intent, TENANT)
    expect(sent[0].url).toBe(
      `/v1/m/sessions/channels/${CHANNEL}/grants/${GRANT}/revoke`,
    )
    expect(sent[0].method).toBe('POST')
    expect(sent[0].body).toBeUndefined()
    expect(sent[0].headers.get('If-Match')).toBe('"v3"')
    expect(sent[0].headers.get('Idempotency-Key')).toBeNull()
    expect(out.etag).toBe('"v4"')
  })

  it('GET /inbox/cursors/personal/{recipient} sends workspace and the page target, RUNS ITS REQUIRED GUARD before the fetch, returns the cursor ETag and mutates nothing', async () => {
    respond(
      200,
      { cursor: 'c2v2.token', cursor_id: 'cur-1', version: 2, etag: '"v2"' },
      { ETag: '"v2"' },
    )
    // The guard is REQUIRED on this caller (IR-I2-1): the preparation carries no
    // intent, so without it nothing would compare the authority before the wire.
    const guard = vi.fn()
    const out = await getCursorToken(
      RECIPIENT,
      { workspace_id: WS, target: 'c2n1.target' },
      { ...TENANT, guard },
    )
    expect(guard).toHaveBeenCalledTimes(1)
    expect(sent[0].url).toBe(
      `/v1/m/sessions/inbox/cursors/personal/${RECIPIENT}?workspace_id=${WS}&target=c2n1.target`,
    )
    expect(sent[0].method).toBe('GET')
    expect(sent[0].body).toBeUndefined()
    expect(out.etag).toBe('"v2"')
    expect(out.result.cursor).toBe('c2v2.token')
  })

  it('PUT /inbox/cursors/personal/{recipient} has NO query, the frozen {cursor, delivery_id} body, If-Match of the CURSOR and its own canonical key; a retry re-sends the identical object', async () => {
    const intent = buildCursorIntent(
      SCOPE,
      RECIPIENT,
      { cursor: 'c2v2.token', version: 2, etag: '"v2"' },
      'd-last',
    )
    expect(intent.key).toMatch(
      /^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/,
    )
    expect(Object.isFrozen(intent)).toBe(true)
    expect(Object.isFrozen(intent.body)).toBe(true)
    respond(
      200,
      {
        command_id: 'cmd',
        cursor_id: 'cur-1',
        version: 3,
        etag: '"v3"',
        projection: { last_seen_seq: 7 },
        audit_seq: 6,
        replayed: false,
      },
      { ETag: '"v3"' },
    )
    const first = await advanceCursor(intent, TENANT)
    expect(sent[0].url).toBe(
      `/v1/m/sessions/inbox/cursors/personal/${RECIPIENT}`,
    )
    expect(sent[0].method).toBe('PUT')
    expect(sent[0].headers.get('If-Match')).toBe('"v2"')
    expect(sent[0].headers.get('Idempotency-Key')).toBe(intent.key)
    expect(JSON.parse(sent[0].body!)).toEqual({
      cursor: 'c2v2.token',
      delivery_id: 'd-last',
    })
    expect(first.result.projection.last_seen_seq).toBe(7)
    respond(200, {
      command_id: 'cmd',
      cursor_id: 'cur-1',
      version: 3,
      etag: '"v3"',
      projection: { last_seen_seq: 7 },
      audit_seq: 6,
      replayed: true,
    })
    const second = await advanceCursor(intent, TENANT)
    expect(sent[1].headers.get('Idempotency-Key')).toBe(intent.key)
    expect(sent[1].headers.get('If-Match')).toBe('"v2"')
    expect(sent[1].body).toBe(sent[0].body)
    expect(second.result.replayed).toBe(true)
  })
})

describe('classifyFailure — seven answers, never two', () => {
  const api = (status: number, code: string, body?: unknown) =>
    new ApiError(status, code, 'm', 'req-1', {}, body)
  it.each([
    [
      api(412, 'version_mismatch', {
        verdict: 'BROKEN',
        code: 'version_mismatch',
      }),
      'version_mismatch',
    ],
    [
      api(412, 'plan_changed', { verdict: 'BROKEN', code: 'plan_changed' }),
      'plan_changed',
    ],
    [api(428, 'version_required'), 'version_required'],
    [
      api(503, 'evidence_unavailable', {
        verdict: 'UNKNOWN',
        code: 'evidence_unavailable',
      }),
      'unavailable',
    ],
    [api(403, 'forbidden'), 'forbidden'],
    [api(403, 'step_up_required'), 'step_up'],
    [api(404, 'not_found'), 'not_found'],
    [api(409, 'terminal', { verdict: 'BROKEN', code: 'terminal' }), 'terminal'],
    // The grant history's OWN conflict: the Channel moved between two pages.
    [
      api(409, 'channel_snapshot_changed', {
        verdict: 'BROKEN',
        code: 'channel_snapshot_changed',
      }),
      'snapshot_changed',
    ],
    // The served engine's bare store conflict for a stale Ack (measured 2026-09-06).
    [api(409, 'internal', { error: { message: 'conflict' } }), 'conflict'],
    [api(400, 'invalid_request'), 'invalid'],
    [api(429, 'rate_limited'), 'rate_limited'],
    [new NetworkError('down'), 'ambiguous'],
    [new DOMException('x', 'AbortError'), 'aborted'],
  ])('%o → %s', (err, kind) => {
    expect(classifyFailure(err).kind).toBe(kind)
  })
  it("does not show the client's placeholder code as the engine's", () => {
    // The served engine's bare store conflict for a stale Ack (measured 2026-09-06):
    // no code in the envelope, so the shared client fills in `internal`.
    const f = classifyFailure(
      api(409, 'internal', { error: { message: 'conflict' } }),
    )
    expect(f.kind).toBe('conflict')
    expect(f.code).toBeUndefined()
    expect(f.status).toBe(409)
  })

  it('keeps the code, status and request id beside the kind', () => {
    const f = classifyFailure(
      api(412, 'version_mismatch', { code: 'version_mismatch' }),
    )
    expect(f).toMatchObject({
      status: 412,
      code: 'version_mismatch',
      requestId: 'req-1',
    })
  })
})

/* ── I3: the four handoff callers, measured through the REAL client ───────────── */

const OFFER_BODY = {
  channel_id: '0192f2c0-bbbb-7000-8000-000000000001',
  work_item_id: '0192f2c0-7777-7000-8000-000000000001',
  recipient: { kind: 'user' as const, ref: 'u-b' },
  handoff: {
    summary: 'Deploy freeze needs an owner',
    next_action: 'Confirm the window',
    artifact_refs: [{ kind: 'runbook', ref: 'rb-1' }],
  },
  ack_deadline: '2026-09-30T12:00:00Z',
  expected_owner_epoch: 1,
}

const RESPONSE_RECEIPT = {
  ack_id: 'ack-1',
  audit_seq: 42,
  command_id: 'cmd-1',
  delivery_id: 'd-h',
  etag: '"h-v2"',
  event_id: 'ev-1',
  handoff_id: 'h-1',
  message_id: 'm-h',
  owner_epoch: 2,
  replayed: false,
  state: 'accepted',
  version: 2,
  work_item_id: 'w-1',
}

describe('I3 wire contract — the work handoff', () => {
  it('POST /handoffs carries the WORKITEM If-Match, the intent key and the exact body; 201 is fresh and 200 is a replay', async () => {
    const intent = buildHandoffOfferIntent(SCOPE, 'w-1', '"w-v3"', OFFER_BODY)
    respond(
      201,
      {
        handoff_id: 'h-1',
        delivery_id: 'd-h',
        replayed: false,
        etag: '"h-v1"',
      },
      { ETag: '"h-v1"' },
    )
    const fresh = await offerHandoff(intent, TENANT)
    expect(sent[0].url).toBe('/v1/m/sessions/handoffs')
    expect(sent[0].method).toBe('POST')
    // The PRECONDITION is the work item's, not the handoff's: nothing else exists yet.
    expect(sent[0].headers.get('If-Match')).toBe('"w-v3"')
    expect(sent[0].headers.get('Idempotency-Key')).toBe(intent.key)
    expect(intent.key).toMatch(
      /^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/,
    )
    expect(sent[0].headers.get('X-Olivares-Tenant')).toBe('t-1')
    // The BYTES, in full: the epoch travels in the body and the ETag in the header,
    // and both come from the same read.
    expect(JSON.parse(sent[0].body as string)).toEqual(OFFER_BODY)
    expect(fresh.replayed).toBe(false)
    expect(fresh.status).toBe(201)
    expect(fresh.etag).toBe('"h-v1"')

    // The ledger answering: 200 + replayed, and the caller SAYS replay.
    respond(200, {
      handoff_id: 'h-1',
      delivery_id: 'd-h',
      replayed: true,
      etag: '"h-v1"',
    })
    const again = await offerHandoff(intent, TENANT)
    expect(sent[1].headers.get('Idempotency-Key')).toBe(intent.key)
    expect(again.replayed).toBe(true)
    expect(again.status).toBe(200)
  })

  it('GET /inbox/handoffs sends the explicit workspace, EXACTLY ONE state, the limit and the opaque continuation', async () => {
    respond(200, { items: [], has_more: false })
    await listHandoffInbox(
      { workspace_id: WS, state: 'offered', limit: 50 },
      TENANT,
    )
    expect(sent[0].url).toBe(
      `/v1/m/sessions/inbox/handoffs?workspace_id=${WS}&state=offered&limit=50`,
    )
    expect(sent[0].method).toBe('GET')
    respond(200, { items: [], has_more: false })
    await listHandoffInbox(
      {
        workspace_id: WS,
        state: 'accepted',
        limit: 200,
        continuation: 'h3n1.opaque',
      },
      TENANT,
    )
    expect(sent[1].url).toBe(
      `/v1/m/sessions/inbox/handoffs?workspace_id=${WS}&state=accepted&limit=200&continuation=h3n1.opaque`,
    )
  })

  it('GET /deliveries/{id}/handoff resolves through the CARRIER delivery id and encodes it', async () => {
    respond(200, { handoff: { id: 'h-1' } })
    await getHandoffDetail('d/1', TENANT)
    expect(sent[0].url).toBe('/v1/m/sessions/deliveries/d%2F1/handoff')
    expect(sent[0].method).toBe('GET')
    // There is no `GET /handoffs/{id}` on this engine, and this caller never invents
    // one: the only path it can build is the Delivery-bound read above.
    expect(sent[0].url).not.toMatch(/\/handoffs\/[^/]+$/)
  })

  it('POST /handoffs/{id}/responses carries the HANDOFF ETag — never the carrier version — its own key, and the exact accept body', async () => {
    const intent = buildHandoffResponseIntent(
      SCOPE,
      {
        handoffId: 'h-1',
        // The handoff's OWN validator, verbatim from the protected detail.
        etag: '"h-v1"',
        workItemId: 'w-1',
        recipient: { kind: 'user', ref: 'u-b' },
        deliveryId: 'd-h',
      },
      { transition: 'accept' },
    )
    respond(200, RESPONSE_RECEIPT, { ETag: '"h-v2"' })
    const out = await respondToHandoff(intent, TENANT)
    expect(sent[0].url).toBe('/v1/m/sessions/handoffs/h-1/responses')
    expect(sent[0].method).toBe('POST')
    expect(sent[0].headers.get('If-Match')).toBe('"h-v1"')
    // The carrier's version is 1 in every fixture of this file; an ETag rebuilt from
    // it would be `"v1"`. It is not what travels, and that is the whole assertion.
    expect(sent[0].headers.get('If-Match')).not.toBe(deliveryEtag(1))
    expect(sent[0].headers.get('Idempotency-Key')).toBe(intent.key)
    // ACCEPT SENDS NO REASON: the key is absent, not null and not empty.
    expect(JSON.parse(sent[0].body as string)).toEqual({ transition: 'accept' })
    expect(out.result.owner_epoch).toBe(2)
    // The R45 vacant-generation path carries NO fence, and the caller does not add one.
    expect(out.result.resulting_lease_fence).toBeUndefined()
    expect(out.etag).toBe('"h-v2"')
  })

  it('a rejection carries the required reason verbatim, and its replay is reported from the BODY (this route answers 200 either way)', async () => {
    const intent = buildHandoffResponseIntent(
      SCOPE,
      {
        handoffId: 'h-1',
        etag: '"h-v1"',
        workItemId: 'w-1',
        recipient: { kind: 'user', ref: 'u-b' },
        deliveryId: 'd-h',
      },
      {
        transition: 'reject',
        reason: {
          code: 'outside_scope',
          text: 'Platform owns the freeze window.',
          references: [{ kind: 'runbook', ref: 'rb-1', hash: 'sha256:ab' }],
        },
      },
    )
    respond(200, { ...RESPONSE_RECEIPT, state: 'rejected', replayed: false })
    const first = await respondToHandoff(intent, TENANT)
    expect(JSON.parse(sent[0].body as string)).toEqual({
      transition: 'reject',
      reason: {
        code: 'outside_scope',
        text: 'Platform owns the freeze window.',
        references: [{ kind: 'runbook', ref: 'rb-1', hash: 'sha256:ab' }],
      },
    })
    expect(first.replayed).toBe(false)

    respond(200, { ...RESPONSE_RECEIPT, state: 'rejected', replayed: true })
    const second = await respondToHandoff(intent, TENANT)
    expect(sent[1].headers.get('Idempotency-Key')).toBe(intent.key)
    expect(sent[1].body).toBe(sent[0].body)
    expect(second.replayed).toBe(true)
  })

  it('BOTH I3 intents are DEEPLY frozen: a nested edit after confirmation cannot change the bytes of a same-key retry', () => {
    const draftRefs = [{ kind: 'runbook', ref: 'rb-1' }]
    const draft = {
      ...OFFER_BODY,
      handoff: { ...OFFER_BODY.handoff, artifact_refs: draftRefs },
    }
    const offer = buildHandoffOfferIntent(SCOPE, 'w-1', '"w-v3"', draft)
    expect(Object.isFrozen(offer)).toBe(true)
    expect(Object.isFrozen(offer.body)).toBe(true)
    expect(Object.isFrozen(offer.body.handoff)).toBe(true)
    expect(Object.isFrozen(offer.body.handoff.artifact_refs)).toBe(true)
    expect(Object.isFrozen(offer.body.recipient)).toBe(true)
    // The draft the FORM still holds is a different object graph.
    draftRefs.push({ kind: 'ticket', ref: 'sneaked-in' })
    draft.handoff.summary = 'edited after confirmation'
    expect(offer.body.handoff.artifact_refs).toHaveLength(1)
    expect(offer.body.handoff.summary).toBe('Deploy freeze needs an owner')
    // …and the frozen copy itself refuses a direct nested mutation.
    expect(() => {
      ;(
        offer.body.handoff.artifact_refs as { kind: string; ref: string }[]
      ).push({
        kind: 'ticket',
        ref: 'x',
      })
    }).toThrow()

    const reject = buildHandoffResponseIntent(
      SCOPE,
      {
        handoffId: 'h-1',
        etag: '"h-v1"',
        workItemId: 'w-1',
        recipient: { kind: 'user', ref: 'u-b' },
        deliveryId: 'd-h',
      },
      { transition: 'reject', reason: { code: 'other', references: [] } },
    )
    expect(Object.isFrozen(reject)).toBe(true)
    expect(Object.isFrozen(reject.body)).toBe(true)
    expect(Object.isFrozen(reject.body.reason)).toBe(true)
    expect(Object.isFrozen(reject.recipient)).toBe(true)
  })

  it('classifies the R45 handoff failure shapes as DISTINCT answers, including the code-less 409', () => {
    // At R45 a stale precondition, a stale offer, an ended generation, an owner-epoch
    // mismatch and a rebound key are ALL this one shape. It is one conflict CLASS;
    // nothing here decides which internal condition failed.
    expect(
      classifyFailure(
        new ApiError(409, 'internal', 'conflict', undefined, {
          error: { message: 'conflict' },
        }),
      ),
    ).toMatchObject({ kind: 'conflict', status: 409, code: undefined })
    // …and 412 plan_changed is NOT that class: the common mapper really returns it.
    expect(
      classifyFailure(
        new ApiError(412, 'plan_changed', 'plan changed', undefined, {
          code: 'plan_changed',
        }),
      ),
    ).toMatchObject({ kind: 'plan_changed', status: 412 })
    // 428 is the console's own defect: a precondition it must not have omitted.
    expect(
      classifyFailure(new ApiError(428, 'version_required', 'need If-Match')),
    ).toMatchObject({ kind: 'version_required', status: 428 })
    // 503 evidence_unavailable stays UNKNOWN — never empty, never refused.
    expect(
      classifyFailure(
        new ApiError(503, 'evidence_unavailable', 'no evidence', undefined, {
          code: 'evidence_unavailable',
        }),
      ),
    ).toMatchObject({ kind: 'unavailable', status: 503 })
    // A personal-detail 404 keeps its generic concealment, and a list 403 is a refusal.
    expect(
      classifyFailure(new ApiError(404, 'not_found', 'nope')),
    ).toMatchObject({
      kind: 'not_found',
    })
    expect(classifyFailure(new ApiError(403, 'forbidden', 'no'))).toMatchObject(
      {
        kind: 'forbidden',
      },
    )
    // A dropped response is AMBIGUOUS, and it is not any of the above.
    expect(classifyFailure(new NetworkError('socket closed'))).toMatchObject({
      kind: 'ambiguous',
    })
  })
})
