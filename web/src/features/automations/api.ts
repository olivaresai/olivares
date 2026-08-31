// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Automations — the unified read surface over the three automation rails:
// schedules (orchestration), event subscriptions (eventing) and alert routes
// (notify). This page AGGREGATES; authoring stays in each rail's own feature.
import { http } from '@/lib/api/client'
import type { ListResponse } from '@/lib/api/types'

/** The minimal per-rail projections this page needs (each rail's own feature
 * owns the full DTO). */
export interface RailSchedule {
  id: string
  name: string
  desired_status: string
  health: string
}

export interface RailSubscription {
  id: string
  name: string
  enabled: boolean
}

export interface RailRoute {
  id: string
  name: string
  enabled: boolean
}

export interface EventTypeInfo {
  type: string
  stability: string
  permission: string
  description: string
}

export interface MatchTypeInfo {
  type: string
  description: string
}

// El techo que pedimos al motor. Los tres raíles de esta pantalla los sirven handlers que
// llaman `listQuery(r)` —`modules/orchestration/schedules.go:343`,
// `modules/eventing/subscription.go:269` y `modules/notify/route.go:141`—, así que SIN `limit`
// el repositorio genérico pagina a 100 (`core/internal/store/sqlstore/generic.go:28`) y esta
// vista cuenta sobre una lista recortada sin decirlo. Pedimos el máximo que el motor acepta
// (`maxLimit`, misma línea 29). Eso sube el umbral de 100 a 1000 y, cuando aun así se recorte,
// lo deja DECLARABLE. ⛔ NO se afirma que el recorte pase a ser «raro»: eso dependería de la
// cardinalidad real de cada inquilino, que no está medida. Subir el techo cambia CUÁNDO ocurre,
// no cuánto de infrecuente es:
// el motor sigue publicando `has_more` y el aviso lo lee de ahí.
// ⛔ Y EL TECHO ES EL VALOR POR DEFECTO, no disciplina del llamante. F-03 del contraste: con
// `{ query }` a secas, `automationsApi.routes()` era TypeScript válido y salía SIN `limit`, y el
// motor volvía al 100 por defecto. No había tal llamante hoy, pero la invariante la sostenía la
// vista, no la API. Con `{ limit: EVIDENCE_PAGE, ...query }` —en ESE orden— el techo va siempre y
// el `limit` de un llamante que pida otra cosa GANA, que es la forma que prescribe la receta del
// censo (paso 2).
export const EVIDENCE_PAGE = 1000

export const automationsApi = {
  schedules: (query?: { limit?: number }) =>
    http.get<ListResponse<RailSchedule>>('/v1/m/orchestration/schedules', {
      query: { limit: EVIDENCE_PAGE, ...query },
    }),
  subscriptions: (query?: { limit?: number }) =>
    http.get<ListResponse<RailSubscription>>('/v1/m/eventing/subscriptions', {
      query: { limit: EVIDENCE_PAGE, ...query },
    }),
  routes: (query?: { limit?: number }) =>
    http.get<ListResponse<RailRoute>>('/v1/m/notify/routes', {
      query: { limit: EVIDENCE_PAGE, ...query },
    }),
  eventTypes: () =>
    http.get<{ event_types: EventTypeInfo[] }>('/v1/m/eventing/event-types'),
  matchTypes: () =>
    http.get<{ match_types: MatchTypeInfo[] }>('/v1/m/notify/match-types'),
}

export const automationsKeys = {
  all: (tenant: string | null) => ['automations', tenant] as const,
  schedules: (tenant: string | null) =>
    ['automations', tenant, 'schedules'] as const,
  subscriptions: (tenant: string | null) =>
    ['automations', tenant, 'subscriptions'] as const,
  routes: (tenant: string | null) => ['automations', tenant, 'routes'] as const,
  eventTypes: (tenant: string | null) =>
    ['automations', tenant, 'event-types'] as const,
  matchTypes: (tenant: string | null) =>
    ['automations', tenant, 'match-types'] as const,
}
