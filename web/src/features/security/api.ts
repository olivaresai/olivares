// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Security (module IX) endpoint wrappers + query keys. Thin `http.*` calls against the
// engine's `/v1/m/security/…` routes (the web presents tamper-evident evidence — it
// never recomputes it). Tenant-scoped keys include the active tenant so switching org
// refetches cleanly. Privileged reads (anomalies, case timelines, integrity verify)
// are self-audited server-side; the UI surfaces that with a SelfAuditNotice.
import { http } from '@/lib/api/client'
import { ApiError, NetworkError, parseErrorEnvelope } from '@/lib/api/errors'
import type { ListResponse } from '@/lib/api/types'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import type { ExportFormat } from '@/features/audit/types'
import type {
  CaseLink,
  CaseLinkKind,
  CreateCaseInput,
  UpdateCaseInput,
  AnomaliesResponse,
  CaseTimeline,
  EnforcementInput,
  EnforcementEntry,
  EnforcementResponse,
  Finding,
  FindingFilters,
  FindingsExportResult,
  FindingTriageInput,
  ForensicCase,
  InspectInput,
  InspectResult,
  IntegrityVerify,
  SafetyPostureView,
} from './types'

const BASE = '/v1/m/security'

/** fetchFindingsExport uses a RAW fetch (not `http.*`) for the same reason the
 *  compliance evidence export does: the file a consumer ingests must be the
 *  server's EXACT bytes, never re-serialized by the client — a SARIF run that
 *  the browser re-encoded is no longer the artifact the server signed off on.
 *  It also needs two things off the response the JSON client discards: the
 *  filename the server suggests, and the honest truncation header the export
 *  sets when the result cap is hit. It reuses the same token/tenant the client
 *  is wired to, so RBAC and the server-side export self-audit still apply. */
async function fetchFindingsExport(
  filters?: FindingFilters,
): Promise<FindingsExportResult> {
  const token = useSessionStore.getState().token
  const tenant = useTenantStore.getState().activeTenant
  const headers = new Headers({ Accept: 'application/json' })
  if (token) headers.set('Authorization', `Bearer ${token}`)
  if (tenant) headers.set('X-Olivares-Tenant', tenant)

  const query = new URLSearchParams({ format: 'sarif' })
  for (const [key, value] of Object.entries(filters ?? {})) {
    if (value !== undefined && value !== null && value !== '') {
      query.set(key, String(value))
    }
  }

  let res: Response
  try {
    res = await fetch(`${BASE}/findings/export?${query.toString()}`, {
      method: 'GET',
      headers,
      credentials: 'same-origin',
    })
  } catch (cause) {
    throw new NetworkError('The control plane is unreachable.', cause)
  }

  const text = await res.text()
  if (!res.ok) {
    let parsed: unknown
    try {
      parsed = JSON.parse(text)
    } catch {
      parsed = undefined
    }
    const { code, message } = parseErrorEnvelope(parsed, res.statusText)
    throw new ApiError(
      res.status,
      code,
      message,
      res.headers.get('X-Request-ID') ?? undefined,
    )
  }

  // The server names the artifact (Content-Disposition); the console must not
  // invent a second name for the same file the CLI writes. Built via the RegExp
  // constructor, not a literal: the export scrubber's lexer has no regex concept,
  // and a literal with an odd number of double quotes desynchronizes its string
  // tracking for the rest of the file.
  const disposition = res.headers.get('Content-Disposition') ?? ''
  const suggested = new RegExp('filename="([^"]+)"', 'i').exec(disposition)?.[1]

  return {
    filename: suggested ?? 'olivares-findings.sarif',
    content_type: res.headers.get('Content-Type') ?? 'application/json',
    text,
    // The server sets this when the export hit its result cap: the file is
    // valid but partial, and saying so is the whole point of the header.
    truncated: res.headers.get('X-Olivares-Truncated') === 'true',
  }
}

export const securityApi = {
  // 1. Findings
  findings: (filters?: FindingFilters) =>
    http.get<ListResponse<Finding>>(`${BASE}/findings`, {
      query: { ...filters },
    }),
  finding: (id: string) => http.get<Finding>(`${BASE}/findings/${id}`),
  triageFinding: (id: string, body: FindingTriageInput) =>
    http.patch<Finding>(`${BASE}/findings/${id}`, body),
  // 1a. SARIF 2.1.0 export — the same filters as the list, the server's exact
  // bytes, and the truncation flag when the result cap was reached.
  exportFindings: (filters?: FindingFilters) => fetchFindingsExport(filters),

  // 1b. Safety posture — the read-first per-provider AI-safety view.
  safetyPosture: () => http.get<SafetyPostureView>(`${BASE}/safety-posture`),

  // 2. Guardrail inspect
  inspect: (body: InspectInput) =>
    http.post<InspectResult>(`${BASE}/guardrails/inspect`, body),

  // 3. Enforcement posture
  enforcement: () => http.get<EnforcementResponse>(`${BASE}/enforcement`),
  setEnforcement: (body: EnforcementInput) =>
    http.put<EnforcementEntry>(`${BASE}/enforcement`, body),

  // 4. Anomalies (privileged + self-audited)
  anomalies: () => http.get<AnomaliesResponse>(`${BASE}/anomalies`),

  // 5. Forensic cases + timeline
  // Mismo techo y misma razón que en findings: `handleListCases` publica `has_more`
  // (`modules/security/forensic.go:84`) y sin `limit` el motor pagina a 100.
  cases: (status?: string, limit?: number) =>
    http.get<ListResponse<ForensicCase>>(`${BASE}/cases`, {
      query: { status, limit },
    }),
  case: (id: string) => http.get<ForensicCase>(`${BASE}/cases/${id}`),

  /** Abre un caso. `security:case:write`. El motor fija `status: "open"` — no se manda. */
  createCase: (body: CreateCaseInput) =>
    http.post<ForensicCase>(`${BASE}/cases`, body),

  /** Edición PARCIAL. Se manda SOLO lo que cambia: el motor lee punteros, así que un campo
   *  ausente se conserva. Nunca se esparce el caso entero aquí. */
  updateCase: (id: string, body: UpdateCaseInput) =>
    http.patch<ForensicCase>(`${BASE}/cases/${encodeURIComponent(id)}`, body),

  /** La cadena de custodia del caso. `security:case:read`. */
  caseLinks: (id: string) =>
    http.get<ListResponse<CaseLink>>(
      `${BASE}/cases/${encodeURIComponent(id)}/links`,
    ),

  /** Añade un enlace. `security:case:write`. Un caso inexistente da 404. */
  linkCase: (
    id: string,
    body: { link_kind: CaseLinkKind; link_ref: string; note?: string },
  ) =>
    http.post<CaseLink>(`${BASE}/cases/${encodeURIComponent(id)}/links`, body),

  /** Exporta el caso en un formato del catálogo del ledger. `security:case:read`. */
  exportCase: (id: string, format: ExportFormat) =>
    http.get<unknown>(`${BASE}/cases/${encodeURIComponent(id)}/export`, {
      query: { format },
    }),
  caseTimeline: (id: string) =>
    http.get<CaseTimeline>(`${BASE}/cases/${id}/timeline`),

  // 6. Integrity verify (self-audited)
  integrity: () => http.get<IntegrityVerify>(`${BASE}/integrity/verify`),
}

export const securityKeys = {
  all: (tenant: string | null) => ['security', tenant] as const,
  // ⛔ SIN PARAMS, LA CLAVE TIENE TRES SEGMENTOS — Y NO ES ESTÉTICA. Antes se añadía
  //    `filters ?? null` SIEMPRE, así que `findings(t)` daba `[…,'findings',null]` y una
  //    consulta con params daba `[…,'findings',{limit:1000}]`: `null` **no es prefijo de**
  //    `{limit:1000}`, así que `invalidateQueries(findings(t))` NO alcanzaba a la consulta
  //    viva. Lo introduje yo al pasar el `limit` y lo devolvió el contraste, comprobado
  //    contra el TanStack instalado: `{"invalidated": false}`.
  //
  //    Consecuencia: el triaje de un hallazgo dejaba de refrescar su fila. Con tres
  //    segmentos, la clave base es PREFIJO de la filtrada y la invalidación por prefijo
  //    vuelve a alcanzarlas todas.
  findings: (tenant: string | null, filters?: unknown) =>
    filters === undefined
      ? (['security', tenant, 'findings'] as const)
      : (['security', tenant, 'findings', filters] as const),
  finding: (tenant: string | null, id: string) =>
    ['security', tenant, 'findings', id] as const,
  safetyPosture: (tenant: string | null) =>
    ['security', tenant, 'safety-posture'] as const,
  enforcement: (tenant: string | null) =>
    ['security', tenant, 'enforcement'] as const,
  anomalies: (tenant: string | null) =>
    ['security', tenant, 'anomalies'] as const,
  // Misma corrección que en `findings`: la clave base es prefijo de la filtrada, así que
  // el alta y la edición de un expediente vuelven a invalidar la lista viva.
  cases: (tenant: string | null, status?: unknown) =>
    status === undefined
      ? (['security', tenant, 'cases'] as const)
      : (['security', tenant, 'cases', status] as const),
  case: (tenant: string | null, id: string) =>
    ['security', tenant, 'cases', id] as const,
  caseLinks: (tenant: string | null, id: string) =>
    ['security', tenant, 'cases', id, 'links'] as const,
  caseTimeline: (tenant: string | null, id: string) =>
    ['security', tenant, 'cases', id, 'timeline'] as const,
  integrity: (tenant: string | null) =>
    ['security', tenant, 'integrity'] as const,
}
