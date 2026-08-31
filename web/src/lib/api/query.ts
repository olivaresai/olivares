// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient } from '@tanstack/react-query'
import { ApiError } from './errors'

/**
 * TanStack Query is the single source of truth for SERVER state (cache,
 * invalidation, polling for the live views builds). Zustand owns only local
 * UI state. createQueryClient returns a fresh client (the app makes one; tests
 * make isolated ones).
 *
 * Defaults:
 *  - Never retry a 4xx (a 401/403/404 won't fix itself by retrying); retry
 *    transient 5xx/network up to twice with backoff.
 *  - 30s staleTime — operator data is fresh-ish without hammering the engine;
 *    live views opt into shorter intervals via refetchInterval.
 */
export function createQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: {
        staleTime: 30_000,
        gcTime: 5 * 60_000,
        refetchOnWindowFocus: true,
        retry: (failureCount, error) => {
          if (error instanceof ApiError && error.status < 500) return false
          return failureCount < 2
        },
      },
      mutations: {
        retry: false,
      },
    },
  })
}

/**
 * Query-key factory. CONTRACT:
 *  - Tenant-scoped data MUST include the active tenant id in its key (via
 *    `tenantScope`), so switching tenant cache-isolates and refetches cleanly.
 *  - Global/system data uses a flat key.
 *  - Invalidate the narrowest prefix that changed, e.g. after creating an agent:
 *      qc.invalidateQueries({ queryKey: queryKeys.agents.all(tenant) })
 * A feature-module defines its own keys the same way under its namespace.
 */
export const queryKeys = {
  serverInfo: ['server-info'] as const,
  whoami: ['whoami'] as const,
  orgs: ['system', 'orgs'] as const,
  // ⛔ Con `['users', params ?? null]` acumulaba los DOS defectos que los trinquetes de esta casa
  //    existen para cazar: sin inquilino, y con el ausente resuelto a `null`, que produce una
  //    clave CONCRETA en vez de un prefijo estable por el que invalidar. Hoy no tiene consumidor
  //    —cero llamadas en `web/src`—, y por eso se arregla en vez de borrarse: quien la estrene
  //    mañana se encuentra la forma buena, no la trampa.
  users: (tenant: string | null, params?: unknown) =>
    ['users', tenant, params ?? null] as const,
  agents: {
    all: (tenant: string | null) => ['agents', tenant] as const,
    list: (tenant: string | null, params?: unknown) =>
      ['agents', tenant, 'list', params ?? null] as const,
    detail: (tenant: string | null, id: string) =>
      ['agents', tenant, 'detail', id] as const,
  },
  accessGraph: {
    all: (tenant: string | null) => ['access-graph', tenant] as const,
    edges: (tenant: string | null, params?: unknown) =>
      ['access-graph', tenant, 'edges', params ?? null] as const,
    drift: (tenant: string | null, params?: unknown) =>
      ['access-graph', tenant, 'drift', params ?? null] as const,
  },
  audit: {
    all: (tenant: string | null) => ['audit', tenant] as const,
    list: (tenant: string | null, params?: unknown) =>
      ['audit', tenant, 'list', params ?? null] as const,
    systemList: (tenant: string | null, params?: unknown) =>
      ['audit', tenant, 'system-list', params ?? null] as const,
    verify: (tenant: string | null) => ['audit', tenant, 'verify'] as const,
    pubkey: (tenant: string | null) => ['audit', tenant, 'pubkey'] as const,
  },
}
