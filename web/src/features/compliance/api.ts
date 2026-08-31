// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Compliance (module XIII) endpoint wrappers + query keys. Thin `http.*` calls
// against the engine's `/v1/m/compliance/…` routes (docs/UI-CONTRACT-COMPLIANCE.md —
// the web presents control status + evidence, it never recomputes a verdict). Reads
// are RBAC-gated server-side; the container mirrors that to hide privileged actions.
// Tenant-scoped keys include the active tenant so switching org refetches cleanly.
//
// ── POR QUE ESTAS LISTAS NO LLEVAN AVISO DE RECORTE (2026-08-27) ─────────────────────
//
// Se re-midio el motor —paso 1 de la receta de `scripts/check-list-truncation-witness.sh`— y el
// veredicto CONFIRMA el que ya estaba tomado en `./list-ceilings.test.ts`: caso (a), DRENA. Las
// sondas quedan escritas para que la siguiente sesion no vuelva a derivarlas:
//
//   · `modules/compliance` no tiene `listQuery`: NINGUN handler lee `?limit` ni `?cursor`
//     (0 apariciones de `URL.Query().Get("limit"|"cursor")` en el modulo).
//   · Las listas se sirven con `listAll` (helpers.go:150-165), que sigue el cursor hasta el final
//     — `Limit: listCap` es el tamano de CADA pagina, no un tope del total — y cuyo comentario
//     dice para que existe: «so an aggregation is never silently truncated». 56 llamadas.
//   · CERO `listResponse[…]` del modulo lleva `HasMore` o `Cursor`. El unico `page.HasMore` que
//     se devuelve es `pageCount` (helpers.go:184-191), usado dos veces en `capabilities.go` para
//     EVIDENCIA DE CAPACIDAD (`CapabilityEvidence.More`), no para paginar una respuesta.
//   · Y `/v1/m/compliance/*` no esta en `web/openapi/openapi.json`: el contrato embebido solo
//     describe las diez operaciones del nucleo, asi que aqui no hay contrato que declare recorte.
//
// ⛔ Y POR ESO EL AVISO NO VA, que es una decision con gate y no una omision. `./list-ceilings.
//    test.ts` nombra los DIECIOCHO handlers uno a uno y su segunda celda exige que NINGUNA vista
//    de compliance ate un `<ListTruncationBadge>` a una de estas listas. Un aviso que no puede
//    encenderse no protege: afirma. Y en compliance afirmar cobertura es exactamente el daño.
//
//    Lo intento al reves —puso el aviso en las 14 listas atadas a `useQuery`— y ese gate lo
//    refuto en la misma tanda. Se anota aqui en vez de borrarse: la proxima sesion que lea «pon el
//    aviso en las features del censo» va a llegar a esta misma idea, y esta linea le ahorra el
//    viaje. La feature se queda NOMBRADA en `docs/list-truncation-baseline.txt`, que es la salida
//    que la receta prescribe para el caso (a): documentar el handler, no fabricar un aviso.
//
//    Si algun dia una de estas rutas deja de drenar, lo que cambia PRIMERO es este fichero y su
//    trinquete — y solo entonces el aviso.
import { http } from '@/lib/api/client'
import { ApiError, NetworkError, parseErrorEnvelope } from '@/lib/api/errors'
import type { ListResponse } from '@/lib/api/types'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import type {
  HipaaGapReport,
  CapabilityCatalogResponse,
  ClaudeFileEraseResult,
  ClaudeFilesInventory,
  CcmDriftFinding,
  CcmSnapshot,
  CcmSnapshotInput,
  ComplianceSummaryResponse,
  CreateErasureInput,
  CreateHoldInput,
  DataClassListResponse,
  DataSubjectEraseInput,
  DataSubjectErasureStatus,
  AimsPack,
  DepthPack,
  FedrampKsiPack,
  DoraIncident,
  DoraRegister,
  ErasureEvent,
  ErasureReceipt,
  ErasureRequest,
  ErasureStatus,
  EvidenceExportFormat,
  EvidenceExportResult,
  EvidenceInput,
  EvidenceListResponse,
  EvidencePackage,
  ExecuteErasureInput,
  ExecuteErasureResult,
  FrameworkListResponse,
  FrameworkStatusResponse,
  GapAnalysisResponse,
  HoldDecision,
  HoldEvent,
  HoldStatus,
  LegalHold,
  Nis2Incident,
  Nis2UpdateInput,
  OscalExport,
  OscalProfile,
  RegulatoryCalendarResponse,
  ReleaseHoldInput,
  ReleaseHoldResult,
  ResidencyAttestation,
  ResidencyInput,
  ResidencyScanReport,
  RetentionPolicy,
  RetentionPolicyInput,
  RetentionPolicyPending,
  RetentionRun,
  RetentionSummary,
  RiskClassification,
  RiskClassifyInput,
  RiskReviewInput,
} from './types'

const BASE = '/v1/m/compliance'

/** Fetch an export as the server's EXACT bytes.
 *
 *  A RAW fetch (not `http.*`) because the engine answers text/csv for format=csv
 *  and the thin JSON client discards non-JSON bodies — an auditor export must be
 *  the server's exact bytes, never recomputed (ARCHITECTURE.md). It reuses the same
 *  token/tenant the client is wired to (app/providers.tsx) so auth and the
 *  server-side export self-audit (modules/compliance/evidence.go:241) still apply.
 *
 *  Shared by every export surface (evidence, DORA register, DORA incident report,
 *  depth packs): they all have the same "hand the auditor what the server sealed"
 *  contract, and duplicating the byte-exactness rule per surface is how one copy
 *  quietly starts recomputing. */
async function fetchRawExport(
  path: string,
  filename: string,
): Promise<{ filename: string; content_type: string; text: string }> {
  const token = useSessionStore.getState().token
  const tenant = useTenantStore.getState().activeTenant
  const headers = new Headers({ Accept: 'application/json, text/csv' })
  if (token) headers.set('Authorization', `Bearer ${token}`)
  if (tenant) headers.set('X-Olivares-Tenant', tenant)

  let res: Response
  try {
    res = await fetch(path, {
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
  return {
    filename,
    content_type: res.headers.get('Content-Type') ?? '',
    text,
  }
}

/** Evidence export — the byte-exact fetch above, plus the OSCAL parse the view
 *  needs to render honest finding status. */
async function fetchEvidenceExport(
  id: string,
  format: EvidenceExportFormat,
): Promise<EvidenceExportResult> {
  const token = useSessionStore.getState().token
  const tenant = useTenantStore.getState().activeTenant
  const headers = new Headers({ Accept: 'application/json, text/csv' })
  if (token) headers.set('Authorization', `Bearer ${token}`)
  if (tenant) headers.set('X-Olivares-Tenant', tenant)

  let res: Response
  try {
    res = await fetch(
      `${BASE}/evidence/${encodeURIComponent(id)}/export?format=${format}`,
      { method: 'GET', headers, credentials: 'same-origin' },
    )
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

  const contentType = res.headers.get('Content-Type') ?? ''
  const ext = format === 'oscal' ? 'oscal.json' : format
  const result: EvidenceExportResult = {
    format,
    filename: `evidence-${id}.${ext}`,
    content_type: contentType,
    text,
  }
  if (format === 'oscal') {
    // The bundle is JSON; parse it so the view can render OSCAL finding status without
    // recomputing it. A parse failure is a genuine error (the server promised JSON).
    result.oscal = JSON.parse(text) as OscalExport
  }
  return result
}

/** POST an operator-authored document the way these handlers actually read it:
 *  the body is the document's EXACT BYTES and every side input is a QUERY param.
 *
 *  Five writes on this module share that shape, and all five were measured before
 *  this helper was written:
 *
 *    DORA register           regpackage.go:261 (body) · :265 (reference_date)
 *    DORA incident classify  doraincident.go:109 (body) · :97,:108 (reference, finding_id)
 *    OSCAL ingestion         oscalprofile.go:249 (body) · :253,:254 (framework, scope_note)
 *    US state-law pack       depthhandlers.go:251 (body) · :256 (scope_note)
 *    Sector overlay pack     depthhandlers.go:530 (body) · :535 (scope_note)
 *
 *  Each reads the body with `readBoundedBody` (oscalprofile.go:493) and hashes
 *  exactly those bytes into `doc_sha256` (regpackage.go:268, doraincident.go:115,
 *  oscalprofile.go:277, depthhandlers.go:262, :541) BEFORE the add-on parses them.
 *
 *  IT IS ONE FUNCTION ON PURPOSE, and the reason is a measured defect, not taste.
 *  `http.post(path, <string>)` JSON-encodes its body argument (client.ts:139), so
 *  a document passed that way arrives re-quoted and escaped — `{"a":1}` goes out
 *  as `"{\"a\":1}"`. The engine would then hash a document the operator never
 *  wrote and hand the packager a JSON string where it expects an object: a
 *  minimal-data anchor attesting the wrong bytes, and it looks like success from
 *  every surface. Five hand-written copies of that rule are five chances to write
 *  the next one wrong; this campaign has already paid for the class twice (the
 *  tool-pinning payloads, fixed in #690).
 *
 *  A side input that is blank is OMITTED rather than sent empty. That is URL
 *  hygiene, NOT a persistence fix, and the difference is worth stating because the
 *  first version of this comment claimed the latter: `r.URL.Query().Get` returns ""
 *  for an absent key and for `?k=` alike, so the handler cannot tell them apart, and
 *  `nullableText` stores blank as NULL either way (evidence.go:450-455). Omitting
 *  keeps the request honest about what the operator supplied; it changes nothing
 *  downstream. Corrected after the the model contrast (F5).*/
function postDocument<T>(
  path: string,
  document: string,
  query?: Record<string, string | undefined>,
): Promise<T> {
  return http.post<T>(path, undefined, {
    rawBody: document,
    contentType: 'application/json',
    query,
  })
}

export const complianceApi = {
  // (1) catalog
  frameworks: () => http.get<FrameworkListResponse>(`${BASE}/frameworks`),
  // (2) per-framework status
  status: (id: string) =>
    http.get<FrameworkStatusResponse>(`${BASE}/frameworks/${id}/status`),
  // (3) gap analysis
  gaps: (id: string) =>
    http.get<GapAnalysisResponse>(`${BASE}/frameworks/${id}/gaps`),
  // (5) cross-framework roll-up (dashboard header)
  summary: () => http.get<ComplianceSummaryResponse>(`${BASE}/summary`),
  // (5b) the capability catalog with the tenant's LIVE evidence state — "what the
  //      platform can evidence right now". Served since `compliance.go:454` under
  //      `permFrameworkRead`, and until this wrapper the console never called it.
  capabilities: () =>
    http.get<CapabilityCatalogResponse>(`${BASE}/capabilities`),
  // (6) audit evidence
  evidence: (framework?: string) =>
    http.get<EvidenceListResponse>(`${BASE}/evidence`, {
      query: { framework },
    }),
  generateEvidence: (id: string, body: EvidenceInput) =>
    http.post<EvidencePackage>(`${BASE}/frameworks/${id}/evidence`, body),
  // (6a) export a sealed package as json | csv | oscal (FIN-10). LIVE GET; the export
  // self-audits server-side (gated on compliance:framework:read).
  exportEvidence: (id: string, format: EvidenceExportFormat) =>
    fetchEvidenceExport(id, format),
  // (7) agent risk register
  risk: () => http.get<ListResponse<RiskClassification>>(`${BASE}/risk`),
  classifyRisk: (body: RiskClassifyInput) =>
    http.post<RiskClassification>(`${BASE}/risk/classify`, body),
  reviewRisk: (id: string, body: RiskReviewInput) =>
    http.post<RiskClassification>(`${BASE}/risk/${id}/review`, body),
  // (8) data residency
  residency: () =>
    http.get<ListResponse<ResidencyAttestation>>(`${BASE}/residency`),
  attestResidency: (body: ResidencyInput) =>
    http.post<ResidencyAttestation>(`${BASE}/residency`, body),
  scanResidency: () => http.post<ResidencyScanReport>(`${BASE}/residency/scan`),

  // (9) the §2 data-class registry. Not decoration: a data_class-scope hold is
  // REJECTED unless the id matches the registry exactly (holds.go:309), so the
  // console offers the registry instead of a text box.
  dataClasses: () =>
    http.get<DataClassListResponse>(`${BASE}/retention/classes`),

  // (9-bis) retention schedules, sweeps and certificates —.
  // Five of the six routes at compliance.go:575-580; the sixth is `dataClasses`
  // above, which until now was this plane's ONLY console surface.
  retentionPolicies: () =>
    http.get<ListResponse<RetentionPolicy>>(`${BASE}/retention/policies`),
  /** Authoring a schedule. putWithMeta for the same reason the holds and erasure
   *  writes use postWithMeta, and here it is the difference between "the purge is
   *  on" and "the purge is off pending two humans":
   *
   *    200 → the schedule as persisted (read `enabled` off the DTO, never assume)
   *    202 → an approval was OPENED and the policy was persisted DISABLED
   *          (retention.go:304,315) — nothing will be destroyed yet
   *
   *  The thin client discards the status, so on `http.put` those two answers are
   *  the same object shape with different fields, and the 202 would render as a
   *  saved purge schedule. Every other outcome is an ApiError the view names:
   *  409 the approval expired, 503 no gate is wired (deny-closed), 403 rejected /
   *  plan-hash mismatch / no approver evidence, 422 under a regulatory floor. */
  putRetentionPolicy: (dataClass: string, body: RetentionPolicyInput) =>
    http.putWithMeta<RetentionPolicy | RetentionPolicyPending>(
      `${BASE}/retention/policies/${encodeURIComponent(dataClass)}`,
      body,
    ),
  /** Deleting a schedule STOPS a purge — the safe direction, and the engine gates
   *  it accordingly (no approval, retention.go:397-398). 204 on success; a 403 here
   *  is the compliance-mode seal, not a missing permission (retention.go:405).*/
  deleteRetentionPolicy: (dataClass: string) =>
    http.delete<void>(
      `${BASE}/retention/policies/${encodeURIComponent(dataClass)}`,
    ),
  /** Run the sweep NOW, attributed to the calling admin (retention.go:486). Takes
   *  no body: the worklist is the set of ENABLED purge schedules, so what it
   *  destroys is decided by the schedules, not by this call. */
  runRetentionSweep: () =>
    http.post<RetentionSummary>(`${BASE}/retention/sweep`),
  /** The sealed destruction certificates (retention.go:444). `class` filters
   *  server-side (`:446`). */
  retentionRuns: (dataClass?: string) =>
    http.get<ListResponse<RetentionRun>>(`${BASE}/retention/runs`, {
      query: { class: dataClass },
    }),

  // (10) legal holds — Preservation plane (E1).
  holds: (status?: HoldStatus) =>
    http.get<ListResponse<LegalHold>>(`${BASE}/holds`, { query: { status } }),
  hold: (id: string) =>
    http.get<LegalHold>(`${BASE}/holds/${encodeURIComponent(id)}`),
  holdEvents: (id: string) =>
    http.get<ListResponse<HoldEvent>>(
      `${BASE}/holds/${encodeURIComponent(id)}/events`,
    ),
  /** The §4 matching rule as a PREVIEW: what a hold already covers, asked before
   *  an operator confirms anything. Same rule the erasure path enforces. */
  checkHold: (q: {
    subject_kind?: string
    subject_ref?: string
    data_class?: string
  }) => http.get<HoldDecision>(`${BASE}/holds/check`, { query: q }),
  /** Uses postWithMeta for the same reason the governed writes do: the thin
   *  client discards the status, so a 202 — which on this route would mean the
   *  hold was NOT placed — is indistinguishable from the 201 the engine sends. */
  createHold: (body: CreateHoldInput) =>
    http.postWithMeta<LegalHold>(`${BASE}/holds`, body),
  /** Dual-control. A 202 means the hold is STILL ACTIVE; only a 200 returns the
   *  released hold. Uses postWithMeta because the STATUS is the contract here —
   *  see `incompleteReason`. */
  releaseHold: (id: string, body: ReleaseHoldInput) =>
    http.postWithMeta<LegalHold | ReleaseHoldResult>(
      `${BASE}/holds/${encodeURIComponent(id)}/release`,
      body,
    ),

  // (11) right to erasure / DSAR — (E2).
  /** Inventario del almacén de ficheros de Anthropic. Lectura: `compliance:erasure:read`. */
  claudeFiles: () => http.get<ClaudeFilesInventory>(`${BASE}/claude-files`),

  /** Borrado puntual gobernado. Admin: `compliance:erasure:admin`. La razón es opcional y
   *  viaja en el cuerpo; el motor la audita. Un no-2xx NO es un error de transporte: trae el
   *  resultado de dominio, que el llamante lee de `ApiError.body`. */
  eraseClaudeFile: (id: string, reason?: string) =>
    http.post<ClaudeFileEraseResult>(
      `${BASE}/claude-files/${encodeURIComponent(id)}/erase`,
      reason ? { reason } : {},
    ),

  erasures: (status?: ErasureStatus) =>
    http.get<ListResponse<ErasureRequest>>(`${BASE}/erasure`, {
      query: { status },
    }),
  erasure: (id: string) =>
    http.get<ErasureRequest>(`${BASE}/erasure/${encodeURIComponent(id)}`),
  erasureEvents: (id: string) =>
    http.get<ListResponse<ErasureEvent>>(
      `${BASE}/erasure/${encodeURIComponent(id)}/events`,
    ),
  erasureReceipt: (id: string) =>
    http.get<ErasureReceipt>(
      `${BASE}/erasure/${encodeURIComponent(id)}/receipt`,
    ),
  createErasure: (body: CreateErasureInput) =>
    http.postWithMeta<ErasureRequest>(`${BASE}/erasure`, body),
  /** IRREVERSIBLE once both gates clear. A 202 means the erasure did NOT finish —
   *  see `incompleteReason` for the two distinct ways that happens. */
  executeErasure: (id: string, body: ExecuteErasureInput) =>
    http.postWithMeta<ExecuteErasureResult | Record<string, unknown>>(
      `${BASE}/erasure/${encodeURIComponent(id)}/execute`,
      body,
    ),
  eraseDataSubject: (id: string, body: DataSubjectEraseInput) =>
    http.postWithMeta<ExecuteErasureResult | Record<string, unknown>>(
      `${BASE}/data-subjects/${encodeURIComponent(id)}/erase`,
      body,
    ),
  dataSubjectErasureStatus: (id: string, subjectKind?: string) =>
    http.get<DataSubjectErasureStatus>(
      `${BASE}/data-subjects/${encodeURIComponent(id)}/erasure-status`,
      { query: { subject_kind: subjectKind } },
    ),

  // (12) DORA register + incidents, OSCAL profiles (E3).
  // NOTE: every GENERATOR below (POST) answers 501 unless the enterprise add-on
  // is linked (regpackage.go:256, doraincident.go:93, oscalprofile.go:245). The
  // reads are open-core and work against whatever the add-on persisted.
  doraExport: () => http.get<Record<string, unknown>>(`${BASE}/dora`),
  doraRegisters: () =>
    http.get<ListResponse<DoraRegister>>(`${BASE}/dora/register`),
  doraRegister: (id: string) =>
    http.get<DoraRegister>(`${BASE}/dora/register/${encodeURIComponent(id)}`),
  exportDoraRegister: (id: string) =>
    fetchRawExport(
      `${BASE}/dora/register/${encodeURIComponent(id)}/export`,
      `dora-register-${id}.json`,
    ),
  /** Generate the Register of Information from the operator's register document.
   *
   *  RAW BYTES + a query param, not a JSON body: the handler reads the document
   *  with `readBoundedBody` (regpackage.go:261) and takes the reference date from
   *  `?reference_date` (`:265`) — a date sent inside the document would never
   *  reach `RegisterInput.ReferenceDate`. See `postDocument` for why the shape
   *  matters more than it looks: `doc_sha256` is the hash of these exact bytes
   *  (`:268`), and the register is the artefact a regulator is handed.
   *
   *  201 on success. 501 without the add-on (`:258`), 422 on a document the
   *  packager rejects, 403 when the add-on is present but unlicensed. */
  generateDoraRegister: (document: string, referenceDate?: string) =>
    postDocument<DoraRegister>(`${BASE}/dora/register`, document, {
      reference_date: referenceDate?.trim() || undefined,
    }),
  /** 204, bodyless (regpackage.go:476) — see `confirmedRemoval` for why the
   *  status is the evidence here and the sibling's body helper is not. */
  deleteDoraRegister: (id: string) =>
    http.deleteWithMeta<void>(
      `${BASE}/dora/register/${encodeURIComponent(id)}`,
    ),
  doraIncidents: () =>
    http.get<ListResponse<DoraIncident>>(`${BASE}/dora/incidents`),
  doraIncident: (id: string) =>
    http.get<DoraIncident>(`${BASE}/dora/incidents/${encodeURIComponent(id)}`),
  exportIncidentReport: (id: string) =>
    fetchRawExport(
      `${BASE}/dora/incidents/${encodeURIComponent(id)}/report`,
      `dora-incident-${id}.json`,
    ),
  /** Classify an ICT incident against the DORA Art. 19 criteria.
   *
   *  Same shape as the NIS 2 sibling and for the same measured reason (see
   *  `postDocument`): the impact document is the BODY as raw bytes
   *  (doraincident.go:109, hashed at `:115`), while `reference` — REQUIRED,
   *  `:97-101` — and the optional `finding_id` (`:108`) are query params. A
   *  reference sent in the body is simply absent: 400 "reference is required".
   *
   *  Re-classifying the same reference UPDATES the row rather than duplicating it
   *  (`:154-162`), which is the engine's own answer to a repeat. */
  classifyIncident: (reference: string, impact: string, findingId?: string) =>
    postDocument<DoraIncident>(`${BASE}/dora/incidents`, impact, {
      reference,
      finding_id: findingId?.trim() || undefined,
    }),
  /** 204, bodyless (doraincident.go:321). */
  deleteIncident: (id: string) =>
    http.deleteWithMeta<void>(
      `${BASE}/dora/incidents/${encodeURIComponent(id)}`,
    ),

  // (12b) NIS 2 significant-incident classification — six routes the engine
  // has served since compliance.go:547-552 and no console reached. The reads are
  // open-core and work against whatever the add-on persisted; only the CLASSIFY
  // answers 501 without it (nis2incident.go:121-126).
  // GET /hipaa/gap-report — el informe TÉCNICO de 45 CFR §164.312, que no es el framework
  // `hipaa_clinical_ai` del catálogo genérico. Ver el comentario del tipo en types.ts.
  hipaaGapReport: () => http.get<HipaaGapReport>(`${BASE}/hipaa/gap-report`),

  nis2Incidents: () =>
    http.get<ListResponse<Nis2Incident>>(`${BASE}/nis2/incidents`),
  nis2Incident: (id: string) =>
    http.get<Nis2Incident>(`${BASE}/nis2/incidents/${encodeURIComponent(id)}`),
  /** POST /nis2/incidents/classify — the write, and its shape is not this
   *  console's usual one, in two ways that both matter.
   *
   *  IDENTIFIERS ARE QUERY PARAMS. `reference` is required and `finding_id` is
   *  optional, and the handler reads BOTH from the URL (nis2incident.go:127,136),
   *  not from the body. A reference sent in the body is simply absent: 400
   *  "reference is required".
   *
   *  THE BODY IS THE OPERATOR'S DOCUMENT, VERBATIM. It is read with
   *  readBoundedBody (oscalprofile.go:475) and hashed byte-for-byte into the
   *  minimal-data anchor (`doc_sha256`, nis2incident.go:143), then handed to the
   *  add-on to parse. So it travels as `rawBody`, never as `body`: `body` is
   *  JSON.stringify-ed (client.ts:139) and a STRING argument comes back out
   *  re-quoted — `{"a":1}` posted as `"{\"a\":1}"`. That is a different document
   *  under a different hash, and the anchor would attest bytes the operator never
   *  wrote.
   *
   *  No Idempotency-Key and no expected_version: this route requires neither
   *  (contrast the tool-pinning writes, toolpins.go:102,106,116). Re-classifying
   *  the SAME reference UPDATES the row rather than duplicating it
   *  (nis2incident.go:186-194), which is the engine's own answer to a repeat. */
  classifyNis2Incident: (
    reference: string,
    impact: string,
    findingId?: string,
  ) =>
    http.postWithMeta<Nis2Incident>(
      `${BASE}/nis2/incidents/classify`,
      undefined,
      {
        rawBody: impact,
        contentType: 'application/json',
        query: { reference, finding_id: findingId || undefined },
      },
    ),
  /** PUT /nis2/incidents/{id} — phase advance and/or note. The body carries ONLY
   *  those two keys: the engine decodes with DisallowUnknownFields
   *  (helpers.go:97-116), so echoing the DTO back is a 400, and a backward or
   *  same-phase move is a 409 (nis2incident.go:295). */
  updateNis2Incident: (id: string, body: Nis2UpdateInput) =>
    http.put<Nis2Incident>(
      `${BASE}/nis2/incidents/${encodeURIComponent(id)}`,
      body,
    ),
  /** The auditor's artefact: the classification plus its LIVE ledger anchor
   *  (nis2incident.go:341-345), as the server's exact bytes. A GET that mutates —
   *  it self-audits `compliance.nis2.incident.export` in the caller's transaction
   *  (`:346`), so it is never issued speculatively. */
  exportNis2Incident: (id: string) =>
    fetchRawExport(
      `${BASE}/nis2/incidents/${encodeURIComponent(id)}/export`,
      `nis2-incident-${id}.json`,
    ),
  /** Deleting a classification removes governed evidence. `deleteWithMeta`
   *  because the engine's answer is a BODY (`{"deleted":true}`,
   *  nis2incident.go:403) and a 2xx alone is not evidence it happened — the same
   *  allowlist rule the holds/erasure writes follow. */
  deleteNis2Incident: (id: string) =>
    http.deleteWithMeta<{ deleted?: unknown }>(
      `${BASE}/nis2/incidents/${encodeURIComponent(id)}`,
    ),
  oscalProfiles: (framework?: string) =>
    http.get<ListResponse<OscalProfile>>(`${BASE}/oscal/profiles`, {
      query: { framework },
    }),
  oscalProfile: (id: string) =>
    http.get<OscalProfile>(`${BASE}/oscal/profiles/${encodeURIComponent(id)}`),
  /** Ingest an OSCAL profile or SSP. The document is the BODY as raw bytes
   *  (oscalprofile.go:249, hashed at `:277`); the framework HINT and the scope
   *  note are query params (`:253`, `:254`). Both were unreachable before: sent
   *  inside the body they never reach `ProfileInput.Framework`, and the resolver
   *  would have to guess the framework it was asked to resolve against.
   *
   *  201 on success. 422 when the document resolves to no controls or to an
   *  unknown framework (`:260`, `:267`, `:273`) — deny-closed, never partially
   *  applied. */
  registerOscalProfile: (
    document: string,
    opts?: { framework?: string; scopeNote?: string },
  ) =>
    postDocument<OscalProfile>(`${BASE}/oscal/profiles`, document, {
      framework: opts?.framework?.trim() || undefined,
      scope_note: opts?.scopeNote?.trim() || undefined,
    }),
  /** 204, bodyless (oscalprofile.go:415). */
  deleteOscalProfile: (id: string) =>
    http.deleteWithMeta<void>(
      `${BASE}/oscal/profiles/${encodeURIComponent(id)}`,
    ),

  // (13) compliance-depth packs (E4) — same open-core split: reads work, the
  // generators answer 501 without the add-on (depthhandlers.go:241,520,795,975).
  usLawPacks: () => http.get<ListResponse<DepthPack>>(`${BASE}/depth/us-law`),
  usLawPack: (id: string) =>
    http.get<DepthPack>(`${BASE}/depth/us-law/${encodeURIComponent(id)}`),
  exportUsLawPack: (id: string) =>
    fetchRawExport(
      `${BASE}/depth/us-law/${encodeURIComponent(id)}/export`,
      `us-law-pack-${id}.json`,
    ),
  /** The jurisdiction-context document is the BODY as raw bytes
   *  (depthhandlers.go:251, hashed at `:262`); the scope note is `?scope_note`
   *  (`:256`). The pack is built against the LIVE assessment of the four US
   *  state-law frameworks (`:38-40`), so what it contains depends on the estate
   *  at generation time, not only on this document. */
  generateUsLawPack: (document: string, scopeNote?: string) =>
    postDocument<DepthPack>(`${BASE}/depth/us-law`, document, {
      scope_note: scopeNote?.trim() || undefined,
    }),
  /** 204, bodyless (depthhandlers.go:503). */
  deleteUsLawPack: (id: string) =>
    http.deleteWithMeta<void>(`${BASE}/depth/us-law/${encodeURIComponent(id)}`),
  sectorPacks: () => http.get<ListResponse<DepthPack>>(`${BASE}/depth/sector`),
  sectorPack: (id: string) =>
    http.get<DepthPack>(`${BASE}/depth/sector/${encodeURIComponent(id)}`),
  exportSectorPack: (id: string) =>
    fetchRawExport(
      `${BASE}/depth/sector/${encodeURIComponent(id)}/export`,
      `sector-pack-${id}.json`,
    ),
  /** Same shape as the US pack, over the three sector-overlay frameworks
   *  (depthhandlers.go:44-46): document as raw bytes (`:530`, hashed `:541`),
   *  scope note as `?scope_note` (`:535`). */
  generateSectorPack: (document: string, scopeNote?: string) =>
    postDocument<DepthPack>(`${BASE}/depth/sector`, document, {
      scope_note: scopeNote?.trim() || undefined,
    }),
  /** 204, bodyless (depthhandlers.go:779). */
  deleteSectorPack: (id: string) =>
    http.deleteWithMeta<void>(`${BASE}/depth/sector/${encodeURIComponent(id)}`),
  // (13b) Las otras DOS familias de profundidad que el motor sirve y la consola no llamaba.
  // Medido contra el binario: `GET /depth/fedramp` y `GET /aims/pack` contestan 200 con
  // `{items:[]}`, y sus POST contestan 501 nombrando add-ons DISTINTOS —`compliancedepth` para
  // FedRAMP, `iso42001` para AIMS—, así que no comparten puerta aunque compartan panel.
  fedrampPacks: () =>
    http.get<ListResponse<FedrampKsiPack>>(`${BASE}/depth/fedramp`),
  fedrampPack: (id: string) =>
    http.get<FedrampKsiPack>(`${BASE}/depth/fedramp/${encodeURIComponent(id)}`),
  exportFedrampPack: (id: string) =>
    fetchRawExport(
      `${BASE}/depth/fedramp/${encodeURIComponent(id)}/export`,
      `fedramp-ksi-${id}.json`,
    ),
  /** El contexto de autorización es el CUERPO en bytes crudos (depthhandlers.go:1171); el nivel
   *  de impacto va como `?impact_level` (`:1176`) y la nota como `?scope_note` (`:1181`).
   *  ⚠ `impact_level` NO es opcional de hecho aunque lo sea de forma: si llega vacío el motor lo
   *  defaultea a `IL2` (`:1233-1237`) — elige por el operador y no se lo dice. Por eso el diálogo
   *  lo pide explícitamente en vez de dejarlo en blanco. */
  generateFedrampPack: (
    document: string,
    impactLevel: string,
    scopeNote?: string,
  ) =>
    postDocument<FedrampKsiPack>(`${BASE}/depth/fedramp`, document, {
      impact_level: impactLevel.trim() || undefined,
      scope_note: scopeNote?.trim() || undefined,
    }),
  deleteFedrampPack: (id: string) =>
    http.deleteWithMeta<void>(
      `${BASE}/depth/fedramp/${encodeURIComponent(id)}`,
    ),
  /** ⚠ AIMS NO cuelga de `/depth`: su ruta es `/aims/pack` (compliance.go). Una función que
   *  compusiera la ruta desde el `kind` de la familia se equivocaría justo aquí, y por eso cada
   *  familia trae su llamada entera en vez de un prefijo. */
  aimsPacks: () => http.get<ListResponse<AimsPack>>(`${BASE}/aims/pack`),
  aimsPack: (id: string) =>
    http.get<AimsPack>(`${BASE}/aims/pack/${encodeURIComponent(id)}`),
  exportAimsPack: (id: string) =>
    fetchRawExport(
      `${BASE}/aims/pack/${encodeURIComponent(id)}/export`,
      `aims-pack-${id}.json`,
    ),
  /** Documento de alcance como cuerpo crudo; sólo `?scope_note` (aimspack.go:223). */
  generateAimsPack: (document: string, scopeNote?: string) =>
    postDocument<AimsPack>(`${BASE}/aims/pack`, document, {
      scope_note: scopeNote?.trim() || undefined,
    }),
  deleteAimsPack: (id: string) =>
    http.deleteWithMeta<void>(`${BASE}/aims/pack/${encodeURIComponent(id)}`),
  ccmSnapshots: () =>
    http.get<ListResponse<CcmSnapshot>>(`${BASE}/depth/ccm/snapshots`),
  ccmSnapshot: (id: string) =>
    http.get<CcmSnapshot>(
      `${BASE}/depth/ccm/snapshots/${encodeURIComponent(id)}`,
    ),
  /** THE ONE WRITE ON THIS PLANE WHOSE INPUT REALLY IS A JSON BODY, and it is
   *  typed to exactly the two keys the handler declares
   *  (depthhandlers.go:807-810). It decodes through `decodeJSON`, which sets
   *  `DisallowUnknownFields` (helpers.go:99), so any third key is a 400 rather
   *  than an ignored field — `Record<string, unknown>` invited precisely that.
   *
   *  Omitting the input entirely snapshots EVERY catalog framework (`:821-826`),
   *  which is a different and much larger action than snapshotting a chosen few.
   *  The body is only read when `ContentLength > 0` (`:811`), so `undefined` here
   *  reaches the engine as "all frameworks" — the caller says which it meant. */
  triggerCcmSnapshot: (input?: CcmSnapshotInput) =>
    http.post<CcmSnapshot>(`${BASE}/depth/ccm/snapshot`, input),
  ccmDrift: () =>
    http.get<ListResponse<CcmDriftFinding>>(`${BASE}/depth/ccm/drift`),
  /** Detect drift, optionally against ONE snapshot.
   *
   *  ⛔ THE FILTER IS A QUERY PARAM AND WAS BEING SENT IN THE BODY. The handler
   *  reads `r.URL.Query().Get("snapshot_id")` (depthhandlers.go:984-985) and never
   *  touches the request body; the previous `http.post(path, body)` therefore
   *  produced a request that SUCCEEDS and silently ignores the filter — a 201 with
   *  drift computed against the engine's default pair of snapshots instead of the
   *  one the operator picked. Not a 400, not a network fault: the worst kind of
   *  wrong, because every surface reports it as done.
   *
   *  An unparseable or unknown id is `store.ErrNotFound` (`:999`, `:1003`) → 404,
   *  so a mistyped id is refused rather than silently widened. */
  detectCcmDrift: (snapshotId?: string) =>
    http.post<ListResponse<CcmDriftFinding>>(
      `${BASE}/depth/ccm/drift`,
      undefined,
      { query: { snapshot_id: snapshotId?.trim() || undefined } },
    ),

  // (14) the regulatory calendar (E6) — registered at compliance.go:460, served
  // at calendar.go:600, and until this session reached by no console and no CLI.
  calendar: (framework?: string) =>
    http.get<RegulatoryCalendarResponse>(`${BASE}/calendar`, {
      query: { framework },
    }),
}

/** Why a governed write did NOT finish, or null when it did.
 *
 *  THE STATUS IS THE CONTRACT, NOT THE BODY. A first version of this matched the
 *  body string `pending_approval`, and the sol-max contrast showed what that let
 *  through: the engine has a SECOND 202 — `provider_pending` (erasure.go:1233) —
 *  returned after local targets were already erased and the account leg ran, when
 *  the provider still has deletions outstanding. Matching only the first string
 *  sent that answer down the success branch, so the console said "Erasure
 *  executed and receipt sealed" when no key had been shredded and no receipt
 *  sealed. Any future 202 the engine adds would have failed the same way.
 *
 *  So: every 202 is incomplete, and the body only chooses the WORDING. The two
 *  are genuinely different and must not be collapsed —
 *  `pending_approval` means nothing was destroyed; `provider_pending` means part
 *  of the erasure already happened. */
export type IncompleteReason =
  'pending_approval' | 'provider_pending' | 'unknown'

export function incompleteReason(res: {
  status: number
  data: unknown
}): IncompleteReason | null {
  if (res.status !== 202) return null
  const body = res.data as { status?: unknown } | null
  const kind = typeof body?.status === 'string' ? body.status : ''
  if (kind === 'pending_approval' || kind === 'provider_pending') return kind
  // A 202 whose body we do not recognise is still incomplete. Reporting it as
  // done is the one outcome that is never acceptable here.
  return 'unknown'
}

/** Did a create actually create what it claims?
 *
 *  Same allowlist rule as the governed writes, applied to the two verbs the
 *  earlier rounds did not cover: an id and the expected status, or it is not
 *  confirmed. A 2xx alone is not evidence — a 202 on these routes would mean the
 *  write did not finish. */
export function confirmedCreate(
  res: { status: number; data: unknown },
  wantStatus: string,
): boolean {
  if (res.status !== 201 && res.status !== 200) return false
  const dto = res.data as { id?: unknown; status?: unknown } | null
  if (typeof dto?.id !== 'string' || dto.id.trim() === '') return false
  return dto.status === wantStatus
}

/** Is this failure the open-core boundary rather than a fault?
 *
 *  Detected by STATUS, never by matching the message: 501 is the engine saying
 *  "this capability lives in an add-on that is not linked"
 *  (nis2incident.go:121-126). The message is prose and gets rewritten; the status
 *  is the contract. A console that paints this red teaches operators to distrust
 *  real errors. */
// Se define en `@/lib/api/errors` desde que `reporting` necesita la misma regla; se re-exporta
// aquí para no cambiar a sus importadores.
export { isOpenCoreSeam } from '@/lib/api/errors'

/** Did a delete actually delete?
 *
 *  Same allowlist rule as `confirmedCreate`, applied to the verb that removes
 *  governed evidence: the engine states the outcome in the body
 *  (`{"deleted":true}`, nis2incident.go:403), so the console reports a removal
 *  only when the engine said one happened. Announcing it from the HTTP status
 *  alone would tell an auditor a classification is gone on any 2xx the route
 *  might grow. */
export function confirmedDeleted(res: {
  status: number
  data: unknown
}): boolean {
  // 200 + {"deleted":true} AND NOTHING ELSE. An earlier version also returned true
  // for a bodyless 204, which contradicted the rule stated right above it — the
  // engine's word is the evidence, and a 204 carries no word. The Codex sol max
  // contrast (F7) caught it as an untested status-only escape: this route answers
  // 200 (nis2incident.go:403), so the branch was unreachable today and would have
  // become a silent "it is gone" the moment the route changed.
  if (res.status !== 200) return false
  const body = res.data as { deleted?: unknown } | null
  return body?.deleted === true
}

/** Did a BODYLESS delete actually delete?
 *
 *  ⚠ THIS IS NOT `confirmedDeleted`, AND THE DIFFERENCE IS MEASURED, NOT STYLISTIC.
 *  The NIS 2 route states its outcome in a body (`{"deleted":true}` with 200,
 *  nis2incident.go:403) and `confirmedDeleted` demands exactly that. The five
 *  regulatory-operations deletes answer a bodyless **204** — regpackage.go:476,
 *  doraincident.go:321, oscalprofile.go:415, depthhandlers.go:503 and `:779` —
 *  so `confirmedDeleted` would call every successful one of them unconfirmed.
 *  Reaching for the sibling helper because the verb has the same name is exactly
 *  the copy-without-measuring class that cost this campaign two rounds.
 *
 *  The allowlist still holds, on the only word these routes say: 204 AND NOTHING
 *  ELSE. Each reaches `WriteHeader(StatusNoContent)` only after the whole Mutate
 *  transaction committed — the row was fetched (404 if absent), deleted, and the
 *  audit event written — so a 204 here IS the engine's evidence. A 202 would mean
 *  something entirely different, and `http.delete` resolving on any 2xx would
 *  report it as gone. */
export function confirmedRemoval(res: { status: number }): boolean {
  return res.status === 204
}

/** The approval reference a 202 body carries, when it carries one. */
export function approvalRefOf(res: { data: unknown }): string | undefined {
  const body = res.data as { approval_ref?: unknown } | null
  return typeof body?.approval_ref === 'string' ? body.approval_ref : undefined
}

export const complianceKeys = {
  /** El inventario es del TENANT, no de un sujeto: su clave no lleva más que el tenant. */
  claudeFiles: (t: string | null) => ['compliance', t, 'claude-files'] as const,
  all: (tenant: string | null) => ['compliance', tenant] as const,
  summary: (tenant: string | null) =>
    ['compliance', tenant, 'summary'] as const,
  capabilities: (tenant: string | null) =>
    ['compliance', tenant, 'capabilities'] as const,
  frameworks: (tenant: string | null) =>
    ['compliance', tenant, 'frameworks'] as const,
  status: (tenant: string | null, id: string) =>
    ['compliance', tenant, 'status', id] as const,
  gaps: (tenant: string | null, id: string) =>
    ['compliance', tenant, 'gaps', id] as const,
  /** El informe HIPAA técnico no se indexa por framework: es su propia ruta, no una vista de
   *  `/gaps/{id}`. Colgarlo del prefijo de `gaps` lo invalidaría con el framework equivocado. */
  hipaaGapReport: (tenant: string | null) =>
    ['compliance', tenant, 'hipaa-gap-report'] as const,
  evidence: (tenant: string | null, framework?: string) =>
    ['compliance', tenant, 'evidence', framework ?? null] as const,
  evidenceExport: (tenant: string | null, id: string, format: string) =>
    ['compliance', tenant, 'evidence', 'export', id, format] as const,
  risk: (tenant: string | null) => ['compliance', tenant, 'risk'] as const,
  residency: (tenant: string | null) =>
    ['compliance', tenant, 'residency'] as const,
  dataClasses: (tenant: string | null) =>
    ['compliance', tenant, 'data-classes'] as const,
  // retention — the PREFIX first, for the same reason `holdsAll`
  // exists: a sweep changes both the schedules (nothing) and the certificates
  // (everything), and a delete/put changes the schedules and the sweep PREVIEW
  // built from them. Invalidate the prefix so no panel keeps serving a stale
  // answer about what is scheduled to be destroyed.
  retentionAll: (tenant: string | null) =>
    ['compliance', tenant, 'retention'] as const,
  retentionPolicies: (tenant: string | null) =>
    ['compliance', tenant, 'retention', 'policies'] as const,
  retentionRuns: (tenant: string | null, dataClass?: string) =>
    ['compliance', tenant, 'retention', 'runs', dataClass ?? null] as const,
  // holds (E1)
  /** The PREFIX every holds query shares. Invalidate this, not `holds()`, after a
   *  create or a release: `holds()` ends in the status slot (`null` by default),
   *  so it does not prefix-match `holdCheck`'s `'check'` — invalidating it would
   *  refresh the list and leave the scope preview serving a cached answer about
   *  preservation that just changed. */
  holdsAll: (tenant: string | null) => ['compliance', tenant, 'holds'] as const,
  holds: (tenant: string | null, status?: string) =>
    ['compliance', tenant, 'holds', status ?? null] as const,
  hold: (tenant: string | null, id: string) =>
    ['compliance', tenant, 'holds', id] as const,
  holdEvents: (tenant: string | null, id: string) =>
    ['compliance', tenant, 'holds', id, 'events'] as const,
  holdCheck: (
    tenant: string | null,
    q: { subject_kind?: string; subject_ref?: string; data_class?: string },
  ) =>
    [
      'compliance',
      tenant,
      'holds',
      'check',
      q.subject_kind ?? null,
      q.subject_ref ?? null,
      q.data_class ?? null,
    ] as const,
  // erasure / DSAR (E2)
  erasures: (tenant: string | null, status?: string) =>
    ['compliance', tenant, 'erasure', status ?? null] as const,
  erasure: (tenant: string | null, id: string) =>
    ['compliance', tenant, 'erasure', id] as const,
  erasureEvents: (tenant: string | null, id: string) =>
    ['compliance', tenant, 'erasure', id, 'events'] as const,
  erasureReceipt: (tenant: string | null, id: string) =>
    ['compliance', tenant, 'erasure', id, 'receipt'] as const,
  dataSubject: (tenant: string | null, id: string, kind?: string) =>
    ['compliance', tenant, 'data-subject', id, kind ?? null] as const,
  // DORA + OSCAL (E3)
  doraRegisters: (tenant: string | null) =>
    ['compliance', tenant, 'dora', 'register'] as const,
  doraRegister: (tenant: string | null, id: string) =>
    ['compliance', tenant, 'dora', 'register', id] as const,
  doraIncidents: (tenant: string | null) =>
    ['compliance', tenant, 'dora', 'incidents'] as const,
  doraIncident: (tenant: string | null, id: string) =>
    ['compliance', tenant, 'dora', 'incidents', id] as const,
  // NIS 2 incidents. `nis2All` is the PREFIX every write invalidates —
  // same reason as `holdsAll`: a classify updates the list AND the row, and a
  // phase advance changes both. Invalidating only the list would leave an open
  // detail panel showing the phase the operator just moved off.
  nis2All: (tenant: string | null) => ['compliance', tenant, 'nis2'] as const,
  nis2Incidents: (tenant: string | null) =>
    ['compliance', tenant, 'nis2', 'incidents'] as const,
  nis2Incident: (tenant: string | null, id: string) =>
    ['compliance', tenant, 'nis2', 'incidents', id] as const,
  oscalProfiles: (tenant: string | null, framework?: string) =>
    ['compliance', tenant, 'oscal', 'profiles', framework ?? null] as const,
  oscalProfile: (tenant: string | null, id: string) =>
    ['compliance', tenant, 'oscal', 'profiles', id] as const,
  // depth packs (E4)
  fedrampPacks: (tenant: string | null) =>
    ['compliance', tenant, 'fedramp-packs'] as const,
  aimsPacks: (tenant: string | null) =>
    ['compliance', tenant, 'aims-packs'] as const,
  usLawPacks: (tenant: string | null) =>
    ['compliance', tenant, 'depth', 'us-law'] as const,
  sectorPacks: (tenant: string | null) =>
    ['compliance', tenant, 'depth', 'sector'] as const,
  ccmSnapshots: (tenant: string | null) =>
    ['compliance', tenant, 'depth', 'ccm', 'snapshots'] as const,
  ccmDrift: (tenant: string | null) =>
    ['compliance', tenant, 'depth', 'ccm', 'drift'] as const,
  // regulatory calendar (E6) — NOT tenant-scoped in the engine (static verified
  // data, calendar.go:598), but keyed with the tenant anyway so switching org
  // does not serve a cached page from another tenant's session.
  calendar: (tenant: string | null, framework?: string) =>
    ['compliance', tenant, 'calendar', framework ?? null] as const,
}
