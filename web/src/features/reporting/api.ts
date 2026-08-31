// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Reporting console API. Presentation over the SAME module API the backend
// already exposes (ARCHITECTURE.md) — no logic here, zero invented endpoints:
//   GET  /v1/m/reporting/reports            → the report-type catalog (types.go)
//   GET  /v1/m/reporting/reports/{type}     → generate + stream one report (html|pdf)
//   GET  /v1/m/reporting/schedules          → scheduled reports (enterprise)
//   POST /v1/m/reporting/schedules          → create a schedule (enterprise)
//   DELETE /v1/m/reporting/schedules/{id}   → delete a schedule (enterprise)
// The schedule routes answer 501 in the community build (the scheduler seam is nil);
// the view renders an honest "enterprise capability" notice off `isEnterprisePending`
// rather than faking a scheduler. Report generation streams the engine's verbatim
// bytes (html or pdf), so it uses a raw same-origin fetch like the audit export — the
// JSON client cannot consume a non-JSON stream; PDF answers 501 when the renderer is
// not installed and the view says so honestly.
import { http } from '@/lib/api'
import {
  ensureFreshSession,
  notifyUnauthorized,
  type TenantRequestOptions,
} from '@/lib/api/client'
import { ApiError, NetworkError } from '@/lib/api/errors'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'

const BASE = '/v1/m/reporting'

/** The output formats a report type offers (mirrors reporting.Format). */
export type ReportFormat = 'html' | 'pdf'

/** One report type as the catalog endpoint surfaces it (reporting.ReportMeta). */
export interface ReportMeta {
  type: string
  title: string
  description: string
  formats: ReportFormat[]
}

/** The parameters a generation request accepts (reporting.parseReportParams). Empty
 * fields are omitted; the engine defaults the window to the last month. */
export interface GenerateParams {
  format: ReportFormat
  /** RFC3339 or YYYY-MM-DD; omit to accept the engine default (now-1month). */
  from?: string
  to?: string
  /** compliance-evidence only: filter by framework id. */
  framework?: string
  /** finops-report only: filter by team. */
  team?: string
  locale?: string
}

/** A scheduled report (enterprise) — reporting.ScheduleConfig.*/
export interface ScheduleConfig {
  id: string
  report_type: string
  format: ReportFormat
  cron: string
  framework?: string
  team?: string
  locale?: string
  enabled: boolean
}

/** True when an error is the honest "enterprise capability not wired" 501 the
 * community build returns for the scheduler routes.*/
export function isEnterprisePending(err: unknown): boolean {
  return err instanceof ApiError && err.status === 501
}

/** A fetched report ready to hand to the browser: the engine's verbatim bytes plus
 * the content type it declared and a suggested filename. */
export interface FetchedReport {
  blob: Blob
  contentType: string
  filename: string
}

function ext(format: ReportFormat): string {
  return format === 'pdf' ? 'pdf' : 'html'
}

/** Generate + fetch one report as a Blob of the engine's verbatim bytes. Raw
 * same-origin fetch (the JSON `http` client cannot consume the html/pdf stream);
 * carries the SAME bearer/tenant headers the client injects and surfaces a typed
 * ApiError on 4xx/5xx (a 501 = PDF renderer not installed, handled honestly). */
export async function fetchReport(
  type: string,
  params: GenerateParams,
): Promise<FetchedReport> {
  const search = new URLSearchParams({ format: params.format })
  if (params.from) search.set('from', params.from)
  if (params.to) search.set('to', params.to)
  if (params.framework) search.set('framework', params.framework)
  if (params.team) search.set('team', params.team)
  if (params.locale) search.set('locale', params.locale)

  // La renovación va ANTES de la petición: este camino rodea `apiFetch`, así que sin esto
  // sería el único del console que sigue muriendo por caducidad. Comparte el vuelo único.
  // ⛔ EL INQUILINO SE LEE ANTES DE LA ESPERA. Este camino rodea `apiFetch`, así que no
  //    hereda la fijación de `apiFetchWithMeta`: si se leyera después del refresco, un
  //    cambio de inquilino durante la renovación mandaría la petición al inquilino nuevo.
  const tenant = useTenantStore.getState().activeTenant
  await ensureFreshSession()
  const headers = new Headers({
    Accept: params.format === 'pdf' ? 'application/pdf' : 'text/html',
  })
  const token = useSessionStore.getState().token
  if (token) headers.set('Authorization', `Bearer ${token}`)
  if (tenant) headers.set('X-Olivares-Tenant', tenant)

  let res: Response
  try {
    res = await fetch(
      `${BASE}/reports/${encodeURIComponent(type)}?${search.toString()}`,
      { method: 'GET', headers, credentials: 'same-origin' },
    )
  } catch (cause) {
    throw new NetworkError('The control plane is unreachable.', cause)
  }
  if (!res.ok) {
    if (res.status === 401) notifyUnauthorized()
    // The engine answers JSON errors ({error:{code,message}}); surface its message.
    let message = res.statusText || 'Report generation failed'
    try {
      const body = (await res.json()) as { error?: { message?: string } }
      if (body?.error?.message) message = body.error.message
    } catch {
      // Non-JSON error body — keep the status text.
    }
    throw new ApiError(
      res.status,
      'report_failed',
      message,
      res.headers.get('X-Request-ID') ?? undefined,
    )
  }
  const contentType =
    res.headers.get('Content-Type') ?? 'application/octet-stream'
  const stamp = new Date().toISOString().slice(0, 10)
  return {
    blob: await res.blob(),
    contentType,
    filename: `olivares-${type}-${stamp}.${ext(params.format)}`,
  }
}

/** Trigger a browser download of a fetched blob (shared anchor pattern). */
export function downloadBlob(blob: Blob, filename: string): void {
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  document.body.appendChild(a)
  a.click()
  a.remove()
  URL.revokeObjectURL(url)
}

export const reportingApi = {
  /** The report-type catalog (always available; the five built-in reports). */
  listReports: () => http.get<{ items: ReportMeta[] }>(`${BASE}/reports`),
  /** Scheduled reports. 501 in the community build → isEnterprisePending.*/
  listSchedules: (options: TenantRequestOptions) =>
    http.get<{ items: ScheduleConfig[] }>(`${BASE}/schedules`, options),
  createSchedule: (
    cfg: Omit<ScheduleConfig, 'id'>,
    options: TenantRequestOptions,
  ) =>
    http.post<{ items: ScheduleConfig[] }>(`${BASE}/schedules`, cfg, options),
  // ── C07-04 · las diez rutas de reporting que la consola nunca llamaba ───────────────
  //
  // Medido el 2026-08-17: `modules/reporting` registra 15 rutas (api.go:29-30 más las 13 de
  // enterprise.go:89-107) y el cliente llamaba 5. Entre las diez que faltaban está **el
  // paquete de evidencia de auditoría firmado — lo que se entrega a un auditor externo** —,
  // que no tenía ninguna superficie.
  //
  // ⚠ LOS TRES INFORMES ENTERPRISE CONTESTAN 501 CON EL MOTOR SIN CABLEAR
  // (`enterprise.go:104-106`, `writeNotWired`), y un add-on caducado llega como su propio
  // error, NO como un 500 (`enterprise.go:141-148` lo dice con todas las letras: «a customer
  // who had paid for everything except this add-on was told the server was broken»). Quien
  // consuma esto tiene que distinguir las dos cosas con `isOpenCoreSeam`, o la consola pinta
  // de rojo la frontera comercial y enseña a desconfiar de los errores de verdad.

  /** Postura de cumplimiento en vivo: estado por framework y control. */
  enterprisePosture: () => http.get<unknown>(`${BASE}/enterprise/posture`),
  /** El resumen ejecutivo de riesgo — el informe de una página para dirección. */
  enterpriseRisk: () => http.get<unknown>(`${BASE}/enterprise/risk`),
  /** El paquete de evidencia de auditoría FIRMADO: lo que se entrega a un auditor. */
  enterpriseBundle: () => http.get<unknown>(`${BASE}/enterprise/bundle`),

  /** Historial de ejecuciones de un informe programado: cuándo corrió, y si falló, por qué.
   *  Sin esto, una programación que lleva semanas fallando se ve igual que una que va bien. */
  scheduleRuns: (id: string, options: TenantRequestOptions) =>
    http.get<{ items: unknown[] }>(
      `${BASE}/schedules/${encodeURIComponent(id)}/runs`,
      options,
    ),
  /** El artefacto que produjo UNA ejecución concreta. */
  scheduleRun: (id: string, rid: string) =>
    http.get<unknown>(
      `${BASE}/schedules/${encodeURIComponent(id)}/runs/${encodeURIComponent(rid)}`,
    ),

  /** La marca del tenant que se aplica a TODOS los informes. */
  branding: () => http.get<unknown>(`${BASE}/branding`),
  setBranding: (cfg: unknown) => http.put<unknown>(`${BASE}/branding`, cfg),

  /** La plantilla HTML propia de un tipo de informe. 404 significa «usa la integrada», que
   *  NO es un error: es la respuesta normal para un tipo sin personalizar. */
  /**
   * ⛔ LA PLANTILLA SE GUARDA EN CRUDO, Y ASÍ ESTABA CORROMPIÉNDOSE. El motor la lee con
   *    `io.ReadAll(r.Body)` y persiste **esos bytes** (`modules/reporting/enterprise.go`,
   *    `handleSetTemplate`). Pasarla como `body` hace que el cliente le aplique `JSON.stringify`
   *    (`lib/api/client.ts:139`), así que lo almacenado era la cadena JSON —entrecomillada y con
   *    `\n` y `\"` escapados—, no el HTML. El informe con la marca del cliente habría salido con
   *    esa basura dentro.
   *
   *    `putRaw` es el mecanismo que este cliente ya tiene para «raw-bytes endpoints», y el
   *    `Content-Type` va con él para que el motor no tenga que adivinar.
   */
  setTemplate: (type: string, html: string) =>
    http.putRaw<unknown>(
      `${BASE}/templates/${encodeURIComponent(type)}`,
      html,
      { contentType: 'text/html; charset=utf-8' },
    ),
  deleteTemplate: (type: string) =>
    http.delete<unknown>(`${BASE}/templates/${encodeURIComponent(type)}`),

  deleteSchedule: (id: string, options: TenantRequestOptions) =>
    http.delete<{ deleted: boolean }>(
      `${BASE}/schedules/${encodeURIComponent(id)}`,
      undefined,
      options,
    ),
}

export const reportingKeys = {
  reports: () => ['reporting', 'reports'] as const,
  schedules: (t: string | null) => ['reporting', t, 'schedules'] as const,
  scheduleRuns: (t: string | null, id: string) =>
    ['reporting', t, 'schedules', id, 'runs'] as const,
}

/**
 * ⛔ LA PLANTILLA SE LEE COMO HTML, NO COMO JSON, y por eso no puede ir por `http.get`.
 *    `handleGetTemplate` escribe `Content-Type: text/html` y devuelve el HTML tal cual
 *    (`modules/reporting/enterprise.go`). El cliente compartido hace `JSON.parse(text)` dentro de
 *    un `catch` que deja `parsed = undefined` (`lib/api/client.ts:154-164`), así que
 *    `reportingApi.template` devolvía **`undefined` en el caso de ÉXITO** — la segunda vez hoy que
 *    aparece esta trampa, tras `knowledgeApi.exportMemory`.
 *
 *    Nunca se notó porque ninguna pantalla lo llamaba: es exactamente lo que el trinquete de
 *    llamantes existe para destapar — un cliente sin llamante no se ha ejercido contra una
 *    respuesta real ni una sola vez.
 *
 * ⛔ Y EL 404 AQUÍ NO ES UN ERROR: significa «este tipo de informe no tiene plantilla
 *    personalizada», que es el estado normal. Se devuelve `null` para que la pantalla lo distinga
 *    de un fallo.
 */
export async function fetchReportTemplate(
  type: string,
): Promise<string | null> {
  // La renovación va ANTES de la petición: este camino rodea `apiFetch`, así que sin esto
  // sería el único del console que sigue muriendo por caducidad. Comparte el vuelo único.
  // ⛔ EL INQUILINO SE LEE ANTES DE LA ESPERA. Este camino rodea `apiFetch`, así que no
  //    hereda la fijación de `apiFetchWithMeta`: si se leyera después del refresco, un
  //    cambio de inquilino durante la renovación mandaría la petición al inquilino nuevo.
  const tenant = useTenantStore.getState().activeTenant
  await ensureFreshSession()
  const headers = new Headers({ Accept: 'text/html' })
  const token = useSessionStore.getState().token
  if (token) headers.set('Authorization', `Bearer ${token}`)
  if (tenant) headers.set('X-Olivares-Tenant', tenant)

  let res: Response
  try {
    res = await fetch(`${BASE}/templates/${encodeURIComponent(type)}`, {
      method: 'GET',
      headers,
      credentials: 'same-origin',
    })
  } catch (cause) {
    throw new NetworkError('The control plane is unreachable.', cause)
  }
  // Sin plantilla personalizada NO es un fallo: es el estado por defecto.
  if (res.status === 404) return null
  if (!res.ok) {
    throw new ApiError(
      res.status,
      'template_read_failed',
      res.statusText || 'Failed to read template',
      res.headers.get('X-Request-ID') ?? undefined,
    )
  }
  return res.text()
}
