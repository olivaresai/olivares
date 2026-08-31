// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// FinOps (module XI) endpoint wrappers + query keys. Thin `http.*` calls against the
// engine's `/v1/m/finops/…` routes (ARCHITECTURE.md — the web presents, never recomputes).
// Tenant-scoped keys include the active tenant so switching org refetches cleanly
//. Reads are RBAC-gated server-side; the UI mirrors that to hide actions.
//
// Every route here is a REAL backend route (modules/finops/api.go:APIRoutes). The
// FUTURE Anthropic dimensions/breakdowns the backend does not yet model (speed,
// service_account_id, account_id, advisor/thinking/programmatic) are NOT requested as
// live /spend slices — they live as a DECLARED reference catalog in ./schema and are
// rendered behind a SeamBadge, never faked as a queryable slice (§5).
import {
  http,
  ensureFreshSession,
  notifyUnauthorized,
  type TenantRequestOptions,
} from '@/lib/api/client'
import { ApiError, NetworkError } from '@/lib/api/errors'
import type { ListResponse } from '@/lib/api/types'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import type {
  Alert,
  AllocationResponse,
  Budget,
  BudgetInput,
  BudgetStatus,
  ChargebackStatement,
  ComparisonWithProjection,
  CostCenter,
  CostCenterMapping,
  EnhancedBudgetStatus,
  EnhancedForecastResponse,
  ExportProvenance,
  ForecastResponse,
  ModelRate,
  Recommendation,
  ReconciliationResponse,
  SpendDimension,
  SpendResponse,
  SummaryResponse,
  TrendResponse,
  Outcome,
  ValueBreakdown,
  ValueSummary,
} from './types'

const BASE = '/v1/m/finops'

/**
 * ⛔ EL TECHO SE PIDE, NO SE HEREDA. Sin `limit`, el motor sirve su página por defecto —100 filas
 *    (`core/internal/store/sqlstore/generic.go`)— y estas siete listas se pintaban recortadas SIN
 *    decirlo: ninguna vista miraba `has_more`. En finops eso no es una molestia de paginación, es
 *    dinero: un presupuesto, un centro de coste o una tarifa que no aparece se lee como que no
 *    existe. 1000 es el máximo que el motor acepta; por encima lo recorta él.
 */
const FINOPS_PAGE = 1000

export interface RangeParams {
  since?: string
  until?: string
}

export interface ExportParams extends RangeParams {
  provenance: ExportProvenance
}

/**
 * Fetch the FOCUS export as a CSV Blob. The /spend/export route returns `text/csv`
 * (FOCUS v1.3), NOT JSON, so the shared JSON `http` client cannot consume it — this
 * uses a raw same-origin fetch with the SAME auth/tenant headers the client injects
 * (read from the stores, like configureApiClient does). It reimplements no server
 * logic; it only carries the bearer/tenant and surfaces a typed error so a 404/403/5xx
 * is handled honestly by the caller (never a corrupt download). EffectiveCost is
 * sum-safe (estimated rows omit it in the mixed export) — the UI never recomputes
 * totals from the CSV.
 */
export async function fetchFocusExport(params: ExportParams): Promise<Blob> {
  const search = new URLSearchParams({
    format: 'focus',
    provenance: params.provenance,
  })
  if (params.since) search.set('since', params.since)
  if (params.until) search.set('until', params.until)

  // La renovación va ANTES de la petición: este camino rodea `apiFetch`, así que sin esto
  // sería el único del console que sigue muriendo por caducidad. Comparte el vuelo único.
  // ⛔ EL INQUILINO SE LEE ANTES DE LA ESPERA. Este camino rodea `apiFetch`, así que no
  //    hereda la fijación de `apiFetchWithMeta`: si se leyera después del refresco, un
  //    cambio de inquilino durante la renovación mandaría la petición al inquilino nuevo.
  const tenant = useTenantStore.getState().activeTenant
  await ensureFreshSession()
  const headers = new Headers({ Accept: 'text/csv' })
  const token = useSessionStore.getState().token
  if (token) headers.set('Authorization', `Bearer ${token}`)
  if (tenant) headers.set('X-Olivares-Tenant', tenant)

  let res: Response
  try {
    res = await fetch(`${BASE}/spend/export?${search.toString()}`, {
      method: 'GET',
      headers,
      credentials: 'same-origin',
    })
  } catch (cause) {
    throw new NetworkError('The control plane is unreachable.', cause)
  }
  if (!res.ok) {
    // ⛔ ESTE CAMINO NO TENÍA GANCHO DE 401, y su propio comentario dice que lleva «las MISMAS
    //    cabeceras de auth/tenant que el cliente inyecta». Llevaba las cabeceras y no la
    //    consecuencia: una sesión caída durante una descarga salía como un error genérico y la
    //    sesión NUNCA se limpiaba, así que el operador se quedaba con una consola que ya no
    //    autenticaba y ninguna pantalla se lo decía.
    if (res.status === 401) notifyUnauthorized()
    throw new ApiError(
      res.status,
      'export_failed',
      res.statusText || 'Export failed',
      res.headers.get('X-Request-ID') ?? undefined,
    )
  }
  return res.blob()
}

export const finopsApi = {
  summary: (params?: RangeParams) =>
    http.get<SummaryResponse>(`${BASE}/spend/summary`, {
      query: { ...params },
    }),
  trend: (params?: RangeParams) =>
    http.get<TrendResponse>(`${BASE}/spend/trend`, { query: { ...params } }),
  spend: (dimension: SpendDimension, params?: RangeParams) =>
    http.get<SpendResponse>(`${BASE}/spend`, {
      query: { dimension, ...params },
    }),
  reconciliation: (params?: RangeParams) =>
    http.get<ReconciliationResponse>(`${BASE}/spend/reconciliation`, {
      query: { ...params },
    }),
  allocation: (params?: RangeParams) =>
    http.get<AllocationResponse>(`${BASE}/spend/allocation`, {
      query: { ...params },
    }),
  forecast: (period: string) =>
    http.get<ForecastResponse>(`${BASE}/forecast`, { query: { period } }),
  recommendations: () =>
    http.get<{ recommendations: Recommendation[] }>(`${BASE}/recommendations`),
  // ── C07-04 · las doce rutas de finops que la consola nunca llamaba ─────────────────
  //
  // 42 rutas en `modules/finops/api.go:52-113`, 30 llamadas aquí. Entre las doce que faltaban
  // está el **panel del CFO** entero — gasto frente a resultados y la lista de riesgo de
  // cancelación — y una **asimetría** que se repite tres veces: la consola envuelve la LISTA,
  // el PUT y el DELETE de un recurso, pero no el GET de UNO. Se puede editar y borrar algo que
  // no se puede abrir.

  /** El panel del CFO: totales de gasto frente a resultados, coste por resultado y la lista
   *  de riesgo de cancelación (consumo sin resultados satisfechos). */
  valueSummary: (params?: {
    dimension?: string
    since?: string
    until?: string
  }) =>
    http.get<ValueSummary>(`${BASE}/value/summary`, { query: { ...params } }),
  /** El desglose coste-por-resultado por dimensión. Ojo: `total_cost_micro_usd` incluye lo
   *  NO atribuido; sumar los cubos y llamarlo «el gasto» infra-declara la factura. */
  value: (params?: { dimension?: string; since?: string; until?: string }) =>
    http.get<ValueBreakdown>(`${BASE}/value`, { query: { ...params } }),
  /** La evidencia sobre la que se calcula todo lo anterior. */
  outcomes: (
    params: { subject_kind?: string; subject_ref?: string } | undefined,
    request: TenantRequestOptions,
  ) =>
    http.get<ListResponse<Outcome>>(`${BASE}/outcomes`, {
      query: { limit: FINOPS_PAGE, ...params },
      ...request,
    }),
  ingestOutcome: (body: Partial<Outcome>, request: TenantRequestOptions) =>
    http.post<Outcome>(`${BASE}/outcomes`, body, request),

  /** Gasto unificado entre superficies: recorre el flujo COMPLETO de coste, estimado y
   *  facturado, etiquetando cada tramo. */
  spendUnified: (params?: { since?: string; until?: string }) =>
    http.get<unknown>(`${BASE}/spend/unified`, { query: { ...params } }),
  /** Ingesta HTTP de una muestra de coste, por el mismo camino que el colector. */
  ingestCost: (body: unknown) => http.post<unknown>(`${BASE}/cost`, body),

  /** Snapshot de asientos de un proveedor para un día UTC. */
  upsertSeats: (body: unknown) => http.post<unknown>(`${BASE}/seats`, body),
  /** Utilización de asientos: cruza asignados contra actores realmente activos. Es la
   *  medición que dice si se paga por asientos que nadie usa. */
  seatUtilization: (
    params: {
      provider?: string
      from?: string
      to?: string
    },
    request: TenantRequestOptions,
  ) =>
    http.get<unknown>(`${BASE}/seats/utilization`, {
      query: { ...params },
      ...request,
    }),

  /** Las tres lecturas de UNO que faltaban, y que la consola ya sabía editar y borrar. */
  costCenter: (id: string) =>
    http.get<unknown>(`${BASE}/cost-centers/${encodeURIComponent(id)}`),
  modelRate: (id: string) =>
    http.get<unknown>(`${BASE}/model-rates/${encodeURIComponent(id)}`),
  budget: (id: string) =>
    http.get<Budget>(`${BASE}/budgets/${encodeURIComponent(id)}`),

  /** El extracto de chargeback en CSV: una línea por modelo/proveedor/agente. */

  budgets: () =>
    http.get<ListResponse<Budget>>(`${BASE}/budgets`, {
      query: { limit: FINOPS_PAGE },
    }),
  budgetStatus: (id: string) =>
    http.get<BudgetStatus>(`${BASE}/budgets/${id}/status`),
  alerts: (params?: { budget_id?: string; limit?: number }) =>
    http.get<ListResponse<Alert>>(`${BASE}/alerts`, {
      query: { limit: FINOPS_PAGE, ...params },
    }),
  createBudget: (body: BudgetInput) =>
    http.post<Budget>(`${BASE}/budgets`, body),
  // Budget lifecycle beyond create: the engine has always exposed
  // PUT/DELETE (modules/finops/api.go:handleUpdateBudget/handleDeleteBudget), but
  // the console only wired create — so a ceiling could never be edited or retired.
  updateBudget: (id: string, body: BudgetInput) =>
    http.put<Budget>(`${BASE}/budgets/${encodeURIComponent(id)}`, body),
  deleteBudget: (id: string) =>
    http.delete<void>(`${BASE}/budgets/${encodeURIComponent(id)}`),

  //cost centers
  costCenters: (
    params: { status?: string } | undefined,
    request: TenantRequestOptions,
  ) =>
    http.get<ListResponse<CostCenter>>(`${BASE}/cost-centers`, {
      query: { limit: FINOPS_PAGE, ...params },
      ...request,
    }),
  createCostCenter: (
    body: Partial<CostCenter>,
    request: TenantRequestOptions,
  ) => http.post<CostCenter>(`${BASE}/cost-centers`, body, request),
  updateCostCenter: (
    id: string,
    body: Partial<CostCenter>,
    request: TenantRequestOptions,
  ) => http.put<CostCenter>(`${BASE}/cost-centers/${id}`, body, request),
  deleteCostCenter: (id: string, request: TenantRequestOptions) =>
    http.delete(`${BASE}/cost-centers/${id}`, undefined, request),
  costCenterMappings: (ccId: string, request: TenantRequestOptions) =>
    http.get<ListResponse<CostCenterMapping>>(
      `${BASE}/cost-centers/${ccId}/mappings`,
      { ...request, query: { limit: FINOPS_PAGE } },
    ),
  createCostCenterMapping: (
    ccId: string,
    body: Partial<CostCenterMapping>,
    request: TenantRequestOptions,
  ) =>
    http.post<CostCenterMapping>(
      `${BASE}/cost-centers/${ccId}/mappings`,
      body,
      request,
    ),
  deleteCostCenterMapping: (
    ccId: string,
    mappingId: string,
    request: TenantRequestOptions,
  ) =>
    http.delete(
      `${BASE}/cost-centers/${ccId}/mappings/${mappingId}`,
      undefined,
      request,
    ),

  //model rate catalog
  modelRates: (
    params: { provider?: string; model?: string } | undefined,
    request: TenantRequestOptions,
  ) =>
    http.get<ListResponse<ModelRate>>(`${BASE}/model-rates`, {
      query: { limit: FINOPS_PAGE, ...params },
      ...request,
    }),
  createModelRate: (body: Partial<ModelRate>) =>
    http.post<ModelRate>(`${BASE}/model-rates`, body),
  updateModelRate: (id: string, body: Partial<ModelRate>) =>
    http.put<ModelRate>(`${BASE}/model-rates/${id}`, body),
  deleteModelRate: (id: string) => http.delete(`${BASE}/model-rates/${id}`),

  //model cost comparison
  comparison: (params: {
    source_model: string
    target_models: string
    since?: string
    until?: string
    dimension?: string
    dim_key?: string
    forecast_period?: string
    window_days?: number
  }) =>
    http.get<ComparisonWithProjection>(`${BASE}/comparison`, {
      query: { ...params },
    }),

  //chargeback statements
  generateStatements: (
    body: { period: string; period_start: string },
    request: TenantRequestOptions,
  ) =>
    http.post<{ statements: ChargebackStatement[] }>(
      `${BASE}/statements/generate`,
      body,
      request,
    ),
  statements: (
    params:
      | {
          cost_center_id?: string
          period?: string
          status?: string
        }
      | undefined,
    request: TenantRequestOptions,
  ) =>
    http.get<ListResponse<ChargebackStatement>>(`${BASE}/statements`, {
      query: { limit: FINOPS_PAGE, ...params },
      ...request,
    }),
  statement: (id: string, request: TenantRequestOptions) =>
    http.get<ChargebackStatement>(`${BASE}/statements/${id}`, request),

  //enhanced forecast with per-dimension
  forecastEnhanced: (period: string, dimension?: string, windowDays?: number) =>
    http.get<EnhancedForecastResponse>(`${BASE}/forecast`, {
      query: { period, dimension, window_days: windowDays },
    }),

  //enhanced budget status with exhaustion
  budgetStatusEnhanced: (id: string) =>
    http.get<EnhancedBudgetStatus>(`${BASE}/budgets/${id}/status`),
}

export const finopsKeys = {
  all: (tenant: string | null) => ['finops', tenant] as const,
  summary: (tenant: string | null, params?: unknown) =>
    params === undefined
      ? (['finops', tenant, 'summary'] as const)
      : (['finops', tenant, 'summary', params] as const),
  trend: (tenant: string | null, params?: unknown) =>
    params === undefined
      ? (['finops', tenant, 'trend'] as const)
      : (['finops', tenant, 'trend', params] as const),
  spend: (tenant: string | null, dimension: string, params?: unknown) =>
    params === undefined
      ? (['finops', tenant, 'spend', dimension] as const)
      : (['finops', tenant, 'spend', dimension, params] as const),
  reconciliation: (tenant: string | null, params?: unknown) =>
    params === undefined
      ? (['finops', tenant, 'reconciliation'] as const)
      : (['finops', tenant, 'reconciliation', params] as const),
  allocation: (tenant: string | null, params?: unknown) =>
    params === undefined
      ? (['finops', tenant, 'allocation'] as const)
      : (['finops', tenant, 'allocation', params] as const),
  forecast: (tenant: string | null, period: string) =>
    ['finops', tenant, 'forecast', period] as const,
  recommendations: (tenant: string | null) =>
    ['finops', tenant, 'recommendations'] as const,
  budgets: (tenant: string | null) => ['finops', tenant, 'budgets'] as const,
  budgetStatus: (tenant: string | null, id: string) =>
    ['finops', tenant, 'budgets', id, 'status'] as const,
  alerts: (tenant: string | null, params?: unknown) =>
    params === undefined
      ? (['finops', tenant, 'alerts'] as const)
      : (['finops', tenant, 'alerts', params] as const),
  //
  costCenters: (tenant: string | null) =>
    ['finops', tenant, 'cost-centers'] as const,
  costCenterMappings: (tenant: string | null, ccId: string) =>
    ['finops', tenant, 'cost-centers', ccId, 'mappings'] as const,
  modelRates: (tenant: string | null, params?: unknown) =>
    params === undefined
      ? (['finops', tenant, 'model-rates'] as const)
      : (['finops', tenant, 'model-rates', params] as const),
  comparison: (tenant: string | null, params?: unknown) =>
    params === undefined
      ? (['finops', tenant, 'comparison'] as const)
      : (['finops', tenant, 'comparison', params] as const),
  statements: (tenant: string | null, params?: unknown) =>
    params === undefined
      ? (['finops', tenant, 'statements'] as const)
      : (['finops', tenant, 'statements', params] as const),
  statement: (tenant: string | null, id: string) =>
    ['finops', tenant, 'statements', id] as const,
  outcomes: (tenant: string | null) => ['finops', tenant, 'outcomes'] as const,
  seatUtilization: (tenant: string | null, provider: string) =>
    ['finops', tenant, 'seats', 'utilization', provider] as const,
}

/**
 * ⛔ EL EXTRACTO DE REPARTO ES CSV, NO JSON, y así devolvía `undefined` en el ÉXITO.
 *    `modules/finops/statements.go`, `handleExportStatement`, escribe
 *    `Content-Type: text/csv` y emite filas con un `csv.Writer`. El cliente compartido hace
 *    `JSON.parse(text)` dentro de un `catch` que deja `parsed = undefined`
 *    (`lib/api/client.ts:154-164`), así que `exportStatement` descartaba el extracto entero y
 *    devolvía nada — declarándolo además como `string`, que era el tipo correcto para un cuerpo
 *    que nunca llegaba.
 *
 *    Nunca se notó porque no tenía llamante. Mismo patrón que `fetchFocusExport`, que ya vive en
 *    este fichero para el otro CSV de este módulo.
 *
 * ⚠ Y UNA PRECISIÓN QUE ME COSTÓ CASI ROMPER CÓDIGO SANO: un handler que escribe un Content-Type
 *    no-JSON **de forma CONDICIONAL no es un endpoint no-JSON**. `handleModelCard` sólo emite
 *    markdown con `?format=md` y por defecto hace `writeJSON`, así que su cliente estaba bien y
 *    rreglarlo\ lo habría roto — con dos llamantes vivos detrás. Éste no tiene rama: su único
 *    `writeJSON` es el 400 de id inválido.
 */
export async function fetchStatementExport(id: string): Promise<Blob> {
  // La renovación va ANTES de la petición: este camino rodea `apiFetch`, así que sin esto
  // sería el único del console que sigue muriendo por caducidad. Comparte el vuelo único.
  // ⛔ EL INQUILINO SE LEE ANTES DE LA ESPERA. Este camino rodea `apiFetch`, así que no
  //    hereda la fijación de `apiFetchWithMeta`: si se leyera después del refresco, un
  //    cambio de inquilino durante la renovación mandaría la petición al inquilino nuevo.
  const tenant = useTenantStore.getState().activeTenant
  await ensureFreshSession()
  const headers = new Headers({ Accept: 'text/csv' })
  const token = useSessionStore.getState().token
  if (token) headers.set('Authorization', `Bearer ${token}`)
  if (tenant) headers.set('X-Olivares-Tenant', tenant)

  let res: Response
  try {
    res = await fetch(`${BASE}/statements/${encodeURIComponent(id)}/export`, {
      method: 'GET',
      headers,
      credentials: 'same-origin',
    })
  } catch (cause) {
    throw new NetworkError('The control plane is unreachable.', cause)
  }
  if (!res.ok) {
    throw new ApiError(
      res.status,
      'statement_export_failed',
      res.statusText || 'Export failed',
      res.headers.get('X-Request-ID') ?? undefined,
    )
  }
  return res.blob()
}
