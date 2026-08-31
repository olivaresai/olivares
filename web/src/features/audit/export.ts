// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Ledger export — a raw same-origin fetch + browser download. GET /v1/audit/export
// streams the EXACT engine bytes (cef|leef|syslog text, or
// otlp|otlp_envelope|otlp_log_record|ocsf NDJSON) carrying
// the chain-integrity fields and a completion terminator, so an external WORM/SIEM
// holds an independently verifiable copy. The shared JSON `http` client cannot consume
// a non-JSON stream, so this carries the SAME bearer/tenant headers the client injects
// (read from the stores, like configureApiClient does) and surfaces a typed ApiError
// on 4xx/5xx — a 403/404/5xx is handled honestly, never written as a corrupt file.
// It reimplements no server logic: the engine produces every byte; the web only saves
// what it received (ARCHITECTURE.md, docs/SECURITY-HARDENING.md).
import { ensureFreshSession, notifyUnauthorized } from '@/lib/api/client'
import { ApiError, NetworkError } from '@/lib/api/errors'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import type { AuditExportOptions, ExportFormat } from './types'

/** NDJSON formats stream one JSON object per line; the line formats are text. */
function isNdjson(format: ExportFormat): boolean {
  return (
    format === 'otlp' ||
    format === 'otlp_envelope' ||
    format === 'otlp_log_record' ||
    format === 'ocsf'
  )
}

/** Suggested filename for a saved export (extension reflects the projection). */
export function exportFilename(format: ExportFormat): string {
  return `olivares-audit-${format}.${isNdjson(format) ? 'ndjson' : 'log'}`
}

/** Fetch the tenant ledger export as a Blob of the engine's verbatim bytes. */
export async function fetchAuditExport(
  format: ExportFormat,
  options: AuditExportOptions = {},
): Promise<Blob> {
  const { from = 1, to, ...filters } = options
  const search = new URLSearchParams({ format, from: String(from) })
  if (to !== undefined) search.set('to', String(to))
  for (const [key, value] of Object.entries(filters)) {
    if (value) search.set(key, value)
  }
  // La renovación va ANTES de la petición: este camino rodea `apiFetch`, así que sin esto
  // sería el único del console que sigue muriendo por caducidad. Comparte el vuelo único.
  // ⛔ EL INQUILINO SE LEE ANTES DE LA ESPERA. Este camino rodea `apiFetch`, así que no
  //    hereda la fijación de `apiFetchWithMeta`: si se leyera después del refresco, un
  //    cambio de inquilino durante la renovación mandaría la petición al inquilino nuevo.
  const tenant = useTenantStore.getState().activeTenant
  await ensureFreshSession()
  const headers = new Headers({
    Accept: isNdjson(format) ? 'application/x-ndjson' : 'text/plain',
  })
  const token = useSessionStore.getState().token
  if (token) headers.set('Authorization', `Bearer ${token}`)
  if (tenant) headers.set('X-Olivares-Tenant', tenant)

  let res: Response
  try {
    res = await fetch(`/v1/audit/export?${search.toString()}`, {
      method: 'GET',
      headers,
      credentials: 'same-origin',
    })
  } catch (cause) {
    throw new NetworkError('The control plane is unreachable.', cause)
  }
  if (!res.ok) {
    // A 401 means the session expired/was revoked — route to login like the JSON
    // client does (apiFetch's onUnauthorized hook), since this raw path skips it.
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

/** Trigger a browser download of a fetched export blob (shared anchor pattern). */
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
