// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// View-local types for the Audit / Evidence Explorer. The wire shapes
// (AuditEventDTO, AuditVerifyResponse, AuditPubkeyResponse) live in lib/api/types
// because the ledger is a CORE surface (/v1/audit), not a /v1/m module; this file
// only adds the small UI vocabulary the view owns.

/** The ledger export formats the engine streams (core/audit Format). cef/leef/
 * syslog are line text; otlp (a postable export request), its exact alias
 * otlp_envelope, the bare otlp_log_record projection and ocsf are NDJSON
 * object-per-line. Mirrors audit.Formats() — the engine derives every list from
 * the sdk/siemwire catalog, so keep this in the same order if it gains a
 * format (the export-format test pins it against the OpenAPI snapshot). */
export type ExportFormat =
  | 'cef'
  | 'leef'
  | 'syslog'
  | 'otlp'
  | 'otlp_envelope'
  | 'otlp_log_record'
  | 'ocsf'

export const EXPORT_FORMATS: readonly ExportFormat[] = [
  'cef',
  'leef',
  'syslog',
  'otlp',
  'otlp_envelope',
  'otlp_log_record',
  'ocsf',
] as const

/** Which chain the explorer is reading: the active tenant's ledger, or the
 * system-tenant auth-partition chain (superadmin only; list-read only — the
 * engine exposes no verify/export route for it). */
export type LedgerScope = 'tenant' | 'system'

/** Server-side audit filters shared by list and export. Empty values are omitted
 * before reaching either endpoint. */
export interface AuditFilters {
  since?: string
  until?: string
  actor?: string
  action?: string
  target_kind?: string
  target_id?: string
  q?: string
}

/** Options accepted by the streamed tenant-ledger export. */
export interface AuditExportOptions extends AuditFilters {
  from?: number
  to?: number
}
