// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  buildQueryString,
  buildUpstreamHeaders,
  buildUpstreamUrl,
  joinUrl,
  trimTrailingSlash,
} from './upstream';

test('trimTrailingSlash removes any number of trailing slashes', () => {
  assert.equal(trimTrailingSlash('https://cp.example.com///'), 'https://cp.example.com');
  assert.equal(trimTrailingSlash('https://cp.example.com'), 'https://cp.example.com');
});

test('joinUrl inserts exactly one slash regardless of input slashing', () => {
  assert.equal(joinUrl('https://cp/', '/v1/agents'), 'https://cp/v1/agents');
  assert.equal(joinUrl('https://cp', 'v1/agents'), 'https://cp/v1/agents');
});

test('buildQueryString skips empty/undefined and encodes keys and values', () => {
  assert.equal(buildQueryString({}), '');
  assert.equal(buildQueryString({ a: '', b: undefined }), '');
  assert.equal(
    buildQueryString({ kind: 'mcp server', cursor: 'a/b=c' }),
    '?kind=mcp%20server&cursor=a%2Fb%3Dc',
  );
});

test('buildQueryString preserves insertion order (deterministic)', () => {
  assert.equal(buildQueryString({ limit: '10', cursor: 'x' }), '?limit=10&cursor=x');
});

test('buildUpstreamUrl composes base, path and query', () => {
  assert.equal(
    buildUpstreamUrl('https://cp/', '/v1/m/inventory/entities', { kind: 'agent' }),
    'https://cp/v1/m/inventory/entities?kind=agent',
  );
});

test('buildUpstreamHeaders attaches bearer, tenant and on-behalf-of only when set', () => {
  assert.deepEqual(buildUpstreamHeaders({}), { Accept: 'application/json' });

  assert.deepEqual(
    buildUpstreamHeaders({ token: 'secret', tenant: 't1', onBehalfOf: 'user:default/jo' }),
    {
      Accept: 'application/json',
      Authorization: 'Bearer secret',
      'X-Olivares-Tenant': 't1',
      'X-Olivares-On-Behalf-Of': 'user:default/jo',
    },
  );
});

test('buildUpstreamHeaders omits the Authorization header when there is no token', () => {
  const h = buildUpstreamHeaders({ tenant: 't1' });
  assert.equal('Authorization' in h, false);
  assert.equal(h['X-Olivares-Tenant'], 't1');
});
