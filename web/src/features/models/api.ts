// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Models/providers (module X) endpoint wrappers + query keys. The routing policy is
// DEFINED here but EXECUTED by the gateway/connector — the UI configures and
// shows it. Key endpoints govern REFERENCES only; no call ever sends or receives a
// secret value (docs/SECURITY-HARDENING.md).
import {
  http,
  type TenantListOptions,
  type TenantRequestOptions,
} from '@/lib/api/client'
import type { ListResponse } from '@/lib/api/types'
import type {
  AdmissionPolicy,
  AdmissionPolicyInput,
  AdmitInput,
  AdmitResponse,
  AibomSeal,
  AibomSealReceipt,
  CatalogResponse,
  Dataset,
  DatasetInput,
  Decision,
  FinetuneJob,
  FinetuneJobInput,
  GovernedModel,
  GpaiPosture,
  GpaiPostureInput,
  InferenceDeployment,
  InferenceDeploymentInput,
  KeyRef,
  KeyRefInput,
  ModelAccessGrant,
  ModelAccessInput,
  ModelAdmission,
  ModelCardDoc,
  ModelGroup,
  ModelGroupInput,
  ModelVersion,
  ModelVersionInput,
  OwnedModel,
  OwnedModelInput,
  RoutingPolicy,
  RoutingPolicyInput,
} from './types'

const BASE = '/v1/m/models'

/** El techo REAL del repositorio genérico (`maxLimit`, `core/internal/store/sqlstore`). Se pide
 *  entero en las listas de `features/models`, `features/model-ops` y `features/agent-artifacts`
 *  —NO en «todas las de este módulo»: el KPI del panel ejecutivo y la sección de roles de la
 *  consola llaman a `models` y a `model-access`/`model-groups` por su cuenta y siguen sin techo;
 *  van declarados en `sessions-sin-recorte.md`— porque el
 *  defecto que arregla no es de rendimiento: sin `limit` el motor pagina a 100 y la consola tiraba
 *  el `has_more` que sí publica, así que una lista recortada se leía como completa. Pedir mil no
 *  elimina el recorte: lo vuelve DECLARABLE.
 *
 *  Se llama `EVIDENCE_PAGE` porque nació con las superficies de evidencia; el nombre se conserva
 *  para no partir en dos una constante que es UNA —dos copias del mismo techo envejecen aparte—. */
export const EVIDENCE_PAGE = 1000

export const modelsApi = {
  // ── C07-04 · las diez rutas de models que la consola nunca llamaba ─────────────────
  //
  // 67 rutas en `modules/models/api.go` y 57 llamadas. Las diez que faltaban incluyen dos
  // superficies de gobierno con nombre propio: los **derechos por nivel de acceso** y la
  // **residencia por workspace**, que decide DÓNDE se procesan los datos de un workspace.
  //
  // ⚠ Y una asimetría de la misma familia que ya cerré en finops: la consola listaba y creaba
  // políticas de enrutado pero **no podía abrir UNA**, ni ejecutarla.

  /** ADMIN. Ejecuta una política de enrutado — la operación que la pone en marcha. */
  executeRoutingPolicy: (id: string, body?: unknown) =>
    http.post<unknown>(
      `${BASE}/routing-policies/${encodeURIComponent(id)}/execute`,
      body ?? {},
    ),
  /** La lectura de UNA política, que faltaba. */
  routingPolicy: (id: string) =>
    http.get<unknown>(`${BASE}/routing-policies/${encodeURIComponent(id)}`),

  /** Los derechos por nivel de acceso: qué niveles puede usar quién. */
  accessTierEntitlements: () =>
    http.get<unknown>(`${BASE}/access-tier-entitlements`),
  setAccessTierEntitlements: (body: unknown) =>
    http.put<unknown>(`${BASE}/access-tier-entitlements`, body),

  /** La residencia por workspace: DÓNDE se procesan sus datos. Una consola que no la
   *  enseñe deja una decisión de soberanía de datos sólo accesible por `curl`. */
  workspaceResidency: (query?: { limit?: number }) =>
    http.get<unknown>(`${BASE}/workspace-residency`, { query }),
  setWorkspaceResidency: (body: unknown) =>
    http.put<unknown>(`${BASE}/workspace-residency`, body),

  /** Actualizar UNA clave de proveedor. */
  updateKey: (id: string, body: unknown) =>
    http.put<unknown>(`${BASE}/keys/${encodeURIComponent(id)}`, body),

  /** Tres catálogos de sólo lectura que la consola no consultaba. */
  dataGovernance: () => http.get<unknown>(`${BASE}/data-governance`),
  toolTypes: () => http.get<unknown>(`${BASE}/tool-types`),
  features: () => http.get<unknown>(`${BASE}/features`),

  catalog: () => http.get<CatalogResponse>(`${BASE}/catalog`),
  models: ({ tenant, signal, query }: TenantListOptions) =>
    http.get<ListResponse<GovernedModel>>(`${BASE}/models`, {
      tenant,
      signal,
      query: { ...query, limit: EVIDENCE_PAGE },
    }),
  model: (id: string) => http.get<GovernedModel>(`${BASE}/models/${id}`),
  routingPolicies: (query?: { limit?: number }) =>
    http.get<ListResponse<RoutingPolicy>>(`${BASE}/routing-policies`, {
      query,
    }),
  createRoutingPolicy: (body: RoutingPolicyInput) =>
    http.post<RoutingPolicy>(`${BASE}/routing-policies`, body),
  //this was http.patch and the engine registers PUT (modules/models/api.go:95),
  // so every save from this panel got a bare 405 — found by the console-route class
  // check, NOT by any test, because test:web mocks the call. The body is a full
  // replacement (RoutingPolicyInput carries every field), so PUT is the correct verb.
  updateRoutingPolicy: (id: string, body: RoutingPolicyInput) =>
    http.put<RoutingPolicy>(`${BASE}/routing-policies/${id}`, body),
  deleteRoutingPolicy: (id: string) =>
    http.delete<void>(`${BASE}/routing-policies/${id}`),
  resolve: (id: string) =>
    http.post<Decision>(`${BASE}/routing-policies/${id}/resolve`),
  keys: (query?: { limit?: number }) =>
    http.get<ListResponse<KeyRef>>(`${BASE}/keys`, { query }),
  createKey: (body: KeyRefInput) => http.post<KeyRef>(`${BASE}/keys`, body),
  deleteKey: (id: string) => http.delete<void>(`${BASE}/keys/${id}`),
  gpaiPostures: (query?: { provider_ref?: string; limit?: number }) =>
    http.get<ListResponse<GpaiPosture>>(`${BASE}/gpai-posture`, { query }),
  attestGpaiPosture: (body: GpaiPostureInput) =>
    http.put<GpaiPosture>(`${BASE}/gpai-posture`, body),

  // --- Claude model-access governance (consumed by the console) ----
  // model-groups (admin-defined named sets) + model-access grants (who may use what,
  // where, on which surface). Authoring a grant is ADMIN-tier server-side.
  modelGroups: ({ tenant, signal, query }: TenantListOptions) =>
    http.get<ListResponse<ModelGroup>>(`${BASE}/model-groups`, {
      tenant,
      signal,
      query: { ...query, limit: EVIDENCE_PAGE },
    }),
  modelGroup: (id: string) =>
    http.get<ModelGroup>(`${BASE}/model-groups/${id}`),
  createModelGroup: (body: ModelGroupInput) =>
    http.post<ModelGroup>(`${BASE}/model-groups`, body),
  updateModelGroup: (id: string, body: ModelGroupInput) =>
    http.put<ModelGroup>(`${BASE}/model-groups/${id}`, body),
  deleteModelGroup: (id: string) =>
    http.delete<void>(`${BASE}/model-groups/${id}`),
  modelAccess: (
    options: TenantRequestOptions,
    query?: {
      subject_kind?: string
      subject_ref?: string
      target_ref?: string
      limit?: number
    },
  ) =>
    http.get<ListResponse<ModelAccessGrant>>(`${BASE}/model-access`, {
      ...options,
      query,
    }),
  createModelAccess: (body: ModelAccessInput) =>
    http.post<ModelAccessGrant>(`${BASE}/model-access`, body),
  updateModelAccess: (id: string, body: ModelAccessInput) =>
    http.put<ModelAccessGrant>(`${BASE}/model-access/${id}`, body),
  deleteModelAccess: (id: string) =>
    http.delete<void>(`${BASE}/model-access/${id}`),

  // --- model operations (module XXIII) — own-model registry, versions, local
  // inference deployments, and signed-model admission. The write bodies are the
  // dedicated `…Input` command types (never a response spread), so no response-only
  // field can reach the DisallowUnknownFields decoder.
  ownedModels: (query?: { kind?: string; status?: string; limit?: number }) =>
    http.get<ListResponse<OwnedModel>>(`${BASE}/owned-models`, { query }),
  ownedModel: (id: string) =>
    http.get<OwnedModel>(`${BASE}/owned-models/${id}`),
  createOwnedModel: (body: OwnedModelInput) =>
    http.post<OwnedModel>(`${BASE}/owned-models`, body),
  updateOwnedModel: (id: string, body: OwnedModelInput) =>
    http.put<OwnedModel>(`${BASE}/owned-models/${id}`, body),
  deleteOwnedModel: (id: string) =>
    http.delete<void>(`${BASE}/owned-models/${id}`),

  // Versions are immutable: create + delete + the admit verdict. No update route.
  modelVersions: (query?: {
    owned_ref?: string
    status?: string
    limit?: number
  }) =>
    http.get<ListResponse<ModelVersion>>(`${BASE}/model-versions`, { query }),
  createModelVersion: (body: ModelVersionInput) =>
    http.post<ModelVersion>(`${BASE}/model-versions`, body),
  deleteModelVersion: (id: string) =>
    http.delete<void>(`${BASE}/model-versions/${id}`),
  /** POST /model-versions/{id}/admit — a 200 with admitted:false is a recorded deny
   *  verdict (evidence), NOT an error; a malformed bundle is a 400. */
  admitVersion: (versionId: string, body: AdmitInput) =>
    http.post<AdmitResponse>(`${BASE}/model-versions/${versionId}/admit`, body),

  // Inference deployments. Create/update can be refused by the deny-closed admission
  // gate with HTTP 422 (surfaced inline). No server-side owned_ref filter (only
  // runtime/status) — this is a flat cross-model inventory, never nested by model.
  deployments: (query?: {
    runtime?: string
    status?: string
    limit?: number
  }) =>
    http.get<ListResponse<InferenceDeployment>>(
      `${BASE}/inference-deployments`,
      { query },
    ),
  createDeployment: (body: InferenceDeploymentInput) =>
    http.post<InferenceDeployment>(`${BASE}/inference-deployments`, body),
  updateDeployment: (id: string, body: InferenceDeploymentInput) =>
    http.put<InferenceDeployment>(`${BASE}/inference-deployments/${id}`, body),
  deleteDeployment: (id: string) =>
    http.delete<void>(`${BASE}/inference-deployments/${id}`),

  // Signed-model admission. Policy is a tenant singleton; GET returns the
  // saved policy OR an unconfigured stub carrying `configured:false`.
  admissionPolicy: () => http.get<AdmissionPolicy>(`${BASE}/admission-policy`),
  putAdmissionPolicy: (body: AdmissionPolicyInput) =>
    http.put<AdmissionPolicy>(`${BASE}/admission-policy`, body),
  /** GET /model-admissions — el veredicto de admisión, **UNA fila por versión**.
   *
   *  ⛔ NO ES UN HISTORIAL, y este comentario lo decía. `handleAdmitVersion`
   *  (`modules/models/admission.go`) lista por `version_ref` con `Limit: 1` y, si hay fila,
   *  **la ACTUALIZA**; sólo crea cuando no hay ninguna. O sea que re-admitir **sustituye** el
   *  veredicto: no hay intentos anteriores que consultar. `VersionEvidence` toma `items[0]` y
   *  eso es correcto **por esa razón**, no por suerte — la razón queda escrita aquí para que
   *  el día que el motor pase a append-only alguien vea que ese `[0]` deja de valer.
   *
   *  `verified` omitido = todos; true/false es un filtro de verdad. */
  modelAdmissions: (query?: {
    version_ref?: string
    verified?: boolean
    limit?: number
  }) =>
    http.get<ListResponse<ModelAdmission>>(`${BASE}/model-admissions`, {
      query,
    }),

  /** El ledger de precintos de AI-BOM (lectura) — lo usan el cajón por modelo y la pestaña
   *  de evidencia entre modelos.
   *
   *  ⛔ ES APPEND-ONLY Y CRECE: `handleSealAIBOM` hace `repo.Create` en cada precinto y lo
   *  ancla a la cabeza de la cadena de auditoría (`ledger_seq`, `ledger_hash`). Por eso aquí
   *  el `limit` no es adorno: sin él el repositorio genérico pagina a **100** por `id ASC`
   *  —que en UUIDv7 es orden de creación—, así que la consola enseñaría los **primeros** cien
   *  precintos. En un ledger de evidencia, lo que se pierde por ese lado es lo RECIENTE. */
  aibomSeals: (query?: { owned_ref?: string; limit?: number }) =>
    http.get<ListResponse<AibomSeal>>(`${BASE}/aiboms`, { query }),

  // --- slice 2: lineage & evidence ---------------------------------------
  // Datasets are AIBOM lineage components — tenant-wide, create/delete only. The
  // list filters by owned_ref for the per-model lineage view. `verified` is an
  // operator CLAIM, sent on create.
  datasets: (query?: { owned_ref?: string; limit?: number }) =>
    http.get<ListResponse<Dataset>>(`${BASE}/datasets`, { query }),
  createDataset: (body: DatasetInput) =>
    http.post<Dataset>(`${BASE}/datasets`, body),
  deleteDataset: (id: string) => http.delete<void>(`${BASE}/datasets/${id}`),

  // Fine-tune job RECORDS — create + update (status/lineage transitions); no delete.
  // The list filters by status ONLY (the backend offers no owned_ref/runtime filter).
  finetuneJobs: (query?: { status?: string; limit?: number }) =>
    http.get<ListResponse<FinetuneJob>>(`${BASE}/finetune-jobs`, { query }),
  finetuneJob: (id: string) =>
    http.get<FinetuneJob>(`${BASE}/finetune-jobs/${id}`),
  createFinetuneJob: (body: FinetuneJobInput) =>
    http.post<FinetuneJob>(`${BASE}/finetune-jobs`, body),
  updateFinetuneJob: (id: string, body: FinetuneJobInput) =>
    http.put<FinetuneJob>(`${BASE}/finetune-jobs/${id}`, body),

  // AIBOM: GET GENERATES a live CycloneDX 1.6 doc (or ?format=spdx JSON-LD) — read-only,
  // never sealed. POST SEALS it — reads NO request body and returns the seal row plus the
  // CycloneDX document it anchored. The generated doc is opaque JSON (never rebuilt here).
  generateAibom: (ownedId: string, query?: { format?: 'spdx' }) =>
    http.get<unknown>(`${BASE}/owned-models/${ownedId}/aibom`, { query }),
  sealAibom: (ownedId: string) =>
    http.post<AibomSealReceipt>(`${BASE}/owned-models/${ownedId}/aibom`),

  // Model card: the live generated JSON document. The ?format=md Markdown export is a
  // raw-text download (model-ops/export.ts), not this JSON client.
  modelCard: (ownedId: string) =>
    http.get<ModelCardDoc>(`${BASE}/owned-models/${ownedId}/model-card`),
}

/** Los parámetros que entran en una clave de caché: valores de consulta, nunca un id suelto.
 *  ⛔ NO es `unknown`: con `unknown` un llamante puede pasar una cadena por descuido y la clave
 *  «lista con filtros» colisiona con la clave «detalle por id» —mismo número de segmentos, mismo
 *  tipo—. Lo señaló el contraste externo; hoy no hay ningún llamante así y el tipo lo mantiene. */
type KeyParams = Record<string, string | number | boolean | undefined>

export const modelsKeys = {
  all: (tenant: string | null) => ['models', tenant] as const,
  catalog: (tenant: string | null) => ['models', tenant, 'catalog'] as const,
  models: (tenant: string | null, params?: KeyParams) =>
    params === undefined
      ? (['models', tenant, 'estate'] as const)
      : (['models', tenant, 'estate', params] as const),
  /** La residencia por workspace: DÓNDE se procesan los datos. No tenía entrada de fábrica y su
   *  clave se escribía a mano en la vista. */
  workspaceResidency: (tenant: string | null, params?: KeyParams) =>
    params === undefined
      ? (['models', tenant, 'workspace-residency'] as const)
      : (['models', tenant, 'workspace-residency', params] as const),
  routingPolicies: (tenant: string | null, params?: KeyParams) =>
    params === undefined
      ? (['models', tenant, 'routing-policies'] as const)
      : (['models', tenant, 'routing-policies', params] as const),
  resolve: (tenant: string | null, id: string) =>
    ['models', tenant, 'routing-policies', id, 'resolve'] as const,
  keys: (tenant: string | null, params?: KeyParams) =>
    params === undefined
      ? (['models', tenant, 'keys'] as const)
      : (['models', tenant, 'keys', params] as const),
  gpaiPostures: (
    tenant: string | null,
    providerRef?: string,
    params?: KeyParams,
  ) =>
    params === undefined
      ? (['models', tenant, 'gpai-postures', providerRef ?? '*'] as const)
      : ([
          'models',
          tenant,
          'gpai-postures',
          providerRef ?? '*',
          params,
        ] as const),
  modelGroups: (tenant: string | null, params?: KeyParams) =>
    params === undefined
      ? (['models', tenant, 'model-groups'] as const)
      : (['models', tenant, 'model-groups', params] as const),
  modelAccess: (tenant: string | null, params?: KeyParams) =>
    params === undefined
      ? (['models', tenant, 'model-access'] as const)
      : (['models', tenant, 'model-access', params] as const),

  // --- model operations. Tenant-scoped, filter-aware. Model and agent AIBOM
  // seals stay on DISTINCT keys (they are separate ledgers) — never invalidated together.
  ownedModels: (tenant: string | null, filters?: KeyParams) =>
    filters === undefined
      ? (['models', tenant, 'owned-models'] as const)
      : (['models', tenant, 'owned-models', filters] as const),
  ownedModel: (tenant: string | null, id: string) =>
    ['models', tenant, 'owned-models', id] as const,
  modelVersions: (
    tenant: string | null,
    ownedRef?: string,
    params?: KeyParams,
  ) =>
    params === undefined
      ? (['models', tenant, 'model-versions', ownedRef ?? '*'] as const)
      : ([
          'models',
          tenant,
          'model-versions',
          ownedRef ?? '*',
          params,
        ] as const),
  deployments: (tenant: string | null, filters?: KeyParams) =>
    filters === undefined
      ? (['models', tenant, 'inference-deployments'] as const)
      : (['models', tenant, 'inference-deployments', filters] as const),
  admissionPolicy: (tenant: string | null) =>
    ['models', tenant, 'admission-policy'] as const,
  /** ⛔ SIN FILTROS DEVUELVE EL PREFIJO, no `{}`. Un `filters ?? {}` produce una clave
   *  CONCRETA que no prefija a la filtrada, así que invalidar por ella no casaría con nada —
   *  el defecto que `features/invalidation-prefix.test.ts` existe para cazar, y que su regla
   *  no ve porque busca `params ?? null` y aquí el vacío se escribía `?? {}`. */
  modelAdmissions: (tenant: string | null, filters?: KeyParams) =>
    filters === undefined
      ? (['models', tenant, 'model-admissions'] as const)
      : (['models', tenant, 'model-admissions', filters] as const),
  aibomSeals: (tenant: string | null, ownedRef?: string, params?: KeyParams) =>
    params === undefined
      ? (['models', tenant, 'aiboms', ownedRef ?? '*'] as const)
      : (['models', tenant, 'aiboms', ownedRef ?? '*', params] as const),

  // --- slice 2: lineage & evidence. Datasets and fine-tune jobs are their own
  // filter-aware lists; the AIBOM/model-card generates are per-owned-model, fetched on
  // demand (preview) and never confused with the durable seal ledger (`aibomSeals`).
  datasets: (tenant: string | null, ownedRef?: string, params?: KeyParams) =>
    params === undefined
      ? (['models', tenant, 'datasets', ownedRef ?? '*'] as const)
      : (['models', tenant, 'datasets', ownedRef ?? '*', params] as const),
  finetuneJobs: (tenant: string | null, filters?: KeyParams) =>
    filters === undefined
      ? (['models', tenant, 'finetune-jobs'] as const)
      : (['models', tenant, 'finetune-jobs', filters] as const),
  aibomGenerate: (tenant: string | null, ownedId: string, format?: string) =>
    [
      'models',
      tenant,
      'aibom-generate',
      ownedId,
      format ?? 'cyclonedx',
    ] as const,
  modelCard: (tenant: string | null, ownedId: string) =>
    ['models', tenant, 'model-card', ownedId] as const,
}
