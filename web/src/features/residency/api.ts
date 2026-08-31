// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Data-residency administration. The org region pin lives on the SYSTEM
// store, so both routes are superadmin-gated (authzSystem, `system:admin`):
//   GET  /v1/system/residency                → configured registry + posture
//   GET  /v1/system/orgs                     → the orgs + their current pin
//   PUT  /v1/system/orgs/{tenant}/region     → set/clear the pin
// The engine is the authority on which regions are valid: an empty pin always
// unpins; a non-empty pin is validated server-side against the instance's residency
// config (400 names the known regions) — the console NEVER fabricates a region list.
// Moving a pinned tenant's existing data between regions is a separate migration,
// out of this endpoint's scope (core/api/server.go).
//
// ── EL RECORTE DE `GET /v1/system/orgs`, MEDIDO (2026-08-27) ─────────────────────────
//
// Paso 1 de la receta de `scripts/check-list-truncation-witness.sh` («MIDE EL MOTOR PRIMERO»).
// Veredicto: caso (a), DRENA. Las sondas, para que la siguiente sesion no lo re-derive:
//
//   · `handleListOrgs` (core/api/handlers_core.go:739-761) NO llama a `parseListQuery`, no lee
//     `?limit` ni `?cursor`, y nunca fija `Cursor` ni el campo de recorte de la respuesta.
//   · Su almacen (`core/internal/store/sqlstore/system.go:497` -> `listOrgsVisibleRows`) emite
//     `SELECT ... ORDER BY id ASC` SIN `LIMIT`. O devuelve todo lo visible, o falla con
//     `store.ErrEnumerationNotAuthoritative` — nunca un parcial callado.
//   · Es el UNICO listador del nucleo que todavia no usa `parseListQuery`: los demas
//     (`handleListUsers` :233, `handleListAgents` :60) si lo usan y si publican el recorte.
//
// ⇒ HOY el campo de recorte llega siempre apagado, y por eso `<ListTruncationBadge>` no pinta
//   nada: exige el valor booleano verdadero (`features/_intel/notices.tsx`) y devuelve `null` en
//   cualquier otro caso. No afirma cobertura: no dice nada.
//
// ⛔ ENTONCES POR QUE VA EL AVISO. Porque manda el CONTRATO, no la implementacion de hoy:
//    `web/openapi/openapi.json` declara ese campo en el 200 de esta operacion (`operationId`
//    `listOrgs`), asi que el motor puede empezar a paginar SIN romper nada publicado — y siendo
//    el unico listador que aun no pagina, es el candidato natural a que alguien lo alinee con
//    sus vecinos. El dia que pase, esta pantalla ya lo dice en vez de ensenar las primeras cien
//    filas como si fueran el censo entero.
//
//    El coste es asimetrico y por eso se decide asi: ponerlo mientras drena no cuesta nada y no
//    miente; omitirlo cuando deje de drenar se descubre con la lista ya mal leida.
import { http } from '@/lib/api/client'
import type { ListResponse } from '@/lib/api/types'

/** One org as the system roster surfaces it (core/api OrgDTO). `data_region` is the
 * residency pin — empty/absent when the tenant is unpinned. */
export interface OrgDTO {
  id: string
  tenant_id: string
  name: string
  slug: string
  status: string
  data_region?: string
  created_at: string
}

/** Deployment-wide residency registry (core/api residencyRegistryDTO). */
export interface ResidencyRegistryDTO {
  home_region: string
  regions: string[]
  enforces: boolean
}

export const residencyApi = {
  getRegistry: () => http.get<ResidencyRegistryDTO>('/v1/system/residency'),
  listOrgs: () => http.get<ListResponse<OrgDTO>>('/v1/system/orgs'),
  // An empty `dataRegion` clears the pin (unpinned); a non-empty value pins the
  // tenant and is validated by the engine (400 `bad_request` on an unknown region
  // or a non-region-scoped instance).
  setOrgRegion: (tenant: string, dataRegion: string) =>
    http.put<OrgDTO>(`/v1/system/orgs/${encodeURIComponent(tenant)}/region`, {
      data_region: dataRegion,
    }),
}

export const residencyKeys = {
  // System-level data (not tenant-scoped): the deployment registry + org roster.
  registry: () => ['residency', 'registry'] as const,
  orgs: () => ['residency', 'orgs'] as const,
}
