// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// DTOs for the knowledge module (VIII /) — mirror the Go DTOs colocated per
// handler in modules/knowledge/{kb,documents,ingest,retrieval,lineage,prompt,memory,
// context}.go 1:1 (snake_case JSON tags). The web is a thin client (ARCHITECTURE.md):
// these are the exact shapes the engine returns at /v1/m/knowledge.
//
// CRITICAL invariants encoded here (minimum-data by construction):
//   - No DTO field carries a secret VALUE. ACLs (default_acl/acl) are permission
//     REFERENCES (group/role/principal handles), rendered as ref chips — never values.
//   - Hash-only fields (query_hash, content_hash, template_hash, egress_provider) are
//     opaque hashes — rendered as hash chips, NEVER expanded to the underlying text.
//   - embed_model='local-hash' is an AUTHORITATIVE degraded state (lexical retrieval,
//     NOT semantic) — surfaced prominently, but it is not an untrusted hint.
//   - Documents expose NO body; chunk text is the already-redacted store content.

import type { SourceMode } from '@/features/shared'

/** A KB's classification level (metadata badge). */
export type Classification =
  'public' | 'internal' | 'confidential' | 'secret' | (string & {})

/** A KB's data-residency region (metadata badge). */
export type Residency = 'global' | 'eu' | 'us' | (string & {})

/** A KB's embedding policy. local_only = zero-egress lock. */
export type EmbedPolicy = 'local_only' | 'model_backed' | 'auto' | (string & {})

/** A KB / document status. */
export type KbStatus = 'active' | 'archived' | (string & {})

/** A document's index status. pending links to reindex. */
export type DocStatus = 'indexed' | 'pending' | (string & {})

/** A document's connector/source kind. */
export type SourceKind =
  | 'gdrive'
  | 'confluence'
  | 'notion'
  | 'sharepoint'
  | 's3'
  | 'inline'
  | (string & {})

/** A prompt's lifecycle status. */
export type PromptStatus = 'active' | 'deprecated' | (string & {})

/** A lineage decision. */
export type LineageDecision = 'allowed' | 'denied' | (string & {})

/** A context policy's scope kind. */
export type ScopeKind = 'agent' | 'kb' | 'tenant' | (string & {})

/** A context policy's compaction strategy. */
export type ContextStrategy =
  'truncate' | 'summarize' | 'window' | (string & {})

export const CLASSIFICATIONS: Classification[] = [
  'public',
  'internal',
  'confidential',
  'secret',
]
export const RESIDENCIES: Residency[] = ['global', 'eu', 'us']
export const EMBED_POLICIES: EmbedPolicy[] = [
  'local_only',
  'model_backed',
  'auto',
]
export const KB_STATUSES: KbStatus[] = ['active', 'archived']
export const SCOPE_KINDS: ScopeKind[] = ['agent', 'kb', 'tenant']
export const CONTEXT_STRATEGIES: ContextStrategy[] = [
  'truncate',
  'summarize',
  'window',
]

/** The local-hash embedder ref — lexical retrieval, NOT semantic vector search. */
export const LOCAL_HASH_MODEL = 'local-hash'

// --- knowledge bases ---------------------------------------------------------

/** A knowledge base (GET/POST/PUT /kbs[/{id}]). */
export interface KbDTO {
  id: string
  name: string
  classification: Classification
  residency_region: Residency
  embed_policy: EmbedPolicy
  /** Model ref of the wired embedder; 'local-hash' = lexical, NON-semantic. */
  embed_model: string
  dim: number
  /** Permission REFERENCES only, never credentials. */
  default_acl: string[]
  status: KbStatus
  doc_count: number
  chunk_count: number
}

/** The write payload for create/update — exactly the fields the backend accepts
 * (it rejects unknown fields). embed_model/dim are recorded at create, not sent. */
export interface KbInput {
  name?: string
  classification?: Classification
  residency_region?: Residency
  embed_policy?: EmbedPolicy
  default_acl?: string[]
  status?: KbStatus
}

// --- documents ---------------------------------------------------------------

/** A KB's ingested document (metadata + provenance only — NO body). */
export interface DocumentDTO {
  id: string
  /** Parent KB id. */
  kb_ref: string
  source_kind: SourceKind
  source_ref: string
  source_mode?: SourceMode
  source_doc_id: string
  title: string
  content_type: string
  classification: Classification
  residency_region: Residency
  /** Permission REFERENCES only (metadata). */
  acl: string[]
  /** Hash of the raw body (metadata; the body itself is NOT exposed). */
  content_hash: string
  /** 'N secrets removed before indexing' (metadata). */
  redaction_count: number
  chunk_count: number
  /** Source space/folder ref (omitempty). */
  space_ref?: string
  status: DocStatus
}

/** One inline document for ingest (no body is ever read back; this is write-only). */
export interface InlineDocInput {
  source_kind?: SourceKind
  source_mode?: SourceMode
  source_doc_id: string
  title?: string
  body: string
  content_type?: string
  /** Permission REFERENCES only — never credentials. */
  acl?: string[]
  classification?: Classification
  space_ref?: string
}

/** The ingest request: from a registered connector NAME or inline documents. */
export interface IngestInput {
  /** Registered connector name (never a credential value). */
  source?: string
  documents?: InlineDocInput[]
}

/** POST /kbs/{id}/ingest. */
export interface IngestResponse {
  documents: number
  chunks: number
  /** Total secrets/PII redacted before indexing (surface this). */
  redactions_total: number
  /** Whether the wired embedder sent text out. */
  egress: boolean
  embed_model: string
}

/** POST /kbs/{id}/reindex. */
export interface ReindexResponse {
  reindexed: number
}

// --- governed retrieval ------------------------------------------------------

/** The query request. */
export interface QueryInput {
  query: string
  /** Default 10, cap 100. */
  top_k?: number
  agent_ref?: string
  session_ref?: string
}

/** One retrieved chunk (already permission-filtered server-side). */
export interface QueryResult {
  chunk_id: string
  document_id: string
  source_kind: SourceKind
  source_ref: string
  source_mode?: SourceMode
  title: string
  /** The REDACTED chunk content (only form that ever existed in store). */
  text: string
  classification: Classification
  score: number
}

/** POST /kbs/{id}/query. */
export interface QueryResponse {
  /** Open its lineage trace. */
  lineage_id: string
  results: QueryResult[]
  count: number
  embed_model: string
  /** false = query stayed in perimeter. */
  egress: boolean
  /**
   * ⛔ LO QUE NO ESTÁ EN `results`, y por qué se declara aquí en vez de dejarse fuera del tipo.
   *
   * `context_truncated` + `context_dropped_chunks` (`modules/knowledge/retrieval.go:69-70`): el
   * contexto NO cabía en el presupuesto y se soltaron trozos. La respuesta se construyó sobre
   * menos evidencia de la que la lista sugiere.
   *
   * `excluded_*` y `redacted_items` (`:72-77`) son el EFECTO de los dos suelos de contexto, y el
   * motor los añadió con estas palabras: «Reporting the flag without reporting its effect is how
   * a control that applies nothing looks identical to one that applies something and finds
   * nothing». De ahí que la pantalla separe TRES estados y no dos.
   */
  context_truncated?: boolean
  context_dropped_chunks?: number
  redaction_required?: boolean
  excluded_sources?: string[]
  excluded_chunks?: number
  redacted_items?: number
}

// --- lineage -----------------------------------------------------------------

/** One origin->answer evidence ref (refs + hashes only, NEVER text). */
export interface ChunkRef {
  chunk_id: string
  kb_ref: string
  doc_ref: string
  source_kind: SourceKind
  source_ref: string
  source_mode?: SourceMode
  /** Hash only, NEVER text. */
  content_hash: string
}

/** A lineage record (GET /lineage[/{id}]). */
export interface LineageDTO {
  id: string
  kb_ref: string
  agent_ref: string
  session_ref?: string
  /** Hash of the query, never the query text (metadata). */
  query_hash: string
  /** Origin->answer evidence, refs + hashes only. */
  chunk_refs: ChunkRef[]
  /** Document ids drawn from. */
  source_refs: string[]
  residency_region: Residency
  decision: LineageDecision
  /** Populated on denied — show it, not a generic error (omitempty). */
  reason?: string
  /** false = data did not leave the perimeter (prominent column). */
  egress: boolean
  /** HASH of provider when egress=true, never the provider value (omitempty). */
  egress_provider?: string
  result_count: number
  occurred_at: string
}

// --- prompt registry ---------------------------------------------------------

/** A prompt (GET/POST /prompts, GET /prompts/{id}). */
export interface PromptDTO {
  id: string
  name: string
  /** Pointer to the active revision. */
  current_rev: number
  /** Hash of the current template (omitempty). */
  latest_hash?: string
  status: PromptStatus
}

/** The create-prompt payload. */
export interface PromptInput {
  name: string
  template: string
  label?: string
  note?: string
}

/** An immutable prompt revision (GET /prompts/{id}/revisions[/{rev}]). */
export interface RevisionDTO {
  prompt_id: string
  rev: number
  label?: string
  /** REDACTED template body (metadata-safe; secrets scrubbed). */
  template: string
  template_hash: string
  note?: string
  created_by: string
}

/** The add-revision payload. */
export interface RevisionInput {
  template: string
  label?: string
  note?: string
}

/** POST /prompts/{id}/revisions. */
export interface AddRevisionResponse {
  prompt_id: string
  rev: number
}

/** The rollback payload. */
export interface RollbackInput {
  rev: number
}

/** POST /prompts/{id}/rollback. */
export interface RollbackResponse {
  prompt_id: string
  current_rev: number
}

// --- agent memory ------------------------------------------------------------

/** A governed agent-memory entry (GET/POST /memory, GET /memory/{id}). */
export interface MemoryDTO {
  id: string
  agent_ref: string
  /** namespace: entry isolated to this user within the tenant+agent (omitempty).*/
  user_ref?: string
  /** namespace: entry isolated to this session (omitempty).*/
  session_ref?: string
  key: string
  /** REDACTED memory content (secrets scrubbed before storage); withheld ("") when integrity === 'failed'. */
  content: string
  /** SHA-256 of the redacted content — the read-path integrity baseline.*/
  content_hash?: string
  classification: Classification
  residency_region: Residency
  /** badge 'expires'; expired entries are never returned (omitempty). */
  expires_at?: string
  created_by: string
  /** 'failed' only in the admin GET /memory/all view for a tamper-detected entry. */
  integrity?: string
}

/** The write-memory (upsert) payload. */
export interface MemoryInput {
  agent_ref: string
  key: string
  content?: string
  classification?: Classification
  residency_region?: Residency
  ttl_seconds?: number
  /**: write into this user's namespace (deny-closed across users).*/
  user_ref?: string
  /**: write into this session's namespace (deny-closed across sessions).*/
  session_ref?: string
}

/** GET /memory & GET /memory/all envelope: list + the integrity counter.*/
export interface MemoryListResponse {
  items: MemoryDTO[]
  cursor?: string
  has_more: boolean
  /**
   * Entries that failed the read-path integrity check. On GET /memory
   * they are WITHHELD from items; on GET /memory/all they are listed with
   * integrity='failed' and content withheld — the counter counts failures.
   */
  integrity_excluded?: number
}

/** DELETE /memory/{id} & DELETE /kbs/{id}. */
export interface DeletedResponse {
  deleted: boolean
}

/** POST /memory/purge. */
export interface PurgeResponse {
  purged: number
  /** Expired rows preserved under an active legal hold.*/
  excluded_held?: number
}

// --- context policies --------------------------------------------------------

/** A context/compaction policy (GET/POST /context-policies). */
export interface ContextPolicyDTO {
  id: string
  scope_kind: ScopeKind
  scope_ref: string
  max_tokens: number
  strategy: ContextStrategy
  redaction_required: boolean
  /** Free-form JSON policy spec (omitempty). */
  spec?: Record<string, unknown>
}

/** The upsert-policy payload. */
export interface ContextPolicyInput {
  scope_kind: ScopeKind
  scope_ref: string
  max_tokens?: number
  strategy?: ContextStrategy
  redaction_required?: boolean
  spec?: Record<string, unknown>
}

// --- data products ---------------------------------------------------

/** A data product's lifecycle status. */
export type DataProductStatus =
  'draft' | 'published' | 'deprecated' | 'archived' | (string & {})
export const DATA_PRODUCT_STATUSES: DataProductStatus[] = [
  'draft',
  'published',
  'deprecated',
  'archived',
]

/** Enforcement mode for quality gates. */
export type EnforcementMode = 'enforce' | 'warn' | 'observe' | (string & {})
export const ENFORCEMENT_MODES: EnforcementMode[] = [
  'enforce',
  'warn',
  'observe',
]

/** Contract validation mode. */
export type ValidationMode = 'strict' | 'lenient' | 'none' | (string & {})
export const VALIDATION_MODES: ValidationMode[] = ['strict', 'lenient', 'none']

/** A governed data product (GET/POST/PUT /data-products[/{id}]). */
export interface DataProductDTO {
  id: string
  name: string
  description?: string
  owner_ref: string
  status: DataProductStatus
  kb_ref?: string
  tags?: Record<string, string>
  freshness_sla_seconds: number
  availability_target?: string
  quality_score: number
  usage_count: number
  enforcement_mode: EnforcementMode
  last_ingest_at?: string
  last_health_at?: string
}

/** The write payload for create/update. */
export interface DataProductInput {
  name: string
  description?: string
  owner_ref: string
  kb_ref?: string
  tags?: Record<string, string>
  freshness_sla_seconds?: number
  availability_target?: string
  enforcement_mode?: EnforcementMode
}

/** A data contract version (GET/POST /data-products/{id}/contracts[/{ver}]). */
export interface DataContractDTO {
  id: string
  product_ref: string
  version: number
  schema_definition?: Record<string, unknown>
  validation_mode: ValidationMode
  completeness_threshold: number
  freshness_override_seconds: number
  status: 'active' | 'superseded'
  created_by: string
  note?: string
}

/** The create-contract payload. */
export interface DataContractInput {
  schema_definition?: Record<string, unknown>
  validation_mode?: ValidationMode
  completeness_threshold?: number
  freshness_override_seconds?: number
  note?: string
}

/** An enforcement event for a data product. */
export interface DPEventDTO {
  id: string
  product_ref: string
  contract_ref?: string
  event_type: string
  severity: string
  subject_kind?: string
  subject_ref?: string
  details?: Record<string, unknown>
  occurred_at: string
}

/** Health probe response for a data product. */
export interface DataProductHealthDTO {
  product_id: string
  freshness: {
    status: 'fresh' | 'stale' | 'unknown'
    age_seconds: number
    sla_seconds: number
  }
  quality: {
    score: number
    threshold: number
    status: 'passing' | 'failing' | 'unconfigured'
  }
  usage: {
    total: number
  }
  contract?: {
    version: number
    validation_mode: string
    violations_last_30d: number
  }
  kb?: {
    id: string
    name: string
    doc_count: number
    chunk_count: number
  }
  overall_health: 'healthy' | 'degraded' | 'unhealthy'
  checked_at: string
}

/** POST /data-products/{id}/validate response. */
export interface ValidateResponse {
  valid: boolean
  errors?: string[]
}
