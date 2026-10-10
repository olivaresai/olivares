// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Ledger export — the shared client's raw download + browser save. GET /v1/audit/export
// streams the EXACT engine bytes (cef|leef|syslog text, or
// otlp|otlp_envelope|otlp_log_record|ocsf NDJSON) carrying
// the chain-integrity fields and a completion terminator, so an external WORM/SIEM
// holds an independently verifiable copy. A 403/404/5xx surfaces as a typed ApiError,
// never written as a corrupt file.
// It reimplements no server logic: the engine produces every byte; the web only saves
// what it received (ARCHITECTURE.md, docs/SECURITY-HARDENING.md).
import { apiFetchRaw } from '@/lib/api/client'
import type { AuditExportOptions, ExportFormat } from './types'

export { downloadBlob } from '@/lib/api/download'

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
  const res = await apiFetchRaw(`/v1/audit/export?${search.toString()}`, {
    headers: {
      Accept: isNdjson(format) ? 'application/x-ndjson' : 'text/plain',
    },
  })
  return res.blob()
}
