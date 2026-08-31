// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Federated console search: GET /v1/search fans out server-side to every
// searchable kind, deny-closed per kind on the caller's own read permissions.
// Results carry entity names + a short non-sensitive detail only.
import { z } from 'zod'
import { http } from '@/lib/api/client'

const searchResultSchema = z.object({
  kind: z.string(),
  id: z.string(),
  name: z.string(),
  detail: z.string().optional(),
})
const searchResponseSchema = z.object({
  results: z.array(searchResultSchema),
  truncated: z.boolean(),
  // `degraded` is a DIFFERENT answer from `truncated` and the palette renders it
  // differently: truncated means "narrow your query", degraded means "a source failed and
  // this list is missing whatever it held". Optional in the schema so a console built
  // against an older engine still parses — the server always sends both.
  degraded: z.boolean().optional(),
  degraded_kinds: z.array(z.string()).optional(),
})

export type ConsoleSearchResult = z.infer<typeof searchResultSchema>
export type ConsoleSearchResponse = z.infer<typeof searchResponseSchema>

export const searchKeys = {
  /**
   * ⛔ EL INQUILINO VA EN LA CLAVE, y falta aquí costaba datos del inquilino ANTERIOR.
   * `/v1/search` lo resuelve por inquilino (`core/api/search.go` `handleSearch` ->
   * `resolveTenant`), así que la respuesta es distinta en cada uno. Con `['console-search', q]`
   * las dos compartían entrada: buscar «acme» en A, cambiar a B y repetir el término dentro de
   * los 30 s de `staleTime` servía de caché los nombres de entidad de A bajo la cabecera de B,
   * sin emitir una sola petición. Nadie limpia la caché al cambiar de inquilino — el único
   * `queryClient.clear()` es el del logout (`lib/auth/context.tsx`) —, así que la clave es la
   * ÚNICA segregación que hay. Medido y reproducido el 2026-08-23.
   */
  query: (tenant: string | null, q: string) =>
    ['console-search', tenant, q] as const,
}

export async function searchConsole(q: string): Promise<ConsoleSearchResponse> {
  const raw = await http.get<unknown>('/v1/search', { query: { q } })
  return searchResponseSchema.parse(raw)
}

/** Maps a server result kind to the console route that lists that entity. */
export const SEARCH_KIND_ROUTES: Record<string, string> = {
  workspace: '/workspace',
  user: '/console',
  connector: '/console',
  'governance.policy': '/permissions',
  'eventing.subscription': '/eventing',
  'notify.route': '/alerting',
  'orchestration.schedule': '/orchestration',
}

/** Maps a server result kind to the feature id whose nav label names it. */
export const SEARCH_KIND_FEATURE: Record<string, string> = {
  workspace: 'workspaceDashboard',
  user: 'console',
  connector: 'console',
  'governance.policy': 'permissions',
  'eventing.subscription': 'eventing',
  'notify.route': 'alerting',
  'orchestration.schedule': 'orchestration',
}
