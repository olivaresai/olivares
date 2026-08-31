// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Endpoint helpers + query keys for the knowledge module (VIII). Thin wrappers over
// the core HTTP client against /v1/m/knowledge (ARCHITECTURE.md — no logic here). The
// active tenant header is attached automatically; tenant-scoped keys cache-isolate
// per tenant (query.ts contract).
import { apiFetch, http } from '@/lib/api'
import type { TenantListOptions, TenantRequestOptions } from '@/lib/api/client'
// El techo de una lista paginada es UNO en todo el arbol y vive donde nacio: es el maximo real
// del store (`core/internal/store/sqlstore/generic.go`: defaultLimit 100, maxLimit 1000). Definir
// aqui otra constante con el mismo 1000 fabricaria dos copias de un control que envejecen aparte.
import { EVIDENCE_PAGE } from '@/features/models/api'
import { ApiError, NetworkError } from '@/lib/api/errors'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import type { ListResponse } from '@/lib/api/types'
import type {
  AddRevisionResponse,
  ContextPolicyDTO,
  ContextPolicyInput,
  DataContractDTO,
  DataContractInput,
  DataProductDTO,
  DataProductHealthDTO,
  DataProductInput,
  DeletedResponse,
  DocumentDTO,
  DPEventDTO,
  IngestInput,
  IngestResponse,
  KbDTO,
  KbInput,
  LineageDTO,
  MemoryDTO,
  MemoryInput,
  MemoryListResponse,
  PromptDTO,
  PromptInput,
  PurgeResponse,
  QueryInput,
  QueryResponse,
  ReindexResponse,
  RevisionDTO,
  RevisionInput,
  RollbackInput,
  RollbackResponse,
  ValidateResponse,
} from './types'

const BASE = '/v1/m/knowledge'

export interface ListParams {
  cursor?: string
  limit?: number
}

export const knowledgeApi = {
  // --- knowledge bases -------------------------------------------------------
  listKbs: (params?: ListParams & { status?: string }) =>
    http.get<ListResponse<KbDTO>>(`${BASE}/kbs`, {
      query: { ...params, limit: params?.limit ?? EVIDENCE_PAGE },
    }),
  getKb: (id: string) =>
    http.get<KbDTO>(`${BASE}/kbs/${encodeURIComponent(id)}`),
  createKb: (input: KbInput) => http.post<KbDTO>(`${BASE}/kbs`, input),
  updateKb: (id: string, input: KbInput) =>
    http.put<KbDTO>(`${BASE}/kbs/${encodeURIComponent(id)}`, input),
  deleteKb: (id: string) =>
    http.delete<DeletedResponse>(`${BASE}/kbs/${encodeURIComponent(id)}`),
  ingest: (id: string, input: IngestInput) =>
    http.post<IngestResponse>(
      `${BASE}/kbs/${encodeURIComponent(id)}/ingest`,
      input,
    ),
  reindex: (id: string) =>
    http.post<ReindexResponse>(`${BASE}/kbs/${encodeURIComponent(id)}/reindex`),

  // --- documents -------------------------------------------------------------
  listDocuments: (kbId: string, params?: ListParams & { status?: string }) =>
    http.get<ListResponse<DocumentDTO>>(
      `${BASE}/kbs/${encodeURIComponent(kbId)}/documents`,
      { query: { ...params, limit: params?.limit ?? EVIDENCE_PAGE } },
    ),
  getDocument: (id: string) =>
    http.get<DocumentDTO>(`${BASE}/documents/${encodeURIComponent(id)}`),

  // --- governed retrieval (writes a lineage row) -----------------------------
  query: (kbId: string, input: QueryInput) =>
    http.post<QueryResponse>(
      `${BASE}/kbs/${encodeURIComponent(kbId)}/query`,
      input,
    ),

  // --- lineage (self-audited reads) ------------------------------------------
  listLineage: (
    params?: ListParams & {
      kb_id?: string
      agent_ref?: string
      decision?: string
    },
  ) =>
    http.get<ListResponse<LineageDTO>>(`${BASE}/lineage`, {
      query: { ...params, limit: params?.limit ?? EVIDENCE_PAGE },
    }),
  getLineage: (id: string) =>
    http.get<LineageDTO>(`${BASE}/lineage/${encodeURIComponent(id)}`),

  // --- prompt registry -------------------------------------------------------
  listPrompts: (params?: ListParams) =>
    http.get<ListResponse<PromptDTO>>(`${BASE}/prompts`, {
      query: { ...params, limit: params?.limit ?? EVIDENCE_PAGE },
    }),
  getPrompt: (id: string) =>
    http.get<PromptDTO>(`${BASE}/prompts/${encodeURIComponent(id)}`),
  createPrompt: (input: PromptInput) =>
    http.post<PromptDTO>(`${BASE}/prompts`, input),
  listRevisions: (id: string) =>
    http.get<ListResponse<RevisionDTO>>(
      `${BASE}/prompts/${encodeURIComponent(id)}/revisions`,
    ),
  getRevision: (id: string, rev: number) =>
    http.get<RevisionDTO>(
      `${BASE}/prompts/${encodeURIComponent(id)}/revisions/${rev}`,
    ),
  addRevision: (id: string, input: RevisionInput) =>
    http.post<AddRevisionResponse>(
      `${BASE}/prompts/${encodeURIComponent(id)}/revisions`,
      input,
    ),
  rollbackPrompt: (id: string, input: RollbackInput) =>
    http.post<RollbackResponse>(
      `${BASE}/prompts/${encodeURIComponent(id)}/rollback`,
      input,
    ),

  // ── C07-04 · las trece rutas de knowledge que la consola nunca llamaba ─────────────
  //
  // Medido el 2026-08-17: `modules/knowledge/knowledge.go:299-375` registra 53 rutas y el
  // cliente escrito a mano llamaba 40. Las trece que faltaban NO son accesorias: son las
  // superficies de GOBIERNO del módulo — exportar, importar y VERIFICAR la memoria de agente,
  // las reglas DLP, y los escaneos con sus etiquetas.
  //
  // ⚠ Los permisos vuelven a NO ser uniformes, y aquí importa más que en otros sitios:
  // `memory/all` y `memory/verify` son **admin** (`knowledge.go`), no `memory:read`. Leer la
  // memoria de UN agente y leer la de TODOS no son la misma operación, y el motor lo separa.

  /** Importa el paquete JSONL exportado, sin volver a serializarlo como un solo JSON. */
  importMemory: (raw: string) =>
    apiFetch<unknown>(`${BASE}/memory/import`, {
      method: 'POST',
      rawBody: raw,
      contentType: 'application/x-ndjson',
    }),
  /**
   * ADMIN. Toda la memoria del tenant, no la de un agente.
   *
   * ⛔ SIN `limit`, Y LA AUSENCIA ES EL ARREGLO. Esta llamada declaraba
   *    `params?: { limit?: number }` y el handler **no lee `limit`**: `handleListAllMemory`
   *    llama a `listAll`, que recorre `q.Cursor` en BUCLE hasta `!page.HasMore`. Es decir,
   *    DRENA. Un parámetro que el servidor ignora no es inocuo: anuncia un control que no
   *    existe, y quien pasara `limit: 10` recibiría la memoria entera creyendo tener diez.
   *    Cero llamantes de producción hoy (los dos usos son de un test de contrato), así que
   *    se retira antes de que alguien lo crea.
   */
  allMemory: () => http.get<unknown>(`${BASE}/memory/all`),
  /** ADMIN. Verifica la integridad de la memoria — la comprobación que dice si lo
   *  almacenado sigue siendo lo que se escribió. */
  verifyMemory: (body?: unknown) =>
    http.post<unknown>(`${BASE}/memory/verify`, body ?? {}),

  /** Fuerza la sincronización de una base de conocimiento. */
  syncKb: (id: string) =>
    http.post<unknown>(`${BASE}/kbs/${encodeURIComponent(id)}/sync`, {}),
  /** Lanza un escaneo de clasificación sobre una base de conocimiento. */
  scanKb: (id: string) =>
    http.post<unknown>(`${BASE}/kbs/${encodeURIComponent(id)}/scan`, {}),
  /** Lanza un escaneo sobre una fuente por NOMBRE. */
  scanSource: (name: string, options: TenantRequestOptions) =>
    http.post<unknown>(
      `${BASE}/sources/${encodeURIComponent(name)}/scan`,
      {},
      options,
    ),
  /** Las etiquetas de clasificación que los escaneos producen. */
  /**
   * ⛔ TECHO SÍ, AVISO NO — y el silencio es una decisión con cadena de evidencia, no un olvido.
   *
   *    `handleListLabels` pasa por `listQuery`, así que RECORTA a cien sin techo: por eso lleva
   *    techo. Pero **no hay ninguna vista que la llame**, así que no hay lista donde pintar un
   *    aviso. Verificado en tres puntos, y por la auditoría de significado además:
   *      · cero llamantes en `.tsx` (el único uso vive en `governance-contract.test.ts`);
   *      · el router sólo monta `KnowledgeView` (`registry.tsx`);
   *      · `knowledge-view.tsx` no tiene pestaña ni tabla de etiquetas.
   *
   *    Si mañana aparece esa vista, el aviso es obligatorio y el censo lo pedirá: el techo ya
   *    está puesto y `has_more` ya viaja.
   *
   * ⚠ NO CONFUNDIR CON LAS QUE CALLAN PORQUE DRENAN. `/memory` y `/prompts/{id}/revisions` SÍ son
   *   listas visibles, y no llevan aviso porque el motor las agota con `listAll` — no hay
   *   `has_more` que declarar. Son tres silencios de tres clases distintas: sin vista (ésta), sin
   *   recorte (aquéllas) y sin lista pintada (el editor de entradas del catálogo).
   */
  labels: () =>
    http.get<unknown>(`${BASE}/labels`, { query: { limit: EVIDENCE_PAGE } }),
  /** El historial de escaneos: sin esto, un escaneo que falla no se ve. */
  scans: (params: { limit?: number }, options: TenantRequestOptions) =>
    http.get<unknown>(`${BASE}/scans`, {
      ...options,
      query: { ...params, limit: params?.limit ?? EVIDENCE_PAGE },
    }),

  /** Las reglas DLP del tenant.
   *
   * ⛔ PEDIA LA LISTA SIN TECHO, y el motor la pagina. `handleListDLPRules`
   *    (`modules/knowledge/dlp.go:168`) construye su respuesta con `listQuery(r)` y devuelve
   *    `Items` + `Cursor` + `HasMore`: sin `?limit` el store sirve su pagina por omision de CIEN
   *    (`generic.go`, `defaultLimit`). Un tenant con mas de cien reglas DLP veia las cien primeras
   *    en una pantalla de gobierno **sin nada que dijera que faltaban**, que es la clase de recorte
   *    silencioso que esta campana existe para cerrar.
   *
   *    Se RECONSTRUYE la peticion en vez de expandirla, igual que las listas de modelos: asi el
   *    techo gana siempre —un `limit: undefined` del llamante no lo borra— y `anonymous`/`headers`
   *    no llegan al cliente aunque entren por una variable ancha.
   */
  dlpRules: ({ tenant, signal, query }: TenantListOptions) =>
    http.get<unknown>(`${BASE}/dlp/rules`, {
      tenant,
      signal,
      query: { ...query, limit: EVIDENCE_PAGE },
    }),
  /** ADMIN. Fija las reglas DLP. */
  setDlpRules: (body: unknown, options: TenantRequestOptions) =>
    http.put<unknown>(`${BASE}/dlp/rules`, body, options),
  /** ADMIN. Retira UNA regla DLP. */
  deleteDlpRule: (id: string, options: TenantRequestOptions) =>
    http.delete<unknown>(
      `${BASE}/dlp/rules/${encodeURIComponent(id)}`,
      undefined,
      options,
    ),

  /** El contrato ACTIVO de un producto de datos — cuál rige ahora, no el histórico. */
  /** El contrato ACTIVO, PREGUNTADO. El motor responde 200 con el contrato o **404**
   *  (`writeContractBySelector`, `dataproduct.go:897`), asi que «no hay activo» pasa a ser
   *  un hecho del servidor y no una deduccion de la primera pagina de `listContracts`. */
  activeContract: (id: string) =>
    http.get<DataContractDTO>(
      `${BASE}/data-products/${encodeURIComponent(id)}/contracts/active`,
    ),

  // --- agent memory ----------------------------------------------------------
  listMemory: (params?: {
    agent_ref?: string
    user_ref?: string
    session_ref?: string
  }) =>
    http.get<MemoryListResponse>(`${BASE}/memory`, {
      query: { ...params },
    }),
  getMemory: (id: string) =>
    http.get<MemoryDTO>(`${BASE}/memory/${encodeURIComponent(id)}`),
  writeMemory: (input: MemoryInput) =>
    http.post<MemoryDTO>(`${BASE}/memory`, input),
  deleteMemory: (id: string) =>
    http.delete<DeletedResponse>(`${BASE}/memory/${encodeURIComponent(id)}`),
  purgeMemory: (params?: { agent_ref?: string }) =>
    http.post<PurgeResponse>(`${BASE}/memory/purge`, undefined, {
      query: { ...params },
    }),

  // --- context policies ------------------------------------------------------
  listContextPolicies: (
    params?: ListParams & { scope_kind?: string; scope_ref?: string },
  ) =>
    http.get<ListResponse<ContextPolicyDTO>>(`${BASE}/context-policies`, {
      query: { ...params, limit: params?.limit ?? EVIDENCE_PAGE },
    }),
  upsertContextPolicy: (input: ContextPolicyInput) =>
    http.post<ContextPolicyDTO>(`${BASE}/context-policies`, input),

  // --- data products --------------------------------------------------
  listDataProducts: (params?: ListParams & { status?: string }) =>
    http.get<ListResponse<DataProductDTO>>(`${BASE}/data-products`, {
      query: { ...params, limit: params?.limit ?? EVIDENCE_PAGE },
    }),
  getDataProduct: (id: string) =>
    http.get<DataProductDTO>(`${BASE}/data-products/${encodeURIComponent(id)}`),
  createDataProduct: (input: DataProductInput) =>
    http.post<DataProductDTO>(`${BASE}/data-products`, input),
  updateDataProduct: (id: string, input: Partial<DataProductInput>) =>
    http.put<DataProductDTO>(
      `${BASE}/data-products/${encodeURIComponent(id)}`,
      input,
    ),
  deleteDataProduct: (id: string) =>
    http.delete<DeletedResponse>(
      `${BASE}/data-products/${encodeURIComponent(id)}`,
    ),
  publishDataProduct: (id: string) =>
    http.post<DataProductDTO>(
      `${BASE}/data-products/${encodeURIComponent(id)}/publish`,
    ),
  deprecateDataProduct: (id: string) =>
    http.post<DataProductDTO>(
      `${BASE}/data-products/${encodeURIComponent(id)}/deprecate`,
    ),
  archiveDataProduct: (id: string) =>
    http.post<DataProductDTO>(
      `${BASE}/data-products/${encodeURIComponent(id)}/archive`,
    ),
  dataProductHealth: (id: string) =>
    http.get<DataProductHealthDTO>(
      `${BASE}/data-products/${encodeURIComponent(id)}/health`,
    ),
  validateDataProduct: (id: string, payload: unknown) =>
    http.post<ValidateResponse>(
      `${BASE}/data-products/${encodeURIComponent(id)}/validate`,
      payload,
    ),
  listContracts: (productId: string, params?: ListParams) =>
    http.get<ListResponse<DataContractDTO>>(
      `${BASE}/data-products/${encodeURIComponent(productId)}/contracts`,
      { query: { ...params, limit: params?.limit ?? EVIDENCE_PAGE } },
    ),
  createContract: (productId: string, input: DataContractInput) =>
    http.post<DataContractDTO>(
      `${BASE}/data-products/${encodeURIComponent(productId)}/contracts`,
      input,
    ),
  getContract: (productId: string, ver: number) =>
    http.get<DataContractDTO>(
      `${BASE}/data-products/${encodeURIComponent(productId)}/contracts/${ver}`,
    ),
  listDPEvents: (productId: string, params?: ListParams) =>
    http.get<ListResponse<DPEventDTO>>(
      `${BASE}/data-products/${encodeURIComponent(productId)}/events`,
      { query: { ...params, limit: params?.limit ?? EVIDENCE_PAGE } },
    ),
}

/** Tenant-scoped query keys (query.ts contract: tenant id in every key). */
export const knowledgeKeys = {
  all: (t: string | null) => ['knowledge', t] as const,

  kbs: (t: string | null, params?: unknown) =>
    params === undefined
      ? (['knowledge', t, 'kbs'] as const)
      : (['knowledge', t, 'kbs', params] as const),
  kb: (t: string | null, id: string) => ['knowledge', t, 'kb', id] as const,
  documents: (t: string | null, kbId: string, params?: unknown) =>
    params === undefined
      ? (['knowledge', t, 'kb', kbId, 'documents'] as const)
      : (['knowledge', t, 'kb', kbId, 'documents', params] as const),

  lineage: (t: string | null, params?: unknown) =>
    params === undefined
      ? (['knowledge', t, 'lineage'] as const)
      : (['knowledge', t, 'lineage', params] as const),
  lineageOne: (t: string | null, id: string) =>
    ['knowledge', t, 'lineage', id] as const,

  prompts: (t: string | null, params?: unknown) =>
    params === undefined
      ? (['knowledge', t, 'prompts'] as const)
      : (['knowledge', t, 'prompts', params] as const),
  prompt: (t: string | null, id: string) =>
    ['knowledge', t, 'prompt', id] as const,
  revisions: (t: string | null, id: string) =>
    ['knowledge', t, 'prompt', id, 'revisions'] as const,
  revision: (t: string | null, id: string, rev: number) =>
    ['knowledge', t, 'prompt', id, 'revision', rev] as const,

  memory: (t: string | null, params?: unknown) =>
    params === undefined
      ? (['knowledge', t, 'memory'] as const)
      : (['knowledge', t, 'memory', params] as const),

  contextPolicies: (t: string | null, params?: unknown) =>
    params === undefined
      ? (['knowledge', t, 'context-policies'] as const)
      : (['knowledge', t, 'context-policies', params] as const),

  dataProducts: (t: string | null, params?: unknown) =>
    params === undefined
      ? (['knowledge', t, 'data-products'] as const)
      : (['knowledge', t, 'data-products', params] as const),
  dataProduct: (t: string | null, id: string) =>
    ['knowledge', t, 'data-product', id] as const,
  dataProductHealth: (t: string | null, id: string) =>
    ['knowledge', t, 'data-product', id, 'health'] as const,
  dataProductActiveContract: (t: string | null, id: string) =>
    ['knowledge', t, 'dataProduct', id, 'contract', 'active'] as const,
  dataProductContracts: (t: string | null, id: string) =>
    ['knowledge', t, 'data-product', id, 'contracts'] as const,
  dataProductEvents: (t: string | null, id: string) =>
    ['knowledge', t, 'data-product', id, 'events'] as const,

  scans: (t: string | null, params?: unknown) =>
    params === undefined
      ? (['knowledge', t, 'scans'] as const)
      : (['knowledge', t, 'scans', params] as const),
  dlpRules: (t: string | null) => ['knowledge', t, 'dlp', 'rules'] as const,
}

/**
 * ⛔ LA EXPORTACIÓN NO PUEDE IR POR `http.get`, Y POR ESO `exportMemory` SE HA RETIRADO.
 *    `/memory/export` emite **JSONL** —línea 1 el manifiesto firmado, una línea por entrada
 *    (`modules/knowledge/portability.go`)— y el cliente HTTP hace `JSON.parse(text)` con un
 *    `catch` que deja `parsed = undefined` (`lib/api/client.ts:154-164`). Es decir: el método
 *    devolvía **`undefined` en el caso de ÉXITO** y descartaba el paquete entero en silencio.
 *    Nunca se notó porque no tenía ni una pantalla que lo llamara — que es exactamente lo que el
 *    trinquete de llamantes existe para destapar.
 *
 *    Se sigue el patrón ya sancionado en este repo para descargas no-JSON
 *    (`features/finops/api.ts: fetchFocusExport`): `fetch` propio con las cabeceras de sesión y
 *    tenant, y el cuerpo CRUDO de vuelta.
 *
 * ⛔ Y DEVUELVE EL MANIFIESTO APARTE A PROPÓSITO. Trae `count` **y `integrity_excluded`**: el
 *    motor DEJA FUERA las filas que fallan la comprobación de integridad y las cuenta. Un paquete
 *    de portabilidad entregado a un interesado sin decir cuántas filas faltan se presenta como
 *    completo sin serlo.
 */
export type MemoryExportManifest = {
  schema?: string
  tenant?: string
  agent_ref?: string
  count?: number
  integrity_excluded?: number
  entries_sha256?: string
  signature?: string
}

export async function fetchMemoryExport(params?: {
  agent_ref?: string
}): Promise<{ manifest: MemoryExportManifest | null; raw: string }> {
  const search = new URLSearchParams()
  if (params?.agent_ref) search.set('agent_ref', params.agent_ref)

  const headers = new Headers({ Accept: 'application/x-ndjson' })
  const token = useSessionStore.getState().token
  if (token) headers.set('Authorization', `Bearer ${token}`)
  const tenant = useTenantStore.getState().activeTenant
  if (tenant) headers.set('X-Olivares-Tenant', tenant)

  const qs = search.toString()
  let res: Response
  try {
    res = await fetch(`${BASE}/memory/export${qs ? `?${qs}` : ''}`, {
      method: 'GET',
      headers,
      credentials: 'same-origin',
    })
  } catch (cause) {
    throw new NetworkError('The control plane is unreachable.', cause)
  }
  if (!res.ok) {
    // ⛔ El 501 de aquí NO es la costura open-core. `handleExportMemory` falla CERRADO con 501
    //    cuando la clave de firma de portabilidad no está cableada — «it never emits an unsigned
    //    bundle». Leerlo como «tu edición no lo incluye» manda a alguien a comprar un add-on por
    //    una clave que le falta.
    throw new ApiError(
      res.status,
      res.status === 501 ? 'portability_key_unwired' : 'export_failed',
      res.statusText || 'Export failed',
      res.headers.get('X-Request-ID') ?? undefined,
    )
  }
  const raw = await res.text()
  const primera = raw.split('\n', 1)[0] ?? ''
  let manifest: MemoryExportManifest | null = null
  try {
    manifest = primera ? (JSON.parse(primera) as MemoryExportManifest) : null
  } catch {
    // Un manifiesto ilegible NO se convierte en un paquete sin manifiesto: se dice.
    manifest = null
  }
  return { manifest, raw }
}
