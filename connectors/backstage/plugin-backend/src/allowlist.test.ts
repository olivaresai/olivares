// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { ROUTES, RouteKey, isSafePathParam, pickQuery, upstreamPathFor } from './allowlist';

test('every route maps to a /v1/ control-plane read path', () => {
  for (const key of Object.keys(ROUTES) as RouteKey[]) {
    const path = ROUTES[key].upstream({ ref: 'r', id: 'i' });
    assert.match(path, /^\/v1\//, `${key} -> ${path}`);
  }
});

test('upstreamPathFor builds the documented control-plane paths', () => {
  assert.equal(upstreamPathFor('whoami'), '/v1/auth/whoami');
  assert.equal(upstreamPathFor('inventorySummary'), '/v1/m/inventory/summary');
  assert.equal(upstreamPathFor('accessMapDrift'), '/v1/m/accessmap/drift');
  assert.equal(
    upstreamPathFor('sessionsLiveOne', { ref: 'sess-123' }),
    '/v1/m/sessions/live/sess-123',
  );
  assert.equal(
    upstreamPathFor('sessionsTimeline', { ref: 'sess-123' }),
    '/v1/m/sessions/live/sess-123/timeline',
  );
});

test('isSafePathParam rejects separators, whitespace and emptiness', () => {
  assert.equal(isSafePathParam('sess-123'), true);
  assert.equal(isSafePathParam('agt_8f3c2a'), true);
  assert.equal(isSafePathParam(''), false);
  assert.equal(isSafePathParam('a/b'), false);
  assert.equal(isSafePathParam('a?b=c'), false);
  assert.equal(isSafePathParam('a b'), false);
  assert.equal(isSafePathParam('../etc'), false);
});

test('upstreamPathFor throws on an unsafe path parameter (no segment escape)', () => {
  assert.throws(() => upstreamPathFor('sessionsLiveOne', { ref: '../../v1/agents' }));
  assert.throws(() => upstreamPathFor('sessionsTimeline', { ref: 'a/b' }));
});

test('a path param that survives validation is URL-encoded by the builder', () => {
  // ':' and '@' are allowed by the charset but still encoded into the segment.
  assert.equal(
    upstreamPathFor('sessionsLiveOne', { ref: 'svc:default@host' }),
    '/v1/m/sessions/live/svc%3Adefault%40host',
  );
});

test('pickQuery keeps only allow-listed params and drops the rest', () => {
  const picked = pickQuery('inventoryEntities', {
    kind: 'agent',
    status: 'active',
    limit: '50',
    cursor: 'abc',
    // not allow-listed — must be dropped:
    evil: 'rm -rf',
    tenant: 'other-tenant',
    sort: 'name',
  });
  assert.deepEqual(picked, { kind: 'agent', status: 'active', limit: '50', cursor: 'abc' });
});

test('pickQuery coerces array values to the first string and skips empties', () => {
  const picked = pickQuery('sessionsLive', {
    cc_state: ['active', 'idle'],
    limit: '',
    cursor: undefined,
  });
  assert.deepEqual(picked, { cc_state: 'active' });
});

test('pickQuery forwards the full edge-filter set for the access map', () => {
  const raw = {
    origin_kind: 'agent',
    origin_id: 'a1',
    resource_id: 'r1',
    mode: 'readwrite',
    confidence: 'attributed',
    signal_source: 'otel',
    limit: '100',
    cursor: 'c1',
  };
  assert.deepEqual(pickQuery('accessMapGraph', raw), raw);
});
