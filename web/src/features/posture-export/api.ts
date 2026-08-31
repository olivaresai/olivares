// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Posture-export console API. Presentation over the SINGLE route the
// posture-export module (modules/posture-export) already serves:
//   GET /v1/m/posture/export?severity&category&kind → the JSON posture projection
// The engine returns a minimal-data JSON document (inventory/drift/findings, refs +
// hashes only) that a control tower pulls; the export is audited server-side
// (action posture.export). There is NO server-side format switch, destination push,
// or export-history endpoint — so this client offers exactly what the backend does
// (filters + download of the verbatim bytes) and is honest about the rest: "history"
// is the tamper-evident audit ledger, reached from the Audit Explorer. The stream is
// JSON but the raw same-origin fetch preserves the engine's exact bytes for download
// (no client recompute) while also parsing them for the on-screen summary.
import { ensureFreshSession, notifyUnauthorized } from '@/lib/api/client'
import { ApiError, NetworkError } from '@/lib/api/errors'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'

const BASE = '/v1/m/posture'

/** The severity floor the export filter offers (mirrors model.Severity + the
 * handler's validation; empty = no floor). */
export type SeverityFloor = '' | 'low' | 'medium' | 'high' | 'critical'

/** The export filters the backend accepts (postureexport.handleExport). */
export interface PostureExportParams {
  severity?: SeverityFloor
  /** matches a finding kind OR subject_kind (there is no category column). */
  category?: string
  /** an inventory entity kind. */
  kind?: string
}

/** The least-privilege drift block (driftSummary). Counts are exact; the edges are
 * bounded and flagged truncated when the cap is hit. */
export interface PostureDrift {
  unexpected_count: number
  unused_grant_count: number
  inventory_grant_count: number
  truncated: boolean
}

/** The posture projection the export returns (exportDocument). Only the fields the
 * summary needs are typed; the rest ride through verbatim in the downloaded blob. */
export interface PostureExportDoc {
  tenant: string
  note: string
  inventory: unknown[]
  inventory_truncated: boolean
  posture_drift: PostureDrift
  findings: unknown[]
  findings_truncated: boolean
}

/** A fetched export: the parsed document (for the summary) + the engine's verbatim
 * bytes (for the download) + the content type it declared. */
export interface FetchedPostureExport {
  doc: PostureExportDoc
  blob: Blob
}

/** Fetch the posture export. Raw same-origin fetch preserving the engine's exact
 * bytes; carries the same bearer/tenant headers the JSON client injects and surfaces
 * a typed ApiError on 4xx/5xx (400 = an invalid severity floor, named honestly). */
export async function fetchPostureExport(
  params: PostureExportParams,
): Promise<FetchedPostureExport> {
  const search = new URLSearchParams()
  if (params.severity) search.set('severity', params.severity)
  if (params.category?.trim()) search.set('category', params.category.trim())
  if (params.kind?.trim()) search.set('kind', params.kind.trim())
  const qs = search.toString()

  // La renovación va ANTES de la petición: este camino rodea `apiFetch`, así que sin esto
  // sería el único del console que sigue muriendo por caducidad. Comparte el vuelo único.
  // ⛔ EL INQUILINO SE LEE ANTES DE LA ESPERA. Este camino rodea `apiFetch`, así que no
  //    hereda la fijación de `apiFetchWithMeta`: si se leyera después del refresco, un
  //    cambio de inquilino durante la renovación mandaría la petición al inquilino nuevo.
  const tenant = useTenantStore.getState().activeTenant
  await ensureFreshSession()
  const headers = new Headers({ Accept: 'application/json' })
  const token = useSessionStore.getState().token
  if (token) headers.set('Authorization', `Bearer ${token}`)
  if (tenant) headers.set('X-Olivares-Tenant', tenant)

  let res: Response
  try {
    res = await fetch(`${BASE}/export${qs ? `?${qs}` : ''}`, {
      method: 'GET',
      headers,
      credentials: 'same-origin',
    })
  } catch (cause) {
    throw new NetworkError('The control plane is unreachable.', cause)
  }
  const text = await res.text()
  if (!res.ok) {
    if (res.status === 401) notifyUnauthorized()
    let message = res.statusText || 'Posture export failed'
    try {
      const body = JSON.parse(text) as { error?: string | { message?: string } }
      if (typeof body?.error === 'string') message = body.error
      else if (body?.error?.message) message = body.error.message
    } catch {
      // keep the status text
    }
    throw new ApiError(
      res.status,
      'posture_export_failed',
      message,
      res.headers.get('X-Request-ID') ?? undefined,
    )
  }
  const doc = JSON.parse(text) as PostureExportDoc
  return { doc, blob: new Blob([text], { type: 'application/json' }) }
}

/** Trigger a browser download of the fetched export blob (shared anchor pattern). */
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

/** A suggested filename for a saved posture export. */
export function postureExportFilename(): string {
  return `olivares-posture-${new Date().toISOString().slice(0, 10)}.json`
}
