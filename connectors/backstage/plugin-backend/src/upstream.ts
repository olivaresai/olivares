// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Framework-free helpers for talking to the Olivares control-plane REST surface.
// These are pure functions (no Express, no Backstage, no global state) so the
// proxy router stays a thin shell over tested logic. The control-plane bearer
// token is assembled into a header HERE and never leaves the backend process —
// it is never returned to the browser, logged, or copied into a response body.

/** trimTrailingSlash removes any trailing '/' so joins produce exactly one. */
export function trimTrailingSlash(base: string): string {
  return base.replace(/\/+$/, '');
}

/** joinUrl concatenates a base and a path with exactly one separating slash. */
export function joinUrl(base: string, path: string): string {
  return `${trimTrailingSlash(base)}/${path.replace(/^\/+/, '')}`;
}

/**
 * buildQueryString renders an allow-listed parameter bag as a query string
 * (leading '?'), skipping undefined/empty values and URL-encoding every key and
 * value. An empty bag yields '' (no '?'). Insertion order is preserved so the
 * output is deterministic for tests and caches.
 */
export function buildQueryString(params: Record<string, string | undefined>): string {
  const parts: string[] = [];
  for (const [k, v] of Object.entries(params)) {
    if (v === undefined || v === '') {
      continue;
    }
    parts.push(`${encodeURIComponent(k)}=${encodeURIComponent(v)}`);
  }
  return parts.length === 0 ? '' : `?${parts.join('&')}`;
}

/** buildUpstreamUrl joins base + upstream path + an optional query bag. */
export function buildUpstreamUrl(
  baseUrl: string,
  upstreamPath: string,
  query: Record<string, string | undefined> = {},
): string {
  return joinUrl(baseUrl, upstreamPath) + buildQueryString(query);
}

/** Inputs for the upstream authentication and identity-hint headers. */
export interface UpstreamHeaderInput {
  /** The control-plane bearer token (sent as Authorization: Bearer). */
  token?: string;
  /** Optional multi-tenant selector (sent as X-Olivares-Tenant). */
  tenant?: string;
  /**
   * The Backstage user entity ref of the portal caller (e.g. user:default/jdoe),
   * sent as X-Olivares-On-Behalf-Of as an informational identity hint. The current
   * control plane does not consume this header for authorization or ledger
   * attribution. Omitted when the caller could not be resolved.
   */
  onBehalfOf?: string;
}

/**
 * buildUpstreamHeaders assembles the request headers for a forwarded read. The
 * bearer token is attached ONLY here, as an Authorization header; the tenant and
 * on-behalf-of identity hint are added when present. Nothing sensitive is ever
 * echoed back to the browser (the router returns only the upstream JSON body).
 */
export function buildUpstreamHeaders(input: UpstreamHeaderInput): Record<string, string> {
  const headers: Record<string, string> = { Accept: 'application/json' };
  if (input.token) {
    headers.Authorization = `Bearer ${input.token}`;
  }
  if (input.tenant) {
    headers['X-Olivares-Tenant'] = input.tenant;
  }
  if (input.onBehalfOf) {
    headers['X-Olivares-On-Behalf-Of'] = input.onBehalfOf;
  }
  return headers;
}
