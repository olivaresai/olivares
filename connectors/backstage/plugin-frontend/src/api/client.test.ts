// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  FetchLike,
  FetchResponseLike,
  OlivaresApiError,
  buildQuery,
  createOlivaresClient,
  joinUrl,
} from './client';

test('joinUrl keeps exactly one slash', () => {
  assert.equal(joinUrl('https://b/api/olivares/', '/whoami'), 'https://b/api/olivares/whoami');
  assert.equal(joinUrl('https://b/api/olivares', 'whoami'), 'https://b/api/olivares/whoami');
});

test('buildQuery skips empties, coerces numbers, encodes, and is ordered', () => {
  assert.equal(buildQuery(), '');
  assert.equal(buildQuery({ a: undefined, b: '' }), '');
  assert.equal(buildQuery({ limit: 25, cursor: 'a b' }), '?limit=25&cursor=a%20b');
});

/** recordingFetch captures requested URLs and returns a fixed JSON body. */
function recordingFetch(body: unknown, ok = true, status = 200): {
  fetch: FetchLike;
  urls: string[];
} {
  const urls: string[] = [];
  const fetch: FetchLike = async (input: string) => {
    urls.push(input);
    const res: FetchResponseLike = {
      ok,
      status,
      statusText: ok ? 'OK' : 'ERR',
      json: async () => body,
    };
    return res;
  };
  return { fetch, urls };
}

test('client maps each logical read to the right proxy subpath + query', async () => {
  const { fetch, urls } = recordingFetch({ items: [], has_more: false });
  const c = createOlivaresClient({ baseUrl: 'https://b/api/olivares', fetch });

  await c.whoami();
  await c.inventorySummary();
  await c.inventoryEntities({ kind: 'agent', limit: 50 });
  await c.sessionsLive({ cc_state: 'active' });
  await c.sessionGet('sess 1');
  await c.sessionTimeline('sess 1', { limit: 10 });
  await c.accessGraph({ origin_id: 'a1', mode: 'readwrite' });
  await c.accessNeighbors('node1', 'outgoing', 'agent');
  await c.accessDrift();

  assert.deepEqual(urls, [
    'https://b/api/olivares/whoami',
    'https://b/api/olivares/inventory/summary',
    'https://b/api/olivares/inventory/entities?kind=agent&limit=50',
    'https://b/api/olivares/sessions/live?cc_state=active',
    'https://b/api/olivares/sessions/live/sess%201',
    'https://b/api/olivares/sessions/live/sess%201/timeline?limit=10',
    'https://b/api/olivares/access-map/graph?origin_id=a1&mode=readwrite',
    'https://b/api/olivares/access-map/neighbors?id=node1&direction=outgoing&kind=agent',
    'https://b/api/olivares/access-map/drift',
  ]);
});

test('client decodes the JSON body on success', async () => {
  const { fetch } = recordingFetch({ total: 7, by_kind: {}, by_source: {} });
  const c = createOlivaresClient({ baseUrl: 'https://b/api/olivares', fetch });
  const summary = await c.inventorySummary();
  assert.equal(summary.total, 7);
});

test('client throws OlivaresApiError with the status on a non-2xx', async () => {
  const { fetch } = recordingFetch({ error: { message: 'forbidden' } }, false, 403);
  const c = createOlivaresClient({ baseUrl: 'https://b/api/olivares', fetch });
  await assert.rejects(
    () => c.accessDrift(),
    (err: unknown) => {
      assert.ok(err instanceof OlivaresApiError);
      assert.equal((err as OlivaresApiError).status, 403);
      assert.equal((err as OlivaresApiError).message, 'forbidden');
      return true;
    },
  );
});

test('client falls back to a status message when the error body is unhelpful', async () => {
  const fetch: FetchLike = async () => ({
    ok: false,
    status: 502,
    statusText: 'Bad Gateway',
    json: async () => {
      throw new Error('not json');
    },
  });
  const c = createOlivaresClient({ baseUrl: 'https://b/api/olivares', fetch });
  await assert.rejects(
    () => c.whoami(),
    (err: unknown) => {
      assert.equal((err as OlivaresApiError).status, 502);
      assert.match((err as OlivaresApiError).message, /502/);
      return true;
    },
  );
});
