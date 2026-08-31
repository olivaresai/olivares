// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Endpoint helpers + query keys for the catalog module (XIV). Thin wrappers over the
// core HTTP client against /v1/m/catalog (ARCHITECTURE.md — no logic here). The active
// tenant header is attached automatically; tenant-scoped keys cache-isolate per
// tenant (query.ts contract). Writes send ONLY the allowed fields (the backend
// rejects unknown fields) — see EntryInput / InstantiateInput / TransitionInput.
import { http } from '@/lib/api'
import type { ListResponse } from '@/lib/api/types'
import type {
  AdmissionDTO,
  AdmissionKind,
  AdmitInput,
  AdmitResponse,
  EntryDTO,
  EntryInput,
  InstanceDTO,
  InstantiateInput,
  PubkeyDTO,
  TransitionInput,
  VerifyDTO,
  AdmissionPolicy,
  AdmissionPolicyInput,
} from './types'

const BASE = '/v1/m/catalog'

/** Las dos rutas de política de admisión, DELETREADAS como las deletrea el router
 *  (`modules/catalog/api.go:58,67`), no compuestas con `${kind}-admission/policy`.
 *
 *  No es capricho: el motor registra DOS rutas con DOS manejadores, y un parámetro pegado a un
 *  literal dentro de un segmento es además ilegible para el guardián de rutas de consola —
 *  `listAdmissions` ya arrastra ese punto ciego con `${kind}-admissions`. Escribirlas enteras cuesta
 *  una línea y las hace comprobables. */
const ADMISSION_POLICY_PATH: Record<AdmissionKind, string> = {
  mcp: `${BASE}/mcp-admission/policy`,
  connector: `${BASE}/connector-admission/policy`,
}

export interface EntryListParams {
  cursor?: string
  limit?: number
  kind?: string
  status?: string
  slug?: string
}

export interface InstanceListParams {
  cursor?: string
  limit?: number
  status?: string
  entry_id?: string
}

/**
 * El techo de las listas del catalogo. Mismo valor que el `maxLimit` del store generico
 * (`core/internal/store/sqlstore/generic.go:26-29`): pedir mas seria pedir algo que el motor
 * recorta igual, y pedir menos, recortar dos veces.
 *
 * ⛔ EL TECHO SE PIDE, NO SE HEREDA. Sin `limit` el store sirve su pagina por omision de CIEN, y
 * las tres listas de abajo no mandaban ninguno: una consola de catalogo con mas de cien entradas
 * ensenaba cien y nada decia que faltaran.
 *
 * ⛔ Y EL ORDEN DEL SPREAD ES EL CONTRATO, no estilo: `{ limit, ...params }` deja que un `params`
 * con `limit: undefined` BORRE el techo —el serializador omite los `undefined` y el motor vuelve a
 * su omision—, mientras `{ ...params, limit: ... ?? TOPE }` deja al llamante BAJARLO y no borrarlo.
 * Por eso las dos que aceptan `params` llevan esa forma y la de admisiones, que no acepta ninguno,
 * lo lleva a secas: ahi no hay llamante que pueda borrarlo.
 */
const CATALOG_PAGE = 1000

export const catalogApi = {
  // --- entries ---------------------------------------------------------------
  listEntries: (params?: EntryListParams) =>
    http.get<ListResponse<EntryDTO>>(`${BASE}/entries`, {
      query: { ...params, limit: params?.limit ?? CATALOG_PAGE },
    }),
  getEntry: (id: string) =>
    http.get<EntryDTO>(`${BASE}/entries/${encodeURIComponent(id)}`),
  createEntry: (input: EntryInput) =>
    http.post<EntryDTO>(`${BASE}/entries`, input),
  updateEntry: (id: string, input: EntryInput) =>
    http.put<EntryDTO>(`${BASE}/entries/${encodeURIComponent(id)}`, input),
  deleteEntry: (id: string) =>
    http.delete<void>(`${BASE}/entries/${encodeURIComponent(id)}`),
  submitEntry: (id: string) =>
    http.post<EntryDTO>(`${BASE}/entries/${encodeURIComponent(id)}/submit`),
  approveEntry: (id: string) =>
    http.post<EntryDTO>(`${BASE}/entries/${encodeURIComponent(id)}/approve`),
  deprecateEntry: (id: string) =>
    http.post<EntryDTO>(`${BASE}/entries/${encodeURIComponent(id)}/deprecate`),
  verifyEntry: (id: string) =>
    http.get<VerifyDTO>(`${BASE}/entries/${encodeURIComponent(id)}/verify`),
  instantiateEntry: (id: string, input: InstantiateInput) =>
    http.post<InstanceDTO>(
      `${BASE}/entries/${encodeURIComponent(id)}/instantiate`,
      input,
    ),

  // --- attestation admissions (mcp/connector entries) ------------------------
  // The recorded verdict is the DURABLE "why was this admitted/refused?" surface.
  // Endpoints differ per kind (there is no catalog route for the model gate — it
  // lives in the models module), so the caller passes the gated kind explicitly.
  /** La política de admisión del tenant para una CLASE de entrada. Lectura: `entry:read`. */
  admissionPolicy: (kind: AdmissionKind) =>
    http.get<AdmissionPolicy>(ADMISSION_POLICY_PATH[kind]),

  /** Escribe la política. El motor exige ADMIN y lo audita (`api.go:59,68`), así que la vista tiene
   *  que gatearla por permiso: un botón que siempre se ve y contesta 403 es peor que uno ausente. */
  putAdmissionPolicy: (kind: AdmissionKind, body: AdmissionPolicyInput) =>
    http.put<AdmissionPolicy>(ADMISSION_POLICY_PATH[kind], body),

  listAdmissions: (kind: AdmissionKind, entryRef: string) =>
    http.get<ListResponse<AdmissionDTO>>(`${BASE}/${kind}-admissions`, {
      query: { entry_ref: entryRef, limit: CATALOG_PAGE },
    }),
  admitEntry: (id: string, input: AdmitInput) =>
    http.post<AdmitResponse>(
      `${BASE}/entries/${encodeURIComponent(id)}/admit`,
      input,
    ),

  // --- signing posture (effectively constant per node — fetch once) ----------
  pubkey: () => http.get<PubkeyDTO>(`${BASE}/pubkey`),

  // --- instances -------------------------------------------------------------
  listInstances: (params?: InstanceListParams) =>
    http.get<ListResponse<InstanceDTO>>(`${BASE}/instances`, {
      query: { ...params, limit: params?.limit ?? CATALOG_PAGE },
    }),
  getInstance: (id: string) =>
    http.get<InstanceDTO>(`${BASE}/instances/${encodeURIComponent(id)}`),
  transitionInstance: (id: string, input: TransitionInput) =>
    http.post<InstanceDTO>(
      `${BASE}/instances/${encodeURIComponent(id)}/transition`,
      input,
    ),
}

/** Tenant-scoped query keys (query.ts contract: tenant id in every key). */
export const catalogKeys = {
  all: (t: string | null) => ['catalog', t] as const,
  entries: (t: string | null, params?: unknown) =>
    params === undefined
      ? (['catalog', t, 'entries'] as const)
      : (['catalog', t, 'entries', params] as const),
  entry: (t: string | null, id: string) => ['catalog', t, 'entry', id] as const,
  verify: (t: string | null, id: string) =>
    ['catalog', t, 'entry', id, 'verify'] as const,
  admissionPolicy: (t: string | null, kind: AdmissionKind) =>
    ['catalog', t, 'admission-policy', kind] as const,
  admissions: (t: string | null, kind: AdmissionKind, entryRef: string) =>
    ['catalog', t, 'admissions', kind, entryRef] as const,
  pubkey: (t: string | null) => ['catalog', t, 'pubkey'] as const,
  instances: (t: string | null, params?: unknown) =>
    params === undefined
      ? (['catalog', t, 'instances'] as const)
      : (['catalog', t, 'instances', params] as const),
  instance: (t: string | null, id: string) =>
    ['catalog', t, 'instance', id] as const,
}
