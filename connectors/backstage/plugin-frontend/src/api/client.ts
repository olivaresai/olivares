// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// A framework-free typed client for the Olivares portal-proxy surface. It takes
// an injected `fetch` (the Backstage `fetchApiRef` at the call site, a stub in
// tests) and a base URL (resolved from `discoveryApiRef` at the call site), so it
// has no dependency on Backstage and is unit-testable in isolation. It maps each
// logical read to the backend proxy subpath; the backend forwards it to the
// control plane (see ../plugin-backend). It performs only GETs — read-first.

import type {
  AgentDTO,
  CatalogEntry,
  DiffResponse,
  GraphResponse,
  InventorySummary,
  ListResponse,
  LiveDTO,
  TimelineDTO,
  Whoami,
} from './types';

/** The minimal response shape the client needs; the DOM `Response` satisfies it. */
export interface FetchResponseLike {
  ok: boolean;
  status: number;
  statusText: string;
  json(): Promise<unknown>;
}

/** The minimal fetch shape the client needs; Backstage's `fetchApi.fetch` fits. */
export type FetchLike = (
  input: string,
  init?: { method?: string; headers?: Record<string, string>; signal?: AbortSignal },
) => Promise<FetchResponseLike>;

/** An upstream/proxy read failure carrying the HTTP status for the UI to branch on. */
export class OlivaresApiError extends Error {
  constructor(
    public readonly status: number,
    message: string,
  ) {
    super(message);
    this.name = 'OlivaresApiError';
  }
}

// These are `type` aliases (not interfaces) on purpose: an object-literal type
// carries an implicit index signature, so it satisfies buildQuery's
// Record<string, …> parameter without a cast.

/** Query parameters for a paged inventory-entities read. */
export type EntityListParams = {
  kind?: string;
  status?: string;
  limit?: number;
  cursor?: string;
};

/** Query parameters for a live-sessions read. */
export type LiveListParams = {
  cc_state?: string;
  limit?: number;
  cursor?: string;
};

/** Query parameters for the access-map graph/drift reads (the edge filters). */
export type GraphParams = {
  origin_kind?: string;
  origin_id?: string;
  resource_id?: string;
  mode?: string;
  confidence?: string;
  signal_source?: string;
  limit?: number;
  cursor?: string;
};

export type NeighborDirection = 'outgoing' | 'incoming' | 'both';

/** The typed client surface the React layer consumes via the OlivaresApi. */
export interface OlivaresClient {
  whoami(): Promise<Whoami>;
  agents(params?: { limit?: number; cursor?: string }): Promise<ListResponse<AgentDTO>>;
  inventorySummary(): Promise<InventorySummary>;
  inventoryEntities(params?: EntityListParams): Promise<ListResponse<CatalogEntry>>;
  sessionsLive(params?: LiveListParams): Promise<ListResponse<LiveDTO>>;
  sessionGet(ref: string): Promise<LiveDTO>;
  sessionTimeline(
    ref: string,
    params?: { limit?: number; cursor?: string },
  ): Promise<ListResponse<TimelineDTO>>;
  accessGraph(params?: GraphParams): Promise<GraphResponse>;
  accessNeighbors(
    id: string,
    direction?: NeighborDirection,
    kind?: string,
  ): Promise<GraphResponse>;
  accessDrift(params?: GraphParams): Promise<DiffResponse>;
}

/** trimTrailingSlash + joinUrl keep exactly one slash between base and path. */
export function joinUrl(base: string, path: string): string {
  return `${base.replace(/\/+$/, '')}/${path.replace(/^\/+/, '')}`;
}

/**
 * buildQuery renders a parameter bag as a query string (leading '?'), skipping
 * undefined/empty values, coercing numbers, and URL-encoding keys and values. An
 * empty bag yields '' so a path stays clean. Deterministic insertion order.
 */
export function buildQuery(
  params: Record<string, string | number | undefined> = {},
): string {
  const parts: string[] = [];
  for (const [k, v] of Object.entries(params)) {
    if (v === undefined || v === '') {
      continue;
    }
    parts.push(`${encodeURIComponent(k)}=${encodeURIComponent(String(v))}`);
  }
  return parts.length === 0 ? '' : `?${parts.join('&')}`;
}

/**
 * createOlivaresClient builds a client bound to a base URL (the proxy base, e.g.
 * https://backstage/api/olivares) and an injected fetch. Every method issues a
 * single GET and decodes JSON, throwing OlivaresApiError on a non-2xx so the
 * React Query layer surfaces a typed error (401/403 → an auth notice, etc.).
 */
export function createOlivaresClient(opts: {
  baseUrl: string;
  fetch: FetchLike;
}): OlivaresClient {
  const { baseUrl, fetch: doFetch } = opts;

  async function getJSON<T>(path: string): Promise<T> {
    const res = await doFetch(joinUrl(baseUrl, path), { method: 'GET' });
    if (!res.ok) {
      const detail = await readErrorMessage(res);
      throw new OlivaresApiError(
        res.status,
        detail ?? `request failed: ${res.status} ${res.statusText}`,
      );
    }
    return (await res.json()) as T;
  }

  const enc = encodeURIComponent;

  return {
    whoami: () => getJSON<Whoami>('/whoami'),
    agents: params => getJSON<ListResponse<AgentDTO>>(`/agents${buildQuery(params)}`),
    inventorySummary: () => getJSON<InventorySummary>('/inventory/summary'),
    inventoryEntities: params =>
      getJSON<ListResponse<CatalogEntry>>(`/inventory/entities${buildQuery(params)}`),
    sessionsLive: params =>
      getJSON<ListResponse<LiveDTO>>(`/sessions/live${buildQuery(params)}`),
    sessionGet: ref => getJSON<LiveDTO>(`/sessions/live/${enc(ref)}`),
    sessionTimeline: (ref, params) =>
      getJSON<ListResponse<TimelineDTO>>(
        `/sessions/live/${enc(ref)}/timeline${buildQuery(params)}`,
      ),
    accessGraph: params =>
      getJSON<GraphResponse>(`/access-map/graph${buildQuery(params)}`),
    accessNeighbors: (id, direction = 'both', kind) =>
      getJSON<GraphResponse>(
        `/access-map/neighbors${buildQuery({ id, direction, kind })}`,
      ),
    accessDrift: params =>
      getJSON<DiffResponse>(`/access-map/drift${buildQuery(params)}`),
  };
}

/**
 * readErrorMessage best-effort extracts the proxy's `{ error: { message } }`
 * envelope without throwing if the body is empty or not JSON. The message is
 * non-sensitive (the proxy never forwards an upstream body).
 */
async function readErrorMessage(res: FetchResponseLike): Promise<string | undefined> {
  try {
    const body = await res.json();
    if (body && typeof body === 'object') {
      const err = (body as { error?: unknown }).error;
      if (err && typeof err === 'object') {
        const msg = (err as { message?: unknown }).message;
        if (typeof msg === 'string') {
          return msg;
        }
      }
    }
  } catch {
    // no/!json body — fall through to the status-based message
  }
  return undefined;
}
