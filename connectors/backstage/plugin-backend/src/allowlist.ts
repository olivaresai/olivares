// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// The read-only allow-list: the single source of truth for which control-plane
// endpoints this proxy will forward, and which query parameters it will pass
// through. It is deliberately a closed list — a backend subpath that is not here
// is NOT proxied (the router returns 404), and a query parameter that is not in a
// route's allow-list is dropped. Read-first by construction (decision: V1):
// every entry is a GET against a documented read endpoint; nothing here mutates
// the control plane or the Olivares estate.

/** The logical routes this proxy exposes. Stable keys used by the router + tests. */
export type RouteKey =
  | 'whoami'
  | 'agents'
  | 'accessEdges'
  | 'inventorySummary'
  | 'inventoryEntities'
  | 'sessionsLive'
  | 'sessionsLiveOne'
  | 'sessionsTimeline'
  | 'accessMapGraph'
  | 'accessMapNeighbors'
  | 'accessMapDrift';

/** One forwardable route: how to build its upstream path and what query it allows. */
export interface RouteSpec {
  /** Builds the control-plane path from captured (already-validated) path params. */
  upstream: (params: Record<string, string>) => string;
  /** The query parameters forwarded for this route; all others are dropped. */
  query: readonly string[];
}

// The shared edge filter columns the control plane accepts on the graph, drift
// and raw access-edge reads (modules/access-map/api.go edgeFilterColumns + the
// core /v1/access-edges list), plus keyset paging.
const EDGE_FILTERS = [
  'origin_kind',
  'origin_id',
  'resource_id',
  'mode',
  'confidence',
  'signal_source',
  'limit',
  'cursor',
] as const;

/**
 * ROUTES is the closed forwarding table. Each value maps a logical route to its
 * upstream control-plane path and the query allow-list. Path parameters (:ref,
 * :id) are validated by isSafePathParam before they reach the upstream builder,
 * and the builder URL-encodes them, so a hostile ref cannot escape its segment.
 */
export const ROUTES: Record<RouteKey, RouteSpec> = {
  whoami: { upstream: () => '/v1/auth/whoami', query: [] },
  agents: { upstream: () => '/v1/agents', query: ['limit', 'cursor'] },
  accessEdges: { upstream: () => '/v1/access-edges', query: EDGE_FILTERS },
  inventorySummary: { upstream: () => '/v1/m/inventory/summary', query: [] },
  inventoryEntities: {
    upstream: () => '/v1/m/inventory/entities',
    query: ['kind', 'status', 'limit', 'cursor'],
  },
  sessionsLive: {
    upstream: () => '/v1/m/sessions/live',
    query: ['cc_state', 'limit', 'cursor'],
  },
  sessionsLiveOne: {
    upstream: p => `/v1/m/sessions/live/${encodeURIComponent(p.ref)}`,
    query: [],
  },
  sessionsTimeline: {
    upstream: p => `/v1/m/sessions/live/${encodeURIComponent(p.ref)}/timeline`,
    query: ['limit', 'cursor'],
  },
  accessMapGraph: { upstream: () => '/v1/m/accessmap/graph', query: EDGE_FILTERS },
  accessMapNeighbors: {
    upstream: () => '/v1/m/accessmap/neighbors',
    query: ['id', 'kind', 'direction'],
  },
  accessMapDrift: { upstream: () => '/v1/m/accessmap/drift', query: EDGE_FILTERS },
};

/**
 * isSafePathParam rejects a path parameter that could break out of its URL
 * segment or inject a new one. A session ref or node id is an opaque token, so
 * we allow a conservative charset and forbid slashes, query/fragment markers and
 * whitespace. An empty value is unsafe (the route requires the param).
 */
export function isSafePathParam(value: string): boolean {
  if (!value) {
    return false;
  }
  // No path/query/fragment separators, no whitespace, no control characters.
  return /^[A-Za-z0-9._:@~-]+$/.test(value);
}

/**
 * upstreamPathFor resolves a route key + path params to a control-plane path, or
 * throws if a required path param is unsafe. Unknown keys are a programming error
 * (the router only ever passes a RouteKey), so this throws rather than returning
 * null.
 */
export function upstreamPathFor(key: RouteKey, params: Record<string, string> = {}): string {
  const spec = ROUTES[key];
  for (const v of Object.values(params)) {
    if (!isSafePathParam(v)) {
      throw new Error('olivares: unsafe path parameter');
    }
  }
  return spec.upstream(params);
}

/**
 * pickQuery filters a raw query bag down to the route's allow-list, coercing
 * array/duplicate values to their first string and dropping everything else. The
 * result is safe to forward verbatim: no parameter the control plane did not
 * document can reach it through this proxy.
 */
export function pickQuery(
  key: RouteKey,
  raw: Record<string, unknown>,
): Record<string, string> {
  const out: Record<string, string> = {};
  for (const name of ROUTES[key].query) {
    const v = raw[name];
    const s = firstString(v);
    if (s !== undefined && s !== '') {
      out[name] = s;
    }
  }
  return out;
}

/** firstString coerces a query value (string | string[] | other) to one string. */
function firstString(v: unknown): string | undefined {
  if (typeof v === 'string') {
    return v;
  }
  if (Array.isArray(v) && typeof v[0] === 'string') {
    return v[0];
  }
  return undefined;
}
